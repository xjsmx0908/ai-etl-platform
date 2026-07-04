package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	defaultMaxSteps = 8
	defaultLockTTL  = 30 * time.Second
)

// Planner decides the next Agent action from the durable run state.
type Planner interface {
	Plan(ctx context.Context, run Run) (PlanDecision, error)
}

// Orchestrator coordinates planner decisions, tool execution, persistence, and locking.
type Orchestrator struct {
	store      Store
	locks      LockManager
	registry   *Registry
	planner    Planner
	authorizer Authorizer
	nodeID     string
	maxSteps   int
	lockTTL    time.Duration
	now        func() time.Time
}

// Options configures an Orchestrator.
type Options struct {
	NodeID     string
	MaxSteps   int
	LockTTL    time.Duration
	Authorizer Authorizer
}

// NewOrchestrator creates a stateful Agent orchestrator.
func NewOrchestrator(store Store, locks LockManager, registry *Registry, planner Planner, opts Options) (*Orchestrator, error) {
	if store == nil {
		return nil, fmt.Errorf("agent store is required")
	}
	if locks == nil {
		return nil, fmt.Errorf("agent lock manager is required")
	}
	if registry == nil {
		return nil, fmt.Errorf("agent tool registry is required")
	}
	if planner == nil {
		return nil, fmt.Errorf("agent planner is required")
	}
	if opts.NodeID == "" {
		opts.NodeID = "agent-node"
	}
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = defaultMaxSteps
	}
	if opts.LockTTL <= 0 {
		opts.LockTTL = defaultLockTTL
	}
	if opts.Authorizer == nil {
		opts.Authorizer = StaticAuthorizer{}
	}
	return &Orchestrator{
		store:      store,
		locks:      locks,
		registry:   registry,
		planner:    planner,
		authorizer: opts.Authorizer,
		nodeID:     opts.NodeID,
		maxSteps:   opts.MaxSteps,
		lockTTL:    opts.LockTTL,
		now:        time.Now,
	}, nil
}

// Start creates a new durable Agent run.
func (o *Orchestrator) Start(ctx context.Context, actor Actor, task string) (Run, error) {
	task = strings.TrimSpace(task)
	if task == "" {
		return Run{}, fmt.Errorf("agent task is required")
	}
	if err := o.authorizer.Authorize(ctx, actor, ToolDefinition{Name: "agent_run"}); err != nil {
		return Run{}, err
	}

	now := o.now().UTC()
	run := Run{
		ID:        newRunID(),
		TenantID:  actor.TenantID,
		UserID:    actor.UserID,
		Version:   1,
		Task:      task,
		State:     StateCreated,
		MaxSteps:  o.maxSteps,
		Memory:    map[string]interface{}{},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := o.store.CreateRun(ctx, run); err != nil {
		return Run{}, err
	}
	return run, nil
}

// ExecuteNext executes at most one planner decision for a run.
func (o *Orchestrator) ExecuteNext(ctx context.Context, runID string, actor Actor) (Run, error) {
	lease, err := o.locks.Acquire(ctx, runID, o.nodeID, o.lockTTL)
	if err != nil {
		return Run{}, err
	}
	defer o.locks.Release(context.Background(), lease)

	run, err := o.store.LoadRun(ctx, runID)
	if err != nil {
		return Run{}, err
	}
	if run.TenantID != actor.TenantID {
		return Run{}, fmt.Errorf("run %q belongs to a different tenant", runID)
	}
	if terminalState(run.State) {
		return run, nil
	}
	run, err = o.claimRun(ctx, run, lease)
	if err != nil {
		return Run{}, err
	}
	if run.State == StateWaitingTool || run.State == StatePendingApproval {
		return o.recoverWaitingTool(ctx, run, actor, lease)
	}
	if len(run.Steps) >= run.MaxSteps {
		return o.failRun(ctx, run, lease, "max_steps_exceeded")
	}

	if run.State == StateCreated {
		run.State = StateRunning
		run.UpdatedAt = o.now().UTC()
		run, err = o.saveRun(ctx, run, lease)
		if err != nil {
			return Run{}, err
		}
	}

	decision, err := o.planner.Plan(ctx, cloneRun(run))
	if err != nil {
		return o.failRun(ctx, run, lease, err.Error())
	}

	switch decision.Type {
	case DecisionFinal:
		return o.completeRun(ctx, run, lease, decision)
	case DecisionToolCall:
		return o.executeToolDecision(ctx, run, actor, lease, decision)
	default:
		return o.failRun(ctx, run, lease, fmt.Sprintf("unsupported planner decision %q", decision.Type))
	}
}

// RunToCompletion executes a run until it reaches a terminal state.
func (o *Orchestrator) RunToCompletion(ctx context.Context, runID string, actor Actor) (Run, error) {
	var run Run
	for {
		next, err := o.ExecuteNext(ctx, runID, actor)
		if err != nil {
			return Run{}, err
		}
		run = next
		if terminalState(run.State) || run.State == StatePendingApproval {
			return run, nil
		}
	}
}

func (o *Orchestrator) executeToolDecision(ctx context.Context, run Run, actor Actor, lease LockLease, decision PlanDecision) (Run, error) {
	start := o.now().UTC()
	stepIndex := len(run.Steps) + 1
	step := Step{
		Index:          stepIndex,
		Type:           StepToolCall,
		State:          StateWaitingTool,
		Thought:        decision.Thought,
		ToolName:       normalizeToolName(decision.ToolName),
		ToolArguments:  append([]byte(nil), decision.Arguments...),
		IdempotencyKey: idempotencyKey(run.ID, stepIndex, decision.ToolName),
		StartedAt:      start,
	}
	run.State = StateWaitingTool
	run.Steps = append(run.Steps, step)
	run.UpdatedAt = start
	var err error
	run, err = o.saveRun(ctx, run, lease)
	if err != nil {
		return Run{}, err
	}

	return o.executePersistedTool(ctx, run, actor, lease, len(run.Steps)-1)
}

func (o *Orchestrator) recoverWaitingTool(ctx context.Context, run Run, actor Actor, lease LockLease) (Run, error) {
	if len(run.Steps) == 0 {
		return o.failRun(ctx, run, lease, "waiting_tool_without_step")
	}
	last := len(run.Steps) - 1
	step := run.Steps[last]
	if step.Type != StepToolCall {
		return o.failRun(ctx, run, lease, "waiting_tool_without_tool_call_step")
	}
	if step.ToolResult != nil {
		run.State = StateRunning
		run.Steps[last].State = StateCompleted
		run.UpdatedAt = o.now().UTC()
		return o.saveRun(ctx, run, lease)
	}
	return o.executePersistedTool(ctx, run, actor, lease, last)
}

func (o *Orchestrator) executePersistedTool(ctx context.Context, run Run, actor Actor, lease LockLease, stepPos int) (Run, error) {
	step := &run.Steps[stepPos]
	result, err := o.registry.execute(ctx, actor, run.ID, step.Index, step.ToolName, step.ToolArguments, o.authorizer)
	completedAt := o.now().UTC()
	current := &run.Steps[stepPos]
	current.CompletedAt = completedAt
	current.Duration = completedAt.Sub(current.StartedAt)
	if err != nil {
		var approvalErr ApprovalRequiredError
		if errors.As(err, &approvalErr) {
			current.State = StatePendingApproval
			current.Error = err.Error()
			run.State = StatePendingApproval
			run.Error = ""
			run.UpdatedAt = completedAt
			return o.saveRun(ctx, run, lease)
		}
		if result.CompensationRequired {
			compensation, compErr := o.registry.compensate(ctx, actor, run.ID, current.Index, current.ToolName, current.ToolArguments, result, o.authorizer)
			if compErr != nil {
				current.CompensationError = compErr.Error()
			} else {
				current.CompensationResult = &compensation
				current.Compensated = true
			}
		}
		current.Type = StepError
		current.State = StateFailed
		current.Error = err.Error()
		if result.Content != "" || len(result.Data) > 0 || result.CompensationRequired {
			current.ToolResult = &result
		}
		run.State = StateFailed
		run.Error = err.Error()
		run.UpdatedAt = completedAt
		return o.saveRun(ctx, run, lease)
	}

	current.ToolResult = &result
	current.Observation = result.Content
	current.State = StateCompleted
	run.State = StateRunning
	run.UpdatedAt = completedAt
	return o.saveRun(ctx, run, lease)
}

func (o *Orchestrator) completeRun(ctx context.Context, run Run, lease LockLease, decision PlanDecision) (Run, error) {
	now := o.now().UTC()
	run.Steps = append(run.Steps, Step{
		Index:       len(run.Steps) + 1,
		Type:        StepFinal,
		State:       StateCompleted,
		Thought:     decision.Thought,
		Observation: decision.Final,
		StartedAt:   now,
		CompletedAt: now,
	})
	run.State = StateCompleted
	run.Final = decision.Final
	run.UpdatedAt = now
	return o.saveRun(ctx, run, lease)
}

func (o *Orchestrator) failRun(ctx context.Context, run Run, lease LockLease, reason string) (Run, error) {
	now := o.now().UTC()
	run.Steps = append(run.Steps, Step{
		Index:       len(run.Steps) + 1,
		Type:        StepError,
		State:       StateFailed,
		Error:       reason,
		StartedAt:   now,
		CompletedAt: now,
	})
	run.State = StateFailed
	run.Error = reason
	run.UpdatedAt = now
	return o.saveRun(ctx, run, lease)
}

func (o *Orchestrator) claimRun(ctx context.Context, run Run, lease LockLease) (Run, error) {
	if lease.FencingToken <= run.FencingToken {
		return run, nil
	}
	run.UpdatedAt = o.now().UTC()
	return o.saveRun(ctx, run, lease)
}

func (o *Orchestrator) saveRun(ctx context.Context, run Run, lease LockLease) (Run, error) {
	return o.store.SaveRun(ctx, run, SaveOptions{
		ExpectedVersion: run.Version,
		FencingToken:    lease.FencingToken,
	})
}

func newRunID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("run-%d", time.Now().UnixNano())
	}
	return "run-" + hex.EncodeToString(b[:])
}

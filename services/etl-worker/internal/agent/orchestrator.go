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
	defaultMaxSteps        = 8
	defaultLockTTL         = 30 * time.Second
	defaultRunTimeout      = 30 * time.Minute
	defaultApprovalTimeout = 15 * time.Minute
)

// Planner decides the next Agent action from the durable run state.
type Planner interface {
	Plan(ctx context.Context, run Run) (PlanDecision, error)
}

// Orchestrator coordinates planner decisions, tool execution, persistence, and locking.
type Orchestrator struct {
	store           Store
	locks           LockManager
	registry        *Registry
	planner         Planner
	authorizer      Authorizer
	nodeID          string
	maxSteps        int
	lockTTL         time.Duration
	runTimeout      time.Duration
	approvalTimeout time.Duration
	maxTokenBudget  int64
	now             func() time.Time
}

// Options configures an Orchestrator.
type Options struct {
	NodeID          string
	MaxSteps        int
	LockTTL         time.Duration
	RunTimeout      time.Duration
	ApprovalTimeout time.Duration
	MaxTokenBudget  int64
	Authorizer      Authorizer
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
	if opts.RunTimeout <= 0 {
		opts.RunTimeout = defaultRunTimeout
	}
	if opts.ApprovalTimeout <= 0 {
		opts.ApprovalTimeout = defaultApprovalTimeout
	}
	if opts.Authorizer == nil {
		opts.Authorizer = StaticAuthorizer{}
	}
	return &Orchestrator{
		store:           store,
		locks:           locks,
		registry:        registry,
		planner:         planner,
		authorizer:      opts.Authorizer,
		nodeID:          opts.NodeID,
		maxSteps:        opts.MaxSteps,
		lockTTL:         opts.LockTTL,
		runTimeout:      opts.RunTimeout,
		approvalTimeout: opts.ApprovalTimeout,
		maxTokenBudget:  opts.MaxTokenBudget,
		now:             time.Now,
	}, nil
}

// Start creates a new durable Agent run.
func (o *Orchestrator) Start(ctx context.Context, actor Actor, task string) (Run, error) {
	return o.StartOrResume(ctx, actor, newRunID(), task, nil)
}

// StartOrResume creates a durably identified run or returns the matching
// existing run after a retry, process restart, or lock handoff.
func (o *Orchestrator) StartOrResume(ctx context.Context, actor Actor, runID, task string, memory map[string]interface{}) (Run, error) {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return Run{}, fmt.Errorf("agent run id is required")
	}
	task = strings.TrimSpace(task)
	if task == "" {
		return Run{}, fmt.Errorf("agent task is required")
	}
	if err := o.authorizer.Authorize(ctx, actor, ToolDefinition{Name: "agent_run"}); err != nil {
		return Run{}, err
	}

	now := o.now().UTC()
	run := Run{
		ID:             runID,
		TenantID:       actor.TenantID,
		UserID:         actor.UserID,
		Version:        1,
		Task:           task,
		State:          StateCreated,
		MaxSteps:       o.maxSteps,
		MaxTokenBudget: o.maxTokenBudget,
		Memory:         cloneMap(memory),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := o.store.CreateRun(ctx, run); err != nil {
		existing, loadErr := o.store.LoadRun(ctx, runID)
		if loadErr != nil {
			return Run{}, err
		}
		if existing.TenantID != actor.TenantID || existing.UserID != actor.UserID || existing.Task != task {
			return Run{}, fmt.Errorf("run %q binding conflict", runID)
		}
		return existing, nil
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
	run, expired, err := o.expireRunIfNeeded(ctx, run, lease)
	if err != nil {
		return Run{}, err
	}
	if expired {
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
	if run.MaxTokenBudget > 0 && run.TokensUsed >= run.MaxTokenBudget {
		return o.failRun(ctx, run, lease, "token_budget_exceeded")
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
		run.TokensUsed += decision.Usage.Total()
		reason := err.Error()
		if run.MaxTokenBudget > 0 && run.TokensUsed > run.MaxTokenBudget {
			reason = "token_budget_exceeded"
		}
		return o.failRunWithUsage(ctx, run, lease, reason, decision.Usage)
	}
	run.TokensUsed += decision.Usage.Total()
	if run.MaxTokenBudget > 0 && run.TokensUsed > run.MaxTokenBudget {
		return o.failRunWithUsage(ctx, run, lease, "token_budget_exceeded", decision.Usage)
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

func (o *Orchestrator) ApplyLifecycle(ctx context.Context, runID string, actor Actor) (Run, error) {
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
	run, _, err = o.expireRunIfNeeded(ctx, run, lease)
	if err != nil {
		return Run{}, err
	}
	return run, nil
}

func (o *Orchestrator) Cancel(ctx context.Context, runID string, actor Actor, reason string) (Run, error) {
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
	if run.UserID != actor.UserID && !hasString(actor.Permissions, "agent:approve") {
		return Run{}, fmt.Errorf("missing cancel permission for run %q", runID)
	}
	if terminalState(run.State) {
		return Run{}, fmt.Errorf("run %q is already terminal", runID)
	}

	now := o.now().UTC()
	reason = strings.TrimSpace(reason)
	message := "run cancelled"
	if reason != "" {
		message += ": " + reason
	}
	run.State = StateCancelled
	run.Error = message
	run.CancelledBy = actor.UserID
	run.CancelReason = reason
	run.CancelledAt = now
	run.UpdatedAt = now
	if idx := activeStepIndex(run); idx >= 0 {
		run.Steps[idx].State = StateCancelled
		run.Steps[idx].Error = message
		run.Steps[idx].CompletedAt = now
		run.Steps[idx].Duration = now.Sub(run.Steps[idx].StartedAt)
	}
	run = o.compensateCompletedSideEffects(ctx, run, Actor{TenantID: run.TenantID, UserID: run.UserID})
	return o.saveRun(ctx, run, lease)
}

func (o *Orchestrator) RejectApproval(ctx context.Context, runID string, actor Actor, reason string) (Run, error) {
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
	if run.State != StatePendingApproval {
		return Run{}, fmt.Errorf("run %q is not pending approval", runID)
	}
	last := pendingApprovalStepIndex(run)
	if last < 0 {
		return o.failRun(ctx, run, lease, "pending_approval_without_tool_call_step")
	}
	now := o.now().UTC()
	message := "approval rejected"
	reason = strings.TrimSpace(reason)
	if reason != "" {
		message += ": " + reason
	}
	run.Steps[last].State = StateFailed
	run.Steps[last].Error = message
	run.Steps[last].CompletedAt = now
	run.Steps[last].Duration = now.Sub(run.Steps[last].StartedAt)
	run.State = StateFailed
	run.Error = message
	run.UpdatedAt = now
	run = o.compensateCompletedSideEffects(ctx, run, Actor{TenantID: run.TenantID, UserID: run.UserID})
	return o.saveRun(ctx, run, lease)
}

func (o *Orchestrator) expireRunIfNeeded(ctx context.Context, run Run, lease LockLease) (Run, bool, error) {
	now := o.now().UTC()
	if o.runTimeout > 0 && !run.CreatedAt.IsZero() && !now.Before(run.CreatedAt.Add(o.runTimeout)) {
		run = o.failActiveRun(run, now, "run_timeout_exceeded")
		run = o.compensateCompletedSideEffects(ctx, run, Actor{TenantID: run.TenantID, UserID: run.UserID})
		saved, err := o.saveRun(ctx, run, lease)
		return saved, true, err
	}
	if run.State == StatePendingApproval && o.approvalTimeout > 0 {
		idx := pendingApprovalStepIndex(run)
		if idx >= 0 && !run.Steps[idx].StartedAt.IsZero() && !now.Before(run.Steps[idx].StartedAt.Add(o.approvalTimeout)) {
			run = o.failActiveRun(run, now, "approval_timeout_exceeded")
			run = o.compensateCompletedSideEffects(ctx, run, Actor{TenantID: run.TenantID, UserID: run.UserID})
			saved, err := o.saveRun(ctx, run, lease)
			return saved, true, err
		}
	}
	return run, false, nil
}

func (o *Orchestrator) failActiveRun(run Run, now time.Time, reason string) Run {
	if idx := activeStepIndex(run); idx >= 0 {
		run.Steps[idx].State = StateFailed
		run.Steps[idx].Error = reason
		run.Steps[idx].CompletedAt = now
		run.Steps[idx].Duration = now.Sub(run.Steps[idx].StartedAt)
	}
	run.State = StateFailed
	run.Error = reason
	run.UpdatedAt = now
	return run
}

func (o *Orchestrator) executeToolDecision(ctx context.Context, run Run, actor Actor, lease LockLease, decision PlanDecision) (Run, error) {
	start := o.now().UTC()
	stepIndex := len(run.Steps) + 1
	step := Step{
		Index:          stepIndex,
		Type:           StepToolCall,
		State:          StateWaitingTool,
		Thought:        decision.Thought,
		PlannerUsage:   decision.Usage,
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

func pendingApprovalStepIndex(run Run) int {
	for i := len(run.Steps) - 1; i >= 0; i-- {
		if run.Steps[i].Type == StepToolCall && run.Steps[i].State == StatePendingApproval {
			return i
		}
	}
	return -1
}

func activeStepIndex(run Run) int {
	for i := len(run.Steps) - 1; i >= 0; i-- {
		switch run.Steps[i].State {
		case StateCreated, StateRunning, StateWaitingTool, StatePendingApproval:
			return i
		}
	}
	return -1
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
		run = o.compensateCompletedSideEffects(ctx, run, actor)
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
		Index:        len(run.Steps) + 1,
		Type:         StepFinal,
		State:        StateCompleted,
		Thought:      decision.Thought,
		PlannerUsage: decision.Usage,
		Observation:  decision.Final,
		StartedAt:    now,
		CompletedAt:  now,
	})
	run.State = StateCompleted
	run.Final = decision.Final
	run.UpdatedAt = now
	return o.saveRun(ctx, run, lease)
}

func (o *Orchestrator) failRun(ctx context.Context, run Run, lease LockLease, reason string) (Run, error) {
	return o.failRunWithUsage(ctx, run, lease, reason, PlanUsage{})
}

func (o *Orchestrator) failRunWithUsage(ctx context.Context, run Run, lease LockLease, reason string, usage PlanUsage) (Run, error) {
	now := o.now().UTC()
	run.Steps = append(run.Steps, Step{
		Index:        len(run.Steps) + 1,
		Type:         StepError,
		State:        StateFailed,
		Error:        reason,
		PlannerUsage: usage,
		StartedAt:    now,
		CompletedAt:  now,
	})
	run.State = StateFailed
	run.Error = reason
	run.UpdatedAt = now
	run = o.compensateCompletedSideEffects(ctx, run, Actor{TenantID: run.TenantID, UserID: run.UserID})
	return o.saveRun(ctx, run, lease)
}

func (o *Orchestrator) compensateCompletedSideEffects(ctx context.Context, run Run, actor Actor) Run {
	if o.registry == nil {
		return run
	}
	for i := len(run.Steps) - 1; i >= 0; i-- {
		step := &run.Steps[i]
		if step.Type != StepToolCall || step.State != StateCompleted || step.Compensated || !o.registry.HasCompensation(step.ToolName) {
			continue
		}
		prior := ToolResult{}
		if step.ToolResult != nil {
			prior = *step.ToolResult
		}
		compensation, err := o.registry.compensate(ctx, actor, run.ID, step.Index, step.ToolName, step.ToolArguments, prior, nil)
		if err != nil {
			step.CompensationError = err.Error()
			continue
		}
		step.CompensationResult = &compensation
		step.Compensated = true
	}
	return run
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

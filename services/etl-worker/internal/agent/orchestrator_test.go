package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type sequencePlanner struct {
	decisions []PlanDecision
}

func (p *sequencePlanner) Plan(_ context.Context, run Run) (PlanDecision, error) {
	if len(run.Steps) >= len(p.decisions) {
		return PlanDecision{Type: DecisionFinal, Final: "done"}, nil
	}
	return p.decisions[len(run.Steps)], nil
}

type loopingPlanner struct{}

func (loopingPlanner) Plan(context.Context, Run) (PlanDecision, error) {
	return PlanDecision{
		Type:      DecisionToolCall,
		Thought:   "try another tool call",
		ToolName:  "check_inventory",
		Arguments: json.RawMessage(`{"sku":"sku-1"}`),
	}, nil
}

func TestOrchestrator_RunToCompletionPersistsSteps(t *testing.T) {
	store := NewMemoryStore()
	registry := testRegistry(t)
	planner := &sequencePlanner{decisions: []PlanDecision{
		{
			Type:      DecisionToolCall,
			Thought:   "need inventory",
			ToolName:  "check_inventory",
			Arguments: json.RawMessage(`{"sku":"sku-1"}`),
		},
		{
			Type:    DecisionFinal,
			Thought: "inventory checked",
			Final:   "sku-1 has stock",
		},
	}}
	orchestrator := newTestOrchestrator(t, store, registry, planner, 4, "node-a")
	actor := Actor{TenantID: "tenant-a", UserID: "user-a", Permissions: []string{"inventory:read"}}

	run, err := orchestrator.Start(context.Background(), actor, "check stock")
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	run, err = orchestrator.RunToCompletion(context.Background(), run.ID, actor)
	if err != nil {
		t.Fatalf("run to completion: %v", err)
	}
	if run.State != StateCompleted {
		t.Fatalf("expected completed run, got %s", run.State)
	}
	if run.Final != "sku-1 has stock" {
		t.Fatalf("unexpected final result: %q", run.Final)
	}
	if len(run.Steps) != 2 {
		t.Fatalf("expected 2 steps, got %d", len(run.Steps))
	}
	if run.Steps[0].State != StateCompleted {
		t.Fatalf("expected completed tool step, got %+v", run.Steps[0])
	}
	if run.Steps[0].ToolResult == nil || run.Steps[0].ToolResult.Content != "stock available" {
		t.Fatalf("expected persisted tool result, got %+v", run.Steps[0])
	}

	stored, err := store.LoadRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("load stored run: %v", err)
	}
	if stored.State != StateCompleted || len(stored.Steps) != 2 {
		t.Fatalf("stored run was not persisted correctly: %+v", stored)
	}
}

func TestOrchestrator_ResumesRunFromPersistedState(t *testing.T) {
	store := NewMemoryStore()
	locks := NewMemoryLockManager()
	registry := testRegistry(t)
	planner := &sequencePlanner{decisions: []PlanDecision{
		{
			Type:      DecisionToolCall,
			Thought:   "need inventory",
			ToolName:  "check_inventory",
			Arguments: json.RawMessage(`{"sku":"sku-1"}`),
		},
		{
			Type:    DecisionFinal,
			Thought: "resume from observation",
			Final:   "resumed and completed",
		},
	}}
	actor := Actor{TenantID: "tenant-a", UserID: "user-a", Permissions: []string{"inventory:read"}}
	first := newTestOrchestratorWithLocks(t, store, locks, registry, planner, 4, "node-a")
	run, err := first.Start(context.Background(), actor, "check stock")
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	run, err = first.ExecuteNext(context.Background(), run.ID, actor)
	if err != nil {
		t.Fatalf("execute first step: %v", err)
	}
	if run.State != StateRunning || len(run.Steps) != 1 {
		t.Fatalf("expected resumable running state, got %+v", run)
	}
	if run.Steps[0].State != StateCompleted {
		t.Fatalf("expected completed tool step before resume, got %+v", run.Steps[0])
	}

	second := newTestOrchestratorWithLocks(t, store, locks, registry, planner, 4, "node-b")
	run, err = second.RunToCompletion(context.Background(), run.ID, actor)
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	if run.State != StateCompleted || run.Final != "resumed and completed" {
		t.Fatalf("expected resumed completion, got %+v", run)
	}
}

func TestOrchestrator_MaxStepsFailsRun(t *testing.T) {
	store := NewMemoryStore()
	registry := testRegistry(t)
	orchestrator := newTestOrchestrator(t, store, registry, loopingPlanner{}, 2, "node-a")
	actor := Actor{TenantID: "tenant-a", UserID: "user-a", Permissions: []string{"inventory:read"}}

	run, err := orchestrator.Start(context.Background(), actor, "loop forever")
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	run, err = orchestrator.RunToCompletion(context.Background(), run.ID, actor)
	if err != nil {
		t.Fatalf("run to completion: %v", err)
	}
	if run.State != StateFailed {
		t.Fatalf("expected failed run, got %s", run.State)
	}
	if !strings.Contains(run.Error, "max_steps_exceeded") {
		t.Fatalf("expected max steps error, got %q", run.Error)
	}
}

func TestOrchestrator_WaitsForApprovalThenResumesSameToolStep(t *testing.T) {
	store := NewMemoryStore()
	registry := NewRegistry()
	calls := 0
	err := registry.Register(ToolDefinition{
		Name:                "refund_order",
		RequiredPermissions: []string{"order:refund"},
		RequiresApproval:    true,
		SideEffect:          true,
		Idempotent:          true,
		Parameters: JSONSchema{
			Type:     "object",
			Required: []string{"order_id"},
			Properties: map[string]SchemaProperty{
				"order_id": {Type: "string"},
			},
		},
	}, func(context.Context, ToolInvocation) (ToolResult, error) {
		calls++
		return ToolResult{Content: "refund accepted"}, nil
	})
	if err != nil {
		t.Fatalf("register refund tool: %v", err)
	}
	planner := &sequencePlanner{decisions: []PlanDecision{
		{Type: DecisionToolCall, ToolName: "refund_order", Arguments: json.RawMessage(`{"order_id":"ord-1"}`)},
		{Type: DecisionFinal, Final: "refund completed"},
	}}
	orchestrator := newTestOrchestrator(t, store, registry, planner, 4, "node-a")
	actor := Actor{TenantID: "tenant-a", UserID: "user-a", Permissions: []string{"order:refund"}}

	run, err := orchestrator.Start(context.Background(), actor, "refund order")
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	run, err = orchestrator.ExecuteNext(context.Background(), run.ID, actor)
	if err != nil {
		t.Fatalf("execute pending approval step: %v", err)
	}
	if run.State != StatePendingApproval {
		t.Fatalf("expected pending approval, got %+v", run)
	}
	if calls != 0 {
		t.Fatalf("tool should not execute before approval, got calls=%d", calls)
	}

	approved := actor
	approved.ApprovedTools = []string{"refund_order"}
	run, err = orchestrator.RunToCompletion(context.Background(), run.ID, approved)
	if err != nil {
		t.Fatalf("resume approved run: %v", err)
	}
	if run.State != StateCompleted || run.Final != "refund completed" {
		t.Fatalf("expected completed approved run, got %+v", run)
	}
	if calls != 1 {
		t.Fatalf("expected one tool execution after approval, got %d", calls)
	}
}

func TestOrchestrator_RejectApprovalFailsPendingRun(t *testing.T) {
	store := NewMemoryStore()
	registry := NewRegistry()
	calls := 0
	err := registry.Register(ToolDefinition{
		Name:                "refund_order",
		RequiredPermissions: []string{"order:refund"},
		RequiresApproval:    true,
		SideEffect:          true,
		Idempotent:          true,
		Parameters: JSONSchema{
			Type:     "object",
			Required: []string{"order_id"},
			Properties: map[string]SchemaProperty{
				"order_id": {Type: "string"},
			},
		},
	}, func(context.Context, ToolInvocation) (ToolResult, error) {
		calls++
		return ToolResult{Content: "refund accepted"}, nil
	})
	if err != nil {
		t.Fatalf("register refund tool: %v", err)
	}
	planner := &sequencePlanner{decisions: []PlanDecision{
		{Type: DecisionToolCall, ToolName: "refund_order", Arguments: json.RawMessage(`{"order_id":"ord-1"}`)},
		{Type: DecisionFinal, Final: "refund completed"},
	}}
	orchestrator := newTestOrchestrator(t, store, registry, planner, 4, "node-a")
	actor := Actor{TenantID: "tenant-a", UserID: "user-a", Permissions: []string{"order:refund"}}

	run, err := orchestrator.Start(context.Background(), actor, "refund order")
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	run, err = orchestrator.ExecuteNext(context.Background(), run.ID, actor)
	if err != nil {
		t.Fatalf("execute pending approval step: %v", err)
	}
	if run.State != StatePendingApproval {
		t.Fatalf("expected pending approval, got %+v", run)
	}

	run, err = orchestrator.RejectApproval(context.Background(), run.ID, actor, "risk too high")
	if err != nil {
		t.Fatalf("reject approval: %v", err)
	}
	if run.State != StateFailed || !strings.Contains(run.Error, "risk too high") {
		t.Fatalf("expected rejected failed run, got %+v", run)
	}
	if len(run.Steps) != 1 || run.Steps[0].State != StateFailed || !strings.Contains(run.Steps[0].Error, "approval rejected") {
		t.Fatalf("expected failed pending step, got %+v", run.Steps)
	}
	if calls != 0 {
		t.Fatalf("tool should not execute after rejection, got calls=%d", calls)
	}

	stored, err := store.LoadRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("load stored run: %v", err)
	}
	if stored.State != StateFailed {
		t.Fatalf("expected stored failed run, got %+v", stored)
	}
}

func TestOrchestrator_RecoversWaitingToolWithSameIdempotencyKey(t *testing.T) {
	store := NewMemoryStore()
	registry := NewRegistry()
	var gotKey string
	err := registry.Register(ToolDefinition{
		Name:                "check_inventory",
		RequiredPermissions: []string{"inventory:read"},
		Idempotent:          true,
		Parameters: JSONSchema{
			Type:     "object",
			Required: []string{"sku"},
			Properties: map[string]SchemaProperty{
				"sku": {Type: "string"},
			},
		},
	}, func(_ context.Context, inv ToolInvocation) (ToolResult, error) {
		gotKey = inv.IdempotencyKey
		return ToolResult{Content: "stock available"}, nil
	})
	if err != nil {
		t.Fatalf("register tool: %v", err)
	}

	now := time.Now().UTC()
	run := Run{
		ID:        "run-recover",
		TenantID:  "tenant-a",
		UserID:    "user-a",
		Version:   1,
		State:     StateWaitingTool,
		MaxSteps:  4,
		CreatedAt: now,
		UpdatedAt: now,
		Steps: []Step{
			{
				Index:          1,
				Type:           StepToolCall,
				State:          StateWaitingTool,
				ToolName:       "check_inventory",
				ToolArguments:  json.RawMessage(`{"sku":"sku-1"}`),
				IdempotencyKey: "agent:run-recover:1:check_inventory",
				StartedAt:      now,
			},
		},
	}
	if err := store.CreateRun(context.Background(), run); err != nil {
		t.Fatalf("create recovering run: %v", err)
	}
	orchestrator := newTestOrchestrator(t, store, registry, &sequencePlanner{}, 4, "node-b")
	actor := Actor{TenantID: "tenant-a", UserID: "user-a", Permissions: []string{"inventory:read"}}

	run, err = orchestrator.ExecuteNext(context.Background(), run.ID, actor)
	if err != nil {
		t.Fatalf("recover waiting tool: %v", err)
	}
	if run.State != StateRunning {
		t.Fatalf("expected running after recovered tool, got %+v", run)
	}
	if gotKey != "agent:run-recover:1:check_inventory" {
		t.Fatalf("expected stable idempotency key, got %q", gotKey)
	}
	if run.Steps[0].State != StateCompleted {
		t.Fatalf("expected recovered tool step to complete, got %+v", run.Steps[0])
	}
}

func TestOrchestrator_CompensatesFailedSideEffect(t *testing.T) {
	store := NewMemoryStore()
	registry := NewRegistry()
	compensated := false
	err := registry.RegisterWithCompensation(ToolDefinition{
		Name:                "refund_order",
		RequiredPermissions: []string{"order:refund"},
		RequiresApproval:    true,
		SideEffect:          true,
		Idempotent:          false,
		Parameters: JSONSchema{
			Type:     "object",
			Required: []string{"order_id"},
			Properties: map[string]SchemaProperty{
				"order_id": {Type: "string"},
			},
		},
	}, func(context.Context, ToolInvocation) (ToolResult, error) {
		return ToolResult{Content: "refund reserved", CompensationRequired: true}, errors.New("settlement failed")
	}, func(context.Context, ToolInvocation, ToolResult) (ToolResult, error) {
		compensated = true
		return ToolResult{Content: "refund reservation voided"}, nil
	})
	if err != nil {
		t.Fatalf("register compensating tool: %v", err)
	}
	planner := &sequencePlanner{decisions: []PlanDecision{
		{Type: DecisionToolCall, ToolName: "refund_order", Arguments: json.RawMessage(`{"order_id":"ord-1"}`)},
	}}
	orchestrator := newTestOrchestrator(t, store, registry, planner, 4, "node-a")
	actor := Actor{
		TenantID:      "tenant-a",
		UserID:        "user-a",
		Permissions:   []string{"order:refund"},
		ApprovedTools: []string{"refund_order"},
	}

	run, err := orchestrator.Start(context.Background(), actor, "refund order")
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	run, err = orchestrator.ExecuteNext(context.Background(), run.ID, actor)
	if err != nil {
		t.Fatalf("execute refund step: %v", err)
	}
	if run.State != StateFailed {
		t.Fatalf("expected failed run, got %+v", run)
	}
	if !compensated || !run.Steps[0].Compensated {
		t.Fatalf("expected compensation recorded, got compensated=%v step=%+v", compensated, run.Steps[0])
	}
	if run.Steps[0].CompensationResult == nil || run.Steps[0].CompensationResult.Content != "refund reservation voided" {
		t.Fatalf("unexpected compensation result: %+v", run.Steps[0].CompensationResult)
	}
}

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	registry := NewRegistry()
	err := registry.Register(ToolDefinition{
		Name:                "check_inventory",
		RequiredPermissions: []string{"inventory:read"},
		Timeout:             time.Second,
		Parameters: JSONSchema{
			Type:     "object",
			Required: []string{"sku"},
			Properties: map[string]SchemaProperty{
				"sku": {Type: "string"},
			},
		},
	}, func(_ context.Context, inv ToolInvocation) (ToolResult, error) {
		return ToolResult{
			Content: "stock available",
			Data: map[string]interface{}{
				"sku": inv.Arguments["sku"],
			},
		}, nil
	})
	if err != nil {
		t.Fatalf("register tool: %v", err)
	}
	return registry
}

func newTestOrchestrator(t *testing.T, store Store, registry *Registry, planner Planner, maxSteps int, nodeID string) *Orchestrator {
	t.Helper()
	return newTestOrchestratorWithLocks(t, store, NewMemoryLockManager(), registry, planner, maxSteps, nodeID)
}

func newTestOrchestratorWithLocks(t *testing.T, store Store, locks LockManager, registry *Registry, planner Planner, maxSteps int, nodeID string) *Orchestrator {
	t.Helper()
	orchestrator, err := NewOrchestrator(store, locks, registry, planner, Options{
		NodeID:     nodeID,
		MaxSteps:   maxSteps,
		LockTTL:    time.Second,
		Authorizer: StaticAuthorizer{},
	})
	if err != nil {
		t.Fatalf("new orchestrator: %v", err)
	}
	return orchestrator
}

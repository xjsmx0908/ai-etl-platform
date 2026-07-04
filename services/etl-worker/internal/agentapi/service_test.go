package agentapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ai-etl-pipeline/internal/agent"
	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/query"
)

type fakeQueryService struct {
	calls      int
	lastReq    query.Request
	lastAccess query.AccessContext
}

func (f *fakeQueryService) Ask(_ context.Context, req query.Request, access query.AccessContext) (query.Response, error) {
	f.calls++
	f.lastReq = req
	f.lastAccess = access
	return query.Response{
		Answer:   "报销制度要求提交发票并经过直属经理审批。",
		Sources:  []query.SourceContext{{ChunkID: "c1", DocID: "d1", Content: "ctx", Score: 0.91}},
		Duration: "1ms",
	}, nil
}

func TestHandleRunsCreatesAndExecutesRAGRun(t *testing.T) {
	qs := &fakeQueryService{}
	store := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	if err := registerRAGQueryTool(registry, qs); err != nil {
		t.Fatalf("register rag tool: %v", err)
	}
	orchestrator := newTestOrchestrator(t, store, registry, RulePlanner{}, 4)
	svc := newServiceWithComponents(orchestrator, store)

	req := authenticatedRequest(http.MethodPost, "/v1/agent/runs", []byte(`{"task":"公司的报销制度是什么"}`))
	rr := httptest.NewRecorder()
	svc.HandleRuns(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d body=%s", http.StatusCreated, rr.Code, rr.Body.String())
	}
	var run agent.Run
	if err := json.NewDecoder(rr.Body).Decode(&run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if run.State != agent.StateCompleted {
		t.Fatalf("expected completed run, got %+v", run)
	}
	if run.Final != "报销制度要求提交发票并经过直属经理审批。" {
		t.Fatalf("unexpected final answer: %q", run.Final)
	}
	if len(run.Steps) != 2 || run.Steps[0].ToolName != ragQueryToolName {
		t.Fatalf("expected rag tool and final steps, got %+v", run.Steps)
	}
	if qs.calls != 1 {
		t.Fatalf("expected one query service call, got %d", qs.calls)
	}
	if qs.lastAccess.TenantID != "tenant-a" || qs.lastAccess.Role != "user" {
		t.Fatalf("expected tenant/role propagation, got %+v", qs.lastAccess)
	}
	if qs.lastReq.Question != "公司的报销制度是什么" || qs.lastReq.TopK != 5 {
		t.Fatalf("unexpected query request: %+v", qs.lastReq)
	}

	getReq := authenticatedRequest(http.MethodGet, "/v1/agent/runs/"+run.ID, nil)
	getRR := httptest.NewRecorder()
	svc.HandleRun(getRR, getReq)
	if getRR.Code != http.StatusOK {
		t.Fatalf("expected get status %d, got %d body=%s", http.StatusOK, getRR.Code, getRR.Body.String())
	}
}

func TestHandleRunApproveResumesPendingTool(t *testing.T) {
	store := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	calls := 0
	if err := registry.Register(agent.ToolDefinition{
		Name:                "publish_report",
		RequiredPermissions: []string{"agent"},
		RequiresApproval:    true,
		SideEffect:          true,
		Idempotent:          true,
		Parameters: agent.JSONSchema{
			Type: "object",
			Properties: map[string]agent.SchemaProperty{
				"title": {Type: "string"},
			},
		},
	}, func(context.Context, agent.ToolInvocation) (agent.ToolResult, error) {
		calls++
		return agent.ToolResult{Content: "report published"}, nil
	}); err != nil {
		t.Fatalf("register publish_report: %v", err)
	}
	planner := &approvalPlanner{}
	orchestrator := newTestOrchestrator(t, store, registry, planner, 4)
	svc := newServiceWithComponents(orchestrator, store)

	req := authenticatedRequest(http.MethodPost, "/v1/agent/runs", []byte(`{"task":"发布报表"}`))
	rr := httptest.NewRecorder()
	svc.HandleRuns(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected create status %d, got %d body=%s", http.StatusCreated, rr.Code, rr.Body.String())
	}
	var run agent.Run
	if err := json.NewDecoder(rr.Body).Decode(&run); err != nil {
		t.Fatalf("decode pending run: %v", err)
	}
	if run.State != agent.StatePendingApproval {
		t.Fatalf("expected pending approval, got %+v", run)
	}
	if calls != 0 {
		t.Fatalf("tool should not execute before approval, got calls=%d", calls)
	}

	approveReq := authenticatedRequest(http.MethodPost, "/v1/agent/runs/"+run.ID+"/approve", []byte(`{"tool_name":"publish_report"}`))
	approveRR := httptest.NewRecorder()
	svc.HandleRun(approveRR, approveReq)
	if approveRR.Code != http.StatusOK {
		t.Fatalf("expected approve status %d, got %d body=%s", http.StatusOK, approveRR.Code, approveRR.Body.String())
	}
	if err := json.NewDecoder(approveRR.Body).Decode(&run); err != nil {
		t.Fatalf("decode approved run: %v", err)
	}
	if run.State != agent.StateCompleted || run.Final != "report published" {
		t.Fatalf("expected approved completion, got %+v", run)
	}
	if calls != 1 {
		t.Fatalf("expected one tool execution after approval, got %d", calls)
	}
}

type approvalPlanner struct{}

func (approvalPlanner) Plan(_ context.Context, run agent.Run) (agent.PlanDecision, error) {
	if len(run.Steps) == 0 {
		return agent.PlanDecision{
			Type:      agent.DecisionToolCall,
			ToolName:  "publish_report",
			Arguments: json.RawMessage(`{"title":"quarterly"}`),
		}, nil
	}
	return agent.PlanDecision{Type: agent.DecisionFinal, Final: run.Steps[len(run.Steps)-1].ToolResult.Content}, nil
}

func authenticatedRequest(method, target string, body []byte) *http.Request {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	ctx := context.WithValue(req.Context(), auth.CtxTenantID, "tenant-a")
	ctx = context.WithValue(ctx, auth.CtxUserID, "user-a")
	ctx = context.WithValue(ctx, auth.CtxPermission, "user")
	ctx = context.WithValue(ctx, auth.CtxScopes, []string{"agent", "query"})
	return req.WithContext(ctx)
}

func newTestOrchestrator(t *testing.T, store agent.Store, registry *agent.Registry, planner agent.Planner, maxSteps int) *agent.Orchestrator {
	t.Helper()
	orchestrator, err := agent.NewOrchestrator(store, agent.NewMemoryLockManager(), registry, planner, agent.Options{
		NodeID:     "agentapi-test",
		MaxSteps:   maxSteps,
		LockTTL:    time.Second,
		Authorizer: agent.StaticAuthorizer{},
	})
	if err != nil {
		t.Fatalf("new orchestrator: %v", err)
	}
	return orchestrator
}

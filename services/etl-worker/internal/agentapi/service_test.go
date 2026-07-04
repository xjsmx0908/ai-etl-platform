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
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/query"
	"ai-etl-pipeline/internal/taskstatus"
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

	listReq := authenticatedRequest(http.MethodGet, "/v1/agent/runs/"+run.ID+"/approvals", nil)
	listRR := httptest.NewRecorder()
	svc.HandleRun(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("expected approval list status %d, got %d body=%s", http.StatusOK, listRR.Code, listRR.Body.String())
	}
	var approvals []agent.ApprovalRequest
	if err := json.NewDecoder(listRR.Body).Decode(&approvals); err != nil {
		t.Fatalf("decode approvals: %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("expected one pending approval, got %+v", approvals)
	}
	if approvals[0].Status != agent.ApprovalPending || approvals[0].ToolName != "publish_report" {
		t.Fatalf("unexpected pending approval: %+v", approvals[0])
	}

	userApproveReq := authenticatedRequest(http.MethodPost, "/v1/agent/runs/"+run.ID+"/approve", []byte(`{"tool_name":"publish_report"}`))
	userApproveRR := httptest.NewRecorder()
	svc.HandleRun(userApproveRR, userApproveReq)
	if userApproveRR.Code != http.StatusForbidden {
		t.Fatalf("expected user approve status %d, got %d body=%s", http.StatusForbidden, userApproveRR.Code, userApproveRR.Body.String())
	}
	if calls != 0 {
		t.Fatalf("tool should not execute after forbidden approval, got calls=%d", calls)
	}

	approveReq := adminRequest(http.MethodPost, "/v1/agent/runs/"+run.ID+"/approve", []byte(`{"approval_id":"`+approvals[0].ID+`","tool_name":"publish_report","reason":"release approved"}`))
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

	decided, err := svc.approvalStore.LoadApproval(context.Background(), "tenant-a", approvals[0].ID)
	if err != nil {
		t.Fatalf("load decided approval: %v", err)
	}
	if decided.Status != agent.ApprovalApproved || decided.DecidedBy != "admin-a" || decided.Reason != "release approved" {
		t.Fatalf("expected approved audit record, got %+v", decided)
	}
}

func TestHandleRunRejectFailsPendingToolAndAuditsDecision(t *testing.T) {
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
	orchestrator := newTestOrchestrator(t, store, registry, &approvalPlanner{}, 4)
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
	approvals, err := svc.approvalStore.ListRunApprovals(context.Background(), "tenant-a", run.ID)
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("expected one approval, got %+v", approvals)
	}

	otherTenantReq := authenticatedRequestAs(http.MethodGet, "/v1/agent/runs/"+run.ID+"/approvals", nil, "tenant-b", "user-b", "user", []string{"agent", "query"})
	otherTenantRR := httptest.NewRecorder()
	svc.HandleRun(otherTenantRR, otherTenantReq)
	if otherTenantRR.Code != http.StatusNotFound {
		t.Fatalf("expected tenant isolation status %d, got %d body=%s", http.StatusNotFound, otherTenantRR.Code, otherTenantRR.Body.String())
	}

	rejectReq := adminRequest(http.MethodPost, "/v1/agent/runs/"+run.ID+"/reject", []byte(`{"approval_id":"`+approvals[0].ID+`","reason":"missing release window"}`))
	rejectRR := httptest.NewRecorder()
	svc.HandleRun(rejectRR, rejectReq)
	if rejectRR.Code != http.StatusOK {
		t.Fatalf("expected reject status %d, got %d body=%s", http.StatusOK, rejectRR.Code, rejectRR.Body.String())
	}
	if err := json.NewDecoder(rejectRR.Body).Decode(&run); err != nil {
		t.Fatalf("decode rejected run: %v", err)
	}
	if run.State != agent.StateFailed {
		t.Fatalf("expected failed run after rejection, got %+v", run)
	}
	if len(run.Steps) != 1 || run.Steps[0].State != agent.StateFailed {
		t.Fatalf("expected failed pending step, got %+v", run.Steps)
	}
	if calls != 0 {
		t.Fatalf("tool should not execute after rejection, got %d", calls)
	}
	decided, err := svc.approvalStore.LoadApproval(context.Background(), "tenant-a", approvals[0].ID)
	if err != nil {
		t.Fatalf("load rejected approval: %v", err)
	}
	if decided.Status != agent.ApprovalRejected || decided.DecidedBy != "admin-a" || decided.Reason != "missing release window" {
		t.Fatalf("expected rejected audit record, got %+v", decided)
	}
}

func TestHandleRunCancelPendingApprovalRejectsApprovalAudit(t *testing.T) {
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
	orchestrator := newTestOrchestrator(t, store, registry, &approvalPlanner{}, 4)
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
	approvals, err := svc.approvalStore.ListRunApprovals(context.Background(), "tenant-a", run.ID)
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("expected one approval, got %+v", approvals)
	}

	cancelReq := authenticatedRequest(http.MethodPost, "/v1/agent/runs/"+run.ID+"/cancel", []byte(`{"reason":"duplicate request"}`))
	cancelRR := httptest.NewRecorder()
	svc.HandleRun(cancelRR, cancelReq)
	if cancelRR.Code != http.StatusOK {
		t.Fatalf("expected cancel status %d, got %d body=%s", http.StatusOK, cancelRR.Code, cancelRR.Body.String())
	}
	if err := json.NewDecoder(cancelRR.Body).Decode(&run); err != nil {
		t.Fatalf("decode cancelled run: %v", err)
	}
	if run.State != agent.StateCancelled || run.CancelledBy != "user-a" || run.CancelReason != "duplicate request" {
		t.Fatalf("expected cancelled run metadata, got %+v", run)
	}
	if len(run.Steps) != 1 || run.Steps[0].State != agent.StateCancelled {
		t.Fatalf("expected pending step cancelled, got %+v", run.Steps)
	}
	if calls != 0 {
		t.Fatalf("tool should not execute after cancel, got %d", calls)
	}
	decided, err := svc.approvalStore.LoadApproval(context.Background(), "tenant-a", approvals[0].ID)
	if err != nil {
		t.Fatalf("load cancelled approval: %v", err)
	}
	if decided.Status != agent.ApprovalRejected || decided.DecidedBy != "user-a" || decided.Reason != "duplicate request" {
		t.Fatalf("expected rejected approval after cancel, got %+v", decided)
	}
}

func TestHandleRunApproveExpiredApprovalRejectsAudit(t *testing.T) {
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
	orchestrator := newTestOrchestratorWithTimeouts(t, store, registry, &approvalPlanner{}, 4, time.Hour, time.Nanosecond)
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
	approvals, err := svc.approvalStore.ListRunApprovals(context.Background(), "tenant-a", run.ID)
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("expected one approval, got %+v", approvals)
	}
	stored, err := store.LoadRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("load pending run: %v", err)
	}
	stored.Steps[0].StartedAt = time.Now().UTC().Add(-time.Hour)
	if _, err := store.SaveRun(context.Background(), stored, agent.SaveOptions{ExpectedVersion: stored.Version, FencingToken: stored.FencingToken}); err != nil {
		t.Fatalf("age pending approval step: %v", err)
	}

	approveReq := adminRequest(http.MethodPost, "/v1/agent/runs/"+run.ID+"/approve", []byte(`{"approval_id":"`+approvals[0].ID+`","tool_name":"publish_report"}`))
	approveRR := httptest.NewRecorder()
	svc.HandleRun(approveRR, approveReq)
	if approveRR.Code != http.StatusConflict {
		t.Fatalf("expected expired approve status %d, got %d body=%s", http.StatusConflict, approveRR.Code, approveRR.Body.String())
	}
	if calls != 0 {
		t.Fatalf("tool should not execute after approval timeout, got %d", calls)
	}
	decided, err := svc.approvalStore.LoadApproval(context.Background(), "tenant-a", approvals[0].ID)
	if err != nil {
		t.Fatalf("load timed-out approval: %v", err)
	}
	if decided.Status != agent.ApprovalRejected || decided.DecidedBy != "system" || decided.Reason != "approval_timeout_exceeded" {
		t.Fatalf("expected system-rejected timeout approval, got %+v", decided)
	}
	stored, err = store.LoadRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("load timed-out run: %v", err)
	}
	if stored.State != agent.StateFailed || stored.Error != "approval_timeout_exceeded" {
		t.Fatalf("expected timed-out run, got %+v", stored)
	}
}

func TestHandleRunApproveExpiredRunRejectsAudit(t *testing.T) {
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
	orchestrator := newTestOrchestratorWithTimeouts(t, store, registry, &approvalPlanner{}, 4, time.Minute, time.Hour)
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
	approvals, err := svc.approvalStore.ListRunApprovals(context.Background(), "tenant-a", run.ID)
	if err != nil {
		t.Fatalf("list approvals: %v", err)
	}
	if len(approvals) != 1 {
		t.Fatalf("expected one approval, got %+v", approvals)
	}

	stored, err := store.LoadRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("load pending run: %v", err)
	}
	stored.CreatedAt = time.Now().UTC().Add(-2 * time.Minute)
	if _, err := store.SaveRun(context.Background(), stored, agent.SaveOptions{ExpectedVersion: stored.Version, FencingToken: stored.FencingToken}); err != nil {
		t.Fatalf("age pending run: %v", err)
	}

	approveReq := adminRequest(http.MethodPost, "/v1/agent/runs/"+run.ID+"/approve", []byte(`{"approval_id":"`+approvals[0].ID+`","tool_name":"publish_report"}`))
	approveRR := httptest.NewRecorder()
	svc.HandleRun(approveRR, approveReq)
	if approveRR.Code != http.StatusConflict {
		t.Fatalf("expected expired approve status %d, got %d body=%s", http.StatusConflict, approveRR.Code, approveRR.Body.String())
	}
	if calls != 0 {
		t.Fatalf("tool should not execute after run timeout, got %d", calls)
	}
	decided, err := svc.approvalStore.LoadApproval(context.Background(), "tenant-a", approvals[0].ID)
	if err != nil {
		t.Fatalf("load timed-out approval: %v", err)
	}
	if decided.Status != agent.ApprovalRejected || decided.DecidedBy != "system" || decided.Reason != "run_timeout_exceeded" {
		t.Fatalf("expected system-rejected run timeout approval, got %+v", decided)
	}
	stored, err = store.LoadRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("load timed-out run: %v", err)
	}
	if stored.State != agent.StateFailed || stored.Error != "run_timeout_exceeded" {
		t.Fatalf("expected timed-out run, got %+v", stored)
	}
}

func TestHandleRunCancelRejectsTerminalAndOtherTenant(t *testing.T) {
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
		t.Fatalf("expected create status %d, got %d body=%s", http.StatusCreated, rr.Code, rr.Body.String())
	}
	var run agent.Run
	if err := json.NewDecoder(rr.Body).Decode(&run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if run.State != agent.StateCompleted {
		t.Fatalf("expected completed run, got %+v", run)
	}

	cancelReq := authenticatedRequest(http.MethodPost, "/v1/agent/runs/"+run.ID+"/cancel", []byte(`{"reason":"too late"}`))
	cancelRR := httptest.NewRecorder()
	svc.HandleRun(cancelRR, cancelReq)
	if cancelRR.Code != http.StatusConflict {
		t.Fatalf("expected terminal cancel status %d, got %d body=%s", http.StatusConflict, cancelRR.Code, cancelRR.Body.String())
	}

	resumeReq := authenticatedRequest(http.MethodPost, "/v1/agent/runs/"+run.ID+"/resume", nil)
	resumeRR := httptest.NewRecorder()
	svc.HandleRun(resumeRR, resumeReq)
	if resumeRR.Code != http.StatusConflict {
		t.Fatalf("expected terminal resume status %d, got %d body=%s", http.StatusConflict, resumeRR.Code, resumeRR.Body.String())
	}

	otherTenantReq := authenticatedRequestAs(http.MethodPost, "/v1/agent/runs/"+run.ID+"/cancel", []byte(`{"reason":"wrong tenant"}`), "tenant-b", "user-b", "user", []string{"agent", "query"})
	otherTenantRR := httptest.NewRecorder()
	svc.HandleRun(otherTenantRR, otherTenantReq)
	if otherTenantRR.Code != http.StatusNotFound {
		t.Fatalf("expected tenant isolation status %d, got %d body=%s", http.StatusNotFound, otherTenantRR.Code, otherTenantRR.Body.String())
	}
}

func TestHandleRunCancelRejectsSameTenantNonOwner(t *testing.T) {
	qs := &fakeQueryService{}
	store := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	if err := registerRAGQueryTool(registry, qs); err != nil {
		t.Fatalf("register rag tool: %v", err)
	}
	orchestrator := newTestOrchestrator(t, store, registry, RulePlanner{}, 4)
	svc := newServiceWithComponents(orchestrator, store)

	req := authenticatedRequest(http.MethodPost, "/v1/agent/runs", []byte(`{"task":"公司的报销制度是什么","auto_execute":false}`))
	rr := httptest.NewRecorder()
	svc.HandleRuns(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("expected create status %d, got %d body=%s", http.StatusCreated, rr.Code, rr.Body.String())
	}
	var run agent.Run
	if err := json.NewDecoder(rr.Body).Decode(&run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if run.State != agent.StateCreated {
		t.Fatalf("expected created run, got %+v", run)
	}

	otherUserReq := authenticatedRequestAs(http.MethodPost, "/v1/agent/runs/"+run.ID+"/cancel", []byte(`{"reason":"not mine"}`), "tenant-a", "user-b", "user", []string{"agent", "query"})
	otherUserRR := httptest.NewRecorder()
	svc.HandleRun(otherUserRR, otherUserReq)
	if otherUserRR.Code != http.StatusForbidden {
		t.Fatalf("expected same-tenant non-owner cancel status %d, got %d body=%s", http.StatusForbidden, otherUserRR.Code, otherUserRR.Body.String())
	}

	adminReq := adminRequest(http.MethodPost, "/v1/agent/runs/"+run.ID+"/cancel", []byte(`{"reason":"admin stop"}`))
	adminRR := httptest.NewRecorder()
	svc.HandleRun(adminRR, adminReq)
	if adminRR.Code != http.StatusOK {
		t.Fatalf("expected admin cancel status %d, got %d body=%s", http.StatusOK, adminRR.Code, adminRR.Body.String())
	}
	if err := json.NewDecoder(adminRR.Body).Decode(&run); err != nil {
		t.Fatalf("decode cancelled run: %v", err)
	}
	if run.State != agent.StateCancelled || run.CancelledBy != "admin-a" {
		t.Fatalf("expected admin-cancelled run, got %+v", run)
	}
}

func TestHandleRunResumeUsesApprovedAuditRecord(t *testing.T) {
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
	orchestrator := newTestOrchestrator(t, store, registry, &approvalPlanner{}, 4)
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
	approvalID := agent.ApprovalIDForStep(run.ID, 1)
	if _, err := svc.approvalStore.DecideApproval(context.Background(), "tenant-a", approvalID, agent.ApprovalApproved, "admin-a", "pre-approved", time.Now().UTC()); err != nil {
		t.Fatalf("pre-approve audit record: %v", err)
	}

	resumeReq := authenticatedRequest(http.MethodPost, "/v1/agent/runs/"+run.ID+"/resume", nil)
	resumeRR := httptest.NewRecorder()
	svc.HandleRun(resumeRR, resumeReq)
	if resumeRR.Code != http.StatusOK {
		t.Fatalf("expected resume status %d, got %d body=%s", http.StatusOK, resumeRR.Code, resumeRR.Body.String())
	}
	if err := json.NewDecoder(resumeRR.Body).Decode(&run); err != nil {
		t.Fatalf("decode resumed run: %v", err)
	}
	if run.State != agent.StateCompleted || run.Final != "report published" {
		t.Fatalf("expected resume to use approved audit record, got %+v", run)
	}
	if calls != 1 {
		t.Fatalf("expected one tool execution after resume, got %d", calls)
	}
}

func TestHandleRunsExecutesTaskStatusTool(t *testing.T) {
	statusStore := taskstatus.NewMemoryStore()
	now := time.Now().UTC()
	if err := statusStore.Save(context.Background(), model.TaskStatus{
		TaskID:    "doc-123",
		DocID:     "doc-123",
		TenantID:  "tenant-a",
		Status:    model.TaskStatusProcessing,
		Stage:     "embedding",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save task status: %v", err)
	}

	store := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	if err := registerTaskStatusTool(registry, statusStore); err != nil {
		t.Fatalf("register task status tool: %v", err)
	}
	orchestrator := newTestOrchestrator(t, store, registry, taskStatusPlanner{taskID: "doc-123"}, 4)
	svc := newServiceWithComponents(orchestrator, store)

	req := authenticatedRequest(http.MethodPost, "/v1/agent/runs", []byte(`{"task":"查询任务 doc-123 状态"}`))
	rr := httptest.NewRecorder()
	svc.HandleRuns(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d body=%s", http.StatusCreated, rr.Code, rr.Body.String())
	}
	var run agent.Run
	if err := json.NewDecoder(rr.Body).Decode(&run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if run.State != agent.StateCompleted || len(run.Steps) != 2 {
		t.Fatalf("expected completed run with tool and final steps, got %+v", run)
	}
	if run.Steps[0].ToolName != etlTaskStatusToolName {
		t.Fatalf("expected task status tool, got %+v", run.Steps[0])
	}
	if run.Steps[0].ToolResult == nil || run.Steps[0].ToolResult.Data["found"] != true {
		t.Fatalf("expected found task status result, got %+v", run.Steps[0].ToolResult)
	}
}

func TestTaskStatusToolDoesNotLeakOtherTenantStatus(t *testing.T) {
	statusStore := taskstatus.NewMemoryStore()
	now := time.Now().UTC()
	if err := statusStore.Save(context.Background(), model.TaskStatus{
		TaskID:    "doc-secret",
		DocID:     "doc-secret",
		TenantID:  "tenant-b",
		Status:    model.TaskStatusCompleted,
		Stage:     "completed",
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("save task status: %v", err)
	}

	store := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	if err := registerTaskStatusTool(registry, statusStore); err != nil {
		t.Fatalf("register task status tool: %v", err)
	}
	orchestrator := newTestOrchestrator(t, store, registry, taskStatusPlanner{taskID: "doc-secret"}, 4)
	svc := newServiceWithComponents(orchestrator, store)

	req := authenticatedRequest(http.MethodPost, "/v1/agent/runs", []byte(`{"task":"查询任务 doc-secret 状态"}`))
	rr := httptest.NewRecorder()
	svc.HandleRuns(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected %d, got %d body=%s", http.StatusCreated, rr.Code, rr.Body.String())
	}
	var run agent.Run
	if err := json.NewDecoder(rr.Body).Decode(&run); err != nil {
		t.Fatalf("decode run: %v", err)
	}
	if run.Steps[0].ToolResult == nil || run.Steps[0].ToolResult.Data["found"] != false {
		t.Fatalf("expected not_found without tenant leak, got %+v", run.Steps[0].ToolResult)
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

type taskStatusPlanner struct {
	taskID string
}

func (p taskStatusPlanner) Plan(_ context.Context, run agent.Run) (agent.PlanDecision, error) {
	if len(run.Steps) == 0 {
		args, _ := json.Marshal(map[string]string{"task_id": p.taskID})
		return agent.PlanDecision{
			Type:      agent.DecisionToolCall,
			ToolName:  etlTaskStatusToolName,
			Arguments: args,
		}, nil
	}
	return agent.PlanDecision{Type: agent.DecisionFinal, Final: run.Steps[len(run.Steps)-1].ToolResult.Content}, nil
}

func authenticatedRequest(method, target string, body []byte) *http.Request {
	return authenticatedRequestAs(method, target, body, "tenant-a", "user-a", "user", []string{"agent", "query"})
}

func adminRequest(method, target string, body []byte) *http.Request {
	return authenticatedRequestAs(method, target, body, "tenant-a", "admin-a", "admin", []string{"agent", "query"})
}

func authenticatedRequestAs(method, target string, body []byte, tenantID, userID, role string, scopes []string) *http.Request {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		reader = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, reader)
	ctx := context.WithValue(req.Context(), auth.CtxTenantID, tenantID)
	ctx = context.WithValue(ctx, auth.CtxUserID, userID)
	ctx = context.WithValue(ctx, auth.CtxPermission, role)
	ctx = context.WithValue(ctx, auth.CtxScopes, scopes)
	return req.WithContext(ctx)
}

func newTestOrchestrator(t *testing.T, store agent.Store, registry *agent.Registry, planner agent.Planner, maxSteps int) *agent.Orchestrator {
	t.Helper()
	return newTestOrchestratorWithTimeouts(t, store, registry, planner, maxSteps, 0, 0)
}

func newTestOrchestratorWithTimeouts(t *testing.T, store agent.Store, registry *agent.Registry, planner agent.Planner, maxSteps int, runTimeout, approvalTimeout time.Duration) *agent.Orchestrator {
	t.Helper()
	orchestrator, err := agent.NewOrchestrator(store, agent.NewMemoryLockManager(), registry, planner, agent.Options{
		NodeID:          "agentapi-test",
		MaxSteps:        maxSteps,
		LockTTL:         time.Second,
		RunTimeout:      runTimeout,
		ApprovalTimeout: approvalTimeout,
		Authorizer:      agent.StaticAuthorizer{},
	})
	if err != nil {
		t.Fatalf("new orchestrator: %v", err)
	}
	return orchestrator
}

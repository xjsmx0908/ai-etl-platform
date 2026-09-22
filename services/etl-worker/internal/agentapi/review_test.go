package agentapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ai-etl-pipeline/internal/agent"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/knowledgecatalog"
	"ai-etl-pipeline/internal/publicationworkflow"
	"ai-etl-pipeline/internal/releasecenter"
	"ai-etl-pipeline/internal/store"
)

type reviewDocumentStub struct {
	document docstore.Document
}

func (s reviewDocumentStub) Get(context.Context, string, string) (docstore.Document, bool, error) {
	return s.document, true, nil
}

type reviewChunkStub struct {
	chunks []store.StoredChunk
}

func (s reviewChunkStub) ListChunksByDoc(context.Context, string, string, []string) ([]store.StoredChunk, error) {
	return s.chunks, nil
}

func TestAutonomousReviewRulePlannerCompletesEvidenceLoop(t *testing.T) {
	candidate := reviewCandidate()
	workflow := &fakePublicationWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	runStore := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	if err := registerReviewTools(registry, workflow,
		reviewDocumentStub{document: docstore.Document{TenantID: "tenant-a", DocID: candidate.DocumentID, Permission: "internal", KnowledgeSpaceID: "policies", Owner: "owner"}},
		reviewChunkStub{chunks: []store.StoredChunk{{ChunkID: "chunk-1", TenantID: "tenant-a", DocID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, Content: "普通制度内容", Index: 0}}}, runStore); err != nil {
		t.Fatal(err)
	}
	orchestrator := newTestOrchestrator(t, runStore, registry, ReviewRulePlanner{}, 8)
	service := newServiceWithComponents(orchestrator, runStore)
	service.reviewOrchestrator = orchestrator
	service.reviewWorkflow = workflow

	report, err := service.ReviewPublicationReport(context.Background(), agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Role: "admin", Permissions: []string{"agent"}}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || report.Recommendation != "publish" || report.Candidate == nil || *report.Candidate != candidate {
		t.Fatalf("report=%+v", report)
	}
	run, err := runStore.LoadRun(context.Background(), report.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Steps) != 6 {
		t.Fatalf("steps=%+v", run.Steps)
	}
	wantTools := []string{getReviewContextToolName, getExactCandidateChunksToolName, scanSensitiveDataToolName, scanPromptInjectionToolName, assessKnowledgeFitnessToolName}
	for index, toolName := range wantTools {
		if run.Steps[index].ToolName != toolName || run.Steps[index].Observation == "" {
			t.Fatalf("step %d=%+v", index, run.Steps[index])
		}
	}
}

func TestAutonomousReviewResumesPersistedRunWithoutDuplicatingSteps(t *testing.T) {
	candidate := reviewCandidate()
	workflow := &fakePublicationWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	runStore := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	if err := registerReviewTools(registry, workflow,
		reviewDocumentStub{document: docstore.Document{TenantID: "tenant-a", DocID: candidate.DocumentID, Permission: "internal", KnowledgeSpaceID: "policies", Owner: "owner"}},
		reviewChunkStub{chunks: []store.StoredChunk{{ChunkID: "chunk-1", TenantID: "tenant-a", DocID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, Content: "普通制度内容", Index: 0}}}, runStore); err != nil {
		t.Fatal(err)
	}
	actor := agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Role: "admin", Permissions: []string{"agent"}}
	first := newTestOrchestrator(t, runStore, registry, ReviewRulePlanner{}, 8)
	started, err := first.StartOrResume(context.Background(), actor, reviewRunID(actor.TenantID, candidate, reviewPromptVersion, 1, ""), documentReviewTaskPrefix+candidate.DocumentID, map[string]interface{}{"review_candidate": structMap(candidate)})
	if err != nil {
		t.Fatal(err)
	}
	started, err = first.ExecuteNext(context.Background(), started.ID, actor)
	if err != nil || len(started.Steps) != 1 || started.Steps[0].ToolName != getReviewContextToolName {
		t.Fatalf("expected one persisted review step before restart: run=%+v err=%v", started, err)
	}

	second := newTestOrchestrator(t, runStore, registry, ReviewRulePlanner{}, 8)
	service := newServiceWithComponents(second, runStore)
	service.reviewOrchestrator = second
	service.reviewWorkflow = workflow
	report, err := service.ReviewPublicationReport(context.Background(), actor, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || report.RunID != started.ID || report.Candidate == nil || *report.Candidate != candidate {
		t.Fatalf("resumed report=%+v", report)
	}
	resumed, err := runStore.LoadRun(context.Background(), started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.Steps) != 6 {
		t.Fatalf("expected five tools plus final step, got %d steps", len(resumed.Steps))
	}
	if resumed.Steps[0].ToolName != getReviewContextToolName || resumed.Steps[1].ToolName != getExactCandidateChunksToolName {
		t.Fatalf("resume changed persisted step order: %+v", resumed.Steps)
	}
}

func TestAutonomousReviewRejectsCandidateDriftDuringResume(t *testing.T) {
	candidate := reviewCandidate()
	changed := candidate
	changed.GenerationID = "generation-2"
	workflow := &fakePublicationWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	runStore := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	if err := registerReviewTools(registry, workflow,
		reviewDocumentStub{document: docstore.Document{TenantID: "tenant-a", DocID: candidate.DocumentID, Permission: "internal", KnowledgeSpaceID: "policies", Owner: "owner"}},
		reviewChunkStub{chunks: []store.StoredChunk{{ChunkID: "chunk-1", TenantID: "tenant-a", DocID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, Content: "普通制度内容", Index: 0}}}, runStore); err != nil {
		t.Fatal(err)
	}
	actor := agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Role: "admin", Permissions: []string{"agent"}}
	orchestrator := newTestOrchestrator(t, runStore, registry, ReviewRulePlanner{}, 8)
	started, err := orchestrator.StartOrResume(context.Background(), actor, reviewRunID(actor.TenantID, candidate, reviewPromptVersion, 1, ""), documentReviewTaskPrefix+candidate.DocumentID, map[string]interface{}{"review_candidate": structMap(candidate)})
	if err != nil {
		t.Fatal(err)
	}
	started, err = orchestrator.ExecuteNext(context.Background(), started.ID, actor)
	if err != nil {
		t.Fatal(err)
	}
	workflow.assessment.Candidate = &changed
	service := newServiceWithComponents(orchestrator, runStore)
	service.reviewOrchestrator = orchestrator
	service.reviewWorkflow = workflow
	report, err := service.ResumePublicationReport(context.Background(), actor, started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "failed" || report.Recommendation != "manual_review" || !strings.Contains(report.Summary, "exact candidate changed") {
		t.Fatalf("candidate drift was not failed closed: %+v", report)
	}
}

func TestAutonomousReviewRedisRestartResumesSameRun(t *testing.T) {
	addr := os.Getenv("AGENT_REDIS_REVIEW_TEST_ADDR")
	if addr == "" {
		t.Skip("set AGENT_REDIS_REVIEW_TEST_ADDR to run Redis review restart integration test")
	}
	candidate := reviewCandidate()
	candidate.DocumentID = "redis-" + strings.ReplaceAll(t.Name(), "/", "-")
	workflow := &fakePublicationWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	documents := reviewDocumentStub{document: docstore.Document{TenantID: "tenant-a", DocID: candidate.DocumentID, Permission: "internal", KnowledgeSpaceID: "policies", Owner: "owner"}}
	chunks := reviewChunkStub{chunks: []store.StoredChunk{{ChunkID: "chunk-1", TenantID: "tenant-a", DocID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, Content: "普通制度内容", Index: 0}}}
	actor := agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Role: "admin", Permissions: []string{"agent"}}
	firstStore, err := agent.NewRedisStore(addr, os.Getenv("AGENT_REDIS_REVIEW_TEST_PASSWORD"), 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	firstLocks, err := agent.NewRedisLockManager(addr, os.Getenv("AGENT_REDIS_REVIEW_TEST_PASSWORD"), 0, time.Hour)
	if err != nil {
		_ = firstStore.Close()
		t.Fatal(err)
	}
	registry := agent.NewRegistry()
	if err := registerReviewTools(registry, workflow, documents, chunks, firstStore); err != nil {
		_ = firstStore.Close()
		_ = firstLocks.Close()
		t.Fatal(err)
	}
	first, err := agent.NewOrchestrator(firstStore, firstLocks, registry, ReviewRulePlanner{}, agent.Options{NodeID: "redis-review-a", MaxSteps: 8, LockTTL: time.Second, MaxTokenBudget: 32000, Authorizer: agent.StaticAuthorizer{}})
	if err != nil {
		_ = firstStore.Close()
		_ = firstLocks.Close()
		t.Fatal(err)
	}
	runID := reviewRunID(actor.TenantID, candidate, reviewPromptVersion, 1, "")
	run, err := first.StartOrResume(context.Background(), actor, runID, documentReviewTaskPrefix+candidate.DocumentID, map[string]interface{}{"review_candidate": structMap(candidate)})
	if err != nil {
		_ = firstStore.Close()
		_ = firstLocks.Close()
		t.Fatal(err)
	}
	run, err = first.ExecuteNext(context.Background(), run.ID, actor)
	if err != nil || len(run.Steps) != 1 {
		_ = firstStore.Close()
		_ = firstLocks.Close()
		t.Fatalf("first instance did not persist resumable step: run=%+v err=%v", run, err)
	}
	_ = firstStore.Close()
	_ = firstLocks.Close()

	secondStore, err := agent.NewRedisStore(addr, os.Getenv("AGENT_REDIS_REVIEW_TEST_PASSWORD"), 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer secondStore.Close()
	secondLocks, err := agent.NewRedisLockManager(addr, os.Getenv("AGENT_REDIS_REVIEW_TEST_PASSWORD"), 0, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer secondLocks.Close()
	secondRegistry := agent.NewRegistry()
	if err := registerReviewTools(secondRegistry, workflow, documents, chunks, secondStore); err != nil {
		t.Fatal(err)
	}
	second, err := agent.NewOrchestrator(secondStore, secondLocks, secondRegistry, ReviewRulePlanner{}, agent.Options{NodeID: "redis-review-b", MaxSteps: 8, LockTTL: time.Second, MaxTokenBudget: 32000, Authorizer: agent.StaticAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	service := newServiceWithComponents(second, secondStore)
	service.reviewOrchestrator = second
	service.reviewWorkflow = workflow
	report, err := service.ReviewPublicationReport(context.Background(), actor, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.RunID != runID || report.Status != "completed" {
		t.Fatalf("redis restart did not resume same review run: %+v", report)
	}
	resumed, err := secondStore.LoadRun(context.Background(), runID)
	if err != nil {
		t.Fatal(err)
	}
	if len(resumed.Steps) != 6 || resumed.Steps[0].IdempotencyKey != "agent:"+runID+":1:"+getReviewContextToolName {
		t.Fatalf("redis restart changed review audit chain: %+v", resumed)
	}
}

func TestAutonomousReviewPreservesDeterministicSensitiveFinding(t *testing.T) {
	candidate := reviewCandidate()
	workflow := &fakePublicationWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	runStore := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	if err := registerReviewTools(registry, workflow,
		reviewDocumentStub{document: docstore.Document{TenantID: "tenant-a", DocID: candidate.DocumentID, Permission: "internal", KnowledgeSpaceID: "policies", Owner: "owner"}},
		reviewChunkStub{chunks: []store.StoredChunk{{ChunkID: "chunk-secret", TenantID: "tenant-a", DocID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, Content: "api_key=exposed", Index: 0}}}, runStore); err != nil {
		t.Fatal(err)
	}
	orchestrator := newTestOrchestrator(t, runStore, registry, ReviewRulePlanner{}, 8)
	service := newServiceWithComponents(orchestrator, runStore)
	service.reviewOrchestrator = orchestrator
	service.reviewWorkflow = workflow

	report, err := service.ReviewPublicationReport(context.Background(), agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Role: "admin", Permissions: []string{"agent"}}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || report.Recommendation != "needs_info" || report.RiskLevel != releasecenter.RiskHigh || len(report.Findings) != 1 || report.Findings[0].EvidenceRef != "chunk-secret" {
		t.Fatalf("report=%+v", report)
	}
}

func TestAutonomousReviewEscalatesConfidentialSensitiveFindingWithoutBlocking(t *testing.T) {
	candidate := reviewCandidate()
	workflow := &fakePublicationWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	runStore := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	if err := registerReviewTools(registry, workflow,
		reviewDocumentStub{document: docstore.Document{TenantID: "tenant-a", DocID: candidate.DocumentID, Permission: "confidential"}},
		reviewChunkStub{chunks: []store.StoredChunk{{ChunkID: "chunk-secret", DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, Content: "api_key=exposed", Index: 0}}}, runStore); err != nil {
		t.Fatal(err)
	}
	orchestrator := newTestOrchestrator(t, runStore, registry, ReviewRulePlanner{}, 8)
	service := newServiceWithComponents(orchestrator, runStore)
	service.reviewOrchestrator = orchestrator
	service.reviewWorkflow = workflow

	report, err := service.ReviewPublicationReport(context.Background(), agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Role: "admin", Permissions: []string{"agent"}}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || report.Recommendation != "publish" || report.RiskLevel != releasecenter.RiskHigh || len(report.Findings) != 1 {
		t.Fatalf("report=%+v", report)
	}
}

func TestReviewToolsFailClosedOnIncompleteExactCandidate(t *testing.T) {
	candidate := reviewCandidate()
	candidate.ExpectedChunkCount = 2
	workflow := &fakePublicationWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	runStore := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	if err := registerReviewTools(registry, workflow,
		reviewDocumentStub{document: docstore.Document{TenantID: "tenant-a", DocID: candidate.DocumentID, Permission: "internal"}},
		reviewChunkStub{chunks: []store.StoredChunk{{ChunkID: "chunk-1", DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, Content: "内容", Index: 0}}}, runStore); err != nil {
		t.Fatal(err)
	}
	service := newServiceWithComponents(newTestOrchestrator(t, runStore, registry, ReviewRulePlanner{}, 8), runStore)
	service.reviewOrchestrator = service.orchestrator
	service.reviewWorkflow = workflow
	report, err := service.ReviewPublicationReport(context.Background(), agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Role: "admin", Permissions: []string{"agent"}}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "failed" || report.Recommendation != "manual_review" || report.RiskLevel != releasecenter.RiskHigh {
		t.Fatalf("incomplete candidate was not failed closed: %+v", report)
	}
}

func TestAutonomousReviewFailsClosedWhenTokenBudgetIsExceeded(t *testing.T) {
	candidate := reviewCandidate()
	workflow := &fakePublicationWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	runStore := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	if err := registerReviewTools(registry, workflow,
		reviewDocumentStub{document: docstore.Document{TenantID: "tenant-a", DocID: candidate.DocumentID, Permission: "internal"}},
		reviewChunkStub{chunks: []store.StoredChunk{{ChunkID: "chunk-1", DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, Content: "内容", Index: 0}}}, runStore); err != nil {
		t.Fatal(err)
	}
	planner := &usagePlannerForReview{decision: agent.PlanDecision{Type: agent.DecisionFinal, Final: `{}`}, usage: agent.PlanUsage{PromptTokens: 6, CompletionTokens: 5}}
	orchestrator, err := agent.NewOrchestrator(runStore, agent.NewMemoryLockManager(), registry, planner, agent.Options{NodeID: "review", MaxSteps: 8, MaxTokenBudget: 10, Authorizer: agent.StaticAuthorizer{}})
	if err != nil {
		t.Fatal(err)
	}
	service := newServiceWithComponents(orchestrator, runStore)
	service.reviewOrchestrator = orchestrator
	service.reviewWorkflow = workflow
	report, err := service.ReviewPublicationReport(context.Background(), agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Permissions: []string{"agent"}}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "failed" || report.Recommendation != "manual_review" || report.RiskLevel != releasecenter.RiskHigh || !strings.Contains(report.Summary, "token_budget_exceeded") {
		t.Fatalf("budget exhaustion was not failed closed: %+v", report)
	}
}

func TestAutonomousReviewLLMPlannerUsesPersistedObservations(t *testing.T) {
	candidate := reviewCandidate()
	workflow := &fakePublicationWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	runStore := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	if err := registerReviewTools(registry, workflow,
		reviewDocumentStub{document: docstore.Document{TenantID: "tenant-a", DocID: candidate.DocumentID, Permission: "internal", KnowledgeSpaceID: "policies", Owner: "owner"}},
		reviewChunkStub{chunks: []store.StoredChunk{{ChunkID: "chunk-1", TenantID: "tenant-a", DocID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, Content: "普通制度内容", Index: 0}}}, runStore); err != nil {
		t.Fatal(err)
	}
	toolSequence := []string{getReviewContextToolName, getExactCandidateChunksToolName, scanSensitiveDataToolName, scanPromptInjectionToolName, assessKnowledgeFitnessToolName}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode planner request: %v", err)
		}
		if calls > 0 && !strings.Contains(request.Messages[1].Content, toolSequence[calls-1]) {
			t.Errorf("planner request %d omitted previous observation: %s", calls, request.Messages[1].Content)
		}
		var content string
		if calls < len(toolSequence) {
			arguments := `{}`
			if toolSequence[calls] == getExactCandidateChunksToolName {
				arguments = `{"offset":0,"limit":20}`
			}
			content = `{"type":"tool_call","tool_name":"` + toolSequence[calls] + `","arguments":` + arguments + `}`
		} else {
			content = `{"type":"final","final":"{\"status\":\"completed\",\"recommendation\":\"publish\",\"risk_level\":\"low\",\"summary\":\"reviewed\",\"findings\":[]}"}`
		}
		calls++
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"choices": []interface{}{map[string]interface{}{"message": map[string]string{"content": content}}}})
	}))
	defer server.Close()
	planner, err := NewLLMPlanner(LLMPlannerOptions{Endpoint: server.URL, Model: "review-model", Tools: registry.Definitions(), SystemPrompt: reviewPlannerSystemPrompt})
	if err != nil {
		t.Fatal(err)
	}
	orchestrator := newTestOrchestrator(t, runStore, registry, planner, 8)
	service := newServiceWithComponents(orchestrator, runStore)
	service.reviewOrchestrator = orchestrator
	service.reviewWorkflow = workflow
	service.reviewModel = "review-model"

	report, err := service.ReviewPublicationReport(context.Background(), agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Role: "admin", Permissions: []string{"agent"}}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 6 || report.Status != "completed" || report.Recommendation != "publish" || report.Model != "review-model" || report.PromptVersion != reviewPromptVersion {
		t.Fatalf("calls=%d report=%+v", calls, report)
	}
}

func TestNewReviewPlannerReusesRAGModelConfig(t *testing.T) {
	t.Setenv("LLM_ENDPOINT", "http://rag.example/v1")
	t.Setenv("LLM_MODEL", "rag-model")
	t.Setenv("LLM_API_KEY", "rag-key")
	registry := agent.NewRegistry()
	if err := registry.Register(readOnlyReviewTool(getReviewContextToolName, "context"), func(context.Context, agent.ToolInvocation) (agent.ToolResult, error) {
		return agent.ToolResult{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	planner, err := newReviewPlanner(config.Config{Environment: "development", AgentPlannerType: config.AgentPlannerAuto}, registry)
	if err != nil {
		t.Fatal(err)
	}
	constrained, ok := planner.(constrainedReviewPlanner)
	if !ok {
		t.Fatalf("planner=%T", planner)
	}
	llmPlanner, ok := constrained.inner.(*LLMPlanner)
	if !ok || llmPlanner.endpoint != "http://rag.example/v1/chat/completions" || llmPlanner.model != "rag-model" || llmPlanner.apiKey != "rag-key" {
		t.Fatalf("planner=%+v", planner)
	}
}

func TestValidateAutonomousReviewRejectsMissingToolAndUnknownEvidence(t *testing.T) {
	candidate := reviewCandidate()
	run := agent.Run{Steps: []agent.Step{
		reviewStep(1, getReviewContextToolName, candidate, nil),
		reviewStep(2, getExactCandidateChunksToolName, candidate, map[string]interface{}{"chunk_ids": []string{"chunk-1"}, "total": 1}),
		reviewStep(3, scanSensitiveDataToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{}}),
	}}
	report := releasecenter.AgentReview{Status: "completed", Recommendation: "publish", RiskLevel: releasecenter.RiskLow, Summary: "ok"}
	if _, _, err := validateAutonomousReview(run, report); err == nil {
		t.Fatal("expected missing prompt-injection scan to fail")
	}
	run.Steps = append(run.Steps, reviewStep(4, scanPromptInjectionToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{}}))
	run.Steps = append(run.Steps, reviewStep(5, assessKnowledgeFitnessToolName, candidate, reviewFitnessPublishExtra("chunk-1")))
	report.Findings = []releasecenter.Finding{{Code: "invented", Severity: "high", Summary: "invented", EvidenceRef: "unknown"}}
	normalized, _, err := validateAutonomousReview(run, report)
	if err != nil {
		t.Fatalf("unknown evidence should be dropped, got %v", err)
	}
	if len(normalized.Findings) != 0 {
		t.Fatalf("unknown evidence leaked into report: %+v", normalized.Findings)
	}
}

func TestValidateAutonomousReviewRestoresOmittedDeterministicFinding(t *testing.T) {
	candidate := reviewCandidate()
	finding := releasecenter.Finding{Code: "sensitive_data_detected", Severity: "high", Summary: "sensitive", EvidenceRef: "chunk-1"}
	run := agent.Run{Steps: []agent.Step{
		reviewStep(1, getReviewContextToolName, candidate, nil),
		reviewStep(2, getExactCandidateChunksToolName, candidate, map[string]interface{}{"chunk_ids": []string{"chunk-1"}, "total": 1}),
		reviewStep(3, scanSensitiveDataToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{finding}, "risk_level": releasecenter.RiskHigh, "recommendation": "needs_info"}),
		reviewStep(4, scanPromptInjectionToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{}, "risk_level": releasecenter.RiskLow, "recommendation": "publish"}),
		reviewStep(5, assessKnowledgeFitnessToolName, candidate, reviewFitnessPublishExtra("chunk-1")),
	}}
	report, _, err := validateAutonomousReview(run, releasecenter.AgentReview{Status: "completed", Recommendation: "publish", RiskLevel: releasecenter.RiskLow, Summary: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Recommendation != "needs_info" || report.RiskLevel != releasecenter.RiskHigh || len(report.Findings) != 1 || report.Findings[0].Code != "sensitive_data_detected" {
		t.Fatalf("deterministic floor was not restored: %+v", report)
	}
}

func TestAutonomousReviewFlagsInsufficientEvidenceContent(t *testing.T) {
	candidate := reviewCandidate()
	workflow := &fakePublicationWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	runStore := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	if err := registerReviewTools(registry, workflow,
		reviewDocumentStub{document: docstore.Document{TenantID: "tenant-a", DocID: candidate.DocumentID, Permission: "internal", KnowledgeSpaceID: "policies", Owner: "owner"}},
		reviewChunkStub{chunks: []store.StoredChunk{{ChunkID: "chunk-draft", TenantID: "tenant-a", DocID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, Content: "本页为占位稿，正式制度正文尚未提供。", Index: 0}}}, runStore); err != nil {
		t.Fatal(err)
	}
	orchestrator := newTestOrchestrator(t, runStore, registry, ReviewRulePlanner{}, 8)
	service := newServiceWithComponents(orchestrator, runStore)
	service.reviewOrchestrator = orchestrator
	service.reviewWorkflow = workflow
	report, err := service.ReviewPublicationReport(context.Background(), agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Role: "admin", Permissions: []string{"agent"}}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || report.Recommendation != "needs_info" || report.RiskLevel != releasecenter.RiskMedium || report.KnowledgeUsable != releasecenter.KnowledgeUseIncomplete || len(report.Findings) != 2 || report.Findings[0].Code != "insufficient_evidence" || report.Findings[1].Code != "incomplete_knowledge" {
		t.Fatalf("insufficient evidence was not flagged: %+v", report)
	}
}

func TestValidateAutonomousReviewRequiresAnInspectedChunk(t *testing.T) {
	candidate := reviewCandidate()
	finding := releasecenter.Finding{Code: "sensitive_data_detected", Severity: "high", Summary: "sensitive", EvidenceRef: "chunk-1"}
	run := agent.Run{Steps: []agent.Step{
		reviewStep(1, getReviewContextToolName, candidate, nil),
		reviewStep(2, getExactCandidateChunksToolName, candidate, map[string]interface{}{"chunk_ids": []string{}, "total": 1}),
		reviewStep(3, scanSensitiveDataToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{finding}, "risk_level": releasecenter.RiskHigh, "recommendation": "needs_info"}),
		reviewStep(4, scanPromptInjectionToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{}, "risk_level": releasecenter.RiskLow, "recommendation": "publish"}),
		reviewStep(5, assessKnowledgeFitnessToolName, candidate, reviewFitnessPublishExtra("chunk-1")),
	}}
	report := releasecenter.AgentReview{Status: "completed", Recommendation: "needs_info", RiskLevel: releasecenter.RiskHigh, Summary: "reviewed", Findings: []releasecenter.Finding{finding}}
	if _, _, err := validateAutonomousReview(run, report); err == nil {
		t.Fatal("expected review without an inspected chunk to fail")
	}
}

func TestValidateAutonomousReviewRejectsCandidateChange(t *testing.T) {
	candidate := reviewCandidate()
	changed := candidate
	changed.GenerationID = "generation-2"
	run := agent.Run{Steps: []agent.Step{
		reviewStep(1, getReviewContextToolName, candidate, nil),
		reviewStep(2, getExactCandidateChunksToolName, candidate, map[string]interface{}{"chunk_ids": []string{"chunk-1"}, "total": 1}),
		reviewStep(3, scanSensitiveDataToolName, changed, map[string]interface{}{"findings": []releasecenter.Finding{}}),
		reviewStep(4, scanPromptInjectionToolName, changed, map[string]interface{}{"findings": []releasecenter.Finding{}}),
	}}
	if _, _, err := validateAutonomousReview(run, releasecenter.AgentReview{Status: "completed", Recommendation: "publish", RiskLevel: releasecenter.RiskLow, Summary: "ok"}); err == nil {
		t.Fatal("expected candidate change to fail")
	}
}

type reviewPlannerFunc func(context.Context, agent.Run) (agent.PlanDecision, error)

func (f reviewPlannerFunc) Plan(ctx context.Context, run agent.Run) (agent.PlanDecision, error) {
	return f(ctx, run)
}

func TestValidateAutonomousReviewRejectsUngroundedBlockWithoutFindings(t *testing.T) {
	candidate := reviewCandidate()
	run := agent.Run{Steps: []agent.Step{
		reviewStep(1, getReviewContextToolName, candidate, nil),
		reviewStep(2, getExactCandidateChunksToolName, candidate, map[string]interface{}{"chunk_ids": []string{"chunk-1"}, "total": 1}),
		reviewStep(3, scanSensitiveDataToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{}, "risk_level": releasecenter.RiskLow, "recommendation": "publish"}),
		reviewStep(4, scanPromptInjectionToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{}, "risk_level": releasecenter.RiskLow, "recommendation": "publish"}),
		reviewStep(5, assessKnowledgeFitnessToolName, candidate, reviewFitnessPublishExtra("chunk-1")),
	}}
	report, _, err := validateAutonomousReview(run, releasecenter.AgentReview{Status: "completed", Recommendation: "needs_info", RiskLevel: releasecenter.RiskMedium, Summary: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Recommendation != "publish" || report.RiskLevel != releasecenter.RiskLow || len(report.Findings) != 0 {
		t.Fatalf("ungrounded block was not cleared: %+v", report)
	}
}

func TestConstrainedReviewPlannerRedirectsPrematureFinal(t *testing.T) {
	inner := reviewPlannerFunc(func(context.Context, agent.Run) (agent.PlanDecision, error) {
		return agent.PlanDecision{Type: agent.DecisionFinal, Final: `{"status":"completed","recommendation":"publish","risk_level":"low","summary":"ok","findings":[]}`, Usage: agent.PlanUsage{PromptTokens: 4, CompletionTokens: 2}}, nil
	})
	decision, err := constrainedReviewPlanner{inner: inner}.Plan(context.Background(), agent.Run{Steps: []agent.Step{
		reviewStep(1, getExactCandidateChunksToolName, reviewCandidate(), map[string]interface{}{"chunk_ids": []string{"chunk-1"}}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Type != agent.DecisionToolCall || decision.ToolName != getReviewContextToolName || decision.Usage.PromptTokens != 4 {
		t.Fatalf("premature final was not redirected: %+v", decision)
	}
}

func TestConstrainedReviewPlannerKeepsRemainingRequiredToolChoice(t *testing.T) {
	inner := reviewPlannerFunc(func(context.Context, agent.Run) (agent.PlanDecision, error) {
		return agent.PlanDecision{Type: agent.DecisionToolCall, ToolName: scanSensitiveDataToolName, Arguments: json.RawMessage(`{}`)}, nil
	})
	decision, err := constrainedReviewPlanner{inner: inner}.Plan(context.Background(), agent.Run{Steps: []agent.Step{
		reviewStep(1, getReviewContextToolName, reviewCandidate(), nil),
		reviewStep(2, getExactCandidateChunksToolName, reviewCandidate(), map[string]interface{}{"chunk_ids": []string{"chunk-1"}}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Type != agent.DecisionToolCall || decision.ToolName != scanSensitiveDataToolName {
		t.Fatalf("remaining tool choice was overwritten: %+v", decision)
	}
}

func reviewFitnessPublishExtra(chunkID string) map[string]interface{} {
	if chunkID == "" {
		chunkID = "chunk-1"
	}
	return map[string]interface{}{
		"space_fit": "", "knowledge_usable": releasecenter.KnowledgeUseUsable,
		"findings": []releasecenter.Finding{}, "risk_level": releasecenter.RiskLow,
		"recommendation": "publish", "chunk_ids": []string{chunkID},
	}
}

type reviewSpaceStub struct {
	space knowledgecatalog.Space
	found bool
}

func (s reviewSpaceStub) Space(context.Context, string, string) (knowledgecatalog.Space, bool, error) {
	return s.space, s.found, nil
}

func reviewCandidate() publicationworkflow.Candidate {

	return publicationworkflow.Candidate{DocumentID: "doc-1", DocumentVersionID: "version-1", GenerationID: "generation-1", ExpectedChunkCount: 1, ExpectedChunkDigest: "sha256:digest", ReleaseRevision: 1}
}

// flakyReviewPlanner fails its first `failures` planning calls and then behaves
// like the deterministic rule planner. It models a transient planner outage (a
// gateway timeout or a 5xx) rather than a permanently unreviewable document.
type flakyReviewPlanner struct {
	failures int
	calls    int
	inner    ReviewRulePlanner
}

func (p *flakyReviewPlanner) Plan(ctx context.Context, run agent.Run) (agent.PlanDecision, error) {
	p.calls++
	if p.calls <= p.failures {
		return agent.PlanDecision{}, fmt.Errorf("planner request failed: context deadline exceeded")
	}
	return p.inner.Plan(ctx, run)
}

func newReviewTestService(t *testing.T, planner agent.Planner) (*Service, agent.Store, publicationworkflow.Candidate, agent.Actor) {
	t.Helper()
	candidate := reviewCandidate()
	workflow := &fakePublicationWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	runStore := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	if err := registerReviewTools(registry, workflow,
		reviewDocumentStub{document: docstore.Document{TenantID: "tenant-a", DocID: candidate.DocumentID, Permission: "internal", KnowledgeSpaceID: "policies", Owner: "owner"}},
		reviewChunkStub{chunks: []store.StoredChunk{{ChunkID: "chunk-1", TenantID: "tenant-a", DocID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, Content: "普通制度内容", Index: 0}}}, runStore); err != nil {
		t.Fatal(err)
	}
	orchestrator := newTestOrchestrator(t, runStore, registry, planner, 8)
	service := newServiceWithComponents(orchestrator, runStore)
	service.reviewOrchestrator = orchestrator
	service.reviewWorkflow = workflow
	return service, runStore, candidate, agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Role: "admin", Permissions: []string{"agent"}}
}

// TestAutonomousReviewRetriesTransientFailureInAFreshRun pins the recovery path
// for a transient planner outage.
//
// The review run used to be keyed on the exact candidate alone. A run that
// reached StateFailed is terminal and ExecuteNext short-circuits on it, so
// resuming that run replayed the recorded error without ever asking the planner
// again: the document stayed in manual_exception forever and the automatic
// re-review path burned a review TTL per attempt without re-running the Agent.
// A retry must therefore start a new run and leave the failed one intact as
// evidence.
func TestAutonomousReviewRetriesTransientFailureInAFreshRun(t *testing.T) {
	planner := &flakyReviewPlanner{failures: 1}
	service, runStore, candidate, actor := newReviewTestService(t, planner)

	first, err := service.ReviewPublicationReport(context.Background(), actor, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "failed" {
		t.Fatalf("expected the transient planner failure to surface as a failed review: %+v", first)
	}
	failed, err := runStore.LoadRun(context.Background(), first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != agent.StateFailed {
		t.Fatalf("first attempt state=%q", failed.State)
	}

	// The planner is healthy again.
	second, err := service.ReviewPublicationReport(context.Background(), actor, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "completed" || second.Recommendation != "publish" {
		t.Fatalf("retry did not recover: %+v", second)
	}
	if second.RunID == first.RunID {
		t.Fatalf("retry replayed the failed run %q instead of starting a new attempt", second.RunID)
	}
	recovered, err := runStore.LoadRun(context.Background(), second.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.State != agent.StateCompleted {
		t.Fatalf("retry run state=%q", recovered.State)
	}
	if preserved, err := runStore.LoadRun(context.Background(), first.RunID); err != nil || preserved.State != agent.StateFailed {
		t.Fatalf("failed attempt was overwritten: run=%+v err=%v", preserved, err)
	}
}

// TestAutonomousReviewStopsRetryingAfterMaxAttempts keeps the retry bounded: a
// permanently broken planner must not spend tokens on every queue tick.
func TestAutonomousReviewStopsRetryingAfterMaxAttempts(t *testing.T) {
	planner := &flakyReviewPlanner{failures: 1000}
	service, runStore, candidate, actor := newReviewTestService(t, planner)
	service.reviewMaxAttempts = 2

	for round := 0; round < 3; round++ {
		report, err := service.ReviewPublicationReport(context.Background(), actor, candidate.DocumentID)
		if err != nil {
			t.Fatal(err)
		}
		if report.Status != "failed" {
			t.Fatalf("round %d: report=%+v", round, report)
		}
	}
	if planner.calls != 2 {
		t.Fatalf("planner calls=%d, want 2 (one per allowed attempt, none after the budget is spent)", planner.calls)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		runID := reviewRunID(actor.TenantID, candidate, reviewPromptVersion, attempt, "")
		if _, err := runStore.LoadRun(context.Background(), runID); err != nil {
			t.Fatalf("attempt %d run %q missing: %v", attempt, runID, err)
		}
	}
	if _, err := runStore.LoadRun(context.Background(), reviewRunID(actor.TenantID, candidate, reviewPromptVersion, 3, "")); err == nil {
		t.Fatal("a third attempt run was created beyond AGENT_REVIEW_MAX_ATTEMPTS")
	}
}

type usagePlannerForReview struct {
	decision agent.PlanDecision
	usage    agent.PlanUsage
}

func (p *usagePlannerForReview) Plan(context.Context, agent.Run) (agent.PlanDecision, error) {
	decision := p.decision
	decision.Usage = p.usage
	return decision, nil
}

func reviewStep(index int, toolName string, candidate publicationworkflow.Candidate, extra map[string]interface{}) agent.Step {
	data := map[string]interface{}{"candidate": structMap(candidate)}
	for key, value := range extra {
		data[key] = value
	}
	encoded, _ := json.Marshal(data)
	return agent.Step{Index: index, Type: agent.StepToolCall, State: agent.StateCompleted, ToolName: toolName, Observation: string(encoded), ToolResult: &agent.ToolResult{Content: string(encoded), Data: data}}
}

func TestConstrainedReviewPlannerRedirectsFinalUntilFitness(t *testing.T) {
	inner := reviewPlannerFunc(func(context.Context, agent.Run) (agent.PlanDecision, error) {
		return agent.PlanDecision{Type: agent.DecisionFinal, Final: `{"status":"completed","recommendation":"publish","risk_level":"low","summary":"ok","findings":[]}`}, nil
	})
	candidate := reviewCandidate()
	decision, err := constrainedReviewPlanner{inner: inner}.Plan(context.Background(), agent.Run{Steps: []agent.Step{
		reviewStep(1, getReviewContextToolName, candidate, nil),
		reviewStep(2, getExactCandidateChunksToolName, candidate, map[string]interface{}{"chunk_ids": []string{"chunk-1"}}),
		reviewStep(3, scanSensitiveDataToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{}}),
		reviewStep(4, scanPromptInjectionToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{}}),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Type != agent.DecisionToolCall || decision.ToolName != assessKnowledgeFitnessToolName {
		t.Fatalf("fitness check was skipped: %+v", decision)
	}
}

func TestValidateAutonomousReviewFloorsPublishWhenSpaceMismatches(t *testing.T) {
	candidate := reviewCandidate()
	finding := releasecenter.Finding{Code: "space_mismatch", Severity: "medium", Summary: "材料不适合进入当前知识空间", EvidenceRef: "chunk-1"}
	run := agent.Run{Steps: []agent.Step{
		reviewStep(1, getReviewContextToolName, candidate, nil),
		reviewStep(2, getExactCandidateChunksToolName, candidate, map[string]interface{}{"chunk_ids": []string{"chunk-1"}, "total": 1}),
		reviewStep(3, scanSensitiveDataToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{}, "risk_level": releasecenter.RiskLow, "recommendation": "publish"}),
		reviewStep(4, scanPromptInjectionToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{}, "risk_level": releasecenter.RiskLow, "recommendation": "publish"}),
		reviewStep(5, assessKnowledgeFitnessToolName, candidate, map[string]interface{}{
			"space_fit": releasecenter.SpaceFitMismatch, "knowledge_usable": releasecenter.KnowledgeUseUsable,
			"findings": []releasecenter.Finding{finding}, "risk_level": releasecenter.RiskMedium, "recommendation": "needs_info",
			"chunk_ids": []string{"chunk-1"},
		}),
	}}
	report, _, err := validateAutonomousReview(run, releasecenter.AgentReview{Status: "completed", Recommendation: "publish", RiskLevel: releasecenter.RiskLow, Summary: "ok", KindLabel: "制度"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Recommendation != "needs_info" || report.RiskLevel != releasecenter.RiskMedium || report.SpaceFit != releasecenter.SpaceFitMismatch || len(report.Findings) != 1 || report.Findings[0].Code != "space_mismatch" {
		t.Fatalf("space mismatch was not floored: %+v", report)
	}
}

func TestValidateAutonomousReviewKeepsSensitiveFloorWhenFitnessMatches(t *testing.T) {
	candidate := reviewCandidate()
	finding := releasecenter.Finding{Code: "sensitive_data_detected", Severity: "high", Summary: "sensitive", EvidenceRef: "chunk-1"}
	run := agent.Run{Steps: []agent.Step{
		reviewStep(1, getReviewContextToolName, candidate, nil),
		reviewStep(2, getExactCandidateChunksToolName, candidate, map[string]interface{}{"chunk_ids": []string{"chunk-1"}, "total": 1}),
		reviewStep(3, scanSensitiveDataToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{finding}, "risk_level": releasecenter.RiskHigh, "recommendation": "needs_info"}),
		reviewStep(4, scanPromptInjectionToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{}, "risk_level": releasecenter.RiskLow, "recommendation": "publish"}),
		reviewStep(5, assessKnowledgeFitnessToolName, candidate, map[string]interface{}{
			"space_fit": releasecenter.SpaceFitMatch, "knowledge_usable": releasecenter.KnowledgeUseUsable,
			"kind_label": "制度", "findings": []releasecenter.Finding{}, "risk_level": releasecenter.RiskLow, "recommendation": "publish",
			"chunk_ids": []string{"chunk-1"},
		}),
	}}
	report, _, err := validateAutonomousReview(run, releasecenter.AgentReview{Status: "completed", Recommendation: "publish", RiskLevel: releasecenter.RiskLow, Summary: "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Recommendation != "needs_info" || report.RiskLevel != releasecenter.RiskHigh || report.SpaceFit != releasecenter.SpaceFitMatch || report.KindLabel != "制度" || len(report.Findings) != 1 || report.Findings[0].Code != "sensitive_data_detected" {
		t.Fatalf("sensitive floor was weakened by fitness: %+v", report)
	}
}

// The language contract's own tests live in releasecenter, next to the stored
// type and the rule: the write path here and the read path in the API both call
// the same function, so a test of the rule belongs with it rather than twice,
// once per caller. What stays here is what only the write path can show -- that
// validation applies the rule to the verdict it assembles.
func TestValidateAutonomousReviewKeepsDeterministicWordingOverPlannerParaphrase(t *testing.T) {
	// The planner is told to copy deterministic findings verbatim. It paraphrased
	// this one instead, and the old merge kept the paraphrase because it compared
	// severity only. The scan's own sentence has to win.
	candidate := reviewCandidate()
	run := agent.Run{Steps: []agent.Step{
		reviewStep(1, getReviewContextToolName, candidate, nil),
		reviewStep(2, getExactCandidateChunksToolName, candidate, map[string]interface{}{"chunk_ids": []string{"chunk-1"}, "total": 1}),
		reviewStep(3, scanSensitiveDataToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{}, "risk_level": releasecenter.RiskLow, "recommendation": "publish"}),
		reviewStep(4, scanPromptInjectionToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{}, "risk_level": releasecenter.RiskLow, "recommendation": "publish"}),
		reviewStep(5, assessKnowledgeFitnessToolName, candidate, map[string]interface{}{
			"space_fit": releasecenter.SpaceFitMismatch, "knowledge_usable": releasecenter.KnowledgeUseNotKnowledge,
			"kind_label": "Test/placeholder marker string with a gate passphrase; not an approved HR, admin",
			"findings": []releasecenter.Finding{
				{Code: "space_mismatch", Severity: "medium", Summary: "材料不适合进入当前知识空间", EvidenceRef: "chunk-1"},
				{Code: "not_knowledge", Severity: "medium", Summary: "材料不能作为正式知识使用", EvidenceRef: "chunk-1"},
			},
			"risk_level": releasecenter.RiskMedium, "recommendation": "needs_info", "chunk_ids": []string{"chunk-1"},
		}),
	}}
	report, _, err := validateAutonomousReview(run, releasecenter.AgentReview{
		Status: "completed", Recommendation: "needs_info", RiskLevel: releasecenter.RiskMedium,
		Summary: "Candidate chunk chunk-1 contains only a test/placeholder marker string rather than an " +
			"approved HR regulation for the production knowledge space.",
		Findings: []releasecenter.Finding{
			{Code: "space_mismatch", Severity: "medium", Summary: "The material does not belong in this knowledge space", EvidenceRef: "chunk-1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.KindLabel != "" {
		t.Fatalf("an English kind_label survived validation: %q", report.KindLabel)
	}
	if report.Summary != "预审未通过，需补充材料或人工确认后重审，共 2 项待确认问题。" {
		t.Fatalf("an English summary survived validation: %q", report.Summary)
	}
	if len(report.Findings) != 2 {
		t.Fatalf("expected the deterministic space_mismatch and not_knowledge findings: %+v", report.Findings)
	}
	for _, finding := range report.Findings {
		if releasecenter.LooksLikeEnglish(finding.Summary) {
			t.Fatalf("an English finding summary survived validation: %+v", finding)
		}
	}
	if report.Findings[0].Summary != "材料不适合进入当前知识空间" {
		t.Fatalf("the deterministic wording must win over the paraphrase, got %q", report.Findings[0].Summary)
	}
}

func TestAutonomousReviewRulePlannerBlocksMismatchedSpace(t *testing.T) {
	candidate := reviewCandidate()
	workflow := &fakePublicationWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	runStore := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	spaces := reviewSpaceStub{found: true, space: knowledgecatalog.Space{ID: "policies", TenantID: "tenant-a", Name: "制度库", Purpose: "存放已生效的劳动合同、合规条款和法务批复", Kind: knowledgecatalog.SpaceKindProduction, Active: true}}
	if err := registerReviewTools(registry, workflow,
		reviewDocumentStub{document: docstore.Document{TenantID: "tenant-a", DocID: candidate.DocumentID, Permission: "internal", KnowledgeSpaceID: "policies", Owner: "owner"}},
		reviewChunkStub{chunks: []store.StoredChunk{{ChunkID: "chunk-1", TenantID: "tenant-a", DocID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, Content: "红烧肉烹饪说明。主料：五花肉、冰糖、生抽。步骤：炒糖色后小火炖煮至软烂。", Index: 0}}}, runStore, spaces); err != nil {
		t.Fatal(err)
	}
	orchestrator := newTestOrchestrator(t, runStore, registry, ReviewRulePlanner{}, 8)
	service := newServiceWithComponents(orchestrator, runStore)
	service.reviewOrchestrator = orchestrator
	service.reviewWorkflow = workflow
	report, err := service.ReviewPublicationReport(context.Background(), agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Role: "admin", Permissions: []string{"agent"}}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || report.Recommendation != "needs_info" || report.SpaceFit != releasecenter.SpaceFitMismatch || len(report.Findings) == 0 || report.Findings[0].Code != "space_mismatch" {
		t.Fatalf("mismatched space should not publish: %+v", report)
	}
}

func TestAutonomousReviewRulePlannerPublishesWhenPurposeMatches(t *testing.T) {
	candidate := reviewCandidate()
	workflow := &fakePublicationWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	runStore := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	spaces := reviewSpaceStub{found: true, space: knowledgecatalog.Space{ID: "policies", TenantID: "tenant-a", Name: "行政制度", Purpose: "存放已生效的差旅报销与行政办公制度", Kind: knowledgecatalog.SpaceKindProduction, Active: true}}
	if err := registerReviewTools(registry, workflow,
		reviewDocumentStub{document: docstore.Document{TenantID: "tenant-a", DocID: candidate.DocumentID, Permission: "internal", KnowledgeSpaceID: "policies", Owner: "owner"}},
		reviewChunkStub{chunks: []store.StoredChunk{{ChunkID: "chunk-1", TenantID: "tenant-a", DocID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, Content: "差旅报销制度适用于全体正式员工。出差前须提交申请，报销时须提供发票和部门经理审批记录。", Index: 0}}}, runStore, spaces); err != nil {
		t.Fatal(err)
	}
	orchestrator := newTestOrchestrator(t, runStore, registry, ReviewRulePlanner{}, 8)
	service := newServiceWithComponents(orchestrator, runStore)
	service.reviewOrchestrator = orchestrator
	service.reviewWorkflow = workflow
	report, err := service.ReviewPublicationReport(context.Background(), agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Role: "admin", Permissions: []string{"agent"}}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || report.Recommendation != "publish" || report.SpaceFit != releasecenter.SpaceFitMatch || report.KnowledgeUsable != releasecenter.KnowledgeUseUsable {
		t.Fatalf("matching purpose should keep publish path: %+v", report)
	}
}

func TestAutonomousReviewRulePlannerBlocksInformalMaterial(t *testing.T) {
	candidate := reviewCandidate()
	workflow := &fakePublicationWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	runStore := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	if err := registerReviewTools(registry, workflow,
		reviewDocumentStub{document: docstore.Document{TenantID: "tenant-a", DocID: candidate.DocumentID, Permission: "internal", KnowledgeSpaceID: "policies", Owner: "owner"}},
		reviewChunkStub{chunks: []store.StoredChunk{{ChunkID: "chunk-1", TenantID: "tenant-a", DocID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, Content: "张三：晚上团建去哪吃？\n李四：随便，烧烤吧。\n王五：行，那我订位置。\n这是即时通讯聊天记录，不能当成可检索的业务知识使用。", Index: 0}}}, runStore); err != nil {
		t.Fatal(err)
	}
	orchestrator := newTestOrchestrator(t, runStore, registry, ReviewRulePlanner{}, 8)
	service := newServiceWithComponents(orchestrator, runStore)
	service.reviewOrchestrator = orchestrator
	service.reviewWorkflow = workflow
	report, err := service.ReviewPublicationReport(context.Background(), agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Role: "admin", Permissions: []string{"agent"}}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || report.Recommendation != "needs_info" || report.KnowledgeUsable != releasecenter.KnowledgeUseNotKnowledge || len(report.Findings) == 0 || report.Findings[0].Code != "not_knowledge" {
		t.Fatalf("informal material should not publish: %+v", report)
	}
}

func TestReviewRuleFitnessArgumentsFromObservations(t *testing.T) {
	candidate := reviewCandidate()
	run := agent.Run{Steps: []agent.Step{
		reviewStep(1, getReviewContextToolName, candidate, map[string]interface{}{"knowledge_space_purpose": "存放已生效的劳动合同、合规条款和法务批复"}),
		reviewStep(2, getExactCandidateChunksToolName, candidate, map[string]interface{}{
			"chunk_ids": []string{"chunk-1"},
			"chunks":    []map[string]interface{}{{"chunk_id": "chunk-1", "content": "红烧肉烹饪说明。主料：五花肉、冰糖、生抽。"}},
		}),
		reviewStep(3, scanSensitiveDataToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{}}),
	}}
	var args map[string]interface{}
	if err := json.Unmarshal(reviewRuleFitnessArguments(run), &args); err != nil {
		t.Fatal(err)
	}
	if args["space_fit"] != releasecenter.SpaceFitMismatch || args["knowledge_usable"] != releasecenter.KnowledgeUseUsable {
		t.Fatalf("expected mismatch usable fitness args: %+v", args)
	}
	refs, _ := args["evidence_chunk_ids"].([]interface{})
	if len(refs) != 1 || refs[0] != "chunk-1" {
		t.Fatalf("expected exact chunk evidence: %+v", args)
	}
}

func TestAutonomousReviewRulePlannerAllowsPublishWhenPurposeMissing(t *testing.T) {
	candidate := reviewCandidate()
	workflow := &fakePublicationWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	runStore := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	if err := registerReviewTools(registry, workflow,
		reviewDocumentStub{document: docstore.Document{TenantID: "tenant-a", DocID: candidate.DocumentID, Permission: "internal", KnowledgeSpaceID: "policies", Owner: "owner"}},
		reviewChunkStub{chunks: []store.StoredChunk{{ChunkID: "chunk-1", TenantID: "tenant-a", DocID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, Content: "普通制度内容", Index: 0}}}, runStore); err != nil {
		t.Fatal(err)
	}
	orchestrator := newTestOrchestrator(t, runStore, registry, ReviewRulePlanner{}, 8)
	service := newServiceWithComponents(orchestrator, runStore)
	service.reviewOrchestrator = orchestrator
	service.reviewWorkflow = workflow
	report, err := service.ReviewPublicationReport(context.Background(), agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Role: "admin", Permissions: []string{"agent"}}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || report.Recommendation != "publish" || report.KnowledgeUsable != releasecenter.KnowledgeUseUsable {
		t.Fatalf("unconfigured space should keep existing publish path: %+v", report)
	}
}

// A review verdict is a cache of "this candidate, this prompt, this model". The
// model is the one input the report names as its own provenance, so a change to
// it has to fork the identity -- otherwise pointing LLM_MODEL elsewhere within
// AGENT_RUN_TTL reuses the old verdict and never asks the new model.
func TestReviewRunIDSeparatesReviewers(t *testing.T) {
	candidate := reviewCandidate()
	base := reviewRunID("tenant-a", candidate, reviewPromptVersion, 1, "model-a")
	if base != reviewRunID("tenant-a", candidate, reviewPromptVersion, 1, "model-a") {
		t.Fatal("the same reviewer must derive the same run id")
	}
	if base == reviewRunID("tenant-a", candidate, reviewPromptVersion, 1, "model-b") {
		t.Fatal("a different review model must derive a different run id")
	}
	if base != reviewRunID("tenant-a", candidate, reviewPromptVersion, 1, "  model-a  ") {
		t.Fatal("surrounding whitespace must not fork the identity")
	}
	if base == reviewRunID("tenant-a", candidate, reviewPromptVersion, 2, "model-a") {
		t.Fatal("the attempt index must stay part of the identity")
	}
	if base == reviewRunID("tenant-b", candidate, reviewPromptVersion, 1, "model-a") {
		t.Fatal("the tenant must stay part of the identity")
	}
	// Derived from the constant on purpose. Hardcoding a literal here made the
	// assertion invert the day the prompt version was bumped to that literal:
	// "a different version forks the id" became "the same version forks the id".
	if base == reviewRunID("tenant-a", candidate, reviewPromptVersion+"-next", 1, "model-a") {
		t.Fatal("the prompt version must stay part of the identity")
	}
}

func TestReviewModelFromRunDoesNotFabricateAModel(t *testing.T) {
	if got := reviewModelFromRun(agent.Run{}); got != "" {
		t.Fatalf("a run with no memory must report no model, got %q", got)
	}
	if got := reviewModelFromRun(agent.Run{Memory: map[string]interface{}{"review_candidate": map[string]interface{}{}}}); got != "" {
		t.Fatalf("a run that never recorded a model must report none, got %q", got)
	}
	if got := reviewModelFromRun(agent.Run{Memory: map[string]interface{}{reviewModelMemoryKey: "  model-a  "}}); got != "model-a" {
		t.Fatalf("recorded model=%q", got)
	}
	if got := reviewModelFromRun(agent.Run{Memory: map[string]interface{}{reviewModelMemoryKey: 7}}); got != "" {
		t.Fatalf("a non-string model must report none, got %q", got)
	}
}

// The two halves of the defect, in one test: a verdict keeps naming the model
// that produced it, and a new model gets a new verdict instead of the cached one.
func TestReviewVerdictKeepsItsModelAndANewModelGetsANewVerdict(t *testing.T) {
	candidate := reviewCandidate()
	workflow := &fakePublicationWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	runStore := agent.NewMemoryStore()
	registry := agent.NewRegistry()
	if err := registerReviewTools(registry, workflow,
		reviewDocumentStub{document: docstore.Document{TenantID: "tenant-a", DocID: candidate.DocumentID, Permission: "internal", KnowledgeSpaceID: "policies", Owner: "owner"}},
		reviewChunkStub{chunks: []store.StoredChunk{{ChunkID: "chunk-1", TenantID: "tenant-a", DocID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, Content: "普通制度内容", Index: 0}}}, runStore); err != nil {
		t.Fatal(err)
	}
	actor := agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Role: "admin", Permissions: []string{"agent"}}
	orchestrator := newTestOrchestrator(t, runStore, registry, ReviewRulePlanner{}, 8)
	service := newServiceWithComponents(orchestrator, runStore)
	service.reviewOrchestrator = orchestrator
	service.reviewWorkflow = workflow
	service.reviewModel = "model-a"

	first, err := service.ReviewPublicationReport(context.Background(), actor, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "completed" || first.Model != "model-a" {
		t.Fatalf("first review=%+v", first)
	}

	// An operator repoints LLM_MODEL at another model inside AGENT_RUN_TTL.
	service.reviewModel = "model-b"

	// Reading the old verdict back must not relabel it with the new model.
	replayed, err := service.ResumePublicationReport(context.Background(), actor, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Model != "model-a" {
		t.Fatalf("verdict produced by model-a was relabelled as %q", replayed.Model)
	}

	// And the new model must actually get asked: a fresh run, not the cached one.
	second, err := service.ReviewPublicationReport(context.Background(), actor, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if second.RunID == first.RunID {
		t.Fatal("changing the review model must not reuse the cached verdict")
	}
	if second.Model != "model-b" {
		t.Fatalf("second review=%+v", second)
	}
}

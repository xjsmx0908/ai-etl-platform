package agentapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-etl-pipeline/internal/agent"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/docstore"
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
	if len(run.Steps) != 5 {
		t.Fatalf("steps=%+v", run.Steps)
	}
	wantTools := []string{getReviewContextToolName, getExactCandidateChunksToolName, scanSensitiveDataToolName, scanPromptInjectionToolName}
	for index, toolName := range wantTools {
		if run.Steps[index].ToolName != toolName || run.Steps[index].Observation == "" {
			t.Fatalf("step %d=%+v", index, run.Steps[index])
		}
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
	report, err := service.ReviewPublicationReport(context.Background(), agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Role: "admin", Permissions: []string{"agent"}}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "failed" || report.Recommendation != "manual_review" || report.RiskLevel != releasecenter.RiskHigh {
		t.Fatalf("incomplete candidate was not failed closed: %+v", report)
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
	toolSequence := []string{getReviewContextToolName, getExactCandidateChunksToolName, scanSensitiveDataToolName, scanPromptInjectionToolName}
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
	service.reviewModel = "review-model"

	report, err := service.ReviewPublicationReport(context.Background(), agent.Actor{TenantID: "tenant-a", UserID: "review-agent", Role: "admin", Permissions: []string{"agent"}}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 5 || report.Status != "completed" || report.Recommendation != "publish" || report.Model != "review-model" || report.PromptVersion != reviewPromptVersion {
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
	llmPlanner, ok := planner.(*LLMPlanner)
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
	if _, err := validateAutonomousReview(run, report); err == nil {
		t.Fatal("expected missing prompt-injection scan to fail")
	}
	run.Steps = append(run.Steps, reviewStep(4, scanPromptInjectionToolName, candidate, map[string]interface{}{"findings": []releasecenter.Finding{}}))
	report.Findings = []releasecenter.Finding{{Code: "invented", Severity: "high", Summary: "invented", EvidenceRef: "unknown"}}
	if _, err := validateAutonomousReview(run, report); err == nil {
		t.Fatal("expected unknown evidence to fail")
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
	}}
	report := releasecenter.AgentReview{Status: "completed", Recommendation: "needs_info", RiskLevel: releasecenter.RiskHigh, Summary: "reviewed", Findings: []releasecenter.Finding{finding}}
	if _, err := validateAutonomousReview(run, report); err == nil {
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
	if _, err := validateAutonomousReview(run, releasecenter.AgentReview{Status: "completed", Recommendation: "publish", RiskLevel: releasecenter.RiskLow, Summary: "ok"}); err == nil {
		t.Fatal("expected candidate change to fail")
	}
}

func reviewCandidate() publicationworkflow.Candidate {
	return publicationworkflow.Candidate{DocumentID: "doc-1", DocumentVersionID: "version-1", GenerationID: "generation-1", ExpectedChunkCount: 1, ExpectedChunkDigest: "sha256:digest", ReleaseRevision: 1}
}

func reviewStep(index int, toolName string, candidate publicationworkflow.Candidate, extra map[string]interface{}) agent.Step {
	data := map[string]interface{}{"candidate": structMap(candidate)}
	for key, value := range extra {
		data[key] = value
	}
	encoded, _ := json.Marshal(data)
	return agent.Step{Index: index, Type: agent.StepToolCall, State: agent.StateCompleted, ToolName: toolName, Observation: string(encoded), ToolResult: &agent.ToolResult{Content: string(encoded), Data: data}}
}

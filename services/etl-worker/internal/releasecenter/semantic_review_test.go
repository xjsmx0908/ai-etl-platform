package releasecenter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-etl-pipeline/internal/publicationworkflow"
)

func semanticInput() SemanticReviewInput {
	return SemanticReviewInput{
		TenantID: "acme", DocumentID: "doc-1", KnowledgeSpaceID: "production", Permission: "internal",
		Candidate: publicationworkflow.Candidate{DocumentID: "doc-1", DocumentVersionID: "version-1", GenerationID: "generation-1", ExpectedChunkCount: 1, ExpectedChunkDigest: "sha256:digest", ReleaseRevision: 1},
		Chunks:    []ContentChunk{{ChunkID: "chunk-1", Content: "文档正文"}},
	}
}

func TestHTTPSemanticReviewerReturnsValidatedStructuredResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("authorization header missing")
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		response := map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"status":"completed","recommendation":"needs_info","risk_level":"high","summary":"发现合规风险","findings":[{"code":"retention_risk","severity":"high","summary":"缺少保留期限","evidence_ref":"chunk-1"}]}`}}}}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()
	reviewer, err := NewHTTPSemanticReviewer(SemanticReviewerOptions{Endpoint: server.URL, APIKey: "test-key", Model: "review-model"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := reviewer.Review(context.Background(), semanticInput())
	if err != nil {
		t.Fatal(err)
	}
	if result.Recommendation != "needs_info" || result.RiskLevel != RiskHigh || result.Model != "review-model" || len(result.Findings) != 1 {
		t.Fatalf("result=%+v", result)
	}
}

func TestValidateSemanticReviewResultRejectsUnknownEvidence(t *testing.T) {
	result := SemanticReviewResult{Status: "completed", Recommendation: "publish", RiskLevel: RiskLow, Summary: "通过", Model: "model", PromptVersion: "prompt", Findings: []Finding{{Code: "risk", Severity: "high", Summary: "风险", EvidenceRef: "unknown"}}}
	if err := ValidateSemanticReviewResult(semanticInput(), result); err == nil || !strings.Contains(err.Error(), "unknown chunk") {
		t.Fatalf("expected unknown evidence rejection, got %v", err)
	}
}

func TestValidateSemanticReviewInputRejectsDuplicateChunks(t *testing.T) {
	input := semanticInput()
	input.Chunks = append(input.Chunks, input.Chunks[0])
	if err := ValidateSemanticReviewInput(input); err == nil || !strings.Contains(err.Error(), "duplicate chunk") {
		t.Fatalf("expected duplicate chunk rejection, got %v", err)
	}
}

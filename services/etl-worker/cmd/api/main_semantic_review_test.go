package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"ai-etl-pipeline/internal/publicationworkflow"
	"ai-etl-pipeline/internal/releasecenter"
)

func TestConfigureSemanticReviewerReusesRAGModelConfig(t *testing.T) {
	server := semanticReviewTestServer(t, "rag-model", "rag-key")
	defer server.Close()
	setSemanticReviewEnv(t, server.URL, "rag-model", "rag-key")

	reviewer, err := configureSemanticReviewer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviewer.Review(context.Background(), semanticReviewTestInput()); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureSemanticReviewerAllowsDedicatedOverrides(t *testing.T) {
	server := semanticReviewTestServer(t, "dedicated-model", "dedicated-key")
	defer server.Close()
	setSemanticReviewEnv(t, "http://rag.example/v1/chat/completions", "rag-model", "rag-key")
	t.Setenv("AGENT_SEMANTIC_REVIEW_ENDPOINT", server.URL)
	t.Setenv("AGENT_SEMANTIC_REVIEW_MODEL", "dedicated-model")
	t.Setenv("AGENT_SEMANTIC_REVIEW_API_KEY", "dedicated-key")

	reviewer, err := configureSemanticReviewer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviewer.Review(context.Background(), semanticReviewTestInput()); err != nil {
		t.Fatal(err)
	}
}

func TestConfigureSemanticReviewerReusesRAGAPIKeyFile(t *testing.T) {
	server := semanticReviewTestServer(t, "rag-model", "file-key")
	defer server.Close()
	keyFile := filepath.Join(t.TempDir(), "llm-api-key")
	if err := os.WriteFile(keyFile, []byte("file-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	setSemanticReviewEnv(t, server.URL, "rag-model", "")
	t.Setenv("LLM_API_KEY_FILE", keyFile)

	reviewer, err := configureSemanticReviewer()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reviewer.Review(context.Background(), semanticReviewTestInput()); err != nil {
		t.Fatal(err)
	}
}

func setSemanticReviewEnv(t *testing.T, endpoint, model, apiKey string) {
	t.Helper()
	t.Setenv("LLM_ENDPOINT", endpoint)
	t.Setenv("LLM_MODEL", model)
	t.Setenv("LLM_API_KEY", apiKey)
	t.Setenv("LLM_API_KEY_FILE", "")
	t.Setenv("AGENT_SEMANTIC_REVIEW_ENDPOINT", "")
	t.Setenv("AGENT_SEMANTIC_REVIEW_MODEL", "")
	t.Setenv("AGENT_SEMANTIC_REVIEW_API_KEY", "")
}

func semanticReviewTestServer(t *testing.T, expectedModel, expectedKey string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+expectedKey {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		var request struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if request.Model != expectedModel {
			t.Errorf("model = %q, want %q", request.Model, expectedModel)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"status\":\"completed\",\"recommendation\":\"publish\",\"risk_level\":\"low\",\"summary\":\"ok\",\"findings\":[]}"}}]}`))
	}))
}

func semanticReviewTestInput() releasecenter.SemanticReviewInput {
	return releasecenter.SemanticReviewInput{
		TenantID: "tenant", DocumentID: "document", KnowledgeSpaceID: "space", Permission: "internal",
		Candidate: publicationworkflow.Candidate{DocumentID: "document", DocumentVersionID: "version", GenerationID: "generation", ExpectedChunkCount: 1, ExpectedChunkDigest: "digest", ReleaseRevision: 1},
		Chunks:    []releasecenter.ContentChunk{{ChunkID: "chunk", Content: "content"}},
	}
}

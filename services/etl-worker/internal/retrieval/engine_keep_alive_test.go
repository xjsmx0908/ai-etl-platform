package retrieval

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-etl-pipeline/internal/config"
)

func TestEmbedQuestionSendsOllamaKeepAlive(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embedding":[0.1,0.2]}`))
	}))
	defer srv.Close()

	engine := NewEngine(config.Config{
		EmbedEndpoint:  srv.URL + "/api/embeddings",
		EmbedModel:     "bge-m3",
		EmbedKeepAlive: "24h",
	})
	if _, err := engine.embedQuestion(context.Background(), "办公用品"); err != nil {
		t.Fatalf("embed question: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if payload["keep_alive"] != "24h" {
		t.Fatalf("expected keep_alive 24h, got %v in %s", payload["keep_alive"], raw)
	}
}

func TestWarmPinsOllamaEmbeddingModel(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embedding":[0.1,0.2]}`))
	}))
	defer srv.Close()

	engine := NewEngine(config.Config{
		EmbedEndpoint:  srv.URL + "/api/embeddings",
		EmbedModel:     "bge-m3",
		EmbedKeepAlive: "24h",
	})
	if err := engine.Warm(context.Background()); err != nil {
		t.Fatalf("warm: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected one warmup embed, got %d", calls)
	}
}

func TestEmbedQuestionOmitsKeepAliveForOpenAICompatibleEndpoint(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer srv.Close()

	engine := NewEngine(config.Config{
		EmbedEndpoint:  srv.URL + "/v1/embeddings",
		EmbedModel:     "text-embedding-3-small",
		EmbedKeepAlive: "24h",
	})
	if _, err := engine.embedQuestion(context.Background(), "办公用品"); err != nil {
		t.Fatalf("embed question: %v", err)
	}
	if strings.Contains(string(raw), "keep_alive") {
		t.Fatalf("openai-compatible embed must not send keep_alive, got %s", raw)
	}
}

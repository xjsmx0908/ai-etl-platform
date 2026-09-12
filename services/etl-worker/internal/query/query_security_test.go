package query

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
)

func TestClampQuestion(t *testing.T) {
	if _, err := clampQuestion("  ", 2000); err != ErrQuestionRequired {
		t.Fatalf("empty question err=%v", err)
	}
	if _, err := clampQuestion(strings.Repeat("问", 2001), 2000); err != ErrQuestionTooLong {
		t.Fatalf("long question err=%v", err)
	}
	got, err := clampQuestion("  hello  ", 2000)
	if err != nil || got != "hello" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if utf8.RuneCountInString(strings.Repeat("问", 2000)) != 2000 {
		t.Fatal("fixture length")
	}
}

func TestHandleQueryRejectsOversizedBody(t *testing.T) {
	svc := NewService(config.Config{QueryMaxBodyBytes: 64})
	req := httptest.NewRequest(http.MethodPost, "/v1/query", strings.NewReader(`{"question":"`+strings.Repeat("a", 200)+`"}`))
	w := httptest.NewRecorder()
	svc.HandleQuery(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestAskRejectsLongQuestion(t *testing.T) {
	svc := NewService(config.Config{QuestionMaxRunes: 8})
	_, err := svc.Ask(context.Background(), Request{Question: "123456789"}, AccessContext{TenantID: "acme", Role: "user"})
	if err != ErrQuestionTooLong {
		t.Fatalf("err=%v", err)
	}
}

func TestHandleQueryRedactsSensitiveAnswer(t *testing.T) {
	t.Setenv("LLM_ENDPOINT", "")
	t.Setenv("LLM_API_KEY", "")
	t.Setenv("LLM_MODEL", "test-llm")

	embedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2]}]}`))
	}))
	defer embedSrv.Close()
	qdrantSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"points":[{"score":0.9,"payload":{"chunk_id":"c1","doc_id":"d1","content":"ctx","tenant_id":"acme"}}]}}`))
	}))
	defer qdrantSrv.Close()
	llmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"password=super-secret-value"}}],"usage":{"prompt_tokens":8,"completion_tokens":4}}`))
	}))
	defer llmSrv.Close()
	t.Setenv("LLM_ENDPOINT", llmSrv.URL)

	hookCalls := 0
	svc := NewService(config.Config{
		EmbedEndpoint:   embedSrv.URL,
		EmbedModel:      "test-embed",
		StoreEndpoint:   qdrantSrv.URL,
		StoreCollection: "docs",
		SparseK1:        1.2,
		SparseB:         0.75,
		SparseAvgDL:     256,
	}).WithSensitiveAnswerHook(func(context.Context, AccessContext, string) { hookCalls++ })

	req := httptest.NewRequest(http.MethodPost, "/v1/query", strings.NewReader(`{"question":"what is the password"}`))
	ctx := context.WithValue(req.Context(), auth.CtxTenantID, "acme")
	ctx = context.WithValue(ctx, auth.CtxPermission, "user")
	w := httptest.NewRecorder()
	svc.HandleQuery(w, req.WithContext(ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), SensitiveAnswerRefusal) {
		t.Fatalf("expected refusal, body=%s", w.Body.String())
	}
	if strings.Contains(w.Body.String(), "super-secret-value") {
		t.Fatal("secret leaked in response")
	}
	if hookCalls != 1 {
		t.Fatalf("hookCalls=%d", hookCalls)
	}
}

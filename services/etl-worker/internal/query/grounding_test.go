package query

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-etl-pipeline/internal/config"
)

func TestParseGroundingVerdict(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
		wantErr bool
	}{
		{"both pass", `{"supported": true, "answers_question": true}`, true, false},
		{"unsupported", `{"supported": false, "answers_question": true}`, false, false},
		{"does not answer question", `{"supported":true,"answers_question":false}`, false, false},
		{"markdown fence", "```json\n{\"supported\": true, \"answers_question\": true}\n```", true, false},
		{"prose prefix", `思考后判定：{"supported": false, "answers_question": true}，因为答案引入了文档外的数字`, false, false},
		{"case insensitive", `{"SUPPORTED": TRUE, "ANSWERS_QUESTION": TRUE}`, true, false},
		{"string instead of bool", `{"supported": "yes"}`, false, true},
		{"missing answer relevance", `{"supported": true}`, false, true},
		{"missing fields", `{"ok": true}`, false, true},
		{"empty", ``, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseGroundingVerdict(tt.content)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got verdict=%v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("expected supported=%v, got %v", tt.want, got)
			}
		})
	}
}

// llmStub returns an OpenAI-compatible chat-completions response whose message
// content is the given string, optionally failing with status.
func llmStub(content string, status int) *httptest.Server {
	return llmStubWithFinish(content, "stop", status)
}

// llmStubWithFinish also controls finish_reason, so the truncation path a
// reasoning model takes (empty content + finish_reason=length) can be tested.
func llmStubWithFinish(content, finishReason string, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			http.Error(w, "boom", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// The exact body shape callLLM parses.
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` +
			`"` + strings.ReplaceAll(content, `"`, `\"`) + `"},` +
			`"finish_reason":"` + finishReason + `"}],` +
			`"usage":{"prompt_tokens":10,"completion_tokens":64}}`))
	}))
}

func TestGroundingCheck(t *testing.T) {
	sources := []SourceContext{{DocID: "d1", Content: "文档上传支持 PDF、Word 和 Markdown 三种格式。"}}

	t.Run("verifier says supported", func(t *testing.T) {
		srv := llmStub(`{"supported": true, "answers_question": true}`, http.StatusOK)
		defer srv.Close()
		t.Setenv("LLM_ENDPOINT", srv.URL)
		t.Setenv("LLM_API_KEY", "test-key")
		svc := NewService(config.Config{})
		ok, err := svc.groundingCheck(context.Background(), "能上传什么格式？", "支持 PDF、Word 和 Markdown", sources)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !ok {
			t.Fatalf("expected supported=true")
		}
	})

	t.Run("verifier says unsupported", func(t *testing.T) {
		srv := llmStub(`{"supported": false, "answers_question": true}`, http.StatusOK)
		defer srv.Close()
		t.Setenv("LLM_ENDPOINT", srv.URL)
		t.Setenv("LLM_API_KEY", "test-key")
		svc := NewService(config.Config{})
		ok, err := svc.groundingCheck(context.Background(), "今年预算多少？", "今年预算增长 34%", sources)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ok {
			t.Fatalf("expected supported=false")
		}
	})

	t.Run("verifier failure surfaces error", func(t *testing.T) {
		srv := llmStub(``, http.StatusInternalServerError)
		defer srv.Close()
		t.Setenv("LLM_ENDPOINT", srv.URL)
		t.Setenv("LLM_API_KEY", "test-key")
		svc := NewService(config.Config{})
		if _, err := svc.groundingCheck(context.Background(), "q", "answer", sources); err == nil {
			t.Fatalf("expected error from failing verifier")
		}
	})

	// Regression: a reasoning model that spends its whole budget on reasoning
	// tokens returns empty content with finish_reason=length. This was previously
	// reported as "no supported verdict in verifier response", which pointed at the
	// prompt instead of at the token budget and left the check silently disabled.
	t.Run("truncated verdict is reported as truncation", func(t *testing.T) {
		srv := llmStubWithFinish(``, "length", http.StatusOK)
		defer srv.Close()
		t.Setenv("LLM_ENDPOINT", srv.URL)
		t.Setenv("LLM_API_KEY", "test-key")
		svc := NewService(config.Config{})
		_, err := svc.groundingCheck(context.Background(), "q", "answer", sources)
		if err == nil {
			t.Fatal("expected an error when the verifier is truncated")
		}
		if !strings.Contains(err.Error(), "truncated") {
			t.Errorf("error must name truncation so the fix is obvious, got: %v", err)
		}
		if !strings.Contains(err.Error(), "finish_reason=length") {
			t.Errorf("error must carry finish_reason, got: %v", err)
		}
	})

	// The budget must leave room for a reasoning pass, not merely for the JSON.
	t.Run("token budget has reasoning headroom", func(t *testing.T) {
		if groundingMaxTokens < 256 {
			t.Errorf("groundingMaxTokens=%d is too tight for a reasoning model; "+
				"empty content and a silently disabled gate is the failure mode",
				groundingMaxTokens)
		}
	})
}

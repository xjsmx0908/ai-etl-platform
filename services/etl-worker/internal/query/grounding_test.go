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
		{"plain true", `{"supported": true}`, true, false},
		{"plain false", `{"supported": false}`, false, false},
		{"no spaces", `{"supported":false}`, false, false},
		{"markdown fence", "```json\n{\"supported\": true}\n```", true, false},
		{"prose prefix", `思考后判定：{"supported": false}，因为答案引入了文档外的数字`, false, false},
		{"case insensitive", `{"SUPPORTED": TRUE}`, true, false},
		{"string instead of bool", `{"supported": "yes"}`, false, true},
		{"missing field", `{"ok": true}`, false, true},
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
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			http.Error(w, "boom", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// The exact body shape callLLM parses.
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` +
			`"` + strings.ReplaceAll(content, `"`, `\"`) + `"}}]}`))
	}))
}

func TestGroundingCheck(t *testing.T) {
	sources := []SourceContext{{DocID: "d1", Content: "文档上传支持 PDF、Word 和 Markdown 三种格式。"}}

	t.Run("verifier says supported", func(t *testing.T) {
		srv := llmStub(`{"supported": true}`, http.StatusOK)
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
		srv := llmStub(`{"supported": false}`, http.StatusOK)
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
}

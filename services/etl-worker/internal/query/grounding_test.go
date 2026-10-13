package query

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

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

// capturingLLMStub records the request body the caller sent and answers with a
// fixed verdict, so a test can assert on exactly what was put in front of the
// verifier.
func capturingLLMStub(verdict string, captured *string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		*captured = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` +
			`"` + strings.ReplaceAll(verdict, `"`, `\"`) + `"},` +
			`"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":10,"completion_tokens":8}}`))
	}))
}

// userMessageOf returns the last user-role message of a chat-completions body.
func userMessageOf(t *testing.T, body string) string {
	t.Helper()
	var reqBody struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &reqBody); err != nil {
		t.Fatalf("decode chat-completions body: %v", err)
	}
	for i := len(reqBody.Messages) - 1; i >= 0; i-- {
		if reqBody.Messages[i].Role == "user" {
			return reqBody.Messages[i].Content
		}
	}
	t.Fatalf("chat-completions body has no user message: %.200s", body)
	return ""
}

// evidenceBlock extracts the rendered <document> section of a prompt so the
// verifier's evidence and the answer prompt's evidence can be compared directly.
func evidenceBlock(t *testing.T, prompt string) string {
	t.Helper()
	const head = "参考文档：\n"
	start := strings.Index(prompt, head)
	if start < 0 {
		t.Fatalf("prompt has no 参考文档 section: %.120q", prompt)
	}
	rest := prompt[start+len(head):]
	end := strings.Index(rest, "\n\n用户问题：")
	if end < 0 {
		t.Fatalf("prompt has no 用户问题 delimiter: %.120q", rest)
	}
	return rest[:end]
}

// The verifier must be given the same evidence the answer prompt was given.
//
// It used to concatenate every source and then slice the concatenation at a
// fixed 8000 BYTES, which hid every source past the cut. That is not a
// hypothetical: on the default tenant at top_k=8 the real context is
// 10756-12852 bytes, so the verifier judged those answers against a prefix and
// rejected ones whose support sat beyond it. A byte-slice cut can also split a
// rune and leaves the last <document> unclosed.
func TestGroundingVerifierSeesTheSameEvidenceAsTheAnswerPrompt(t *testing.T) {
	// Five sources whose concatenation is past 8000 bytes but inside the
	// generation budget, with a marker that exists only in the last one.
	filler := strings.Repeat("这是用来撑满上下文的普通句子。", 50) // 750 runes / 2250 bytes
	marker := "末段独有标记ZEBRA"
	sources := make([]SourceContext, 0, 5)
	for i := 1; i <= 4; i++ {
		sources = append(sources, SourceContext{DocID: fmt.Sprintf("d%d", i), Content: filler})
	}
	sources = append(sources, SourceContext{DocID: "d5", Content: filler + marker})
	question := "最后一个文档里写了什么？"

	var captured string
	srv := capturingLLMStub(`{"supported": true, "answers_question": true}`, &captured)
	defer srv.Close()
	t.Setenv("LLM_ENDPOINT", srv.URL)
	t.Setenv("LLM_API_KEY", "test-key")
	svc := NewService(config.Config{})

	if _, err := svc.groundingCheck(context.Background(), question, "答案", sources); err != nil {
		t.Fatalf("grounding check: %v", err)
	}
	verifierDocs := evidenceBlock(t, userMessageOf(t, captured))

	promptBody, _, err := svc.buildPrompt(question, sources)
	if err != nil {
		t.Fatalf("build prompt: %v", err)
	}
	promptDocs := evidenceBlock(t, userMessageOf(t, string(promptBody)))

	// Without this the test would pass on a corpus that never reached the cap.
	if len(promptDocs) <= 8000 {
		t.Fatalf("test data must exceed the old 8000-byte cap to be meaningful, got %d bytes", len(promptDocs))
	}
	if !strings.Contains(verifierDocs, marker) {
		t.Errorf("verifier context dropped the last source (it stops at %d bytes); "+
			"an answer supported there is rejected as ungrounded", len(verifierDocs))
	}
	if verifierDocs != promptDocs {
		t.Errorf("verifier and answer prompt must see identical evidence: verifier %d bytes, prompt %d bytes",
			len(verifierDocs), len(promptDocs))
	}
	if !utf8.ValidString(verifierDocs) {
		t.Error("verifier context must be valid UTF-8; a byte-slice cut can split a rune")
	}
	opened := strings.Count(verifierDocs, "<document ")
	closed := strings.Count(verifierDocs, "</document>")
	if opened != len(sources) || closed != len(sources) {
		t.Errorf("every source needs a closed envelope: opened=%d closed=%d want=%d",
			opened, closed, len(sources))
	}
}

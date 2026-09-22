package embedder

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
	"ai-etl-pipeline/internal/model"
)

// OpenAI-compatible endpoint: valid embedding + usage.
func TestEmbedOpenAICompatibleEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}],"usage":{"total_tokens":17}}`))
	}))
	defer srv.Close()

	e, err := NewHTTPEmbedder(config.Config{
		Environment:     "staging",
		EmbedEndpoint:   srv.URL,
		EmbedModel:      "test-embed",
		EmbedMaxRetries: 1,
	})
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	defer e.Close()

	chunk := &model.Chunk{ChunkID: "c1", Content: "some text"}
	if err := e.Embed(context.Background(), chunk); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(chunk.Vector) != 3 || chunk.Vector[0] != 0.1 {
		t.Fatalf("unexpected vector: %v", chunk.Vector)
	}
	if chunk.TokenUsed != 17 {
		t.Fatalf("expected 17 tokens, got %d", chunk.TokenUsed)
	}
}

// Ollama-native endpoint (/api/embeddings): embedding without usage.
func TestEmbedOllamaNativeEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embedding":[0.5,0.6]}`))
	}))
	defer srv.Close()

	e, err := NewHTTPEmbedder(config.Config{
		Environment:     "staging",
		EmbedEndpoint:   srv.URL + "/api/embeddings",
		EmbedModel:      "nomic-embed-text",
		EmbedMaxRetries: 1,
	})
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	defer e.Close()

	chunk := &model.Chunk{ChunkID: "c1", Content: "hello world"}
	if err := e.Embed(context.Background(), chunk); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if len(chunk.Vector) != 2 || chunk.Vector[0] != 0.5 {
		t.Fatalf("unexpected vector: %v", chunk.Vector)
	}
	// Ollama reports no usage; token estimate must be positive.
	if chunk.TokenUsed < 1 {
		t.Fatalf("expected positive token estimate, got %d", chunk.TokenUsed)
	}
}

// A 429 followed by success must retry and eventually succeed.
func TestEmbedRetriesOnRateLimit(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate limit"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1]}],"usage":{"total_tokens":3}}`))
	}))
	defer srv.Close()

	e, err := NewHTTPEmbedder(config.Config{
		Environment:     "staging",
		EmbedEndpoint:   srv.URL,
		EmbedModel:      "test-embed",
		EmbedMaxRetries: 3,
		EmbedBackoff:    1, // ms, keep test fast
	})
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	defer e.Close()

	chunk := &model.Chunk{ChunkID: "c1", Content: "text"}
	if err := e.Embed(context.Background(), chunk); err != nil {
		t.Fatalf("embed after retry: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls (1 retry), got %d", calls)
	}
}

// A 4xx (non-retryable) error must fail immediately without retrying.
func TestEmbedFailsFastOnClientError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"bad model"}`))
	}))
	defer srv.Close()

	e, err := NewHTTPEmbedder(config.Config{
		Environment:     "staging",
		EmbedEndpoint:   srv.URL,
		EmbedModel:      "bad-model",
		EmbedMaxRetries: 3,
	})
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	defer e.Close()

	chunk := &model.Chunk{ChunkID: "c1", Content: "text"}
	if err := e.Embed(context.Background(), chunk); err == nil {
		t.Fatal("expected error for 400 response")
	}
	if calls != 1 {
		t.Fatalf("expected no retry on 400, got %d calls", calls)
	}
}

// Sending an API key must set the Authorization header on OpenAI-style calls.
func TestEmbedSendsAPIKeyHeader(t *testing.T) {
	var authHeader string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1]}],"usage":{"total_tokens":1}}`))
	}))
	defer srv.Close()

	e, err := NewHTTPEmbedder(config.Config{
		Environment:     "staging",
		EmbedEndpoint:   srv.URL,
		EmbedModel:      "test-embed",
		EmbedAPIKey:     "sk-test",
		EmbedMaxRetries: 1,
	})
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	defer e.Close()

	if err := e.Embed(context.Background(), &model.Chunk{ChunkID: "c1", Content: "t"}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if authHeader != "Bearer sk-test" {
		t.Fatalf("expected Bearer sk-test, got %q", authHeader)
	}
}

// IsRetryable must unwrap wrapped RetryableErrors.
func TestIsRetryableUnwraps(t *testing.T) {
	inner := &RetryableError{Msg: "rate limit"}
	if !IsRetryable(inner) {
		t.Fatal("expected RetryableError to be retryable")
	}
	if IsRetryable(&plainError{msg: "boom"}) {
		t.Fatal("expected plain error not to be retryable")
	}
}

func TestIsRetryableDeadline(t *testing.T) {
	if !IsRetryable(fmt.Errorf("http: %w", context.DeadlineExceeded)) {
		t.Fatal("expected deadline to be retryable")
	}
}

type plainError struct{ msg string }

func (e *plainError) Error() string { return e.msg }

func TestEmbedOllamaNativeRequestPinsKeepAlive(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embedding":[0.1,0.2]}`))
	}))
	defer srv.Close()

	e, err := NewHTTPEmbedder(config.Config{
		Environment:     "staging",
		EmbedEndpoint:   srv.URL + "/api/embeddings",
		EmbedModel:      "bge-m3",
		EmbedKeepAlive:  "24h",
		EmbedMaxRetries: 1,
	})
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	defer e.Close()

	chunk := &model.Chunk{ChunkID: "c1", Content: "hello"}
	if err := e.Embed(context.Background(), chunk); err != nil {
		t.Fatalf("embed: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if payload["keep_alive"] != "24h" {
		t.Fatalf("expected keep_alive 24h, got %v in %s", payload["keep_alive"], raw)
	}
}

func TestEmbedOpenAICompatibleRequestOmitsKeepAlive(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1]}],"usage":{"total_tokens":1}}`))
	}))
	defer srv.Close()

	e, err := NewHTTPEmbedder(config.Config{
		Environment:     "staging",
		EmbedEndpoint:   srv.URL + "/v1/embeddings",
		EmbedModel:      "text-embedding-3-small",
		EmbedKeepAlive:  "24h",
		EmbedMaxRetries: 1,
	})
	if err != nil {
		t.Fatalf("new embedder: %v", err)
	}
	defer e.Close()
	if err := e.Embed(context.Background(), &model.Chunk{ChunkID: "c1", Content: "hello"}); err != nil {
		t.Fatalf("embed: %v", err)
	}
	if strings.Contains(string(raw), "keep_alive") {
		t.Fatalf("openai-compatible embed must not send keep_alive, got %s", raw)
	}
}

// TestTruncateKeepsThePreviewValidUTF8 pins the contract of the text_preview
// span attribute: whatever comes in, what reaches the tracer is valid UTF-8 and
// no longer than the byte budget.
//
// The regression it guards is subtle. An earlier fix added ToValidUTF8 to repair
// input that was already invalid, but left the cut as s[:maxLen] -- a byte slice.
// Repairing and then cutting at an arbitrary byte offset re-breaks the string, so
// for any non-ASCII text the attribute could still carry invalid UTF-8. A pure-CJK
// preview makes it certain rather than likely: three-byte characters mean byte 100
// always lands inside a character.
func TestTruncateKeepsThePreviewValidUTF8(t *testing.T) {
	const budget = 100
	cases := []struct {
		name    string
		content string
	}{
		// 40 three-byte characters: 120 bytes, so the cut is guaranteed to fall
		// inside a character if it is taken at byte 100.
		{"pure CJK past the budget", strings.Repeat("巡", 40)},
		{"CJK with ASCII mixed in", "机房日常巡检规范\n巡检编号 PXBB8664。\n机房温度标准为 26 摄氏度，湿度应保持在 40% 到 60% 之间，超限需在 30 分钟内上报。"},
		// Four-byte runes exercise a different offset alignment.
		{"emoji", strings.Repeat("🙂", 40)},
		{"combining marks", strings.Repeat("e\u0301", 60)},
		// Already-invalid input: the repair half of the contract.
		{"lone continuation byte", "巡检记录\x80温度上限 32 度"},
		{"truncated CJK sequence", "巡检记录\xe5\xb7"},
		{"short enough to pass through", "warmup"},
		{"exactly the budget", strings.Repeat("a", budget)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncate(tc.content, budget)
			if !utf8.ValidString(got) {
				t.Fatalf("truncate returned invalid UTF-8: %q", got)
			}
			preview := strings.TrimSuffix(got, "...")
			if len(preview) > budget {
				t.Errorf("preview is %d bytes, over the %d-byte budget", len(preview), budget)
			}
			// The preview must be a prefix of the repaired input, so a cut can
			// only ever shorten the text -- never substitute or reorder it.
			repaired := strings.ToValidUTF8(tc.content, "\uFFFD")
			if !strings.HasPrefix(repaired, preview) {
				t.Errorf("preview %q is not a prefix of the repaired input %q", preview, repaired)
			}
		})
	}
}

// TestTruncateCutsOnARuneBoundary is the narrow version of the test above: it
// asserts the boundary property directly instead of inferring it from validity,
// so a future change that switches to runes-as-budget still has to keep the cut
// on a boundary.
func TestTruncateCutsOnARuneBoundary(t *testing.T) {
	content := strings.Repeat("巡", 40)
	got := strings.TrimSuffix(truncate(content, 100), "...")
	if len(got) != 99 {
		t.Fatalf("cut = %d bytes, want 99 (33 three-byte characters); the next "+
			"character starts at byte 99 and would not fit in the 100-byte budget", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("cut is not valid UTF-8: %q", got)
	}
}

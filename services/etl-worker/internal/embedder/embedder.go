package embedder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"strings"
	"time"

	"ai-etl-pipeline/internal/circuit"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/model"
	platformtracing "ai-etl-pipeline/internal/tracing"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Embedder converts text chunks into dense vector representations.
type Embedder interface {
	Embed(ctx context.Context, chunk *model.Chunk) error
	Close() error
}

// HTTPEmbedder calls an external HTTP API for embeddings.
type HTTPEmbedder struct {
	cfg     config.Config
	client  *http.Client
	breaker *circuit.Breaker
	tracer  trace.Tracer
}

// NewHTTPEmbedder creates a new HTTP embedder with circuit breaker and tracing.
func NewHTTPEmbedder(cfg config.Config) (*HTTPEmbedder, error) {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
	}

	client := &http.Client{
		Timeout:   cfg.EmbedTimeout,
		Transport: transport,
	}

	breaker := circuit.New("embedder", 10, 30*time.Second)

	return &HTTPEmbedder{
		cfg:     cfg,
		client:  client,
		breaker: breaker,
		tracer:  otel.Tracer("embedder"),
	}, nil
}

// Embed generates embeddings for the given text chunk with retry logic.
func (e *HTTPEmbedder) Embed(ctx context.Context, chunk *model.Chunk) error {
	ctx, span := e.tracer.Start(ctx, "HTTPEmbedder.Embed")
	defer span.End()
	span.SetAttributes(attribute.String("chunk_id", chunk.ChunkID))
	span.SetAttributes(attribute.String("text_preview", truncate(chunk.Content, 100)))

	var lastErr error
	for attempt := 1; attempt <= e.cfg.EmbedMaxRetries; attempt++ {
		span.SetAttributes(attribute.Int("attempt", attempt))

		vec, tokens, err := e.callAPIWithBreaker(ctx, chunk.Content)
		if err == nil {
			chunk.Vector = vec
			chunk.TokenUsed = tokens
			span.SetAttributes(attribute.Int("tokens", tokens))
			span.SetAttributes(attribute.Int("vector_dim", len(vec)))
			return nil
		}

		lastErr = err
		span.RecordError(err)

		if !IsRetryable(err) {
			span.SetStatus(codes.Error, "non-retryable error")
			return err
		}

		if attempt < e.cfg.EmbedMaxRetries {
			wait := e.backoff(attempt)
			span.SetAttributes(attribute.Int("backoff_ms", int(wait.Milliseconds())))
			slog.Warn("embed retry", "attempt", attempt, "error", err, "wait", wait)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				span.SetStatus(codes.Error, "context cancelled")
				return ctx.Err()
			}
		}
	}

	span.SetStatus(codes.Error, "max retries exceeded")
	return fmt.Errorf("embed failed after %d attempts: %w", e.cfg.EmbedMaxRetries, lastErr)
}

// Close releases HTTP client resources.
func (e *HTTPEmbedder) Close() error {
	e.client.CloseIdleConnections()
	return nil
}

func (e *HTTPEmbedder) callAPIWithBreaker(ctx context.Context, text string) ([]float64, int, error) {
	type embedResult struct {
		vec    []float64
		tokens int
	}

	result, err := e.breaker.Execute(func() (any, error) {
		vec, tokens, err := e.callAPI(ctx, text)
		if err != nil {
			return nil, err
		}
		return embedResult{vec: vec, tokens: tokens}, nil
	})
	if err != nil {
		return nil, 0, err
	}
	r := result.(embedResult)
	return r.vec, r.tokens, nil
}

func (e *HTTPEmbedder) callAPI(ctx context.Context, text string) ([]float64, int, error) {
	if e.cfg.IsDev() {
		return e.mockAPI(text)
	}

	// Auto-detect Ollama native API vs OpenAI-compatible API
	if isOllamaNativeEndpoint(e.cfg.EmbedEndpoint) {
		return e.callOllamaAPI(ctx, text)
	}
	return e.callOpenAIAPI(ctx, text)
}

// isOllamaNativeEndpoint detects if the endpoint is Ollama's native /api/embeddings path.
func isOllamaNativeEndpoint(endpoint string) bool {
	return strings.Contains(endpoint, "/api/embeddings") || strings.Contains(endpoint, "/api/embed")
}

// callOpenAIAPI sends embedding request in OpenAI-compatible format.
func (e *HTTPEmbedder) callOpenAIAPI(ctx context.Context, text string) ([]float64, int, error) {
	reqBody, _ := json.Marshal(map[string]interface{}{
		"model": e.cfg.EmbedModel,
		"input": text,
	})

	req, err := http.NewRequestWithContext(ctx, "POST", e.cfg.EmbedEndpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if e.cfg.EmbedAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.cfg.EmbedAPIKey)
	}
	platformtracing.InjectHTTPHeaders(ctx, req)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 429 {
		return nil, 0, &RetryableError{Msg: "rate limit exceeded (429)"}
	}
	if resp.StatusCode >= 500 {
		return nil, 0, &RetryableError{Msg: fmt.Sprintf("server error (%d)", resp.StatusCode)}
	}
	if resp.StatusCode != 200 {
		return nil, 0, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, 0, fmt.Errorf("decode response: %w", err)
	}
	if len(result.Data) == 0 {
		return nil, 0, fmt.Errorf("empty embedding response")
	}

	return result.Data[0].Embedding, result.Usage.TotalTokens, nil
}

// Warm loads the embedding model and pins it with keep_alive. Best-effort.
func (e *HTTPEmbedder) Warm(ctx context.Context) error {
	if e == nil || !isOllamaNativeEndpoint(e.cfg.EmbedEndpoint) {
		return nil
	}
	chunk := &model.Chunk{ChunkID: "warmup", Content: "warmup"}
	return e.Embed(ctx, chunk)
}

// callOllamaAPI sends an embedding request in Ollama native format.
// Request:  {"model": "...", "prompt": "..."}
// Response: {"embedding": [...]}
func (e *HTTPEmbedder) callOllamaAPI(ctx context.Context, text string) ([]float64, int, error) {
	payload := map[string]interface{}{
		"model":  e.cfg.EmbedModel,
		"prompt": text,
	}
	if keepAlive := strings.TrimSpace(e.cfg.EmbedKeepAlive); keepAlive != "" {
		payload["keep_alive"] = keepAlive
	}
	reqBody, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", e.cfg.EmbedEndpoint, bytes.NewReader(reqBody))
	if err != nil {
		return nil, 0, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	platformtracing.InjectHTTPHeaders(ctx, req)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 429 {
		return nil, 0, &RetryableError{Msg: "rate limit exceeded (429)"}
	}
	if resp.StatusCode >= 500 {
		return nil, 0, &RetryableError{Msg: fmt.Sprintf("server error (%d)", resp.StatusCode)}
	}
	if resp.StatusCode != 200 {
		return nil, 0, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body))
	}

	// Ollama native response: {"embedding": [...]}
	var result struct {
		Embedding []float64 `json:"embedding"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, 0, fmt.Errorf("decode ollama response: %w", err)
	}
	if len(result.Embedding) == 0 {
		return nil, 0, fmt.Errorf("empty ollama embedding response")
	}

	// Ollama doesn't return token usage, estimate from text length
	tokens := len([]rune(text)) / 2
	if tokens < 1 {
		tokens = 1
	}

	return result.Embedding, tokens, nil
}

func (e *HTTPEmbedder) mockAPI(text string) ([]float64, int, error) {
	time.Sleep(time.Duration(30+rand.Intn(70)) * time.Millisecond)
	if rand.Float64() < 0.03 {
		return nil, 0, &RetryableError{Msg: "mock 429"}
	}

	vec := make([]float64, e.cfg.EmbedDimension)
	var norm float64
	for i := range vec {
		vec[i] = rand.Float64()*2 - 1
		norm += vec[i] * vec[i]
	}
	norm = math.Sqrt(norm)
	for i := range vec {
		vec[i] /= norm
	}
	tokens := len([]rune(text)) / 2
	if tokens < 1 {
		tokens = 1
	}
	return vec, tokens, nil
}

func (e *HTTPEmbedder) backoff(attempt int) time.Duration {
	b := e.cfg.EmbedBackoff * time.Duration(1<<(attempt-1))
	if b > e.cfg.EmbedMaxBackoff {
		b = e.cfg.EmbedMaxBackoff
	}
	jitter := time.Duration(float64(b) * (0.7 + rand.Float64()*0.6))
	return jitter
}

func truncate(s string, maxLen int) string {
	s = strings.ToValidUTF8(s, "�")
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// --- Retryable error ---

// RetryableError indicates a transient failure that can be retried.
type RetryableError struct {
	Msg string
}

func (e *RetryableError) Error() string { return e.Msg }

// IsRetryable checks if an error is transient and worth retrying.
func IsRetryable(err error) bool {
	for err != nil {
		if _, ok := err.(*RetryableError); ok {
			return true
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return true
		}
		type causer interface{ Unwrap() error }
		u, ok := err.(causer)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

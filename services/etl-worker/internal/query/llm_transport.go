package query

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"ai-etl-pipeline/internal/circuit"
	"ai-etl-pipeline/internal/tracing"
)

// normalizeLLMEndpoint accepts either a full chat-completions URL or a base URL.
// Examples:
//
//	https://api.openai.com/v1                -> /v1/chat/completions
//	https://api.openai.com                   -> /v1/chat/completions
//	https://host/v1/chat/completions         -> unchanged
func normalizeLLMEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "https://api.openai.com/v1/chat/completions"
	}

	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}

	path := strings.TrimSuffix(u.Path, "/")
	switch path {
	case "":
		u.Path = "/v1/chat/completions"
	case "/v1":
		u.Path = "/v1/chat/completions"
	default:
		if !strings.Contains(path, "/chat/completions") {
			u.Path = path + "/chat/completions"
		}
	}
	return u.String()
}

func (s *Service) generateAnswer(ctx context.Context, question string, sources []SourceContext) (answer string, usage llmCallResult, err error) {
	return s.completeAnswer(ctx, question, sources, nil)
}

func (s *Service) generateAnswerStreaming(ctx context.Context, question string, sources []SourceContext, onDelta func(string)) (answer string, usage llmCallResult, err error) {
	if onDelta == nil {
		onDelta = func(string) {}
	}
	return s.completeAnswer(ctx, question, sources, onDelta)
}

func enableChatStream(data []byte) ([]byte, error) {
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	body["stream"] = true
	return json.Marshal(body)
}

func (s *Service) completeAnswer(ctx context.Context, question string, sources []SourceContext, onDelta func(string)) (answer string, usage llmCallResult, err error) {
	_, promptSpan := s.tracer.Start(ctx, "Prompt.Build")
	data, contextChars, err := s.buildPrompt(question, sources)
	if err != nil {
		promptSpan.RecordError(err)
		promptSpan.SetStatus(codes.Error, "prompt build failed")
		promptSpan.End()
		return "", llmCallResult{}, err
	}
	promptSpan.SetAttributes(
		attribute.Int("prompt.source_count", len(sources)),
		attribute.Int("prompt.context_chars", contextChars),
		attribute.Int("prompt.request_bytes", len(data)),
	)
	promptSpan.End()

	ctx, llmSpan := s.tracer.Start(ctx, "LLM.ChatCompletion",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("gen_ai.request.model", s.llmModel),
			attribute.Int("gen_ai.request.max_tokens", s.llmMaxTokens),
		),
	)
	defer func() {
		if err != nil {
			llmSpan.RecordError(err)
			llmSpan.SetStatus(codes.Error, "LLM generation failed")
		}
		llmSpan.End()
	}()
	llmStarted := time.Now()
	llmOutcome := llmOutcomeSuccess
	defer func() {
		s.llmObserver.RecordLLMRequest(s.llmModel, llmOutcome, time.Since(llmStarted))
	}()

	if onDelta != nil {
		streamData, streamErr := enableChatStream(data)
		if streamErr != nil {
			llmOutcome = llmOutcomeInvalidResponse
			return "", llmCallResult{}, streamErr
		}
		var assembled strings.Builder
		result, err := s.breaker.Execute(func() (any, error) {
			return s.streamChat(ctx, streamData, func(delta string) {
				assembled.WriteString(delta)
				onDelta(delta)
			})
		})
		if err != nil {
			llmOutcome = classifyLLMOutcome(err)
			return "", llmCallResult{}, err
		}
		streamResult, ok := result.(llmStreamResult)
		content := assembled.String()
		if !ok || strings.TrimSpace(content) == "" {
			llmOutcome = llmOutcomeInvalidResponse
			return "", llmCallResult{}, newLLMCallError(llmOutcomeInvalidResponse, errors.New("empty LLM response"))
		}
		call := llmCallResult{
			Content:          content,
			PromptTokens:     streamResult.PromptTokens,
			CompletionTokens: streamResult.CompletionTokens,
		}
		if call.PromptTokens > 0 || call.CompletionTokens > 0 {
			s.llmObserver.RecordLLMTokens(s.llmModel, call.PromptTokens, call.CompletionTokens)
		}
		llmSpan.SetAttributes(
			attribute.Int("gen_ai.usage.prompt_tokens", int(call.PromptTokens)),
			attribute.Int("gen_ai.usage.completion_tokens", int(call.CompletionTokens)),
			attribute.Int("gen_ai.response.chars", len(content)),
		)
		return content, call, nil
	}

	result, err := s.breaker.Execute(func() (any, error) {
		return s.callLLM(ctx, data)
	})
	if err != nil {
		llmOutcome = classifyLLMOutcome(err)
		return "", llmCallResult{}, err
	}

	call, ok := result.(llmCallResult)
	if !ok || strings.TrimSpace(call.Content) == "" {
		llmOutcome = llmOutcomeInvalidResponse
		return "", llmCallResult{}, newLLMCallError(llmOutcomeInvalidResponse, errors.New("empty LLM response"))
	}
	if call.PromptTokens > 0 || call.CompletionTokens > 0 {
		s.llmObserver.RecordLLMTokens(s.llmModel, call.PromptTokens, call.CompletionTokens)
	}
	llmSpan.SetAttributes(
		attribute.Int("gen_ai.usage.prompt_tokens", int(call.PromptTokens)),
		attribute.Int("gen_ai.usage.completion_tokens", int(call.CompletionTokens)),
	)
	answer = call.Content

	llmSpan.SetAttributes(attribute.Int("gen_ai.response.chars", len(answer)))
	return answer, call, nil
}

// llmStreamResult carries per-call streaming stats: token usage if the provider
// reports it, and the time to first token (for TTFT observability).
type llmStreamResult struct {
	PromptTokens     int64
	CompletionTokens int64
	TTFT             time.Duration
}

// streamChat performs a streaming chat-completions call, invoking onDelta for
// each content chunk as it arrives. It returns token usage and time-to-first-
// token. The request body must already carry stream=true.
func (s *Service) streamChat(ctx context.Context, data []byte, onDelta func(string)) (llmStreamResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.llmEndpoint, bytes.NewReader(data))
	if err != nil {
		return llmStreamResult{}, newLLMCallError(llmOutcomeRequestError, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.llmAPIKey)
	tracing.InjectHTTPHeaders(ctx, req)

	started := time.Now()
	resp, err := s.httpClient.Do(req)
	if err != nil {
		outcome := llmOutcomeRequestError
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			outcome = llmOutcomeTimeout
		}
		return llmStreamResult{}, newLLMCallError(outcome, fmt.Errorf("LLM stream request failed: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		outcome := llmOutcomeClientError
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			outcome = llmOutcomeRateLimited
		case resp.StatusCode >= 500:
			outcome = llmOutcomeServerError
		}
		return llmStreamResult{}, newLLMCallError(outcome, fmt.Errorf("LLM stream error %d: %s", resp.StatusCode, string(body)))
	}

	var result llmStreamResult
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	firstToken := true
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				PromptTokens     int64 `json:"prompt_tokens"`
				CompletionTokens int64 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue // skip malformed keep-alive or non-JSON lines
		}
		if chunk.Usage != nil {
			result.PromptTokens = chunk.Usage.PromptTokens
			result.CompletionTokens = chunk.Usage.CompletionTokens
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta.Content
		if delta == "" {
			continue
		}
		if firstToken {
			result.TTFT = time.Since(started)
			firstToken = false
		}
		onDelta(delta)
	}
	if err := scanner.Err(); err != nil {
		return result, newLLMCallError(llmOutcomeInvalidResponse, fmt.Errorf("LLM stream read: %w", err))
	}
	return result, nil
}

func (s *Service) callLLM(ctx context.Context, data []byte) (llmCallResult, error) {

	req, err := http.NewRequestWithContext(ctx, "POST", s.llmEndpoint, bytes.NewReader(data))
	if err != nil {
		return llmCallResult{}, newLLMCallError(llmOutcomeRequestError, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.llmAPIKey)
	tracing.InjectHTTPHeaders(ctx, req)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		outcome := llmOutcomeRequestError
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			outcome = llmOutcomeTimeout
		}
		return llmCallResult{}, newLLMCallError(outcome, fmt.Errorf("LLM request failed: %w", err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		outcome := llmOutcomeClientError
		switch {
		case resp.StatusCode == http.StatusTooManyRequests:
			outcome = llmOutcomeRateLimited
		case resp.StatusCode >= 500:
			outcome = llmOutcomeServerError
		}
		return llmCallResult{}, newLLMCallError(outcome, fmt.Errorf("LLM API error %d: %s", resp.StatusCode, string(body)))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			// Carried so a truncated completion is reported as truncation. Without
			// it, a reasoning model that exhausts max_tokens before emitting any
			// content looks indistinguishable from one that ignored the output
			// format, which sends debugging in the wrong direction entirely.
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int64 `json:"prompt_tokens"`
			CompletionTokens int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return llmCallResult{}, newLLMCallError(llmOutcomeInvalidResponse, fmt.Errorf("decode LLM response: %w", err))
	}
	if len(result.Choices) == 0 {
		return llmCallResult{}, newLLMCallError(llmOutcomeInvalidResponse, errors.New("empty LLM response"))
	}

	usage := llmCallResult{}
	if result.Usage != nil {
		usage.PromptTokens = result.Usage.PromptTokens
		usage.CompletionTokens = result.Usage.CompletionTokens
	}
	usage.Content = result.Choices[0].Message.Content
	usage.FinishReason = result.Choices[0].FinishReason
	return usage, nil
}

// llmCallResult carries the LLM answer plus token usage reported in the response.
type llmCallResult struct {
	Content          string
	FinishReason     string
	PromptTokens     int64
	CompletionTokens int64
}

type llmCallError struct {
	outcome string
	err     error
}

func newLLMCallError(outcome string, err error) *llmCallError {
	return &llmCallError{outcome: outcome, err: err}
}

func (e *llmCallError) Error() string { return e.err.Error() }

func (e *llmCallError) Unwrap() error { return e.err }

func classifyLLMOutcome(err error) string {
	if circuit.IsRejected(err) {
		return llmOutcomeCircuitOpen
	}
	var callErr *llmCallError
	if errors.As(err, &callErr) {
		return callErr.outcome
	}
	return llmOutcomeRequestError
}

type noopLLMObserver struct{}

func (noopLLMObserver) RecordLLMRequest(string, string, time.Duration) {}
func (noopLLMObserver) RecordLLMTokens(string, int64, int64)           {}

func estimateLLMCost(usage llmCallResult, promptPrice, completionPrice float64) float64 {
	if promptPrice <= 0 && completionPrice <= 0 {
		return 0
	}
	return float64(usage.PromptTokens)/1000*promptPrice +
		float64(usage.CompletionTokens)/1000*completionPrice
}

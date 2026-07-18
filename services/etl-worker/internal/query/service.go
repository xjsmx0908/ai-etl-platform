// Package query implements the RAG Query Service (Retrieval + Generation).
package query

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/circuit"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/retrieval"
	"ai-etl-pipeline/internal/tracing"
)

// Service handles RAG queries: question → hybrid search → LLM generation.
type Service struct {
	cfg          config.Config
	llmEndpoint  string
	llmAPIKey    string
	llmModel     string
	llmMaxTokens int
	retriever    *retrieval.Engine
	httpClient   *http.Client
	breaker      *circuit.Breaker
	tracer       trace.Tracer
	llmObserver  LLMObserver
}

// LLMObserver receives low-cardinality LLM request outcomes for metrics adapters.
type LLMObserver interface {
	RecordLLMRequest(model, outcome string, duration time.Duration)
}

type llmModelInitializer interface {
	InitializeLLMModel(model string)
}

const (
	llmOutcomeSuccess         = "success"
	llmOutcomeTimeout         = "timeout"
	llmOutcomeRateLimited     = "rate_limited"
	llmOutcomeClientError     = "client_error"
	llmOutcomeServerError     = "server_error"
	llmOutcomeInvalidResponse = "invalid_response"
	llmOutcomeRequestError    = "request_error"
	llmOutcomeCircuitOpen     = "circuit_open"
)

// Request represents a query request from the client.
type Request struct {
	Question string `json:"question"`
	TopK     int    `json:"top_k,omitempty"`
}

// Response represents the query result returned to the client.
type Response struct {
	Answer   string          `json:"answer"`
	Sources  []SourceContext `json:"sources"`
	Duration string          `json:"duration"`
}

// SourceContext represents a retrieved document chunk with its relevance score.
type SourceContext struct {
	ChunkID  string  `json:"chunk_id"`
	DocID    string  `json:"doc_id"`
	Content  string  `json:"content"`
	Score    float64 `json:"score"`
	TenantID string  `json:"tenant_id,omitempty"`
}

// AccessContext is the authenticated caller context used by HTTP handlers and internal tools.
type AccessContext struct {
	TenantID string
	Role     string
}

var (
	ErrQuestionRequired = errors.New("question is required")
	ErrUnauthorized     = errors.New("unauthorized")
	ErrSearchFailed     = errors.New("search failed")
	ErrGenerationFailed = errors.New("generation failed")
)

var roleAllowedDocPermissions = map[string][]string{
	"admin":    {"public", "internal", "confidential"},
	"user":     {"public", "internal"},
	"readonly": {"public"},
}

// NewService creates a Query Service with its own LLM configuration.
func NewService(cfg config.Config) *Service {
	return NewServiceWithObserver(cfg, nil)
}

// NewServiceWithObserver creates a Query Service and reports LLM calls to observer.
func NewServiceWithObserver(cfg config.Config, observer LLMObserver) *Service {
	if observer == nil {
		observer = noopLLMObserver{}
	}
	service := &Service{
		cfg:          cfg,
		llmEndpoint:  normalizeLLMEndpoint(config.EnvStr("LLM_ENDPOINT", "https://api.openai.com/v1/chat/completions")),
		llmAPIKey:    config.EnvSecret("LLM_API_KEY", ""),
		llmModel:     config.EnvStr("LLM_MODEL", "gpt-4o-mini"),
		llmMaxTokens: config.EnvInt("LLM_MAX_TOKENS", 1024),
		retriever:    retrieval.NewEngine(cfg),
		httpClient:   &http.Client{Timeout: 30 * time.Second},
		breaker:      circuit.New("llm-api", 5, 60*time.Second),
		tracer:       tracing.Tracer("query"),
		llmObserver:  observer,
	}
	if initializer, ok := observer.(llmModelInitializer); ok {
		initializer.InitializeLLMModel(service.llmModel)
	}
	return service
}

// HandleQuery is the HTTP handler for POST /v1/query.
func (s *Service) HandleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, span := s.tracer.Start(r.Context(), "HandleQuery")
	defer span.End()

	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	resp, err := s.Ask(ctx, req, AccessContext{
		TenantID: auth.GetTenantID(r.Context()),
		Role:     auth.GetPermission(r.Context()),
	})
	switch {
	case err == nil:
	case errors.Is(err, ErrQuestionRequired):
		http.Error(w, "question is required", http.StatusBadRequest)
		return
	case errors.Is(err, ErrUnauthorized):
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	case errors.Is(err, ErrSearchFailed):
		span.RecordError(err)
		span.SetStatus(codes.Error, "search failed")
		http.Error(w, "search failed", http.StatusInternalServerError)
		return
	case errors.Is(err, ErrGenerationFailed):
		span.RecordError(err)
		span.SetStatus(codes.Error, "generation failed")
		http.Error(w, "generation failed", http.StatusInternalServerError)
		return
	default:
		span.RecordError(err)
		span.SetStatus(codes.Error, "query failed")
		http.Error(w, "query failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// Ask executes the RAG query pipeline for an authenticated caller.
func (s *Service) Ask(ctx context.Context, req Request, access AccessContext) (response Response, err error) {
	ctx, askSpan := s.tracer.Start(ctx, "QueryService.Ask")
	defer func() {
		if err != nil {
			askSpan.RecordError(err)
			askSpan.SetStatus(codes.Error, "query failed")
		}
		askSpan.End()
	}()

	if strings.TrimSpace(req.Question) == "" {
		return Response{}, ErrQuestionRequired
	}
	if strings.TrimSpace(access.TenantID) == "" {
		return Response{}, ErrUnauthorized
	}
	if req.TopK <= 0 {
		req.TopK = 5
	}
	allowedPermissions := allowedDocumentPermissionsForRole(access.Role)
	span := trace.SpanFromContext(ctx)

	span.SetAttributes(
		attribute.String("tenant_id", access.TenantID),
		attribute.String("permission_role", access.Role),
		attribute.Int("allowed_permission_levels", len(allowedPermissions)),
		attribute.Int("top_k", req.TopK),
		attribute.Int("question_len", len(req.Question)),
	)

	start := time.Now()

	retrievalResult, err := s.retriever.Retrieve(ctx, retrieval.Request{
		Question:           req.Question,
		TopK:               req.TopK,
		TenantID:           access.TenantID,
		AllowedPermissions: allowedPermissions,
	})
	if err != nil {
		slog.Error("retrieval failed", "error", err)
		return Response{}, fmt.Errorf("%w: %v", ErrSearchFailed, err)
	}
	if len(retrievalResult.PartialErrors) > 0 {
		slog.Warn("retrieval completed with partial errors",
			"tenant_id", access.TenantID,
			"route", retrievalResult.Route.Strategy,
			"errors", retrievalResult.PartialErrors)
	}
	sources := sourceContextsFromCandidates(retrievalResult.Sources)

	if len(sources) == 0 {
		return Response{
			Answer:   "未找到相关文档，无法回答该问题。",
			Sources:  []SourceContext{},
			Duration: time.Since(start).String(),
		}, nil
	}

	span.SetAttributes(
		attribute.Int("retrieved_sources", len(sources)),
		attribute.String("retrieval_strategy", string(retrievalResult.Route.Strategy)),
		attribute.Bool("retrieval_cache_hit", retrievalResult.CacheHit),
		attribute.Int("retrieval_partial_errors", len(retrievalResult.PartialErrors)),
	)

	answer, err := s.generateAnswer(ctx, req.Question, sources)
	if err != nil {
		slog.Error("LLM generation failed", "error", err)
		return Response{}, fmt.Errorf("%w: %v", ErrGenerationFailed, err)
	}

	resp := Response{
		Answer:   answer,
		Sources:  sources,
		Duration: time.Since(start).String(),
	}

	slog.Info("query completed",
		"question_len", len(req.Question),
		"sources", len(sources),
		"retrieval_strategy", retrievalResult.Route.Strategy,
		"retrieval_cache_hit", retrievalResult.CacheHit,
		"duration", resp.Duration)

	span.SetAttributes(attribute.Int("answer_len", len(answer)))

	return resp, nil
}

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

func allowedDocumentPermissionsForRole(role string) []string {
	normalizedRole := strings.ToLower(strings.TrimSpace(role))
	if allowed, ok := roleAllowedDocPermissions[normalizedRole]; ok {
		return allowed
	}
	// Fail-safe fallback: unknown or missing role can only access public docs.
	return roleAllowedDocPermissions["readonly"]
}

func (s *Service) generateAnswer(ctx context.Context, question string, sources []SourceContext) (answer string, err error) {
	_, promptSpan := s.tracer.Start(ctx, "Prompt.Build")
	var contextBuilder bytes.Buffer
	for i, src := range sources {
		fmt.Fprintf(&contextBuilder, "[文档%d] (来源: %s)\n%s\n\n", i+1, src.DocID, src.Content)
	}

	systemPrompt := `你是一个专业的知识问答助手。根据提供的参考文档内容回答用户问题。
规则：
1. 只基于提供的文档内容回答，不要编造信息
2. 如果文档中没有相关信息，明确告知用户
3. 回答要简洁、准确、有条理
4. 如果引用了特定文档，标注来源`

	messages := []map[string]string{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": fmt.Sprintf("参考文档：\n%s\n\n用户问题：%s", contextBuilder.String(), question)},
	}

	reqBody := map[string]interface{}{
		"model":      s.llmModel,
		"messages":   messages,
		"max_tokens": s.llmMaxTokens,
	}
	data, _ := json.Marshal(reqBody)
	promptSpan.SetAttributes(
		attribute.Int("prompt.source_count", len(sources)),
		attribute.Int("prompt.context_chars", contextBuilder.Len()),
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

	result, err := s.breaker.Execute(func() (any, error) {
		return s.callLLM(ctx, data)
	})
	if err != nil {
		llmOutcome = classifyLLMOutcome(err)
		return "", err
	}

	answer, ok := result.(string)
	if !ok || strings.TrimSpace(answer) == "" {
		llmOutcome = llmOutcomeInvalidResponse
		return "", newLLMCallError(llmOutcomeInvalidResponse, errors.New("empty LLM response"))
	}

	llmSpan.SetAttributes(attribute.Int("gen_ai.response.chars", len(answer)))
	return answer, nil
}

func (s *Service) callLLM(ctx context.Context, data []byte) (string, error) {

	req, err := http.NewRequestWithContext(ctx, "POST", s.llmEndpoint, bytes.NewReader(data))
	if err != nil {
		return "", newLLMCallError(llmOutcomeRequestError, err)
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
		return "", newLLMCallError(outcome, fmt.Errorf("LLM request failed: %w", err))
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
		return "", newLLMCallError(outcome, fmt.Errorf("LLM API error %d: %s", resp.StatusCode, string(body)))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", newLLMCallError(llmOutcomeInvalidResponse, fmt.Errorf("decode LLM response: %w", err))
	}
	if len(result.Choices) == 0 {
		return "", newLLMCallError(llmOutcomeInvalidResponse, errors.New("empty LLM response"))
	}

	return result.Choices[0].Message.Content, nil
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

func sourceContextsFromCandidates(candidates []retrieval.Candidate) []SourceContext {
	sources := make([]SourceContext, 0, len(candidates))
	for _, c := range candidates {
		sources = append(sources, SourceContext{
			ChunkID:  c.ChunkID,
			DocID:    c.DocID,
			Content:  c.Content,
			Score:    c.Score,
			TenantID: c.TenantID,
		})
	}
	return sources
}

// Package query implements the RAG Query Service (Retrieval + Generation).
package query

import (
	"bytes"
	"context"
	"encoding/json"
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
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/sparse"
	"ai-etl-pipeline/internal/tracing"
)

// Service handles RAG queries: question → hybrid search → LLM generation.
type Service struct {
	cfg           config.Config
	llmEndpoint   string
	llmAPIKey     string
	llmModel      string
	llmMaxTokens  int
	sparseEncoder *sparse.Encoder
	httpClient    *http.Client
	breaker       *circuit.Breaker
	tracer        trace.Tracer
}

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

var roleAllowedDocPermissions = map[string][]string{
	"admin":    {"public", "internal", "confidential"},
	"user":     {"public", "internal"},
	"readonly": {"public"},
}

// NewService creates a Query Service with its own LLM configuration.
func NewService(cfg config.Config) *Service {
	sparseEnc := sparse.NewEncoder(sparse.Params{
		K1:     cfg.SparseK1,
		B:      cfg.SparseB,
		AvgDL:  cfg.SparseAvgDL,
		MinTF:  1,
		MaxDim: 30000,
	})

	return &Service{
		cfg:           cfg,
		llmEndpoint:   normalizeLLMEndpoint(config.EnvStr("LLM_ENDPOINT", "https://api.openai.com/v1/chat/completions")),
		llmAPIKey:     config.EnvSecret("LLM_API_KEY", ""),
		llmModel:      config.EnvStr("LLM_MODEL", "gpt-4o-mini"),
		llmMaxTokens:  config.EnvInt("LLM_MAX_TOKENS", 1024),
		sparseEncoder: sparseEnc,
		httpClient:    &http.Client{Timeout: 30 * time.Second},
		breaker:       circuit.New("llm-api", 5, 60*time.Second),
		tracer:        tracing.Tracer("query"),
	}
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
	if req.Question == "" {
		http.Error(w, "question is required", http.StatusBadRequest)
		return
	}
	if req.TopK <= 0 {
		req.TopK = 5
	}
	tenantID := auth.GetTenantID(r.Context())
	if tenantID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	role := auth.GetPermission(r.Context())
	allowedPermissions := allowedDocumentPermissionsForRole(role)

	span.SetAttributes(
		attribute.String("tenant_id", tenantID),
		attribute.String("permission_role", role),
		attribute.Int("allowed_permission_levels", len(allowedPermissions)),
		attribute.Int("top_k", req.TopK),
		attribute.Int("question_len", len(req.Question)),
	)

	start := time.Now()

	// 1. Generate dense vector
	denseVector, err := s.embedQuestion(ctx, req.Question)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "embedding failed")
		slog.Error("embed question failed", "error", err)
		http.Error(w, "embedding failed", http.StatusInternalServerError)
		return
	}

	// 2. Generate sparse vector (BM25)
	sparseVector := s.sparseEncoder.Encode(req.Question)

	// 3. Qdrant Hybrid Search (Dense + Sparse + RRF fusion)
	sources, err := s.hybridSearch(ctx, denseVector, sparseVector, req.TopK, tenantID, allowedPermissions)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "search failed")
		slog.Error("hybrid search failed", "error", err)
		http.Error(w, "search failed", http.StatusInternalServerError)
		return
	}

	if len(sources) == 0 {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(Response{
			Answer:   "未找到相关文档，无法回答该问题。",
			Sources:  []SourceContext{},
			Duration: time.Since(start).String(),
		})
		return
	}

	span.SetAttributes(attribute.Int("retrieved_sources", len(sources)))

	// 4. LLM generation
	answer, err := s.generateAnswer(ctx, req.Question, sources)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "generation failed")
		slog.Error("LLM generation failed", "error", err)
		http.Error(w, "generation failed", http.StatusInternalServerError)
		return
	}

	resp := Response{
		Answer:   answer,
		Sources:  sources,
		Duration: time.Since(start).String(),
	}

	slog.Info("query completed",
		"question_len", len(req.Question),
		"sources", len(sources),
		"duration", resp.Duration)

	span.SetAttributes(attribute.Int("answer_len", len(answer)))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
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

func (s *Service) embedQuestion(ctx context.Context, question string) ([]float64, error) {
	ollamaNative := isOllamaNativeEndpoint(s.cfg.EmbedEndpoint)
	reqBody := map[string]interface{}{"model": s.cfg.EmbedModel}
	if ollamaNative {
		reqBody["prompt"] = question
	} else {
		reqBody["input"] = question
	}
	data, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, "POST", s.cfg.EmbedEndpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if !ollamaNative && s.cfg.EmbedAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.EmbedAPIKey)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("embed API error %d: %s", resp.StatusCode, string(body))
	}

	if ollamaNative {
		var result struct {
			Embedding  []float64   `json:"embedding"`
			Embeddings [][]float64 `json:"embeddings"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("decode ollama embed response: %w", err)
		}
		if len(result.Embedding) > 0 {
			return result.Embedding, nil
		}
		if len(result.Embeddings) > 0 && len(result.Embeddings[0]) > 0 {
			return result.Embeddings[0], nil
		}
		return nil, fmt.Errorf("empty ollama embedding response")
	}

	var result struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("decode embed response: %w", err)
	}
	if len(result.Data) == 0 {
		return nil, fmt.Errorf("empty embedding response")
	}

	return result.Data[0].Embedding, nil
}

func isOllamaNativeEndpoint(endpoint string) bool {
	return strings.Contains(endpoint, "/api/embeddings") || strings.Contains(endpoint, "/api/embed")
}

func allowedDocumentPermissionsForRole(role string) []string {
	normalizedRole := strings.ToLower(strings.TrimSpace(role))
	if allowed, ok := roleAllowedDocPermissions[normalizedRole]; ok {
		return allowed
	}
	// Fail-safe fallback: unknown or missing role can only access public docs.
	return roleAllowedDocPermissions["readonly"]
}

func (s *Service) hybridSearch(ctx context.Context, dense []float64, sv model.SparseVector, topK int, tenantID string, allowedPermissions []string) ([]SourceContext, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant_id is required for query")
	}
	if len(allowedPermissions) == 0 {
		allowedPermissions = roleAllowedDocPermissions["readonly"]
	}

	query := map[string]interface{}{
		"prefetch": []map[string]interface{}{
			{
				"query": dense,
				"using": "dense",
				"limit": topK * 3,
			},
			{
				"query": map[string]interface{}{
					"indices": sv.Indices,
					"values":  sv.Values,
				},
				"using": "sparse",
				"limit": topK * 3,
			},
		},
		"query":        map[string]string{"fusion": "rrf"},
		"limit":        topK,
		"with_payload": true,
		"filter": map[string]interface{}{
			"must": []map[string]interface{}{
				{
					"key":   "tenant_id",
					"match": map[string]string{"value": tenantID},
				},
				{
					"key": "permission",
					"match": map[string]interface{}{
						"any": allowedPermissions,
					},
				},
			},
		},
	}

	data, _ := json.Marshal(query)
	url := fmt.Sprintf("%s/collections/%s/points/query", s.cfg.StoreEndpoint, s.cfg.StoreCollection)

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.cfg.StoreAPIKey != "" {
		req.Header.Set("api-key", s.cfg.StoreAPIKey)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qdrant query failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("qdrant query error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Result struct {
			Points []struct {
				Score   float64                `json:"score"`
				Payload map[string]interface{} `json:"payload"`
			} `json:"points"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode qdrant response: %w", err)
	}

	sources := make([]SourceContext, 0, len(result.Result.Points))
	for _, p := range result.Result.Points {
		sc := SourceContext{Score: p.Score}
		if v, ok := p.Payload["chunk_id"].(string); ok {
			sc.ChunkID = v
		}
		if v, ok := p.Payload["doc_id"].(string); ok {
			sc.DocID = v
		}
		if v, ok := p.Payload["content"].(string); ok {
			sc.Content = v
		}
		if v, ok := p.Payload["tenant_id"].(string); ok {
			sc.TenantID = v
		}
		sources = append(sources, sc)
	}

	return sources, nil
}

func (s *Service) generateAnswer(ctx context.Context, question string, sources []SourceContext) (string, error) {
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

	req, err := http.NewRequestWithContext(ctx, "POST", s.llmEndpoint, bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.llmAPIKey)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("LLM API error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("decode LLM response: %w", err)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("empty LLM response")
	}

	return result.Choices[0].Message.Content, nil
}

package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/sparse"
)

// Engine orchestrates embedding, cache lookup, routing, scatter-gather
// retrieval, fusion, and optional reranking.
type Engine struct {
	cfg           config.Config
	sparseEncoder *sparse.Encoder
	httpClient    *http.Client
	retrievers    map[string]Retriever
	cache         SourceCache
	reranker      Reranker
	timeout       time.Duration
	candidateK    int
	defaultTopK   int
}

// NewEngine wires the retrieval engine from environment-backed configuration.
func NewEngine(cfg config.Config) *Engine {
	timeout := cfg.RetrievalTimeout
	if timeout <= 0 {
		timeout = 300 * time.Millisecond
	}
	candidateK := cfg.RetrievalCandidateK
	if candidateK <= 0 {
		candidateK = 50
	}
	defaultTopK := cfg.RetrievalFinalTopK
	if defaultTopK <= 0 {
		defaultTopK = 5
	}

	client := &http.Client{Timeout: 30 * time.Second}
	retrievers := map[string]Retriever{
		SourceQdrant: NewQdrantRetriever(cfg.StoreEndpoint, cfg.StoreAPIKey, cfg.StoreCollection, client),
	}
	if cfg.RetrievalEnableES && strings.TrimSpace(cfg.ESAddress) != "" && strings.TrimSpace(cfg.ESIndex) != "" {
		retrievers[SourceElasticsearch] = NewElasticRetriever(cfg.ESAddress, cfg.ESAPIKey, cfg.ESIndex, client)
	}

	var cache SourceCache = NoopCache{}
	if cfg.SemanticCacheEnabled {
		redisCache, err := NewRedisSemanticCache(
			cfg.RedisAddr,
			cfg.RedisPassword,
			cfg.RedisDB,
			cfg.SemanticCacheTTL,
			cfg.SemanticCacheThreshold,
			cfg.SemanticCacheMaxEntries,
		)
		if err != nil {
			slog.Warn("semantic cache disabled", "error", err)
		} else {
			cache = redisCache
		}
	}

	var reranker Reranker = NoopReranker{}
	if cfg.RetrievalEnableRerank && strings.TrimSpace(cfg.RerankEndpoint) != "" {
		reranker = NewHTTPReranker(cfg.RerankEndpoint, cfg.RerankAPIKey, cfg.RerankModel, client)
	}

	return &Engine{
		cfg: cfg,
		sparseEncoder: sparse.NewEncoder(sparse.Params{
			K1:     cfg.SparseK1,
			B:      cfg.SparseB,
			AvgDL:  cfg.SparseAvgDL,
			MinTF:  1,
			MaxDim: 30000,
		}),
		httpClient:  client,
		retrievers:  retrievers,
		cache:       cache,
		reranker:    reranker,
		timeout:     timeout,
		candidateK:  candidateK,
		defaultTopK: defaultTopK,
	}
}

// Retrieve returns ranked document chunks for a user question.
func (e *Engine) Retrieve(ctx context.Context, req Request) (Result, error) {
	start := time.Now()
	if strings.TrimSpace(req.Question) == "" {
		return Result{}, fmt.Errorf("question is required")
	}
	if strings.TrimSpace(req.TenantID) == "" {
		return Result{}, fmt.Errorf("tenant_id is required")
	}
	if len(req.AllowedPermissions) == 0 {
		req.AllowedPermissions = []string{"public"}
	}
	if req.TopK <= 0 {
		req.TopK = e.defaultTopK
	}
	candidateLimit := e.candidateK
	if minLimit := req.TopK * 3; candidateLimit < minLimit {
		candidateLimit = minLimit
	}

	denseVector, err := e.embedQuestion(ctx, req.Question)
	if err != nil {
		return Result{}, err
	}

	cacheKey := CacheKey{
		TenantID:           req.TenantID,
		AllowedPermissions: req.AllowedPermissions,
		Question:           req.Question,
		TopK:               req.TopK,
	}
	if sources, ok, err := e.cache.Lookup(ctx, cacheKey, denseVector); err != nil {
		slog.Warn("retrieval cache lookup failed", "tenant_id", req.TenantID, "error", err)
	} else if ok {
		return Result{
			Sources:  topCandidates(sources, req.TopK),
			Route:    RouteQuery(req.Question, e.hasRetriever(SourceElasticsearch)),
			CacheHit: true,
			Duration: time.Since(start),
		}, nil
	}

	sparseVector := e.sparseEncoder.Encode(req.Question)
	route := RouteQuery(req.Question, e.hasRetriever(SourceElasticsearch))
	active := e.activeRetrievers(route)
	if len(active) == 0 {
		return Result{}, fmt.Errorf("no retrieval backends enabled")
	}

	searchCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	type backendResult struct {
		name       string
		candidates []Candidate
		err        error
	}
	resultCh := make(chan backendResult, len(active))
	var wg sync.WaitGroup
	for _, retriever := range active {
		wg.Add(1)
		go func(r Retriever) {
			defer wg.Done()
			candidates, err := r.Search(searchCtx, SearchRequest{
				Question:           req.Question,
				DenseVector:        denseVector,
				SparseVector:       sparseVector,
				Limit:              candidateLimit,
				TenantID:           req.TenantID,
				AllowedPermissions: req.AllowedPermissions,
				ExactSchemaFields:  e.cfg.RetrievalExactSchemaFields,
			})
			resultCh <- backendResult{name: r.Name(), candidates: candidates, err: err}
		}(retriever)
	}
	wg.Wait()
	close(resultCh)

	results := make(map[string][]Candidate)
	partialErrors := make([]string, 0)
	for result := range resultCh {
		if result.err != nil {
			partialErrors = append(partialErrors, fmt.Sprintf("%s: %v", result.name, result.err))
			continue
		}
		if len(result.candidates) > 0 {
			results[result.name] = result.candidates
		}
	}
	if len(results) == 0 && len(partialErrors) > 0 {
		return Result{Route: route, PartialErrors: partialErrors, Duration: time.Since(start)}, fmt.Errorf("all retrieval backends failed: %s", strings.Join(partialErrors, "; "))
	}

	fused := Fuse(results, route, candidateLimit)
	if len(fused) == 0 {
		return Result{Route: route, PartialErrors: partialErrors, Duration: time.Since(start)}, nil
	}

	ranked := topCandidates(fused, req.TopK)
	if e.rerankerConfigured() {
		decision := planRerank(e.cfg.RetrievalRerankPolicy, route, req.Question, fused)
		rerankErr := ""
		if decision.ShouldRerank {
			rerankTopK := req.TopK
			if decision.ProtectExactMatches {
				rerankTopK = len(fused)
			}
			reranked, err := e.reranker.Rerank(ctx, req.Question, fused, rerankTopK)
			if err != nil {
				partialErrors = append(partialErrors, "reranker: "+err.Error())
				rerankErr = err.Error()
			} else {
				if decision.ProtectExactMatches {
					ranked = protectExactMatches(reranked, fused, decision.Evidence, req.TopK)
				} else {
					ranked = topCandidates(reranked, req.TopK)
				}
			}
		}
		e.logRerankDecision(req, route, decision, fused, ranked, rerankErr)
	} else if decision := planRerank(e.cfg.RetrievalRerankPolicy, route, req.Question, fused); decision.ProtectExactMatches {
		decision.ShouldRerank = false
		decision.Reason = "reranker_not_configured_exact_candidate_pinned"
		ranked = protectExactMatches(fused, fused, decision.Evidence, req.TopK)
		e.logRerankDecision(req, route, decision, fused, ranked, "")
	}

	if err := e.cache.Store(ctx, cacheKey, denseVector, ranked); err != nil {
		slog.Warn("retrieval cache store failed", "tenant_id", req.TenantID, "error", err)
	}

	return Result{
		Sources:       ranked,
		Route:         route,
		CacheHit:      false,
		PartialErrors: partialErrors,
		Duration:      time.Since(start),
	}, nil
}

func (e *Engine) Close() error {
	e.httpClient.CloseIdleConnections()
	return e.cache.Close()
}

func (e *Engine) hasRetriever(name string) bool {
	_, ok := e.retrievers[name]
	return ok
}

func (e *Engine) rerankerConfigured() bool {
	return e.cfg.RetrievalEnableRerank && strings.TrimSpace(e.cfg.RerankEndpoint) != ""
}

func (e *Engine) activeRetrievers(route Route) []Retriever {
	active := make([]Retriever, 0, 2)
	if route.UseQdrant {
		if r, ok := e.retrievers[SourceQdrant]; ok {
			active = append(active, r)
		}
	}
	if route.UseElastic {
		if r, ok := e.retrievers[SourceElasticsearch]; ok {
			active = append(active, r)
		}
	}
	return active
}

func (e *Engine) logRerankDecision(req Request, route Route, decision rerankDecision, fused, ranked []Candidate, rerankErr string) {
	topFused := firstCandidateID(fused)
	topFinal := firstCandidateID(ranked)
	previousRank := rankOfCandidate(fused, topFinal)
	rankDelta := 0
	if previousRank > 0 {
		rankDelta = previousRank - 1
	}

	slog.Info("retrieval rerank decision",
		"tenant_id", req.TenantID,
		"strategy", route.Strategy,
		"rerank_policy", strings.ToLower(strings.TrimSpace(e.cfg.RetrievalRerankPolicy)),
		"should_rerank", decision.ShouldRerank,
		"reason", decision.Reason,
		"protect_exact_matches", decision.ProtectExactMatches,
		"exact_tokens_count", len(decision.Evidence.QueryTokens),
		"exact_token_hashes", decision.Evidence.TokenHashes(),
		"exact_match_candidates", decision.Evidence.MatchedCandidateCount(),
		"top_fused_chunk_id", topFused,
		"top_final_chunk_id", topFinal,
		"top_fused_contains_exact", decision.Evidence.TopCandidateMatched,
		"top_final_contains_exact", topCandidateMatches(decision.Evidence, ranked),
		"top_final_previous_rank", previousRank,
		"rank_delta", rankDelta,
		"rerank_error", rerankErr != "",
	)
}

func firstCandidateID(candidates []Candidate) string {
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0].ChunkID
}

func rankOfCandidate(candidates []Candidate, id string) int {
	if id == "" {
		return 0
	}
	for i, candidate := range candidates {
		if candidate.ChunkID == id {
			return i + 1
		}
	}
	return 0
}

func topCandidateMatches(evidence exactEvidence, candidates []Candidate) bool {
	if len(candidates) == 0 {
		return false
	}
	return evidence.MatchesCandidate(candidates[0])
}

func (e *Engine) embedQuestion(ctx context.Context, question string) ([]float64, error) {
	ollamaNative := isOllamaNativeEndpoint(e.cfg.EmbedEndpoint)
	reqBody := map[string]interface{}{"model": e.cfg.EmbedModel}
	if ollamaNative {
		reqBody["prompt"] = question
	} else {
		reqBody["input"] = question
	}
	data, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal embed request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.cfg.EmbedEndpoint, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if !ollamaNative && e.cfg.EmbedAPIKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.cfg.EmbedAPIKey)
	}

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed request failed: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	if resp.StatusCode != http.StatusOK {
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

func trimRightSlash(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

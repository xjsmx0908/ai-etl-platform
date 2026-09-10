package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/indexmanifest"
	"ai-etl-pipeline/internal/sparse"
	platformtracing "ai-etl-pipeline/internal/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
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
	visibility    VisibilityResolver
	timeout       time.Duration
	candidateK    int
	defaultTopK   int
	tracer        trace.Tracer
}

// WithVisibilityResolver enables active-generation filtering for backend and
// cached candidates. Query API wiring must provide the PostgreSQL adapter.
func (e *Engine) WithVisibilityResolver(resolver VisibilityResolver) *Engine {
	e.visibility = resolver
	return e
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
			cfg.RedisCacheAddr,
			cfg.RedisCachePassword,
			cfg.RedisCacheDB,
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
		tracer:      platformtracing.Tracer("retrieval"),
	}
}

// InvalidateCache drops cached retrieval results. Called by the query API after
// a document write or delete so stale answers are not served from cache.
func (e *Engine) InvalidateCache(ctx context.Context) error {
	return e.cache.Flush(ctx)
}

// Retrieve returns ranked document chunks for a user question.
func (e *Engine) Retrieve(ctx context.Context, req Request) (result Result, err error) {
	tracer := e.tracer
	if tracer == nil {
		tracer = platformtracing.Tracer("retrieval")
	}
	ctx, span := tracer.Start(ctx, "RetrievalEngine.Retrieve")
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "retrieval failed")
		}
		span.End()
	}()

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
	span.SetAttributes(
		attribute.Int("retrieval.top_k", req.TopK),
		attribute.Int("retrieval.candidate_limit", candidateLimit),
		attribute.Int("retrieval.question_chars", len(req.Question)),
	)

	embedCtx, embedSpan := tracer.Start(ctx, "Retrieval.EmbedQuery")
	denseVector, err := e.embedQuestion(embedCtx, req.Question)
	if err != nil {
		embedSpan.RecordError(err)
		embedSpan.SetStatus(codes.Error, "query embedding failed")
		embedSpan.End()
		return Result{}, err
	}
	embedSpan.SetAttributes(attribute.Int("embedding.vector_dimension", len(denseVector)))
	embedSpan.End()

	cacheKey := CacheKey{
		TenantID:           req.TenantID,
		AllowedPermissions: req.AllowedPermissions,
		Question:           req.Question,
		TopK:               req.TopK,
		KnowledgeBaseID:    req.KnowledgeBaseID,
		ApplicableScope:    req.ApplicableScope,
	}
	cacheDiagnostics := len(req.DiagnosticRequiredDocIDs) > 0
	cacheCtx, cacheSpan := tracer.Start(ctx, "Retrieval.CacheLookup")
	if cacheDiagnostics {
		cacheSpan.SetAttributes(attribute.Bool("cache.skipped_for_diagnostics", true))
		cacheSpan.End()
	} else if sources, ok, err := e.cache.Lookup(cacheCtx, cacheKey, denseVector); err != nil {
		cacheSpan.RecordError(err)
		cacheSpan.SetStatus(codes.Error, "semantic cache lookup failed")
		cacheSpan.End()
		slog.Warn("retrieval cache lookup failed", "tenant_id", req.TenantID, "error", err)
	} else if ok && len(sources) > 0 {
		sources, err = e.visibleCandidates(cacheCtx, req.TenantID, sources)
		if err != nil {
			cacheSpan.RecordError(err)
			cacheSpan.SetStatus(codes.Error, "generation visibility failed")
			cacheSpan.End()
			return Result{}, err
		}
		if len(sources) == 0 {
			cacheSpan.SetAttributes(attribute.Bool("cache.hit", true), attribute.Int("cache.result_count", 0))
			cacheSpan.End()
			goto cacheMiss
		}
		selected, diversity := diversifyCandidates(sources, req.TopK, defaultMaxChunksPerDocument)
		cacheSpan.SetAttributes(attribute.Bool("cache.hit", true), attribute.Int("cache.result_count", len(sources)))
		cacheSpan.End()
		span.SetAttributes(attribute.Bool("retrieval.cache_hit", true))
		return Result{
			Sources:                    selected,
			Route:                      RouteQuery(req.Question, e.hasRetriever(SourceElasticsearch)),
			CacheHit:                   true,
			DeduplicatedCandidateCount: diversity.DeduplicatedCount,
			SelectedContextCount:       len(selected),
			UniqueDocumentCount:        diversity.UniqueDocumentCount,
			Duration:                   time.Since(start),
		}, nil
	} else {
		cacheSpan.SetAttributes(attribute.Bool("cache.hit", false))
		cacheSpan.End()
	}

cacheMiss:
	_, routeSpan := tracer.Start(ctx, "Retrieval.Route")
	sparseVector := e.sparseEncoder.Encode(req.Question)
	route := RouteQuery(req.Question, e.hasRetriever(SourceElasticsearch))
	active := e.activeRetrievers(route)
	routeSpan.SetAttributes(
		attribute.String("retrieval.strategy", string(route.Strategy)),
		attribute.Int("retrieval.backend_count", len(active)),
	)
	routeSpan.End()
	span.SetAttributes(
		attribute.String("retrieval.strategy", string(route.Strategy)),
		attribute.Bool("retrieval.cache_hit", false),
	)
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
			backendCtx, backendSpan := tracer.Start(searchCtx, "Retrieval.BackendSearch",
				trace.WithSpanKind(trace.SpanKindClient),
				trace.WithAttributes(attribute.String("retrieval.backend", r.Name())),
			)
			candidates, err := r.Search(backendCtx, SearchRequest{
				Question:           req.Question,
				DenseVector:        denseVector,
				SparseVector:       sparseVector,
				Limit:              candidateLimit,
				TenantID:           req.TenantID,
				AllowedPermissions: req.AllowedPermissions,
				ExactSchemaFields:  e.cfg.RetrievalExactSchemaFields,
				KnowledgeBaseID:    req.KnowledgeBaseID,
				ApplicableScope:    req.ApplicableScope,
				TitleMatchDocIDs:   req.TitleMatchDocIDs,
			})
			backendSpan.SetAttributes(attribute.Int("retrieval.candidate_count", len(candidates)))
			if err != nil {
				backendSpan.RecordError(err)
				backendSpan.SetStatus(codes.Error, "retrieval backend failed")
			}
			backendSpan.End()
			resultCh <- backendResult{name: r.Name(), candidates: candidates, err: err}
		}(retriever)
	}
	wg.Wait()
	close(resultCh)

	results := make(map[string][]Candidate)
	backendCandidateCounts := make(map[string]int, len(active))
	partialErrors := make([]string, 0)
	for result := range resultCh {
		if result.err != nil {
			partialErrors = append(partialErrors, fmt.Sprintf("%s: %v", result.name, result.err))
			continue
		}
		results[result.name] = result.candidates
	}
	if len(results) == 0 && len(partialErrors) > 0 {
		result := Result{Route: route, PartialErrors: partialErrors, Duration: time.Since(start)}
		if cacheDiagnostics {
			diagnostics := DiagnoseStages(req.DiagnosticRequiredDocIDs, results, nil, nil)
			result.StageDiagnostics = &diagnostics
		}
		return result, fmt.Errorf("all retrieval backends failed: %s", strings.Join(partialErrors, "; "))
	}
	results, err = e.visibleBackendResults(ctx, req.TenantID, results)
	if err != nil {
		return Result{}, err
	}
	for name, candidates := range results {
		backendCandidateCounts[name] = len(candidates)
	}

	_, fusionSpan := tracer.Start(ctx, "Retrieval.Fusion")
	fused := Fuse(results, route, candidateLimit)
	fusedCandidateCount := len(fused)
	fusionSpan.SetAttributes(
		attribute.Int("retrieval.backend_result_count", len(results)),
		attribute.Int("retrieval.fused_candidate_count", len(fused)),
	)
	fusionSpan.End()
	if len(fused) == 0 {
		result := Result{Route: route, BackendCandidateCounts: backendCandidateCounts, PartialErrors: partialErrors, Duration: time.Since(start)}
		if cacheDiagnostics {
			diagnostics := DiagnoseStages(req.DiagnosticRequiredDocIDs, results, fused, nil)
			result.StageDiagnostics = &diagnostics
		}
		return result, nil
	}

	ranked := append([]Candidate(nil), fused...)
	if e.rerankerConfigured() {
		decision := planRerank(e.cfg.RetrievalRerankPolicy, route, req.Question, fused)
		span.SetAttributes(
			attribute.Bool("retrieval.rerank_applied", decision.ShouldRerank),
			attribute.String("retrieval.rerank_reason", decision.Reason),
		)
		rerankErr := ""
		if decision.ShouldRerank {
			rerankTopK := len(fused)
			rerankCtx, rerankSpan := tracer.Start(ctx, "Retrieval.Rerank",
				trace.WithSpanKind(trace.SpanKindClient),
				trace.WithAttributes(
					attribute.Int("rerank.candidate_count", len(fused)),
					attribute.Int("rerank.top_k", rerankTopK),
					attribute.Bool("rerank.protect_exact_matches", decision.ProtectExactMatches),
				),
			)
			reranked, err := e.reranker.Rerank(rerankCtx, req.Question, fused, rerankTopK)
			if err != nil {
				rerankSpan.RecordError(err)
				rerankSpan.SetStatus(codes.Error, "reranker failed")
				partialErrors = append(partialErrors, "reranker: "+err.Error())
				rerankErr = err.Error()
			} else {
				rerankSpan.SetAttributes(attribute.Int("rerank.result_count", len(reranked)))
				if decision.ProtectExactMatches {
					ranked = protectExactMatches(reranked, fused, decision.Evidence, len(fused))
				} else {
					ranked = reranked
				}
			}
			rerankSpan.End()
		}
		if decision.ProtectExactMatches && (!decision.ShouldRerank || rerankErr != "") {
			if rerankErr != "" {
				decision.Reason = "reranker_failed_exact_candidate_pinned"
			}
			ranked = protectExactMatches(fused, fused, decision.Evidence, len(fused))
		}
		e.logRerankDecision(req, route, decision, fused, ranked, rerankErr)
	} else if decision := planRerank(e.cfg.RetrievalRerankPolicy, route, req.Question, fused); decision.ProtectExactMatches {
		decision.ShouldRerank = false
		decision.Reason = "reranker_not_configured_exact_candidate_pinned"
		ranked = protectExactMatches(fused, fused, decision.Evidence, len(fused))
		e.logRerankDecision(req, route, decision, fused, ranked, "")
	}
	ranked, diversity := diversifyCandidates(ranked, req.TopK, defaultMaxChunksPerDocument)

	cacheStoreCtx, cacheStoreSpan := tracer.Start(ctx, "Retrieval.CacheStore")
	// Never cache an empty result set: a cached empty hit would make every
	// subsequent identical question answer with "no relevant documents", even
	// after the corpus grows. A no-result answer is cheap to recompute anyway.
	if len(ranked) > 0 && !cacheDiagnostics {
		if err := e.cache.Store(cacheStoreCtx, cacheKey, denseVector, ranked); err != nil {
			cacheStoreSpan.RecordError(err)
			cacheStoreSpan.SetStatus(codes.Error, "semantic cache store failed")
			slog.Warn("retrieval cache store failed", "tenant_id", req.TenantID, "error", err)
		}
	}
	cacheStoreSpan.SetAttributes(attribute.Int("cache.result_count", len(ranked)))
	cacheStoreSpan.End()
	span.SetAttributes(
		attribute.Int("retrieval.result_count", len(ranked)),
		attribute.Int("retrieval.partial_error_count", len(partialErrors)),
	)

	result = Result{
		Sources:                    ranked,
		Route:                      route,
		CacheHit:                   false,
		BackendCandidateCounts:     backendCandidateCounts,
		FusedCandidateCount:        fusedCandidateCount,
		DeduplicatedCandidateCount: diversity.DeduplicatedCount,
		SelectedContextCount:       len(ranked),
		UniqueDocumentCount:        diversity.UniqueDocumentCount,
		PartialErrors:              partialErrors,
		Duration:                   time.Since(start),
	}
	if cacheDiagnostics {
		diagnostics := DiagnoseStages(req.DiagnosticRequiredDocIDs, results, fused, ranked)
		result.StageDiagnostics = &diagnostics
	}
	return result, nil
}

func (e *Engine) Close() error {
	e.httpClient.CloseIdleConnections()
	return e.cache.Close()
}

func (e *Engine) visibleCandidates(ctx context.Context, tenantID string, candidates []Candidate) ([]Candidate, error) {
	if e.visibility == nil || len(candidates) == 0 {
		return candidates, nil
	}
	refs := make([]indexmanifest.GenerationReference, len(candidates))
	for i, candidate := range candidates {
		refs[i] = indexmanifest.GenerationReference{
			DocumentID:        candidate.DocID,
			DocumentVersionID: candidate.DocumentVersionID,
			GenerationID:      candidate.GenerationID,
		}
	}
	visible, err := e.visibility.ResolveVisibility(ctx, tenantID, refs)
	if err != nil {
		return nil, fmt.Errorf("resolve candidate generation visibility: %w", err)
	}
	if len(visible) != len(candidates) {
		return nil, fmt.Errorf("resolve candidate generation visibility: invalid result count")
	}
	filtered := make([]Candidate, 0, len(candidates))
	for i, candidate := range candidates {
		if visible[i] {
			filtered = append(filtered, candidate)
		}
	}
	return filtered, nil
}

func (e *Engine) visibleBackendResults(ctx context.Context, tenantID string, results map[string][]Candidate) (map[string][]Candidate, error) {
	if e.visibility == nil {
		return results, nil
	}
	names := make([]string, 0, len(results))
	for name := range results {
		names = append(names, name)
	}
	sort.Strings(names)
	type locatedCandidate struct {
		backend   string
		candidate Candidate
	}
	all := make([]locatedCandidate, 0)
	for _, name := range names {
		for _, candidate := range results[name] {
			all = append(all, locatedCandidate{backend: name, candidate: candidate})
		}
	}
	refs := make([]indexmanifest.GenerationReference, len(all))
	for i, item := range all {
		refs[i] = indexmanifest.GenerationReference{
			DocumentID:        item.candidate.DocID,
			DocumentVersionID: item.candidate.DocumentVersionID,
			GenerationID:      item.candidate.GenerationID,
		}
	}
	visible, err := e.visibility.ResolveVisibility(ctx, tenantID, refs)
	if err != nil {
		return nil, fmt.Errorf("resolve candidate generation visibility: %w", err)
	}
	if len(visible) != len(all) {
		return nil, fmt.Errorf("resolve candidate generation visibility: invalid result count")
	}
	filtered := make(map[string][]Candidate, len(results))
	for _, name := range names {
		filtered[name] = make([]Candidate, 0, len(results[name]))
	}
	for i, item := range all {
		if visible[i] {
			filtered[item.backend] = append(filtered[item.backend], item.candidate)
		}
	}
	return filtered, nil
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

// Warm pins the embedding model on Ollama-native endpoints so the first
// user query does not pay a cold load.
func (e *Engine) Warm(ctx context.Context) error {
	if e == nil || !isOllamaNativeEndpoint(e.cfg.EmbedEndpoint) {
		return nil
	}
	_, err := e.embedQuestion(ctx, "warmup")
	return err
}

func (e *Engine) embedQuestion(ctx context.Context, question string) ([]float64, error) {
	ollamaNative := isOllamaNativeEndpoint(e.cfg.EmbedEndpoint)
	reqBody := map[string]interface{}{"model": e.cfg.EmbedModel}
	if ollamaNative {
		reqBody["prompt"] = question
		if keepAlive := strings.TrimSpace(e.cfg.EmbedKeepAlive); keepAlive != "" {
			reqBody["keep_alive"] = keepAlive
		}
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
	platformtracing.InjectHTTPHeaders(ctx, req)

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

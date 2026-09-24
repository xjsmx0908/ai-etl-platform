// Package query implements the RAG Query Service (Retrieval + Generation).
package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/circuit"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/knowledgecatalog"
	"ai-etl-pipeline/internal/releasecenter"
	"ai-etl-pipeline/internal/retrieval"
	"ai-etl-pipeline/internal/tracing"
)

// Service handles RAG queries: question → hybrid search → LLM generation.
type Service struct {
	cfg                config.Config
	llmEndpoint        string
	llmAPIKey          string
	llmModel           string
	llmMaxTokens       int
	llmMaxContextChars int
	retriever          *retrieval.Engine
	httpClient         *http.Client
	breaker            *circuit.Breaker
	tracer             trace.Tracer
	llmObserver        LLMObserver
	systemPrompt       string
	promptVersion      string

	// governance is the optional document registry used to drop retired
	// (superseded/archived) candidates and to disclose conflicting sources. It is
	// nil in the worker and in tests, in which case both features are skipped —
	// retrieval behaviour is then exactly as before.
	governance governanceLookup
	documents  documentLister
	catalog    *knowledgecatalog.Catalog

	sensitiveAnswerHook func(context.Context, AccessContext, string)

	// Per-1k-token USD prices for cost estimation (LLM_PRICE_*). Zero means no
	// cost is reported.
	promptPricePer1K     float64
	completionPricePer1K float64
}

// governanceLookup is the narrow slice of docstore.Store that retrieval needs.
// Declaring it here keeps query decoupled from the full registry surface and
// makes the dependency trivial to fake in tests.
type governanceLookup interface {
	GovernanceByDocIDs(ctx context.Context, tenantID string, docIDs []string) (map[string]docstore.Governance, error)
}

type documentLister interface {
	List(ctx context.Context, q docstore.ListQuery) ([]docstore.Document, int, error)
}

// WithGovernance attaches the document registry so retrieval can filter retired
// documents and disclose conflicts. Wired in cmd/api; safe to omit.
func (s *Service) WithGovernance(g governanceLookup) *Service {
	s.governance = g
	if lister, ok := g.(documentLister); ok {
		s.documents = lister
	}
	return s
}

func (s *Service) WithDocuments(d documentLister) *Service {
	s.documents = d
	return s
}

// WithKnowledgeCatalog enables mandatory pre-retrieval space resolution and
// fail-closed publication filtering.
func (s *Service) WithKnowledgeCatalog(catalog *knowledgecatalog.Catalog) *Service {
	s.catalog = catalog
	return s
}

// WithReleaseVisibility makes every backend and semantic-cache candidate pass
// the exact published-release gate before it can become query evidence.
func (s *Service) WithReleaseVisibility(resolver retrieval.VisibilityResolver) *Service {
	s.retriever.WithVisibilityResolver(resolver)
	return s
}

// WithSensitiveAnswerHook records blocked answers that matched deterministic
// secret/id/phone patterns. The raw answer must not be persisted by the hook.
func (s *Service) WithSensitiveAnswerHook(hook func(context.Context, AccessContext, string)) *Service {
	s.sensitiveAnswerHook = hook
	return s
}

// ResolveKnowledgeSpace exposes the catalog decision to the upload handler so
// reads and writes share one policy module.
func (s *Service) ResolveKnowledgeSpace(ctx context.Context, requestedSpaceID string, access AccessContext, capability knowledgecatalog.Capability) (knowledgecatalog.Space, error) {
	if s.catalog == nil {
		return knowledgecatalog.Space{ID: "user-uploads", Slug: "user-uploads", Name: "用户上传", Kind: knowledgecatalog.SpaceKindProduction, Active: true}, nil
	}
	return s.catalog.Resolve(ctx, knowledgecatalog.Principal{TenantID: access.TenantID, UserID: access.UserID, Role: access.Role}, requestedSpaceID, capability)
}

func (s *Service) ListKnowledgeSpaces(ctx context.Context, access AccessContext) ([]knowledgecatalog.Space, error) {
	if s.catalog == nil {
		return []knowledgecatalog.Space{{ID: "user-uploads", Name: "用户上传", Kind: knowledgecatalog.SpaceKindProduction, IsDefault: true, Active: true}}, nil
	}
	return s.catalog.List(ctx, knowledgecatalog.Principal{TenantID: access.TenantID, UserID: access.UserID, Role: access.Role})
}

// LLMObserver receives low-cardinality LLM request outcomes for metrics adapters.
type LLMObserver interface {
	RecordLLMRequest(model, outcome string, duration time.Duration)
	// RecordLLMTokens reports prompt/completion token consumption after a
	// successful LLM call. Implementations that do not track tokens may ignore it.
	RecordLLMTokens(model string, promptTokens, completionTokens int64)
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
	Question         string `json:"question"`
	TopK             int    `json:"top_k,omitempty"`
	KnowledgeSpaceID string `json:"knowledge_space_id,omitempty"`
	// DiagnosticRequiredDocIDs is accepted only when the controlled evaluation
	// diagnostic flag is enabled; responses contain aggregate counts only.
	DiagnosticRequiredDocIDs []string `json:"diagnostic_required_doc_ids,omitempty"`
	// RetrievalOnly is honored only with the controlled diagnostics flag. It
	// returns selected evidence without invoking answer generation.
	RetrievalOnly bool `json:"retrieval_only,omitempty"`
	// Deprecated compatibility fields. They are never used to authorize or
	// automatically select a knowledge space.
	KnowledgeBaseID string `json:"knowledge_base_id,omitempty"`
	ApplicableScope string `json:"applicable_scope,omitempty"`
}

// QueryProgress is emitted by the streaming endpoint at meaningful pipeline
// boundaries. It intentionally describes user-visible work, not every internal
// function call, so a long-running stage can remain visibly active until it
// actually completes.
type QueryProgress struct {
	Stage   string `json:"stage"`
	Message string `json:"message"`
	State   string `json:"state"`
}

// queryStream lets the SSE handler observe retrieval and generation as they
// happen. JSON callers pass a zero value and keep the fully buffered path.
type queryStream struct {
	progress func(QueryProgress)
	sources  func(retrieved []SourceContext)
	delta    func(text string)
}

// Response represents the query result returned to the client.
type Response struct {
	Answer string `json:"answer"`
	// RefusalReason is set only when Answer is a refusal, and names which of the
	// Refusal* reasons produced it. It exists so a caller can act on the cause
	// (ask for access, publish the document, rephrase) without parsing prose.
	// Empty means the answer is a real answer.
	RefusalReason string `json:"refusal_reason,omitempty"`
	// Sources is the legacy name for all retrieved evidence. New clients should
	// use RetrievedSources and Citations so evidence considered by the model is
	// never presented as if the answer actually cited it.
	Sources          []SourceContext `json:"sources"`
	RetrievedSources []SourceContext `json:"retrieved_sources"`
	Citations        []SourceContext `json:"citations"`
	Duration         string          `json:"duration"`
	// TimeToFirstToken is set on the streaming path: request start to first
	// visible answer token. Omitted for JSON callers.
	TimeToFirstToken string `json:"ttft,omitempty"`
	// TokenUsage is populated when the LLM provider reports usage. Omitted
	// otherwise so callers can rely on zero value meaning "not reported".
	TokenUsage *TokenUsage `json:"token_usage,omitempty"`
	// PromptVersion identifies which system prompt produced this answer, so
	// prompt changes can be tracked and A/B'd in the eval.
	PromptVersion string `json:"prompt_version,omitempty"`
	// Retrieval exposes how this answer was retrieved, for the demo UI and
	// observability: routing strategy, cache hit, candidate counts.
	Retrieval *RetrievalInfo `json:"retrieval,omitempty"`
}

// RetrievalInfo summarizes the retrieval stage of a query.
type RetrievalInfo struct {
	Strategy                   string                      `json:"strategy"`
	CacheHit                   bool                        `json:"cache_hit"`
	Backends                   []string                    `json:"backends"`
	BackendCandidateCounts     map[string]int              `json:"backend_candidate_counts,omitempty"`
	FusedCandidateCount        int                         `json:"fused_candidate_count,omitempty"`
	DeduplicatedCandidateCount int                         `json:"deduplicated_candidate_count,omitempty"`
	StageDiagnostics           *retrieval.StageDiagnostics `json:"stage_diagnostics,omitempty"`
	SelectedContextCount       int                         `json:"selected_context_count"`
	UniqueDocumentCount        int                         `json:"unique_document_count"`
	SelectedKnowledgeBaseID    string                      `json:"selected_knowledge_base_id,omitempty"`
	SelectedApplicableScope    string                      `json:"selected_applicable_scope,omitempty"`
	ResolvedKnowledgeSpaceID   string                      `json:"resolved_knowledge_space_id,omitempty"`
	ResolvedKnowledgeSpaceName string                      `json:"resolved_knowledge_space_name,omitempty"`
	UnpublishedFiltered        int                         `json:"unpublished_filtered,omitempty"`
	ExactEvidenceRequired      bool                        `json:"exact_evidence_required"`
	ExactEvidenceMatched       bool                        `json:"exact_evidence_matched"`
	CrossScopeFiltered         int                         `json:"cross_scope_filtered,omitempty"`
	ScopeAmbiguous             bool                        `json:"scope_ambiguous,omitempty"`
	// CandidateCount is retained for API compatibility and equals the final
	// selected context count, not the pre-fusion candidate pool.
	CandidateCount int   `json:"candidate_count"`
	DurationMs     int64 `json:"duration_ms"`
	// AllowedPermissions are the permission levels the caller's role may
	// retrieve (e.g. user → [public internal]). Exposed so the UI can state the
	// retrieval boundary explicitly: confidential docs are filtered at the
	// source, never reaching the candidate set.
	AllowedPermissions []string `json:"allowed_permissions"`
	// PermissionRole is the caller's role that produced the boundary above.
	PermissionRole string `json:"permission_role,omitempty"`
	// MaxRelevance is the highest Qdrant cosine score among returned candidates.
	// Exposed for observability: a hard threshold is not reliable with the current
	// embedding (distributions overlap heavily — see ADR 0006), so the demo shows
	// this as evidence confidence instead of silently gating on it.
	MaxRelevance float64 `json:"max_relevance,omitempty"`
	// GroundingChecked reports whether post-generation answer verification ran
	// on this answer (only queries in the ambiguous relevance band).
	GroundingChecked bool `json:"grounding_checked,omitempty"`
	// GroundingPassed reports whether the verifier found the answer supported by
	// the retrieved sources and responsive to the question. False with
	// GroundingChecked true means the answer was blocked and replaced with the
	// fixed refusal sentence.
	GroundingPassed bool `json:"grounding_passed,omitempty"`
	// GroundingUnavailable reports that the check was due to run but the verifier
	// itself failed, so the answer shipped unverified (the gate fails open). This
	// is distinct from GroundingChecked=false, which means the query never
	// qualified for a check. Persistent true here means this defense is off.
	GroundingUnavailable bool     `json:"grounding_unavailable,omitempty"`
	PartialErrors        []string `json:"partial_errors,omitempty"`
	// RetiredFiltered counts candidates dropped because their document is marked
	// superseded or archived in the registry. Chunk payloads are written once at
	// ingest, so this filtering necessarily happens after search — the count is
	// exposed so a shrunken evidence set is explainable rather than mysterious.
	RetiredFiltered int `json:"retired_filtered,omitempty"`
	// ConflictDetected reports that the evidence set contains documents that may
	// disagree (one supersedes another, or two versions of the same file with
	// different effective dates). The system deliberately does NOT pick a winner;
	// it discloses the conflict and leaves adjudication to a human.
	ConflictDetected bool `json:"conflict_detected,omitempty"`
	// ConflictingDocs describes each document involved in the disclosed conflict.
	ConflictingDocs []ConflictingDoc `json:"conflicting_docs,omitempty"`
}

// ConflictingDoc identifies one side of a disclosed evidence conflict.
type ConflictingDoc struct {
	DocID         string `json:"doc_id"`
	FileName      string `json:"file_name,omitempty"`
	EffectiveDate string `json:"effective_date,omitempty"` // YYYY-MM-DD, empty = not tracked
	Supersedes    string `json:"supersedes,omitempty"`
}

// TokenUsage carries provider-reported token consumption for one answer.
type TokenUsage struct {
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	EstimatedCostUSD float64 `json:"estimated_cost_usd,omitempty"`
}

// SourceContext represents a retrieved document chunk with its relevance score.
type SourceContext struct {
	ChunkID         string  `json:"chunk_id"`
	DocID           string  `json:"doc_id"`
	Content         string  `json:"content"`
	Score           float64 `json:"score"`
	TenantID        string  `json:"tenant_id,omitempty"`
	FileName        string  `json:"file_name,omitempty"`
	EffectiveDate   string  `json:"effective_date,omitempty"`
	KnowledgeBase   string  `json:"knowledge_base_id,omitempty"`
	ApplicableScope string  `json:"applicable_scope,omitempty"`
}

// AccessContext is the authenticated caller context used by HTTP handlers and internal tools.
type AccessContext struct {
	TenantID string
	UserID   string
	Role     string
}

// NoEvidenceAnswer is the sentence the *system prompt* asks the model to reply
// with when the reference documents cannot answer the question. It is also the
// canonical form any model-authored refusal is normalised to. It is NOT
// necessarily the sentence the caller receives: once the system knows *why* it
// is refusing, it answers with the matching reason-specific sentence below —
// telling a user "no related documents found" when a document exists but is
// unpublished or outside their access scope sends them off to create a
// duplicate of something they already have.
const NoEvidenceAnswer = "未找到相关文档，无法回答该问题。"

// Refusal reasons are the machine-readable counterpart of the refusal sentence.
// Callers branch on this instead of parsing Chinese prose; the UI renders the
// sentence that goes with it.
const (
	// RefusalNoEvidence: retrieval returned nothing usable. The system cannot
	// separate "the corpus does not have it" from "it is outside your access
	// scope", because permission filtering happens inside the search backends
	// and reports no count — so the sentence names both possibilities instead of
	// asserting the document does not exist.
	RefusalNoEvidence = "no_evidence"
	// RefusalExactEvidenceMissing: the question carried a strong identifier
	// (contract/order/ticket id) that no surviving candidate contained.
	RefusalExactEvidenceMissing = "exact_evidence_missing"
	// RefusalEvidenceFiltered: documents matched, but the publish-state or
	// governance filters removed every one of them.
	RefusalEvidenceFiltered = "evidence_filtered"
	// RefusalInsufficientSupport: candidates survived, but the model refused, or
	// the post-generation verifier found the answer unsupported by them.
	RefusalInsufficientSupport = "insufficient_support"
	// RefusalSensitiveContent: the generated answer matched credential-like
	// patterns and was withheld.
	RefusalSensitiveContent = "sensitive_content"
)

// refusalAnswers maps a reason to the sentence the caller receives. Every
// reason a caller can see must be listed here; refusalAnswer falls back to the
// canonical sentence so an unmapped reason can never ship an empty answer.
var refusalAnswers = map[string]string{
	RefusalNoEvidence:           "未找到可用的相关文档。可能尚未收录，也可能不在你的访问范围内。",
	RefusalExactEvidenceMissing: "未找到与该标识符匹配的文档。请确认编号是否正确，或该文档是否已入库。",
	RefusalEvidenceFiltered:     "找到了相关文档，但它尚未发布或已被取代，当前不能作为回答依据。",
	RefusalInsufficientSupport:  "找到了相关文档，但其中内容不足以支撑这个问题的回答。",
	RefusalSensitiveContent:     SensitiveAnswerRefusal,
}

// refusalAnswer returns the sentence for a reason, falling back to the canonical
// sentence for an unknown or empty reason.
func refusalAnswer(reason string) string {
	if answer, ok := refusalAnswers[reason]; ok {
		return answer
	}
	return NoEvidenceAnswer
}

// isRefusalAnswer reports whether a sentence is one this package emits as a
// refusal — the canonical one, or any reason-specific one. Used by the streaming
// path to decide whether the buffered text must be replaced, and by callers that
// only have the text.
func isRefusalAnswer(answer string) bool {
	trimmed := strings.TrimSpace(answer)
	for _, candidate := range refusalAnswers {
		if trimmed == candidate {
			return true
		}
	}
	return trimmed == NoEvidenceAnswer
}

// refusalMarkers are substrings that mean the *model* wrote a refusal of its
// own. They are deliberately loose (the model paraphrases), which is why they
// must not be used to recognise the sentences this package emits — those are
// matched exactly by isRefusalAnswer.
var refusalMarkers = []string{
	NoEvidenceAnswer,
	"未找到相关文档",
	"未在参考文档中直接定位锚点",
	"参考文档不足",
	"无法从参考文档",
	"没有相关文档",
	"无法回答",
}

func isNoEvidenceAnswer(answer string) bool {
	for _, marker := range refusalMarkers {
		if strings.Contains(answer, marker) {
			return true
		}
	}
	return false
}

func canonicalizeRefusal(answer string) string {
	if isNoEvidenceAnswer(answer) {
		return NoEvidenceAnswer
	}
	return answer
}

// maxTopK caps a client-requested top-K so it cannot scale the retrieval
// candidate window or the LLM context without bound.
const maxTopK = 50

func clampTopK(topK int) int {
	if topK <= 0 {
		return 5
	}
	if topK > maxTopK {
		return maxTopK
	}
	return topK
}

var (
	ErrQuestionRequired     = errors.New("question is required")
	ErrQuestionTooLong      = errors.New("question is too long")
	ErrUnauthorized         = errors.New("unauthorized")
	ErrSearchFailed         = errors.New("search failed")
	ErrGenerationFailed     = errors.New("generation failed")
	ErrKnowledgeForbidden   = errors.New("knowledge space forbidden")
	ErrKnowledgeUnavailable = errors.New("knowledge catalog unavailable")
)

const (
	defaultQuestionMaxRunes = 2000
	defaultQueryBodyBytes   = int64(64 << 10)
	// SensitiveAnswerRefusal replaces generated text that matched credential-like patterns.
	SensitiveAnswerRefusal = "抱歉，该回答包含疑似敏感信息，已拒绝输出。"
)

func clampQuestion(question string, maxRunes int) (string, error) {
	question = strings.TrimSpace(question)
	if question == "" {
		return "", ErrQuestionRequired
	}
	if maxRunes <= 0 {
		maxRunes = defaultQuestionMaxRunes
	}
	if utf8.RuneCountInString(question) > maxRunes {
		return "", ErrQuestionTooLong
	}
	return question, nil
}

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
	prompt, promptVersion := loadSystemPrompt(cfg)
	service := &Service{
		cfg:                cfg,
		llmEndpoint:        normalizeLLMEndpoint(config.EnvStr("LLM_ENDPOINT", "https://api.openai.com/v1/chat/completions")),
		llmAPIKey:          config.EnvSecret("LLM_API_KEY", ""),
		llmModel:           config.EnvStr("LLM_MODEL", "deepseek-v4-flash"),
		llmMaxTokens:       config.EnvInt("LLM_MAX_TOKENS", 1024),
		llmMaxContextChars: config.EnvInt("LLM_MAX_CONTEXT_CHARS", 20_000),
		retriever:          retrieval.NewEngine(cfg),
		httpClient:         &http.Client{Timeout: config.EnvDuration("LLM_TIMEOUT", 30*time.Second)},
		breaker:            circuit.New("llm-api", 5, 60*time.Second),
		tracer:             tracing.Tracer("query"),
		llmObserver:        observer,
		systemPrompt:       prompt,
		promptVersion:      promptVersion,

		promptPricePer1K:     config.EnvFloat("LLM_PRICE_PROMPT_PER_1K", 0),
		completionPricePer1K: config.EnvFloat("LLM_PRICE_COMPLETION_PER_1K", 0),
	}
	if initializer, ok := observer.(llmModelInitializer); ok {
		initializer.InitializeLLMModel(service.llmModel)
	}
	return service
}

// HandleQuery is the HTTP handler for POST /v1/query.
// InvalidateSemanticCache drops cached retrieval results so a document write or
// delete cannot leave stale answers behind. Used by the upload/delete handlers.
func (s *Service) InvalidateSemanticCache(ctx context.Context) error {
	return s.retriever.InvalidateCache(ctx)
}

// WarmEmbeddings pins the query-time embedding model so the first question
// after process start does not wait on an Ollama cold load.
func (s *Service) WarmEmbeddings(ctx context.Context) error {
	if s == nil || s.retriever == nil {
		return nil
	}
	return s.retriever.Warm(ctx)
}

func (s *Service) HandleQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	ctx, span := s.tracer.Start(r.Context(), "HandleQuery")
	defer span.End()

	req, err := decodeQueryRequest(r, s.cfg)
	if err != nil {
		if isMaxBytesError(err) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	resp, err := s.Ask(ctx, req, AccessContext{
		TenantID: auth.GetTenantID(r.Context()),
		UserID:   auth.GetUserID(r.Context()),
		Role:     auth.GetPermission(r.Context()),
	})
	switch {
	case err == nil:
	case errors.Is(err, ErrQuestionRequired):
		http.Error(w, "question is required", http.StatusBadRequest)
		return
	case errors.Is(err, ErrQuestionTooLong):
		http.Error(w, "question too long", http.StatusBadRequest)
		return
	case errors.Is(err, ErrUnauthorized):
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	case errors.Is(err, ErrKnowledgeForbidden):
		http.Error(w, "knowledge space forbidden", http.StatusForbidden)
		return
	case errors.Is(err, ErrKnowledgeUnavailable):
		http.Error(w, "knowledge catalog unavailable", http.StatusServiceUnavailable)
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
	return s.ask(ctx, req, access, queryStream{})
}

func (s *Service) ask(ctx context.Context, req Request, access AccessContext, stream queryStream) (response Response, err error) {
	ctx, askSpan := s.tracer.Start(ctx, "QueryService.Ask")
	defer func() {
		if err != nil {
			askSpan.RecordError(err)
			askSpan.SetStatus(codes.Error, "query failed")
		}
		askSpan.End()
	}()

	question, qerr := clampQuestion(req.Question, s.cfg.QuestionMaxRunes)
	if qerr != nil {
		return Response{}, qerr
	}
	req.Question = question
	if strings.TrimSpace(access.TenantID) == "" {
		return Response{}, ErrUnauthorized
	}
	if req.TopK <= 0 {
		req.TopK = 5
	}
	// Clamp the requested top-K: an unbounded value would scale the candidate
	// limit (candidateLimit = TopK * 3 in the retrieval engine) and balloon both
	// the vector search and the LLM context.
	req.TopK = clampTopK(req.TopK)
	allowedPermissions := AllowedPermissionsForRole(access.Role)
	var resolvedSpace knowledgecatalog.Space
	if s.catalog != nil {
		resolvedSpace, err = s.catalog.Resolve(ctx, knowledgecatalog.Principal{
			TenantID: access.TenantID, UserID: access.UserID, Role: access.Role,
		}, req.KnowledgeSpaceID, knowledgecatalog.CapabilityQuery)
		if err != nil {
			if errors.Is(err, knowledgecatalog.ErrForbidden) || errors.Is(err, knowledgecatalog.ErrNotFound) {
				return Response{}, fmt.Errorf("%w: %v", ErrKnowledgeForbidden, err)
			}
			return Response{}, fmt.Errorf("%w: %v", ErrKnowledgeUnavailable, err)
		}
	}
	span := trace.SpanFromContext(ctx)

	span.SetAttributes(
		attribute.String("tenant_id", access.TenantID),
		attribute.String("permission_role", access.Role),
		attribute.Int("allowed_permission_levels", len(allowedPermissions)),
		attribute.Int("top_k", req.TopK),
		attribute.Int("question_len", len(req.Question)),
	)

	start := time.Now()
	emit := func(stage, message, state string) {
		if stream.progress != nil {
			stream.progress(QueryProgress{Stage: stage, Message: message, State: state})
		}
	}
	emit("preparing", "正在确认检索范围…", "completed")
	emit("retrieving", "正在检索相关文档…", "running")

	publishedDocs := s.listedPublishedDocuments(ctx, access.TenantID, resolvedSpace.ID, allowedPermissions)
	retrievalResult, err := s.retriever.Retrieve(ctx, retrieval.Request{
		Question:                 req.Question,
		TopK:                     req.TopK,
		TenantID:                 access.TenantID,
		AllowedPermissions:       allowedPermissions,
		KnowledgeBaseID:          resolvedSpace.ID,
		ApplicableScope:          strings.TrimSpace(req.ApplicableScope),
		DiagnosticRequiredDocIDs: diagnosticRequiredDocIDs(s.cfg.RetrievalDiagnosticsEnabled, req.DiagnosticRequiredDocIDs),
		TitleMatchDocIDs:         titleMatchedDocIDs(req.Question, publishedDocs),
		FileNames:                publishedFileNames(publishedDocs),
	})
	if err != nil {
		slog.Error("retrieval failed", "error", err)
		return Response{}, fmt.Errorf("%w: %v", ErrSearchFailed, err)
	}
	emit("retrieving", "文档检索完成", "completed")
	emit("screening", "正在筛选并校验有效证据…", "running")
	if len(retrievalResult.PartialErrors) > 0 {
		slog.Warn("retrieval completed with partial errors",
			"tenant_id", access.TenantID,
			"route", retrievalResult.Route.Strategy,
			"errors", retrievalResult.PartialErrors)
	}
	candidates := retrievalResult.Sources
	// Permission filtering removes the target document but still leaves unrelated
	// same-tenant chunks in the result set, so len(candidates) == 0 alone does not
	// detect "no supporting evidence". Without a relevance gate the LLM answers
	// from those unrelated chunks — a real hallucination path that the mock model
	// hides, because the mock emits a refusal string of its own.
	if gated, dropped := gateByRelevance(candidates, s.cfg.RetrievalMinRelevance); dropped > 0 {
		slog.Info("retrieval candidates below relevance floor",
			"tenant_id", access.TenantID,
			"min_relevance", s.cfg.RetrievalMinRelevance,
			"dropped", dropped,
			"kept", len(gated))
		candidates = gated
	}

	var scope scopeDecision
	if resolvedSpace.ID != "" {
		scope.KnowledgeBaseID = resolvedSpace.ID
	}

	unpublishedFiltered := 0
	if s.catalog != nil {
		decision, catalogErr := s.catalog.FilterEvidence(ctx, access.TenantID, resolvedSpace.ID, uniqueDocIDs(candidates))
		if catalogErr != nil {
			return Response{}, fmt.Errorf("%w: %v", ErrKnowledgeUnavailable, catalogErr)
		}
		kept := make([]retrieval.Candidate, 0, len(candidates))
		for _, candidate := range candidates {
			if decision.AllowedDocIDs[candidate.DocID] {
				kept = append(kept, candidate)
			}
		}
		candidates = kept
		unpublishedFiltered = decision.UnpublishedFiltered
	}

	// Corpus governance: a document that has been superseded or archived must not
	// be used as evidence even though its chunks are still indexed.
	gov := s.applyGovernance(ctx, access.TenantID, candidates)
	candidates = gov.candidates
	annotateInfo := func(info *RetrievalInfo) *RetrievalInfo {
		info = scope.annotate(gov.annotate(info))
		if info != nil {
			info.ResolvedKnowledgeSpaceID = resolvedSpace.ID
			info.ResolvedKnowledgeSpaceName = resolvedSpace.Name
			info.UnpublishedFiltered = unpublishedFiltered
			info.ExactEvidenceRequired = retrieval.HasStrongExactTokens(req.Question)
			info.ExactEvidenceMatched = retrieval.ExactEvidenceSufficient(req.Question, candidates)
		}
		return info
	}
	if gov.retiredFiltered > 0 {
		slog.Info("retired documents excluded from evidence",
			"tenant_id", access.TenantID, "dropped", gov.retiredFiltered, "kept", len(candidates))
	}

	// Strong identifiers such as contract, order, and trace ids are not safely
	// answerable from semantic similarity alone. After all authorization and
	// lifecycle filters have run, require the requested identifier to appear in
	// one surviving evidence candidate before calling the LLM.
	if !retrieval.ExactEvidenceSufficient(req.Question, candidates) {
		emit("screening", "证据筛选完成", "completed")
		emit("refused", refusalAnswer(RefusalExactEvidenceMissing), "completed")
		span.SetAttributes(
			attribute.Bool("retrieval.exact_evidence_insufficient", true),
			attribute.String("refusal.reason", RefusalExactEvidenceMissing),
		)
		return Response{
			Answer:           refusalAnswer(RefusalExactEvidenceMissing),
			RefusalReason:    RefusalExactEvidenceMissing,
			Sources:          []SourceContext{},
			RetrievedSources: []SourceContext{},
			Citations:        []SourceContext{},
			Duration:         time.Since(start).String(),
			Retrieval:        annotateInfo(retrievalInfoFromResult(retrievalResult, candidates, access.Role, allowedPermissions)),
		}, nil
	}

	sources := sourceContextsFromCandidates(candidates, gov.documents)
	if s.cfg.RetrievalDiagnosticsEnabled && req.RetrievalOnly {
		return Response{
			Answer:           "",
			Sources:          sources,
			RetrievedSources: sources,
			Citations:        []SourceContext{},
			Duration:         time.Since(start).String(),
			Retrieval:        annotateInfo(retrievalInfoFromResult(retrievalResult, candidates, access.Role, allowedPermissions)),
		}, nil
	}

	if len(sources) == 0 {
		// Distinguish "documents matched but were filtered out" from "nothing
		// matched at all". Only the former can be stated with certainty — a
		// document removed by permission filtering never reaches this layer and
		// leaves no count, so the no-evidence sentence names that possibility
		// instead of asserting the document does not exist.
		refusalReason := RefusalNoEvidence
		if unpublishedFiltered > 0 || gov.retiredFiltered > 0 {
			refusalReason = RefusalEvidenceFiltered
		}
		emit("screening", "证据筛选完成", "completed")
		emit("refused", refusalAnswer(refusalReason), "completed")
		span.SetAttributes(
			attribute.Bool("retrieval.no_supporting_evidence", true),
			attribute.String("refusal.reason", refusalReason),
		)
		return Response{
			Answer:           refusalAnswer(refusalReason),
			RefusalReason:    refusalReason,
			Sources:          []SourceContext{},
			RetrievedSources: []SourceContext{},
			Citations:        []SourceContext{},
			Duration:         time.Since(start).String(),
			// Retrieval info is included so the demo can show WHY it refused
			// (retrieval ran, but no sufficiently relevant evidence came back —
			// possibly because every match was a retired document).
			Retrieval: annotateInfo(retrievalInfoFromResult(retrievalResult, candidates, access.Role, allowedPermissions)),
		}, nil
	}

	span.SetAttributes(
		attribute.Int("retrieved_sources", len(sources)),
		attribute.String("retrieval_strategy", string(retrievalResult.Route.Strategy)),
		attribute.Bool("retrieval_cache_hit", retrievalResult.CacheHit),
		attribute.Int("retrieval_partial_errors", len(retrievalResult.PartialErrors)),
	)

	emit("screening", "证据筛选完成", "completed")
	if stream.sources != nil {
		stream.sources(sources)
	}
	emit("generating", "正在根据证据生成回答…", "running")
	var ttft time.Duration
	var answer string
	var usage llmCallResult
	if stream.delta != nil {
		answer, usage, err = s.generateAnswerStreaming(ctx, req.Question, sources, func(text string) {
			if ttft == 0 {
				ttft = time.Since(start)
			}
			stream.delta(text)
		})
	} else {
		answer, usage, err = s.generateAnswer(ctx, req.Question, sources)
	}
	if err != nil {
		slog.Error("LLM generation failed", "error", err)
		return Response{}, fmt.Errorf("%w: %v", ErrGenerationFailed, err)
	}
	answer = canonicalizeRefusal(answer)

	// The model may refuse even when candidates passed the gate (rule 2 above).
	// Return no sources in that case, so a refusal never ships with citations
	// that could be mistaken for supporting evidence.
	if answer == NoEvidenceAnswer {
		emit("refused", "回答缺少可验证证据", "completed")
		span.SetAttributes(
			attribute.Bool("llm.refused_for_lack_of_evidence", true),
			attribute.String("refusal.reason", RefusalInsufficientSupport),
		)
		resp := Response{
			Answer:           refusalAnswer(RefusalInsufficientSupport),
			RefusalReason:    RefusalInsufficientSupport,
			Sources:          []SourceContext{},
			RetrievedSources: []SourceContext{},
			Citations:        []SourceContext{},
			Duration:         time.Since(start).String(),
			Retrieval:        annotateInfo(retrievalInfoFromResult(retrievalResult, candidates, access.Role, allowedPermissions)),
		}
		if ttft > 0 {
			resp.TimeToFirstToken = ttft.String()
		}
		return resp, nil
	}

	// Post-generation answer verification. The relevance band is ambiguous: on
	// bge-m3, 26/38 real positives hit at max_relevance=0.5 and the failing
	// negative also scores 0.5, so no threshold separates them. When the top
	// candidate sits in the ambiguous band, ask a verifier whether the answer is
	// supported by the retrieved sources and actually answers the question.
	groundingChecked := false
	groundingPassed := true
	groundingUnavailable := false
	emit("generating", "回答生成完成", "completed")
	emit("verifying", "正在校验回答与引用…", "running")
	if s.cfg.RetrievalGroundingCheck && len(sources) > 0 {
		maxRel := maxSourceRelevance(candidates)
		if maxRel >= s.cfg.RetrievalGroundingLowBound && maxRel < s.cfg.RetrievalGroundingHighBound {
			ok, gerr := s.groundingCheck(ctx, req.Question, answer, sources)
			if gerr != nil {
				// Fail open: a broken verifier must not turn every answer into a
				// refusal. But report it as NOT checked rather than checked-and-passed,
				// so `grounding_checked` stops claiming a guarantee that did not run.
				// A persistently failing verifier means this defense is off.
				groundingUnavailable = true
				span.SetAttributes(attribute.Bool("llm.grounding_verifier_unavailable", true))
				slog.Warn("grounding verifier unavailable; answer passed through unverified",
					"error", gerr, "tenant_id", access.TenantID)
			} else {
				groundingChecked = true
				groundingPassed = ok
			}
		}
	}
	if groundingChecked && !groundingPassed {
		emit("verifying", "回答校验未通过", "completed")
		emit("refused", "回答未通过证据校验", "completed")
		span.SetAttributes(
			attribute.Bool("llm.ungrounded_answer_blocked", true),
			attribute.String("refusal.reason", RefusalInsufficientSupport),
		)
		info := annotateInfo(retrievalInfoFromResult(retrievalResult, candidates, access.Role, allowedPermissions))
		info.GroundingChecked = true
		info.GroundingPassed = false
		resp := Response{
			Answer:           refusalAnswer(RefusalInsufficientSupport),
			RefusalReason:    RefusalInsufficientSupport,
			Sources:          []SourceContext{},
			RetrievedSources: []SourceContext{},
			Citations:        []SourceContext{},
			Duration:         time.Since(start).String(),
			Retrieval:        info,
		}
		if ttft > 0 {
			resp.TimeToFirstToken = ttft.String()
		}
		return resp, nil
	}

	var tokenUsage *TokenUsage
	if usage.PromptTokens > 0 || usage.CompletionTokens > 0 {
		tokenUsage = &TokenUsage{
			PromptTokens:     usage.PromptTokens,
			CompletionTokens: usage.CompletionTokens,
			EstimatedCostUSD: estimateLLMCost(usage, s.promptPricePer1K, s.completionPricePer1K),
		}
	}

	retrievalInfo := annotateInfo(retrievalInfoFromResult(retrievalResult, candidates, access.Role, allowedPermissions))
	retrievalInfo.GroundingChecked = groundingChecked
	retrievalInfo.GroundingPassed = groundingPassed
	retrievalInfo.GroundingUnavailable = groundingUnavailable

	// refusalReason stays empty on the answering path; it is set below only when
	// the answer is replaced by a refusal.
	refusalReason := ""
	if releasecenter.ContainsSensitiveData(answer) {
		if s.sensitiveAnswerHook != nil {
			s.sensitiveAnswerHook(ctx, access, answer)
		}
		emit("refused", "回答包含敏感信息，已拒绝输出", "completed")
		span.SetAttributes(
			attribute.Bool("llm.sensitive_answer_blocked", true),
			attribute.String("refusal.reason", RefusalSensitiveContent),
		)
		answer = SensitiveAnswerRefusal
		refusalReason = RefusalSensitiveContent
		sources = []SourceContext{}
	}

	resp := Response{
		Answer:           answer,
		RefusalReason:    refusalReason,
		Sources:          sources,
		RetrievedSources: sources,
		Citations:        citationsFromAnswer(answer, sources),
		Duration:         time.Since(start).String(),
		TokenUsage:       tokenUsage,
		PromptVersion:    s.promptVersion,
		Retrieval:        retrievalInfo,
	}
	if ttft > 0 {
		resp.TimeToFirstToken = ttft.String()
	}
	emit("verifying", "回答校验完成", "completed")
	emit("finalizing", "正在整理回答和引用…", "running")
	emit("finalizing", "回答和引用整理完成", "completed")
	emit("completed", "回答已完成", "completed")

	slog.Info("query completed",
		"question_len", len(req.Question),
		"sources", len(sources),
		"retrieval_strategy", retrievalResult.Route.Strategy,
		"retrieval_cache_hit", retrievalResult.CacheHit,
		"retrieval_ms", retrievalResult.Duration.Milliseconds(),
		"ttft", resp.TimeToFirstToken,
		"grounding_checked", groundingChecked,
		"duration", resp.Duration)

	span.SetAttributes(attribute.Int("answer_len", len(answer)))

	return resp, nil
}

// HandleQueryStreaming serves POST /v1/query with SSE when the client sends
// Accept: text/event-stream. Retrieval and generation stream incrementally.
// Grounding still runs before the done event; if it rejects the answer, a
// replace event overwrites any provisional tokens.
//
// Event protocol:
//
//	event: status   data: {"stage":"...","message":"...","state":"..."}
//	event: sources  data: {"sources":[...],"retrieved_sources":[...]}
//	event: delta    data: {"text":"..."}
//	event: replace  data: {"text":"...","sources":[...]}
//	event: done     data: {"answer":"...","duration":"...","retrieval":{...}}
//	event: error    data: {"error":"..."}
func (s *Service) HandleQueryStreaming(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.cfg.HTTPHandlerTimeout > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), s.cfg.HTTPHandlerTimeout)
		defer cancel()
		r = r.WithContext(ctx)
	}

	req, err := decodeQueryRequest(r, s.cfg)
	if err != nil {
		if isMaxBytesError(err) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if _, qerr := clampQuestion(req.Question, s.cfg.QuestionMaxRunes); qerr != nil {
		if errors.Is(qerr, ErrQuestionTooLong) {
			http.Error(w, "question too long", http.StatusBadRequest)
			return
		}
		http.Error(w, "question is required", http.StatusBadRequest)
		return
	}
	tenantID := auth.GetTenantID(r.Context())
	if tenantID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	role := auth.GetPermission(r.Context())
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher := http.NewResponseController(w)
	writeEvent := func(event string, payload any) {
		body, _ := json.Marshal(payload)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, body)
		_ = flusher.Flush()
	}
	writeSSEStatus := func(progress QueryProgress) {
		writeEvent("status", progress)
	}
	writeSSEStatus(QueryProgress{Stage: "preparing", Message: "正在确认检索范围…", State: "running"})

	var streamed strings.Builder
	sourcesSent := false
	writeSources := func(retrieved, citations []SourceContext) {
		sourcesSent = true
		shown := citations
		if len(shown) == 0 {
			shown = retrieved
		}
		writeEvent("sources", map[string]any{
			"sources":           shown,
			"citations":         citations,
			"retrieved_sources": retrieved,
		})
	}

	resp, err := s.ask(r.Context(), req, AccessContext{TenantID: tenantID, UserID: auth.GetUserID(r.Context()), Role: role}, queryStream{
		progress: writeSSEStatus,
		sources: func(retrieved []SourceContext) {
			writeSources(retrieved, nil)
		},
		delta: func(text string) {
			streamed.WriteString(text)
			writeEvent("delta", map[string]string{"text": text})
		},
	})
	if err != nil {
		failureMessage := "问答处理失败，请稍后重试"
		switch {
		case errors.Is(err, ErrSearchFailed):
			failureMessage = "文档检索失败，请稍后重试"
			writeSSEError(w, "search failed")
		case errors.Is(err, ErrGenerationFailed):
			failureMessage = "回答生成失败，请稍后重试"
			writeSSEError(w, "generation failed")
		case errors.Is(err, ErrKnowledgeForbidden):
			failureMessage = "无权访问当前知识空间"
			writeSSEError(w, "knowledge space forbidden")
		case errors.Is(err, ErrKnowledgeUnavailable):
			failureMessage = "知识空间暂时不可用"
			writeSSEError(w, "knowledge catalog unavailable")
		default:
			writeSSEError(w, "query failed")
		}
		writeSSEStatus(QueryProgress{Stage: "failed", Message: failureMessage, State: "failed"})
		return
	}

	if !sourcesSent {
		writeSources(resp.RetrievedSources, resp.Citations)
	}
	if streamed.Len() == 0 {
		writeEvent("delta", map[string]string{"text": resp.Answer})
	} else if streamed.String() != resp.Answer || isRefusalAnswer(resp.Answer) {
		// A refusal always replaces whatever was streamed: a partially streamed
		// answer followed by a refusal sentence would read as both at once.
		writeEvent("replace", map[string]any{
			"text":              resp.Answer,
			"sources":           resp.Citations,
			"citations":         resp.Citations,
			"retrieved_sources": resp.RetrievedSources,
		})
	}

	done := map[string]any{
		"answer":            resp.Answer,
		"refusal_reason":    resp.RefusalReason,
		"sources":           resp.Citations,
		"citations":         resp.Citations,
		"retrieved_sources": resp.RetrievedSources,
		"duration":          resp.Duration,
		"prompt_version":    resp.PromptVersion,
		"retrieval":         resp.Retrieval,
	}
	if resp.TimeToFirstToken != "" {
		done["ttft"] = resp.TimeToFirstToken
	}
	if resp.TokenUsage != nil {
		done["token_usage"] = resp.TokenUsage
	}
	writeEvent("done", done)
}

// AllowedPermissionsForRole returns the document permission levels a role may
// access. Unknown or missing roles fail safe to readonly (public only). It is
// shared by query filtering and the document registry listing/detail endpoints.
func AllowedPermissionsForRole(role string) []string {
	normalizedRole := strings.ToLower(strings.TrimSpace(role))
	if allowed, ok := roleAllowedDocPermissions[normalizedRole]; ok {
		return allowed
	}
	// Fail-safe fallback: unknown or missing role can only access public docs.
	return roleAllowedDocPermissions["readonly"]
}

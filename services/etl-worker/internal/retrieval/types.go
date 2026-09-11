// Package retrieval implements query routing, multi-source retrieval, fusion,
// reranking, and semantic caching for the Query API.
package retrieval

import (
	"context"
	"time"

	"ai-etl-pipeline/internal/indexmanifest"
	"ai-etl-pipeline/internal/model"
)

// Strategy describes the route chosen for a user query.
type Strategy string

const (
	StrategyHybrid       Strategy = "hybrid"
	StrategyExactKeyword Strategy = "exact_keyword"
	StrategySemantic     Strategy = "semantic"
)

const (
	SourceQdrant        = "qdrant"
	SourceElasticsearch = "elasticsearch"
)

// Route controls which backends are queried and how much each backend
// contributes during score fusion.
type Route struct {
	Strategy      Strategy `json:"strategy"`
	UseQdrant     bool     `json:"use_qdrant"`
	UseElastic    bool     `json:"use_elastic"`
	QdrantWeight  float64  `json:"qdrant_weight"`
	ElasticWeight float64  `json:"elastic_weight"`
}

// Request is the retrieval-layer request derived from /v1/query.
type Request struct {
	Question           string
	TopK               int
	TenantID           string
	AllowedPermissions []string
	KnowledgeBaseID    string
	ApplicableScope    string
	// DiagnosticRequiredDocIDs is populated only by controlled local
	// evaluations. Results expose aggregate stage coverage, never these ids.
	DiagnosticRequiredDocIDs []string
	// TitleMatchDocIDs are published documents whose file names appear in the
	// question. Elasticsearch uses them to recover title-only hits.
	TitleMatchDocIDs []string
	// FileNames maps published doc_id to original file name so rerank can
	// distinguish near-duplicate policies without a lexical score boost.
	FileNames map[string]string
}

// SearchRequest is passed to individual retrieval backends.
type SearchRequest struct {
	Question           string
	DenseVector        []float64
	SparseVector       model.SparseVector
	Limit              int
	TenantID           string
	AllowedPermissions []string
	ExactSchemaFields  []string
	KnowledgeBaseID    string
	ApplicableScope    string
	TitleMatchDocIDs   []string
}

// Candidate is one retrieved chunk candidate from one or more backends.
type Candidate struct {
	ChunkID           string `json:"chunk_id"`
	DocID             string `json:"doc_id"`
	DocumentVersionID string `json:"document_version_id,omitempty"`
	GenerationID      string `json:"generation_id,omitempty"`
	Content           string `json:"content"`
	// Score carries the backend's raw relevance score before fusion, and the
	// RRF fusion score afterwards. Fusion is rank-based, so the post-fusion
	// value says nothing about semantic relevance — use Relevance for that.
	Score float64 `json:"score"`
	// Relevance preserves the backend's raw similarity score (Qdrant cosine,
	// Elasticsearch BM25) across fusion and reranking. RRF overwrites Score
	// with 1/(k+rank), which is identical for every rank-1 candidate whether
	// or not the document is actually relevant — so relevance gating needs
	// this field, not Score.
	Relevance float64 `json:"relevance,omitempty"`
	// RelevanceSource records which backend produced Relevance, since cosine
	// and BM25 are not comparable scales.
	RelevanceSource string            `json:"relevance_source,omitempty"`
	TenantID        string            `json:"tenant_id,omitempty"`
	Source          string            `json:"source,omitempty"`
	Rank            int               `json:"rank,omitempty"`
	Metadata        map[string]string `json:"metadata,omitempty"`
}

// VisibilityResolver decides which generation-scoped candidates may be read.
type VisibilityResolver interface {
	ResolveVisibility(context.Context, string, []indexmanifest.GenerationReference) ([]bool, error)
}

// Result is the output consumed by the RAG answer-generation layer.
type Result struct {
	Sources                    []Candidate       `json:"sources"`
	Route                      Route             `json:"route"`
	CacheHit                   bool              `json:"cache_hit"`
	BackendCandidateCounts     map[string]int    `json:"backend_candidate_counts,omitempty"`
	FusedCandidateCount        int               `json:"fused_candidate_count,omitempty"`
	DeduplicatedCandidateCount int               `json:"deduplicated_candidate_count,omitempty"`
	SelectedContextCount       int               `json:"selected_context_count"`
	UniqueDocumentCount        int               `json:"unique_document_count"`
	StageDiagnostics           *StageDiagnostics `json:"stage_diagnostics,omitempty"`
	PartialErrors              []string          `json:"partial_errors,omitempty"`
	Duration                   time.Duration
}

// Retriever is implemented by Qdrant and Elasticsearch backends.
type Retriever interface {
	Name() string
	Search(ctx context.Context, req SearchRequest) ([]Candidate, error)
}

// Reranker reorders fused candidates by query relevance.
type Reranker interface {
	Rerank(ctx context.Context, query string, candidates []Candidate, topK int) ([]Candidate, error)
}

// SourceCache stores retrieval outputs keyed by tenant, permission scope, and
// normalized query semantics.
type SourceCache interface {
	Lookup(ctx context.Context, key CacheKey, vector []float64) ([]Candidate, bool, error)
	Store(ctx context.Context, key CacheKey, vector []float64, sources []Candidate) error
	// Flush drops all cached entries. Called after document writes/deletes so
	// stale answers never outlive the knowledge they were grounded in.
	Flush(ctx context.Context) error
	Close() error
}

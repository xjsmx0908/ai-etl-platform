// Package retrieval implements query routing, multi-source retrieval, fusion,
// reranking, and semantic caching for the Query API.
package retrieval

import (
	"context"
	"time"

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
}

// SearchRequest is passed to individual retrieval backends.
type SearchRequest struct {
	Question           string
	DenseVector        []float64
	SparseVector       model.SparseVector
	Limit              int
	TenantID           string
	AllowedPermissions []string
}

// Candidate is one retrieved chunk candidate from one or more backends.
type Candidate struct {
	ChunkID  string  `json:"chunk_id"`
	DocID    string  `json:"doc_id"`
	Content  string  `json:"content"`
	Score    float64 `json:"score"`
	TenantID string  `json:"tenant_id,omitempty"`
	Source   string  `json:"source,omitempty"`
	Rank     int     `json:"rank,omitempty"`
}

// Result is the output consumed by the RAG answer-generation layer.
type Result struct {
	Sources       []Candidate `json:"sources"`
	Route         Route       `json:"route"`
	CacheHit      bool        `json:"cache_hit"`
	PartialErrors []string    `json:"partial_errors,omitempty"`
	Duration      time.Duration
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
	Close() error
}

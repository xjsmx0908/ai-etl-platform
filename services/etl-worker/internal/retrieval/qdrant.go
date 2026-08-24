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

	"ai-etl-pipeline/internal/tracing"
)

// QdrantRetriever performs Qdrant named-vector hybrid search.
type QdrantRetriever struct {
	endpoint   string
	apiKey     string
	collection string
	client     *http.Client
}

// NewQdrantRetriever creates a Qdrant retrieval backend.
func NewQdrantRetriever(endpoint, apiKey, collection string, client *http.Client) *QdrantRetriever {
	return &QdrantRetriever{
		endpoint:   trimRightSlash(endpoint),
		apiKey:     apiKey,
		collection: collection,
		client:     client,
	}
}

func (r *QdrantRetriever) Name() string {
	return SourceQdrant
}

func (r *QdrantRetriever) Search(ctx context.Context, req SearchRequest) ([]Candidate, error) {
	if r.endpoint == "" || r.collection == "" {
		return nil, fmt.Errorf("qdrant endpoint and collection are required")
	}
	if req.TenantID == "" {
		return nil, fmt.Errorf("tenant_id is required")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 50
	}
	allowed := req.AllowedPermissions
	if len(allowed) == 0 {
		allowed = []string{"public"}
	}
	filter := map[string]interface{}{
		"must": []map[string]interface{}{
			{"key": "tenant_id", "match": map[string]string{"value": req.TenantID}},
			{"key": "permission", "match": map[string]interface{}{"any": allowed}},
		},
	}
	must := filter["must"].([]map[string]interface{})
	if value := strings.TrimSpace(req.KnowledgeBaseID); value != "" {
		must = append(must, map[string]interface{}{
			"key": "metadata.knowledge_base_id", "match": map[string]string{"value": value},
		})
	}
	if value := strings.TrimSpace(req.ApplicableScope); value != "" {
		must = append(must, map[string]interface{}{
			"key": "metadata.applicable_scope", "match": map[string]string{"value": value},
		})
	}
	filter["must"] = must
	// Main query: dense + sparse prefetch fused by RRF. Scores here are
	// rank-based fusion scores (1/(k+rank)), which carry no similarity signal.
	query := map[string]interface{}{
		"prefetch": []map[string]interface{}{
			{"query": req.DenseVector, "using": "dense", "limit": limit},
			{
				"query": map[string]interface{}{"indices": req.SparseVector.Indices, "values": req.SparseVector.Values},
				"using": "sparse", "limit": limit,
			},
		},
		"query":        map[string]string{"fusion": "rrf"},
		"limit":        limit,
		"with_payload": true,
		"filter":       filter,
	}
	points, err := r.postQuery(ctx, query)
	if err != nil {
		return nil, err
	}

	candidates := make([]Candidate, 0, len(points))
	for i, p := range points {
		c := Candidate{
			Score:  p.Score,
			Source: SourceQdrant,
			Rank:   i + 1,
		}
		if v, ok := p.Payload["chunk_id"].(string); ok {
			c.ChunkID = v
		}
		if v, ok := p.Payload["doc_id"].(string); ok {
			c.DocID = v
		}
		if v, ok := p.Payload["content"].(string); ok {
			c.Content = v
		}
		if v, ok := p.Payload["tenant_id"].(string); ok {
			c.TenantID = v
		}
		c.Metadata = exactMetadataFromPayload(p.Payload, req.ExactSchemaFields)
		candidates = append(candidates, c)
	}

	// Attach the raw dense cosine score for relevance observability. This is a
	// second, dense-only query because RRF fusion discards the original
	// similarity; without it Candidate.Relevance would hold a rank score.
	denseScores, err := r.denseCosineScores(ctx, req, limit, filter)
	if err != nil {
		slog.Warn("qdrant dense score query failed", "error", err)
		return candidates, nil
	}
	for i := range candidates {
		if s, ok := denseScores[candidates[i].ChunkID]; ok {
			candidates[i].Relevance = s
			candidates[i].RelevanceSource = SourceQdrant
		}
	}
	return candidates, nil
}

// qdrantPoint is a single point returned by /points/query.
type qdrantPoint struct {
	Score   float64                `json:"score"`
	Payload map[string]interface{} `json:"payload"`
}

// postQuery sends a Qdrant /points/query body and returns the points.
func (r *QdrantRetriever) postQuery(ctx context.Context, body map[string]interface{}) ([]qdrantPoint, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal qdrant query: %w", err)
	}
	url := fmt.Sprintf("%s/collections/%s/points/query", r.endpoint, r.collection)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if r.apiKey != "" {
		req.Header.Set("api-key", r.apiKey)
	}
	tracing.InjectHTTPHeaders(ctx, req)

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qdrant query failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("qdrant query error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Result struct {
			Points []qdrantPoint `json:"points"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode qdrant response: %w", err)
	}
	return result.Result.Points, nil
}

// denseCosineScores returns chunk_id -> raw dense cosine from a dense-only query.
func (r *QdrantRetriever) denseCosineScores(ctx context.Context, req SearchRequest, limit int, filter map[string]interface{}) (map[string]float64, error) {
	body := map[string]interface{}{
		"query":        req.DenseVector,
		"using":        "dense",
		"limit":        limit,
		"with_payload": []string{"chunk_id"},
		"filter":       filter,
	}
	points, err := r.postQuery(ctx, body)
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(points))
	for _, p := range points {
		if cid, ok := p.Payload["chunk_id"].(string); ok {
			out[cid] = p.Score
		}
	}
	return out, nil
}

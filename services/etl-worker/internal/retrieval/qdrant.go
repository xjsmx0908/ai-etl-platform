package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	query := map[string]interface{}{
		"prefetch": []map[string]interface{}{
			{
				"query": req.DenseVector,
				"using": "dense",
				"limit": limit,
			},
			{
				"query": map[string]interface{}{
					"indices": req.SparseVector.Indices,
					"values":  req.SparseVector.Values,
				},
				"using": "sparse",
				"limit": limit,
			},
		},
		"query":        map[string]string{"fusion": "rrf"},
		"limit":        limit,
		"with_payload": true,
		"filter": map[string]interface{}{
			"must": []map[string]interface{}{
				{
					"key":   "tenant_id",
					"match": map[string]string{"value": req.TenantID},
				},
				{
					"key": "permission",
					"match": map[string]interface{}{
						"any": allowed,
					},
				},
			},
		},
	}

	data, err := json.Marshal(query)
	if err != nil {
		return nil, fmt.Errorf("marshal qdrant query: %w", err)
	}
	url := fmt.Sprintf("%s/collections/%s/points/query", r.endpoint, r.collection)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if r.apiKey != "" {
		httpReq.Header.Set("api-key", r.apiKey)
	}

	resp, err := r.client.Do(httpReq)
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
			Points []struct {
				Score   float64                `json:"score"`
				Payload map[string]interface{} `json:"payload"`
			} `json:"points"`
		} `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode qdrant response: %w", err)
	}

	candidates := make([]Candidate, 0, len(result.Result.Points))
	for i, p := range result.Result.Points {
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
	return candidates, nil
}

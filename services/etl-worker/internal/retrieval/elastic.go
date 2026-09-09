package retrieval

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"ai-etl-pipeline/internal/tracing"
)

// ElasticRetriever performs tenant- and permission-filtered BM25 retrieval.
type ElasticRetriever struct {
	address string
	apiKey  string
	index   string
	client  *http.Client
}

// NewElasticRetriever creates an Elasticsearch retrieval backend.
func NewElasticRetriever(address, apiKey, index string, client *http.Client) *ElasticRetriever {
	return &ElasticRetriever{
		address: trimRightSlash(address),
		apiKey:  strings.TrimSpace(apiKey),
		index:   strings.TrimSpace(index),
		client:  client,
	}
}

func (r *ElasticRetriever) Name() string {
	return SourceElasticsearch
}

func (r *ElasticRetriever) Search(ctx context.Context, req SearchRequest) ([]Candidate, error) {
	if r.address == "" || r.index == "" {
		return nil, fmt.Errorf("elasticsearch address and index are required")
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

	contentClauses := titleAwareShouldClauses(req)
	boolQuery := map[string]interface{}{
		"filter": []map[string]interface{}{
			{
				"term": map[string]interface{}{
					"tenant_id": req.TenantID,
				},
			},
			{
				"terms": map[string]interface{}{
					"permission": allowed,
				},
			},
		},
		"should":               contentClauses,
		"minimum_should_match": 1,
	}
	filters := boolQuery["filter"].([]map[string]interface{})
	if value := strings.TrimSpace(req.KnowledgeBaseID); value != "" {
		filters = append(filters, map[string]interface{}{
			"term": map[string]interface{}{"metadata.knowledge_base_id": value},
		})
	}
	if value := strings.TrimSpace(req.ApplicableScope); value != "" {
		filters = append(filters, map[string]interface{}{
			"term": map[string]interface{}{"metadata.applicable_scope": value},
		})
	}
	boolQuery["filter"] = filters
	if exact := metadataExactShouldClauses(req.Question, req.ExactSchemaFields); len(exact) > 0 {
		boolQuery["should"] = append(contentClauses, exact...)
	}

	body := map[string]interface{}{
		"size": limit,
		"query": map[string]interface{}{
			"bool": boolQuery,
		},
		"_source": []string{"chunk_id", "doc_id", "document_version_id", "generation_id", "tenant_id", "content", "file_hash", "metadata"},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal es query: %w", err)
	}

	endpoint := fmt.Sprintf("%s/%s/_search", r.address, url.PathEscape(r.index))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create es search request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if r.apiKey != "" {
		httpReq.Header.Set("Authorization", "ApiKey "+r.apiKey)
	}
	tracing.InjectHTTPHeaders(ctx, httpReq)

	resp, err := r.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("es search failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("es search error %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var result struct {
		Hits struct {
			Hits []struct {
				Score  float64 `json:"_score"`
				Source struct {
					ChunkID           string                 `json:"chunk_id"`
					DocID             string                 `json:"doc_id"`
					DocumentVersionID string                 `json:"document_version_id"`
					GenerationID      string                 `json:"generation_id"`
					TenantID          string                 `json:"tenant_id"`
					Content           string                 `json:"content"`
					FileHash          string                 `json:"file_hash"`
					Metadata          map[string]interface{} `json:"metadata"`
				} `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode es response: %w", err)
	}

	candidates := make([]Candidate, 0, len(result.Hits.Hits))
	for i, hit := range result.Hits.Hits {
		metadata := exactMetadataFromPayload(map[string]interface{}{
			"file_hash": hit.Source.FileHash,
			"metadata":  hit.Source.Metadata,
		}, req.ExactSchemaFields)
		candidates = append(candidates, Candidate{
			ChunkID:           hit.Source.ChunkID,
			DocID:             hit.Source.DocID,
			DocumentVersionID: hit.Source.DocumentVersionID,
			GenerationID:      hit.Source.GenerationID,
			Content:           hit.Source.Content,
			TenantID:          hit.Source.TenantID,
			Score:             hit.Score,
			Source:            SourceElasticsearch,
			Rank:              i + 1,
			Metadata:          metadata,
		})
	}
	return candidates, nil
}

func titleAwareShouldClauses(req SearchRequest) []map[string]interface{} {
	clauses := []map[string]interface{}{
		{
			"match_phrase": map[string]interface{}{
				"content": map[string]interface{}{
					"query": req.Question,
					"boost": 4.0,
				},
			},
		},
		{
			"match": map[string]interface{}{
				"content": map[string]interface{}{
					"query":    req.Question,
					"operator": "and",
				},
			},
		},
		{
			"match_phrase": map[string]interface{}{
				"file_name": map[string]interface{}{
					"query": req.Question,
					"boost": 6.0,
				},
			},
		},
		{
			"match": map[string]interface{}{
				"file_name": map[string]interface{}{
					"query": req.Question,
					"boost": 3.0,
				},
			},
		},
	}
	ids := make([]string, 0, len(req.TitleMatchDocIDs))
	seen := map[string]struct{}{}
	for _, id := range req.TitleMatchDocIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) > 0 {
		clauses = append(clauses, map[string]interface{}{
			"terms": map[string]interface{}{
				"doc_id": ids,
			},
		})
	}
	return clauses
}

func metadataExactShouldClauses(question string, fields []string) []map[string]interface{} {
	if len(fields) == 0 {
		return nil
	}
	tokens := extractExactTokens(question)
	if len(tokens) == 0 {
		return nil
	}

	clauses := make([]map[string]interface{}, 0, len(tokens)*len(fields))
	for _, token := range tokens {
		for _, field := range fields {
			field = strings.TrimSpace(field)
			if field == "" || field == "doc_id" || field == "chunk_id" {
				continue
			}
			clauses = append(clauses, map[string]interface{}{
				"term": map[string]interface{}{
					"metadata." + field: map[string]interface{}{
						"value": token,
						"boost": 4.0,
					},
				},
			})
		}
	}
	return clauses
}

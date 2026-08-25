package publicationworkflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

type IndexInspectorOptions struct {
	QdrantEndpoint       string
	QdrantAPIKey         string
	QdrantCollection     string
	ElasticsearchAddress string
	ElasticsearchAPIKey  string
	ElasticsearchIndex   string
	HTTPClient           *http.Client
}

type HTTPIndexInspector struct {
	options IndexInspectorOptions
	client  *http.Client
}

func NewHTTPIndexInspector(options IndexInspectorOptions) *HTTPIndexInspector {
	client := options.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &HTTPIndexInspector{options: options, client: client}
}

func (i *HTTPIndexInspector) CountDocumentChunks(ctx context.Context, tenantID, docID string) (IndexCounts, error) {
	tenantID = strings.TrimSpace(tenantID)
	docID = strings.TrimSpace(docID)
	if tenantID == "" || docID == "" {
		return IndexCounts{}, fmt.Errorf("tenant_id and doc_id are required")
	}
	if strings.TrimSpace(i.options.QdrantEndpoint) == "" || strings.TrimSpace(i.options.QdrantCollection) == "" ||
		strings.TrimSpace(i.options.ElasticsearchAddress) == "" || strings.TrimSpace(i.options.ElasticsearchIndex) == "" {
		return IndexCounts{}, fmt.Errorf("index inspector endpoints are required")
	}

	var counts IndexCounts
	var vectorErr, textErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		counts.Vector, vectorErr = i.countQdrant(ctx, tenantID, docID)
	}()
	go func() {
		defer wg.Done()
		counts.Text, textErr = i.countElasticsearch(ctx, tenantID, docID)
	}()
	wg.Wait()
	if vectorErr != nil {
		return IndexCounts{}, vectorErr
	}
	if textErr != nil {
		return IndexCounts{}, textErr
	}
	return counts, nil
}

func (i *HTTPIndexInspector) countQdrant(ctx context.Context, tenantID, docID string) (int, error) {
	body := map[string]any{
		"exact": true,
		"filter": map[string]any{"must": []map[string]any{
			{"key": "tenant_id", "match": map[string]string{"value": tenantID}},
			{"key": "doc_id", "match": map[string]string{"value": docID}},
		}},
	}
	endpoint := strings.TrimRight(i.options.QdrantEndpoint, "/") + "/collections/" + url.PathEscape(i.options.QdrantCollection) + "/points/count"
	var response struct {
		Result struct {
			Count int `json:"count"`
		} `json:"result"`
	}
	if err := i.postJSON(ctx, endpoint, i.options.QdrantAPIKey, "api-key", body, &response); err != nil {
		return 0, fmt.Errorf("count qdrant document chunks: %w", err)
	}
	return response.Result.Count, nil
}

func (i *HTTPIndexInspector) countElasticsearch(ctx context.Context, tenantID, docID string) (int, error) {
	body := map[string]any{"query": map[string]any{"bool": map[string]any{"filter": []map[string]any{
		{"term": map[string]string{"tenant_id": tenantID}},
		{"term": map[string]string{"doc_id": docID}},
	}}}}
	endpoint := strings.TrimRight(i.options.ElasticsearchAddress, "/") + "/" + url.PathEscape(i.options.ElasticsearchIndex) + "/_count"
	var response struct {
		Count int `json:"count"`
	}
	auth := ""
	if key := strings.TrimSpace(i.options.ElasticsearchAPIKey); key != "" {
		auth = "ApiKey " + key
	}
	if err := i.postJSON(ctx, endpoint, auth, "Authorization", body, &response); err != nil {
		return 0, fmt.Errorf("count elasticsearch document chunks: %w", err)
	}
	return response.Count, nil
}

func (i *HTTPIndexInspector) postJSON(ctx context.Context, endpoint, credential, header string, body, response any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(credential) != "" {
		req.Header.Set(header, credential)
	}
	resp, err := i.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("backend returned %d: %s", resp.StatusCode, strings.TrimSpace(string(message)))
	}
	if err := json.NewDecoder(resp.Body).Decode(response); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

var _ IndexInspector = (*HTTPIndexInspector)(nil)

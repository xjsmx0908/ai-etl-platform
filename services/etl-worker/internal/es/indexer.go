// Package es provides eventual-consistency full-text indexing to Elasticsearch.
package es

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

	"ai-etl-pipeline/internal/model"
)

// HTTPIndexer writes chunk documents to Elasticsearch.
type HTTPIndexer struct {
	address string
	apiKey  string
	index   string
	client  *http.Client
}

// NewHTTPIndexer creates an indexer and ensures target index exists.
func NewHTTPIndexer(address, apiKey, index string) (*HTTPIndexer, error) {
	address = strings.TrimRight(strings.TrimSpace(address), "/")
	index = strings.TrimSpace(index)

	if address == "" {
		return nil, fmt.Errorf("es address is required")
	}
	if index == "" {
		return nil, fmt.Errorf("es index is required")
	}

	idx := &HTTPIndexer{
		address: address,
		apiKey:  strings.TrimSpace(apiKey),
		index:   index,
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := idx.ensureIndex(ctx); err != nil {
		return nil, err
	}

	slog.Info("elasticsearch indexer connected", "address", address, "index", index)
	return idx, nil
}

// Close releases HTTP resources.
func (i *HTTPIndexer) Close() error {
	i.client.CloseIdleConnections()
	return nil
}

// IndexChunk indexes (or upserts) one chunk into Elasticsearch.
func (i *HTTPIndexer) IndexChunk(ctx context.Context, chunk model.Chunk) error {
	if strings.TrimSpace(chunk.ChunkID) == "" {
		return fmt.Errorf("chunk_id is required")
	}

	doc := mapChunkToESDoc(chunk)
	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal es document: %w", err)
	}

	docID := url.PathEscape(chunk.ChunkID)
	endpoint := fmt.Sprintf("%s/%s/_doc/%s", i.address, pathEscape(i.index), docID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create es request: %w", err)
	}
	i.setHeaders(req)

	resp, err := i.client.Do(req)
	if err != nil {
		return fmt.Errorf("es request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("es index failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
}

func (i *HTTPIndexer) ensureIndex(ctx context.Context) error {
	headURL := fmt.Sprintf("%s/%s", i.address, pathEscape(i.index))
	headReq, err := http.NewRequestWithContext(ctx, http.MethodHead, headURL, nil)
	if err != nil {
		return fmt.Errorf("create es head request: %w", err)
	}
	i.setHeaders(headReq)

	headResp, err := i.client.Do(headReq)
	if err != nil {
		return fmt.Errorf("es head request failed: %w", err)
	}
	defer headResp.Body.Close()

	switch headResp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		// continue with create
	default:
		body, _ := io.ReadAll(io.LimitReader(headResp.Body, 4096))
		return fmt.Errorf("es index check failed: status=%d body=%s", headResp.StatusCode, strings.TrimSpace(string(body)))
	}

	createBody := map[string]interface{}{
		"mappings": map[string]interface{}{
			"properties": map[string]interface{}{
				"chunk_id":   map[string]string{"type": "keyword"},
				"doc_id":     map[string]string{"type": "keyword"},
				"tenant_id":  map[string]string{"type": "keyword"},
				"content":    map[string]string{"type": "text"},
				"permission": map[string]string{"type": "keyword"},
				"chunk_index": map[string]string{
					"type": "integer",
				},
				"file_hash":  map[string]string{"type": "keyword"},
				"created_at": map[string]string{"type": "date"},
				"metadata":   map[string]string{"type": "flattened"},
			},
		},
	}
	data, err := json.Marshal(createBody)
	if err != nil {
		return fmt.Errorf("marshal es create index body: %w", err)
	}

	createReq, err := http.NewRequestWithContext(ctx, http.MethodPut, headURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create es index request: %w", err)
	}
	i.setHeaders(createReq)

	createResp, err := i.client.Do(createReq)
	if err != nil {
		return fmt.Errorf("es create index request failed: %w", err)
	}
	defer createResp.Body.Close()

	if createResp.StatusCode >= 200 && createResp.StatusCode < 300 {
		slog.Info("elasticsearch index created", "index", i.index)
		return nil
	}

	body, _ := io.ReadAll(io.LimitReader(createResp.Body, 4096))
	if createResp.StatusCode == http.StatusBadRequest &&
		(strings.Contains(string(body), "resource_already_exists_exception") ||
			strings.Contains(string(body), "already_exists")) {
		return nil
	}

	return fmt.Errorf("es create index failed: status=%d body=%s", createResp.StatusCode, strings.TrimSpace(string(body)))
}

func pathEscape(segment string) string {
	return url.PathEscape(strings.TrimSpace(segment))
}

func (i *HTTPIndexer) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if i.apiKey != "" {
		req.Header.Set("Authorization", "ApiKey "+i.apiKey)
	}
}

type esChunkDoc struct {
	ChunkID    string            `json:"chunk_id"`
	DocID      string            `json:"doc_id"`
	TenantID   string            `json:"tenant_id"`
	Content    string            `json:"content"`
	Permission string            `json:"permission"`
	ChunkIndex int               `json:"chunk_index"`
	FileHash   string            `json:"file_hash,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	CreatedAt  string            `json:"created_at"`
}

func mapChunkToESDoc(chunk model.Chunk) esChunkDoc {
	createdAt := chunk.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now()
	}
	return esChunkDoc{
		ChunkID:    chunk.ChunkID,
		DocID:      chunk.DocID,
		TenantID:   chunk.TenantID,
		Content:    chunk.Content,
		Permission: normalizePermission(chunk.Permission),
		ChunkIndex: chunk.Index,
		FileHash:   chunk.FileHash,
		Metadata:   copyMetadata(chunk.Metadata),
		CreatedAt:  createdAt.UTC().Format(time.RFC3339Nano),
	}
}

func copyMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]string, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

func normalizePermission(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "public":
		return "public"
	case "confidential":
		return "confidential"
	default:
		return "internal"
	}
}

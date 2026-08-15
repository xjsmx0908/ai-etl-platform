// Package store provides vector storage implementations (memory and Qdrant).
package store

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"ai-etl-pipeline/internal/model"
)

// --- In-memory store (development) ---

// MemoryStorer provides an in-memory Storer for development and testing.
type MemoryStorer struct {
	mu   sync.RWMutex
	data map[string]model.Chunk
}

// NewMemoryStorer creates an in-memory store.
func NewMemoryStorer() *MemoryStorer {
	return &MemoryStorer{data: make(map[string]model.Chunk)}
}

func (s *MemoryStorer) Upsert(_ context.Context, chunk model.Chunk) error {
	time.Sleep(time.Duration(2+rand.Intn(5)) * time.Millisecond)
	s.mu.Lock()
	s.data[chunk.ChunkID] = chunk
	s.mu.Unlock()
	return nil
}

func (s *MemoryStorer) Exists(_ context.Context, chunkID string) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data[chunkID]
	return ok, nil
}

func (s *MemoryStorer) Close() error { return nil }

// Count returns the number of stored chunks.
func (s *MemoryStorer) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data)
}

// --- Qdrant vector database store ---

// QdrantStorer uses Qdrant REST API for vector storage with Named Vectors.
type QdrantStorer struct {
	endpoint   string
	apiKey     string
	collection string
	dimension  int
	client     *http.Client
}

var allowedDocPermissions = map[string]struct{}{
	"public":       {},
	"internal":     {},
	"confidential": {},
}

// NewQdrantStorer creates a Qdrant store and ensures the collection exists.
func NewQdrantStorer(endpoint, apiKey, collection string, dimension int) (*QdrantStorer, error) {
	qs := &QdrantStorer{
		endpoint:   endpoint,
		apiKey:     apiKey,
		collection: collection,
		dimension:  dimension,
		client:     &http.Client{Timeout: 30 * time.Second},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := qs.ensureCollection(ctx); err != nil {
		return nil, fmt.Errorf("qdrant init: %w", err)
	}

	slog.Info("qdrant storer connected", "endpoint", endpoint, "collection", collection)
	return qs, nil
}

// Upsert writes a chunk with dense + sparse Named Vectors (idempotent).
func (q *QdrantStorer) Upsert(ctx context.Context, chunk model.Chunk) error {
	vectors := map[string]interface{}{
		"dense": chunk.Vector,
	}
	if !chunk.SparseVector.IsEmpty() {
		vectors["sparse"] = map[string]interface{}{
			"indices": chunk.SparseVector.Indices,
			"values":  chunk.SparseVector.Values,
		}
	}

	payload := map[string]interface{}{
		"chunk_id":   chunk.ChunkID,
		"doc_id":     chunk.DocID,
		"tenant_id":  chunk.TenantID,
		"content":    chunk.Content,
		"index":      chunk.Index,
		"permission": normalizeChunkPermission(chunk.Permission),
	}
	if len(chunk.Metadata) > 0 {
		payload["metadata"] = chunk.Metadata
	}

	point := map[string]interface{}{
		"id":      chunkIDToUint(chunk.ChunkID),
		"vector":  vectors,
		"payload": payload,
	}

	body := map[string]interface{}{
		"points": []interface{}{point},
	}

	url := fmt.Sprintf("%s/collections/%s/points", q.endpoint, q.collection)
	return q.doPut(ctx, url, body)
}

// Exists checks if a vector point already exists by chunk ID.
func (q *QdrantStorer) Exists(ctx context.Context, chunkID string) (bool, error) {
	url := fmt.Sprintf("%s/collections/%s/points/%d",
		q.endpoint, q.collection, chunkIDToUint(chunkID))

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false, err
	}
	q.setHeaders(req)

	resp, err := q.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == 404 {
		return false, nil
	}
	if resp.StatusCode == 200 {
		return true, nil
	}
	return false, fmt.Errorf("qdrant exists check: status %d", resp.StatusCode)
}

// DeleteByDocID deletes every point belonging to a document (matched on the
// doc_id payload). Used by document deletion and re-index flows.
func (q *QdrantStorer) DeleteByDocID(ctx context.Context, docID string) error {
	if strings.TrimSpace(docID) == "" {
		return fmt.Errorf("doc_id is required")
	}
	return q.deleteByFilter(ctx, []map[string]interface{}{
		{"key": "doc_id", "match": map[string]string{"value": docID}},
	})
}

// DeleteByDocIDAndTenant deletes every point for a document scoped to a tenant.
// The tenant_id filter is what prevents a colliding doc_id in another tenant
// from being wiped by a tenant-supplied delete (the old DeleteByDocID alone was
// a cross-tenant deletion bug).
func (q *QdrantStorer) DeleteByDocIDAndTenant(ctx context.Context, tenantID, docID string) error {
	if strings.TrimSpace(tenantID) == "" {
		return fmt.Errorf("tenant_id is required")
	}
	if strings.TrimSpace(docID) == "" {
		return fmt.Errorf("doc_id is required")
	}
	return q.deleteByFilter(ctx, []map[string]interface{}{
		{"key": "tenant_id", "match": map[string]string{"value": tenantID}},
		{"key": "doc_id", "match": map[string]string{"value": docID}},
	})
}

func (q *QdrantStorer) deleteByFilter(ctx context.Context, must []map[string]interface{}) error {
	if len(must) == 0 {
		return fmt.Errorf("delete filter is required")
	}
	body := map[string]interface{}{
		"filter": map[string]interface{}{
			"must": must,
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal delete filter: %w", err)
	}

	url := fmt.Sprintf("%s/collections/%s/points/delete", q.endpoint, q.collection)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	q.setHeaders(req)

	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant delete request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("qdrant delete error %d: %s", resp.StatusCode, string(respBody))
}

// DocRef is a minimal document identity read from Qdrant payloads, used by the
// registry reconciliation.
type DocRef struct {
	TenantID   string
	DocID      string
	Permission string
}

// ListDocs scrolls the collection and returns the distinct (tenant_id, doc_id)
// groups with their permission. Used by document-registry reconciliation; a
// limit <= 0 scrolls the whole collection.
func (q *QdrantStorer) ListDocs(ctx context.Context, limit, pageSize int) ([]DocRef, error) {
	if pageSize <= 0 {
		pageSize = 100
	}
	seen := map[string]DocRef{}
	var offset any
	collected := 0

	for {
		if limit > 0 && collected >= limit {
			break
		}
		body := map[string]interface{}{
			"limit":        pageSize,
			"with_payload": true,
		}
		if offset != nil {
			body["offset"] = offset
		}
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal scroll body: %w", err)
		}

		url := fmt.Sprintf("%s/collections/%s/points/scroll", q.endpoint, q.collection)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		q.setHeaders(req)

		resp, err := q.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("qdrant scroll request failed: %w", err)
		}
		var sr struct {
			Result struct {
				Points []struct {
					Payload map[string]interface{} `json:"payload"`
				} `json:"points"`
				NextPageOffset any `json:"next_page_offset"`
			} `json:"result"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&sr)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode scroll response: %w", decodeErr)
		}

		for _, p := range sr.Result.Points {
			tenant, _ := p.Payload["tenant_id"].(string)
			docID, _ := p.Payload["doc_id"].(string)
			perm, _ := p.Payload["permission"].(string)
			if tenant == "" || docID == "" {
				continue
			}
			key := tenant + "/" + docID
			if _, ok := seen[key]; !ok {
				seen[key] = DocRef{TenantID: tenant, DocID: docID, Permission: perm}
			}
		}
		collected += len(sr.Result.Points)
		if sr.Result.NextPageOffset == nil {
			break
		}
		offset = sr.Result.NextPageOffset
	}

	refs := make([]DocRef, 0, len(seen))
	for _, r := range seen {
		refs = append(refs, r)
	}
	return refs, nil
}

// Close releases HTTP client resources.
func (q *QdrantStorer) Close() error {
	q.client.CloseIdleConnections()
	return nil
}

func (q *QdrantStorer) ensureCollection(ctx context.Context) error {
	url := fmt.Sprintf("%s/collections/%s", q.endpoint, q.collection)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	q.setHeaders(req)

	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant connection failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		return nil
	}

	createBody := map[string]interface{}{
		"vectors": map[string]interface{}{
			"dense": map[string]interface{}{
				"size":     q.dimension,
				"distance": "Cosine",
			},
		},
		"sparse_vectors": map[string]interface{}{
			"sparse": map[string]interface{}{},
		},
	}

	return q.doPut(ctx, url, createBody)
}

func (q *QdrantStorer) doPut(ctx context.Context, url string, body interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "PUT", url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	q.setHeaders(req)

	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}

	respBody, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("qdrant error %d: %s", resp.StatusCode, string(respBody))
}

func (q *QdrantStorer) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if q.apiKey != "" {
		req.Header.Set("api-key", q.apiKey)
	}
}

func normalizeChunkPermission(raw string) string {
	permission := strings.ToLower(strings.TrimSpace(raw))
	if permission == "" {
		return "internal"
	}
	if _, ok := allowedDocPermissions[permission]; !ok {
		return "internal"
	}
	return permission
}

// chunkIDToUint converts a chunk ID to uint64 via FNV hash (Qdrant requires numeric or UUID IDs).
func chunkIDToUint(chunkID string) uint64 {
	var h uint64 = 14695981039346656037 // FNV offset basis
	for _, c := range chunkID {
		h ^= uint64(c)
		h *= 1099511628211 // FNV prime
	}
	return h
}

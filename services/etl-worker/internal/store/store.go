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
	"sort"
	"strings"
	"sync"
	"time"

	"ai-etl-pipeline/internal/indexmanifest"
	"ai-etl-pipeline/internal/model"
)

// --- In-memory store (development) ---

// MemoryStorer provides an in-memory Storer for development and testing.
type MemoryStorer struct {
	mu          sync.RWMutex
	data        map[string]model.Chunk
	generations map[string]map[string]model.Chunk
}

// NewMemoryStorer creates an in-memory store.
func NewMemoryStorer() *MemoryStorer {
	return &MemoryStorer{data: make(map[string]model.Chunk), generations: make(map[string]map[string]model.Chunk)}
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

func (s *MemoryStorer) UpsertGeneration(_ context.Context, identity indexmanifest.GenerationIdentity, chunk model.Chunk) error {
	if identity.GenerationID == "" || identity.TenantID != chunk.TenantID || identity.DocumentID != chunk.DocID || identity.DocumentVersionID == "" {
		return indexmanifest.ErrInvalidManifest
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generations[identity.GenerationID] == nil {
		s.generations[identity.GenerationID] = make(map[string]model.Chunk)
	}
	s.generations[identity.GenerationID][chunk.ChunkID] = chunk
	return nil
}

func (s *MemoryStorer) ObserveGeneration(_ context.Context, identity indexmanifest.GenerationIdentity) (indexmanifest.BackendObservation, error) {
	if identity.GenerationID == "" || identity.TenantID == "" || identity.DocumentID == "" || identity.DocumentVersionID == "" {
		return indexmanifest.BackendObservation{}, indexmanifest.ErrInvalidManifest
	}
	s.mu.RLock()
	chunks := make([]model.Chunk, 0, len(s.generations[identity.GenerationID]))
	for _, chunk := range s.generations[identity.GenerationID] {
		chunks = append(chunks, chunk)
	}
	s.mu.RUnlock()
	digest, err := indexmanifest.ChunkIdentityDigest(identity, chunks)
	if err != nil {
		return indexmanifest.BackendObservation{}, err
	}
	return indexmanifest.BackendObservation{Count: len(chunks), Digest: digest, ObservedAt: time.Now().UTC()}, nil
}

func (s *MemoryStorer) DeleteGeneration(_ context.Context, identity indexmanifest.GenerationIdentity) error {
	if identity.GenerationID == "" || identity.TenantID == "" || identity.DocumentID == "" || identity.DocumentVersionID == "" {
		return indexmanifest.ErrInvalidManifest
	}
	s.mu.Lock()
	delete(s.generations, identity.GenerationID)
	s.mu.Unlock()
	return nil
}

func (s *MemoryStorer) DeleteDocument(_ context.Context, tenantID, docID string) error {
	if tenantID == "" || docID == "" {
		return fmt.Errorf("tenant_id and doc_id are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, chunk := range s.data {
		if chunk.TenantID == tenantID && chunk.DocID == docID {
			delete(s.data, id)
		}
	}
	for generationID, chunks := range s.generations {
		for _, chunk := range chunks {
			if chunk.TenantID == tenantID && chunk.DocID == docID {
				delete(s.generations, generationID)
				break
			}
		}
	}
	return nil
}

var _ indexmanifest.Projection = (*MemoryStorer)(nil)

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

func (q *QdrantStorer) UpsertGeneration(ctx context.Context, identity indexmanifest.GenerationIdentity, chunk model.Chunk) error {
	if identity.GenerationID == "" || identity.TenantID != chunk.TenantID || identity.DocumentID != chunk.DocID || identity.DocumentVersionID == "" {
		return indexmanifest.ErrInvalidManifest
	}
	vectors := map[string]interface{}{"dense": chunk.Vector}
	if !chunk.SparseVector.IsEmpty() {
		vectors["sparse"] = map[string]interface{}{"indices": chunk.SparseVector.Indices, "values": chunk.SparseVector.Values}
	}
	payload := map[string]interface{}{
		"chunk_id": chunk.ChunkID, "doc_id": chunk.DocID, "tenant_id": chunk.TenantID,
		"document_version_id": identity.DocumentVersionID, "generation_id": identity.GenerationID,
		"content_hash": indexmanifest.ContentHash(chunk.Content), "content": chunk.Content,
		"index": chunk.Index, "permission": normalizeChunkPermission(chunk.Permission),
	}
	if len(chunk.Metadata) > 0 {
		payload["metadata"] = chunk.Metadata
	}
	body := map[string]interface{}{"points": []interface{}{map[string]interface{}{
		"id": chunkIDToUint(identity.GenerationID + "\x00" + chunk.ChunkID), "vector": vectors, "payload": payload,
	}}}
	url := fmt.Sprintf("%s/collections/%s/points?wait=true", q.endpoint, q.collection)
	return q.doPut(ctx, url, body)
}

func (q *QdrantStorer) ObserveGeneration(ctx context.Context, identity indexmanifest.GenerationIdentity) (indexmanifest.BackendObservation, error) {
	if identity.GenerationID == "" || identity.TenantID == "" || identity.DocumentID == "" || identity.DocumentVersionID == "" {
		return indexmanifest.BackendObservation{}, indexmanifest.ErrInvalidManifest
	}
	must := []map[string]interface{}{
		{"key": "tenant_id", "match": map[string]string{"value": identity.TenantID}},
		{"key": "doc_id", "match": map[string]string{"value": identity.DocumentID}},
		{"key": "document_version_id", "match": map[string]string{"value": identity.DocumentVersionID}},
		{"key": "generation_id", "match": map[string]string{"value": identity.GenerationID}},
	}
	var offset any
	identities := []indexmanifest.ChunkIdentity{}
	for {
		body := map[string]interface{}{"limit": 100, "with_payload": []string{"chunk_id", "index", "content_hash"}, "filter": map[string]interface{}{"must": must}}
		if offset != nil {
			body["offset"] = offset
		}
		data, err := json.Marshal(body)
		if err != nil {
			return indexmanifest.BackendObservation{}, err
		}
		url := fmt.Sprintf("%s/collections/%s/points/scroll", q.endpoint, q.collection)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			return indexmanifest.BackendObservation{}, err
		}
		q.setHeaders(req)
		resp, err := q.client.Do(req)
		if err != nil {
			return indexmanifest.BackendObservation{}, fmt.Errorf("qdrant generation scroll: %w", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return indexmanifest.BackendObservation{}, fmt.Errorf("qdrant generation scroll status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var result struct {
			Result struct {
				Points []struct {
					Payload map[string]interface{} `json:"payload"`
				} `json:"points"`
				// Keep the cursor as raw JSON. Qdrant point IDs are uint64 and
				// decoding them into interface{} would route through float64,
				// rounding large offsets and skipping points on later pages.
				NextPageOffset json.RawMessage `json:"next_page_offset"`
			} `json:"result"`
		}
		err = json.NewDecoder(resp.Body).Decode(&result)
		resp.Body.Close()
		if err != nil {
			return indexmanifest.BackendObservation{}, fmt.Errorf("decode qdrant generation: %w", err)
		}
		for _, point := range result.Result.Points {
			idx, ok := point.Payload["index"].(float64)
			if !ok {
				return indexmanifest.BackendObservation{}, fmt.Errorf("qdrant generation chunk index missing")
			}
			identities = append(identities, indexmanifest.ChunkIdentity{ChunkID: strVal(point.Payload["chunk_id"]), Index: int(idx), ContentHash: strVal(point.Payload["content_hash"])})
		}
		if len(result.Result.NextPageOffset) == 0 || string(result.Result.NextPageOffset) == "null" {
			break
		}
		offset = result.Result.NextPageOffset
	}
	digest, err := indexmanifest.IdentityDigest(identity, identities)
	if err != nil {
		return indexmanifest.BackendObservation{}, err
	}
	return indexmanifest.BackendObservation{Count: len(identities), Digest: digest, ObservedAt: time.Now().UTC()}, nil
}

func (q *QdrantStorer) DeleteGeneration(ctx context.Context, identity indexmanifest.GenerationIdentity) error {
	if identity.GenerationID == "" || identity.TenantID == "" || identity.DocumentID == "" || identity.DocumentVersionID == "" {
		return indexmanifest.ErrInvalidManifest
	}
	return q.deleteGenerationByFilter(ctx, []map[string]interface{}{
		{"key": "tenant_id", "match": map[string]string{"value": identity.TenantID}},
		{"key": "doc_id", "match": map[string]string{"value": identity.DocumentID}},
		{"key": "document_version_id", "match": map[string]string{"value": identity.DocumentVersionID}},
		{"key": "generation_id", "match": map[string]string{"value": identity.GenerationID}},
	})
}

func (q *QdrantStorer) deleteGenerationByFilter(ctx context.Context, must []map[string]interface{}) error {
	if len(must) == 0 {
		return fmt.Errorf("delete filter is required")
	}
	body := map[string]interface{}{"filter": map[string]interface{}{"must": must}}
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal generation delete filter: %w", err)
	}
	url := fmt.Sprintf("%s/collections/%s/points/delete?wait=true", q.endpoint, q.collection)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	q.setHeaders(req)
	resp, err := q.client.Do(req)
	if err != nil {
		return fmt.Errorf("qdrant generation delete request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("qdrant generation delete error %d: %s", resp.StatusCode, string(respBody))
}

var _ indexmanifest.Projection = (*QdrantStorer)(nil)
var _ indexmanifest.GenerationDeleter = (*QdrantStorer)(nil)

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

// DeleteDocument satisfies the recoverable deletion dependency seam.
func (q *QdrantStorer) DeleteDocument(ctx context.Context, tenantID, docID string) error {
	return q.DeleteByDocIDAndTenant(ctx, tenantID, docID)
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
				NextPageOffset json.RawMessage `json:"next_page_offset"`
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
		if len(sr.Result.NextPageOffset) == 0 || string(sr.Result.NextPageOffset) == "null" {
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

// StoredChunk is a chunk read back from a Qdrant payload (no vectors).
type StoredChunk struct {
	ChunkID           string
	DocID             string
	TenantID          string
	DocumentVersionID string
	GenerationID      string
	Content           string
	Index             int
	Permission        string
	Metadata          map[string]string
}

// ListChunksByDoc scrolls all points for a (tenant, doc) visible to the given
// permissions and returns them sorted by chunk index. Used by the document
// detail page to render a document's chunks. An empty allowedPermissions list
// omits the permission clause entirely.
func (q *QdrantStorer) ListChunksByDoc(ctx context.Context, tenantID, docID string, allowedPermissions []string) ([]StoredChunk, error) {
	if tenantID == "" || docID == "" {
		return nil, fmt.Errorf("tenant_id and doc_id are required")
	}
	must := []map[string]interface{}{
		{"key": "tenant_id", "match": map[string]string{"value": tenantID}},
		{"key": "doc_id", "match": map[string]string{"value": docID}},
	}
	if len(allowedPermissions) > 0 {
		must = append(must, map[string]interface{}{
			"key": "permission", "match": map[string]interface{}{"any": allowedPermissions},
		})
	}
	filter := map[string]interface{}{"must": must}

	var offset any
	var chunks []StoredChunk
	seen := make(map[string]struct{})
	for {
		body := map[string]interface{}{
			"limit":        100,
			"with_payload": true,
			"filter":       filter,
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
				NextPageOffset json.RawMessage `json:"next_page_offset"`
			} `json:"result"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&sr)
		resp.Body.Close()
		if decodeErr != nil {
			return nil, fmt.Errorf("decode scroll response: %w", decodeErr)
		}

		for _, p := range sr.Result.Points {
			chunk := StoredChunk{
				ChunkID:           strVal(p.Payload["chunk_id"]),
				DocID:             strVal(p.Payload["doc_id"]),
				TenantID:          strVal(p.Payload["tenant_id"]),
				DocumentVersionID: strVal(p.Payload["document_version_id"]),
				GenerationID:      strVal(p.Payload["generation_id"]),
				Content:           strVal(p.Payload["content"]),
				Permission:        strVal(p.Payload["permission"]),
			}
			if chunk.ChunkID == "" || chunk.Content == "" {
				continue
			}
			// Defensive read-side deduplication for historical parser output: old
			// paragraph overlap could emit a chunk fully contained in the following
			// chunk. Keep the richer chunk and never show both in document details.
			key := strings.Join(strings.Fields(chunk.Content), " ")
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			if idx, ok := p.Payload["index"].(float64); ok {
				chunk.Index = int(idx)
			}
			if md, ok := p.Payload["metadata"].(map[string]interface{}); ok {
				chunk.Metadata = stringMetadata(md)
			}
			chunks = append(chunks, chunk)
		}
		if len(sr.Result.NextPageOffset) == 0 || string(sr.Result.NextPageOffset) == "null" {
			break
		}
		offset = sr.Result.NextPageOffset
	}

	sort.SliceStable(chunks, func(i, j int) bool { return chunks[i].Index < chunks[j].Index })
	return removeContainedAdjacentChunks(chunks), nil
}

func removeContainedAdjacentChunks(chunks []StoredChunk) []StoredChunk {
	if len(chunks) < 2 {
		return chunks
	}
	result := make([]StoredChunk, 0, len(chunks))
	for _, chunk := range chunks {
		normalized := strings.Join(strings.Fields(chunk.Content), " ")
		if len(result) > 0 {
			previous := strings.Join(strings.Fields(result[len(result)-1].Content), " ")
			if previous != "" && strings.Contains(normalized, previous) && float64(len(previous))/float64(len(normalized)) >= 0.65 {
				result[len(result)-1] = chunk
				continue
			}
			if normalized != "" && strings.Contains(previous, normalized) && float64(len(normalized))/float64(len(previous)) >= 0.65 {
				continue
			}
		}
		result = append(result, chunk)
	}
	return result
}

// strVal returns a payload value as a string when it is one.
func strVal(v interface{}) string {
	s, _ := v.(string)
	return s
}

// stringMetadata keeps only string-valued entries of a JSON metadata map.
func stringMetadata(m map[string]interface{}) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
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

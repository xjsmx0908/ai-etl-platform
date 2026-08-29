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
	"sync"
	"time"

	"ai-etl-pipeline/internal/indexmanifest"
	"ai-etl-pipeline/internal/model"
)

// HTTPIndexer writes chunk documents to Elasticsearch.
type HTTPIndexer struct {
	address               string
	apiKey                string
	index                 string
	client                *http.Client
	generationMappingOnce sync.Once
	generationMappingErr  error
}

const cjkIndexVersion = "v2"

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

// DeleteByDocID removes all documents belonging to a doc_id via delete-by-query.
// Needed for document deletion and re-index flows.
func (i *HTTPIndexer) DeleteByDocID(ctx context.Context, docID string) error {
	if strings.TrimSpace(docID) == "" {
		return fmt.Errorf("doc_id is required")
	}
	return i.deleteByQuery(ctx, map[string]interface{}{
		"term": map[string]string{"doc_id": docID},
	})
}

// DeleteByDocIDAndTenant removes ES documents for a doc_id scoped to a tenant.
// The tenant_id term closes the same cross-tenant deletion bug as in Qdrant.
func (i *HTTPIndexer) DeleteByDocIDAndTenant(ctx context.Context, tenantID, docID string) error {
	if strings.TrimSpace(tenantID) == "" {
		return fmt.Errorf("tenant_id is required")
	}
	if strings.TrimSpace(docID) == "" {
		return fmt.Errorf("doc_id is required")
	}
	return i.deleteByQuery(ctx, map[string]interface{}{
		"bool": map[string]interface{}{
			"must": []map[string]interface{}{
				{"term": map[string]string{"tenant_id": tenantID}},
				{"term": map[string]string{"doc_id": docID}},
			},
		},
	})
}

func (i *HTTPIndexer) deleteByQuery(ctx context.Context, query map[string]interface{}) error {
	body, err := json.Marshal(map[string]interface{}{"query": query})
	if err != nil {
		return fmt.Errorf("marshal es delete query: %w", err)
	}

	endpoint := fmt.Sprintf("%s/%s/_delete_by_query?refresh=true", i.address, pathEscape(i.index))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create es delete request: %w", err)
	}
	i.setHeaders(req)

	resp, err := i.client.Do(req)
	if err != nil {
		return fmt.Errorf("es delete request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("es delete failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(respBody)))
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

func (i *HTTPIndexer) UpsertGeneration(ctx context.Context, identity indexmanifest.GenerationIdentity, chunk model.Chunk) error {
	if identity.GenerationID == "" || identity.TenantID != chunk.TenantID || identity.DocumentID != chunk.DocID || identity.DocumentVersionID == "" {
		return indexmanifest.ErrInvalidManifest
	}
	if err := i.ensureGenerationMappingOnce(ctx); err != nil {
		return err
	}
	doc := mapChunkToESDoc(chunk)
	doc.DocumentVersionID = identity.DocumentVersionID
	doc.GenerationID = identity.GenerationID
	doc.ContentHash = indexmanifest.ContentHash(chunk.Content)
	data, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("marshal generation document: %w", err)
	}
	docID := url.PathEscape(identity.GenerationID + "__" + chunk.ChunkID)
	endpoint := fmt.Sprintf("%s/%s/_doc/%s?refresh=wait_for", i.address, pathEscape(i.index), docID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	i.setHeaders(req)
	resp, err := i.client.Do(req)
	if err != nil {
		return fmt.Errorf("es generation index: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("es generation index failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
}

func (i *HTTPIndexer) ObserveGeneration(ctx context.Context, identity indexmanifest.GenerationIdentity) (indexmanifest.BackendObservation, error) {
	if identity.GenerationID == "" || identity.TenantID == "" || identity.DocumentID == "" || identity.DocumentVersionID == "" {
		return indexmanifest.BackendObservation{}, indexmanifest.ErrInvalidManifest
	}
	if err := i.ensureGenerationMappingOnce(ctx); err != nil {
		return indexmanifest.BackendObservation{}, err
	}
	refreshReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/%s/_refresh", i.address, pathEscape(i.index)), nil)
	if err != nil {
		return indexmanifest.BackendObservation{}, err
	}
	i.setHeaders(refreshReq)
	refreshResp, err := i.client.Do(refreshReq)
	if err != nil {
		return indexmanifest.BackendObservation{}, fmt.Errorf("es generation refresh: %w", err)
	}
	refreshResp.Body.Close()
	if refreshResp.StatusCode < 200 || refreshResp.StatusCode >= 300 {
		return indexmanifest.BackendObservation{}, fmt.Errorf("es generation refresh status=%d", refreshResp.StatusCode)
	}
	query := map[string]interface{}{"size": 500, "_source": []string{"chunk_id", "chunk_index", "content_hash"}, "sort": []string{"_doc"}, "query": map[string]interface{}{"bool": map[string]interface{}{"filter": []map[string]interface{}{
		{"term": map[string]string{"tenant_id": identity.TenantID}}, {"term": map[string]string{"doc_id": identity.DocumentID}}, {"term": map[string]string{"document_version_id": identity.DocumentVersionID}}, {"term": map[string]string{"generation_id": identity.GenerationID}},
	}}}}
	data, _ := json.Marshal(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/%s/_search?scroll=1m", i.address, pathEscape(i.index)), bytes.NewReader(data))
	if err != nil {
		return indexmanifest.BackendObservation{}, err
	}
	i.setHeaders(req)
	identities := []indexmanifest.ChunkIdentity{}
	var scrollID string
	for {
		resp, err := i.client.Do(req)
		if err != nil {
			return indexmanifest.BackendObservation{}, fmt.Errorf("es generation search: %w", err)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return indexmanifest.BackendObservation{}, fmt.Errorf("es generation search status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var page struct {
			ScrollID string `json:"_scroll_id"`
			Hits     struct {
				Hits []struct {
					Source struct {
						ChunkID     string `json:"chunk_id"`
						ChunkIndex  int    `json:"chunk_index"`
						ContentHash string `json:"content_hash"`
					} `json:"_source"`
				} `json:"hits"`
			} `json:"hits"`
		}
		err = json.NewDecoder(resp.Body).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return indexmanifest.BackendObservation{}, fmt.Errorf("decode es generation: %w", err)
		}
		scrollID = page.ScrollID
		if len(page.Hits.Hits) == 0 {
			break
		}
		for _, hit := range page.Hits.Hits {
			identities = append(identities, indexmanifest.ChunkIdentity{ChunkID: hit.Source.ChunkID, Index: hit.Source.ChunkIndex, ContentHash: hit.Source.ContentHash})
		}
		scrollData, _ := json.Marshal(map[string]string{"scroll": "1m", "scroll_id": scrollID})
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, i.address+"/_search/scroll", bytes.NewReader(scrollData))
		if err != nil {
			return indexmanifest.BackendObservation{}, err
		}
		i.setHeaders(req)
	}
	if scrollID != "" {
		clearData, _ := json.Marshal(map[string][]string{"scroll_id": {scrollID}})
		clearReq, _ := http.NewRequestWithContext(ctx, http.MethodDelete, i.address+"/_search/scroll", bytes.NewReader(clearData))
		i.setHeaders(clearReq)
		if clearResp, clearErr := i.client.Do(clearReq); clearErr == nil {
			clearResp.Body.Close()
		}
	}
	digest, err := indexmanifest.IdentityDigest(identity, identities)
	if err != nil {
		return indexmanifest.BackendObservation{}, err
	}
	return indexmanifest.BackendObservation{Count: len(identities), Digest: digest, ObservedAt: time.Now().UTC()}, nil
}

var _ indexmanifest.Projection = (*HTTPIndexer)(nil)

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

	physicalIndex := i.index + "_" + cjkIndexVersion
	createBody := map[string]interface{}{
		"mappings": map[string]interface{}{
			"properties": map[string]interface{}{
				"chunk_id":  map[string]string{"type": "keyword"},
				"doc_id":    map[string]string{"type": "keyword"},
				"tenant_id": map[string]string{"type": "keyword"},
				"content": map[string]string{
					"type":            "text",
					"analyzer":        "cjk",
					"search_analyzer": "cjk",
				},
				"permission": map[string]string{"type": "keyword"},
				"chunk_index": map[string]string{
					"type": "integer",
				},
				"file_hash":           map[string]string{"type": "keyword"},
				"document_version_id": map[string]string{"type": "keyword"},
				"generation_id":       map[string]string{"type": "keyword"},
				"content_hash":        map[string]string{"type": "keyword"},
				"created_at":          map[string]string{"type": "date"},
				"metadata":            map[string]string{"type": "flattened"},
			},
		},
	}
	data, err := json.Marshal(createBody)
	if err != nil {
		return fmt.Errorf("marshal es create index body: %w", err)
	}

	physicalURL := fmt.Sprintf("%s/%s", i.address, pathEscape(physicalIndex))
	createReq, err := http.NewRequestWithContext(ctx, http.MethodPut, physicalURL, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create es index request: %w", err)
	}
	i.setHeaders(createReq)

	createResp, err := i.client.Do(createReq)
	if err != nil {
		return fmt.Errorf("es create index request failed: %w", err)
	}
	defer createResp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(createResp.Body, 4096))
	created := createResp.StatusCode >= 200 && createResp.StatusCode < 300
	alreadyExists := createResp.StatusCode == http.StatusBadRequest &&
		(strings.Contains(string(body), "resource_already_exists_exception") ||
			strings.Contains(string(body), "already_exists"))
	if !created && !alreadyExists {
		return fmt.Errorf("es create index failed: status=%d body=%s", createResp.StatusCode, strings.TrimSpace(string(body)))
	}

	aliasData, err := json.Marshal(map[string]interface{}{
		"actions": []map[string]interface{}{
			{"add": map[string]interface{}{
				"index":          physicalIndex,
				"alias":          i.index,
				"is_write_index": true,
			}},
		},
	})
	if err != nil {
		return fmt.Errorf("marshal es alias body: %w", err)
	}
	aliasReq, err := http.NewRequestWithContext(ctx, http.MethodPost, i.address+"/_aliases", bytes.NewReader(aliasData))
	if err != nil {
		return fmt.Errorf("create es alias request: %w", err)
	}
	i.setHeaders(aliasReq)
	aliasResp, err := i.client.Do(aliasReq)
	if err != nil {
		return fmt.Errorf("es alias request failed: %w", err)
	}
	defer aliasResp.Body.Close()
	if aliasResp.StatusCode < 200 || aliasResp.StatusCode >= 300 {
		aliasBody, _ := io.ReadAll(io.LimitReader(aliasResp.Body, 4096))
		return fmt.Errorf("es alias creation failed: status=%d body=%s", aliasResp.StatusCode, strings.TrimSpace(string(aliasBody)))
	}

	slog.Info("elasticsearch CJK index created", "index", physicalIndex, "alias", i.index)
	return nil
}

func (i *HTTPIndexer) ensureGenerationMappings(ctx context.Context) error {
	data, err := json.Marshal(map[string]interface{}{"properties": generationMappings()})
	if err != nil {
		return fmt.Errorf("marshal generation mappings: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, fmt.Sprintf("%s/%s/_mapping", i.address, pathEscape(i.index)), bytes.NewReader(data))
	if err != nil {
		return err
	}
	i.setHeaders(req)
	resp, err := i.client.Do(req)
	if err != nil {
		return fmt.Errorf("update generation mappings: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("update generation mappings failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(responseBody)))
}

func (i *HTTPIndexer) ensureGenerationMappingOnce(ctx context.Context) error {
	i.generationMappingOnce.Do(func() {
		i.generationMappingErr = i.ensureGenerationMappings(ctx)
	})
	return i.generationMappingErr
}

func generationMappings() map[string]interface{} {
	return map[string]interface{}{
		"document_version_id": map[string]string{"type": "keyword"},
		"generation_id":       map[string]string{"type": "keyword"},
		"content_hash":        map[string]string{"type": "keyword"},
	}
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
	ChunkID           string            `json:"chunk_id"`
	DocID             string            `json:"doc_id"`
	TenantID          string            `json:"tenant_id"`
	Content           string            `json:"content"`
	Permission        string            `json:"permission"`
	ChunkIndex        int               `json:"chunk_index"`
	FileHash          string            `json:"file_hash,omitempty"`
	DocumentVersionID string            `json:"document_version_id,omitempty"`
	GenerationID      string            `json:"generation_id,omitempty"`
	ContentHash       string            `json:"content_hash,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	CreatedAt         string            `json:"created_at"`
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

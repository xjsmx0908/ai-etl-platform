package es

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-etl-pipeline/internal/model"
)

func TestMapChunkToESDoc_NormalizesPermissionAndCreatedAt(t *testing.T) {
	chunk := model.Chunk{
		ChunkID:    "doc-1_0001",
		DocID:      "doc-1",
		TenantID:   "tenant-a",
		Content:    "hello",
		Index:      1,
		Permission: "UNKNOWN",
		FileHash:   "abc",
		Metadata:   map[string]string{"contract_no": "CN-2026-0001"},
		CreatedAt:  time.Date(2026, 5, 24, 9, 0, 0, 0, time.UTC),
	}
	doc := mapChunkToESDoc(chunk)

	if doc.Permission != "internal" {
		t.Fatalf("expected permission normalized to internal, got %q", doc.Permission)
	}
	if doc.CreatedAt != "2026-05-24T09:00:00Z" {
		t.Fatalf("unexpected created_at: %q", doc.CreatedAt)
	}
	if doc.Metadata["contract_no"] != "CN-2026-0001" {
		t.Fatalf("expected metadata copied, got %+v", doc.Metadata)
	}
}

func TestHTTPIndexer_IndexChunk(t *testing.T) {
	var gotMethod string
	var gotPath string
	var gotAuth string
	var gotDoc map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")

		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
			return
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/_doc/"):
			if err := json.NewDecoder(r.Body).Decode(&gotDoc); err != nil {
				t.Fatalf("decode body: %v", err)
			}
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"result":"created"}`))
			return
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}))
	defer srv.Close()

	idx, err := NewHTTPIndexer(srv.URL, "abc123", "documents_text")
	if err != nil {
		t.Fatalf("new indexer: %v", err)
	}
	defer idx.Close()

	chunk := model.Chunk{
		ChunkID:    "doc-1_0003",
		DocID:      "doc-1",
		TenantID:   "tenant-a",
		Content:    "content",
		Index:      3,
		Permission: "public",
		Metadata:   map[string]string{"contract_no": "CN-2026-0001"},
		CreatedAt:  time.Date(2026, 5, 24, 9, 1, 2, 0, time.UTC),
	}
	if err := idx.IndexChunk(context.Background(), chunk); err != nil {
		t.Fatalf("index chunk: %v", err)
	}

	if gotMethod != http.MethodPut {
		t.Fatalf("expected PUT, got %s", gotMethod)
	}
	if !strings.Contains(gotPath, "/documents_text/_doc/doc-1_0003") {
		t.Fatalf("unexpected path: %s", gotPath)
	}
	if gotAuth != "ApiKey abc123" {
		t.Fatalf("unexpected auth header: %q", gotAuth)
	}
	if gotDoc["doc_id"] != "doc-1" {
		t.Fatalf("expected doc_id=doc-1, got %#v", gotDoc["doc_id"])
	}
	metadata := gotDoc["metadata"].(map[string]interface{})
	if metadata["contract_no"] != "CN-2026-0001" {
		t.Fatalf("expected metadata indexed, got %#v", metadata)
	}
}

func TestHTTPIndexer_EnsureIndex_CreatesWhenMissing(t *testing.T) {
	headCount := 0
	createCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			headCount++
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPut && !strings.Contains(r.URL.Path, "/_doc/") {
			createCount++
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"acknowledged":true}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	idx, err := NewHTTPIndexer(srv.URL, "", "documents_text")
	if err != nil {
		t.Fatalf("new indexer: %v", err)
	}
	defer idx.Close()

	if headCount != 1 {
		t.Fatalf("expected one HEAD request, got %d", headCount)
	}
	if createCount != 1 {
		t.Fatalf("expected one create request, got %d", createCount)
	}
}

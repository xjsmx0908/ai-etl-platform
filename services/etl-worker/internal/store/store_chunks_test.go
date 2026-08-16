package store

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ListChunksByDoc must send a tenant+doc+permission scroll filter, paginate,
// sort by index, and convert metadata to map[string]string.
func TestQdrantListChunksByDoc(t *testing.T) {
	var scrollBodies [][]byte
	scrollCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/points/scroll") {
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			raw, _ := json.Marshal(body)
			scrollBodies = append(scrollBodies, raw)
			scrollCalls++
			if scrollCalls == 1 {
				// First page: two chunks out of index order, one bad point skipped,
				// a non-string metadata value dropped by the converter.
				_, _ = w.Write([]byte(`{
					"result": {
						"points": [
							{"payload": {"chunk_id": "c2", "doc_id": "d1", "tenant_id": "t1",
								"content": "second", "index": 1, "permission": "internal",
								"metadata": {"order": "A-1", "n": 3}}},
							{"payload": {"chunk_id": "c1", "doc_id": "d1", "tenant_id": "t1",
								"content": "first", "index": 0, "permission": "internal",
								"metadata": {"order": "A-0"}}},
							{"payload": {"doc_id": "d1", "content": "no chunk id"}}
						],
						"next_page_offset": 100
					}
				}`))
				return
			}
			// Second page: no more points.
			_, _ = w.Write([]byte(`{"result": {"points": [], "next_page_offset": null}}`))
			return
		}
		w.WriteHeader(http.StatusOK) // ensureCollection etc.
	}))
	defer srv.Close()

	qs, err := NewQdrantStorer(srv.URL, "", "docs", 4)
	if err != nil {
		t.Fatalf("new storer: %v", err)
	}
	defer qs.Close()

	chunks, err := qs.ListChunksByDoc(context.Background(), "t1", "d1", []string{"public", "internal"})
	if err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[0].ChunkID != "c1" || chunks[1].ChunkID != "c2" {
		t.Fatalf("expected index order c1,c2, got %q,%q", chunks[0].ChunkID, chunks[1].ChunkID)
	}
	if chunks[0].Index != 0 || chunks[1].Index != 1 {
		t.Fatalf("expected indexes 0,1, got %d,%d", chunks[0].Index, chunks[1].Index)
	}
	if chunks[0].Metadata["order"] != "A-0" {
		t.Fatalf("expected metadata order A-0, got %v", chunks[0].Metadata)
	}
	if _, ok := chunks[1].Metadata["n"]; ok {
		t.Fatalf("non-string metadata value should be dropped, got %v", chunks[1].Metadata)
	}

	// Verify the scroll filter body.
	if len(scrollBodies) == 0 {
		t.Fatal("no scroll request captured")
	}
	var body map[string]interface{}
	if err := json.Unmarshal(scrollBodies[0], &body); err != nil {
		t.Fatalf("decode scroll body: %v", err)
	}
	filter := body["filter"].(map[string]interface{})
	must := filter["must"].([]interface{})
	if len(must) != 3 {
		t.Fatalf("expected 3 must clauses (tenant+doc+permission), got %d", len(must))
	}
}

// ListChunksByDoc must require a tenant and doc id.
func TestQdrantListChunksByDocRequiresTenantAndDoc(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	qs, err := NewQdrantStorer(srv.URL, "", "docs", 4)
	if err != nil {
		t.Fatalf("new storer: %v", err)
	}
	defer qs.Close()

	if _, err := qs.ListChunksByDoc(context.Background(), "", "d1", nil); err == nil {
		t.Fatal("expected error for empty tenant")
	}
	if _, err := qs.ListChunksByDoc(context.Background(), "t1", "", nil); err == nil {
		t.Fatal("expected error for empty doc id")
	}
}

// ListChunksByDoc with no allowed permissions must omit the permission clause.
func TestQdrantListChunksByDocNoPermissionFilter(t *testing.T) {
	var captured []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/points/scroll") {
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			raw, _ := json.Marshal(body)
			captured = raw
			_, _ = w.Write([]byte(`{"result": {"points": [], "next_page_offset": null}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	qs, err := NewQdrantStorer(srv.URL, "", "docs", 4)
	if err != nil {
		t.Fatalf("new storer: %v", err)
	}
	defer qs.Close()

	if _, err := qs.ListChunksByDoc(context.Background(), "t1", "d1", nil); err != nil {
		t.Fatalf("list chunks: %v", err)
	}
	var body map[string]interface{}
	_ = json.Unmarshal(captured, &body)
	filter := body["filter"].(map[string]interface{})
	must := filter["must"].([]interface{})
	if len(must) != 2 {
		t.Fatalf("expected 2 must clauses (no permission), got %d", len(must))
	}
}

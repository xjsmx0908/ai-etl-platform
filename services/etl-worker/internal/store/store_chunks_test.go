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

// A chunk fully contained in a sibling chunk is still a chunk of this document.
// The endpoint used to hide it, but hiding it here does not remove it from the
// retrieval index -- a citation could then name a chunk the detail page never
// shows. The endpoint stays faithful to the store instead; the cross-layer
// consistency check asserts endpoint chunks == stored chunks.
func TestQdrantListChunksByDocKeepsContainedOverlapVisible(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/points/scroll") {
			_, _ = w.Write([]byte(`{"result":{"points":[
				{"payload":{"chunk_id":"c1","doc_id":"d1","tenant_id":"t1","content":"完整的上一段","index":0}},
				{"payload":{"chunk_id":"c2","doc_id":"d1","tenant_id":"t1","content":"完整的上一段\n追加","index":1}}
			],"next_page_offset":null}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	qs, err := NewQdrantStorer(srv.URL, "", "docs", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer qs.Close()
	chunks, err := qs.ListChunksByDoc(context.Background(), "t1", "d1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected both chunks to stay visible, got %d: %+v", len(chunks), chunks)
	}
	if chunks[0].ChunkID != "c1" || chunks[1].ChunkID != "c2" {
		t.Fatalf("expected index order c1,c2, got %q,%q", chunks[0].ChunkID, chunks[1].ChunkID)
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

// Repeated points for one chunk id come back as they are stored: one entry per
// point, never one entry per chunk id. Which copy is the current one is decided
// by the document's published generation, and this layer does not read that --
// the caller applies publication policy and only then collapses.
//
// Collapsing here chose by scroll order, and when a document had been ingested
// more than once those copies belonged to different generations: a first copy
// from a superseded generation failed the caller's policy filter, which took the
// whole chunk id off the detail page even though the published copy was still in
// the index and still served by retrieval.
func TestQdrantListChunksByDocReturnsEveryStoredCopy(t *testing.T) {
	anonymous := `{"payload":{"chunk_id":"c1","doc_id":"d1","tenant_id":"t1","content":"同一段内容","index":0}}`
	superseded := `{"payload":{"chunk_id":"c1","doc_id":"d1","tenant_id":"t1","content":"同一段内容","index":0,` +
		`"document_version_id":"job-old","generation_id":"gen-old"}}`
	published := `{"payload":{"chunk_id":"c1","doc_id":"d1","tenant_id":"t1","content":"同一段内容","index":0,` +
		`"document_version_id":"job-new","generation_id":"gen-new","metadata":{"order":"A-0"}}}`

	for _, tc := range []struct {
		name   string
		points string
		want   string
	}{
		{"superseded copy first", superseded + "," + published, "job-old/gen-old,job-new/gen-new"},
		{"published copy first", published + "," + superseded, "job-new/gen-new,job-old/gen-old"},
		{"anonymous copy alongside", anonymous + "," + published, "/,job-new/gen-new"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/points/scroll") {
					_, _ = w.Write([]byte(`{"result":{"points":[` + tc.points + `],"next_page_offset":null}}`))
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			qs, err := NewQdrantStorer(srv.URL, "", "docs", 4)
			if err != nil {
				t.Fatal(err)
			}
			defer qs.Close()

			chunks, err := qs.ListChunksByDoc(context.Background(), "t1", "d1", nil)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, 0, len(chunks))
			for _, chunk := range chunks {
				got = append(got, chunk.DocumentVersionID+"/"+chunk.GenerationID)
			}
			if strings.Join(got, ",") != tc.want {
				t.Fatalf("expected every stored copy in scroll order %q, got %q", tc.want, strings.Join(got, ","))
			}
			// Nothing is decoded away on the way out, so the copy that names its
			// generation still carries the rest of its payload too.
			for _, chunk := range chunks {
				if chunk.GenerationID != "gen-new" {
					continue
				}
				if chunk.Metadata["order"] != "A-0" {
					t.Fatalf("expected the identified copy to keep its metadata, got %+v", chunk.Metadata)
				}
			}
		})
	}
}

// A chunk without identity that has different content is still a chunk of this
// document, and must not be dropped merely because a sibling chunk carries an
// identity.
func TestQdrantListChunksByDocKeepsIdentitylessChunkWithDistinctContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/points/scroll") {
			_, _ = w.Write([]byte(`{"result":{"points":[
				{"payload":{"chunk_id":"c1","doc_id":"d1","tenant_id":"t1","content":"没有身份的一段","index":0}},
				{"payload":{"chunk_id":"c2","doc_id":"d1","tenant_id":"t1","content":"有身份的另一段","index":1,
					"document_version_id":"job-1","generation_id":"gen-1"}}
			],"next_page_offset":null}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	qs, err := NewQdrantStorer(srv.URL, "", "docs", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer qs.Close()

	chunks, err := qs.ListChunksByDoc(context.Background(), "t1", "d1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected both distinct chunks to survive, got %d: %+v", len(chunks), chunks)
	}
	if chunks[0].ChunkID != "c1" || chunks[1].ChunkID != "c2" {
		t.Fatalf("expected index order c1,c2, got %q,%q", chunks[0].ChunkID, chunks[1].ChunkID)
	}
}

// Two different chunk ids can carry a byte-identical body: a document ingested
// twice produces the same text under different ids. Retrieval deduplicates by
// content and keeps whichever copy ranks first, so it can return either id --
// hiding one of them here would let a citation name a chunk the detail page
// never shows. Both stay visible; only repeated points for one chunk id collapse.
func TestQdrantListChunksByDocKeepsDistinctChunkIDsWithIdenticalContent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/points/scroll") {
			_, _ = w.Write([]byte(`{"result":{"points":[
				{"payload":{"chunk_id":"c1","doc_id":"d1","tenant_id":"t1","content":"同一段内容","index":0,
					"document_version_id":"job-1","generation_id":"gen-1"}},
				{"payload":{"chunk_id":"c2","doc_id":"d1","tenant_id":"t1","content":"同一段内容","index":1,
					"document_version_id":"job-1","generation_id":"gen-1"}}
			],"next_page_offset":null}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	qs, err := NewQdrantStorer(srv.URL, "", "docs", 4)
	if err != nil {
		t.Fatal(err)
	}
	defer qs.Close()

	chunks, err := qs.ListChunksByDoc(context.Background(), "t1", "d1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 {
		t.Fatalf("expected both chunk ids to stay visible, got %d: %+v", len(chunks), chunks)
	}
	if chunks[0].ChunkID != "c1" || chunks[1].ChunkID != "c2" {
		t.Fatalf("expected index order c1,c2, got %q,%q", chunks[0].ChunkID, chunks[1].ChunkID)
	}
}

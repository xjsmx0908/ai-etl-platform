package es

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-etl-pipeline/internal/indexmanifest"
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

func TestGenerationWriteUpdatesExistingIndexMappings(t *testing.T) {
	var mapping map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && r.URL.Path == "/documents_text/_mapping":
			_ = json.NewDecoder(r.Body).Decode(&mapping)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/_doc/"):
			w.WriteHeader(http.StatusCreated)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	idx, err := NewHTTPIndexer(srv.URL, "", "documents_text")
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	identity := indexmanifest.GenerationIdentity{GenerationID: "gen-1", VersionIdentity: indexmanifest.VersionIdentity{TenantID: "tenant-a", DocumentID: "doc", DocumentVersionID: "job-1"}}
	if err := idx.UpsertGeneration(context.Background(), identity, model.Chunk{ChunkID: "doc_0000", TenantID: "tenant-a", DocID: "doc", Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	properties := mapping["properties"].(map[string]interface{})
	for _, field := range []string{"generation_id", "document_version_id", "content_hash"} {
		if properties[field] == nil {
			t.Errorf("mapping missing %s", field)
		}
	}
}

func TestNewHTTPIndexerCreatesVersionedCJKIndexAndWriteAlias(t *testing.T) {
	var created map[string]interface{}
	var aliased map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead && r.URL.Path == "/documents_text":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPut && r.URL.Path == "/documents_text_v2":
			_ = json.NewDecoder(r.Body).Decode(&created)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/_aliases":
			_ = json.NewDecoder(r.Body).Decode(&aliased)
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	idx, err := NewHTTPIndexer(srv.URL, "", "documents_text")
	if err != nil {
		t.Fatalf("new indexer: %v", err)
	}
	defer idx.Close()

	mappings := created["mappings"].(map[string]interface{})
	properties := mappings["properties"].(map[string]interface{})
	content := properties["content"].(map[string]interface{})
	if content["analyzer"] != "cjk" || content["search_analyzer"] != "cjk" {
		t.Fatalf("expected CJK analyzer mapping, got %#v", content)
	}
	actions := aliased["actions"].([]interface{})
	add := actions[0].(map[string]interface{})["add"].(map[string]interface{})
	if add["alias"] != "documents_text" || add["index"] != "documents_text_v2" || add["is_write_index"] != true {
		t.Fatalf("unexpected alias action: %#v", add)
	}
}

// DeleteByDocID must POST a delete-by-query with a doc_id term filter.
func TestHTTPIndexer_DeleteByDocID(t *testing.T) {
	var got map[string]interface{}
	var path, rawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		rawQuery = r.URL.RawQuery
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	idx, err := NewHTTPIndexer(srv.URL, "", "documents_text")
	if err != nil {
		t.Fatalf("new indexer: %v", err)
	}
	defer idx.Close()

	if err := idx.DeleteByDocID(context.Background(), "doc-7"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if path != "/documents_text/_delete_by_query" {
		t.Fatalf("unexpected path: %s", path)
	}
	if rawQuery != "refresh=true" {
		t.Fatalf("expected refresh=true, got %q", rawQuery)
	}
	query := got["query"].(map[string]interface{})
	term := query["term"].(map[string]interface{})
	if term["doc_id"] != "doc-7" {
		t.Fatalf("unexpected delete query: %v", got)
	}
}

// DeleteByDocIDAndTenant must scope the delete-by-query to tenant_id AND doc_id
// (cross-tenant bug fix).
func TestHTTPIndexer_DeleteByDocIDAndTenant(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	idx, err := NewHTTPIndexer(srv.URL, "", "documents_text")
	if err != nil {
		t.Fatalf("new indexer: %v", err)
	}
	defer idx.Close()

	if err := idx.DeleteByDocIDAndTenant(context.Background(), "tenant-a", "doc-7"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	query := got["query"].(map[string]interface{})
	boolQ := query["bool"].(map[string]interface{})
	must := boolQ["must"].([]interface{})
	seen := map[string]string{}
	for _, m := range must {
		term := m.(map[string]interface{})["term"].(map[string]interface{})
		for k, v := range term {
			seen[k] = v.(string)
		}
	}
	if seen["tenant_id"] != "tenant-a" || seen["doc_id"] != "doc-7" {
		t.Fatalf("expected tenant+doc scoped delete query, got %v", got)
	}
}

func TestHTTPIndexer_DeleteByDocIDRequiresID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	idx, _ := NewHTTPIndexer(srv.URL, "", "documents_text")
	defer idx.Close()
	if err := idx.DeleteByDocID(context.Background(), ""); err == nil {
		t.Fatal("expected error for empty doc_id")
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

func TestHTTPIndexerGenerationProjectionUsesScopedDocumentIdentity(t *testing.T) {
	var paths []string
	var docs []map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/documents_text/_mapping" {
			w.WriteHeader(http.StatusOK)
			return
		}
		paths = append(paths, r.URL.Path)
		var doc map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&doc)
		docs = append(docs, doc)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	idx, err := NewHTTPIndexer(srv.URL, "", "documents_text")
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	chunk := model.Chunk{ChunkID: "doc_0000", DocID: "doc", TenantID: "tenant-a", Content: "hello", Index: 0}
	for _, generationID := range []string{"gen-1", "gen-2"} {
		identity := indexmanifest.GenerationIdentity{GenerationID: generationID, VersionIdentity: indexmanifest.VersionIdentity{TenantID: "tenant-a", DocumentID: "doc", DocumentVersionID: "job-1"}}
		if err := idx.UpsertGeneration(context.Background(), identity, chunk); err != nil {
			t.Fatal(err)
		}
	}
	if len(paths) != 2 || paths[0] == paths[1] {
		t.Fatalf("generation document paths=%v", paths)
	}
	if docs[0]["generation_id"] != "gen-1" || docs[0]["document_version_id"] != "job-1" || docs[0]["content_hash"] != indexmanifest.ContentHash("hello") {
		t.Fatalf("unexpected document: %+v", docs[0])
	}
}

func TestHTTPIndexerGenerationProjectionObservesIdentityDigest(t *testing.T) {
	identity := indexmanifest.GenerationIdentity{GenerationID: "gen-1", VersionIdentity: indexmanifest.VersionIdentity{TenantID: "tenant-a", DocumentID: "doc", DocumentVersionID: "job-1"}}
	var searchCalls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/documents_text/_mapping":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/documents_text/_refresh":
			w.WriteHeader(http.StatusOK)
		case r.URL.Path == "/documents_text/_search":
			searchCalls++
			_, _ = w.Write([]byte(`{"_scroll_id":"scroll-1","hits":{"hits":[{"_source":{"chunk_id":"doc_0001","chunk_index":1,"content_hash":"` + indexmanifest.ContentHash("second") + `"}},{"_source":{"chunk_id":"doc_0000","chunk_index":0,"content_hash":"` + indexmanifest.ContentHash("first") + `"}}]}}`))
		case r.URL.Path == "/_search/scroll" && r.Method == http.MethodPost:
			_, _ = w.Write([]byte(`{"_scroll_id":"scroll-1","hits":{"hits":[]}}`))
		case r.URL.Path == "/_search/scroll" && r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()
	idx, err := NewHTTPIndexer(srv.URL, "", "documents_text")
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	got, err := idx.ObserveGeneration(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := indexmanifest.IdentityDigest(identity, []indexmanifest.ChunkIdentity{{ChunkID: "doc_0000", Index: 0, ContentHash: indexmanifest.ContentHash("first")}, {ChunkID: "doc_0001", Index: 1, ContentHash: indexmanifest.ContentHash("second")}})
	if searchCalls != 1 || got.Count != 2 || got.Digest != want {
		t.Fatalf("observation=%+v searchCalls=%d", got, searchCalls)
	}
}

func TestHTTPIndexerDeletesOnlyExactGeneration(t *testing.T) {
	identity := indexmanifest.GenerationIdentity{GenerationID: "gen-old", VersionIdentity: indexmanifest.VersionIdentity{TenantID: "tenant-a", DocumentID: "doc", DocumentVersionID: "job-1"}}
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	idx, err := NewHTTPIndexer(srv.URL, "", "documents_text")
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	if err := idx.DeleteGeneration(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	filters := got["query"].(map[string]interface{})["bool"].(map[string]interface{})["filter"].([]interface{})
	seen := map[string]string{}
	for _, raw := range filters {
		for key, value := range raw.(map[string]interface{})["term"].(map[string]interface{}) {
			seen[key] = value.(string)
		}
	}
	if seen["tenant_id"] != "tenant-a" || seen["doc_id"] != "doc" || seen["document_version_id"] != "job-1" || seen["generation_id"] != "gen-old" {
		t.Fatalf("generation delete query=%+v", got)
	}
}

func TestHTTPIndexerGenerationDeleteRejectsPartialFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_, _ = w.Write([]byte(`{"timed_out":false,"failures":[{"cause":{"reason":"shard unavailable"}}]}`))
	}))
	defer srv.Close()
	idx, err := NewHTTPIndexer(srv.URL, "", "documents_text")
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	identity := indexmanifest.GenerationIdentity{GenerationID: "gen-old", VersionIdentity: indexmanifest.VersionIdentity{TenantID: "tenant-a", DocumentID: "doc", DocumentVersionID: "job-1"}}
	if err := idx.DeleteGeneration(context.Background(), identity); err == nil {
		t.Fatal("partial Elasticsearch delete reported success")
	}
}

func TestHTTPIndexer_EnsureIndex_CreatesWhenMissing(t *testing.T) {
	headCount := 0
	createCount := 0
	aliasCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			headCount++
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPut && !strings.Contains(r.URL.Path, "/_doc/") {
			createCount++
			if r.URL.Path != "/documents_text_v2" {
				t.Fatalf("expected versioned physical index, got %s", r.URL.Path)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"acknowledged":true}`))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/_aliases" {
			aliasCount++
			w.WriteHeader(http.StatusOK)
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
	if aliasCount != 1 {
		t.Fatalf("expected one alias request, got %d", aliasCount)
	}
}

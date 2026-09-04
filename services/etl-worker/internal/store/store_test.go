package store

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-etl-pipeline/internal/indexmanifest"
	"ai-etl-pipeline/internal/model"
)

// Upsert must send dense+sparse vectors and the tenant/permission payload.
func TestQdrantUpsertSendsVectorAndPayload(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	qs, err := NewQdrantStorer(srv.URL, "", "docs", 4)
	if err != nil {
		t.Fatalf("new storer: %v", err)
	}
	defer qs.Close()

	chunk := model.Chunk{
		ChunkID:    "c1",
		DocID:      "d1",
		TenantID:   "tenant-a",
		Content:    "text",
		Index:      0,
		Permission: "internal",
		Vector:     []float64{0.1, 0.2, 0.3, 0.4},
	}
	if err := qs.Upsert(context.Background(), chunk); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	points, ok := got["points"].([]interface{})
	if !ok || len(points) != 1 {
		t.Fatalf("expected 1 point, got %v", got)
	}
	point := points[0].(map[string]interface{})
	payload := point["payload"].(map[string]interface{})
	if payload["doc_id"] != "d1" || payload["tenant_id"] != "tenant-a" || payload["permission"] != "internal" {
		t.Fatalf("unexpected payload: %v", payload)
	}
	if _, ok := point["vector"]; !ok {
		t.Fatal("expected vector in point")
	}
}

// Upsert must include sparse vectors when present.
func TestQdrantUpsertIncludesSparseVector(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	qs, _ := NewQdrantStorer(srv.URL, "", "docs", 4)
	defer qs.Close()

	chunk := model.Chunk{
		ChunkID: "c1", DocID: "d1", TenantID: "t", Content: "x",
		Vector:       []float64{0.1, 0.2, 0.3, 0.4},
		SparseVector: model.SparseVector{Indices: []uint32{1, 5}, Values: []float32{0.7, 0.3}},
	}
	if err := qs.Upsert(context.Background(), chunk); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	vectors := got["points"].([]interface{})[0].(map[string]interface{})["vector"].(map[string]interface{})
	if _, ok := vectors["sparse"]; !ok {
		t.Fatal("expected sparse vector in upsert")
	}
}

func TestQdrantGenerationProjectionUsesGenerationScopedIdentity(t *testing.T) {
	var points []map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		var body struct {
			Points []map[string]interface{} `json:"points"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		points = append(points, body.Points...)
		if r.URL.Query().Get("wait") != "true" {
			t.Error("generation upsert must wait for acknowledgement")
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	qs, err := NewQdrantStorer(srv.URL, "", "docs", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer qs.Close()
	chunk := model.Chunk{ChunkID: "doc_0000", DocID: "doc", TenantID: "tenant-a", Content: "hello", Index: 0, Vector: []float64{.1, .2}}
	for _, generationID := range []string{"gen-1", "gen-2"} {
		identity := indexmanifest.GenerationIdentity{GenerationID: generationID, VersionIdentity: indexmanifest.VersionIdentity{TenantID: "tenant-a", DocumentID: "doc", DocumentVersionID: "job-1"}}
		if err := qs.UpsertGeneration(context.Background(), identity, chunk); err != nil {
			t.Fatal(err)
		}
	}
	if len(points) != 2 || points[0]["id"] == points[1]["id"] {
		t.Fatalf("generation point IDs are not distinct: %+v", points)
	}
	payload := points[0]["payload"].(map[string]interface{})
	if payload["generation_id"] != "gen-1" || payload["document_version_id"] != "job-1" || payload["content_hash"] != indexmanifest.ContentHash("hello") {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestQdrantGenerationProjectionObservesIdentityDigest(t *testing.T) {
	identity := indexmanifest.GenerationIdentity{GenerationID: "gen-1", VersionIdentity: indexmanifest.VersionIdentity{TenantID: "tenant-a", DocumentID: "doc", DocumentVersionID: "job-1"}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		filter := body["filter"].(map[string]interface{})
		if len(filter["must"].([]interface{})) != 4 {
			t.Fatalf("generation filter=%+v", filter)
		}
		_, _ = w.Write([]byte(`{"result":{"points":[{"payload":{"chunk_id":"doc_0001","index":1,"content_hash":"` + indexmanifest.ContentHash("second") + `"}},{"payload":{"chunk_id":"doc_0000","index":0,"content_hash":"` + indexmanifest.ContentHash("first") + `"}}],"next_page_offset":null}}`))
	}))
	defer srv.Close()
	qs, err := NewQdrantStorer(srv.URL, "", "docs", 2)
	if err != nil {
		t.Fatal(err)
	}
	got, err := qs.ObserveGeneration(context.Background(), identity)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := indexmanifest.IdentityDigest(identity, []indexmanifest.ChunkIdentity{{ChunkID: "doc_0000", Index: 0, ContentHash: indexmanifest.ContentHash("first")}, {ChunkID: "doc_0001", Index: 1, ContentHash: indexmanifest.ContentHash("second")}})
	if got.Count != 2 || got.Digest != want {
		t.Fatalf("observation=%+v want count=2 digest=%s", got, want)
	}
}

func TestQdrantGenerationProjectionPreservesLargeScrollCursor(t *testing.T) {
	identity := indexmanifest.GenerationIdentity{GenerationID: "gen-1", VersionIdentity: indexmanifest.VersionIdentity{TenantID: "tenant-a", DocumentID: "doc", DocumentVersionID: "job-1"}}
	var secondOffset string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls++
		if calls == 1 {
			_, _ = w.Write([]byte(`{"result":{"points":[],"next_page_offset":5750035}}`))
			return
		}
		secondOffset = string(body["offset"])
		_, _ = w.Write([]byte(`{"result":{"points":[],"next_page_offset":null}}`))
	}))
	defer srv.Close()
	qs, err := NewQdrantStorer(srv.URL, "", "docs", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer qs.Close()
	if _, err := qs.ObserveGeneration(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	if secondOffset != `5750035` {
		t.Fatalf("cursor=%s, want exact integer", secondOffset)
	}
}

func TestQdrantDeletesOnlyExactGeneration(t *testing.T) {
	identity := indexmanifest.GenerationIdentity{GenerationID: "gen-old", VersionIdentity: indexmanifest.VersionIdentity{TenantID: "tenant-a", DocumentID: "doc", DocumentVersionID: "job-1"}}
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Query().Get("wait") != "true" {
			t.Error("generation delete must wait for acknowledgement")
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	qs, err := NewQdrantStorer(srv.URL, "", "docs", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer qs.Close()
	if err := qs.DeleteGeneration(context.Background(), identity); err != nil {
		t.Fatal(err)
	}
	must := got["filter"].(map[string]interface{})["must"].([]interface{})
	seen := map[string]string{}
	for _, raw := range must {
		condition := raw.(map[string]interface{})
		seen[condition["key"].(string)] = condition["match"].(map[string]interface{})["value"].(string)
	}
	if seen["tenant_id"] != "tenant-a" || seen["doc_id"] != "doc" || seen["document_version_id"] != "job-1" || seen["generation_id"] != "gen-old" {
		t.Fatalf("generation delete filter=%+v", got)
	}
}

// Exists must map 200 -> true and 404 -> false.
func TestQdrantExists(t *testing.T) {
	var wantStatus int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ensureCollection check during NewQdrantStorer expects 200.
		if wantStatus == 0 {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(wantStatus)
	}))
	defer srv.Close()

	qs, _ := NewQdrantStorer(srv.URL, "", "docs", 4)
	defer qs.Close()

	wantStatus = http.StatusOK
	ok, err := qs.Exists(context.Background(), "c1")
	if err != nil || !ok {
		t.Fatalf("expected exists=true, got ok=%v err=%v", ok, err)
	}

	wantStatus = http.StatusNotFound
	ok, err = qs.Exists(context.Background(), "c2")
	if err != nil || ok {
		t.Fatalf("expected exists=false, got ok=%v err=%v", ok, err)
	}
}

// Invalid permission values must normalize to "internal".
func TestNormalizeChunkPermission(t *testing.T) {
	tests := map[string]string{
		"public":        "public",
		"INTERNAL":      "internal",
		" confidential": "confidential",
		"unknown-role":  "internal",
		"":              "internal",
	}
	for in, want := range tests {
		if got := normalizeChunkPermission(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// DeleteByDocID must POST a filter matching the doc_id payload.
func TestQdrantDeleteByDocID(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/collections/docs/points/delete" {
			_ = json.NewDecoder(r.Body).Decode(&got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	qs, _ := NewQdrantStorer(srv.URL, "", "docs", 4)
	defer qs.Close()

	if err := qs.DeleteByDocID(context.Background(), "doc-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	filter := got["filter"].(map[string]interface{})
	must := filter["must"].([]interface{})
	cond := must[0].(map[string]interface{})
	if cond["key"] != "doc_id" || cond["match"].(map[string]interface{})["value"] != "doc-1" {
		t.Fatalf("unexpected delete filter: %v", got)
	}
}

// DeleteByDocIDAndTenant must scope the delete filter to the tenant so a
// colliding doc_id in another tenant cannot be wiped (cross-tenant bug fix).
func TestQdrantDeleteByDocIDAndTenant(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path == "/collections/docs/points/delete" {
			_ = json.NewDecoder(r.Body).Decode(&got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	qs, _ := NewQdrantStorer(srv.URL, "", "docs", 4)
	defer qs.Close()

	if err := qs.DeleteByDocIDAndTenant(context.Background(), "tenant-a", "doc-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	filter := got["filter"].(map[string]interface{})
	must := filter["must"].([]interface{})
	seen := map[string]string{}
	for _, m := range must {
		cond := m.(map[string]interface{})
		seen[cond["key"].(string)] = cond["match"].(map[string]interface{})["value"].(string)
	}
	if seen["tenant_id"] != "tenant-a" || seen["doc_id"] != "doc-1" {
		t.Fatalf("expected tenant+doc scoped delete filter, got %v", got)
	}
	if _, hasTenant := seen["tenant_id"]; !hasTenant {
		t.Fatal("delete filter must include tenant_id")
	}
}

func TestQdrantDeleteByDocIDAndTenantRequiresTenant(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	qs, _ := NewQdrantStorer(srv.URL, "", "docs", 4)
	defer qs.Close()
	if err := qs.DeleteByDocIDAndTenant(context.Background(), "", "doc-1"); err == nil {
		t.Fatal("expected error for empty tenant_id")
	}
}

func TestQdrantDeleteByDocIDRequiresID(t *testing.T) {
	qs, _ := NewQdrantStorer(httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).URL, "", "docs", 4)
	defer qs.Close()
	if err := qs.DeleteByDocID(context.Background(), "  "); err == nil {
		t.Fatal("expected error for empty doc_id")
	}
}

// ensureCollection must create the collection when it does not exist.
func TestQdrantEnsureCollectionCreates(t *testing.T) {
	var createCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodPut {
			createCalled = true
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer srv.Close()

	qs, err := NewQdrantStorer(srv.URL, "", "docs", 4)
	if err != nil {
		t.Fatalf("new storer: %v", err)
	}
	defer qs.Close()
	if !createCalled {
		t.Fatal("expected collection creation call")
	}
}

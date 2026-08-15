package store

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

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

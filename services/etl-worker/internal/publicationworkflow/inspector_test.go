package publicationworkflow

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPIndexInspectorCountsTenantDocumentChunks(t *testing.T) {
	qdrant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/collections/docs/points/count" || r.Header.Get("api-key") != "q-key" {
			t.Fatalf("unexpected qdrant request path=%s api-key=%q", r.URL.Path, r.Header.Get("api-key"))
		}
		var body struct {
			Exact  bool `json:"exact"`
			Filter struct {
				Must []struct {
					Key   string `json:"key"`
					Match struct {
						Value string `json:"value"`
					} `json:"match"`
				} `json:"must"`
			} `json:"filter"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode qdrant body: %v", err)
		}
		if !body.Exact || len(body.Filter.Must) != 2 || body.Filter.Must[0].Key != "tenant_id" || body.Filter.Must[0].Match.Value != "tenant-a" || body.Filter.Must[1].Key != "doc_id" || body.Filter.Must[1].Match.Value != "doc-1" {
			t.Fatalf("unexpected qdrant body: %+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": map[string]int{"count": 7}})
	}))
	defer qdrant.Close()

	elastic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/docs_text/_count" || r.Header.Get("Authorization") != "ApiKey es-key" {
			t.Fatalf("unexpected elastic request path=%s auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode elastic body: %v", err)
		}
		query, _ := body["query"].(map[string]any)
		boolQuery, _ := query["bool"].(map[string]any)
		filters, _ := boolQuery["filter"].([]any)
		if len(filters) != 2 {
			t.Fatalf("unexpected elastic body: %+v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"count": 7})
	}))
	defer elastic.Close()

	inspector := NewHTTPIndexInspector(IndexInspectorOptions{
		QdrantEndpoint: qdrant.URL, QdrantAPIKey: "q-key", QdrantCollection: "docs",
		ElasticsearchAddress: elastic.URL, ElasticsearchAPIKey: "es-key", ElasticsearchIndex: "docs_text",
		HTTPClient: qdrant.Client(),
	})
	counts, err := inspector.CountDocumentChunks(context.Background(), "tenant-a", "doc-1")
	if err != nil {
		t.Fatalf("count document chunks: %v", err)
	}
	if counts.Vector != 7 || counts.Text != 7 {
		t.Fatalf("unexpected counts: %+v", counts)
	}
}

func TestHTTPIndexInspectorFailsClosedWhenBackendErrors(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer backend.Close()

	inspector := NewHTTPIndexInspector(IndexInspectorOptions{
		QdrantEndpoint: backend.URL, QdrantCollection: "docs",
		ElasticsearchAddress: backend.URL, ElasticsearchIndex: "docs_text",
		HTTPClient: backend.Client(),
	})
	if _, err := inspector.CountDocumentChunks(context.Background(), "tenant-a", "doc-1"); err == nil {
		t.Fatal("expected backend error")
	}
}

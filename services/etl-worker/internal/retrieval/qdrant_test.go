package retrieval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-etl-pipeline/internal/model"
)

func TestQdrantRetriever_SearchExtractsExactSchemaMetadata(t *testing.T) {
	// Search now issues two queries: the RRF-fused main query, and a dense-only
	// query that supplies the raw cosine score for relevance observability.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, ok := body["prefetch"]; ok {
			// Main RRF query.
			if body["with_payload"] != true {
				t.Fatalf("expected main query with_payload=true")
			}
			_, _ = w.Write([]byte(`{"result":{"points":[{"score":0.9,"payload":{"chunk_id":"c1","doc_id":"d1","tenant_id":"tenant-a","content":"contract status","metadata":{"contract_no":"CN-2026-0001","ignored":"x"}}}]}}`))
			return
		}
		// Dense-only score query.
		_, _ = w.Write([]byte(`{"result":{"points":[{"score":0.85,"payload":{"chunk_id":"c1"}}]}}`))
	}))
	defer srv.Close()

	retriever := NewQdrantRetriever(srv.URL, "", "documents", srv.Client())
	got, err := retriever.Search(context.Background(), SearchRequest{
		DenseVector:        []float64{0.1, 0.2},
		SparseVector:       model.SparseVector{Indices: []uint32{1}, Values: []float32{1}},
		Limit:              3,
		TenantID:           "tenant-a",
		AllowedPermissions: []string{"public"},
		ExactSchemaFields:  []string{"contract_no"},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if got[0].Metadata["contract_no"] != "CN-2026-0001" {
		t.Fatalf("expected schema metadata on candidate, got %+v", got[0].Metadata)
	}
	if _, ok := got[0].Metadata["ignored"]; ok {
		t.Fatalf("did not expect unconfigured metadata field, got %+v", got[0].Metadata)
	}
	// Raw dense cosine must be attached for relevance observability.
	if got[0].Relevance != 0.85 || got[0].RelevanceSource != SourceQdrant {
		t.Fatalf("expected raw cosine 0.85 attached, got %v from %q", got[0].Relevance, got[0].RelevanceSource)
	}
}

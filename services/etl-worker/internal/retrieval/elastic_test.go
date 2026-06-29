package retrieval

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestElasticRetriever_SearchFiltersTenantAndPermission(t *testing.T) {
	var gotAuth string
	var gotTenant string
	var gotPermissions []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")

		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		query := body["query"].(map[string]interface{})
		boolQuery := query["bool"].(map[string]interface{})
		filters := boolQuery["filter"].([]interface{})
		for _, filter := range filters {
			cond := filter.(map[string]interface{})
			if term, ok := cond["term"].(map[string]interface{}); ok {
				gotTenant = term["tenant_id"].(string)
			}
			if terms, ok := cond["terms"].(map[string]interface{}); ok {
				values := terms["permission"].([]interface{})
				for _, v := range values {
					gotPermissions = append(gotPermissions, v.(string))
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits":{"hits":[{"_score":4.2,"_source":{"chunk_id":"c1","doc_id":"d1","tenant_id":"tenant-a","content":"hello"}}]}}`))
	}))
	defer srv.Close()

	retriever := NewElasticRetriever(srv.URL, "abc123", "documents_text", srv.Client())
	got, err := retriever.Search(context.Background(), SearchRequest{
		Question:           "hello",
		Limit:              5,
		TenantID:           "tenant-a",
		AllowedPermissions: []string{"public", "internal"},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if gotAuth != "ApiKey abc123" {
		t.Fatalf("expected auth header, got %q", gotAuth)
	}
	if gotTenant != "tenant-a" {
		t.Fatalf("expected tenant filter, got %q", gotTenant)
	}
	if !reflect.DeepEqual(gotPermissions, []string{"public", "internal"}) {
		t.Fatalf("unexpected permission filter: %v", gotPermissions)
	}
	if len(got) != 1 || got[0].ChunkID != "c1" || got[0].Source != SourceElasticsearch {
		t.Fatalf("unexpected candidates: %+v", got)
	}
}

func TestElasticRetriever_SearchUsesExactSchemaMetadata(t *testing.T) {
	var gotShould []interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		query := body["query"].(map[string]interface{})
		boolQuery := query["bool"].(map[string]interface{})
		gotShould = boolQuery["should"].([]interface{})
		if boolQuery["minimum_should_match"].(float64) != 1 {
			t.Fatalf("expected minimum_should_match=1, got %#v", boolQuery["minimum_should_match"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits":{"hits":[{"_score":9.1,"_source":{"chunk_id":"c1","doc_id":"d1","tenant_id":"tenant-a","content":"contract status","metadata":{"contract_no":"CN-2026-0001","ignored":"x"}}}]}}`))
	}))
	defer srv.Close()

	retriever := NewElasticRetriever(srv.URL, "", "documents_text", srv.Client())
	got, err := retriever.Search(context.Background(), SearchRequest{
		Question:           "status CN-2026-0001",
		Limit:              5,
		TenantID:           "tenant-a",
		AllowedPermissions: []string{"public"},
		ExactSchemaFields:  []string{"contract_no"},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(gotShould) < 2 {
		t.Fatalf("expected content match plus metadata term should clauses, got %#v", gotShould)
	}
	if got[0].Metadata["contract_no"] != "CN-2026-0001" {
		t.Fatalf("expected schema metadata on candidate, got %+v", got[0].Metadata)
	}
	if _, ok := got[0].Metadata["ignored"]; ok {
		t.Fatalf("did not expect unconfigured metadata field, got %+v", got[0].Metadata)
	}
}

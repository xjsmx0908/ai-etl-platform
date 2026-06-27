package retrieval

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPReranker_ReordersByScores(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"index":1,"relevance_score":0.99},{"index":0,"relevance_score":0.10}]}`))
	}))
	defer srv.Close()

	reranker := NewHTTPReranker(srv.URL, "", "test", srv.Client())
	got, err := reranker.Rerank(context.Background(), "query", []Candidate{
		{ChunkID: "c1", Content: "first", Score: 0.8},
		{ChunkID: "c2", Content: "second", Score: 0.7},
	}, 1)
	if err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if len(got) != 1 || got[0].ChunkID != "c2" || got[0].Rank != 1 {
		t.Fatalf("unexpected reranked candidates: %+v", got)
	}
}

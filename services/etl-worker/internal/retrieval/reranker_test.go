package retrieval

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestHTTPReranker_PrefixesFileName(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload struct {
			Documents []string `json:"documents"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		got = payload.Documents
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":0.9}]}`))
	}))
	defer srv.Close()

	reranker := NewHTTPReranker(srv.URL, "", "test", srv.Client())
	if _, err := reranker.Rerank(context.Background(), "陈伟的OA账号是什么？", []Candidate{
		{ChunkID: "c1", DocID: "oa", Content: "陈伟 G00024", Metadata: map[string]string{"file_name": "OA账号.xls"}},
	}, 1); err != nil {
		t.Fatalf("rerank: %v", err)
	}
	if len(got) != 1 || !strings.HasPrefix(got[0], "标题: OA账号.xls\n") || !strings.Contains(got[0], "陈伟 G00024") {
		t.Fatalf("expected filename prefix, got %#v", got)
	}
}

func TestRerankDocumentTextOmitsEmptyFileName(t *testing.T) {
	got := rerankDocumentText(Candidate{Content: "only body"})
	if got != "only body" {
		t.Fatalf("expected original content, got %#v", got)
	}
}

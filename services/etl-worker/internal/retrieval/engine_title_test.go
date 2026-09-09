package retrieval

import (
	"context"
	"reflect"
	"sync"
	"testing"
)

type capturingRetriever struct {
	name string
	mu   sync.Mutex
	got  SearchRequest
}

func (r *capturingRetriever) Name() string { return r.name }

func (r *capturingRetriever) Search(_ context.Context, req SearchRequest) ([]Candidate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = req
	return []Candidate{{
		ChunkID: "c1", DocID: "doc-gang", Content: "签证材料", Source: r.name, Rank: 1, Score: 1,
	}}, nil
}

func TestEnginePassesTitleMatchDocIDsToBackends(t *testing.T) {
	embedServer := newPolicyEvalEmbedServer(t)
	defer embedServer.Close()

	elastic := &capturingRetriever{name: SourceElasticsearch}
	engine := newPolicyEvalEngine(embedServer.URL, embedServer.Client(), map[string]Retriever{
		SourceElasticsearch: elastic,
	}, NoopReranker{}, false, "")

	if _, err := engine.Retrieve(context.Background(), Request{
		Question:           "赴港流程是什么？",
		TopK:               1,
		TenantID:           "tenant-a",
		AllowedPermissions: []string{"public"},
		KnowledgeBaseID:    "user-uploads",
		TitleMatchDocIDs:   []string{"doc-gang"},
	}); err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if !reflect.DeepEqual(elastic.got.TitleMatchDocIDs, []string{"doc-gang"}) {
		t.Fatalf("expected title match doc ids to reach elasticsearch, got %#v", elastic.got.TitleMatchDocIDs)
	}
}

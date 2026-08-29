package retrieval

import (
	"context"
	"errors"
	"testing"

	"ai-etl-pipeline/internal/indexmanifest"
)

type visibilityResolverStub struct {
	visible map[string]bool
	err     error
	calls   int
}

func (r *visibilityResolverStub) ResolveVisibility(_ context.Context, _ string, refs []indexmanifest.GenerationReference) ([]bool, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	result := make([]bool, len(refs))
	for i, ref := range refs {
		result[i] = r.visible[ref.GenerationID]
	}
	return result, nil
}

type visibilityCacheStub struct{ sources []Candidate }

func (c visibilityCacheStub) Lookup(context.Context, CacheKey, []float64) ([]Candidate, bool, error) {
	return c.sources, true, nil
}
func (visibilityCacheStub) Store(context.Context, CacheKey, []float64, []Candidate) error {
	return nil
}
func (visibilityCacheStub) Flush(context.Context) error { return nil }
func (visibilityCacheStub) Close() error                { return nil }

func TestEngineFiltersBackendCandidatesToActiveGeneration(t *testing.T) {
	embedServer := newPolicyEvalEmbedServer(t)
	defer embedServer.Close()
	engine := newPolicyEvalEngine(embedServer.URL, embedServer.Client(), map[string]Retriever{
		SourceQdrant: staticPolicyEvalRetriever{name: SourceQdrant, candidates: []Candidate{
			{ChunkID: "same", DocID: "doc-1", DocumentVersionID: "job-old", GenerationID: "gen-old", Content: "stale", Rank: 1},
			{ChunkID: "same", DocID: "doc-1", DocumentVersionID: "job-new", GenerationID: "gen-active", Content: "active", Rank: 2},
		}},
		SourceElasticsearch: staticPolicyEvalRetriever{name: SourceElasticsearch, candidates: []Candidate{
			{ChunkID: "same", DocID: "doc-1", DocumentVersionID: "job-new", GenerationID: "gen-active", Content: "active", Rank: 1},
		}},
	}, NoopReranker{}, false, "")
	resolver := &visibilityResolverStub{visible: map[string]bool{"gen-active": true}}
	engine.visibility = resolver

	got, err := engine.Retrieve(context.Background(), Request{Question: "policy", TenantID: "acme", TopK: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sources) != 1 || got.Sources[0].GenerationID != "gen-active" {
		t.Fatalf("sources = %+v, want active generation only", got.Sources)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want one batch", resolver.calls)
	}
}

func TestEngineRevalidatesCachedCandidatesAgainstActiveGeneration(t *testing.T) {
	embedServer := newPolicyEvalEmbedServer(t)
	defer embedServer.Close()
	engine := newPolicyEvalEngine(embedServer.URL, embedServer.Client(), map[string]Retriever{
		SourceQdrant: staticPolicyEvalRetriever{name: SourceQdrant},
	}, NoopReranker{}, false, "")
	engine.cache = visibilityCacheStub{sources: []Candidate{
		{ChunkID: "old", DocID: "doc-1", DocumentVersionID: "job-old", GenerationID: "gen-old"},
		{ChunkID: "new", DocID: "doc-1", DocumentVersionID: "job-new", GenerationID: "gen-active"},
	}}
	engine.visibility = &visibilityResolverStub{visible: map[string]bool{"gen-active": true}}

	got, err := engine.Retrieve(context.Background(), Request{Question: "policy", TenantID: "acme", TopK: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !got.CacheHit || len(got.Sources) != 1 || got.Sources[0].GenerationID != "gen-active" {
		t.Fatalf("cached sources = %+v, cache_hit=%v", got.Sources, got.CacheHit)
	}
}

func TestEngineFailsClosedWhenVisibilityCannotBeResolved(t *testing.T) {
	embedServer := newPolicyEvalEmbedServer(t)
	defer embedServer.Close()
	engine := newPolicyEvalEngine(embedServer.URL, embedServer.Client(), map[string]Retriever{
		SourceQdrant: staticPolicyEvalRetriever{name: SourceQdrant, candidates: []Candidate{{ChunkID: "c1", DocID: "doc-1"}}},
	}, NoopReranker{}, false, "")
	engine.visibility = &visibilityResolverStub{err: errors.New("postgres unavailable")}

	if _, err := engine.Retrieve(context.Background(), Request{Question: "policy", TenantID: "acme"}); err == nil {
		t.Fatal("expected visibility resolver failure to fail retrieval closed")
	}
}

package retrieval

import "testing"

func TestFuseKeepsIdenticalChunkIDsFromDifferentGenerationsDistinct(t *testing.T) {
	got := Fuse(map[string][]Candidate{SourceQdrant: {
		{ChunkID: "c1", DocID: "doc-1", GenerationID: "gen-old", Rank: 1},
		{ChunkID: "c1", DocID: "doc-1", GenerationID: "gen-new", Rank: 2},
	}}, Route{UseQdrant: true, QdrantWeight: 1}, 10)
	if len(got) != 2 {
		t.Fatalf("fused candidates = %+v, want distinct generations", got)
	}
}

// RRF overwrites Score with 1/(k+rank), which is identical for every rank-1
// candidate regardless of actual relevance. Relevance gating therefore depends on
// the raw backend score surviving fusion.
func TestFuse_PreservesRawRelevanceScore(t *testing.T) {
	route := Route{Strategy: StrategySemantic, QdrantWeight: 1, ElasticWeight: 0}
	results := map[string][]Candidate{
		SourceQdrant: {
			{ChunkID: "c1", DocID: "d1", Score: 0.87, Source: SourceQdrant, Rank: 1},
		},
	}

	got := Fuse(results, route, 10)

	if len(got) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(got))
	}
	if got[0].Relevance != 0.87 {
		t.Errorf("raw cosine score lost in fusion: got %v, want 0.87", got[0].Relevance)
	}
	if got[0].RelevanceSource != SourceQdrant {
		t.Errorf("expected relevance source qdrant, got %q", got[0].RelevanceSource)
	}
	if got[0].Score == 0.87 {
		t.Error("Score should hold the RRF fusion value, not the raw score")
	}
}

// Cosine and BM25 are different scales; the gate only understands cosine, so a
// multi-backend hit must expose Qdrant's score rather than whichever arrived first.
func TestFuse_PrefersQdrantRelevanceOnCrossSourceHit(t *testing.T) {
	route := Route{Strategy: StrategyHybrid, QdrantWeight: 0.5, ElasticWeight: 0.5}
	results := map[string][]Candidate{
		SourceElasticsearch: {
			{ChunkID: "c1", DocID: "d1", Score: 14.2, Source: SourceElasticsearch, Rank: 1},
		},
		SourceQdrant: {
			{
				ChunkID: "c1", DocID: "d1", Score: 0.5, Source: SourceQdrant, Rank: 1,
				Relevance: 0.73, RelevanceSource: SourceQdrant,
			},
		},
	}

	got := Fuse(results, route, 10)

	if len(got) != 1 {
		t.Fatalf("expected merged candidate, got %d", len(got))
	}
	if got[0].RelevanceSource != SourceQdrant || got[0].Relevance != 0.73 {
		t.Errorf("expected qdrant cosine 0.73, got %v from %q", got[0].Relevance, got[0].RelevanceSource)
	}
}

func TestFuse_DeduplicatesAndWeightsByRoute(t *testing.T) {
	route := Route{
		Strategy:      StrategyExactKeyword,
		QdrantWeight:  0.25,
		ElasticWeight: 0.75,
	}
	results := map[string][]Candidate{
		SourceQdrant: {
			{ChunkID: "c1", DocID: "d1", Content: "from qdrant", Score: 0.9, Source: SourceQdrant, Rank: 1},
			{ChunkID: "c2", DocID: "d2", Content: "only qdrant", Score: 0.8, Source: SourceQdrant, Rank: 2},
		},
		SourceElasticsearch: {
			{ChunkID: "c1", DocID: "d1", Content: "from es", Score: 12, Source: SourceElasticsearch, Rank: 1},
			{ChunkID: "c3", DocID: "d3", Content: "only es", Score: 11, Source: SourceElasticsearch, Rank: 2},
		},
	}

	got := Fuse(results, route, 10)
	if len(got) != 3 {
		t.Fatalf("expected 3 fused candidates, got %d", len(got))
	}
	if got[0].ChunkID != "c1" {
		t.Fatalf("expected duplicated c1 to rank first, got %+v", got[0])
	}
	if got[0].Source != "elasticsearch,qdrant" {
		t.Fatalf("expected merged source, got %q", got[0].Source)
	}
	if got[1].ChunkID != "c3" {
		t.Fatalf("expected ES-only candidate second for exact route, got %+v", got[1])
	}
}

func TestFuse_SemanticRoutePrioritizesQdrantAndCrossSourceAgreement(t *testing.T) {
	route := Route{
		Strategy:      StrategySemantic,
		QdrantWeight:  0.80,
		ElasticWeight: 0.20,
	}
	results := map[string][]Candidate{
		SourceQdrant: {
			{ChunkID: "c1", DocID: "d1", Content: "qdrant top semantic match", Score: 0.8, Source: SourceQdrant, Rank: 1},
			{ChunkID: "c2", DocID: "d2", Content: "shared semantic match", Score: 0.7, Source: SourceQdrant, Rank: 2},
		},
		SourceElasticsearch: {
			{ChunkID: "c2", DocID: "d2", Content: "shared lexical match", Score: 14, Source: SourceElasticsearch, Rank: 1},
			{ChunkID: "c3", DocID: "d3", Content: "only lexical match", Score: 13, Source: SourceElasticsearch, Rank: 2},
		},
	}

	got := Fuse(results, route, 10)
	if len(got) != 3 {
		t.Fatalf("expected 3 fused candidates, got %d", len(got))
	}

	wantOrder := []string{"c2", "c1", "c3"}
	for i, want := range wantOrder {
		if got[i].ChunkID != want {
			t.Fatalf("expected candidate %d to be %s, got %+v", i+1, want, got[i])
		}
	}
	if got[0].Source != "elasticsearch,qdrant" {
		t.Fatalf("expected shared candidate source to be merged, got %q", got[0].Source)
	}
}

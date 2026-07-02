package retrieval

import "testing"

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

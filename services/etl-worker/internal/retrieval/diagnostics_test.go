package retrieval

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDiagnoseStagesReportsAggregateRequiredDocumentCoverage(t *testing.T) {
	required := []string{"required-a", "required-b"}
	backend := map[string][]Candidate{
		SourceQdrant: {
			{ChunkID: "a-1", DocID: "required-a"},
			{ChunkID: "noise-1", DocID: "noise"},
		},
		SourceElasticsearch: {
			{ChunkID: "b-1", DocID: "required-b"},
		},
	}
	fused := []Candidate{
		{ChunkID: "a-1", DocID: "required-a"},
		{ChunkID: "noise-1", DocID: "noise"},
	}
	selected := []Candidate{{ChunkID: "a-1", DocID: "required-a"}}

	got := DiagnoseStages(required, backend, fused, selected)
	if got.RequiredDocumentCount != 2 {
		t.Fatalf("required document count = %d, want 2", got.RequiredDocumentCount)
	}
	if got.Backend[SourceQdrant].HitDocumentCount != 1 || got.Backend[SourceQdrant].AllRequiredHit {
		t.Fatalf("qdrant coverage = %+v, want 1/2 and incomplete", got.Backend[SourceQdrant])
	}
	if got.Backend[SourceElasticsearch].HitDocumentCount != 1 || got.Backend[SourceElasticsearch].AllRequiredHit {
		t.Fatalf("elasticsearch coverage = %+v, want 1/2 and incomplete", got.Backend[SourceElasticsearch])
	}
	if got.Fused.HitDocumentCount != 1 || got.Fused.AllRequiredHit {
		t.Fatalf("fused coverage = %+v, want 1/2 and incomplete", got.Fused)
	}
	if got.Selected.HitDocumentCount != 1 || got.Selected.AllRequiredHit {
		t.Fatalf("selected coverage = %+v, want 1/2 and incomplete", got.Selected)
	}
}

func TestDiagnoseStagesDoesNotExposeRequiredDocumentIdentifiers(t *testing.T) {
	got := DiagnoseStages(
		[]string{"secret-document"},
		map[string][]Candidate{SourceQdrant: {{DocID: "secret-document"}}},
		[]Candidate{{DocID: "secret-document"}},
		[]Candidate{{DocID: "secret-document"}},
	)
	if got.RequiredDocumentCount != 1 || got.Selected.HitDocumentCount != 1 {
		t.Fatalf("unexpected aggregate coverage: %+v", got)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal diagnostics: %v", err)
	}
	if strings.Contains(string(encoded), "secret-document") {
		t.Fatalf("diagnostics exposed required id: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"all_required_max_rank":1`) {
		t.Fatalf("diagnostics omitted aggregate max rank: %s", encoded)
	}
}

func TestDiagnoseStagesReportsDeepestFirstRequiredDocumentRank(t *testing.T) {
	got := DiagnoseStages(
		[]string{"required-a", "required-b"},
		map[string][]Candidate{SourceQdrant: {
			{DocID: "required-a"},
			{DocID: "noise"},
			{DocID: "required-b"},
			{DocID: "required-a"},
		}},
		nil,
		nil,
	)

	coverage := got.Backend[SourceQdrant]
	if !coverage.AllRequiredHit || coverage.AllRequiredMaxRank != 3 {
		t.Fatalf("coverage = %+v, want all required with deepest first rank 3", coverage)
	}
}

func TestDiagnoseStagesLeavesMaxRankZeroWhenRequiredDocumentsAreMissing(t *testing.T) {
	got := DiagnoseStages(
		[]string{"required-a", "required-b"},
		map[string][]Candidate{SourceQdrant: {
			{DocID: "noise"},
			{DocID: "required-a"},
		}},
		nil,
		nil,
	)

	coverage := got.Backend[SourceQdrant]
	if coverage.AllRequiredHit || coverage.AllRequiredMaxRank != 0 {
		t.Fatalf("coverage = %+v, want incomplete coverage with max rank 0", coverage)
	}
}

func TestDiagnoseStagesDeduplicatesRequiredIDsAndCandidateChunks(t *testing.T) {
	got := DiagnoseStages(
		[]string{"required-a", "required-a", ""},
		map[string][]Candidate{SourceQdrant: {
			{ChunkID: "a-1", DocID: "required-a"},
			{ChunkID: "a-2", DocID: "required-a"},
		}},
		[]Candidate{{ChunkID: "a-1", DocID: "required-a"}},
		nil,
	)
	if got.RequiredDocumentCount != 1 || got.Backend[SourceQdrant].HitDocumentCount != 1 {
		t.Fatalf("expected deduplicated document coverage, got %+v", got)
	}
}

func TestDiagnoseStagesIdentifiesFusionTopKTruncation(t *testing.T) {
	candidates := make([]Candidate, 0, 51)
	for i := 1; i <= 50; i++ {
		candidates = append(candidates, Candidate{
			ChunkID: "noise-" + string(rune('a'+i%26)) + string(rune('0'+i%10)),
			DocID:   "noise",
			Rank:    i,
		})
	}
	candidates = append(candidates, Candidate{ChunkID: "required-b", DocID: "required-b", Rank: 51})
	fused := Fuse(
		map[string][]Candidate{SourceQdrant: candidates},
		Route{Strategy: StrategySemantic, QdrantWeight: 1},
		50,
	)
	got := DiagnoseStages(
		[]string{"required-a", "required-b"},
		map[string][]Candidate{SourceQdrant: append([]Candidate{{DocID: "required-a", Rank: 1}}, candidates...)},
		fused,
		nil,
	)
	if got.Backend[SourceQdrant].HitDocumentCount != 2 || !got.Backend[SourceQdrant].AllRequiredHit {
		t.Fatalf("backend coverage = %+v, want both required docs", got.Backend[SourceQdrant])
	}
	if got.Fused.HitDocumentCount != 0 || got.Fused.AllRequiredHit {
		t.Fatalf("fused coverage = %+v, want required docs lost after Top-50", got.Fused)
	}
}

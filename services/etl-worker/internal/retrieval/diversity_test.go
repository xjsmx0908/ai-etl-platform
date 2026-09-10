package retrieval

import "testing"

func TestDiversifyCandidatesDeduplicatesNormalizedContent(t *testing.T) {
	candidates := []Candidate{
		{ChunkID: "a-1", DocID: "a", Content: "办公用品 申请流程"},
		{ChunkID: "a-2", DocID: "a", Content: "  办公用品\n申请流程  "},
		{ChunkID: "b-1", DocID: "b", Content: "采购审批规则"},
	}

	got, stats := diversifyCandidates(candidates, 5, 2)
	if len(got) != 2 || got[0].ChunkID != "a-1" || got[1].ChunkID != "b-1" {
		t.Fatalf("expected normalized duplicate removed, got %+v", got)
	}
	if stats.DeduplicatedCount != 2 || stats.UniqueDocumentCount != 2 {
		t.Fatalf("unexpected diversity stats: %+v", stats)
	}
}

func TestDiversifyCandidatesCapsDocumentsWhenAlternativesExist(t *testing.T) {
	candidates := []Candidate{
		{ChunkID: "a-1", DocID: "a", Content: "a one"},
		{ChunkID: "a-2", DocID: "a", Content: "a two"},
		{ChunkID: "a-3", DocID: "a", Content: "a three"},
		{ChunkID: "a-4", DocID: "a", Content: "a four"},
		{ChunkID: "b-1", DocID: "b", Content: "b one"},
		{ChunkID: "c-1", DocID: "c", Content: "c one"},
	}

	got, _ := diversifyCandidates(candidates, 5, 2)
	want := []string{"a-1", "b-1", "c-1", "a-2"}
	if len(got) != len(want) {
		t.Fatalf("expected unique documents before a second chunk, got %+v", got)
	}
	for i, id := range want {
		if got[i].ChunkID != id {
			t.Fatalf("rank %d: expected %s, got %+v", i+1, id, got)
		}
	}
}

func TestDiversifyCandidatesKeepsSecondRequiredDocumentInTopK(t *testing.T) {
	candidates := []Candidate{
		{ChunkID: "noise-1", DocID: "noise", Content: "employee roster page 1"},
		{ChunkID: "req-a", DocID: "required-a", Content: "chen wei oa account"},
		{ChunkID: "other-1", DocID: "other-1", Content: "unrelated policy"},
		{ChunkID: "noise-2", DocID: "noise", Content: "employee roster page 2"},
		{ChunkID: "other-2", DocID: "other-2", Content: "another handbook"},
		{ChunkID: "req-b", DocID: "required-b", Content: "chen wei department record"},
	}

	got, stats := diversifyCandidates(candidates, 5, 2)
	gotIDs := make([]string, len(got))
	gotDocs := make(map[string]int, len(got))
	for i, candidate := range got {
		gotIDs[i] = candidate.ChunkID
		gotDocs[candidate.DocID]++
	}
	if gotDocs["required-b"] != 1 {
		t.Fatalf("expected required-b to survive top-5, got %v", gotIDs)
	}
	if gotDocs["noise"] != 1 {
		t.Fatalf("expected only one noise chunk before covering remaining documents, got %v", gotIDs)
	}
	if stats.UniqueDocumentCount != 5 {
		t.Fatalf("expected 5 unique documents in top-5, got %+v ids=%v", stats, gotIDs)
	}

	fusedCoverage := DiagnoseStages([]string{"required-a", "required-b"}, nil, candidates, nil)
	selectedCoverage := DiagnoseStages([]string{"required-a", "required-b"}, nil, candidates, got)
	if !fusedCoverage.Fused.AllRequiredHit {
		t.Fatalf("fixture should have both required docs in fused list: %+v", fusedCoverage.Fused)
	}
	if !selectedCoverage.Selected.AllRequiredHit {
		t.Fatalf("selected top-5 dropped a required document: %+v ids=%v", selectedCoverage.Selected, gotIDs)
	}
}

func TestDiversifyCandidatesCollapsesDuplicateDocumentsByFileHash(t *testing.T) {
	candidates := []Candidate{
		{ChunkID: "a-1", DocID: "a", Content: "one", Metadata: map[string]string{"file_hash": "same"}},
		{ChunkID: "b-2", DocID: "b", Content: "two", Metadata: map[string]string{"file_hash": "same"}},
		{ChunkID: "a-2", DocID: "a", Content: "two", Metadata: map[string]string{"file_hash": "same"}},
	}

	got, stats := diversifyCandidates(candidates, 5, 2)
	if len(got) != 2 || got[0].DocID != "a" || got[1].DocID != "a" {
		t.Fatalf("expected one canonical copy of the file, got %+v", got)
	}
	if stats.UniqueDocumentCount != 1 {
		t.Fatalf("expected one unique document, got %+v", stats)
	}
}

func TestDiversifyCandidatesAllowsMultipleChunksWithoutAlternatives(t *testing.T) {
	candidates := []Candidate{
		{ChunkID: "a-1", DocID: "a", Content: "one"},
		{ChunkID: "a-2", DocID: "a", Content: "two"},
		{ChunkID: "a-3", DocID: "a", Content: "three"},
	}

	got, stats := diversifyCandidates(candidates, 3, 2)
	if len(got) != 3 || got[2].ChunkID != "a-3" {
		t.Fatalf("expected third chunk to fill otherwise empty context, got %+v", got)
	}
	if stats.UniqueDocumentCount != 1 {
		t.Fatalf("expected one unique document, got %+v", stats)
	}
}

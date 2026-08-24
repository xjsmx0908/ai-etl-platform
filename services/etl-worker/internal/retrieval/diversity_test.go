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
	want := []string{"a-1", "a-2", "b-1", "c-1"}
	if len(got) != len(want) {
		t.Fatalf("expected strict document cap to return %d candidates, got %+v", len(want), got)
	}
	for i, id := range want {
		if got[i].ChunkID != id {
			t.Fatalf("rank %d: expected %s, got %+v", i+1, id, got)
		}
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

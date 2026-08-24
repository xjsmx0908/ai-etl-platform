package query

import "testing"

func TestCitationsFromAnswerReturnsOnlyNamedDocuments(t *testing.T) {
	sources := []SourceContext{
		{ChunkID: "a-1", DocID: "POLICY-A"},
		{ChunkID: "a-2", DocID: "POLICY-A"},
		{ChunkID: "b-1", DocID: "POLICY-B"},
	}

	got := citationsFromAnswer("申请需经过审批。\n来源: POLICY-B", sources)
	if len(got) != 1 || got[0].ChunkID != "b-1" {
		t.Fatalf("expected only the named document's first chunk, got %+v", got)
	}
}

func TestCitationsFromAnswerDoesNotCallRetrievedEvidenceACitation(t *testing.T) {
	sources := []SourceContext{{ChunkID: "a-1", DocID: "POLICY-A"}}
	if got := citationsFromAnswer("申请需经过审批。", sources); len(got) != 0 {
		t.Fatalf("expected no citations without a source marker, got %+v", got)
	}
}

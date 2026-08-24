package query

import (
	"testing"

	"ai-etl-pipeline/internal/retrieval"
)

func TestIsolateCandidatesByScopePreventsCrossScopeSynthesis(t *testing.T) {
	candidates := []retrieval.Candidate{
		{ChunkID: "upload-1", DocID: "upload", Metadata: map[string]string{"knowledge_base_id": "user-uploads", "applicable_scope": "organization"}},
		{ChunkID: "demo-1", DocID: "demo", Metadata: map[string]string{"knowledge_base_id": "enterprise-demo", "applicable_scope": "demo"}},
		{ChunkID: "upload-2", DocID: "upload", Metadata: map[string]string{"knowledge_base_id": "user-uploads", "applicable_scope": "organization"}},
	}

	got, decision := isolateCandidatesByScope(candidates)
	if len(got) != 2 || got[0].ChunkID != "upload-1" || got[1].ChunkID != "upload-2" {
		t.Fatalf("expected only top-ranked scope, got %+v", got)
	}
	if !decision.Ambiguous || decision.Filtered != 1 || decision.KnowledgeBaseID != "user-uploads" {
		t.Fatalf("unexpected decision: %+v", decision)
	}
}

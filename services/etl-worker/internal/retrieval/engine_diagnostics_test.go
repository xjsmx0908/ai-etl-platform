package retrieval

import (
	"context"
	"testing"
	"time"
)

func TestEngineDiagnosticsSeparatesBackendFusionAndFinalSelection(t *testing.T) {
	embedServer := newPolicyEvalEmbedServer(t)
	defer embedServer.Close()

	engine := newPolicyEvalEngine(
		embedServer.URL,
		embedServer.Client(),
		map[string]Retriever{
			SourceQdrant: staticPolicyEvalRetriever{
				name: SourceQdrant,
				candidates: []Candidate{
					{ChunkID: "required-a-1", DocID: "required-a", Content: "source a", Rank: 1},
					{ChunkID: "required-b-1", DocID: "required-b", Content: "source b", Rank: 2},
				},
			},
		},
		NoopReranker{}, false, "auto",
	)
	engine.timeout = time.Second

	result, err := engine.Retrieve(context.Background(), Request{
		Question:                 "需要两个来源",
		TopK:                     1,
		TenantID:                 "tenant-diagnostics",
		AllowedPermissions:       []string{"public"},
		DiagnosticRequiredDocIDs: []string{"required-a", "required-b"},
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if result.StageDiagnostics == nil {
		t.Fatal("expected stage diagnostics")
	}
	got := result.StageDiagnostics
	if got.Backend[SourceQdrant].HitDocumentCount != 2 || !got.Backend[SourceQdrant].AllRequiredHit {
		t.Fatalf("backend coverage = %+v, want complete 2-document coverage", got.Backend[SourceQdrant])
	}
	if got.Fused.HitDocumentCount != 2 || !got.Fused.AllRequiredHit {
		t.Fatalf("fused coverage = %+v, want complete 2-document coverage", got.Fused)
	}
	if got.Selected.HitDocumentCount != 1 || got.Selected.AllRequiredHit {
		t.Fatalf("selected coverage = %+v, want one of two documents", got.Selected)
	}
}

func TestEngineDiagnosticsRetainsSuccessfulBackendWithNoCandidates(t *testing.T) {
	embedServer := newPolicyEvalEmbedServer(t)
	defer embedServer.Close()

	engine := newPolicyEvalEngine(
		embedServer.URL,
		embedServer.Client(),
		map[string]Retriever{
			SourceQdrant: staticPolicyEvalRetriever{
				name:       SourceQdrant,
				candidates: []Candidate{{ChunkID: "required-a-1", DocID: "required-a", Content: "source a", Rank: 1}},
			},
			SourceElasticsearch: staticPolicyEvalRetriever{name: SourceElasticsearch},
		},
		NoopReranker{}, false, "auto",
	)
	engine.timeout = time.Second

	result, err := engine.Retrieve(context.Background(), Request{
		Question:                 "普通混合查询",
		TopK:                     5,
		TenantID:                 "tenant-diagnostics",
		AllowedPermissions:       []string{"public"},
		DiagnosticRequiredDocIDs: []string{"required-a", "required-b"},
	})
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	if result.StageDiagnostics == nil {
		t.Fatal("expected stage diagnostics")
	}
	elastic, ok := result.StageDiagnostics.Backend[SourceElasticsearch]
	if !ok {
		t.Fatalf("expected empty successful backend in diagnostics: %+v", result.StageDiagnostics.Backend)
	}
	if elastic.HitDocumentCount != 0 || elastic.AllRequiredHit {
		t.Fatalf("unexpected Elasticsearch coverage: %+v", elastic)
	}
}

package main

import (
	"context"
	"strings"
	"testing"

	"ai-etl-pipeline/internal/indexmanifest"
	"ai-etl-pipeline/internal/model"
	"ai-etl-pipeline/internal/store"
)

func TestEnsureDemoShowcase_NoopWhenDisabledOrUnconfigured(t *testing.T) {
	cfg := demoLoginConfig()
	cfg.DemoLoginEnabled = false
	if err := ensureDemoShowcase(context.Background(), cfg, nil); err != nil {
		t.Fatalf("disabled showcase: %v", err)
	}
	if err := ensureDemoShowcase(context.Background(), demoLoginConfig(), nil); err != nil {
		t.Fatalf("nil store showcase: %v", err)
	}
}

func TestDemoShowcaseUsesIsolatedManagedDocuments(t *testing.T) {
	if demoPublishedDocID == demoPendingDocID || demoPendingDocID == demoConfidentialDocID {
		t.Fatal("showcase documents must be distinct")
	}
	if demoSpaceID == "user-uploads" || demoSpaceID == "" {
		t.Fatal("showcase space must be a managed space, not user-uploads")
	}
}

func TestDemoHandbookChunksDescribeReleasePolicy(t *testing.T) {
	joined := strings.Join(demoHandbookChunks(), "\n")
	for _, needle := range []string{"已发布", "两名管理员", "Agent", "不能自行"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("handbook missing %q in %q", needle, joined)
		}
	}
	if len(demoHandbookChunks()) != 3 {
		t.Fatalf("handbook chunks=%d", len(demoHandbookChunks()))
	}
}

type stubEmbedder struct{}

func (stubEmbedder) Embed(_ context.Context, chunk *model.Chunk) error {
	chunk.Vector = []float64{0.1, 0.2, 0.3}
	return nil
}

func (stubEmbedder) Close() error { return nil }

func TestEnsureDemoShowcaseIndex_UpsertsPublishedHandbook(t *testing.T) {
	cfg := demoLoginConfig()
	qdrant := store.NewMemoryStorer()
	elastic := store.NewMemoryStorer()
	if err := ensureDemoShowcaseIndex(context.Background(), cfg, qdrant, elastic, stubEmbedder{}); err != nil {
		t.Fatalf("index showcase: %v", err)
	}
	identity := indexmanifest.GenerationIdentity{
		VersionIdentity: indexmanifest.VersionIdentity{
			TenantID: cfg.DemoTenantID, DocumentID: demoPublishedDocID, DocumentVersionID: demoPublishedVersionID,
		},
		GenerationID: demoPublishedGeneration,
	}
	observed, err := qdrant.ObserveGeneration(context.Background(), identity)
	if err != nil || observed.Count != 3 {
		t.Fatalf("qdrant count=%d err=%v", observed.Count, err)
	}
	if err := ensureDemoShowcaseIndex(context.Background(), cfg, qdrant, elastic, stubEmbedder{}); err != nil {
		t.Fatalf("idempotent index: %v", err)
	}
}

func TestIndexDemoShowcase_NoopWhenDisabled(t *testing.T) {
	cfg := demoLoginConfig()
	cfg.DemoLoginEnabled = false
	if err := indexDemoShowcase(context.Background(), cfg, store.NewMemoryStorer()); err != nil {
		t.Fatalf("disabled index: %v", err)
	}
}

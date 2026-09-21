package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	pgxmock "github.com/pashagolub/pgxmock/v5"

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

// Every showcase document whose seeded manifest claims chunks must actually have
// them in both retrieval backends. A manifest without chunks makes the release
// center Agent pre-review fail with "exact candidate content is unavailable",
// which pins the document in manual_exception forever.
func TestEnsureDemoShowcaseIndex_IndexesEveryShowcaseDocument(t *testing.T) {
	cfg := demoLoginConfig()
	qdrant := store.NewMemoryStorer()
	elastic := store.NewMemoryStorer()
	if err := ensureDemoShowcaseIndex(context.Background(), cfg, qdrant, elastic, stubEmbedder{}); err != nil {
		t.Fatalf("index showcase: %v", err)
	}
	docs := demoShowcaseDocuments()
	if len(docs) != 3 {
		t.Fatalf("showcase documents=%d, want 3", len(docs))
	}
	backends := []struct {
		name  string
		store *store.MemoryStorer
	}{{"qdrant", qdrant}, {"elasticsearch", elastic}}
	for _, doc := range docs {
		if len(doc.chunks) == 0 {
			t.Fatalf("showcase document %s declares no chunks", doc.docID)
		}
		identity := indexmanifest.GenerationIdentity{
			VersionIdentity: indexmanifest.VersionIdentity{
				TenantID: cfg.DemoTenantID, DocumentID: doc.docID, DocumentVersionID: doc.versionID,
			},
			GenerationID: doc.generationID,
		}
		for _, backend := range backends {
			observed, err := backend.store.ObserveGeneration(context.Background(), identity)
			if err != nil {
				t.Fatalf("observe %s/%s: %v", backend.name, doc.docID, err)
			}
			if observed.Count != len(doc.chunks) {
				t.Fatalf("%s holds %d chunks for %s but the manifest claims %d",
					backend.name, observed.Count, doc.docID, len(doc.chunks))
			}
		}
	}
}

// The seeded manifest expectation must equal the identity digest of the chunks
// the retrieval index actually holds. indexmanifest.observationMatches compares
// exactly these two values on every reconciliation pass, so a placeholder digest
// makes the showcase permanently divergent — and because the reconciliation
// failure path used to roll back, the divergence was also invisible.
func TestDemoShowcaseManifestExpectationMatchesIndexedChunks(t *testing.T) {
	cfg := demoLoginConfig()
	qdrant := store.NewMemoryStorer()
	elastic := store.NewMemoryStorer()
	if err := ensureDemoShowcaseIndex(context.Background(), cfg, qdrant, elastic, stubEmbedder{}); err != nil {
		t.Fatalf("index showcase: %v", err)
	}
	backends := []struct {
		name  string
		store *store.MemoryStorer
	}{{"qdrant", qdrant}, {"elasticsearch", elastic}}
	for _, doc := range demoShowcaseDocuments() {
		expected, err := demoShowcaseExpectedDigest(cfg, doc)
		if err != nil {
			t.Fatalf("derive expected digest %s: %v", doc.docID, err)
		}
		if expected == doc.digest {
			t.Fatalf("%s: manifest digest %q is the document file hash, not the chunk identity digest",
				doc.docID, expected)
		}
		if !strings.HasPrefix(expected, "sha256:") || len(expected) != len("sha256:")+64 {
			t.Fatalf("%s: digest %q is not a sha256 identity digest", doc.docID, expected)
		}
		for _, backend := range backends {
			observed, err := backend.store.ObserveGeneration(context.Background(), demoShowcaseIdentity(cfg, doc))
			if err != nil {
				t.Fatalf("observe %s/%s: %v", backend.name, doc.docID, err)
			}
			if observed.Count != len(doc.chunks) || observed.Digest != expected {
				t.Fatalf("%s/%s: observed count=%d digest=%s, manifest expects count=%d digest=%s",
					backend.name, doc.docID, observed.Count, observed.Digest, len(doc.chunks), expected)
			}
		}
	}
}

// The SQL seed is the half that writes index_manifests and ingestion_outbox, so
// the digest it stores has to be the derived one and the outbox event has to
// exist: FinishReconciliation joins ingestion_jobs to ingestion_outbox to
// resolve a repair target, and a manifest seeded without an outbox row has no
// replay path at all.
func TestEnsureDemoShowcaseDocumentSeedsDerivedDigestAndOutbox(t *testing.T) {
	cfg := demoLoginConfig()
	doc := demoShowcaseDocuments()[1]
	digest, err := demoShowcaseExpectedDigest(cfg, doc)
	if err != nil {
		t.Fatal(err)
	}
	if digest == doc.digest || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("seed digest %q is not a derived chunk identity digest", digest)
	}
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	task := fmt.Sprintf(`{"file_path":"demo/%s.md"}`, doc.docID)
	mock.ExpectBegin()
	mock.ExpectExec("INSERT INTO documents").WithArgs(
		cfg.DemoTenantID, doc.docID, doc.fileName, "demo/"+doc.docID+".md", doc.digest, doc.permission,
		"user-1", now, "admin-1", demoSpaceID, doc.publication, len(doc.chunks),
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO ingestion_jobs").WithArgs(
		doc.versionID, doc.eventID, cfg.DemoTenantID, doc.docID, "demo-sig-"+doc.docID, task, now,
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO ingestion_outbox").WithArgs(
		doc.eventID, doc.versionID, cfg.DemoTenantID, doc.docID, task, now,
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO index_manifests").WithArgs(
		doc.generationID, cfg.DemoTenantID, doc.docID, doc.versionID, len(doc.chunks), digest, now,
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("INSERT INTO document_releases").WithArgs(
		cfg.DemoTenantID, doc.docID, doc.versionID,
	).WillReturnResult(pgxmock.NewResult("INSERT", 1))

	tx, err := mock.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureDemoShowcaseDocument(tx, context.Background(), cfg, doc, "admin-1", "user-1", now); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The confidential showcase document must contain the sensitive-data evidence
// its seeded review finding refers to.
func TestDemoPayrollChunksCarrySensitiveEvidence(t *testing.T) {
	joined := strings.Join(demoPayrollChunks(), "\n")
	for _, needle := range []string{"银行账号", "社保公积金", "不得对外提供"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("payroll chunks missing %q in %q", needle, joined)
		}
	}
}

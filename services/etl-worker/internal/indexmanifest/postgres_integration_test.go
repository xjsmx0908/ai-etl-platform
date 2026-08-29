package indexmanifest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestPostgresConcurrentActivation requires a disposable PostgreSQL instance.
// It verifies the database-level serialization and stale-writer CAS rather
// than mocking SQL call order.
func TestPostgresConcurrentActivation(t *testing.T) {
	dsn := os.Getenv("INDEX_MANIFEST_TEST_DSN")
	if dsn == "" {
		t.Skip("set INDEX_MANIFEST_TEST_DSN to run PostgreSQL activation integration test")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("indexmanifest_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE TABLE index_manifests (
		generation_id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL,
		document_id TEXT NOT NULL, document_version_id TEXT NOT NULL,
		state TEXT NOT NULL, activated_at TIMESTAMPTZ
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE UNIQUE INDEX one_active ON index_manifests
		(tenant_id, document_version_id) WHERE state='active'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO index_manifests VALUES
		('gen-1','acme','doc-1','job-1','active',now()),
		('gen-2','acme','doc-1','job-1','ready',NULL),
		('gen-3','acme','doc-1','job-1','ready',NULL)`); err != nil {
		t.Fatal(err)
	}

	store := NewPostgresStore(pool)
	version := VersionIdentity{TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1"}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, generationID := range []string{"gen-2", "gen-3"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- store.Activate(ctx, ActivationTarget{
				Version: version, GenerationID: generationID, ExpectedActiveGenerationID: "gen-1",
			})
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	var successes, conflicts int
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatalf("unexpected activation error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d, want one of each", successes, conflicts)
	}
	var activeCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM index_manifests WHERE state='active'`).Scan(&activeCount); err != nil {
		t.Fatal(err)
	}
	if activeCount != 1 {
		t.Fatalf("active generations=%d, want 1", activeCount)
	}
}

func TestPostgresResolvesActiveAndLegacyVisibility(t *testing.T) {
	dsn := os.Getenv("INDEX_MANIFEST_TEST_DSN")
	if dsn == "" {
		t.Skip("set INDEX_MANIFEST_TEST_DSN to run PostgreSQL visibility integration test")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("indexvisibility_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE TABLE index_manifests (
		generation_id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL,
		document_id TEXT NOT NULL, document_version_id TEXT NOT NULL,
		state TEXT NOT NULL, last_reconcile_error TEXT NOT NULL DEFAULT ''
	); INSERT INTO index_manifests (generation_id,tenant_id,document_id,document_version_id,state) VALUES
		('gen-active','acme','doc-managed','job-1','active'),
		('gen-old','acme','doc-managed','job-1','retired')`); err != nil {
		t.Fatal(err)
	}

	got, err := NewPostgresStore(pool).ResolveVisibility(ctx, "acme", []GenerationReference{
		{DocumentID: "doc-managed", DocumentVersionID: "job-1", GenerationID: "gen-active"},
		{DocumentID: "doc-managed", DocumentVersionID: "job-1", GenerationID: "gen-old"},
		{DocumentID: "doc-managed"},
		{DocumentID: "doc-unmanaged"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []bool{true, false, false, true}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("visibility = %v, want %v", got, want)
		}
	}
}

func TestPostgresReconciliationSchedulesOnlyOnePendingReplay(t *testing.T) {
	dsn := os.Getenv("INDEX_MANIFEST_TEST_DSN")
	if dsn == "" {
		t.Skip("set INDEX_MANIFEST_TEST_DSN to run PostgreSQL reconciliation integration test")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("indexreconcile_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE TABLE ingestion_jobs (
		job_id TEXT PRIMARY KEY,tenant_id TEXT NOT NULL,doc_id TEXT NOT NULL,
		status TEXT NOT NULL,lease_until TIMESTAMPTZ,completed_at TIMESTAMPTZ,error TEXT NOT NULL DEFAULT '',updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
	); CREATE TABLE ingestion_outbox (
		event_id TEXT PRIMARY KEY,job_id TEXT NOT NULL,tenant_id TEXT NOT NULL,doc_id TEXT NOT NULL,
		published_at TIMESTAMPTZ,claimed_at TIMESTAMPTZ,available_at TIMESTAMPTZ NOT NULL DEFAULT now()
	); CREATE TABLE index_manifests (
		generation_id TEXT PRIMARY KEY,tenant_id TEXT NOT NULL,document_id TEXT NOT NULL,document_version_id TEXT NOT NULL,
		chunker_version TEXT NOT NULL,embedding_model TEXT NOT NULL,vector_dimension INT NOT NULL,schema_version TEXT NOT NULL,
		collection_version TEXT NOT NULL,index_version TEXT NOT NULL,expected_active_generation_id TEXT NOT NULL DEFAULT '',
		expected_chunk_count INT,expected_chunk_digest TEXT,qdrant_count INT,qdrant_digest TEXT,qdrant_observed_at TIMESTAMPTZ,
		elasticsearch_count INT,elasticsearch_digest TEXT,elasticsearch_observed_at TIMESTAMPTZ,state TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),last_reconciled_at TIMESTAMPTZ,reconcile_lease_until TIMESTAMPTZ,
		reconcile_claim_token TEXT NOT NULL DEFAULT '',repair_attempts INT NOT NULL DEFAULT 0,last_reconcile_error TEXT NOT NULL DEFAULT ''
	); INSERT INTO ingestion_jobs VALUES ('job-1','acme','doc-1','completed',NULL,now(),'',now());
	INSERT INTO ingestion_outbox VALUES ('event-1','job-1','acme','doc-1',now(),NULL,now());
	INSERT INTO index_manifests (generation_id,tenant_id,document_id,document_version_id,chunker_version,embedding_model,vector_dimension,schema_version,collection_version,index_version,expected_chunk_count,expected_chunk_digest,state)
	VALUES ('gen-1','acme','doc-1','job-1','chunk-v1','embed-v1',3,'schema-v1','collection-v1','index-v1',2,'expected','active')`); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(pool)
	claimed, err := store.ClaimReconciliation(ctx, ReconciliationClaim{Limit: 1, Lease: time.Minute, Token: "claim-1"})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %+v err=%v", claimed, err)
	}
	disposition, err := store.FinishReconciliation(ctx, ReconciliationResult{
		Manifest: claimed[0], Reason: "mismatch", ReconcileAfter: time.Minute,
		Qdrant: BackendObservation{Count: 1, Digest: "wrong"}, Elasticsearch: BackendObservation{Count: 2, Digest: "expected"},
	}, 3)
	if err != nil || disposition != RepairScheduled {
		t.Fatalf("first finish = (%q,%v)", disposition, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE index_manifests SET reconcile_lease_until=NULL`); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimReconciliation(ctx, ReconciliationClaim{Limit: 1, Lease: time.Minute, Token: "claim-2"})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("second claim = %+v err=%v", claimed, err)
	}
	disposition, err = store.FinishReconciliation(ctx, ReconciliationResult{
		Manifest: claimed[0], Reason: "still mismatched", ReconcileAfter: time.Minute,
	}, 3)
	if err != nil || disposition != RepairPending {
		t.Fatalf("second finish = (%q,%v)", disposition, err)
	}
	var status string
	var pending bool
	var repairs int
	if err := pool.QueryRow(ctx, `SELECT j.status,o.published_at IS NULL,m.repair_attempts FROM ingestion_jobs j JOIN ingestion_outbox o USING(job_id) JOIN index_manifests m ON m.document_version_id=j.job_id`).Scan(&status, &pending, &repairs); err != nil {
		t.Fatal(err)
	}
	if status != "published" || !pending || repairs != 1 {
		t.Fatalf("status=%s pending=%v repairs=%d", status, pending, repairs)
	}
	if _, err := pool.Exec(ctx, `UPDATE index_manifests SET reconcile_lease_until=NULL`); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimReconciliation(ctx, ReconciliationClaim{Limit: 1, Lease: time.Minute, Token: "claim-3"})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("healthy claim = %+v err=%v", claimed, err)
	}
	disposition, err = store.FinishReconciliation(ctx, ReconciliationResult{
		Manifest: claimed[0], Healthy: true, ReconcileAfter: time.Minute,
		Qdrant: BackendObservation{Count: 2, Digest: "expected"}, Elasticsearch: BackendObservation{Count: 2, Digest: "expected"},
	}, 3)
	if err != nil || disposition != RepairNotNeeded {
		t.Fatalf("healthy finish = (%q,%v)", disposition, err)
	}
	var reconcileError string
	if err := pool.QueryRow(ctx, `SELECT repair_attempts,last_reconcile_error FROM index_manifests WHERE generation_id='gen-1'`).Scan(&repairs, &reconcileError); err != nil {
		t.Fatal(err)
	}
	if repairs != 0 || reconcileError != "" {
		t.Fatalf("healthy reset repairs=%d error=%q", repairs, reconcileError)
	}
	if _, err := pool.Exec(ctx, `UPDATE index_manifests SET reconcile_lease_until=NULL`); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimReconciliation(ctx, ReconciliationClaim{Limit: 1, Lease: time.Minute, Token: "claim-4"})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("stale-race claim = %+v err=%v", claimed, err)
	}
	if err := store.ConfirmActive(ctx, "gen-1",
		BackendObservation{Count: 2, Digest: "expected"},
		BackendObservation{Count: 2, Digest: "expected"}); err != nil {
		t.Fatalf("repair confirmation: %v", err)
	}
	_, err = store.FinishReconciliation(ctx, ReconciliationResult{
		Manifest: claimed[0], Reason: "late stale mismatch", ReconcileAfter: time.Minute,
	}, 3)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("late reconciliation error=%v, want fencing conflict", err)
	}
	if err := pool.QueryRow(ctx, `SELECT last_reconcile_error FROM index_manifests WHERE generation_id='gen-1'`).Scan(&reconcileError); err != nil {
		t.Fatal(err)
	}
	if reconcileError != "" {
		t.Fatalf("late reconciliation restored error %q", reconcileError)
	}
}

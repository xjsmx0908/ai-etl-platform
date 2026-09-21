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

	"ai-etl-pipeline/internal/db"
	"ai-etl-pipeline/internal/migrations"
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
		state TEXT NOT NULL, activated_at TIMESTAMPTZ,retired_at TIMESTAMPTZ,
		reconcile_claim_token TEXT NOT NULL DEFAULT '',reconcile_lease_until TIMESTAMPTZ
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

func TestPostgresRollbackAndRetentionLifecycle(t *testing.T) {
	dsn := os.Getenv("INDEX_MANIFEST_TEST_DSN")
	if dsn == "" {
		t.Skip("set INDEX_MANIFEST_TEST_DSN to run PostgreSQL rollback/retention integration test")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("indexretention_%d", time.Now().UnixNano())
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
		generation_id TEXT PRIMARY KEY,tenant_id TEXT NOT NULL,document_id TEXT NOT NULL,document_version_id TEXT NOT NULL,
		chunker_version TEXT NOT NULL,embedding_model TEXT NOT NULL,vector_dimension INT NOT NULL,schema_version TEXT NOT NULL,
		collection_version TEXT NOT NULL,index_version TEXT NOT NULL,expected_active_generation_id TEXT NOT NULL DEFAULT '',
		expected_chunk_count INT,expected_chunk_digest TEXT,qdrant_count INT,qdrant_digest TEXT,qdrant_observed_at TIMESTAMPTZ,
		elasticsearch_count INT,elasticsearch_digest TEXT,elasticsearch_observed_at TIMESTAMPTZ,state TEXT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT now(),activated_at TIMESTAMPTZ,retired_at TIMESTAMPTZ,
		last_reconciled_at TIMESTAMPTZ,last_reconcile_error TEXT NOT NULL DEFAULT '',repair_attempts INT NOT NULL DEFAULT 0,
		reconcile_lease_until TIMESTAMPTZ,reconcile_claim_token TEXT NOT NULL DEFAULT '',
		retention_lease_until TIMESTAMPTZ,retention_claim_token TEXT NOT NULL DEFAULT '',
		qdrant_deleted_at TIMESTAMPTZ,elasticsearch_deleted_at TIMESTAMPTZ,
		retention_attempts INT NOT NULL DEFAULT 0,retention_last_error TEXT NOT NULL DEFAULT ''
	); CREATE UNIQUE INDEX one_active_retention ON index_manifests (tenant_id,document_version_id) WHERE state='active';
	INSERT INTO index_manifests (generation_id,tenant_id,document_id,document_version_id,chunker_version,embedding_model,vector_dimension,schema_version,collection_version,index_version,expected_chunk_count,expected_chunk_digest,state,activated_at,retired_at)
	VALUES ('gen-current','acme','doc-1','job-1','chunk-v1','embed-v1',3,'schema-v1','collection-v1','index-v1',2,'expected','active',now(),NULL),
	('gen-old','acme','doc-1','job-1','chunk-v1','embed-v1',3,'schema-v1','collection-v1','index-v1',2,'expected','retired',now()-interval '2 hours',now()-interval '30 minutes')`); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(pool)
	request := RollbackRequest{
		Version:            VersionIdentity{TenantID: "acme", DocumentID: "doc-1", DocumentVersionID: "job-1"},
		TargetGenerationID: "gen-old", ExpectedActiveGenerationID: "gen-current",
		Window: time.Hour, Lease: time.Minute, ClaimToken: "rollback-claim",
	}
	target, err := store.RollbackTarget(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	matching := BackendObservation{Count: 2, Digest: "expected"}
	if err := store.Rollback(ctx, RollbackCommit{Request: request, Qdrant: matching, Elasticsearch: matching}); err != nil {
		t.Fatal(err)
	}
	var active string
	if err := pool.QueryRow(ctx, `SELECT generation_id FROM index_manifests WHERE state='active'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != target.GenerationID {
		t.Fatalf("active=%q, want %q", active, target.GenerationID)
	}
	if _, err := pool.Exec(ctx, `UPDATE index_manifests SET retired_at=now()-interval '2 hours' WHERE generation_id='gen-current'`); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimRetention(ctx, RetentionClaim{Window: time.Hour, Lease: time.Minute, Limit: 1, Token: "cleanup-1"})
	if err != nil || len(claimed) != 1 || claimed[0].GenerationID != "gen-current" {
		t.Fatalf("claim=%+v err=%v", claimed, err)
	}
	if err := store.FinishRetention(ctx, RetentionResult{
		Manifest: claimed[0], QdrantDeleted: true, Reason: "delete elasticsearch: unavailable", RetryAfter: time.Minute,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.OperationsSnapshot(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Manifests[StateActive] != 1 || snapshot.Manifests[StateRetired] != 1 || snapshot.RetentionFailed != 1 {
		t.Fatalf("operations snapshot=%+v", snapshot)
	}
	if _, err := pool.Exec(ctx, `UPDATE index_manifests SET retention_lease_until=NULL WHERE generation_id='gen-current'`); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimRetention(ctx, RetentionClaim{Window: time.Hour, Lease: time.Minute, Limit: 1, Token: "cleanup-2"})
	if err != nil || len(claimed) != 1 || claimed[0].QdrantDeletedAt.IsZero() {
		t.Fatalf("retry claim=%+v err=%v", claimed, err)
	}
	if err := store.FinishRetention(ctx, RetentionResult{Manifest: claimed[0], QdrantDeleted: true, ElasticsearchDeleted: true}); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM index_manifests WHERE generation_id='gen-current'`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("retained manifest remained: %d", remaining)
	}
}

// indexManifestMigrationPool creates a scratch schema with the real migrations
// applied, so these tests exercise the deployed schema instead of a hand-rolled
// subset of it.
func indexManifestMigrationPool(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	dsn := os.Getenv("INDEX_MANIFEST_TEST_DSN")
	if dsn == "" {
		t.Skip("set INDEX_MANIFEST_TEST_DSN to run PostgreSQL index manifest integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("indexmanifest_migrated_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	if err := migrations.Up(ctx, &db.Pool{Pool: pool}); err != nil {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
		t.Fatalf("apply migrations: %v", err)
	}
	return pool, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	}
}

// seedFailedGeneration writes the four rows a failed generation is made of: the
// tenant and document it belongs to, its ingestion job, the outbox event that
// carried its one delivery, and the failed manifest itself. Each statement is
// separate because pgx refuses to prepare a multi-statement query that takes
// parameters.
func seedFailedGeneration(t *testing.T, pool *pgxpool.Pool, generationID, jobID string) {
	t.Helper()
	ctx := context.Background()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id,name) VALUES ('acme','acme')`, nil},
		{`INSERT INTO documents (tenant_id,doc_id,file_name,object_key,status)
			VALUES ('acme','doc-1','handbook.md','acme/doc-1.md','failed')`, nil},
		{`INSERT INTO ingestion_jobs (job_id,event_id,tenant_id,doc_id,request_signature,task,status,error)
			VALUES ($1,$1||'-event','acme','doc-1','sig','{}'::jsonb,'failed','materialize object: missing')`,
			[]any{jobID}},
		{`INSERT INTO ingestion_outbox (event_id,job_id,tenant_id,doc_id,task,published_at)
			VALUES ($1||'-event',$1,'acme','doc-1','{}'::jsonb,now())`, []any{jobID}},
		{`INSERT INTO index_manifests (generation_id,tenant_id,document_id,document_version_id,
			chunker_version,embedding_model,vector_dimension,schema_version,collection_version,
			index_version,state,last_error)
			VALUES ($2,'acme','doc-1',$1,'chunker-v1','embed-v1',3,'schema-v1','collection-v1',
			'index-v1','failed','materialize object: missing')`, []any{jobID, generationID}},
	} {
		if _, err := pool.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed %s: %v", generationID, err)
		}
	}
}

// The unit tests assert the shape of the SQL. This one asserts the property the
// bound exists for, against the real schema: a generation whose rebuild keeps
// failing is replayed exactly maxRepairs times, is then left alone, and the
// diagnostics gauge says so.
//
// A budget that is never charged passes every shape-based assertion about the
// claim and still re-drives a dead generation on every pass forever.
func TestPostgresFailedRepairIsBoundedByTheRepairBudget(t *testing.T) {
	pool, cleanup := indexManifestMigrationPool(t)
	defer cleanup()
	ctx := context.Background()
	seedFailedGeneration(t, pool, "gen-dead", "job-dead")
	store := NewPostgresStore(pool)
	const maxRepairs = 3

	for attempt := 1; attempt <= maxRepairs; attempt++ {
		claimed, err := store.ClaimFailedRepairs(ctx, ReconciliationClaim{
			Limit: 10, Lease: time.Minute, Token: fmt.Sprintf("claim-%d", attempt),
		}, maxRepairs)
		if err != nil {
			t.Fatalf("attempt %d: claim: %v", attempt, err)
		}
		if len(claimed) != 1 {
			t.Fatalf("attempt %d claimed %d generations, want 1", attempt, len(claimed))
		}
		if err := store.ScheduleFailedRepair(ctx, claimed[0]); err != nil {
			t.Fatalf("attempt %d: schedule: %v", attempt, err)
		}
		var state string
		var budget int
		if err := pool.QueryRow(ctx, `SELECT state,repair_attempts FROM index_manifests
			WHERE generation_id='gen-dead'`).Scan(&state, &budget); err != nil {
			t.Fatal(err)
		}
		// Only a build may move a manifest out of failed. A row parked in
		// building by the reconciler would be rebuilt by nothing.
		if state != string(StateFailed) {
			t.Fatalf("attempt %d left the manifest in %s, want failed", attempt, state)
		}
		if budget != attempt {
			t.Fatalf("attempt %d charged %d units of the budget, want %d", attempt, budget, attempt)
		}
		var pending bool
		if err := pool.QueryRow(ctx, `SELECT published_at IS NULL FROM ingestion_outbox
			WHERE job_id='job-dead'`).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if !pending {
			t.Fatalf("attempt %d did not return the outbox event to the relay", attempt)
		}
		// The worker delivers it, fails again, and the job goes back to failed.
		if _, err := pool.Exec(ctx, `UPDATE ingestion_jobs SET status='failed' WHERE job_id='job-dead'`); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE ingestion_outbox SET published_at=now(),claimed_at=NULL WHERE job_id='job-dead'`); err != nil {
			t.Fatal(err)
		}
	}

	claimed, err := store.ClaimFailedRepairs(ctx, ReconciliationClaim{
		Limit: 10, Lease: time.Minute, Token: "claim-after-budget",
	}, maxRepairs)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 0 {
		t.Fatalf("claimed %d generations after the budget was spent, want 0", len(claimed))
	}
	snapshot, err := store.OperationsSnapshot(ctx, maxRepairs)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Manifests[StateFailed] != 1 || snapshot.RepairExhausted != 1 {
		t.Fatalf("snapshot = %+v, want one exhausted failed generation", snapshot)
	}
}

// A rebuild that outlives a reconciliation pass must not be charged for the
// passes it outlives, or a slow document would spend its attempts before it
// finished. The claim is handed back untouched instead.
func TestPostgresFailedRepairWaitsForAnInFlightReplayWithoutSpendingBudget(t *testing.T) {
	pool, cleanup := indexManifestMigrationPool(t)
	defer cleanup()
	ctx := context.Background()
	seedFailedGeneration(t, pool, "gen-slow", "job-slow")
	store := NewPostgresStore(pool)
	const maxRepairs = 3

	claimed, err := store.ClaimFailedRepairs(ctx, ReconciliationClaim{
		Limit: 10, Lease: time.Minute, Token: "claim-1",
	}, maxRepairs)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %v, %v", claimed, err)
	}
	if err := store.ScheduleFailedRepair(ctx, claimed[0]); err != nil {
		t.Fatal(err)
	}
	// The relay has delivered the event and the worker is still on it.
	if _, err := pool.Exec(ctx, `UPDATE ingestion_jobs SET status='processing' WHERE job_id='job-slow'`); err != nil {
		t.Fatal(err)
	}
	claimed, err = store.ClaimFailedRepairs(ctx, ReconciliationClaim{
		Limit: 10, Lease: time.Minute, Token: "claim-2",
	}, maxRepairs)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("second claim = %v, %v", claimed, err)
	}
	err = store.ScheduleFailedRepair(ctx, claimed[0])
	if !errors.Is(err, ErrRepairInFlight) {
		t.Fatalf("err = %v, want ErrRepairInFlight", err)
	}
	var budget int
	var leaseExpired bool
	if err := pool.QueryRow(ctx, `SELECT repair_attempts, reconcile_lease_until IS NULL
		FROM index_manifests WHERE generation_id='gen-slow'`).Scan(&budget, &leaseExpired); err != nil {
		t.Fatal(err)
	}
	if budget != 1 {
		t.Fatalf("repair_attempts = %d, want 1: an in-flight replay must not spend the budget", budget)
	}
	if !leaseExpired {
		t.Fatal("the claim was not handed back, so the next pass would wait a full lease")
	}
}

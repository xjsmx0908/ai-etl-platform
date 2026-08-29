package deletionworkflow

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAcceptDeletionRevokesPublishedReleaseAndReplaysOneJob(t *testing.T) {
	pool, cleanup := deletionTestPool(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		CREATE TABLE documents (
			tenant_id TEXT NOT NULL,doc_id TEXT NOT NULL,
			publication_status TEXT NOT NULL,deletion_status TEXT NOT NULL DEFAULT 'active',
			object_key TEXT NOT NULL DEFAULT '',updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(tenant_id,doc_id));
		CREATE TABLE document_releases (
			tenant_id TEXT NOT NULL,document_id TEXT NOT NULL,published_version_id TEXT,
			published_generation_id TEXT,revision BIGINT NOT NULL,updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY(tenant_id,document_id));
		CREATE TABLE audit_logs (
			id BIGSERIAL PRIMARY KEY,tenant_id TEXT NOT NULL,actor_user_id TEXT NOT NULL,
			actor_role TEXT NOT NULL,action TEXT NOT NULL,resource_type TEXT NOT NULL,
			resource_id TEXT NOT NULL,result TEXT NOT NULL,detail JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now());
		CREATE TABLE ingestion_jobs (job_id TEXT PRIMARY KEY,tenant_id TEXT NOT NULL,doc_id TEXT NOT NULL,status TEXT NOT NULL,task JSONB NOT NULL DEFAULT '{}',
			error TEXT NOT NULL DEFAULT '',completed_at TIMESTAMPTZ,lease_until TIMESTAMPTZ,updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
		CREATE TABLE document_deletion_jobs (
			job_id TEXT PRIMARY KEY,tenant_id TEXT NOT NULL,document_id TEXT NOT NULL,
			object_prefix TEXT NOT NULL,object_keys TEXT[] NOT NULL,state TEXT NOT NULL DEFAULT 'pending',attempts INT NOT NULL DEFAULT 0,
			available_at TIMESTAMPTZ NOT NULL DEFAULT now(),lease_until TIMESTAMPTZ,claim_token TEXT NOT NULL DEFAULT '',
			qdrant_deleted_at TIMESTAMPTZ,elasticsearch_deleted_at TIMESTAMPTZ,objects_deleted_at TIMESTAMPTZ,
			last_error TEXT NOT NULL DEFAULT '',created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			UNIQUE(tenant_id,document_id));
		INSERT INTO documents VALUES ('acme','policy-1','published','active','acme/policy-1.pdf',now());
		INSERT INTO document_releases VALUES ('acme','policy-1','job-1','gen-1',7,now());
	`); err != nil {
		t.Fatal(err)
	}

	store := NewPostgresStore(pool)
	req := AcceptRequest{TenantID: "acme", DocumentID: "policy-1", ActorUserID: "admin-1", ActorRole: "admin"}
	first, err := store.Accept(ctx, req)
	if err != nil {
		t.Fatalf("accept deletion: %v", err)
	}
	replay, err := store.Accept(ctx, req)
	if err != nil {
		t.Fatalf("replay deletion: %v", err)
	}
	if first.JobID == "" || replay.JobID != first.JobID || first.State != StatePending {
		t.Fatalf("first=%+v replay=%+v", first, replay)
	}
	var deletionStatus, publicationStatus string
	if err := pool.QueryRow(ctx, `SELECT deletion_status,publication_status FROM documents WHERE tenant_id='acme' AND doc_id='policy-1'`).Scan(&deletionStatus, &publicationStatus); err != nil {
		t.Fatal(err)
	}
	if deletionStatus != "pending" || publicationStatus != "retired" {
		t.Fatalf("document status deletion=%q publication=%q", deletionStatus, publicationStatus)
	}
	var publishedVersion, publishedGeneration *string
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT published_version_id,published_generation_id,revision FROM document_releases WHERE tenant_id='acme' AND document_id='policy-1'`).Scan(&publishedVersion, &publishedGeneration, &revision); err != nil {
		t.Fatal(err)
	}
	if publishedVersion != nil || publishedGeneration != nil || revision != 8 {
		t.Fatalf("release version=%v generation=%v revision=%d", publishedVersion, publishedGeneration, revision)
	}
	var jobs, audits int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM document_deletion_jobs`).Scan(&jobs)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action='delete_accepted' AND detail->>'deletion_job_id'=$1`, first.JobID).Scan(&audits)
	if jobs != 1 || audits != 1 {
		t.Fatalf("jobs=%d audits=%d", jobs, audits)
	}
}

func TestDeletionClaimPersistsPartialProgressAndRejectsStaleFinalizer(t *testing.T) {
	pool, cleanup := deletionTestPool(t)
	defer cleanup()
	createDeletionStoreTables(t, pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		INSERT INTO documents VALUES ('acme','policy-1','retired','pending','acme/policy-1.pdf',now());
		INSERT INTO document_releases VALUES ('acme','policy-1',NULL,NULL,8,now());
		INSERT INTO document_deletion_jobs (job_id,tenant_id,document_id,object_prefix,object_keys)
		VALUES ('delete-1','acme','policy-1','acme/policy-1',ARRAY['acme/policy-1.pdf']);
	`); err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(pool)
	jobs, err := store.Claim(ctx, ClaimRequest{Token: "claim-1", Lease: time.Minute, Limit: 1})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim jobs=%+v err=%v", jobs, err)
	}
	partial := FinishResult{Job: jobs[0], QdrantDeleted: true, ObjectsDeleted: true, Reason: "elasticsearch: unavailable", RetryAfter: time.Millisecond}
	if err := store.Finish(ctx, partial); err != nil {
		t.Fatalf("finish partial: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	retry, err := store.Claim(ctx, ClaimRequest{Token: "claim-2", Lease: time.Minute, Limit: 1})
	if err != nil || len(retry) != 1 || retry[0].QdrantDeletedAt.IsZero() || retry[0].ObjectsDeletedAt.IsZero() || !retry[0].ElasticsearchDeletedAt.IsZero() {
		t.Fatalf("retry jobs=%+v err=%v", retry, err)
	}
	if err := store.Finish(ctx, FinishResult{Job: jobs[0], QdrantDeleted: true, ElasticsearchDeleted: true, ObjectsDeleted: true}); err != ErrConflict {
		t.Fatalf("stale finish err=%v, want conflict", err)
	}
	if err := store.Finish(ctx, FinishResult{Job: retry[0], QdrantDeleted: true, ElasticsearchDeleted: true, ObjectsDeleted: true}); err != nil {
		t.Fatalf("finish current claim: %v", err)
	}
	var docs, jobsLeft, completedAudits int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM documents`).Scan(&docs)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM document_deletion_jobs`).Scan(&jobsLeft)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action='delete_completed' AND detail->>'deletion_job_id'='delete-1'`).Scan(&completedAudits)
	if docs != 0 || jobsLeft != 0 || completedAudits != 1 {
		t.Fatalf("docs=%d jobs=%d completion audits=%d", docs, jobsLeft, completedAudits)
	}
}

func TestDeletionClaimWaitsForActiveIngestionLease(t *testing.T) {
	pool, cleanup := deletionTestPool(t)
	defer cleanup()
	createDeletionStoreTables(t, pool)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		INSERT INTO documents VALUES ('acme','policy-1','retired','pending','acme/policy-1.pdf',now());
		INSERT INTO document_deletion_jobs (job_id,tenant_id,document_id,object_prefix,object_keys) VALUES ('delete-1','acme','policy-1','acme/policy-1',ARRAY['acme/policy-1.pdf']);
		INSERT INTO ingestion_jobs (job_id,tenant_id,doc_id,status,lease_until)
		VALUES ('job-1','acme','policy-1','processing',now()+interval '1 hour');`)
	if err != nil {
		t.Fatal(err)
	}
	store := NewPostgresStore(pool)
	jobs, err := store.Claim(ctx, ClaimRequest{Token: "claim-1", Lease: time.Minute, Limit: 1})
	if err != nil || len(jobs) != 0 {
		t.Fatalf("active lease claim jobs=%v err=%v", jobs, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ingestion_jobs SET lease_until=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	jobs, err = store.Claim(ctx, ClaimRequest{Token: "claim-2", Lease: time.Minute, Limit: 1})
	if err != nil || len(jobs) != 1 {
		t.Fatalf("expired lease claim jobs=%v err=%v", jobs, err)
	}
}

func createDeletionStoreTables(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		CREATE TABLE documents (
			tenant_id TEXT NOT NULL,doc_id TEXT NOT NULL,
			publication_status TEXT NOT NULL,deletion_status TEXT NOT NULL DEFAULT 'active',
			object_key TEXT NOT NULL DEFAULT '',updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(tenant_id,doc_id));
		CREATE TABLE document_releases (
			tenant_id TEXT NOT NULL,document_id TEXT NOT NULL,published_version_id TEXT,
			published_generation_id TEXT,revision BIGINT NOT NULL,updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY(tenant_id,document_id), FOREIGN KEY(tenant_id,document_id) REFERENCES documents(tenant_id,doc_id) ON DELETE CASCADE);
		CREATE TABLE ingestion_jobs (
			job_id TEXT PRIMARY KEY,tenant_id TEXT NOT NULL,doc_id TEXT NOT NULL,status TEXT NOT NULL,
			task JSONB NOT NULL DEFAULT '{}',error TEXT NOT NULL DEFAULT '',completed_at TIMESTAMPTZ,lease_until TIMESTAMPTZ,updated_at TIMESTAMPTZ NOT NULL DEFAULT now());
		CREATE TABLE audit_logs (
			id BIGSERIAL PRIMARY KEY,tenant_id TEXT NOT NULL,actor_user_id TEXT NOT NULL,
			actor_role TEXT NOT NULL,action TEXT NOT NULL,resource_type TEXT NOT NULL,
			resource_id TEXT NOT NULL,result TEXT NOT NULL,detail JSONB NOT NULL,created_at TIMESTAMPTZ NOT NULL DEFAULT now());
		CREATE TABLE document_deletion_jobs (
			job_id TEXT PRIMARY KEY,tenant_id TEXT NOT NULL,document_id TEXT NOT NULL,
			object_prefix TEXT NOT NULL,object_keys TEXT[] NOT NULL,state TEXT NOT NULL DEFAULT 'pending',attempts INT NOT NULL DEFAULT 0,
			available_at TIMESTAMPTZ NOT NULL DEFAULT now(),lease_until TIMESTAMPTZ,claim_token TEXT NOT NULL DEFAULT '',
			qdrant_deleted_at TIMESTAMPTZ,elasticsearch_deleted_at TIMESTAMPTZ,objects_deleted_at TIMESTAMPTZ,
			last_error TEXT NOT NULL DEFAULT '',created_at TIMESTAMPTZ NOT NULL DEFAULT now(),updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			UNIQUE(tenant_id,document_id), FOREIGN KEY(tenant_id,document_id) REFERENCES documents(tenant_id,doc_id) ON DELETE CASCADE);
	`); err != nil {
		t.Fatal(err)
	}
}

func deletionTestPool(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	dsn := os.Getenv("GOVERNANCE_RELEASE_TEST_DSN")
	if dsn == "" {
		t.Skip("set GOVERNANCE_RELEASE_TEST_DSN to run deletion integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("deletion_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return pool, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	}
}

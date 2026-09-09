package publicationworkflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type recordingInvalidator struct{ calls int }

func (r *recordingInvalidator) InvalidateSemanticCache(context.Context) error {
	r.calls++
	return nil
}

func TestPostgresPublicationCommitsExactCandidateAndAuditOnce(t *testing.T) {
	pool, cleanup := publicationWorkflowPool(t)
	defer cleanup()
	seedExactPublication(t, pool)
	cache := &recordingInvalidator{}
	publication := NewPostgresPublication(pool, cache)

	candidate, found, err := publication.CurrentCandidate(context.Background(), "acme", "policy-1")
	if err != nil || !found {
		t.Fatalf("current candidate found=%t err=%v", found, err)
	}
	want := Candidate{DocumentID: "policy-1", DocumentVersionID: "job-2", GenerationID: "gen-2", ExpectedChunkCount: 4, ExpectedChunkDigest: "sha256:approved", ReleaseRevision: 7}
	if candidate != want {
		t.Fatalf("candidate=%+v, want %+v", candidate, want)
	}
	actor := Actor{TenantID: "acme", UserID: "admin-1", Role: "admin"}
	if err := publication.Publish(context.Background(), actor, candidate, "agent:run-1:2:publish_document"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if _, found, err := publication.CurrentCandidate(context.Background(), "acme", "policy-1"); err != nil || found {
		t.Fatalf("published current release remained approvable found=%t err=%v", found, err)
	}
	if err := publication.Publish(context.Background(), actor, candidate, "agent:run-1:2:publish_document"); err != nil {
		t.Fatalf("replay publish: %v", err)
	}
	tampered := candidate
	tampered.ExpectedChunkDigest = "sha256:different"
	if err := publication.Publish(context.Background(), actor, tampered, "agent:run-1:2:publish_document"); !errors.Is(err, ErrCandidateStale) {
		t.Fatalf("changed candidate replay error=%v, want ErrCandidateStale", err)
	}
	var status, versionID, generationID, key string
	var revision int64
	if err := pool.QueryRow(context.Background(), `SELECT d.publication_status,r.published_version_id,
		r.published_generation_id,r.revision,r.last_publication_idempotency_key
		FROM documents d JOIN document_releases r ON r.tenant_id=d.tenant_id AND r.document_id=d.doc_id
		WHERE d.tenant_id='acme' AND d.doc_id='policy-1'`).Scan(&status, &versionID, &generationID, &revision, &key); err != nil {
		t.Fatal(err)
	}
	var audits int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_logs WHERE tenant_id='acme' AND resource_id='policy-1'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if status != "published" || versionID != "job-2" || generationID != "gen-2" || revision != 8 || key == "" || audits != 1 || cache.calls != 1 {
		t.Fatalf("status=%q version=%q generation=%q revision=%d key=%q audits=%d cache=%d", status, versionID, generationID, revision, key, audits, cache.calls)
	}
	var oldState, newState string
	if err := pool.QueryRow(context.Background(), `SELECT
		(SELECT state FROM index_manifests WHERE generation_id='gen-1'),
		(SELECT state FROM index_manifests WHERE generation_id='gen-2')`).Scan(&oldState, &newState); err != nil {
		t.Fatal(err)
	}
	if oldState != "retired" || newState != "active" {
		t.Fatalf("generation states old=%q new=%q, want retired/active", oldState, newState)
	}
}

func TestPostgresPublicationRejectsStaleOrUnhealthyCandidateWithoutWrites(t *testing.T) {
	pool, cleanup := publicationWorkflowPool(t)
	defer cleanup()
	seedExactPublication(t, pool)
	publication := NewPostgresPublication(pool, nil)
	candidate, found, err := publication.CurrentCandidate(context.Background(), "acme", "policy-1")
	if err != nil || !found {
		t.Fatalf("current candidate found=%t err=%v", found, err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE index_manifests SET last_reconcile_error='digest mismatch' WHERE generation_id='gen-2'`); err != nil {
		t.Fatal(err)
	}
	if _, found, err := publication.CurrentCandidate(context.Background(), "acme", "policy-1"); err != nil || found {
		t.Fatalf("unhealthy candidate found=%t err=%v", found, err)
	}
	err = publication.Publish(context.Background(), Actor{TenantID: "acme", UserID: "admin-1", Role: "admin"}, candidate, "agent:run-2:2:publish_document")
	if !errors.Is(err, ErrCandidateStale) {
		t.Fatalf("publish error=%v, want ErrCandidateStale", err)
	}
	assertPublicationUnchanged(t, pool)
}

func TestPostgresPublicationRollsBackReleaseWhenAuditFails(t *testing.T) {
	pool, cleanup := publicationWorkflowPool(t)
	defer cleanup()
	seedExactPublication(t, pool)
	publication := NewPostgresPublication(pool, nil)
	candidate, _, err := publication.CurrentCandidate(context.Background(), "acme", "policy-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `DROP TABLE audit_logs`); err != nil {
		t.Fatal(err)
	}
	err = publication.Publish(context.Background(), Actor{TenantID: "acme", UserID: "admin-1", Role: "admin"}, candidate, "agent:run-3:2:publish_document")
	if err == nil {
		t.Fatal("publish succeeded without durable audit")
	}
	assertPublicationUnchanged(t, pool)
}

func TestPostgresPublicationRejectsCandidateAfterDeletionAcceptance(t *testing.T) {
	pool, cleanup := publicationWorkflowPool(t)
	defer cleanup()
	seedExactPublication(t, pool)
	publication := NewPostgresPublication(pool, nil)
	candidate, _, err := publication.CurrentCandidate(context.Background(), "acme", "policy-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE documents SET deletion_status='pending',publication_status='retired' WHERE tenant_id='acme' AND doc_id='policy-1'`); err != nil {
		t.Fatal(err)
	}
	err = publication.Publish(context.Background(), Actor{TenantID: "acme", UserID: "admin-1", Role: "admin"}, candidate, "agent:delete-race")
	if !errors.Is(err, ErrCandidateStale) {
		t.Fatalf("publish error=%v, want stale", err)
	}
}

func assertPublicationUnchanged(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var status string
	var revision int64
	var publishedVersion *string
	if err := pool.QueryRow(context.Background(), `SELECT d.publication_status,r.revision,r.published_version_id
		FROM documents d JOIN document_releases r ON r.tenant_id=d.tenant_id AND r.document_id=d.doc_id
		WHERE d.tenant_id='acme' AND d.doc_id='policy-1'`).Scan(&status, &revision, &publishedVersion); err != nil {
		t.Fatal(err)
	}
	if status != "draft" || revision != 7 || publishedVersion == nil || *publishedVersion != "job-1" {
		t.Fatalf("partial publication status=%q revision=%d published=%v", status, revision, publishedVersion)
	}
}

func seedExactPublication(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO documents VALUES ('acme','policy-1','completed','active','policies','draft','legal',now());
		INSERT INTO document_releases (tenant_id,document_id,current_version_id,published_version_id,published_generation_id,revision,resolution_status)
		VALUES ('acme','policy-1','job-2','job-1','gen-1',7,'resolved');
		INSERT INTO index_manifests VALUES
		('gen-1','acme','policy-1','job-1',3,'sha256:old',3,'sha256:old',3,'sha256:old','active',''),
		('gen-2','acme','policy-1','job-2',4,'sha256:approved',4,'sha256:approved',4,'sha256:approved','active','');
	`)
	if err != nil {
		t.Fatal(err)
	}
}

func publicationWorkflowPool(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	dsn := os.Getenv("GOVERNANCE_RELEASE_TEST_DSN")
	if dsn == "" {
		t.Skip("set GOVERNANCE_RELEASE_TEST_DSN to run PostgreSQL publication tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("publicationworkflow_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
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
	_, err = pool.Exec(ctx, `
		CREATE TABLE documents (
			tenant_id TEXT NOT NULL,doc_id TEXT NOT NULL,status TEXT NOT NULL,doc_status TEXT NOT NULL,
			knowledge_space_id TEXT NOT NULL,publication_status TEXT NOT NULL,owner TEXT NOT NULL,
			effective_date TIMESTAMPTZ,deletion_status TEXT NOT NULL DEFAULT 'active',updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(tenant_id,doc_id));
		CREATE TABLE document_releases (
			tenant_id TEXT NOT NULL,document_id TEXT NOT NULL,current_version_id TEXT,published_version_id TEXT,
			published_generation_id TEXT,revision BIGINT NOT NULL,resolution_status TEXT NOT NULL,last_error TEXT NOT NULL DEFAULT '',
			last_publication_idempotency_key TEXT NOT NULL DEFAULT '',last_publication_request_hash TEXT NOT NULL DEFAULT '',
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),PRIMARY KEY(tenant_id,document_id));
		CREATE UNIQUE INDEX document_releases_publication_idempotency_key ON document_releases(tenant_id,last_publication_idempotency_key) WHERE last_publication_idempotency_key<>'';
		CREATE TABLE index_manifests (
			generation_id TEXT PRIMARY KEY,tenant_id TEXT NOT NULL,document_id TEXT NOT NULL,document_version_id TEXT NOT NULL,
			expected_chunk_count INT,expected_chunk_digest TEXT,qdrant_count INT,qdrant_digest TEXT,
			elasticsearch_count INT,elasticsearch_digest TEXT,state TEXT NOT NULL,last_reconcile_error TEXT NOT NULL DEFAULT '',
			retired_at TIMESTAMPTZ);
		CREATE TABLE audit_logs (
			id BIGSERIAL PRIMARY KEY,tenant_id TEXT NOT NULL DEFAULT '',actor_user_id TEXT NOT NULL DEFAULT '',actor_role TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL,resource_type TEXT NOT NULL DEFAULT '',resource_id TEXT NOT NULL DEFAULT '',result TEXT NOT NULL DEFAULT 'success',
			detail JSONB NOT NULL DEFAULT '{}',created_at TIMESTAMPTZ NOT NULL DEFAULT now());
	`)
	if err != nil {
		t.Fatal(err)
	}
	return pool, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	}
}

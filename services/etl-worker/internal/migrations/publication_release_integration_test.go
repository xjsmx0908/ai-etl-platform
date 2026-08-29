package migrations

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"ai-etl-pipeline/internal/db"
)

func TestAllMigrationsApplyOnFreshSchema(t *testing.T) {
	pool, cleanup := publicationReleaseMigrationPool(t)
	defer cleanup()

	if err := Up(context.Background(), &db.Pool{Pool: pool}); err != nil {
		t.Fatalf("apply all migrations: %v", err)
	}
	var applied bool
	if err := pool.QueryRow(context.Background(), `SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name='document_releases'
			AND column_name='last_publication_request_hash')`).Scan(&applied); err != nil {
		t.Fatal(err)
	}
	if !applied {
		t.Fatal("exact-candidate publication migration was not applied")
	}
}

func TestDocumentReleaseMigrationBackfillsUniquePublishedGeneration(t *testing.T) {
	pool, cleanup := publicationReleaseMigrationPool(t)
	defer cleanup()
	ctx := context.Background()
	seedPublicationReleasePrerequisites(t, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO documents VALUES ('acme','policy-1','acme/policy-1/v1','published');
		INSERT INTO ingestion_jobs VALUES ('job-1','acme','policy-1','{"file_path":"acme/policy-1/v1"}');
		INSERT INTO index_manifests VALUES ('gen-1','acme','policy-1','job-1','active');
	`); err != nil {
		t.Fatal(err)
	}

	body, err := migrationFiles.ReadFile("0016_document_releases.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	var currentVersion, publishedVersion, publishedGeneration, resolution string
	var revision int64
	if err := pool.QueryRow(ctx, `SELECT current_version_id,published_version_id,
		published_generation_id,revision,resolution_status FROM document_releases
		WHERE tenant_id='acme' AND document_id='policy-1'`).Scan(
		&currentVersion, &publishedVersion, &publishedGeneration, &revision, &resolution,
	); err != nil {
		t.Fatal(err)
	}
	if currentVersion != "job-1" || publishedVersion != "job-1" || publishedGeneration != "gen-1" ||
		revision != 1 || resolution != "resolved" {
		t.Fatalf("unexpected release backfill: current=%q published=%q generation=%q revision=%d resolution=%q",
			currentVersion, publishedVersion, publishedGeneration, revision, resolution)
	}
}

func TestDocumentReleaseMigrationRejectsAmbiguousPublishedGeneration(t *testing.T) {
	pool, cleanup := publicationReleaseMigrationPool(t)
	defer cleanup()
	ctx := context.Background()
	seedPublicationReleasePrerequisites(t, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO documents VALUES ('acme','policy-1','acme/policy-1/v2','published');
		INSERT INTO ingestion_jobs VALUES
			('job-1','acme','policy-1','{"file_path":"acme/policy-1/v1"}'),
			('job-2','acme','policy-1','{"file_path":"acme/policy-1/v2"}');
		INSERT INTO index_manifests VALUES
			('gen-1','acme','policy-1','job-1','active'),
			('gen-2','acme','policy-1','job-2','active');
	`); err != nil {
		t.Fatal(err)
	}
	body, err := migrationFiles.ReadFile("0016_document_releases.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(body)); err == nil || !strings.Contains(err.Error(), "ambiguous published generation") {
		t.Fatalf("migration error=%v, want ambiguous published generation", err)
	}
}

func TestDocumentReleaseMigrationMarksUnprovenLegacyVersionUnresolved(t *testing.T) {
	pool, cleanup := publicationReleaseMigrationPool(t)
	defer cleanup()
	ctx := context.Background()
	seedPublicationReleasePrerequisites(t, pool)
	if _, err := pool.Exec(ctx, `INSERT INTO documents VALUES
		('acme','legacy-draft','legacy/draft','draft'),
		('acme','legacy-published','legacy/published','published')`); err != nil {
		t.Fatal(err)
	}
	body, err := migrationFiles.ReadFile("0016_document_releases.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	var currentVersion *string
	var resolution, lastError string
	if err := pool.QueryRow(ctx, `SELECT current_version_id,resolution_status,last_error
		FROM document_releases WHERE tenant_id='acme' AND document_id='legacy-draft'`).Scan(
		&currentVersion, &resolution, &lastError,
	); err != nil {
		t.Fatal(err)
	}
	if currentVersion != nil || resolution != "unresolved" || lastError != "legacy version identity unavailable" {
		t.Fatalf("current=%v resolution=%q error=%q", currentVersion, resolution, lastError)
	}
	var publishedVersion, publishedGeneration *string
	if err := pool.QueryRow(ctx, `SELECT current_version_id,published_version_id,
		published_generation_id,resolution_status,last_error FROM document_releases
		WHERE tenant_id='acme' AND document_id='legacy-published'`).Scan(
		&currentVersion, &publishedVersion, &publishedGeneration, &resolution, &lastError,
	); err != nil {
		t.Fatal(err)
	}
	if currentVersion != nil || publishedVersion != nil || publishedGeneration != nil ||
		resolution != "unresolved" || lastError != "legacy published identity unavailable" {
		t.Fatalf("current=%v published=%v generation=%v resolution=%q error=%q",
			currentVersion, publishedVersion, publishedGeneration, resolution, lastError)
	}
}

func TestDocumentReleaseMigrationKeepsPartialPublishedEvidenceUnresolved(t *testing.T) {
	pool, cleanup := publicationReleaseMigrationPool(t)
	defer cleanup()
	ctx := context.Background()
	seedPublicationReleasePrerequisites(t, pool)
	if _, err := pool.Exec(ctx, `
		INSERT INTO documents VALUES
			('acme','job-only','acme/job-only/v1','published'),
			('acme','manifest-only','acme/manifest-only/v1','published');
		INSERT INTO ingestion_jobs VALUES
			('job-1','acme','job-only','{"file_path":"acme/job-only/v1"}'),
			('job-2','acme','manifest-only','{"file_path":"different/path"}');
		INSERT INTO index_manifests VALUES
			('gen-2','acme','manifest-only','job-2','active');
	`); err != nil {
		t.Fatal(err)
	}
	body, err := migrationFiles.ReadFile("0016_document_releases.up.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := pool.Exec(ctx, string(body)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	for _, documentID := range []string{"job-only", "manifest-only"} {
		var publishedVersion, publishedGeneration *string
		var resolution string
		if err := pool.QueryRow(ctx, `SELECT published_version_id,published_generation_id,resolution_status
			FROM document_releases WHERE tenant_id='acme' AND document_id=$1`, documentID).Scan(
			&publishedVersion, &publishedGeneration, &resolution,
		); err != nil {
			t.Fatal(err)
		}
		if publishedVersion != nil || publishedGeneration != nil || resolution != "unresolved" {
			t.Fatalf("document=%s published=%v generation=%v resolution=%q",
				documentID, publishedVersion, publishedGeneration, resolution)
		}
	}
}

func publicationReleaseMigrationPool(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	dsn := os.Getenv("GOVERNANCE_RELEASE_TEST_DSN")
	if dsn == "" {
		t.Skip("set GOVERNANCE_RELEASE_TEST_DSN to run PostgreSQL release migration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("release_migration_%d", time.Now().UnixNano())
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
	return pool, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	}
}

func seedPublicationReleasePrerequisites(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		CREATE TABLE documents (
			tenant_id TEXT NOT NULL, doc_id TEXT NOT NULL, object_key TEXT NOT NULL,
			publication_status TEXT NOT NULL, PRIMARY KEY (tenant_id,doc_id));
		CREATE TABLE ingestion_jobs (
			job_id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, doc_id TEXT NOT NULL, task JSONB NOT NULL);
		CREATE UNIQUE INDEX ingestion_jobs_version_identity_key
			ON ingestion_jobs (tenant_id,doc_id,job_id);
		CREATE TABLE index_manifests (
			generation_id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, document_id TEXT NOT NULL,
			document_version_id TEXT NOT NULL, state TEXT NOT NULL,
			UNIQUE (tenant_id,document_version_id,generation_id));
	`)
	if err != nil {
		t.Fatal(err)
	}
}

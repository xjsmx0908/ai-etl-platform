package publicationrelease

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresReplacementPreservesPublishedReleaseAndRejectsStaleCutover(t *testing.T) {
	dsn := os.Getenv("GOVERNANCE_RELEASE_TEST_DSN")
	if dsn == "" {
		t.Skip("set GOVERNANCE_RELEASE_TEST_DSN to run PostgreSQL release integration tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("publicationrelease_%d", time.Now().UnixNano())
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
	if _, err := pool.Exec(ctx, `CREATE TABLE document_releases (
		tenant_id TEXT NOT NULL,
		document_id TEXT NOT NULL,
		current_version_id TEXT NOT NULL,
		published_version_id TEXT,
		published_generation_id TEXT,
		revision BIGINT NOT NULL CHECK (revision > 0),
		resolution_status TEXT NOT NULL DEFAULT 'resolved',
		last_error TEXT NOT NULL DEFAULT '',
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (tenant_id, document_id)
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO document_releases
		(tenant_id,document_id,current_version_id,published_version_id,published_generation_id,revision)
		VALUES ('acme','policy-1','job-1','job-1','gen-1',1)`); err != nil {
		t.Fatal(err)
	}

	store := NewPostgresStore(pool)
	replacement, err := store.RecordCurrent(ctx, VersionIdentity{
		TenantID: "acme", DocumentID: "policy-1", VersionID: "job-2",
	})
	if err != nil {
		t.Fatalf("record replacement: %v", err)
	}
	if replacement.CurrentVersionID != "job-2" || replacement.PublishedVersionID != "job-1" ||
		replacement.PublishedGenerationID != "gen-1" || replacement.Revision != 2 {
		t.Fatalf("replacement changed approved release: %+v", replacement)
	}
	replayed, err := store.RecordCurrent(ctx, VersionIdentity{
		TenantID: "acme", DocumentID: "policy-1", VersionID: "job-2",
	})
	if err != nil {
		t.Fatalf("replay replacement: %v", err)
	}
	if replayed.Revision != replacement.Revision {
		t.Fatalf("idempotent replay revision=%d, want %d", replayed.Revision, replacement.Revision)
	}
	if _, found, err := store.Get(ctx, "other-tenant", "policy-1"); err != nil || found {
		t.Fatalf("cross-tenant get found=%t err=%v, want not found", found, err)
	}
	loaded, found, err := store.Get(ctx, "acme", "policy-1")
	if err != nil || !found || loaded.CurrentVersionID != "job-2" {
		t.Fatalf("tenant release found=%t err=%v release=%+v", found, err, loaded)
	}

	stale := Candidate{
		VersionIdentity: VersionIdentity{TenantID: "acme", DocumentID: "policy-1", VersionID: "job-2"},
		GenerationID:    "gen-2", ExpectedRevision: 1,
	}
	if _, err := store.Publish(ctx, stale); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale publish error=%v, want ErrConflict", err)
	}

	stale.ExpectedRevision = replacement.Revision
	published, err := store.Publish(ctx, stale)
	if err != nil {
		t.Fatalf("publish replacement: %v", err)
	}
	if published.CurrentVersionID != "job-2" || published.PublishedVersionID != "job-2" ||
		published.PublishedGenerationID != "gen-2" || published.Revision != 3 {
		t.Fatalf("unexpected published release: %+v", published)
	}
}

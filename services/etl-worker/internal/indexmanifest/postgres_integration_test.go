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

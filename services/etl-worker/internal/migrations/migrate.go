// Package migrations applies embedded SQL schema migrations in lexical order.
// It is up-only: each NNNN_*.up.sql file runs once inside its own transaction,
// recorded in schema_migrations. A Postgres advisory lock prevents concurrent
// query-api/worker replicas from racing each other.
package migrations

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"ai-etl-pipeline/internal/db"
)

//go:embed *.up.sql
var migrationFiles embed.FS

// advisoryLockKey is an arbitrary fixed key guarding migration execution.
const advisoryLockKey = 834710392

// execer is satisfied by *pgxpool.Conn (production) and pgxmock (tests).
type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Open connects to PostgreSQL and applies pending migrations. Callers that
// require the registry (query-api) treat an error as fatal; callers that
// tolerate a missing registry (worker) may warn instead.
func Open(ctx context.Context, dsn string) (*db.Pool, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("PG_DSN is not configured")
	}
	pool, err := db.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := Up(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}

// Up applies all pending migrations. Callers that require the schema (query-api)
// should treat an error as fatal; callers that tolerate a missing registry
// (worker) may warn instead.
func Up(ctx context.Context, pool *db.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()
	return applyAll(ctx, conn)
}

// applyAll runs every unapplied .up.sql file on a single connection so the
// advisory lock and each transaction share the same session.
func applyAll(ctx context.Context, conn execer) error {
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		// Best-effort unlock on a background context so a failed migration still
		// releases the lock.
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", advisoryLockKey)
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	files := sortedUpFiles()
	for _, name := range files {
		applied, err := isApplied(ctx, conn, name)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if applied {
			continue
		}
		body, err := migrationFiles.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := applyOne(ctx, conn, name, string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return nil
}

func sortedUpFiles() []string {
	entries, err := migrationFiles.ReadDir(".")
	if err != nil {
		return nil
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files
}

func isApplied(ctx context.Context, conn execer, version string) (bool, error) {
	var exists bool
	err := conn.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)", version,
	).Scan(&exists)
	return exists, err
}

func applyOne(ctx context.Context, conn execer, version, body string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	// Rollback is a no-op after Commit; it only matters on the error path.
	defer func() { _ = tx.Rollback(context.Background()) }()

	if _, err := tx.Exec(ctx, body); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Ensure the production pool type satisfies execer at compile time.
var _ execer = (*pgxpool.Conn)(nil)

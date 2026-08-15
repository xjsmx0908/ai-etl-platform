// Package db provides a thin wrapper around a pgx connection pool so callers
// depend on this package rather than pgx directly.
package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is the read/write surface store packages depend on. It is satisfied by
// *Pool and by pgxmock in tests, letting store SQL be tested without a live
// Postgres.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Pool wraps a pgxpool.Pool with a minimal surface.
type Pool struct {
	*pgxpool.Pool
}

// Compile-time check that Pool satisfies Querier.
var _ Querier = (*Pool)(nil)

// New opens a connection pool from a DSN. Connections are established lazily;
// use Ping to verify connectivity.
func New(ctx context.Context, dsn string) (*Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Pool{Pool: pool}, nil
}

// Ping verifies the connection is alive.
func (p *Pool) Ping(ctx context.Context) error {
	return p.Pool.Ping(ctx)
}

// Close closes the underlying pool.
func (p *Pool) Close() {
	p.Pool.Close()
}

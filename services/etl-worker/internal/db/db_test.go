package db

import (
	"context"
	"testing"
)

func TestNew_InvalidDSN(t *testing.T) {
	ctx := context.Background()
	// A malformed DSN must be rejected at parse time, before any connection.
	if _, err := New(ctx, "://not-a-valid-dsn"); err == nil {
		t.Fatal("expected invalid DSN to be rejected")
	}
}

func TestNew_ValidDSN(t *testing.T) {
	ctx := context.Background()
	pool, err := New(ctx, "postgres://user:pass@localhost:5432/test?sslmode=disable")
	if err != nil {
		t.Fatalf("expected valid DSN to construct a pool, got %v", err)
	}
	pool.Close()
}

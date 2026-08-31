package session_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/db"
	"ai-etl-pipeline/internal/migrations"
	"ai-etl-pipeline/internal/session"
)

func TestPostgresSessionLifecycleThroughManager(t *testing.T) {
	pool, cleanup := sessionPool(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES('demo-tenant','Demo')`); err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,password_hash,role,tenant_id,active)
		VALUES('alice','unused','readonly','demo-tenant',true) RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	manager, err := session.New(
		session.NewPostgresStore(&db.Pool{Pool: pool}), demoPolicy(),
		session.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{
		TenantID: "demo-tenant", SubjectID: userID, Role: "readonly",
		AuthenticationMethod: auth.AuthenticationMethodFederated,
	}
	first, err := manager.Establish(ctx, session.EstablishCommand{
		Principal: principal, Evidence: session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: now.Add(-time.Minute)},
		CorrelationID: "login-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var digestLength int
	var plaintextMatches int
	if err := pool.QueryRow(ctx, `SELECT octet_length(credential_digest),
		count(*) FILTER (WHERE encode(credential_digest,'escape')=$1) OVER ()
		FROM platform_sessions`, first.Token).Scan(&digestLength, &plaintextMatches); err != nil {
		t.Fatal(err)
	}
	if digestLength != 32 || plaintextMatches != 0 {
		t.Fatalf("digest_length=%d plaintext_matches=%d", digestLength, plaintextMatches)
	}

	now = now.Add(time.Minute)
	rotated, err := manager.Establish(ctx, session.EstablishCommand{
		Principal: principal, Evidence: session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: now},
		ReplacesCredential: first.Token, CorrelationID: "reauth-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !rotated.ExpiresAt.Equal(first.ExpiresAt) {
		t.Fatalf("rotation extended absolute expiry: first=%s rotated=%s", first.ExpiresAt, rotated.ExpiresAt)
	}
	assertDecision(t, manager, first.Token, session.DecisionDeny)
	assertDecision(t, manager, rotated.Token, session.DecisionAllow)
	second, err := manager.Establish(ctx, session.EstablishCommand{
		Principal: principal, Evidence: session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: now},
		CorrelationID: "login-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Revoke(ctx, session.RevokeCommand{
		Credential: rotated.Token, Scope: session.RevokeSubject, CorrelationID: "revoke-all-1",
	}); err != nil {
		t.Fatal(err)
	}
	assertDecision(t, manager, rotated.Token, session.DecisionDeny)
	assertDecision(t, manager, second.Token, session.DecisionDeny)
	var correlationID string
	if err := pool.QueryRow(ctx, `SELECT revoked_correlation_id FROM platform_sessions
		WHERE internal_user_id=$1 ORDER BY created_at LIMIT 1`, userID).Scan(&correlationID); err != nil {
		t.Fatal(err)
	}
	if correlationID != "revoke-all-1" {
		t.Fatalf("revoked correlation=%q", correlationID)
	}
}

func TestPostgresSubjectRevocationFenceRejectsConcurrentStaleEstablish(t *testing.T) {
	pool, cleanup := sessionPool(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES('demo-tenant','Demo')`); err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,password_hash,role,tenant_id,active)
		VALUES('bob','unused','readonly','demo-tenant',true) RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 31, 10, 30, 0, 0, time.UTC)
	manager, err := session.New(
		session.NewPostgresStore(&db.Pool{Pool: pool}), demoPolicy(),
		session.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT true FROM users WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, "demo-tenant", userID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, establishErr := manager.Establish(ctx, session.EstablishCommand{
			Principal: auth.Principal{
				TenantID: "demo-tenant", SubjectID: userID,
				AuthenticationMethod: auth.AuthenticationMethodFederated,
			},
			Evidence:      session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: now},
			CorrelationID: "concurrent-login",
		})
		result <- establishErr
	}()
	select {
	case establishErr := <-result:
		t.Fatalf("establish bypassed subject lock: %v", establishErr)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := tx.Exec(ctx, `INSERT INTO platform_session_subject_states (
		tenant_id,internal_user_id,revoked_before,revoked_correlation_id
	) VALUES ($1,$2,$3,$4)`, "demo-tenant", userID, now, "revoke-all-concurrent"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case establishErr := <-result:
		if !errors.Is(establishErr, session.ErrInvalid) {
			t.Fatalf("stale establish error=%v", establishErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stale establish did not finish after revocation fence committed")
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform_sessions WHERE internal_user_id=$1`, userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale establish created %d session rows", count)
	}
}

func sessionPool(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	dsn := os.Getenv("SESSION_TEST_DSN")
	if dsn == "" {
		t.Skip("set SESSION_TEST_DSN to run PostgreSQL session tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("session_%d", time.Now().UnixNano())
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
		admin.Close()
		t.Fatal(err)
	}
	return pool, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	}
}

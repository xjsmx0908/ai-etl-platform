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

func TestPostgresCurrentSessionRevocationIsConcurrentAndIdempotent(t *testing.T) {
	pool, cleanup := sessionPool(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES('demo-tenant','Demo')`); err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,password_hash,role,tenant_id,active)
		VALUES('logout-user','unused','readonly','demo-tenant',true) RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 3, 0, 0, 0, time.UTC)
	manager, err := session.New(
		session.NewPostgresStore(&db.Pool{Pool: pool}), demoPolicy(),
		session.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{
		TenantID: "demo-tenant", SubjectID: userID,
		AuthenticationMethod: auth.AuthenticationMethodLocal,
	}
	first, err := manager.Establish(ctx, session.EstablishCommand{
		Principal: principal, Evidence: session.AuthenticationEvidence{
			Assurance: session.AssuranceLocalPassword, AuthenticatedAt: now,
		}, CorrelationID: "logout-login-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Establish(ctx, session.EstablishCommand{
		Principal: principal, Evidence: session.AuthenticationEvidence{
			Assurance: session.AssuranceLocalPassword, AuthenticatedAt: now,
		}, CorrelationID: "logout-login-2",
	})
	if err != nil {
		t.Fatal(err)
	}

	results := make(chan error, 2)
	for _, correlationID := range []string{"logout-current-1", "logout-current-2"} {
		go func(correlationID string) {
			results <- manager.Revoke(ctx, session.RevokeCommand{
				Credential: first.Token, Scope: session.RevokeCurrent, CorrelationID: correlationID,
			})
		}(correlationID)
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatalf("concurrent current-session revoke: %v", err)
		}
	}
	assertDecision(t, manager, first.Token, session.DecisionDeny)
	assertDecision(t, manager, second.Token, session.DecisionAllow)
	var revokedRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM platform_sessions
		WHERE internal_user_id=$1 AND revoked_at IS NOT NULL AND revocation_reason='current_session'`, userID).Scan(&revokedRows); err != nil {
		t.Fatal(err)
	}
	if revokedRows != 1 {
		t.Fatalf("revoked current-session rows=%d", revokedRows)
	}
	var logoutAudits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs
		WHERE tenant_id=$1 AND actor_user_id=$2 AND action='session_logout' AND result='success'
		AND detail->>'reason'='local_session_revoked'`, "demo-tenant", userID).Scan(&logoutAudits); err != nil {
		t.Fatal(err)
	}
	if logoutAudits != 1 {
		t.Fatalf("atomic logout audits=%d", logoutAudits)
	}
}

func TestPostgresCurrentSessionRevocationFollowsCredentialRotation(t *testing.T) {
	pool, cleanup := sessionPool(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES('demo-tenant','Demo')`); err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,password_hash,role,tenant_id,active)
		VALUES('logout-rotation-user','unused','readonly','demo-tenant',true) RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 3, 30, 0, 0, time.UTC)
	manager, err := session.New(
		session.NewPostgresStore(&db.Pool{Pool: pool}), demoPolicy(),
		session.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{
		TenantID: "demo-tenant", SubjectID: userID,
		AuthenticationMethod: auth.AuthenticationMethodFederated,
	}
	first, err := manager.Establish(ctx, session.EstablishCommand{
		Principal: principal, Evidence: session.AuthenticationEvidence{
			Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now,
		}, CorrelationID: "logout-rotation-login",
	})
	if err != nil {
		t.Fatal(err)
	}
	authenticated, err := manager.Authenticate(ctx, first.Token, "knowledge.query")
	if err != nil || authenticated.Decision != session.DecisionAllow {
		t.Fatalf("authenticate result=%+v err=%v", authenticated, err)
	}
	rotated, err := manager.Establish(ctx, session.EstablishCommand{
		Principal: principal, Evidence: session.AuthenticationEvidence{
			Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now,
		}, ReplacesCredential: first.Token, CorrelationID: "logout-raced-rotation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Revoke(ctx, session.RevokeCommand{
		Credential: first.Token, Reference: authenticated.Reference,
		Scope: session.RevokeCurrent, CorrelationID: "logout-after-rotation",
	}); err != nil {
		t.Fatal(err)
	}
	assertDecision(t, manager, rotated.Token, session.DecisionDeny)
}

func TestPostgresConcurrentLoginsKeepThreeActiveSessionsAndAuditEveryEviction(t *testing.T) {
	pool, cleanup := sessionPool(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES('demo-tenant','Demo')`); err != nil {
		t.Fatal(err)
	}
	var userID string
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,password_hash,role,tenant_id,active)
		VALUES('session-cap-user','unused','readonly','demo-tenant',true) RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)
	policy := demoPolicy()
	policy.MaxActiveSessions = 3
	manager, err := session.New(
		session.NewPostgresStore(&db.Pool{Pool: pool}), policy,
		session.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{
		TenantID: "demo-tenant", SubjectID: userID,
		AuthenticationMethod: auth.AuthenticationMethodFederated,
	}
	results := make(chan session.Credential, 8)
	errorsCh := make(chan error, 8)
	for index := range 8 {
		go func(index int) {
			credential, establishErr := manager.Establish(ctx, session.EstablishCommand{
				Principal: principal,
				Evidence: session.AuthenticationEvidence{
					Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now,
				},
				CorrelationID: fmt.Sprintf("concurrent-cap-%d", index),
			})
			if establishErr != nil {
				errorsCh <- establishErr
				return
			}
			results <- credential
		}(index)
	}
	credentials := make([]session.Credential, 0, 8)
	for range 8 {
		select {
		case credential := <-results:
			credentials = append(credentials, credential)
		case establishErr := <-errorsCh:
			t.Fatal(establishErr)
		case <-time.After(10 * time.Second):
			t.Fatal("concurrent login timed out")
		}
	}
	active := 0
	var current session.Credential
	for _, credential := range credentials {
		result, authErr := manager.Authenticate(ctx, credential.Token, "knowledge.query")
		if authErr != nil {
			t.Fatal(authErr)
		}
		if result.Decision == session.DecisionAllow {
			active++
			current = credential
		}
	}
	if active != 3 {
		t.Fatalf("active sessions=%d want=3", active)
	}
	items, err := manager.List(ctx, session.ListCommand{Credential: current.Token})
	if err != nil || len(items) != 3 {
		t.Fatalf("listed sessions=%+v err=%v", items, err)
	}
	var evictionAudits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs
		WHERE tenant_id=$1 AND actor_user_id=$2 AND action='session_limit_eviction'
		AND result='success' AND detail->>'reason'='active_session_limit'`,
		"demo-tenant", userID).Scan(&evictionAudits); err != nil {
		t.Fatal(err)
	}
	if evictionAudits != 5 {
		t.Fatalf("eviction audits=%d want=5", evictionAudits)
	}
}

func TestPostgresManagedRevocationIsOwnedIdempotentAndAuditedOnce(t *testing.T) {
	pool, cleanup := sessionPool(t)
	defer cleanup()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO tenants(id,name) VALUES('demo-tenant','Demo')`); err != nil {
		t.Fatal(err)
	}
	var userID, otherID string
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,password_hash,role,tenant_id,active)
		VALUES('managed-owner','unused','readonly','demo-tenant',true) RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO users(username,password_hash,role,tenant_id,active)
		VALUES('managed-other','unused','readonly','demo-tenant',true) RETURNING id`).Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 11, 0, 0, 0, time.UTC)
	policy := demoPolicy()
	policy.MaxActiveSessions = 3
	manager, err := session.New(
		session.NewPostgresStore(&db.Pool{Pool: pool}), policy,
		session.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	establish := func(subject, correlation string) session.Credential {
		t.Helper()
		credential, establishErr := manager.Establish(ctx, session.EstablishCommand{
			Principal: auth.Principal{
				TenantID: "demo-tenant", SubjectID: subject,
				AuthenticationMethod: auth.AuthenticationMethodFederated,
			},
			Evidence:      session.AuthenticationEvidence{Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now},
			CorrelationID: correlation,
		})
		if establishErr != nil {
			t.Fatal(establishErr)
		}
		return credential
	}
	current := establish(userID, "managed-current")
	target := establish(userID, "managed-target")
	other := establish(otherID, "managed-other")
	items, err := manager.List(ctx, session.ListCommand{Credential: current.Token})
	if err != nil {
		t.Fatal(err)
	}
	var handle string
	for _, item := range items {
		if !item.Current {
			handle = item.Handle
		}
	}
	otherItems, err := manager.List(ctx, session.ListCommand{Credential: other.Token})
	if err != nil || len(otherItems) != 1 {
		t.Fatalf("other items=%+v err=%v", otherItems, err)
	}
	for _, targetHandle := range []string{handle, handle, otherItems[0].Handle} {
		if err := manager.RevokeManaged(ctx, session.RevokeManagedCommand{
			Credential: current.Token, Handle: targetHandle, CorrelationID: "managed-revoke",
		}); err != nil {
			t.Fatal(err)
		}
	}
	assertDecision(t, manager, target.Token, session.DecisionDeny)
	assertDecision(t, manager, other.Token, session.DecisionAllow)
	var audits int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs
		WHERE tenant_id=$1 AND actor_user_id=$2 AND action='session_device_revoked'
		AND result='success' AND detail->>'reason'='user_managed_session'`,
		"demo-tenant", userID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("managed revoke audits=%d want=1", audits)
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

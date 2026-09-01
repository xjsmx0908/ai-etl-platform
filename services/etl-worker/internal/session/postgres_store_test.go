package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v5"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/session"
)

func TestPostgresSessionStoreFailsClosedWhenReadIsUnavailable(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	mock.ExpectQuery("SELECT .+ FROM platform_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errors.New("database unavailable"))
	manager, err := session.New(session.NewPostgresStore(mock), demoPolicy())
	if err != nil {
		t.Fatal(err)
	}

	result, authErr := manager.Authenticate(context.Background(), "opaque-token", "knowledge.query")
	if result.Decision != session.DecisionDeny || !errors.Is(authErr, session.ErrUnavailable) {
		t.Fatalf("result=%+v err=%v", result, authErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresSessionStoreDeniesConcurrentRevocation(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	now := time.Date(2026, 8, 31, 5, 0, 0, 0, time.UTC)
	mock.ExpectQuery("SELECT .+ FROM platform_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "management_handle", "creation_order", "tenant_id", "internal_user_id", "authentication_method", "assurance_level",
			"authenticated_at", "created_at", "last_activity_at", "absolute_expires_at",
			"revoked_at", "generation", "policy_revision", "established_correlation_id",
		}).AddRow(
			"11111111-1111-1111-1111-111111111111", "sm1_33333333-3333-4333-8333-333333333333",
			int64(1),
			"demo-tenant", "22222222-2222-2222-2222-222222222222",
			"federated", "demo-mfa", now.Add(-time.Minute), now.Add(-time.Minute), now.Add(-time.Minute),
			now.Add(time.Hour), nil, int64(1), "personal-demo-v1", "login-42",
		))
	mock.ExpectExec("UPDATE platform_sessions SET last_activity_at").
		WithArgs("11111111-1111-1111-1111-111111111111", int64(1), now).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	manager, err := session.New(
		session.NewPostgresStore(mock), demoPolicy(),
		session.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}

	result, authErr := manager.Authenticate(context.Background(), "opaque-token", "knowledge.query")
	if authErr != nil || result.Decision != session.DecisionDeny {
		t.Fatalf("result=%+v err=%v", result, authErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresSessionStoreFailsClosedWhenWritesAreUnavailable(t *testing.T) {
	now := time.Date(2026, 8, 31, 8, 0, 0, 0, time.UTC)
	t.Run("establish", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		mock.ExpectBegin()
		mock.ExpectQuery("SELECT true FROM users").
			WithArgs("demo-tenant", "22222222-2222-2222-2222-222222222222").
			WillReturnRows(pgxmock.NewRows([]string{"locked"}).AddRow(true))
		mock.ExpectQuery("SELECT revoked_before FROM platform_session_subject_states").
			WithArgs("demo-tenant", "22222222-2222-2222-2222-222222222222").
			WillReturnError(pgx.ErrNoRows)
		mock.ExpectExec("INSERT INTO platform_sessions").
			WithArgs(
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
				pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			).
			WillReturnError(errors.New("write unavailable"))
		mock.ExpectRollback()
		manager, err := session.New(
			session.NewPostgresStore(mock), demoPolicy(), session.WithClock(func() time.Time { return now }),
		)
		if err != nil {
			t.Fatal(err)
		}
		_, establishErr := manager.Establish(context.Background(), session.EstablishCommand{
			Principal: auth.Principal{
				TenantID: "demo-tenant", SubjectID: "22222222-2222-2222-2222-222222222222",
				AuthenticationMethod: auth.AuthenticationMethodFederated,
			},
			Evidence:      session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: now},
			CorrelationID: "login-1",
		})
		if !errors.Is(establishErr, session.ErrUnavailable) {
			t.Fatalf("establish error=%v", establishErr)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("revoke current", func(t *testing.T) {
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		mock.ExpectBegin()
		mock.ExpectQuery("UPDATE platform_sessions SET revoked_at").
			WithArgs(pgxmock.AnyArg(), now, "logout-1").
			WillReturnError(errors.New("write unavailable"))
		mock.ExpectRollback()
		manager, err := session.New(
			session.NewPostgresStore(mock), demoPolicy(), session.WithClock(func() time.Time { return now }),
		)
		if err != nil {
			t.Fatal(err)
		}
		if revokeErr := manager.Revoke(context.Background(), session.RevokeCommand{
			Credential: "opaque-token", Scope: session.RevokeCurrent, CorrelationID: "logout-1",
		}); !errors.Is(revokeErr, session.ErrUnavailable) {
			t.Fatalf("revoke error=%v", revokeErr)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPostgresCurrentRevocationRollsBackWhenAtomicAuditWriteFails(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	now := time.Date(2026, 9, 1, 4, 30, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery("UPDATE platform_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), now, "logout-audit-failure").
		WillReturnRows(pgxmock.NewRows([]string{"tenant_id", "internal_user_id"}).
			AddRow("demo-tenant", "22222222-2222-2222-2222-222222222222"))
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs("demo-tenant", "22222222-2222-2222-2222-222222222222", "logout-audit-failure", now).
		WillReturnError(errors.New("audit unavailable"))
	mock.ExpectRollback()
	manager, err := session.New(
		session.NewPostgresStore(mock), demoPolicy(), session.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}

	revokeErr := manager.Revoke(context.Background(), session.RevokeCommand{
		Credential: "opaque-token", Scope: session.RevokeCurrent, CorrelationID: "logout-audit-failure",
	})
	if !errors.Is(revokeErr, session.ErrUnavailable) {
		t.Fatalf("revoke error=%v", revokeErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresManagedRevocationRollsBackWhenAtomicAuditWriteFails(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	now := time.Date(2026, 9, 1, 13, 0, 0, 0, time.UTC)
	credential := "managed-current-credential"
	mock.ExpectQuery("SELECT .+ FROM platform_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "management_handle", "creation_order", "tenant_id", "internal_user_id", "authentication_method", "assurance_level",
			"authenticated_at", "created_at", "last_activity_at", "absolute_expires_at",
			"revoked_at", "generation", "policy_revision", "established_correlation_id",
		}).AddRow(
			"11111111-1111-4111-8111-111111111111", "sm1_22222222-2222-4222-8222-222222222222",
			int64(1),
			"demo-tenant", "33333333-3333-4333-8333-333333333333", "federated", "demo-mfa",
			now, now, now, now.Add(time.Hour), nil, int64(1), "personal-demo-v1", "managed-current",
		))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id FROM platform_sessions").
		WithArgs(pgxmock.AnyArg(), "11111111-1111-4111-8111-111111111111", "demo-tenant",
			"33333333-3333-4333-8333-333333333333", now, now.Add(-30*time.Minute), "personal-demo-v1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("11111111-1111-4111-8111-111111111111"))
	handle := mustManagementHandle(t, "sm1_44444444-4444-4444-8444-444444444444")
	mock.ExpectQuery("SELECT id FROM platform_sessions").
		WithArgs(handle.String(), "demo-tenant", "33333333-3333-4333-8333-333333333333",
			now, now.Add(-30*time.Minute), "personal-demo-v1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("55555555-5555-4555-8555-555555555555"))
	mock.ExpectExec("UPDATE platform_sessions SET revoked_at").
		WithArgs("55555555-5555-4555-8555-555555555555", now, "managed-audit-failure").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs("demo-tenant", "33333333-3333-4333-8333-333333333333", "managed-audit-failure", now).
		WillReturnError(errors.New("audit unavailable"))
	mock.ExpectRollback()
	manager, err := session.New(
		session.NewPostgresStore(mock), demoPolicy(), session.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	err = manager.RevokeManaged(context.Background(), session.RevokeManagedCommand{
		Credential: credential, Handle: handle,
		CorrelationID: "managed-audit-failure",
	})
	if !errors.Is(err, session.ErrUnavailable) {
		t.Fatalf("revoke error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresSessionLimitEvictionRollsBackNewSessionWhenAuditFails(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	now := time.Date(2026, 9, 1, 13, 30, 0, 0, time.UTC)
	userID := "33333333-3333-4333-8333-333333333333"
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT true FROM users").
		WithArgs("demo-tenant", userID).
		WillReturnRows(pgxmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery("SELECT revoked_before FROM platform_session_subject_states").
		WithArgs("demo-tenant", userID).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery("SELECT id FROM platform_sessions").
		WithArgs("demo-tenant", userID, now, now.Add(-30*time.Minute), "personal-demo-v1").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("11111111-1111-4111-8111-111111111111"))
	mock.ExpectExec("UPDATE platform_sessions SET revoked_at").
		WithArgs("11111111-1111-4111-8111-111111111111", now, "cap-audit-failure").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("INSERT INTO audit_logs").
		WithArgs("demo-tenant", userID, "cap-audit-failure", now).
		WillReturnError(errors.New("audit unavailable"))
	mock.ExpectRollback()
	policy := demoPolicy()
	policy.MaxActiveSessions = 1
	manager, err := session.New(
		session.NewPostgresStore(mock), policy, session.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Establish(context.Background(), session.EstablishCommand{
		Principal: auth.Principal{
			TenantID: "demo-tenant", SubjectID: userID,
			AuthenticationMethod: auth.AuthenticationMethodFederated,
		},
		Evidence:      session.AuthenticationEvidence{Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now},
		CorrelationID: "cap-audit-failure",
	})
	if !errors.Is(err, session.ErrUnavailable) {
		t.Fatalf("establish error=%v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresSubjectRevokeLocksCredentialBeforeBulkRevocation(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT tenant_id,internal_user_id FROM platform_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"tenant_id", "internal_user_id"}).
			AddRow("demo-tenant", "22222222-2222-2222-2222-222222222222"))
	mock.ExpectQuery("SELECT true FROM users").
		WithArgs("demo-tenant", "22222222-2222-2222-2222-222222222222").
		WillReturnRows(pgxmock.NewRows([]string{"locked"}).AddRow(true))
	mock.ExpectQuery("SELECT true FROM platform_sessions").
		WithArgs(pgxmock.AnyArg(), "demo-tenant", "22222222-2222-2222-2222-222222222222", now, now.Add(-30*time.Minute), "personal-demo-v1").
		WillReturnRows(pgxmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectExec("INSERT INTO platform_session_subject_states").
		WithArgs("demo-tenant", "22222222-2222-2222-2222-222222222222", now, "revoke-all-1").
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec("UPDATE platform_sessions SET revoked_at").
		WithArgs("demo-tenant", "22222222-2222-2222-2222-222222222222", now, "revoke-all-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	mock.ExpectCommit()
	manager, err := session.New(
		session.NewPostgresStore(mock), demoPolicy(), session.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Revoke(context.Background(), session.RevokeCommand{
		Credential: "opaque-token", Scope: session.RevokeSubject, CorrelationID: "revoke-all-1",
	}); err != nil {
		t.Fatalf("revoke subject: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

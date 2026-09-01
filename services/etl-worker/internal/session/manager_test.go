package session_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/session"
)

func TestEstablishedSessionAuthenticatesItsPrincipal(t *testing.T) {
	now := time.Date(2026, 8, 31, 2, 30, 0, 0, time.UTC)
	manager, err := session.New(session.NewMemoryStore(), demoPolicy(), session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("new manager: %v", err)
	}
	principal := auth.Principal{
		TenantID:             "demo-tenant",
		SubjectID:            "user-42",
		Role:                 "readonly",
		AuthenticationMethod: auth.AuthenticationMethodFederated,
		Capabilities:         []string{"query"},
	}
	credential, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal: principal,
		Evidence: session.AuthenticationEvidence{
			Assurance: "demo-mfa", AuthenticatedAt: now,
		},
		CorrelationID: "login-1",
	})
	if err != nil {
		t.Fatalf("establish: %v", err)
	}
	if credential.Token == "" || !credential.ExpiresAt.Equal(now.Add(8*time.Hour)) {
		t.Fatalf("unexpected credential: %+v", credential)
	}

	result, err := manager.Authenticate(context.Background(), credential.Token, "knowledge.query")
	if err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if result.Decision != session.DecisionAllow || result.Principal.TenantID != principal.TenantID ||
		result.Principal.SubjectID != principal.SubjectID ||
		result.Principal.AuthenticationMethod != principal.AuthenticationMethod ||
		result.Principal.Role != "" || len(result.Principal.Capabilities) != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestUserListsOnlyActiveSessionsWithOpaqueManagementHandles(t *testing.T) {
	base := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	now := base
	policy := demoPolicy()
	policy.MaxActiveSessions = 3
	manager, err := session.New(session.NewMemoryStore(), policy, session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{
		TenantID: "demo-tenant", SubjectID: "user-42",
		AuthenticationMethod: auth.AuthenticationMethodFederated,
	}
	first, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal:     principal,
		Evidence:      session.AuthenticationEvidence{Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now},
		CorrelationID: "login-list-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	second, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal:     principal,
		Evidence:      session.AuthenticationEvidence{Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now},
		CorrelationID: "login-list-2",
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal: auth.Principal{
			TenantID: "demo-tenant", SubjectID: "user-99",
			AuthenticationMethod: auth.AuthenticationMethodFederated,
		},
		Evidence:      session.AuthenticationEvidence{Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now},
		CorrelationID: "login-other",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Revoke(context.Background(), session.RevokeCommand{
		Credential: first.Token, Scope: session.RevokeCurrent, CorrelationID: "logout-old",
	}); err != nil {
		t.Fatal(err)
	}

	items, err := manager.List(context.Background(), session.ListCommand{Credential: second.Token})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("active sessions=%d want=1: %+v", len(items), items)
	}
	item := items[0]
	if item.Handle == "" || string(item.Handle) == second.Token || string(item.Handle) == first.Token || string(item.Handle) == other.Token {
		t.Fatalf("management handle is not independently opaque: %+v", item)
	}
	if !item.Current || item.AuthenticationMethod != auth.AuthenticationMethodFederated ||
		!item.CreatedAt.Equal(base.Add(time.Minute)) || !item.LastActivityAt.Equal(base.Add(time.Minute)) ||
		!item.ExpiresAt.Equal(base.Add(time.Minute).Add(8*time.Hour)) {
		t.Fatalf("unexpected session view: %+v", item)
	}
}

func TestFourthLoginEvictsOldestActiveSessionButRotationKeepsItsSlot(t *testing.T) {
	base := time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	now := base
	policy := demoPolicy()
	policy.MaxActiveSessions = 3
	manager, err := session.New(session.NewMemoryStore(), policy, session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{
		TenantID: "demo-tenant", SubjectID: "user-42",
		AuthenticationMethod: auth.AuthenticationMethodFederated,
	}
	establish := func(correlation string) session.Credential {
		t.Helper()
		credential, establishErr := manager.Establish(context.Background(), session.EstablishCommand{
			Principal:     principal,
			Evidence:      session.AuthenticationEvidence{Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now},
			CorrelationID: correlation,
		})
		if establishErr != nil {
			t.Fatal(establishErr)
		}
		return credential
	}
	first := establish("cap-login-1")
	now = now.Add(time.Minute)
	second := establish("cap-login-2")
	now = now.Add(time.Minute)
	third := establish("cap-login-3")
	now = now.Add(time.Minute)
	fourth := establish("cap-login-4")

	assertDecision(t, manager, first.Token, session.DecisionDeny)
	for _, active := range []session.Credential{second, third, fourth} {
		assertDecision(t, manager, active.Token, session.DecisionAllow)
	}
	items, err := manager.List(context.Background(), session.ListCommand{Credential: fourth.Token})
	if err != nil || len(items) != 3 {
		t.Fatalf("sessions=%+v err=%v", items, err)
	}
	var fourthHandle session.ManagementHandle
	for _, item := range items {
		if item.Current {
			fourthHandle = item.Handle
		}
	}

	now = now.Add(time.Minute)
	rotated, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal:          principal,
		Evidence:           session.AuthenticationEvidence{Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now},
		ReplacesCredential: fourth.Token, CorrelationID: "cap-reauth-4",
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err = manager.List(context.Background(), session.ListCommand{Credential: rotated.Token})
	if err != nil || len(items) != 3 {
		t.Fatalf("rotation consumed a new slot: sessions=%+v err=%v", items, err)
	}
	current := 0
	for _, item := range items {
		if item.Current {
			current++
			if item.Handle != fourthHandle {
				t.Fatalf("rotation changed management handle: before=%q after=%q", fourthHandle, item.Handle)
			}
		}
	}
	if current != 1 {
		t.Fatalf("current sessions=%d: %+v", current, items)
	}
}

func TestUserRevokesOwnedNonCurrentSessionByManagementHandle(t *testing.T) {
	now := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	policy := demoPolicy()
	policy.MaxActiveSessions = 3
	manager, err := session.New(session.NewMemoryStore(), policy, session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	establish := func(subject, correlation string) session.Credential {
		t.Helper()
		credential, establishErr := manager.Establish(context.Background(), session.EstablishCommand{
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
	current := establish("user-42", "manage-login-current")
	target := establish("user-42", "manage-login-target")
	other := establish("user-99", "manage-login-other")
	targets, err := manager.List(context.Background(), session.ListCommand{Credential: current.Token})
	if err != nil {
		t.Fatal(err)
	}
	var targetHandle, currentHandle session.ManagementHandle
	for _, item := range targets {
		if item.Current {
			currentHandle = item.Handle
		} else {
			targetHandle = item.Handle
		}
	}
	otherItems, err := manager.List(context.Background(), session.ListCommand{Credential: other.Token})
	if err != nil || len(otherItems) != 1 {
		t.Fatalf("other sessions=%+v err=%v", otherItems, err)
	}

	if err := manager.RevokeManaged(context.Background(), session.RevokeManagedCommand{
		Credential: current.Token, Handle: targetHandle, CorrelationID: "device-revoke-1",
	}); err != nil {
		t.Fatal(err)
	}
	assertDecision(t, manager, target.Token, session.DecisionDeny)
	assertDecision(t, manager, current.Token, session.DecisionAllow)

	for _, handle := range []session.ManagementHandle{targetHandle, otherItems[0].Handle, "sm1_00000000-0000-4000-8000-000000000000"} {
		if err := manager.RevokeManaged(context.Background(), session.RevokeManagedCommand{
			Credential: current.Token, Handle: handle, CorrelationID: "device-revoke-idempotent",
		}); err != nil {
			t.Fatalf("non-disclosing revoke handle=%q: %v", handle, err)
		}
	}
	assertDecision(t, manager, other.Token, session.DecisionAllow)
	if err := manager.RevokeManaged(context.Background(), session.RevokeManagedCommand{
		Credential: current.Token, Handle: currentHandle, CorrelationID: "device-revoke-current",
	}); !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("current-session managed revoke error=%v", err)
	}
}

func TestManagedRevokeTreatsExpiredOwnedSessionAsAlreadyInactive(t *testing.T) {
	base := time.Date(2026, 9, 1, 10, 30, 0, 0, time.UTC)
	now := base
	policy := demoPolicy()
	policy.IdleTimeout = 15 * time.Minute
	manager, err := session.New(session.NewMemoryStore(), policy, session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{
		TenantID: "demo-tenant", SubjectID: "user-42",
		AuthenticationMethod: auth.AuthenticationMethodFederated,
	}
	target, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal:     principal,
		Evidence:      session.AuthenticationEvidence{Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now},
		CorrelationID: "expired-target",
	})
	if err != nil {
		t.Fatal(err)
	}
	targets, err := manager.List(context.Background(), session.ListCommand{Credential: target.Token})
	if err != nil || len(targets) != 1 {
		t.Fatalf("target sessions=%+v err=%v", targets, err)
	}
	targetHandle := targets[0].Handle
	now = base.Add(14 * time.Minute)
	current, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal:     principal,
		Evidence:      session.AuthenticationEvidence{Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now},
		CorrelationID: "active-current",
	})
	if err != nil {
		t.Fatal(err)
	}
	now = base.Add(16 * time.Minute)
	if err := manager.RevokeManaged(context.Background(), session.RevokeManagedCommand{
		Credential: current.Token, Handle: session.ManagementHandle(targetHandle), CorrelationID: "expired-target-revoke",
	}); err != nil {
		t.Fatal(err)
	}
	items, err := manager.List(context.Background(), session.ListCommand{Credential: current.Token})
	if err != nil || len(items) != 1 || !items[0].Current {
		t.Fatalf("items=%+v err=%v", items, err)
	}
}

func TestLocalPasswordSessionAllowsStandardActionsButRequiresStrongerEvidenceForHighRisk(t *testing.T) {
	now := time.Date(2026, 8, 31, 19, 0, 0, 0, time.UTC)
	manager, err := session.New(session.NewMemoryStore(), demoPolicy(), session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal: auth.Principal{
			TenantID: "demo-tenant", SubjectID: "local-user",
			AuthenticationMethod: auth.AuthenticationMethodLocal,
		},
		Evidence: session.AuthenticationEvidence{
			Assurance: "local-password", AuthenticatedAt: now,
		},
		CorrelationID: "password-login-1",
	})
	if err != nil {
		t.Fatalf("establish local session: %v", err)
	}
	standard, err := manager.Authenticate(context.Background(), credential.Token, "knowledge.query")
	if err != nil || standard.Decision != session.DecisionAllow {
		t.Fatalf("standard result=%+v err=%v", standard, err)
	}
	highRisk, err := manager.Authenticate(context.Background(), credential.Token, "identity.binding.change")
	if err != nil || highRisk.Decision != session.DecisionReauthenticate {
		t.Fatalf("high-risk result=%+v err=%v", highRisk, err)
	}
}

func TestSessionEnforcesIdleAbsoluteAndReauthenticationBoundaries(t *testing.T) {
	base := time.Date(2026, 8, 31, 3, 0, 0, 0, time.UTC)
	now := base
	manager, err := session.New(session.NewMemoryStore(), demoPolicy(), session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	establish := func(t *testing.T) session.Credential {
		t.Helper()
		credential, establishErr := manager.Establish(context.Background(), session.EstablishCommand{
			Principal: auth.Principal{
				TenantID: "demo-tenant", SubjectID: "user-42", Role: "readonly",
				AuthenticationMethod: auth.AuthenticationMethodFederated,
			},
			Evidence:      session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: base},
			CorrelationID: "login-boundary",
		})
		if establishErr != nil {
			t.Fatalf("establish: %v", establishErr)
		}
		return credential
	}

	t.Run("high risk freshness expires at the exact boundary", func(t *testing.T) {
		credential := establish(t)
		now = base.Add(10 * time.Minute)
		result, authErr := manager.Authenticate(context.Background(), credential.Token, "identity.binding.change")
		if authErr != nil || result.Decision != session.DecisionReauthenticate {
			t.Fatalf("result=%+v err=%v", result, authErr)
		}
		if result.Principal.SubjectID != "user-42" || result.Principal.TenantID != "demo-tenant" ||
			result.Principal.AuthenticationMethod != auth.AuthenticationMethodFederated {
			t.Fatalf("reauthentication decision must retain internal identity: %+v", result.Principal)
		}
		if result.Principal.Role != "" || len(result.Principal.Capabilities) != 0 {
			t.Fatalf("session must not return an authority snapshot: %+v", result.Principal)
		}
	})

	t.Run("idle timeout expires at the exact boundary", func(t *testing.T) {
		now = base
		credential := establish(t)
		now = base.Add(30 * time.Minute)
		result, authErr := manager.Authenticate(context.Background(), credential.Token, "knowledge.query")
		if authErr != nil || result.Decision != session.DecisionDeny {
			t.Fatalf("result=%+v err=%v", result, authErr)
		}
	})

	t.Run("absolute lifetime cannot be extended by activity", func(t *testing.T) {
		now = base
		credential := establish(t)
		for elapsed := 20 * time.Minute; elapsed < 8*time.Hour; elapsed += 20 * time.Minute {
			now = base.Add(elapsed)
			result, authErr := manager.Authenticate(context.Background(), credential.Token, "knowledge.query")
			if authErr != nil || result.Decision != session.DecisionAllow {
				t.Fatalf("elapsed=%s result=%+v err=%v", elapsed, result, authErr)
			}
		}
		now = base.Add(8 * time.Hour)
		result, authErr := manager.Authenticate(context.Background(), credential.Token, "knowledge.query")
		if authErr != nil || result.Decision != session.DecisionDeny {
			t.Fatalf("result=%+v err=%v", result, authErr)
		}
	})
}

func TestRevokeStopsCurrentOrAllSubjectSessions(t *testing.T) {
	base := time.Date(2026, 8, 31, 4, 0, 0, 0, time.UTC)
	manager, err := session.New(session.NewMemoryStore(), demoPolicy(), session.WithClock(func() time.Time { return base }))
	if err != nil {
		t.Fatal(err)
	}
	establish := func(subject string) session.Credential {
		credential, establishErr := manager.Establish(context.Background(), session.EstablishCommand{
			Principal: auth.Principal{
				TenantID: "demo-tenant", SubjectID: subject, Role: "readonly",
				AuthenticationMethod: auth.AuthenticationMethodFederated,
			},
			Evidence:      session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: base},
			CorrelationID: "login-" + subject,
		})
		if establishErr != nil {
			t.Fatalf("establish %s: %v", subject, establishErr)
		}
		return credential
	}
	first := establish("user-42")
	second := establish("user-42")
	other := establish("user-99")

	if err := manager.Revoke(context.Background(), session.RevokeCommand{
		Credential: first.Token, Scope: session.RevokeCurrent, CorrelationID: "logout-1",
	}); err != nil {
		t.Fatalf("revoke current: %v", err)
	}
	assertDecision(t, manager, first.Token, session.DecisionDeny)
	assertDecision(t, manager, second.Token, session.DecisionAllow)

	if err := manager.Revoke(context.Background(), session.RevokeCommand{
		Credential: second.Token, Scope: session.RevokeSubject, CorrelationID: "revoke-all-1",
	}); err != nil {
		t.Fatalf("revoke subject: %v", err)
	}
	assertDecision(t, manager, second.Token, session.DecisionDeny)
	assertDecision(t, manager, other.Token, session.DecisionAllow)
}

func TestCurrentRevokeUsesStableReferenceAcrossCredentialRotation(t *testing.T) {
	now := time.Date(2026, 9, 1, 3, 30, 0, 0, time.UTC)
	manager, err := session.New(
		session.NewMemoryStore(), demoPolicy(), session.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{
		TenantID: "demo-tenant", SubjectID: "user-42", Role: "readonly",
		AuthenticationMethod: auth.AuthenticationMethodFederated,
	}
	first, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal: principal, Evidence: session.AuthenticationEvidence{
			Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now,
		}, CorrelationID: "login-before-logout",
	})
	if err != nil {
		t.Fatal(err)
	}
	authenticated, err := manager.Authenticate(context.Background(), first.Token, "knowledge.query")
	if err != nil || authenticated.Decision != session.DecisionAllow {
		t.Fatalf("authenticate result=%+v err=%v", authenticated, err)
	}
	rotated, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal: principal, Evidence: session.AuthenticationEvidence{
			Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now,
		}, ReplacesCredential: first.Token, CorrelationID: "rotation-raced-logout",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Revoke(context.Background(), session.RevokeCommand{
		Credential: first.Token, Reference: authenticated.Reference,
		Scope: session.RevokeCurrent, CorrelationID: "logout-after-rotation",
	}); err != nil {
		t.Fatal(err)
	}
	assertDecision(t, manager, rotated.Token, session.DecisionDeny)
}

func TestRevokedCredentialCannotRevokeOtherSubjectSessions(t *testing.T) {
	base := time.Date(2026, 8, 31, 4, 30, 0, 0, time.UTC)
	manager, err := session.New(session.NewMemoryStore(), demoPolicy(), session.WithClock(func() time.Time { return base }))
	if err != nil {
		t.Fatal(err)
	}
	establish := func() session.Credential {
		credential, establishErr := manager.Establish(context.Background(), session.EstablishCommand{
			Principal: auth.Principal{
				TenantID: "demo-tenant", SubjectID: "user-42",
				AuthenticationMethod: auth.AuthenticationMethodFederated,
			},
			Evidence:      session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: base},
			CorrelationID: "login-revoke",
		})
		if establishErr != nil {
			t.Fatal(establishErr)
		}
		return credential
	}
	first := establish()
	second := establish()
	if err := manager.Revoke(context.Background(), session.RevokeCommand{
		Credential: first.Token, Scope: session.RevokeCurrent, CorrelationID: "logout-1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Revoke(context.Background(), session.RevokeCommand{
		Credential: first.Token, Scope: session.RevokeSubject, CorrelationID: "revoke-all-1",
	}); !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("revoked credential subject-revoke error=%v", err)
	}
	assertDecision(t, manager, second.Token, session.DecisionAllow)
}

func TestRevokeRejectsInvalidCommand(t *testing.T) {
	base := time.Date(2026, 8, 31, 4, 45, 0, 0, time.UTC)
	manager, err := session.New(session.NewMemoryStore(), demoPolicy(), session.WithClock(func() time.Time { return base }))
	if err != nil {
		t.Fatal(err)
	}
	invalid := []session.RevokeCommand{
		{Scope: session.RevokeCurrent, CorrelationID: "logout-1"},
		{Credential: "credential", CorrelationID: "logout-1"},
		{Credential: "credential", Scope: session.RevokeCurrent},
		{Credential: "credential", Scope: session.RevokeCurrent, CorrelationID: " logout-1"},
		{Credential: "credential", Scope: session.RevokeCurrent, CorrelationID: string(make([]byte, 257))},
	}
	for index, command := range invalid {
		if revokeErr := manager.Revoke(context.Background(), command); !errors.Is(revokeErr, session.ErrInvalid) {
			t.Fatalf("case %d error=%v", index, revokeErr)
		}
	}
}

func TestEstablishRotatesCredentialAfterFreshAuthentication(t *testing.T) {
	base := time.Date(2026, 8, 31, 6, 0, 0, 0, time.UTC)
	now := base
	manager, err := session.New(session.NewMemoryStore(), demoPolicy(), session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	principal := auth.Principal{
		TenantID: "demo-tenant", SubjectID: "user-42", Role: "readonly",
		AuthenticationMethod: auth.AuthenticationMethodFederated,
	}
	first, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal:     principal,
		Evidence:      session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: base},
		CorrelationID: "login-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	now = base.Add(15 * time.Minute)
	rotated, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal:          principal,
		Evidence:           session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: now},
		ReplacesCredential: first.Token,
		CorrelationID:      "reauth-1",
	})
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if rotated.Token == "" || rotated.Token == first.Token || !rotated.ExpiresAt.Equal(base.Add(8*time.Hour)) {
		t.Fatalf("unexpected rotated credential: %+v", rotated)
	}
	assertDecision(t, manager, first.Token, session.DecisionDeny)
	assertDecision(t, manager, rotated.Token, session.DecisionAllow)

	if _, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal:          principal,
		Evidence:           session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: now},
		ReplacesCredential: first.Token,
		CorrelationID:      "reauth-replay",
	}); !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("replayed replacement error=%v", err)
	}
}

func TestEstablishRejectsInvalidOrStaleAuthenticationEvidence(t *testing.T) {
	base := time.Date(2026, 8, 31, 7, 0, 0, 0, time.UTC)
	manager, err := session.New(session.NewMemoryStore(), demoPolicy(), session.WithClock(func() time.Time { return base }))
	if err != nil {
		t.Fatal(err)
	}
	valid := session.EstablishCommand{
		Principal: auth.Principal{
			TenantID: "demo-tenant", SubjectID: "user-42",
			AuthenticationMethod: auth.AuthenticationMethodFederated,
		},
		Evidence:      session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: base},
		CorrelationID: "login-42",
	}
	invalid := []session.EstablishCommand{
		{Principal: valid.Principal, Evidence: session.AuthenticationEvidence{Assurance: "weak", AuthenticatedAt: base}},
		{Principal: valid.Principal, Evidence: session.AuthenticationEvidence{Assurance: "local-password", AuthenticatedAt: base}},
		{Principal: auth.Principal{TenantID: "demo-tenant", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodLocal}, Evidence: session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: base}},
		{Principal: valid.Principal, Evidence: session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: base.Add(time.Second)}},
		{Principal: valid.Principal, Evidence: session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: base.Add(-10 * time.Minute)}},
		{Principal: auth.Principal{TenantID: "demo-tenant", AuthenticationMethod: auth.AuthenticationMethodFederated}, Evidence: valid.Evidence},
		{Principal: auth.Principal{TenantID: "demo-tenant", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodService}, Evidence: valid.Evidence},
		{Principal: valid.Principal, Evidence: valid.Evidence, CorrelationID: " login-42"},
		{Principal: valid.Principal, Evidence: valid.Evidence},
		{Principal: valid.Principal, Evidence: valid.Evidence, CorrelationID: string(make([]byte, 257))},
	}
	for index, command := range invalid {
		if _, establishErr := manager.Establish(context.Background(), command); !errors.Is(establishErr, session.ErrInvalid) {
			t.Fatalf("case %d error=%v", index, establishErr)
		}
	}
}

func TestPolicyRejectsInvalidEstablishmentAssuranceConfiguration(t *testing.T) {
	for _, assurances := range []map[auth.AuthenticationMethod]session.Assurance{
		{auth.AuthenticationMethodLocal: "local-password"},
		{auth.AuthenticationMethodFederated: ""},
		{auth.AuthenticationMethodService: "demo-mfa"},
	} {
		policy := demoPolicy()
		policy.EstablishmentAssurances = assurances
		if _, err := session.New(session.NewMemoryStore(), policy); !errors.Is(err, session.ErrInvalid) {
			t.Fatalf("assurances=%v error=%v", assurances, err)
		}
	}
}

func TestAuthenticateDeniesUnknownActionAndUsesFrozenRiskPolicy(t *testing.T) {
	now := time.Date(2026, 8, 31, 7, 30, 0, 0, time.UTC)
	policy := demoPolicy()
	manager, err := session.New(session.NewMemoryStore(), policy, session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal: auth.Principal{
			TenantID: "demo-tenant", SubjectID: "user-42",
			AuthenticationMethod: auth.AuthenticationMethodFederated,
		},
		Evidence:      session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: now},
		CorrelationID: "login-policy",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertDecisionForAction(t, manager, credential.Token, "knowledge.qeury", session.DecisionDeny)

	policy.ActionRisks["identity.binding.change"] = session.RiskStandard
	now = now.Add(10 * time.Minute)
	assertDecisionForAction(t, manager, credential.Token, "identity.binding.change", session.DecisionReauthenticate)
}

func assertDecision(t *testing.T, manager *session.Manager, token string, expected session.Decision) {
	assertDecisionForAction(t, manager, token, "knowledge.query", expected)
}

func assertDecisionForAction(t *testing.T, manager *session.Manager, token, action string, expected session.Decision) {
	t.Helper()
	result, err := manager.Authenticate(context.Background(), token, action)
	if err != nil || result.Decision != expected {
		t.Fatalf("decision=%q want=%q err=%v", result.Decision, expected, err)
	}
}

func demoPolicy() session.Policy {
	return session.Policy{
		IdleTimeout:       30 * time.Minute,
		AbsoluteLifetime:  8 * time.Hour,
		HighRiskFreshness: 10 * time.Minute,
		HighRiskAssurance: "demo-mfa",
		EstablishmentAssurances: map[auth.AuthenticationMethod]session.Assurance{
			auth.AuthenticationMethodFederated: "demo-mfa",
			auth.AuthenticationMethodLocal:     "local-password",
		},
		ActionRisks: map[string]session.Risk{
			"knowledge.query":         session.RiskStandard,
			"identity.binding.change": session.RiskHigh,
		},
		Revision: "personal-demo-v1",
	}
}

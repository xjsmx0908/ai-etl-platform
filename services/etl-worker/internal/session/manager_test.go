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
	for _, assurances := range []map[auth.AuthenticationMethod]string{
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
		EstablishmentAssurances: map[auth.AuthenticationMethod]string{
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

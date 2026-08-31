package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/session"
	"ai-etl-pipeline/internal/userstore"
)

func TestSessionCredentialAuthenticatorPreservesLegacyJWTAuthentication(t *testing.T) {
	users := newFakeUserStore()
	seedUser(t, users, "alice", "unused", "admin", "acme", true)
	user, found, err := users.GetByUsername(context.Background(), "alice")
	if err != nil || !found {
		t.Fatalf("load user: found=%v err=%v", found, err)
	}
	const secret = "test-secret-0123456789abcdef"
	token, _, err := auth.IssueToken(secret, user.ID, user.Username, user.Role, user.TenantID, user.TokenVersion)
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	sessions, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time {
		return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	}))
	if err != nil {
		t.Fatalf("new session manager: %v", err)
	}
	authenticator := newSessionCredentialAuthenticator(auth.NewVerifierWithStore(secret, users), sessions, users)
	request := httptest.NewRequest(http.MethodGet, "/v1/auth/session", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	auth.Middleware(authenticator, auth.ScopeAdmin)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal := auth.GetPrincipal(r.Context())
		if principal.SubjectID != user.ID || principal.Role != "admin" || principal.TenantID != "acme" {
			t.Fatalf("unexpected principal: %+v", principal)
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("legacy JWT should remain valid, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestSessionCredentialAuthenticatorResolvesLiveInternalAuthority(t *testing.T) {
	users := newFakeUserStore()
	seedUser(t, users, "alice", "unused", "admin", "acme", true)
	user, found, err := users.GetByUsername(context.Background(), "alice")
	if err != nil || !found {
		t.Fatalf("load user: found=%v err=%v", found, err)
	}
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	sessions, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("new session manager: %v", err)
	}
	credential, err := sessions.Establish(context.Background(), session.EstablishCommand{
		Principal: auth.Principal{
			TenantID:             user.TenantID,
			SubjectID:            user.ID,
			AuthenticationMethod: auth.AuthenticationMethodLocal,
		},
		Evidence:      session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: now},
		CorrelationID: "login-1",
	})
	if err != nil {
		t.Fatalf("establish session: %v", err)
	}
	readonly := "readonly"
	if _, err := users.Update(context.Background(), user.ID, userstore.UserPatch{Role: &readonly}); err != nil {
		t.Fatalf("change live role: %v", err)
	}
	legacy := &countingAuthenticator{}
	authenticator := newSessionCredentialAuthenticator(legacy, sessions, users)
	request := httptest.NewRequest(http.MethodGet, "/v1/auth/session", nil)
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential.Token)
	recorder := httptest.NewRecorder()

	auth.Middleware(authenticator, auth.ScopeQuery)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal := auth.GetPrincipal(r.Context())
		if principal.SubjectID != user.ID || principal.Role != readonly || principal.TenantID != "acme" {
			t.Fatalf("unexpected principal: %+v", principal)
		}
		if len(principal.Capabilities) != 1 || principal.Capabilities[0] != auth.ScopeQuery {
			t.Fatalf("authority must come from the current role: %v", principal.Capabilities)
		}
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("session credential should authenticate, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if legacy.calls != 0 {
		t.Fatalf("session credential must not invoke JWT adapter, got %d calls", legacy.calls)
	}
}

func TestSessionCredentialAuthenticatorFailsClosedWithoutJWTFallback(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	users := newFakeUserStore()
	seedUser(t, users, "alice", "unused", "user", "acme", true)
	user, _, _ := users.GetByUsername(context.Background(), "alice")

	t.Run("revoked credential", func(t *testing.T) {
		manager, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return now }))
		if err != nil {
			t.Fatalf("new session manager: %v", err)
		}
		credential := establishSessionCredential(t, manager, user.ID, user.TenantID, now)
		if err := manager.Revoke(context.Background(), session.RevokeCommand{
			Credential: credential, Scope: session.RevokeCurrent, CorrelationID: "logout-1",
		}); err != nil {
			t.Fatalf("revoke session: %v", err)
		}
		assertSessionAuthenticationRejectedWithoutFallback(t, manager, users, credential)
	})

	t.Run("expired credential", func(t *testing.T) {
		clock := now
		manager, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return clock }))
		if err != nil {
			t.Fatalf("new session manager: %v", err)
		}
		credential := establishSessionCredential(t, manager, user.ID, user.TenantID, now)
		clock = now.Add(8 * time.Hour)
		assertSessionAuthenticationRejectedWithoutFallback(t, manager, users, credential)
	})

	t.Run("storage failure", func(t *testing.T) {
		manager, err := session.New(session.NewPostgresStore(nil), platformSessionPolicy(), session.WithClock(func() time.Time { return now }))
		if err != nil {
			t.Fatalf("new session manager: %v", err)
		}
		assertSessionAuthenticationRejectedWithoutFallback(t, manager, users, "opaque-token")
	})

	t.Run("JWT-shaped opaque credential", func(t *testing.T) {
		manager, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return now }))
		if err != nil {
			t.Fatalf("new session manager: %v", err)
		}
		assertSessionAuthenticationRejectedWithoutFallback(t, manager, users, "header.payload.signature")
	})
}

func TestSessionCredentialAuthenticatorRejectsInvalidLiveAuthority(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		user   userstore.User
		tenant string
	}{
		{name: "unknown user", user: userstore.User{}, tenant: "acme"},
		{name: "inactive user", user: userstore.User{ID: "u-inactive", Username: "inactive", Role: "user", TenantID: "acme", Active: false}, tenant: "acme"},
		{name: "tenant mismatch", user: userstore.User{ID: "u-other", Username: "other", Role: "user", TenantID: "other", Active: true}, tenant: "acme"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			users := newFakeUserStore()
			subjectID := "u-missing"
			if test.user.ID != "" {
				users.byID[test.user.ID] = test.user
				users.byName[test.user.Username] = test.user
				subjectID = test.user.ID
			}
			manager, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return now }))
			if err != nil {
				t.Fatalf("new session manager: %v", err)
			}
			credential := establishSessionCredential(t, manager, subjectID, test.tenant, now)
			assertSessionAuthenticationRejectedWithoutFallback(t, manager, users, credential)
		})
	}
}

func TestPlatformSessionManagerConstructionIsDefaultOffAndDemoOnly(t *testing.T) {
	disabled, err := newPlatformSessionManager(config.Config{Environment: "dev"}, nil)
	if err != nil || disabled != nil {
		t.Fatalf("default-off session manager = %v, err=%v", disabled, err)
	}

	invalidProfiles := []config.Config{
		{Environment: "dev", SessionCoreEnabled: true},
		{Environment: "staging", SessionCoreEnabled: true, IdentityPolicyProfile: "personal-demo-v1"},
		{Environment: "production", SessionCoreEnabled: true, IdentityPolicyProfile: "personal-demo-v1"},
	}
	for _, cfg := range invalidProfiles {
		manager, managerErr := newPlatformSessionManager(cfg, nil)
		if managerErr == nil || manager != nil {
			t.Fatalf("invalid profile %+v must not construct a manager", cfg)
		}
	}

	enabled, err := newPlatformSessionManager(config.Config{
		Environment: "dev", SessionCoreEnabled: true, IdentityPolicyProfile: "personal-demo-v1",
	}, nil)
	if err != nil || enabled == nil {
		t.Fatalf("approved demo profile should construct a manager, got %v, err=%v", enabled, err)
	}
}

func establishSessionCredential(t *testing.T, manager *session.Manager, subjectID, tenantID string, now time.Time) string {
	t.Helper()
	credential, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal: auth.Principal{
			TenantID: tenantID, SubjectID: subjectID, AuthenticationMethod: auth.AuthenticationMethodLocal,
		},
		Evidence:      session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: now},
		CorrelationID: "login-test",
	})
	if err != nil {
		t.Fatalf("establish session: %v", err)
	}
	return credential.Token
}

func assertSessionAuthenticationRejectedWithoutFallback(t *testing.T, manager *session.Manager, users userstore.Store, credential string) {
	t.Helper()
	legacy := &countingAuthenticator{}
	authenticator := newSessionCredentialAuthenticator(legacy, manager, users)
	request := httptest.NewRequest(http.MethodGet, "/v1/query", nil)
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential)
	if _, err := authenticator.Authenticate(request); err == nil {
		t.Fatal("expected session credential to be rejected")
	}
	if legacy.calls != 0 {
		t.Fatalf("failed session credential must not fall back to JWT, got %d calls", legacy.calls)
	}
}

type countingAuthenticator struct {
	calls int
}

func (a *countingAuthenticator) Authenticate(*http.Request) (auth.Principal, error) {
	a.calls++
	return auth.Principal{}, errors.New("legacy authentication invoked")
}

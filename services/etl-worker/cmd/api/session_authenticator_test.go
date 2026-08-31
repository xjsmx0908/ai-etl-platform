package main

import (
	"context"
	"encoding/json"
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
		Evidence:      session.AuthenticationEvidence{Assurance: "local-password", AuthenticatedAt: now},
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

func TestSessionCredentialAuthenticatorRequiresReauthenticationForStaleHighRiskRequest(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	clock := now
	users := newFakeUserStore()
	seedUser(t, users, "alice", "unused", "admin", "acme", true)
	user, _, _ := users.GetByUsername(context.Background(), "alice")
	manager, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return clock }))
	if err != nil {
		t.Fatalf("new session manager: %v", err)
	}
	credential := establishSessionCredential(t, manager, user.ID, user.TenantID, now)
	clock = now.Add(10 * time.Minute)
	request := httptest.NewRequest(http.MethodPost, "/v1/users/"+user.ID+"/external-identities", nil)
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential)
	recorder := httptest.NewRecorder()

	auth.Middleware(newSessionCredentialAuthenticator(&countingAuthenticator{}, manager, users))(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Fatal("stale high-risk request reached protected handler")
		}),
	).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("expected JSON response, got %q", contentType)
	}
	var response map[string]string
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["error"] != "reauthentication_required" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if response["action"] != identityBindingChangeAction {
		t.Fatalf("response action=%q, want %q", response["action"], identityBindingChangeAction)
	}
}

func TestSessionCredentialAuthenticatorClassifiesApprovedHighRiskOperations(t *testing.T) {
	tests := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/users/user-2/external-identities"},
		{http.MethodDelete, "/v1/users/user-2/external-identities/binding-1"},
		{http.MethodPost, "/v1/users"},
		{http.MethodPut, "/v1/users/user-2"},
		{http.MethodDelete, "/v1/users/user-2"},
		{http.MethodPost, "/v1/users/user-2/password"},
		{http.MethodPost, "/v1/tenants"},
		{http.MethodDelete, "/v1/documents/doc-1"},
		{http.MethodPatch, "/v1/documents/doc-1"},
		{http.MethodPost, "/v1/agent/runs/run-1/approve"},
		{http.MethodPost, "/v1/index-generations/rollback"},
	}
	for _, test := range tests {
		t.Run(test.method+" "+test.path, func(t *testing.T) {
			authenticator, credential := staleSessionAuthenticator(t)
			request := httptest.NewRequest(test.method, test.path, nil)
			request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential)
			if _, err := authenticator.Authenticate(request); !errors.Is(err, auth.ErrReauthenticationRequired) {
				t.Fatalf("expected reauthentication requirement, got %v", err)
			}
		})
	}
}

func TestSessionCredentialAuthenticatorAllowsFreshHighRiskAndStaleStandardRequests(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	clock := now
	users := newFakeUserStore()
	seedUser(t, users, "alice", "unused", "admin", "acme", true)
	user, _, _ := users.GetByUsername(context.Background(), "alice")
	manager, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return clock }))
	if err != nil {
		t.Fatalf("new session manager: %v", err)
	}
	credential := establishSessionCredential(t, manager, user.ID, user.TenantID, now)
	authenticator := newSessionCredentialAuthenticator(&countingAuthenticator{}, manager, users)

	fresh := httptest.NewRequest(http.MethodPost, "/v1/index-generations/rollback", nil)
	fresh.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential)
	if _, err := authenticator.Authenticate(fresh); err != nil {
		t.Fatalf("fresh high-risk request should authenticate: %v", err)
	}

	clock = now.Add(10 * time.Minute)
	standard := httptest.NewRequest(http.MethodPost, "/v1/query", nil)
	standard.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential)
	if _, err := authenticator.Authenticate(standard); err != nil {
		t.Fatalf("standard request must not require fresh high-risk evidence: %v", err)
	}

	nearMisses := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/v1/future-operation"},
		{http.MethodPost, "/v1/anything/external-identities"},
		{http.MethodPost, "/v1/users/user-2/external-identities/binding-1"},
		{http.MethodDelete, "/v1/users/user-2/external-identities"},
		{http.MethodPut, "/v1/users/user-2/unknown"},
		{http.MethodPost, "/v1/agent/runs/run-1/unknown/approve"},
		{http.MethodDelete, "/v1/documents/doc-1/chunks"},
	}
	for _, nearMiss := range nearMisses {
		unrecognized := httptest.NewRequest(nearMiss.method, nearMiss.path, nil)
		unrecognized.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential)
		if _, err := authenticator.Authenticate(unrecognized); err != nil {
			t.Fatalf("unrecognized route %s %s should retain standard risk: %v", nearMiss.method, nearMiss.path, err)
		}
	}
}

func TestSessionCredentialAuthenticatorKeepsLegacyJWTCompatibleOnHighRiskRoutes(t *testing.T) {
	legacy := &countingAuthenticator{principal: auth.Principal{
		TenantID: "acme", SubjectID: "user-1", Role: "admin", Capabilities: []string{auth.ScopeAdmin},
	}}
	authenticator := newSessionCredentialAuthenticator(legacy, nil, nil)
	request := httptest.NewRequest(http.MethodPost, "/v1/index-generations/rollback", nil)
	request.Header.Set("Authorization", "Bearer legacy.jwt.token")
	principal, err := authenticator.Authenticate(request)
	if err != nil || principal.SubjectID != "user-1" {
		t.Fatalf("legacy high-risk authentication failed: principal=%+v err=%v", principal, err)
	}
	if legacy.calls != 1 {
		t.Fatalf("legacy adapter calls=%d, want 1", legacy.calls)
	}
}

func TestSessionCredentialMiddlewareDoesNotMislabelInvalidCredentialAsReauthentication(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	manager, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatalf("new session manager: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/index-generations/rollback", nil)
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+"missing")
	recorder := httptest.NewRecorder()
	auth.Middleware(newSessionCredentialAuthenticator(&countingAuthenticator{}, manager, newFakeUserStore()))(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("invalid credential reached handler") }),
	).ServeHTTP(recorder, request)
	var response map[string]string
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if recorder.Code != http.StatusUnauthorized || response["error"] != "unauthorized" {
		t.Fatalf("invalid session must remain unauthorized, status=%d response=%+v", recorder.Code, response)
	}
}

func TestSessionCredentialMiddlewareChecksLiveAuthorityBeforeRequestingReauthentication(t *testing.T) {
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	clock := now
	users := newFakeUserStore()
	seedUser(t, users, "alice", "unused", "admin", "acme", true)
	user, _, _ := users.GetByUsername(context.Background(), "alice")
	manager, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return clock }))
	if err != nil {
		t.Fatalf("new session manager: %v", err)
	}
	credential := establishSessionCredential(t, manager, user.ID, user.TenantID, now)
	inactive := false
	if _, err := users.Update(context.Background(), user.ID, userstore.UserPatch{Active: &inactive}); err != nil {
		t.Fatalf("deactivate user: %v", err)
	}
	clock = now.Add(10 * time.Minute)
	request := httptest.NewRequest(http.MethodPost, "/v1/index-generations/rollback", nil)
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential)
	recorder := httptest.NewRecorder()
	auth.Middleware(newSessionCredentialAuthenticator(&countingAuthenticator{}, manager, users))(
		http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("inactive user reached handler") }),
	).ServeHTTP(recorder, request)
	var response map[string]string
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if recorder.Code != http.StatusUnauthorized || response["error"] != "unauthorized" {
		t.Fatalf("inactive user must not receive reauthentication challenge, status=%d response=%+v", recorder.Code, response)
	}
}

func staleSessionAuthenticator(t *testing.T) (auth.Authenticator, string) {
	t.Helper()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	clock := now
	users := newFakeUserStore()
	seedUser(t, users, "alice", "unused", "admin", "acme", true)
	user, _, _ := users.GetByUsername(context.Background(), "alice")
	manager, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return clock }))
	if err != nil {
		t.Fatalf("new session manager: %v", err)
	}
	credential := establishSessionCredential(t, manager, user.ID, user.TenantID, now)
	clock = now.Add(10 * time.Minute)
	return newSessionCredentialAuthenticator(&countingAuthenticator{}, manager, users), credential
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
			TenantID: tenantID, SubjectID: subjectID, AuthenticationMethod: auth.AuthenticationMethodFederated,
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
	calls     int
	principal auth.Principal
}

func (a *countingAuthenticator) Authenticate(*http.Request) (auth.Principal, error) {
	a.calls++
	if a.principal.SubjectID != "" {
		return a.principal, nil
	}
	return auth.Principal{}, errors.New("legacy authentication invoked")
}

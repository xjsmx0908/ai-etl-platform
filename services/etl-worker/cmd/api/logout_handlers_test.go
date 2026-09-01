package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"ai-etl-pipeline/internal/audit"
	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/oidcauth"
	"ai-etl-pipeline/internal/session"
)

func TestLogoutRevokesCurrentPlatformSessionBeforeCompleting(t *testing.T) {
	now := time.Date(2026, 9, 1, 2, 0, 0, 0, time.UTC)
	manager, err := session.New(
		session.NewMemoryStore(), platformSessionPolicy(3), session.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal: auth.Principal{
			TenantID: "acme", SubjectID: "user-1", AuthenticationMethod: auth.AuthenticationMethodLocal,
		},
		Evidence: session.AuthenticationEvidence{
			Assurance: session.AssuranceLocalPassword, AuthenticatedAt: now,
		},
		CorrelationID: "password-login:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	audits := newFakeAuditStore()
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential.Token)
	recorder := httptest.NewRecorder()

	handleLogout(manager, nil, audits).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	result, err := manager.Authenticate(context.Background(), credential.Token, platformSessionRequestAction)
	if err != nil || result.Decision != session.DecisionDeny {
		t.Fatalf("revoked credential result=%+v err=%v", result, err)
	}
	if len(audits.entries) != 0 {
		t.Fatalf("success audit must be committed by the session store, got: %+v", audits.entries)
	}
}

func TestLogoutFailsClosedAndAuditsWhenSessionStoreIsUnavailable(t *testing.T) {
	manager, err := session.New(session.NewPostgresStore(nil), platformSessionPolicy(3))
	if err != nil {
		t.Fatal(err)
	}
	audits := newFakeAuditStore()
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+"opaque-token")
	recorder := httptest.NewRecorder()

	handleLogout(manager, nil, audits).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(audits.entries) != 1 || audits.entries[0].Result != "failure" ||
		audits.entries[0].Detail["reason"] != "session_store_unavailable" {
		t.Fatalf("unexpected audits: %+v", audits.entries)
	}
	encoded, _ := json.Marshal(audits.entries[0])
	if strings.Contains(string(encoded), "opaque-token") {
		t.Fatalf("audit leaked credential: %s", encoded)
	}
}

func TestLogoutDoesNotDependOnTheFailureAuditProjection(t *testing.T) {
	now := time.Date(2026, 9, 1, 2, 10, 0, 0, time.UTC)
	manager, err := session.New(
		session.NewMemoryStore(), platformSessionPolicy(3), session.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal: auth.Principal{
			TenantID: "acme", SubjectID: "user-1", AuthenticationMethod: auth.AuthenticationMethodLocal,
		}, Evidence: session.AuthenticationEvidence{
			Assurance: session.AssuranceLocalPassword, AuthenticatedAt: now,
		}, CorrelationID: "password-login:audit-failure",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential.Token)
	recorder := httptest.NewRecorder()

	handleLogout(manager, nil, failingLogoutAuditStore{}).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	result, authenticateErr := manager.Authenticate(context.Background(), credential.Token, platformSessionRequestAction)
	if authenticateErr != nil || result.Decision != session.DecisionDeny {
		t.Fatalf("audit projection failure changed revocation: result=%+v err=%v", result, authenticateErr)
	}
}

type failingLogoutAuditStore struct{}

func (failingLogoutAuditStore) Record(context.Context, audit.Entry) error {
	return errors.New("audit unavailable")
}

func (failingLogoutAuditStore) List(context.Context, audit.ListQuery) ([]audit.Entry, int, error) {
	return nil, 0, errors.New("audit unavailable")
}

func TestLogoutIsIdempotentAndKeepsLegacyJWTCookieCompatibility(t *testing.T) {
	now := time.Date(2026, 9, 1, 2, 15, 0, 0, time.UTC)
	manager, err := session.New(
		session.NewMemoryStore(), platformSessionPolicy(3), session.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal:     auth.Principal{TenantID: "acme", SubjectID: "user-1", AuthenticationMethod: auth.AuthenticationMethodFederated},
		Evidence:      session.AuthenticationEvidence{Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now},
		CorrelationID: "oidc-login:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := handleLogout(manager, nil, nil)
	var wait sync.WaitGroup
	statuses := make(chan int, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			request := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
			request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential.Token)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			statuses <- recorder.Code
		}()
	}
	wait.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusNoContent {
			t.Fatalf("concurrent status=%d", status)
		}
	}

	for _, authorization := range []string{"", "Bearer legacy.jwt.token"} {
		request := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
		request.Header.Set("Authorization", authorization)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("authorization=%q status=%d", authorization, recorder.Code)
		}
	}
}

func TestFederatedLogoutReturnsProviderRedirectOnlyAfterLocalRevocation(t *testing.T) {
	now := time.Date(2026, 9, 1, 2, 30, 0, 0, time.UTC)
	manager, err := session.New(
		session.NewMemoryStore(), platformSessionPolicy(3), session.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal:     auth.Principal{TenantID: "acme", SubjectID: "user-1", AuthenticationMethod: auth.AuthenticationMethodFederated},
		Evidence:      session.AuthenticationEvidence{Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now},
		CorrelationID: "oidc-login:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	flow := logoutTestFlow(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/logout?return_to=/documents", nil)
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential.Token)
	recorder := httptest.NewRecorder()

	handleLogout(manager, flow, nil).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		AuthorizationURL string `json:"authorization_url"`
		State            string `json:"state"`
		ExpiresIn        int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.AuthorizationURL, "/logout?") || response.State == "" || response.ExpiresIn != 300 {
		t.Fatalf("unexpected response: %+v", response)
	}
	result, err := manager.Authenticate(context.Background(), credential.Token, platformSessionRequestAction)
	if err != nil || result.Decision != session.DecisionDeny {
		t.Fatalf("local session was not revoked: result=%+v err=%v", result, err)
	}
}

func TestFederatedLogoutCompletesLocallyWhenProviderTransactionStoreFails(t *testing.T) {
	redisAddress := os.Getenv("OIDC_REDIS_TEST_ADDR")
	if redisAddress == "" {
		t.Skip("OIDC_REDIS_TEST_ADDR is not set")
	}
	now := time.Date(2026, 9, 1, 2, 45, 0, 0, time.UTC)
	manager, err := session.New(
		session.NewMemoryStore(), platformSessionPolicy(3), session.WithClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := manager.Establish(context.Background(), session.EstablishCommand{
		Principal: auth.Principal{
			TenantID: "acme", SubjectID: "deactivated-user", AuthenticationMethod: auth.AuthenticationMethodFederated,
		}, Evidence: session.AuthenticationEvidence{
			Assurance: session.AssuranceDemoMFA, AuthenticatedAt: now,
		}, CorrelationID: "oidc-login-before-deactivation",
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := oidcauth.NewRedisTransactionStore(
		redisAddress, "", 0, "handler-logout-unavailable-"+time.Now().Format("20060102150405.000000000"),
	)
	if err != nil {
		t.Fatal(err)
	}
	flow := logoutTestFlowWithStore(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/logout", nil)
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential.Token)
	recorder := httptest.NewRecorder()

	handleLogout(manager, flow, nil).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	result, authenticateErr := manager.Authenticate(context.Background(), credential.Token, platformSessionRequestAction)
	if authenticateErr != nil || result.Decision != session.DecisionDeny {
		t.Fatalf("local session survived provider failure: result=%+v err=%v", result, authenticateErr)
	}
}

func TestLogoutCallbackConsumesStateOnceAndReturnsSafePath(t *testing.T) {
	flow := logoutTestFlow(t)
	start, err := flow.StartLogout(context.Background(), `//evil.example/steal`)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"state": start.State, "cookie_state": start.State})
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/logout/callback", strings.NewReader(string(body)))
	recorder := httptest.NewRecorder()
	handleLogoutCallback(flow).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"return_to":"/"`) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	replay := httptest.NewRequest(http.MethodPost, "/v1/auth/logout/callback", strings.NewReader(string(body)))
	replayRecorder := httptest.NewRecorder()
	handleLogoutCallback(flow).ServeHTTP(replayRecorder, replay)
	if replayRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("replay status=%d body=%s", replayRecorder.Code, replayRecorder.Body.String())
	}
}

func logoutTestFlow(t *testing.T) *oidcauth.Flow {
	return logoutTestFlowWithStore(t, oidcauth.NewMemoryTransactionStore())
}

func logoutTestFlowWithStore(t *testing.T, store oidcauth.TransactionStore) *oidcauth.Flow {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeOIDCTestJSON(t, w, map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
				"end_session_endpoint": issuer + "/logout",
			})
		case "/jwks":
			writeOIDCTestJSON(t, w, map[string]any{"keys": []any{oidcRSAJWK("logout-key", &key.PublicKey)}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(idp.Close)
	issuer = idp.URL
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback",
		LogoutRedirectURI: "https://rag.example.com/api/auth/logout/callback", HTTPClient: idp.Client(),
	}, oidcDirectoryStub{userID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	return oidcauth.NewFlow(authenticator, store, 5*time.Minute)
}

package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/pashagolub/pgxmock/v5"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/oidcauth"
	"ai-etl-pipeline/internal/session"
	"ai-etl-pipeline/internal/userstore"
)

func TestHandleReauthenticationStartBindsStaleFederatedSessionAndHighRiskAction(t *testing.T) {
	now := time.Date(2026, 8, 31, 17, 0, 0, 0, time.UTC)
	clock := now
	users := newFakeUserStore()
	seedUser(t, users, "alice", "unused", userstore.RoleAdmin, "acme", true)
	user, _, _ := users.GetByUsername(context.Background(), "alice")
	sessions, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return clock }))
	if err != nil {
		t.Fatalf("create session manager: %v", err)
	}
	credential, err := sessions.Establish(context.Background(), session.EstablishCommand{
		Principal: auth.Principal{
			TenantID: user.TenantID, SubjectID: user.ID,
			AuthenticationMethod: auth.AuthenticationMethodFederated,
		},
		Evidence:      session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: now},
		CorrelationID: "login-1",
	})
	if err != nil {
		t.Fatalf("establish session: %v", err)
	}
	flow := reauthenticationStartFlow(t, user.ID)
	audits := newFakeAuditStore()
	clock = now.Add(10 * time.Minute)
	body, _ := json.Marshal(map[string]string{
		"action": identityBindingChangeAction, "return_to": "/users?step=confirm",
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/reauth/start", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential.Token)
	recorder := httptest.NewRecorder()

	auth.Middleware(newSessionCredentialAuthenticator(&countingAuthenticator{}, sessions, users))(
		handleReauthenticationStart(flow, sessions, users, audits),
	).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		AuthorizationURL string `json:"authorization_url"`
		State            string `json:"state"`
		ExpiresIn        int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	authorizationURL, err := url.Parse(response.AuthorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	query := authorizationURL.Query()
	if response.State == "" || response.ExpiresIn != 300 || query.Get("state") != response.State ||
		query.Get("prompt") != "login" || query.Get("max_age") != "0" || query.Get("acr_values") != "2" {
		t.Fatalf("unexpected reauthentication response: %+v", response)
	}
	if len(audits.entries) != 1 || audits.entries[0].Detail["correlation_id"] != reauthenticationCorrelationID(response.State) {
		t.Fatalf("missing state-digest audit correlation: %+v", audits.entries)
	}
	if strings.Contains(audits.entries[0].Detail["correlation_id"].(string), response.State) {
		t.Fatal("audit correlation leaked raw state")
	}
}

func TestHandleReauthenticationCallbackAtomicallyRotatesCredentialAndRejectsReplay(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	clock := base
	users := newFakeUserStore()
	seedUser(t, users, "alice", "unused", userstore.RoleAdmin, "acme", true)
	user, _, _ := users.GetByUsername(context.Background(), "alice")
	sessions, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return clock }))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := sessions.Establish(context.Background(), session.EstablishCommand{
		Principal: auth.Principal{TenantID: user.TenantID, SubjectID: user.ID, AuthenticationMethod: auth.AuthenticationMethodFederated},
		Evidence:  session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: base}, CorrelationID: "login-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var nonce string
	flow := reauthenticationCompletionFlow(t, user.ID, base.Add(10*time.Minute), &nonce)
	clock = base.Add(10 * time.Minute)
	start, err := flow.StartReauthentication(context.Background(), oidcauth.ReauthenticationStartCommand{
		Credential: credential.Token, Action: identityBindingChangeAction, ReturnTo: "/users?step=confirm",
		Principal: auth.Principal{TenantID: user.TenantID, SubjectID: user.ID, AuthenticationMethod: auth.AuthenticationMethodFederated},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(start.AuthorizationURL)
	nonce = parsed.Query().Get("nonce")
	body, _ := json.Marshal(map[string]string{"code": "code-1", "state": start.State, "cookie_state": start.State})
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/reauth/callback", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential.Token)
	recorder := httptest.NewRecorder()
	audits := newFakeAuditStore()
	handleReauthenticationCallback(flow, sessions, users, audits).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Token     string    `json:"token"`
		Action    string    `json:"action"`
		ReturnTo  string    `json:"return_to"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(response.Token, platformSessionCredentialPrefix) || response.Action != identityBindingChangeAction || response.ReturnTo != "/users?step=confirm" {
		t.Fatalf("unexpected response: %+v", response)
	}
	if len(audits.entries) != 1 || audits.entries[0].Detail["correlation_id"] != reauthenticationCorrelationID(start.State) {
		t.Fatalf("missing completion audit correlation: %+v", audits.entries)
	}
	oldResult, _ := sessions.Authenticate(context.Background(), credential.Token, platformSessionRequestAction)
	newResult, newErr := sessions.Authenticate(context.Background(), strings.TrimPrefix(response.Token, platformSessionCredentialPrefix), identityBindingChangeAction)
	if oldResult.Decision != session.DecisionDeny || newErr != nil || newResult.Decision != session.DecisionAllow {
		t.Fatalf("rotation failed: old=%+v new=%+v err=%v", oldResult, newResult, newErr)
	}
	replay := httptest.NewRequest(http.MethodPost, "/v1/auth/reauth/callback", bytes.NewReader(body))
	replay.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential.Token)
	replayRecorder := httptest.NewRecorder()
	handleReauthenticationCallback(flow, sessions, users, nil).ServeHTTP(replayRecorder, replay)
	if replayRecorder.Code == http.StatusOK {
		t.Fatal("expected replay to fail")
	}
}

func TestReauthenticationCallbackFailsClosedForStateCredentialAndProviderDrift(t *testing.T) {
	for _, test := range []struct {
		name               string
		callbackState      string
		callbackCredential string
		code               string
	}{
		{name: "state drift", callbackState: "different-state", code: "code-1"},
		{name: "credential drift", callbackCredential: "different-credential", code: "code-1"},
		{name: "identity provider unavailable", code: "provider-unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			flow, sessions, users, credential, start := prepareReauthenticationCallback(t, "/users")
			state := start.State
			if test.callbackState != "" {
				state = test.callbackState
			}
			callbackCredential := credential.Token
			if test.callbackCredential != "" {
				callbackCredential = test.callbackCredential
			}
			body, _ := json.Marshal(map[string]string{"code": test.code, "state": state, "cookie_state": start.State})
			request := httptest.NewRequest(http.MethodPost, "/v1/auth/reauth/callback", bytes.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+callbackCredential)
			recorder := httptest.NewRecorder()
			handleReauthenticationCallback(flow, sessions, users, nil).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusUnauthorized || strings.Contains(recorder.Body.String(), platformSessionCredentialPrefix) {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			result, err := sessions.Authenticate(context.Background(), credential.Token, platformSessionRequestAction)
			if err != nil || result.Decision != session.DecisionAllow {
				t.Fatalf("failed callback changed current credential: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestReauthenticationCallbackNormalizesUnsafeReturnPath(t *testing.T) {
	flow, sessions, users, credential, start := prepareReauthenticationCallback(t, "//attacker.example/steal")
	body, _ := json.Marshal(map[string]string{"code": "code-1", "state": start.State, "cookie_state": start.State})
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/reauth/callback", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential.Token)
	recorder := httptest.NewRecorder()
	handleReauthenticationCallback(flow, sessions, users, nil).ServeHTTP(recorder, request)
	var response reauthenticationCallbackResponse
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil || response.ReturnTo != "/" {
		t.Fatalf("unsafe return path escaped normalization: %+v err=%v", response, err)
	}
}

func TestReauthenticationCallbackFailsClosedWhenUserOrSessionStoreIsUnavailable(t *testing.T) {
	t.Run("user store", func(t *testing.T) {
		flow, sessions, users, credential, start := prepareReauthenticationCallback(t, "/users")
		users.getByIDErr = errors.New("postgres unavailable")
		body, _ := json.Marshal(map[string]string{"code": "code-1", "state": start.State, "cookie_state": start.State})
		request := httptest.NewRequest(http.MethodPost, "/v1/auth/reauth/callback", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential.Token)
		recorder := httptest.NewRecorder()
		handleReauthenticationCallback(flow, sessions, users, nil).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), platformSessionCredentialPrefix) {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("session store rotation", func(t *testing.T) {
		base := time.Now().UTC().Truncate(time.Second)
		users := newFakeUserStore()
		seedUser(t, users, "alice", "unused", userstore.RoleAdmin, "acme", true)
		user, _, _ := users.GetByUsername(context.Background(), "alice")
		const credential = "opaque-current"
		var nonce string
		flow := reauthenticationCompletionFlow(t, user.ID, base, &nonce)
		start, err := flow.StartReauthentication(context.Background(), oidcauth.ReauthenticationStartCommand{
			Credential: credential, Action: identityBindingChangeAction, ReturnTo: "/users",
			Principal: auth.Principal{TenantID: user.TenantID, SubjectID: user.ID, AuthenticationMethod: auth.AuthenticationMethodFederated},
		})
		if err != nil {
			t.Fatal(err)
		}
		parsed, _ := url.Parse(start.AuthorizationURL)
		nonce = parsed.Query().Get("nonce")
		mock, err := pgxmock.NewPool()
		if err != nil {
			t.Fatal(err)
		}
		defer mock.Close()
		mock.ExpectQuery("SELECT id,tenant_id").WithArgs(pgxmock.AnyArg()).WillReturnRows(
			pgxmock.NewRows([]string{"id", "tenant_id", "internal_user_id", "authentication_method", "assurance_level", "authenticated_at", "created_at", "last_activity_at", "absolute_expires_at", "revoked_at", "generation", "policy_revision", "established_correlation_id"}).
				AddRow("session-1", user.TenantID, user.ID, "federated", "demo-mfa", base.Add(-time.Minute), base.Add(-time.Hour), base, base.Add(time.Hour), nil, int64(1), platformSessionPolicy().Revision, "login-1"),
		)
		mock.ExpectBegin().WillReturnError(errors.New("postgres unavailable"))
		sessions, err := session.New(session.NewPostgresStore(mock), platformSessionPolicy(), session.WithClock(func() time.Time { return base }))
		if err != nil {
			t.Fatal(err)
		}
		body, _ := json.Marshal(map[string]string{"code": "code-1", "state": start.State, "cookie_state": start.State})
		request := httptest.NewRequest(http.MethodPost, "/v1/auth/reauth/callback", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential)
		recorder := httptest.NewRecorder()
		handleReauthenticationCallback(flow, sessions, users, nil).ServeHTTP(recorder, request)
		if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), platformSessionCredentialPrefix) {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestReauthenticationCallbackFailsClosedWhenRedisBecomesUnavailable(t *testing.T) {
	redisAddress := os.Getenv("OIDC_REDIS_TEST_ADDR")
	if redisAddress == "" {
		t.Skip("OIDC_REDIS_TEST_ADDR is not set")
	}
	base := time.Now().UTC().Truncate(time.Second)
	users := newFakeUserStore()
	seedUser(t, users, "alice", "unused", userstore.RoleAdmin, "acme", true)
	user, _, _ := users.GetByUsername(context.Background(), "alice")
	sessions, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return base }))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := sessions.Establish(context.Background(), session.EstablishCommand{
		Principal: auth.Principal{TenantID: user.TenantID, SubjectID: user.ID, AuthenticationMethod: auth.AuthenticationMethodFederated},
		Evidence:  session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: base.Add(-time.Minute)}, CorrelationID: "login-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := oidcauth.NewRedisTransactionStore(redisAddress, "", 0, "handler-unavailable")
	if err != nil {
		t.Fatal(err)
	}
	var nonce string
	flow := reauthenticationCompletionFlowWithStore(t, user.ID, base, &nonce, store)
	start, err := flow.StartReauthentication(context.Background(), oidcauth.ReauthenticationStartCommand{
		Credential: credential.Token, Action: identityBindingChangeAction, ReturnTo: "/users",
		Principal: auth.Principal{TenantID: user.TenantID, SubjectID: user.ID, AuthenticationMethod: auth.AuthenticationMethodFederated},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"code": "code-1", "state": start.State, "cookie_state": start.State})
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/reauth/callback", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential.Token)
	recorder := httptest.NewRecorder()
	handleReauthenticationCallback(flow, sessions, users, nil).ServeHTTP(recorder, request)
	if recorder.Code == http.StatusOK || strings.Contains(recorder.Body.String(), platformSessionCredentialPrefix) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHandleReauthenticationStartRejectsIneligibleRequests(t *testing.T) {
	base := time.Date(2026, 8, 31, 17, 0, 0, 0, time.UTC)
	tests := []struct {
		name, authorization, action string
		method                      auth.AuthenticationMethod
		stale, active               bool
		wantStatus                  int
	}{
		{name: "JWT credential", authorization: "Bearer header.payload.signature", action: identityBindingChangeAction, method: auth.AuthenticationMethodFederated, stale: true, active: true, wantStatus: http.StatusUnauthorized},
		{name: "fresh session", action: identityBindingChangeAction, method: auth.AuthenticationMethodFederated, active: true, wantStatus: http.StatusConflict},
		{name: "local session", action: identityBindingChangeAction, method: auth.AuthenticationMethodLocal, stale: true, active: true, wantStatus: http.StatusConflict},
		{name: "standard action", action: platformSessionRequestAction, method: auth.AuthenticationMethodFederated, stale: true, active: true, wantStatus: http.StatusBadRequest},
		{name: "unknown action", action: "unknown.change", method: auth.AuthenticationMethodFederated, stale: true, active: true, wantStatus: http.StatusBadRequest},
		{name: "inactive user", action: identityBindingChangeAction, method: auth.AuthenticationMethodFederated, stale: true, wantStatus: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clock := base
			users := newFakeUserStore()
			seedUser(t, users, "alice", "unused", userstore.RoleAdmin, "acme", test.active)
			user, _, _ := users.GetByUsername(context.Background(), "alice")
			sessions, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return clock }))
			if err != nil {
				t.Fatal(err)
			}
			assurance := "demo-mfa"
			if test.method == auth.AuthenticationMethodLocal {
				assurance = "local-password"
			}
			credential, err := sessions.Establish(context.Background(), session.EstablishCommand{
				Principal: auth.Principal{TenantID: user.TenantID, SubjectID: user.ID, AuthenticationMethod: test.method},
				Evidence:  session.AuthenticationEvidence{Assurance: assurance, AuthenticatedAt: base}, CorrelationID: "login-1",
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.stale {
				clock = base.Add(10 * time.Minute)
			}
			authorization := test.authorization
			if authorization == "" {
				authorization = "Bearer " + platformSessionCredentialPrefix + credential.Token
			}
			body, _ := json.Marshal(map[string]string{"action": test.action, "return_to": "/users"})
			request := httptest.NewRequest(http.MethodPost, "/v1/auth/reauth/start", bytes.NewReader(body))
			request.Header.Set("Authorization", authorization)
			recorder := httptest.NewRecorder()
			handleReauthenticationStart(reauthenticationStartFlow(t, user.ID), sessions, users, nil).ServeHTTP(recorder, request)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if strings.Contains(recorder.Body.String(), credential.Token) {
				t.Fatal("response leaked session credential")
			}
		})
	}
}

func TestConcurrentReauthenticationCallbacksIssueExactlyOneReplacement(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	clock := base
	users := newFakeUserStore()
	seedUser(t, users, "alice", "unused", userstore.RoleAdmin, "acme", true)
	user, _, _ := users.GetByUsername(context.Background(), "alice")
	sessions, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return clock }))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := sessions.Establish(context.Background(), session.EstablishCommand{
		Principal: auth.Principal{TenantID: user.TenantID, SubjectID: user.ID, AuthenticationMethod: auth.AuthenticationMethodFederated},
		Evidence:  session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: base}, CorrelationID: "login-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var nonce string
	flow := reauthenticationCompletionFlow(t, user.ID, base.Add(10*time.Minute), &nonce)
	clock = base.Add(10 * time.Minute)
	start, err := flow.StartReauthentication(context.Background(), oidcauth.ReauthenticationStartCommand{
		Credential: credential.Token, Action: identityBindingChangeAction, ReturnTo: "/users",
		Principal: auth.Principal{TenantID: user.TenantID, SubjectID: user.ID, AuthenticationMethod: auth.AuthenticationMethodFederated},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(start.AuthorizationURL)
	nonce = parsed.Query().Get("nonce")
	body, _ := json.Marshal(map[string]string{"code": "code-1", "state": start.State, "cookie_state": start.State})
	statuses := make(chan int, 2)
	for range 2 {
		go func() {
			request := httptest.NewRequest(http.MethodPost, "/v1/auth/reauth/callback", bytes.NewReader(body))
			request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential.Token)
			recorder := httptest.NewRecorder()
			handleReauthenticationCallback(flow, sessions, users, nil).ServeHTTP(recorder, request)
			statuses <- recorder.Code
		}()
	}
	successes := 0
	for range 2 {
		if <-statuses == http.StatusOK {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("successful callbacks=%d, want 1", successes)
	}
}

func TestReauthenticationCallbackDoesNotRotateAfterUserDeactivation(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second)
	clock := base
	users := newFakeUserStore()
	seedUser(t, users, "alice", "unused", userstore.RoleAdmin, "acme", true)
	user, _, _ := users.GetByUsername(context.Background(), "alice")
	sessions, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return clock }))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := sessions.Establish(context.Background(), session.EstablishCommand{
		Principal: auth.Principal{TenantID: user.TenantID, SubjectID: user.ID, AuthenticationMethod: auth.AuthenticationMethodFederated},
		Evidence:  session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: base}, CorrelationID: "login-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var nonce string
	flow := reauthenticationCompletionFlow(t, user.ID, base.Add(10*time.Minute), &nonce)
	clock = base.Add(10 * time.Minute)
	start, err := flow.StartReauthentication(context.Background(), oidcauth.ReauthenticationStartCommand{
		Credential: credential.Token, Action: identityBindingChangeAction, ReturnTo: "/users",
		Principal: auth.Principal{TenantID: user.TenantID, SubjectID: user.ID, AuthenticationMethod: auth.AuthenticationMethodFederated},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(start.AuthorizationURL)
	nonce = parsed.Query().Get("nonce")
	inactive := false
	if _, err := users.Update(context.Background(), user.ID, userstore.UserPatch{Active: &inactive}); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"code": "code-1", "state": start.State, "cookie_state": start.State})
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/reauth/callback", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential.Token)
	recorder := httptest.NewRecorder()
	handleReauthenticationCallback(flow, sessions, users, nil).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || strings.Contains(recorder.Body.String(), platformSessionCredentialPrefix) {
		t.Fatalf("unexpected callback response: %d %s", recorder.Code, recorder.Body.String())
	}
	active := true
	if _, err := users.Update(context.Background(), user.ID, userstore.UserPatch{Active: &active}); err != nil {
		t.Fatal(err)
	}
	oldResult, oldErr := sessions.Authenticate(context.Background(), credential.Token, platformSessionRequestAction)
	if oldErr != nil || oldResult.Decision != session.DecisionAllow {
		t.Fatalf("failed callback changed old credential: result=%+v err=%v", oldResult, oldErr)
	}
}

func reauthenticationStartFlow(t *testing.T, userID string) *oidcauth.Flow {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	var issuer string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeOIDCTestJSON(t, w, map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
			})
		case "/jwks":
			writeOIDCTestJSON(t, w, map[string]any{"keys": []any{oidcRSAJWK("reauth-start-key", &key.PublicKey)}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(idp.Close)
	issuer = idp.URL
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback",
		HTTPClient: idp.Client(),
	}, oidcDirectoryStub{userID: userID})
	if err != nil {
		t.Fatalf("create OIDC authenticator: %v", err)
	}
	return oidcauth.NewFlow(authenticator, oidcauth.NewMemoryTransactionStore(), 5*time.Minute)
}

func reauthenticationCompletionFlow(t *testing.T, userID string, now time.Time, nonce *string) *oidcauth.Flow {
	return reauthenticationCompletionFlowWithStore(t, userID, now, nonce, oidcauth.NewMemoryTransactionStore())
}

func reauthenticationCompletionFlowWithStore(t *testing.T, userID string, now time.Time, nonce *string, store oidcauth.TransactionStore) *oidcauth.Flow {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeOIDCTestJSON(t, w, map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks"})
		case "/jwks":
			writeOIDCTestJSON(t, w, map[string]any{"keys": []any{oidcRSAJWK("reauth-callback-key", &key.PublicKey)}})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if r.Form.Get("code") == "provider-unavailable" {
				http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
				return
			}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
				"iss": issuer, "sub": "external-alice", "aud": "rag-web", "exp": now.Add(5 * time.Minute).Unix(),
				"iat": now.Unix(), "nonce": *nonce, "acr": "2", "amr": []string{"pwd", "otp"}, "auth_time": now.Add(-time.Minute).Unix(),
			})
			token.Header["kid"] = "reauth-callback-key"
			signed, signErr := token.SignedString(key)
			if signErr != nil {
				t.Fatal(signErr)
			}
			writeOIDCTestJSON(t, w, map[string]any{"id_token": signed})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(idp.Close)
	issuer = idp.URL
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback", HTTPClient: idp.Client(), Clock: func() time.Time { return now },
	}, oidcDirectoryStub{userID: userID})
	if err != nil {
		t.Fatal(err)
	}
	return oidcauth.NewFlow(authenticator, store, 5*time.Minute)
}

func prepareReauthenticationCallback(t *testing.T, returnTo string) (*oidcauth.Flow, *session.Manager, *fakeUserStore, session.Credential, oidcauth.BrowserStart) {
	t.Helper()
	base := time.Now().UTC().Truncate(time.Second)
	clock := base
	users := newFakeUserStore()
	seedUser(t, users, "alice", "unused", userstore.RoleAdmin, "acme", true)
	user, _, _ := users.GetByUsername(context.Background(), "alice")
	sessions, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return clock }))
	if err != nil {
		t.Fatal(err)
	}
	credential, err := sessions.Establish(context.Background(), session.EstablishCommand{
		Principal: auth.Principal{TenantID: user.TenantID, SubjectID: user.ID, AuthenticationMethod: auth.AuthenticationMethodFederated},
		Evidence:  session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: base}, CorrelationID: "login-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	clock = base.Add(10 * time.Minute)
	var nonce string
	flow := reauthenticationCompletionFlow(t, user.ID, clock, &nonce)
	start, err := flow.StartReauthentication(context.Background(), oidcauth.ReauthenticationStartCommand{
		Credential: credential.Token, Action: identityBindingChangeAction, ReturnTo: returnTo,
		Principal: auth.Principal{TenantID: user.TenantID, SubjectID: user.ID, AuthenticationMethod: auth.AuthenticationMethodFederated},
	})
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(start.AuthorizationURL)
	nonce = parsed.Query().Get("nonce")
	return flow, sessions, users, credential, start
}

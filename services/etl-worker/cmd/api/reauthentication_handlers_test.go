package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

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
	clock = now.Add(10 * time.Minute)
	body, _ := json.Marshal(map[string]string{
		"action": identityBindingChangeAction, "return_to": "/users?step=confirm",
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/reauth/start", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+platformSessionCredentialPrefix+credential.Token)
	recorder := httptest.NewRecorder()

	auth.Middleware(newSessionCredentialAuthenticator(&countingAuthenticator{}, sessions, users))(
		handleReauthenticationStart(flow, sessions, users, nil),
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
	handleReauthenticationCallback(flow, sessions, users, nil).ServeHTTP(recorder, request)
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
			credential, err := sessions.Establish(context.Background(), session.EstablishCommand{
				Principal: auth.Principal{TenantID: user.TenantID, SubjectID: user.ID, AuthenticationMethod: test.method},
				Evidence:  session.AuthenticationEvidence{Assurance: "demo-mfa", AuthenticatedAt: base}, CorrelationID: "login-1",
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
	return oidcauth.NewFlow(authenticator, oidcauth.NewMemoryTransactionStore(), 5*time.Minute)
}

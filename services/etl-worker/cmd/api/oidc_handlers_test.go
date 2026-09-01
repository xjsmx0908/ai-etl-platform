package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/externalidentity"
	"ai-etl-pipeline/internal/oidcauth"
	"ai-etl-pipeline/internal/session"
	"ai-etl-pipeline/internal/userstore"
)

type oidcDirectoryStub struct{ userID string }

func (d oidcDirectoryStub) Resolve(context.Context, externalidentity.ExternalIdentity) (auth.Principal, error) {
	return auth.Principal{
		TenantID: "acme", SubjectID: d.userID, Role: userstore.RoleAdmin,
		AuthenticationMethod: auth.AuthenticationMethodFederated,
		Capabilities:         auth.ScopesForRole(userstore.RoleAdmin),
	}, nil
}

func TestHandleOIDCCallbackIssuesFederatedSessionAndRejectsReplay(t *testing.T) {
	users := newFakeUserStore()
	seedUser(t, users, "alice", "unused-password", userstore.RoleAdmin, "acme", true)
	user, found, err := users.GetByUsername(context.Background(), "alice")
	if err != nil || !found {
		t.Fatalf("load test user: found=%v err=%v", found, err)
	}

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	const keyID = "callback-key"
	var issuer, nonce string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeOIDCTestJSON(t, w, map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
			})
		case "/jwks":
			writeOIDCTestJSON(t, w, map[string]any{"keys": []any{oidcRSAJWK(keyID, &key.PublicKey)}})
		case "/token":
			now := time.Now()
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
				"iss": issuer, "sub": "external-alice", "aud": "rag-web",
				"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "nonce": nonce,
			})
			token.Header["kid"] = keyID
			signed, signErr := token.SignedString(key)
			if signErr != nil {
				t.Fatalf("sign token: %v", signErr)
			}
			writeOIDCTestJSON(t, w, map[string]any{"id_token": signed})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL

	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback",
		HTTPClient: idp.Client(),
	}, oidcDirectoryStub{userID: user.ID})
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	flow := oidcauth.NewFlow(authenticator, oidcauth.NewMemoryTransactionStore(), 5*time.Minute)
	start, err := flow.Start(context.Background(), `/\evil.example/steal`)
	if err != nil {
		t.Fatalf("start flow: %v", err)
	}
	authorizationURL, err := url.Parse(start.AuthorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	nonce = authorizationURL.Query().Get("nonce")

	cfg := testAuthConfig()
	body, _ := json.Marshal(oidcCallbackRequest{State: start.State, CookieState: start.State, Code: "code-1"})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/oidc/callback", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	handleOIDCCallback(cfg, flow, users, nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response oidcCallbackResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.User.ID != user.ID || response.User.TenantID != "acme" || response.User.Role != userstore.RoleAdmin {
		t.Fatalf("unexpected internal user: %+v", response.User)
	}
	if response.ReturnTo != "/" {
		t.Fatalf("unsafe return path survived: %q", response.ReturnTo)
	}
	verifyRequest := httptest.NewRequest(http.MethodGet, "/", nil)
	verifyRequest.Header.Set("Authorization", "Bearer "+response.Token)
	claims, err := auth.NewVerifier(cfg.JWTSecret).Verify(verifyRequest)
	if err != nil {
		t.Fatalf("verify platform session: %v", err)
	}
	if claims.AuthenticationMethod != auth.AuthenticationMethodFederated || claims.UserID != user.ID {
		t.Fatalf("unexpected platform claims: %+v", claims)
	}

	replay := httptest.NewRequest(http.MethodPost, "/v1/auth/oidc/callback", bytes.NewReader(body))
	replayRec := httptest.NewRecorder()
	handleOIDCCallback(cfg, flow, users, nil, nil).ServeHTTP(replayRec, replay)
	if replayRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected replay rejection, got %d: %s", replayRec.Code, replayRec.Body.String())
	}
}

func TestHandleOIDCCallbackIssuesFederatedPlatformSessionFromApprovedEvidence(t *testing.T) {
	users := newFakeUserStore()
	seedUser(t, users, "alice", "unused-password", userstore.RoleAdmin, "acme", true)
	user, _, _ := users.GetByUsername(context.Background(), "alice")
	base := time.Now().UTC().Truncate(time.Second)
	var nonce string
	flow := reauthenticationCompletionFlow(t, user.ID, base, &nonce)
	start, err := flow.Start(context.Background(), "/documents")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(start.AuthorizationURL)
	nonce = parsed.Query().Get("nonce")
	manager, err := session.New(session.NewMemoryStore(), platformSessionPolicy(), session.WithClock(func() time.Time { return base }))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(oidcCallbackRequest{State: start.State, CookieState: start.State, Code: "code-1"})
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/oidc/callback", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	handleOIDCCallback(testAuthConfig(), flow, users, nil, manager).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response oidcCallbackResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(response.Token, platformSessionCredentialPrefix) || response.ReturnTo != "/documents" {
		t.Fatalf("unexpected response: %+v", response)
	}
	result, err := manager.Authenticate(context.Background(), strings.TrimPrefix(response.Token, platformSessionCredentialPrefix), identityBindingChangeAction)
	if err != nil || result.Decision != session.DecisionAllow || result.Principal.AuthenticationMethod != auth.AuthenticationMethodFederated {
		t.Fatalf("session result=%+v err=%v", result, err)
	}
	protectedRequest := httptest.NewRequest(http.MethodGet, "/v1/auth/session", nil)
	protectedRequest.Header.Set("Authorization", "Bearer "+response.Token)
	protected := httptest.NewRecorder()
	auth.Middleware(newSessionCredentialAuthenticator(&countingAuthenticator{}, manager, users))(
		handleCurrentSession(users),
	).ServeHTTP(protected, protectedRequest)
	if protected.Code != http.StatusOK || !strings.Contains(protected.Body.String(), `"username":"alice"`) {
		t.Fatalf("issued OIDC credential cannot access current session: %d %s", protected.Code, protected.Body.String())
	}
}

func TestHandleOIDCCallbackRejectsMissingApprovedEvidenceWhenSessionCoreEnabled(t *testing.T) {
	users := newFakeUserStore()
	seedUser(t, users, "alice", "unused-password", userstore.RoleAdmin, "acme", true)
	user, _, _ := users.GetByUsername(context.Background(), "alice")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer, nonce string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeOIDCTestJSON(t, w, map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks"})
		case "/jwks":
			writeOIDCTestJSON(t, w, map[string]any{"keys": []any{oidcRSAJWK("weak-key", &key.PublicKey)}})
		case "/token":
			now := time.Now()
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
				"iss": issuer, "sub": "external-alice", "aud": "rag-web", "exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "nonce": nonce,
			})
			token.Header["kid"] = "weak-key"
			signed, _ := token.SignedString(key)
			writeOIDCTestJSON(t, w, map[string]any{"id_token": signed})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback", HTTPClient: idp.Client(),
	}, oidcDirectoryStub{userID: user.ID})
	if err != nil {
		t.Fatal(err)
	}
	flow := oidcauth.NewFlow(authenticator, oidcauth.NewMemoryTransactionStore(), 5*time.Minute)
	start, err := flow.Start(context.Background(), "/")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(start.AuthorizationURL)
	nonce = parsed.Query().Get("nonce")
	manager, err := session.New(session.NewMemoryStore(), platformSessionPolicy())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(oidcCallbackRequest{State: start.State, CookieState: start.State, Code: "code-1"})
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/oidc/callback", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	handleOIDCCallback(testAuthConfig(), flow, users, nil, manager).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || strings.Contains(recorder.Body.String(), platformSessionCredentialPrefix) {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHandleOIDCCallbackDoesNotFallBackToJWTWhenSessionStoreIsUnavailable(t *testing.T) {
	users := newFakeUserStore()
	seedUser(t, users, "alice", "unused-password", userstore.RoleAdmin, "acme", true)
	user, _, _ := users.GetByUsername(context.Background(), "alice")
	base := time.Now().UTC().Truncate(time.Second)
	var nonce string
	flow := reauthenticationCompletionFlow(t, user.ID, base, &nonce)
	start, err := flow.Start(context.Background(), "/documents")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(start.AuthorizationURL)
	nonce = parsed.Query().Get("nonce")
	manager, err := session.New(
		session.NewPostgresStore(nil), platformSessionPolicy(), session.WithClock(func() time.Time { return base }),
	)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(oidcCallbackRequest{State: start.State, CookieState: start.State, Code: "code-1"})
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/oidc/callback", bytes.NewReader(body))
	recorder := httptest.NewRecorder()
	handleOIDCCallback(testAuthConfig(), flow, users, nil, manager).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), "token") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHandleOIDCStartReturnsAuthorizationRequestAndSanitizesReturnPath(t *testing.T) {
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
			writeOIDCTestJSON(t, w, map[string]any{"keys": []any{oidcRSAJWK("start-key", &key.PublicKey)}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback",
		HTTPClient: idp.Client(),
	}, oidcDirectoryStub{userID: "unused"})
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	flow := oidcauth.NewFlow(authenticator, oidcauth.NewMemoryTransactionStore(), 5*time.Minute)
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/start?return_to=https://evil.example/steal", nil)
	rec := httptest.NewRecorder()
	handleOIDCStart(flow, false).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		AuthorizationURL string `json:"authorization_url"`
		State            string `json:"state"`
		ExpiresIn        int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	parsed, err := url.Parse(response.AuthorizationURL)
	if err != nil || response.State == "" || parsed.Query().Get("state") != response.State ||
		parsed.Query().Get("code_challenge_method") != "S256" || response.ExpiresIn != 300 {
		t.Fatalf("unsafe start response: %+v", response)
	}
}

func TestHandleOIDCStartRequestsApprovedDemoAssuranceWhenSessionCoreEnabled(t *testing.T) {
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
			writeOIDCTestJSON(t, w, map[string]any{"keys": []any{oidcRSAJWK("start-assurance-key", &key.PublicKey)}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback", HTTPClient: idp.Client(),
	}, oidcDirectoryStub{userID: "unused"})
	if err != nil {
		t.Fatal(err)
	}
	flow := oidcauth.NewFlow(authenticator, oidcauth.NewMemoryTransactionStore(), 5*time.Minute)
	request := httptest.NewRequest(http.MethodGet, "/v1/auth/oidc/start", nil)
	recorder := httptest.NewRecorder()
	handleOIDCStart(flow, true).ServeHTTP(recorder, request)
	var response oidcStartResponse
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(response.AuthorizationURL)
	if parsed.Query().Get("acr_values") != "2" || parsed.Query().Get("prompt") != "login" || parsed.Query().Get("max_age") != "0" {
		t.Fatalf("approved assurance was not requested: %s", response.AuthorizationURL)
	}
}

func writeOIDCTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func oidcRSAJWK(keyID string, publicKey *rsa.PublicKey) map[string]any {
	return map[string]any{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": keyID,
		"n": base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(publicKey.E)).Bytes()),
	}
}

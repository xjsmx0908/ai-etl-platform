package oidcauth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/externalidentity"
	"ai-etl-pipeline/internal/oidcauth"
)

func TestAuthenticateWithEvidenceMapsApprovedKeycloakDemoClaims(t *testing.T) {
	now := time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC)
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	const keyID = "demo-evidence-key"
	var issuer string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
			})
		case "/jwks":
			writeJSON(t, w, map[string]any{"keys": []any{rsaJWK(keyID, &key.PublicKey)}})
		case "/token":
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
				"iss": issuer, "sub": "External-Subject", "aud": "rag-web",
				"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "nonce": "nonce-1",
				"acr": "2", "amr": []string{"pwd", "otp"}, "auth_time": now.Add(-time.Minute).Unix(),
			})
			token.Header["kid"] = keyID
			signed, signErr := token.SignedString(key)
			if signErr != nil {
				t.Fatalf("sign ID token: %v", signErr)
			}
			writeJSON(t, w, map[string]any{"id_token": signed})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback",
		HTTPClient: idp.Client(), Clock: func() time.Time { return now },
	}, &directoryStub{})
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}

	result, err := authenticator.AuthenticateWithEvidence(context.Background(), oidcauth.CodeExchange{
		Code: "code-1", CodeVerifier: "pkce-verifier", Nonce: "nonce-1",
	})
	if err != nil {
		t.Fatalf("authenticate with evidence: %v", err)
	}
	if result.Principal.SubjectID != "user-42" || result.Evidence.Assurance != "demo-mfa" ||
		!result.Evidence.AuthenticatedAt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestAuthenticateWithEvidenceRejectsUnapprovedOrAmbiguousClaims(t *testing.T) {
	now := time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		acr      any
		amr      any
		authTime any
	}{
		{name: "missing acr", amr: []string{"pwd", "otp"}, authTime: now.Unix()},
		{name: "weak acr", acr: "1", amr: []string{"pwd", "otp"}, authTime: now.Unix()},
		{name: "missing amr", acr: "2", authTime: now.Unix()},
		{name: "password only", acr: "2", amr: []string{"pwd"}, authTime: now.Unix()},
		{name: "ambiguous extra method", acr: "2", amr: []string{"pwd", "otp", "sms"}, authTime: now.Unix()},
		{name: "duplicate method", acr: "2", amr: []string{"pwd", "pwd"}, authTime: now.Unix()},
		{name: "missing auth time", acr: "2", amr: []string{"pwd", "otp"}},
		{name: "stale at boundary", acr: "2", amr: []string{"pwd", "otp"}, authTime: now.Add(-10 * time.Minute).Unix()},
		{name: "future auth time", acr: "2", amr: []string{"pwd", "otp"}, authTime: now.Add(time.Second).Unix()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			authenticator := evidenceAuthenticator(t, now, test.acr, test.amr, test.authTime)
			if _, err := authenticator.AuthenticateWithEvidence(context.Background(), oidcauth.CodeExchange{
				Code: "code-1", CodeVerifier: "pkce-verifier", Nonce: "nonce-1",
			}); err == nil {
				t.Fatal("expected unapproved evidence to fail closed")
			}
		})
	}
}

func TestLegacyAuthenticateDoesNotRequireReauthenticationEvidence(t *testing.T) {
	now := time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC)
	authenticator := evidenceAuthenticator(t, now, nil, nil, nil)
	principal, err := authenticator.Authenticate(context.Background(), oidcauth.CodeExchange{
		Code: "code-1", CodeVerifier: "pkce-verifier", Nonce: "nonce-1",
	})
	if err != nil || principal.SubjectID != "user-42" {
		t.Fatalf("legacy OIDC authentication changed: principal=%+v err=%v", principal, err)
	}
}

func TestCompleteLoginWithEvidenceReturnsApprovedEvidence(t *testing.T) {
	now := time.Date(2026, 8, 31, 19, 30, 0, 0, time.UTC)
	var expectedNonce string
	authenticator := reauthenticationAuthenticator(t, now, &expectedNonce, auth.Principal{
		TenantID: "tenant-a", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodFederated,
	})
	flow := oidcauth.NewFlow(authenticator, oidcauth.NewMemoryTransactionStore(), 5*time.Minute)
	start, err := flow.Start(context.Background(), "/documents")
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := url.Parse(start.AuthorizationURL)
	expectedNonce = parsed.Query().Get("nonce")
	result, returnTo, err := flow.CompleteLoginWithEvidence(context.Background(), start.State, start.State, "code-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Principal.SubjectID != "user-42" || result.Evidence.Assurance != "demo-mfa" ||
		!result.Evidence.AuthenticatedAt.Equal(now.Add(-time.Minute)) || returnTo != "/documents" {
		t.Fatalf("unexpected result=%+v return_to=%q", result, returnTo)
	}
}

func evidenceAuthenticator(t *testing.T, now time.Time, acr, amr, authTime any) *oidcauth.Authenticator {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	const keyID = "evidence-table-key"
	var issuer string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
			})
		case "/jwks":
			writeJSON(t, w, map[string]any{"keys": []any{rsaJWK(keyID, &key.PublicKey)}})
		case "/token":
			claims := jwt.MapClaims{
				"iss": issuer, "sub": "External-Subject", "aud": "rag-web",
				"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "nonce": "nonce-1",
			}
			if acr != nil {
				claims["acr"] = acr
			}
			if amr != nil {
				claims["amr"] = amr
			}
			if authTime != nil {
				claims["auth_time"] = authTime
			}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			token.Header["kid"] = keyID
			signed, signErr := token.SignedString(key)
			if signErr != nil {
				t.Fatalf("sign ID token: %v", signErr)
			}
			writeJSON(t, w, map[string]any{"id_token": signed})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(idp.Close)
	issuer = idp.URL
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback",
		HTTPClient: idp.Client(), Clock: func() time.Time { return now },
	}, &directoryStub{})
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	return authenticator
}

func TestStartReauthenticationBindsSessionAndRequestsFreshDemoMFA(t *testing.T) {
	now := time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC)
	authenticator := evidenceAuthenticator(t, now, "2", []string{"pwd", "otp"}, now.Unix())
	flow := oidcauth.NewFlow(authenticator, oidcauth.NewMemoryTransactionStore(), 5*time.Minute)
	start, err := flow.StartReauthentication(context.Background(), oidcauth.ReauthenticationStartCommand{
		Credential: "opaque-current", Action: "identity.binding.change", ReturnTo: "/users?step=confirm",
		Principal: auth.Principal{TenantID: "tenant-a", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodFederated},
	})
	if err != nil {
		t.Fatalf("start reauthentication: %v", err)
	}
	authorizationURL, err := url.Parse(start.AuthorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	query := authorizationURL.Query()
	if query.Get("prompt") != "login" || query.Get("max_age") != "0" || query.Get("acr_values") != "2" ||
		query.Get("state") != start.State || query.Get("nonce") == "" || query.Get("code_challenge_method") != "S256" {
		t.Fatalf("unsafe reauthentication request: %s", start.AuthorizationURL)
	}
}

func TestCompleteReauthenticationReturnsBoundEvidenceAndRejectsReplay(t *testing.T) {
	now := time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC)
	var expectedNonce string
	authenticator := reauthenticationAuthenticator(t, now, &expectedNonce, auth.Principal{
		TenantID: "tenant-a", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodFederated,
	})
	flow := oidcauth.NewFlow(authenticator, oidcauth.NewMemoryTransactionStore(), 5*time.Minute)
	start, err := flow.StartReauthentication(context.Background(), oidcauth.ReauthenticationStartCommand{
		Credential: "opaque-current", Action: "identity.binding.change", ReturnTo: "/users?step=confirm",
		Principal: auth.Principal{TenantID: "tenant-a", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodFederated},
	})
	if err != nil {
		t.Fatalf("start reauthentication: %v", err)
	}
	parsed, _ := url.Parse(start.AuthorizationURL)
	expectedNonce = parsed.Query().Get("nonce")
	result, err := flow.CompleteReauthentication(context.Background(), oidcauth.ReauthenticationCompleteCommand{
		CookieState: start.State, CallbackState: start.State, Code: "code-1", CurrentCredential: "opaque-current",
	})
	if err != nil {
		t.Fatalf("complete reauthentication: %v", err)
	}
	if result.Principal.SubjectID != "user-42" || result.Principal.TenantID != "tenant-a" ||
		result.Action != "identity.binding.change" || result.ReturnTo != "/users?step=confirm" ||
		result.Evidence.Assurance != "demo-mfa" || !result.Evidence.AuthenticatedAt.Equal(now.Add(-time.Minute)) {
		t.Fatalf("unexpected completion: %+v", result)
	}
	if _, err := flow.CompleteReauthentication(context.Background(), oidcauth.ReauthenticationCompleteCommand{
		CookieState: start.State, CallbackState: start.State, Code: "code-replay", CurrentCredential: "opaque-current",
	}); err == nil {
		t.Fatal("expected consumed transaction replay to fail")
	}
}

func TestCompleteReauthenticationFailsClosedWhenBindingsChange(t *testing.T) {
	now := time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC)
	tests := []struct {
		name              string
		currentCredential string
		callbackState     func(string) string
		resolvedPrincipal auth.Principal
	}{
		{
			name: "current credential changed", currentCredential: "different-credential",
			resolvedPrincipal: auth.Principal{TenantID: "tenant-a", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodFederated},
		},
		{
			name: "callback state changed", currentCredential: "opaque-current", callbackState: func(string) string { return "tampered-state" },
			resolvedPrincipal: auth.Principal{TenantID: "tenant-a", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodFederated},
		},
		{
			name: "subject changed", currentCredential: "opaque-current",
			resolvedPrincipal: auth.Principal{TenantID: "tenant-a", SubjectID: "other-user", AuthenticationMethod: auth.AuthenticationMethodFederated},
		},
		{
			name: "tenant changed", currentCredential: "opaque-current",
			resolvedPrincipal: auth.Principal{TenantID: "tenant-b", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodFederated},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var expectedNonce string
			authenticator := reauthenticationAuthenticator(t, now, &expectedNonce, test.resolvedPrincipal)
			flow := oidcauth.NewFlow(authenticator, oidcauth.NewMemoryTransactionStore(), 5*time.Minute)
			start, err := flow.StartReauthentication(context.Background(), oidcauth.ReauthenticationStartCommand{
				Credential: "opaque-current", Action: "identity.binding.change", ReturnTo: "/users",
				Principal: auth.Principal{TenantID: "tenant-a", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodFederated},
			})
			if err != nil {
				t.Fatalf("start reauthentication: %v", err)
			}
			parsed, _ := url.Parse(start.AuthorizationURL)
			expectedNonce = parsed.Query().Get("nonce")
			callbackState := start.State
			if test.callbackState != nil {
				callbackState = test.callbackState(start.State)
			}
			if _, err := flow.CompleteReauthentication(context.Background(), oidcauth.ReauthenticationCompleteCommand{
				CookieState: start.State, CallbackState: callbackState, Code: "code-1", CurrentCredential: test.currentCredential,
			}); err == nil {
				t.Fatal("expected changed binding to fail closed")
			}
		})
	}
}

func TestCompleteReauthenticationRejectsOrdinaryLoginTransaction(t *testing.T) {
	now := time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC)
	var expectedNonce string
	authenticator := reauthenticationAuthenticator(t, now, &expectedNonce, auth.Principal{
		TenantID: "tenant-a", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodFederated,
	})
	flow := oidcauth.NewFlow(authenticator, oidcauth.NewMemoryTransactionStore(), 5*time.Minute)
	start, err := flow.Start(context.Background(), "/documents")
	if err != nil {
		t.Fatalf("start ordinary login: %v", err)
	}
	if _, err := flow.CompleteReauthentication(context.Background(), oidcauth.ReauthenticationCompleteCommand{
		CookieState: start.State, CallbackState: start.State, Code: "code-1", CurrentCredential: "opaque-current",
	}); err == nil {
		t.Fatal("expected ordinary login transaction to be rejected")
	}
}

func TestOrdinaryLoginCompletionRejectsReauthenticationTransaction(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	var expectedNonce string
	authenticator := reauthenticationAuthenticator(t, now, &expectedNonce, auth.Principal{
		TenantID: "tenant-a", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodFederated,
	})
	flow := oidcauth.NewFlow(authenticator, oidcauth.NewMemoryTransactionStore(), 5*time.Minute)
	start, err := flow.StartReauthentication(context.Background(), oidcauth.ReauthenticationStartCommand{
		Credential: "opaque-current", Action: "identity.binding.change", ReturnTo: "/users",
		Principal: auth.Principal{TenantID: "tenant-a", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodFederated},
	})
	if err != nil {
		t.Fatalf("start reauthentication: %v", err)
	}
	parsed, _ := url.Parse(start.AuthorizationURL)
	expectedNonce = parsed.Query().Get("nonce")
	if _, _, err := flow.Complete(context.Background(), start.State, start.State, "code-1"); err == nil {
		t.Fatal("expected ordinary login completion to reject reauthentication transaction")
	}
}

func TestReauthenticationTransactionCompletesOnceAcrossRedisBackedInstances(t *testing.T) {
	redisAddress := os.Getenv("OIDC_REDIS_TEST_ADDR")
	if redisAddress == "" {
		t.Skip("OIDC_REDIS_TEST_ADDR is not set")
	}
	now := time.Now().UTC().Truncate(time.Second)
	var expectedNonce string
	authenticator := reauthenticationAuthenticator(t, now, &expectedNonce, auth.Principal{
		TenantID: "tenant-a", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodFederated,
	})
	namespace := "reauth-" + time.Now().Format("20060102150405.000000000")
	storeA, err := oidcauth.NewRedisTransactionStore(redisAddress, "", 0, namespace)
	if err != nil {
		t.Fatalf("create first Redis transaction store: %v", err)
	}
	t.Cleanup(func() { _ = storeA.Close() })
	storeB, err := oidcauth.NewRedisTransactionStore(redisAddress, "", 0, namespace)
	if err != nil {
		t.Fatalf("create second Redis transaction store: %v", err)
	}
	t.Cleanup(func() { _ = storeB.Close() })
	start, err := oidcauth.NewFlow(authenticator, storeA, 5*time.Minute).StartReauthentication(
		context.Background(), oidcauth.ReauthenticationStartCommand{
			Credential: "opaque-current", Action: "identity.binding.change", ReturnTo: "/users",
			Principal: auth.Principal{TenantID: "tenant-a", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodFederated},
		})
	if err != nil {
		t.Fatalf("start first instance: %v", err)
	}
	parsed, _ := url.Parse(start.AuthorizationURL)
	expectedNonce = parsed.Query().Get("nonce")
	command := oidcauth.ReauthenticationCompleteCommand{
		CookieState: start.State, CallbackState: start.State, Code: "code-1", CurrentCredential: "opaque-current",
	}
	if _, err := oidcauth.NewFlow(authenticator, storeB, 5*time.Minute).CompleteReauthentication(context.Background(), command); err != nil {
		t.Fatalf("complete second instance: %v", err)
	}
	if _, err := oidcauth.NewFlow(authenticator, storeA, 5*time.Minute).CompleteReauthentication(context.Background(), command); err == nil {
		t.Fatal("expected cross-instance replay to fail")
	}
}

func TestReauthenticationFailsClosedWhenRedisBecomesUnavailable(t *testing.T) {
	redisAddress := os.Getenv("OIDC_REDIS_TEST_ADDR")
	if redisAddress == "" {
		t.Skip("OIDC_REDIS_TEST_ADDR is not set")
	}
	now := time.Now().UTC().Truncate(time.Second)
	var expectedNonce string
	authenticator := reauthenticationAuthenticator(t, now, &expectedNonce, auth.Principal{
		TenantID: "tenant-a", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodFederated,
	})
	store, err := oidcauth.NewRedisTransactionStore(redisAddress, "", 0, "reauth-unavailable")
	if err != nil {
		t.Fatalf("create Redis transaction store: %v", err)
	}
	flow := oidcauth.NewFlow(authenticator, store, 5*time.Minute)
	start, err := flow.StartReauthentication(context.Background(), oidcauth.ReauthenticationStartCommand{
		Credential: "opaque-current", Action: "identity.binding.change", ReturnTo: "/users",
		Principal: auth.Principal{TenantID: "tenant-a", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodFederated},
	})
	if err != nil {
		t.Fatalf("start reauthentication: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close Redis transaction store: %v", err)
	}
	if _, err := flow.CompleteReauthentication(context.Background(), oidcauth.ReauthenticationCompleteCommand{
		CookieState: start.State, CallbackState: start.State, Code: "code-1", CurrentCredential: "opaque-current",
	}); err == nil {
		t.Fatal("expected unavailable transaction store to fail closed")
	}
}

func TestReauthenticationFailsClosedWhenTokenEndpointIsUnavailable(t *testing.T) {
	now := time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC)
	var expectedNonce string
	authenticator := reauthenticationAuthenticator(t, now, &expectedNonce, auth.Principal{
		TenantID: "tenant-a", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodFederated,
	})
	flow := oidcauth.NewFlow(authenticator, oidcauth.NewMemoryTransactionStore(), 5*time.Minute)
	start, err := flow.StartReauthentication(context.Background(), oidcauth.ReauthenticationStartCommand{
		Credential: "opaque-current", Action: "identity.binding.change", ReturnTo: "/users",
		Principal: auth.Principal{TenantID: "tenant-a", SubjectID: "user-42", AuthenticationMethod: auth.AuthenticationMethodFederated},
	})
	if err != nil {
		t.Fatalf("start reauthentication: %v", err)
	}
	parsed, _ := url.Parse(start.AuthorizationURL)
	expectedNonce = parsed.Query().Get("nonce")
	if _, err := flow.CompleteReauthentication(context.Background(), oidcauth.ReauthenticationCompleteCommand{
		CookieState: start.State, CallbackState: start.State, Code: "provider-unavailable", CurrentCredential: "opaque-current",
	}); err == nil {
		t.Fatal("expected unavailable token endpoint to fail closed")
	}
}

type reauthenticationDirectory struct {
	principal auth.Principal
}

func (d *reauthenticationDirectory) Resolve(context.Context, externalidentity.ExternalIdentity) (auth.Principal, error) {
	return d.principal, nil
}

func reauthenticationAuthenticator(t *testing.T, now time.Time, nonce *string, principal auth.Principal) *oidcauth.Authenticator {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	const keyID = "reauth-transaction-key"
	var issuer string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
			})
		case "/jwks":
			writeJSON(t, w, map[string]any{"keys": []any{rsaJWK(keyID, &key.PublicKey)}})
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse token request: %v", err)
			}
			if r.Form.Get("code") == "provider-unavailable" {
				http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
				return
			}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
				"iss": issuer, "sub": "External-Subject", "aud": "rag-web",
				"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "nonce": *nonce,
				"acr": "2", "amr": []string{"pwd", "otp"}, "auth_time": now.Add(-time.Minute).Unix(),
			})
			token.Header["kid"] = keyID
			signed, signErr := token.SignedString(key)
			if signErr != nil {
				t.Fatalf("sign ID token: %v", signErr)
			}
			writeJSON(t, w, map[string]any{"id_token": signed})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(idp.Close)
	issuer = idp.URL
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback",
		HTTPClient: idp.Client(), Clock: func() time.Time { return now },
	}, &reauthenticationDirectory{principal: principal})
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	return authenticator
}

package oidcauth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
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

type directoryStub struct {
	want externalidentity.ExternalIdentity
	got  externalidentity.ExternalIdentity
	err  error
}

func (d *directoryStub) Resolve(_ context.Context, identity externalidentity.ExternalIdentity) (auth.Principal, error) {
	d.got = identity
	if d.err != nil {
		return auth.Principal{}, d.err
	}
	return auth.Principal{
		TenantID:             "tenant-a",
		SubjectID:            "user-42",
		Role:                 "admin",
		AuthenticationMethod: auth.AuthenticationMethodFederated,
		Capabilities:         []string{auth.ScopeQuery, auth.ScopeUpload, auth.ScopeAgent, auth.ScopeAdmin},
	}, nil
}

func TestAuthorizationCodeRejectsUntrustedTokenClaimsAndDirectoryFailure(t *testing.T) {
	tests := []struct {
		name      string
		algorithm jwt.SigningMethod
		issuer    func(string) string
		audience  string
		nonce     string
		directory error
	}{
		{name: "wrong issuer", algorithm: jwt.SigningMethodRS256, issuer: func(string) string { return "https://other.example.com" }, audience: "rag-web", nonce: "nonce-1"},
		{name: "wrong audience", algorithm: jwt.SigningMethodRS256, issuer: func(value string) string { return value }, audience: "other-client", nonce: "nonce-1"},
		{name: "algorithm confusion", algorithm: jwt.SigningMethodHS256, issuer: func(value string) string { return value }, audience: "rag-web", nonce: "nonce-1"},
		{name: "wrong nonce", algorithm: jwt.SigningMethodRS256, issuer: func(value string) string { return value }, audience: "rag-web", nonce: "other-nonce"},
		{name: "directory unavailable", algorithm: jwt.SigningMethodRS256, issuer: func(value string) string { return value }, audience: "rag-web", nonce: "nonce-1", directory: externalidentity.ErrUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatalf("generate key: %v", err)
			}
			const keyID = "negative-key"
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
					now := time.Now()
					claims := jwt.MapClaims{
						"iss": test.issuer(issuer), "sub": "External-Subject", "aud": test.audience,
						"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "nonce": test.nonce,
					}
					token := jwt.NewWithClaims(test.algorithm, claims)
					token.Header["kid"] = keyID
					var signed string
					var signErr error
					if test.algorithm == jwt.SigningMethodHS256 {
						signed, signErr = token.SignedString([]byte("attacker-controlled-secret"))
					} else {
						signed, signErr = token.SignedString(key)
					}
					if signErr != nil {
						t.Fatalf("sign token: %v", signErr)
					}
					writeJSON(t, w, map[string]any{"id_token": signed})
				default:
					http.NotFound(w, r)
				}
			}))
			defer idp.Close()
			issuer = idp.URL
			directory := &directoryStub{err: test.directory}
			authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
				Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback",
				HTTPClient: idp.Client(),
			}, directory)
			if err != nil {
				t.Fatalf("create authenticator: %v", err)
			}
			if _, err := authenticator.Authenticate(context.Background(), oidcauth.CodeExchange{
				Code: "code-1", CodeVerifier: "pkce-verifier", Nonce: "nonce-1",
			}); !errors.Is(err, oidcauth.ErrAuthentication) {
				t.Fatalf("expected fail-closed authentication error, got %v", err)
			}
		})
	}
}

func TestAuthorizationCodeFailsClosedWhenTokenEndpointIsUnavailable(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	var issuer string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
			})
		case "/jwks":
			writeJSON(t, w, map[string]any{"keys": []any{rsaJWK("key", &key.PublicKey)}})
		case "/token":
			http.Error(w, "provider unavailable", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback",
		HTTPClient: idp.Client(),
	}, &directoryStub{})
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	if _, err := authenticator.Authenticate(context.Background(), oidcauth.CodeExchange{
		Code: "code-1", CodeVerifier: "pkce-verifier", Nonce: "nonce-1",
	}); !errors.Is(err, oidcauth.ErrAuthentication) {
		t.Fatalf("expected fail-closed provider error, got %v", err)
	}
}

func TestAuthorizationCodeDoesNotFollowTokenEndpointRedirect(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	redirectReached := false
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectReached = true
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer redirectTarget.Close()

	var issuer string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
			})
		case "/jwks":
			writeJSON(t, w, map[string]any{"keys": []any{rsaJWK("key", &key.PublicKey)}})
		case "/token":
			http.Redirect(w, r, redirectTarget.URL, http.StatusTemporaryRedirect)
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", ClientSecret: "must-not-leave-provider",
		RedirectURI: "https://rag.example.com/api/auth/oidc/callback", HTTPClient: idp.Client(),
	}, &directoryStub{})
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	if _, err := authenticator.Authenticate(context.Background(), oidcauth.CodeExchange{
		Code: "code-1", CodeVerifier: "pkce-verifier", Nonce: "nonce-1",
	}); !errors.Is(err, oidcauth.ErrAuthentication) {
		t.Fatalf("expected redirected token exchange to fail closed, got %v", err)
	}
	if redirectReached {
		t.Fatal("token endpoint redirect was followed")
	}
}

func TestAuthorizationCodeRequiresAuthorizedPartyForMultipleAudiences(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	const keyID = "multi-audience-key"
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
			now := time.Now()
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
				"iss": issuer, "sub": "External-Subject", "aud": []string{"rag-web", "other-api"},
				"azp": "other-client", "exp": now.Add(5 * time.Minute).Unix(),
				"iat": now.Unix(), "nonce": "nonce-1",
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
		HTTPClient: idp.Client(),
	}, &directoryStub{})
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	if _, err := authenticator.Authenticate(context.Background(), oidcauth.CodeExchange{
		Code: "code-1", CodeVerifier: "pkce-verifier", Nonce: "nonce-1",
	}); !errors.Is(err, oidcauth.ErrAuthentication) {
		t.Fatalf("expected invalid authorized party to be rejected, got %v", err)
	}
}

func TestAuthorizationCodeResolvesPolicyOwnedPrincipal(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	const keyID = "key-2026-08"
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
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "code-1" ||
				r.Form.Get("code_verifier") != "pkce-verifier" {
				t.Fatalf("unexpected token request: %v", r.Form)
			}
			now := time.Now()
			claims := jwt.MapClaims{
				"iss": issuer, "sub": "External-Subject", "aud": "rag-web",
				"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "nonce": "nonce-1",
			}
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
			token.Header["kid"] = keyID
			signed, signErr := token.SignedString(key)
			if signErr != nil {
				t.Fatalf("sign ID token: %v", signErr)
			}
			writeJSON(t, w, map[string]any{"id_token": signed, "token_type": "Bearer", "expires_in": 300})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL

	directory := &directoryStub{want: externalidentity.ExternalIdentity{Issuer: issuer, Subject: "External-Subject"}}
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", ClientSecret: "client-secret",
		RedirectURI: "https://rag.example.com/api/auth/oidc/callback",
		HTTPClient:  idp.Client(),
	}, directory)
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}

	principal, err := authenticator.Authenticate(context.Background(), oidcauth.CodeExchange{
		Code: "code-1", CodeVerifier: "pkce-verifier", Nonce: "nonce-1",
	})
	if err != nil {
		t.Fatalf("authenticate code: %v", err)
	}
	if principal.TenantID != "tenant-a" || principal.SubjectID != "user-42" ||
		principal.Role != "admin" || principal.AuthenticationMethod != auth.AuthenticationMethodFederated {
		t.Fatalf("unexpected principal: %+v", principal)
	}
	if directory.got != directory.want {
		t.Fatalf("directory identity mismatch: got %+v want %+v", directory.got, directory.want)
	}
}

func TestAuthorizationCodePreservesTrailingSlashInIssuerIdentity(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	const keyID = "trailing-slash-key"
	var issuer string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/realms/acme/.well-known/openid-configuration":
			writeJSON(t, w, map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "authorize",
				"token_endpoint": issuer + "token", "jwks_uri": issuer + "jwks",
			})
		case "/realms/acme/jwks":
			writeJSON(t, w, map[string]any{"keys": []any{rsaJWK(keyID, &key.PublicKey)}})
		case "/realms/acme/token":
			now := time.Now()
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
				"iss": issuer, "sub": "External-Subject", "aud": "rag-web",
				"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "nonce": "nonce-1",
			})
			token.Header["kid"] = keyID
			signed, signErr := token.SignedString(key)
			if signErr != nil {
				t.Fatalf("sign token: %v", signErr)
			}
			writeJSON(t, w, map[string]any{"id_token": signed})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL + "/realms/acme/"
	directory := &directoryStub{want: externalidentity.ExternalIdentity{Issuer: issuer, Subject: "External-Subject"}}
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback",
		HTTPClient: idp.Client(),
	}, directory)
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	if _, err := authenticator.Authenticate(context.Background(), oidcauth.CodeExchange{
		Code: "code-1", CodeVerifier: "pkce-verifier", Nonce: "nonce-1",
	}); err != nil {
		t.Fatalf("authenticate: %v", err)
	}
	if directory.got != directory.want {
		t.Fatalf("issuer identity changed: got %+v want %+v", directory.got, directory.want)
	}
}

func TestAuthorizationCodeRefreshesJWKSOnceForRotatedKey(t *testing.T) {
	oldKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate old key: %v", err)
	}
	newKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate new key: %v", err)
	}
	currentKey, currentKeyID := oldKey, "old-key"
	jwksReads := 0
	var issuer string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
			})
		case "/jwks":
			jwksReads++
			writeJSON(t, w, map[string]any{"keys": []any{rsaJWK(currentKeyID, &currentKey.PublicKey)}})
		case "/token":
			now := time.Now()
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
				"iss": issuer, "sub": "External-Subject", "aud": "rag-web",
				"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "nonce": "nonce-1",
			})
			token.Header["kid"] = currentKeyID
			signed, signErr := token.SignedString(currentKey)
			if signErr != nil {
				t.Fatalf("sign rotated token: %v", signErr)
			}
			writeJSON(t, w, map[string]any{"id_token": signed})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL

	directory := &directoryStub{}
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback",
		HTTPClient: idp.Client(),
	}, directory)
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	currentKey, currentKeyID = newKey, "new-key"

	if _, err := authenticator.Authenticate(context.Background(), oidcauth.CodeExchange{
		Code: "code-rotated", CodeVerifier: "pkce-verifier", Nonce: "nonce-1",
	}); err != nil {
		t.Fatalf("authenticate after rotation: %v", err)
	}
	if jwksReads != 2 {
		t.Fatalf("expected initial JWKS read plus one bounded refresh, got %d", jwksReads)
	}
}

func TestAuthorizationCodeRefreshesJWKSOnceWhenRotatedKeyReusesKeyID(t *testing.T) {
	oldKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate old key: %v", err)
	}
	newKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate new key: %v", err)
	}
	currentKey := oldKey
	jwksReads := 0
	const keyID = "stable-key-id"
	var issuer string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
			})
		case "/jwks":
			jwksReads++
			writeJSON(t, w, map[string]any{"keys": []any{rsaJWK(keyID, &currentKey.PublicKey)}})
		case "/token":
			now := time.Now()
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
				"iss": issuer, "sub": "External-Subject", "aud": "rag-web",
				"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "nonce": "nonce-1",
			})
			token.Header["kid"] = keyID
			signed, signErr := token.SignedString(currentKey)
			if signErr != nil {
				t.Fatalf("sign rotated token: %v", signErr)
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
		HTTPClient: idp.Client(),
	}, &directoryStub{})
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	currentKey = newKey
	if _, err := authenticator.Authenticate(context.Background(), oidcauth.CodeExchange{
		Code: "code-rotated", CodeVerifier: "pkce-verifier", Nonce: "nonce-1",
	}); err != nil {
		t.Fatalf("authenticate after same-kid rotation: %v", err)
	}
	if jwksReads != 2 {
		t.Fatalf("expected exactly one bounded refresh, got %d JWKS reads", jwksReads)
	}
}

func TestNewFailsClosedWhenDiscoveryOrJWKSIsUnavailable(t *testing.T) {
	tests := []struct {
		name      string
		discovery int
		jwks      int
	}{
		{name: "discovery unavailable", discovery: http.StatusServiceUnavailable, jwks: http.StatusOK},
		{name: "JWKS unavailable", discovery: http.StatusOK, jwks: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key, err := rsa.GenerateKey(rand.Reader, 2048)
			if err != nil {
				t.Fatalf("generate signing key: %v", err)
			}
			var issuer string
			idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/.well-known/openid-configuration":
					if test.discovery != http.StatusOK {
						w.WriteHeader(test.discovery)
						return
					}
					writeJSON(t, w, map[string]any{
						"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
						"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
					})
				case "/jwks":
					if test.jwks != http.StatusOK {
						w.WriteHeader(test.jwks)
						return
					}
					writeJSON(t, w, map[string]any{"keys": []any{rsaJWK("key", &key.PublicKey)}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer idp.Close()
			issuer = idp.URL
			if _, err := oidcauth.New(context.Background(), oidcauth.Config{
				Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/callback",
				HTTPClient: idp.Client(),
			}, &directoryStub{}); !errors.Is(err, oidcauth.ErrAuthentication) {
				t.Fatalf("expected fail-closed provider initialization, got %v", err)
			}
		})
	}
}

func TestFlowUsesPKCEAndConsumesBrowserTransactionOnce(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	const keyID = "flow-key"
	var issuer, expectedNonce string
	tokenRequests := 0
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
			tokenRequests++
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse token request: %v", err)
			}
			if len(r.Form.Get("code_verifier")) < 43 {
				t.Fatalf("missing strong PKCE verifier: %v", r.Form)
			}
			now := time.Now()
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
				"iss": issuer, "sub": "External-Subject", "aud": "rag-web",
				"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "nonce": expectedNonce,
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
		HTTPClient: idp.Client(),
	}, &directoryStub{})
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	flow := oidcauth.NewFlow(authenticator, oidcauth.NewMemoryTransactionStore(), 5*time.Minute)
	start, err := flow.Start(context.Background(), "/documents")
	if err != nil {
		t.Fatalf("start flow: %v", err)
	}
	authorizationURL, err := url.Parse(start.AuthorizationURL)
	if err != nil {
		t.Fatalf("parse authorization URL: %v", err)
	}
	query := authorizationURL.Query()
	if authorizationURL.String() == "" || query.Get("response_type") != "code" ||
		query.Get("client_id") != "rag-web" || query.Get("code_challenge_method") != "S256" ||
		len(query.Get("code_challenge")) < 43 || query.Get("state") != start.State ||
		query.Get("nonce") == "" || query.Get("scope") != "openid" {
		t.Fatalf("unsafe authorization request: %s", authorizationURL.String())
	}
	expectedNonce = query.Get("nonce")

	principal, returnTo, err := flow.Complete(context.Background(), start.State, start.State, "code-1")
	if err != nil {
		t.Fatalf("complete flow: %v", err)
	}
	if principal.SubjectID != "user-42" || returnTo != "/documents" {
		t.Fatalf("unexpected completion: principal=%+v return_to=%q", principal, returnTo)
	}
	if _, _, err := flow.Complete(context.Background(), start.State, start.State, "code-replay"); err == nil {
		t.Fatal("expected consumed state replay to fail")
	}
	if tokenRequests != 1 {
		t.Fatalf("replay reached token endpoint: requests=%d", tokenRequests)
	}
}

func TestAuthorizationCodeRejectsIDTokenWithoutIssuedAt(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	const keyID = "missing-iat-key"
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
				"exp": time.Now().Add(5 * time.Minute).Unix(), "nonce": "nonce-1",
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
		HTTPClient: idp.Client(),
	}, &directoryStub{})
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	if _, err := authenticator.Authenticate(context.Background(), oidcauth.CodeExchange{
		Code: "code-1", CodeVerifier: "pkce-verifier", Nonce: "nonce-1",
	}); err == nil {
		t.Fatal("expected missing issued-at claim to be rejected")
	}
}

func TestFlowTransactionCanCompleteOnAnotherRedisBackedInstance(t *testing.T) {
	redisAddress := os.Getenv("OIDC_REDIS_TEST_ADDR")
	if redisAddress == "" {
		t.Skip("OIDC_REDIS_TEST_ADDR is not set")
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}
	const keyID = "redis-flow-key"
	var issuer, nonce string
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
			now := time.Now()
			token := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
				"iss": issuer, "sub": "External-Subject", "aud": "rag-web",
				"exp": now.Add(5 * time.Minute).Unix(), "iat": now.Unix(), "nonce": nonce,
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
		HTTPClient: idp.Client(),
	}, &directoryStub{})
	if err != nil {
		t.Fatalf("create authenticator: %v", err)
	}
	storeA, err := oidcauth.NewRedisTransactionStore(redisAddress, "", 0, "test-a")
	if err != nil {
		t.Fatalf("create first Redis store: %v", err)
	}
	defer storeA.Close()
	storeB, err := oidcauth.NewRedisTransactionStore(redisAddress, "", 0, "test-a")
	if err != nil {
		t.Fatalf("create second Redis store: %v", err)
	}
	defer storeB.Close()
	start, err := oidcauth.NewFlow(authenticator, storeA, 5*time.Minute).Start(context.Background(), "/")
	if err != nil {
		t.Fatalf("start first instance: %v", err)
	}
	authorizationURL, _ := url.Parse(start.AuthorizationURL)
	nonce = authorizationURL.Query().Get("nonce")
	if _, _, err := oidcauth.NewFlow(authenticator, storeB, 5*time.Minute).Complete(
		context.Background(), start.State, start.State, "code-1",
	); err != nil {
		t.Fatalf("complete second instance: %v", err)
	}
	if _, _, err := oidcauth.NewFlow(authenticator, storeA, 5*time.Minute).Complete(
		context.Background(), start.State, start.State, "code-replay",
	); err == nil {
		t.Fatal("expected cross-instance replay to fail")
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func rsaJWK(keyID string, publicKey *rsa.PublicKey) map[string]any {
	e := big.NewInt(int64(publicKey.E)).Bytes()
	return map[string]any{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": keyID,
		"n": base64.RawURLEncoding.EncodeToString(publicKey.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(e),
	}
}

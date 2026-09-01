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

	"ai-etl-pipeline/internal/oidcauth"
)

func TestRPLogoutUsesValidatedDiscoveryAndSingleUseState(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
				"end_session_endpoint": issuer + "/logout",
			})
		case "/jwks":
			writeJSON(t, w, map[string]any{"keys": []any{rsaJWK("logout-key", &key.PublicKey)}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL

	if _, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback",
		LogoutRedirectURI: "https://rag.example.com/api/auth/logout/callback?next=/documents", HTTPClient: idp.Client(),
	}, &directoryStub{}); err == nil {
		t.Fatal("expected logout redirect URI with query to be rejected")
	}

	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback",
		LogoutRedirectURI: "https://rag.example.com/api/auth/logout/callback", HTTPClient: idp.Client(),
	}, &directoryStub{})
	if err != nil {
		t.Fatal(err)
	}
	flow := oidcauth.NewFlow(authenticator, oidcauth.NewMemoryTransactionStore(), 5*time.Minute)
	start, err := flow.StartLogout(context.Background(), "/documents")
	if err != nil {
		t.Fatal(err)
	}
	logoutURL, err := url.Parse(start.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	query := logoutURL.Query()
	if logoutURL.Scheme+"://"+logoutURL.Host+logoutURL.Path != issuer+"/logout" ||
		query.Get("client_id") != "rag-web" || query.Get("state") != start.State ||
		query.Get("post_logout_redirect_uri") != "https://rag.example.com/api/auth/logout/callback" ||
		query.Has("id_token_hint") {
		t.Fatalf("unexpected logout URL: %s", logoutURL)
	}
	returnTo, err := flow.CompleteLogout(context.Background(), start.State, start.State)
	if err != nil || returnTo != "/documents" {
		t.Fatalf("returnTo=%q err=%v", returnTo, err)
	}
	if _, err := flow.CompleteLogout(context.Background(), start.State, start.State); err == nil {
		t.Fatal("expected logout state replay rejection")
	}
}

func TestRPLogoutStateCompletesOnceAcrossRedisBackedInstances(t *testing.T) {
	redisAddress := os.Getenv("OIDC_REDIS_TEST_ADDR")
	if redisAddress == "" {
		t.Skip("OIDC_REDIS_TEST_ADDR is not set")
	}
	authenticator := logoutAuthenticator(t)
	namespace := "logout-" + time.Now().Format("20060102150405.000000000")
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
	start, err := oidcauth.NewFlow(authenticator, storeA, 5*time.Minute).StartLogout(context.Background(), "/documents")
	if err != nil {
		t.Fatalf("start first instance: %v", err)
	}
	returnTo, err := oidcauth.NewFlow(authenticator, storeB, 5*time.Minute).CompleteLogout(
		context.Background(), start.State, start.State,
	)
	if err != nil || returnTo != "/documents" {
		t.Fatalf("complete second instance: returnTo=%q err=%v", returnTo, err)
	}
	if _, err := oidcauth.NewFlow(authenticator, storeA, 5*time.Minute).CompleteLogout(
		context.Background(), start.State, start.State,
	); err == nil {
		t.Fatal("expected cross-instance logout state replay to fail")
	}
}

func TestRPLogoutFailsClosedWhenRedisBecomesUnavailable(t *testing.T) {
	redisAddress := os.Getenv("OIDC_REDIS_TEST_ADDR")
	if redisAddress == "" {
		t.Skip("OIDC_REDIS_TEST_ADDR is not set")
	}
	store, err := oidcauth.NewRedisTransactionStore(
		redisAddress, "", 0, "logout-unavailable-"+time.Now().Format("20060102150405.000000000"),
	)
	if err != nil {
		t.Fatalf("create Redis transaction store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close Redis transaction store: %v", err)
	}
	if _, err := oidcauth.NewFlow(logoutAuthenticator(t), store, 5*time.Minute).StartLogout(
		context.Background(), "/documents",
	); err == nil {
		t.Fatal("expected unavailable logout transaction store to fail closed")
	}
}

func TestRPLogoutIsOptionalWhenProviderOmitsEndSessionEndpoint(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
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
			writeJSON(t, w, map[string]any{"keys": []any{rsaJWK("logout-optional-key", &key.PublicKey)}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer idp.Close()
	issuer = idp.URL
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback",
		LogoutRedirectURI: "https://rag.example.com/api/auth/logout/callback", HTTPClient: idp.Client(),
	}, &directoryStub{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := oidcauth.NewFlow(authenticator, oidcauth.NewMemoryTransactionStore(), 5*time.Minute).StartLogout(
		context.Background(), "/documents",
	); err == nil {
		t.Fatal("expected provider logout without an end-session endpoint to remain disabled")
	}
}

func logoutAuthenticator(t *testing.T) *oidcauth.Authenticator {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var issuer string
	idp := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]any{
				"issuer": issuer, "authorization_endpoint": issuer + "/authorize",
				"token_endpoint": issuer + "/token", "jwks_uri": issuer + "/jwks",
				"end_session_endpoint": issuer + "/logout",
			})
		case "/jwks":
			writeJSON(t, w, map[string]any{"keys": []any{rsaJWK("logout-redis-key", &key.PublicKey)}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(idp.Close)
	issuer = idp.URL
	authenticator, err := oidcauth.New(context.Background(), oidcauth.Config{
		Issuer: issuer, ClientID: "rag-web", RedirectURI: "https://rag.example.com/api/auth/oidc/callback",
		LogoutRedirectURI: "https://rag.example.com/api/auth/logout/callback", HTTPClient: idp.Client(),
	}, &directoryStub{})
	if err != nil {
		t.Fatal(err)
	}
	return authenticator
}

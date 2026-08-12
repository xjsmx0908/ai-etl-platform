package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVerifyValidToken(t *testing.T) {
	secret := "test-secret-32-chars-minimum-length"
	v := NewVerifier(secret)
	token, err := GenerateTestTokenWithPermission(secret, "tenant-a", "user-1", "user", []string{"query"})
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	claims, err := v.Verify(req)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if claims.TenantID != "tenant-a" || claims.UserID != "user-1" || claims.Permission != "user" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if !hasScope(claims.Scopes, "query") {
		t.Fatalf("expected query scope, got %v", claims.Scopes)
	}
}

func TestVerifyMissingHeader(t *testing.T) {
	v := NewVerifier("secret")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if _, err := v.Verify(req); err == nil {
		t.Fatal("expected error for missing authorization header")
	}
}

func TestVerifyRejectsNonBearerFormat(t *testing.T) {
	v := NewVerifier("secret")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Token abc123")
	if _, err := v.Verify(req); err == nil {
		t.Fatal("expected error for non-Bearer format")
	}
}

func TestVerifyRejectsWrongSecret(t *testing.T) {
	token, _ := GenerateTestTokenWithPermission("correct-secret", "t", "u", "user", nil)
	v := NewVerifier("wrong-secret")

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if _, err := v.Verify(req); err == nil {
		t.Fatal("expected error for token signed with different secret")
	}
}

func TestMiddlewareRequiresScope(t *testing.T) {
	secret := "test-secret-32-chars-minimum-length"
	v := NewVerifier(secret)
	token, _ := GenerateTestTokenWithPermission(secret, "t", "u", "user", []string{"query"})

	mw := v.Middleware("query", "admin")
	var passed bool
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		passed = true
	}))

	// Missing admin scope -> 403, handler not called.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for missing scope, got %d", w.Code)
	}
	if passed {
		t.Fatal("handler should not run when scope missing")
	}
}

func TestMiddlewareInjectsContext(t *testing.T) {
	secret := "test-secret-32-chars-minimum-length"
	v := NewVerifier(secret)
	token, _ := GenerateTestTokenWithPermission(secret, "tenant-x", "user-9", "admin", []string{"query", "upload"})

	mw := v.Middleware("query")
	var gotTenant, gotUser, gotPerm string
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant = GetTenantID(r.Context())
		gotUser = GetUserID(r.Context())
		gotPerm = GetPermission(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if gotTenant != "tenant-x" || gotUser != "user-9" || gotPerm != "admin" {
		t.Fatalf("unexpected context: tenant=%q user=%q perm=%q", gotTenant, gotUser, gotPerm)
	}
}

func TestMiddlewareRejectsMissingToken(t *testing.T) {
	v := NewVerifier("secret")
	mw := v.Middleware()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	mw(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler should not run")
	})).ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "unauthorized") {
		t.Fatalf("expected unauthorized body, got %q", w.Body.String())
	}
}

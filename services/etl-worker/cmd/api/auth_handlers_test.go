package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/userstore"
)

func testAuthConfig() config.Config {
	return config.Config{JWTSecret: "test-secret-0123456789abcdef"}
}

func seedUser(t *testing.T, store *fakeUserStore, username, password, role, tenant string, active bool) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	u := &userstore.User{Username: username, PasswordHash: hash, Role: role, TenantID: tenant, Active: active}
	if err := store.Create(context.Background(), u); err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

func doLogin(handler http.HandlerFunc, username, password string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(loginRequest{Username: username, Password: password})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestHandleLogin_Success(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "s3cret-pw", "admin", "acme", true)
	handler := handleLogin(testAuthConfig(), store, nil)

	rec := doLogin(handler, "alice", "s3cret-pw")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp loginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("expected a token")
	}
	if resp.User.Username != "alice" || resp.User.Role != "admin" || resp.User.TenantID != "acme" {
		t.Fatalf("unexpected login user: %+v", resp.User)
	}

	// The issued token must verify with the API's verifier.
	v := auth.NewVerifier(testAuthConfig().JWTSecret)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+resp.Token)
	claims, err := v.Verify(req)
	if err != nil {
		t.Fatalf("verify issued token: %v", err)
	}
	if claims.TenantID != "acme" || claims.UserID != resp.User.ID {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestHandleLogin_BadPassword(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "s3cret-pw", "user", "acme", true)
	rec := doLogin(handleLogin(testAuthConfig(), store, nil), "alice", "wrong-pw")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleLogin_UnknownUser(t *testing.T) {
	store := newFakeUserStore()
	rec := doLogin(handleLogin(testAuthConfig(), store, nil), "nobody", "whatever")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unknown user, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleLogin_InactiveUser(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "s3cret-pw", "user", "acme", false)
	rec := doLogin(handleLogin(testAuthConfig(), store, nil), "alice", "s3cret-pw")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for inactive user, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleLogin_MissingFields(t *testing.T) {
	store := newFakeUserStore()
	handler := handleLogin(testAuthConfig(), store, nil)

	rec := doLogin(handler, "", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for empty fields, got %d", rec.Code)
	}
}

func TestBootstrapAdmin_SeedsOnEmptyDB(t *testing.T) {
	store := newFakeUserStore()
	cfg := testAuthConfig()
	cfg.BootstrapAdminUsername = "root"
	cfg.BootstrapAdminPassword = "root-password"
	cfg.BootstrapAdminTenant = "default"

	if err := bootstrapAdmin(context.Background(), cfg, store); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	u, found, err := store.GetByUsername(context.Background(), "root")
	if err != nil || !found {
		t.Fatalf("expected seeded admin, found=%v err=%v", found, err)
	}
	if u.Role != userstore.RoleAdmin || u.TenantID != "default" {
		t.Fatalf("unexpected admin: %+v", u)
	}
	if !auth.VerifyPassword(u.PasswordHash, "root-password") {
		t.Fatal("seeded admin password must verify")
	}
}

func TestBootstrapAdmin_NoopWhenUsersExist(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "existing", "pw", "user", "acme", true)
	cfg := testAuthConfig()
	cfg.BootstrapAdminUsername = "root"
	cfg.BootstrapAdminPassword = "root-password"

	if err := bootstrapAdmin(context.Background(), cfg, store); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if _, found, _ := store.GetByUsername(context.Background(), "root"); found {
		t.Fatal("bootstrap must not create a second admin when users exist")
	}
}

func TestBootstrapAdmin_EmptyPasswordFailsOutsideDev(t *testing.T) {
	store := newFakeUserStore()
	cfg := testAuthConfig()
	cfg.Environment = "production" // not dev
	cfg.BootstrapAdminPassword = ""

	if err := bootstrapAdmin(context.Background(), cfg, store); err == nil {
		t.Fatal("expected production bootstrap without password to fail")
	}
}

func TestHandleLogin_ProductionOIDCDisablesOrdinaryPasswordLogin(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "s3cret-pw", "admin", "acme", true)
	cfg := testAuthConfig()
	cfg.Environment = "production"
	cfg.OIDCEnabled = true

	rec := doLogin(handleLogin(cfg, store, nil), "alice", "s3cret-pw")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected password endpoint to be unavailable, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleLoginRejectsFederatedOnlyUser(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "s3cret-pw", "readonly", "acme", true)
	user, found, err := store.GetByUsername(context.Background(), "alice")
	if err != nil || !found {
		t.Fatal("seed user")
	}
	user.Origin = "scim"
	store.byID[user.ID] = user
	store.byName["alice"] = user
	recorder := doLogin(handleLogin(testAuthConfig(), store, nil), "alice", "s3cret-pw")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected federated-only password rejection, got %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestHandleCurrentSessionReturnsCurrentInternalUser(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "unused", userstore.RoleAdmin, "acme", true)
	user, found, err := store.GetByUsername(context.Background(), "alice")
	if err != nil || !found {
		t.Fatalf("load user: found=%v err=%v", found, err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/session", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.CtxPrincipal, auth.Principal{
		TenantID: "acme", SubjectID: user.ID, Role: userstore.RoleAdmin,
		AuthenticationMethod: auth.AuthenticationMethodFederated,
	}))
	rec := httptest.NewRecorder()
	handleCurrentSession(store).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var response struct {
		User loginUser `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.User.ID != user.ID || response.User.Username != "alice" || response.User.TenantID != "acme" {
		t.Fatalf("unexpected user: %+v", response.User)
	}
}

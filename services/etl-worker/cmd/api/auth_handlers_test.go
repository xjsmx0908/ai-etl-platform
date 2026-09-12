package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/middleware"
	"ai-etl-pipeline/internal/session"
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
	handler := handleLogin(testAuthConfig(), store, nil, nil)

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

func TestHandleLoginIssuesLocalPlatformSessionWhenSessionCoreEnabled(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "s3cret-pw", userstore.RoleAdmin, "acme", true)
	manager, err := session.New(session.NewMemoryStore(), platformSessionPolicy(3))
	if err != nil {
		t.Fatal(err)
	}
	recorder := doLogin(handleLogin(testAuthConfig(), store, nil, manager), "alice", "s3cret-pw")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response loginResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(response.Token, platformSessionCredentialPrefix) ||
		response.ExpiresAt.Before(time.Now().Add(7*time.Hour+59*time.Minute)) {
		t.Fatalf("unexpected platform session response: %+v", response)
	}
	result, err := manager.Authenticate(
		context.Background(), strings.TrimPrefix(response.Token, platformSessionCredentialPrefix), platformSessionRequestAction,
	)
	if err != nil || result.Decision != session.DecisionAllow ||
		result.Principal.AuthenticationMethod != auth.AuthenticationMethodLocal {
		t.Fatalf("session result=%+v err=%v", result, err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/auth/session", nil)
	request.Header.Set("Authorization", "Bearer "+response.Token)
	protected := httptest.NewRecorder()
	auth.Middleware(newSessionCredentialAuthenticator(&countingAuthenticator{}, manager, store))(
		handleCurrentSession(store),
	).ServeHTTP(protected, request)
	if protected.Code != http.StatusOK || !strings.Contains(protected.Body.String(), `"username":"alice"`) {
		t.Fatalf("issued credential cannot access current session: %d %s", protected.Code, protected.Body.String())
	}
}

func TestHandleLoginDoesNotFallBackToJWTWhenSessionStoreIsUnavailable(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "s3cret-pw", userstore.RoleAdmin, "acme", true)
	manager, err := session.New(session.NewPostgresStore(nil), platformSessionPolicy(3))
	if err != nil {
		t.Fatal(err)
	}
	recorder := doLogin(handleLogin(testAuthConfig(), store, nil, manager), "alice", "s3cret-pw")
	if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), "token") {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestHandleLogin_BadPassword(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "s3cret-pw", "user", "acme", true)
	rec := doLogin(handleLogin(testAuthConfig(), store, nil, nil), "alice", "wrong-pw")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleLogin_UnknownUser(t *testing.T) {
	store := newFakeUserStore()
	rec := doLogin(handleLogin(testAuthConfig(), store, nil, nil), "nobody", "whatever")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unknown user, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleLogin_InactiveUser(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "s3cret-pw", "user", "acme", false)
	rec := doLogin(handleLogin(testAuthConfig(), store, nil, nil), "alice", "s3cret-pw")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for inactive user, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleLogin_MissingFields(t *testing.T) {
	store := newFakeUserStore()
	handler := handleLogin(testAuthConfig(), store, nil, nil)

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

	rec := doLogin(handleLogin(cfg, store, nil, nil), "alice", "s3cret-pw")
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
	user.Origin = userstore.OriginSCIM
	store.byID[user.ID] = user
	store.byName["alice"] = user
	recorder := doLogin(handleLogin(testAuthConfig(), store, nil, nil), "alice", "s3cret-pw")
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

func TestHandleLoginRejectsOversizedBody(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "s3cret-pw", "admin", "acme", true)
	handler := handleLogin(testAuthConfig(), store, nil, nil)
	body := `{"username":"` + strings.Repeat("a", 20*1024) + `","password":"x"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleLoginRateLimitsBeforeLookup(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "s3cret-pw", "admin", "acme", true)
	guard := middleware.NewMemoryLoginGuard(middleware.LoginGuardConfig{
		PerIP: 20, PerUser: 5, LockThreshold: 10, Window: time.Minute,
	})
	handler := handleLoginWithGuard(testAuthConfig(), store, nil, nil, guard)
	for i := 0; i < 5; i++ {
		rec := doLogin(handler, "alice", "wrong-pw")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d body=%s", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := doLogin(handler, "alice", "wrong-pw")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("sixth attempt status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
	if !strings.Contains(rec.Body.String(), "invalid credentials") {
		t.Fatalf("lock/limit must not change the error text: %s", rec.Body.String())
	}
}

func demoLoginConfig() config.Config {
	cfg := testAuthConfig()
	cfg.Environment = "dev"
	cfg.DemoLoginEnabled = true
	cfg.DemoTenantID = "demo"
	cfg.DemoUserUsername = "demo-user"
	cfg.DemoAdminUsername = "demo-admin"
	cfg.BootstrapAdminTenant = "default"
	cfg.BootstrapAdminUsername = "admin"
	return cfg
}

func doDemoLogin(handler http.HandlerFunc, account string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(demoLoginRequest{Account: account})
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/demo-login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestHandleAuthMethods_IncludesDemoLogin(t *testing.T) {
	cfg := demoLoginConfig()
	req := httptest.NewRequest(http.MethodGet, "/v1/auth/methods", nil)
	rec := httptest.NewRecorder()
	handleAuthMethods(cfg).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"demo_login_enabled":true`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	cfg.DemoLoginEnabled = false
	rec = httptest.NewRecorder()
	handleAuthMethods(cfg).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"demo_login_enabled":false`) {
		t.Fatalf("disabled status=%d body=%s", rec.Code, rec.Body.String())
	}

	cfg.DemoLoginEnabled = true
	cfg.Environment = "production"
	rec = httptest.NewRecorder()
	handleAuthMethods(cfg).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"demo_login_enabled":false`) {
		t.Fatalf("production must hide demo login, status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEnsureDemoAccounts_SeedsIsolatedTenant(t *testing.T) {
	store := newFakeUserStore()
	cfg := demoLoginConfig()
	if err := ensureDemoAccounts(context.Background(), cfg, store); err != nil {
		t.Fatalf("ensure demo accounts: %v", err)
	}
	user, found, err := store.GetByUsername(context.Background(), "demo-user")
	if err != nil || !found || user.Role != userstore.RoleUser || user.TenantID != "demo" || !user.Active {
		t.Fatalf("unexpected demo user: found=%v err=%v user=%+v", found, err, user)
	}
	admin, found, err := store.GetByUsername(context.Background(), "demo-admin")
	if err != nil || !found || admin.Role != userstore.RoleAdmin || admin.TenantID != "demo" || !admin.Active {
		t.Fatalf("unexpected demo admin: found=%v err=%v user=%+v", found, err, admin)
	}
	if err := ensureDemoAccounts(context.Background(), cfg, store); err != nil {
		t.Fatalf("idempotent ensure: %v", err)
	}
}

func TestEnsureDemoAccounts_NoopWhenDisabled(t *testing.T) {
	store := newFakeUserStore()
	cfg := demoLoginConfig()
	cfg.DemoLoginEnabled = false
	if err := ensureDemoAccounts(context.Background(), cfg, store); err != nil {
		t.Fatalf("disabled ensure: %v", err)
	}
	if _, found, _ := store.GetByUsername(context.Background(), "demo-user"); found {
		t.Fatal("disabled demo login must not create users")
	}
}

func TestEnsureDemoAccounts_RejectsHijackedUsername(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "demo-user", "pw", userstore.RoleAdmin, "acme", true)
	if err := ensureDemoAccounts(context.Background(), demoLoginConfig(), store); err == nil {
		t.Fatal("expected hijacked demo username to fail")
	}
}

func TestHandleDemoLogin_Success(t *testing.T) {
	store := newFakeUserStore()
	cfg := demoLoginConfig()
	if err := ensureDemoAccounts(context.Background(), cfg, store); err != nil {
		t.Fatalf("ensure demo accounts: %v", err)
	}
	handler := handleDemoLogin(cfg, store, nil, nil)

	userRec := doDemoLogin(handler, "user")
	if userRec.Code != http.StatusOK {
		t.Fatalf("user status=%d body=%s", userRec.Code, userRec.Body.String())
	}
	var userResp loginResponse
	if err := json.Unmarshal(userRec.Body.Bytes(), &userResp); err != nil {
		t.Fatalf("decode user: %v", err)
	}
	if userResp.Token == "" || userResp.User.Username != "demo-user" || userResp.User.Role != userstore.RoleUser || userResp.User.TenantID != "demo" {
		t.Fatalf("unexpected demo user login: %+v", userResp)
	}

	adminRec := doDemoLogin(handler, "admin")
	if adminRec.Code != http.StatusOK {
		t.Fatalf("admin status=%d body=%s", adminRec.Code, adminRec.Body.String())
	}
	var adminResp loginResponse
	if err := json.Unmarshal(adminRec.Body.Bytes(), &adminResp); err != nil {
		t.Fatalf("decode admin: %v", err)
	}
	if adminResp.Token == "" || adminResp.User.Username != "demo-admin" || adminResp.User.Role != userstore.RoleAdmin || adminResp.User.TenantID != "demo" {
		t.Fatalf("unexpected demo admin login: %+v", adminResp)
	}
	if strings.Contains(adminRec.Body.String(), "password") {
		t.Fatalf("demo login must not return a password: %s", adminRec.Body.String())
	}
}

func TestHandleDemoLogin_Disabled(t *testing.T) {
	store := newFakeUserStore()
	cfg := demoLoginConfig()
	cfg.DemoLoginEnabled = false
	rec := doDemoLogin(handleDemoLogin(cfg, store, nil, nil), "user")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleDemoLogin_ProductionHidden(t *testing.T) {
	store := newFakeUserStore()
	cfg := demoLoginConfig()
	cfg.Environment = "production"
	rec := doDemoLogin(handleDemoLogin(cfg, store, nil, nil), "admin")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleDemoLogin_InvalidAccount(t *testing.T) {
	store := newFakeUserStore()
	cfg := demoLoginConfig()
	if err := ensureDemoAccounts(context.Background(), cfg, store); err != nil {
		t.Fatalf("ensure demo accounts: %v", err)
	}
	rec := doDemoLogin(handleDemoLogin(cfg, store, nil, nil), "root")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleDemoLogin_MissingAccount(t *testing.T) {
	store := newFakeUserStore()
	cfg := demoLoginConfig()
	if err := ensureDemoAccounts(context.Background(), cfg, store); err != nil {
		t.Fatalf("ensure demo accounts: %v", err)
	}
	admin, found, err := store.GetByUsername(context.Background(), "demo-admin")
	if err != nil || !found {
		t.Fatal("expected demo admin")
	}
	if err := store.Delete(context.Background(), admin.ID); err != nil {
		t.Fatalf("delete demo admin: %v", err)
	}
	rec := doDemoLogin(handleDemoLogin(cfg, store, nil, nil), "admin")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

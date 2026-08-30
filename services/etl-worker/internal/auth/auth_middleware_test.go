package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ai-etl-pipeline/internal/userstore"
	"github.com/golang-jwt/jwt/v5"
)

// fakeUserLookup is a minimal userstore.Store that only serves GetByID; all
// other methods are no-ops. It is used to exercise token-version revocation.
type fakeUserLookup struct {
	users map[string]userstore.User
}

type authenticatorStub struct {
	principal Principal
	err       error
}

func (s authenticatorStub) Authenticate(*http.Request) (Principal, error) {
	return s.principal, s.err
}

var _ userstore.Store = (*fakeUserLookup)(nil)

func (f *fakeUserLookup) GetByID(_ context.Context, id string) (userstore.User, bool, error) {
	u, ok := f.users[id]
	return u, ok, nil
}
func (f *fakeUserLookup) GetByUsername(context.Context, string) (userstore.User, bool, error) {
	return userstore.User{}, false, nil
}
func (f *fakeUserLookup) List(context.Context, string, int, int) ([]userstore.User, int, error) {
	return nil, 0, nil
}
func (f *fakeUserLookup) Create(context.Context, *userstore.User) error { return nil }
func (f *fakeUserLookup) Update(context.Context, string, userstore.UserPatch) (userstore.User, error) {
	return userstore.User{}, nil
}
func (f *fakeUserLookup) SetPasswordHash(context.Context, string, string) error { return nil }
func (f *fakeUserLookup) Delete(context.Context, string) error                  { return nil }
func (f *fakeUserLookup) ListTenants(context.Context) ([]userstore.Tenant, error) {
	return nil, nil
}
func (f *fakeUserLookup) CreateTenant(context.Context, string, string) error { return nil }
func (f *fakeUserLookup) CountUsers(context.Context) (int, error)            { return 0, nil }

// middlewareRequest runs req through the verifier middleware and returns the
// status written by the protected handler (or the middleware's own status).
func middlewareRequest(t *testing.T, v *Verifier, token string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	v.Middleware()(next).ServeHTTP(rec, req)
	if called {
		return http.StatusOK
	}
	return rec.Code
}

func TestMiddleware_AcceptsCurrentTokenVersion(t *testing.T) {
	lookup := &fakeUserLookup{users: map[string]userstore.User{
		"u-1": {ID: "u-1", Username: "alice", Role: "admin", TenantID: "acme", Active: true, TokenVersion: 3},
	}}
	v := NewVerifierWithStore("test-secret", lookup)
	token, _, err := IssueToken("test-secret", "u-1", "alice", "admin", "acme", 3)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if code := middlewareRequest(t, v, token); code != http.StatusOK {
		t.Fatalf("expected 200 for current token version, got %d", code)
	}
}

func TestAuthenticateReturnsPolicyOwnedPrincipal(t *testing.T) {
	lookup := &fakeUserLookup{users: map[string]userstore.User{
		"u-1": {
			ID: "u-1", Username: "alice", Role: "user", TenantID: "acme",
			Active: true, TokenVersion: 3,
		},
	}}
	v := NewVerifierWithStore("test-secret", lookup)
	// These signed claims deliberately overstate Alice's internal authority.
	token, _, err := IssueToken("test-secret", "u-1", "alice", "admin", "other-tenant", 3)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	principal, err := v.Authenticate(req)
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if principal.SubjectID != "u-1" || principal.TenantID != "acme" || principal.Role != "user" {
		t.Fatalf("principal did not use internal authority: %+v", principal)
	}
	if principal.AuthenticationMethod != AuthenticationMethodLocal {
		t.Fatalf("expected local authentication, got %q", principal.AuthenticationMethod)
	}
	if hasScope(principal.Capabilities, ScopeAdmin) {
		t.Fatalf("token-injected admin capability survived: %v", principal.Capabilities)
	}
}

func TestMiddleware_RejectsRevokedToken(t *testing.T) {
	// The user's password was reset, bumping token_version 0 -> 1. The token
	// below was issued at version 0 and must now be rejected.
	lookup := &fakeUserLookup{users: map[string]userstore.User{
		"u-1": {ID: "u-1", Username: "alice", Role: "admin", TenantID: "acme", Active: true, TokenVersion: 1},
	}}
	v := NewVerifierWithStore("test-secret", lookup)
	token, _, err := IssueToken("test-secret", "u-1", "alice", "admin", "acme", 0)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if code := middlewareRequest(t, v, token); code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for revoked token, got %d", code)
	}
}

func TestMiddleware_AllowsUserAbsentFromDB(t *testing.T) {
	// Offline/test tokens (e.g. eval) carry a UserID with no DB row. The JWT
	// signature still protects them; revocation applies to login-issued tokens.
	lookup := &fakeUserLookup{users: map[string]userstore.User{}}
	v := NewVerifierWithStore("test-secret", lookup)
	token, _, err := IssueToken("test-secret", "offline-user", "tester", "user", "default", 0)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if code := middlewareRequest(t, v, token); code != http.StatusOK {
		t.Fatalf("expected 200 for offline user, got %d", code)
	}
}

func TestProductionMiddlewareRejectsUnknownTestPrincipal(t *testing.T) {
	lookup := &fakeUserLookup{users: map[string]userstore.User{}}
	v := NewVerifierWithPolicy("test-secret", lookup, ProductionIdentityPolicy())
	token, err := GenerateTestTokenWithPermission(
		"test-secret", "untrusted-tenant", "offline-user", "admin", []string{ScopeAdmin},
	)
	if err != nil {
		t.Fatalf("GenerateTestTokenWithPermission: %v", err)
	}
	if code := middlewareRequest(t, v, token); code != http.StatusUnauthorized {
		t.Fatalf("expected production test principal rejection, got %d", code)
	}
}

func TestProductionMiddlewareAcceptsKnownLocalPrincipal(t *testing.T) {
	lookup := &fakeUserLookup{users: map[string]userstore.User{
		"u-1": {ID: "u-1", Role: "readonly", TenantID: "acme", Active: true, TokenVersion: 2},
	}}
	v := NewVerifierWithPolicy("test-secret", lookup, ProductionIdentityPolicy())
	token, _, err := IssueToken("test-secret", "u-1", "alice", "readonly", "acme", 2)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if code := middlewareRequest(t, v, token); code != http.StatusOK {
		t.Fatalf("expected known local principal acceptance, got %d", code)
	}
}

func TestNonProductionMiddlewareKeepsExplicitTestCompatibility(t *testing.T) {
	lookup := &fakeUserLookup{users: map[string]userstore.User{}}
	v := NewVerifierWithPolicy("test-secret", lookup, NonProductionIdentityPolicy())
	token, err := GenerateTestToken("test-secret", "eval", "offline-user", []string{ScopeQuery})
	if err != nil {
		t.Fatalf("GenerateTestToken: %v", err)
	}
	if code := middlewareRequest(t, v, token); code != http.StatusOK {
		t.Fatalf("expected explicit non-production test compatibility, got %d", code)
	}
}

func TestProductionMiddlewareRejectsLegacyCredentialForKnownUser(t *testing.T) {
	lookup := &fakeUserLookup{users: map[string]userstore.User{
		"u-1": {ID: "u-1", Role: "user", TenantID: "acme", Active: true},
	}}
	v := NewVerifierWithPolicy("test-secret", lookup, ProductionIdentityPolicy())
	claims := Claims{
		TenantID: "acme", UserID: "u-1", Permission: "user", Scopes: []string{ScopeQuery},
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign legacy token: %v", err)
	}
	if code := middlewareRequest(t, v, token); code != http.StatusUnauthorized {
		t.Fatalf("expected legacy production credential rejection, got %d", code)
	}
}

func TestNonProductionMiddlewareAcceptsLegacyCredentialDuringMigration(t *testing.T) {
	lookup := &fakeUserLookup{users: map[string]userstore.User{
		"u-1": {ID: "u-1", Role: "user", TenantID: "acme", Active: true},
	}}
	v := NewVerifierWithPolicy("test-secret", lookup, NonProductionIdentityPolicy())
	claims := Claims{
		TenantID: "acme", UserID: "u-1", Permission: "user", Scopes: []string{ScopeQuery},
		RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))},
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("test-secret"))
	if err != nil {
		t.Fatalf("sign legacy token: %v", err)
	}
	if code := middlewareRequest(t, v, token); code != http.StatusOK {
		t.Fatalf("expected non-production legacy migration compatibility, got %d", code)
	}
}

func TestMiddlewareInjectsPolicyOwnedPrincipal(t *testing.T) {
	lookup := &fakeUserLookup{users: map[string]userstore.User{
		"u-1": {ID: "u-1", Role: "readonly", TenantID: "acme", Active: true, TokenVersion: 4},
	}}
	v := NewVerifierWithPolicy("test-secret", lookup, ProductionIdentityPolicy())
	token, _, err := IssueToken("test-secret", "u-1", "alice", "admin", "forged", 4)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	var got Principal
	v.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = GetPrincipal(r.Context())
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", recorder.Code)
	}
	if got.TenantID != "acme" || got.SubjectID != "u-1" || got.Role != "readonly" ||
		got.AuthenticationMethod != AuthenticationMethodLocal || hasScope(got.Capabilities, ScopeAdmin) {
		t.Fatalf("unexpected principal context: %+v", got)
	}
}

func TestIdentityPolicyForEnvironment(t *testing.T) {
	for _, environment := range []string{"production", "Production", " production "} {
		production := IdentityPolicyForEnvironment(environment)
		if !production.RequireKnownSubject || production.AllowedMethods[AuthenticationMethodTest] {
			t.Fatalf("%q policy is not fail closed: %+v", environment, production)
		}
	}
	for _, environment := range []string{"dev", "staging", "evaluation"} {
		policy := IdentityPolicyForEnvironment(environment)
		if policy.RequireKnownSubject || !policy.AllowedMethods[AuthenticationMethodTest] {
			t.Fatalf("%s should retain explicit test compatibility: %+v", environment, policy)
		}
	}
}

func TestMiddlewareConsumesProviderNeutralAuthenticator(t *testing.T) {
	authenticator := authenticatorStub{principal: Principal{
		TenantID: "acme", SubjectID: "federated-user", Role: "user",
		AuthenticationMethod: AuthenticationMethodFederated,
		Capabilities:         []string{ScopeQuery},
	}}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	recorder := httptest.NewRecorder()
	var got Principal
	Middleware(authenticator, ScopeQuery)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = GetPrincipal(r.Context())
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK || got.AuthenticationMethod != AuthenticationMethodFederated {
		t.Fatalf("provider-neutral authenticator was not consumed: status=%d principal=%+v", recorder.Code, got)
	}
}

func TestMiddleware_RejectsInactiveUser(t *testing.T) {
	lookup := &fakeUserLookup{users: map[string]userstore.User{
		"u-1": {ID: "u-1", Username: "alice", Role: "user", TenantID: "acme", Active: false, TokenVersion: 0},
	}}
	v := NewVerifierWithStore("test-secret", lookup)
	token, _, err := IssueToken("test-secret", "u-1", "alice", "user", "acme", 0)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if code := middlewareRequest(t, v, token); code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for inactive user, got %d", code)
	}
}

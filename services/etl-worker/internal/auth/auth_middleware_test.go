package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-etl-pipeline/internal/userstore"
)

// fakeUserLookup is a minimal userstore.Store that only serves GetByID; all
// other methods are no-ops. It is used to exercise token-version revocation.
type fakeUserLookup struct {
	users map[string]userstore.User
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

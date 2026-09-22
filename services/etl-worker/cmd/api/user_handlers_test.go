package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/userstore"
)

func ctxWithTenant(tenant string) context.Context {
	return context.WithValue(context.Background(), auth.CtxTenantID, tenant)
}

func ctxWithUserAndTenant(userID, tenant string) context.Context {
	ctx := context.WithValue(context.Background(), auth.CtxUserID, userID)
	return context.WithValue(ctx, auth.CtxTenantID, tenant)
}

func doRequest(handler http.Handler, method, target string, body any, ctx context.Context) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, target, &buf)
	req.Header.Set("Content-Type", "application/json")
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHandleCreateUser_Success(t *testing.T) {
	store := newFakeUserStore()
	handler := handleUsers(store)

	// Tenant comes from the caller's JWT, not the request body.
	rec := doRequest(handler, http.MethodPost, "/v1/users", createUserRequest{
		Username: "bob", Password: "pw-123", Role: "user", Active: boolPtr(true),
	}, ctxWithTenant("acme"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var view userView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if view.Username != "bob" || view.Role != "user" || view.TenantID != "acme" {
		t.Fatalf("unexpected view: %+v", view)
	}
	if view.ID == "" {
		t.Fatal("expected generated id")
	}

	u, found, _ := store.GetByUsername(context.Background(), "bob")
	if !found {
		t.Fatal("expected user to be persisted")
	}
	if u.PasswordHash == "pw-123" || u.PasswordHash == "" {
		t.Fatal("password must be stored as a hash, not plaintext")
	}
}

func TestHandleUpdateUser_CrossTenantNotFound(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "bob", "pw", "user", "acme", true)
	bob, found, _ := store.GetByUsername(context.Background(), "bob")
	if !found {
		t.Fatal("bob not seeded")
	}
	handler := handleUser(store)

	// Caller belongs to tenant "other"; bob lives in "acme" → 404.
	rec := doRequest(handler, http.MethodPut, "/v1/users/"+bob.ID, updateUserRequest{Active: boolPtr(false)}, ctxWithTenant("other"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-tenant update, got %d", rec.Code)
	}
}

func TestHandleDeleteUser_CrossTenantNotFound(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "bob", "pw", "user", "acme", true)
	bob, found, _ := store.GetByUsername(context.Background(), "bob")
	if !found {
		t.Fatal("bob not seeded")
	}
	handler := handleUser(store)

	rec := doRequest(handler, http.MethodDelete, "/v1/users/"+bob.ID, nil, ctxWithTenant("other"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for cross-tenant delete, got %d", rec.Code)
	}
	if _, found, _ := store.GetByUsername(context.Background(), "bob"); !found {
		t.Fatal("bob must survive a cross-tenant delete attempt")
	}
}

func TestHandleCreateUser_DuplicateUsername(t *testing.T) {
	store := newFakeUserStore()
	handler := handleUsers(store)
	doRequest(handler, http.MethodPost, "/v1/users", createUserRequest{Username: "bob", Password: "pw-123", Role: "user", TenantID: "acme"}, ctxWithTenant("default"))

	rec := doRequest(handler, http.MethodPost, "/v1/users", createUserRequest{Username: "bob", Password: "pw-123", Role: "user", TenantID: "acme"}, ctxWithTenant("default"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for duplicate username, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleCreateUser_InvalidRole(t *testing.T) {
	store := newFakeUserStore()
	handler := handleUsers(store)
	rec := doRequest(handler, http.MethodPost, "/v1/users", createUserRequest{Username: "bob", Password: "pw", Role: "superuser", TenantID: "acme"}, ctxWithTenant("default"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid role, got %d", rec.Code)
	}
}

func TestHandleListUsers_TenantScoped(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "pw", "admin", "acme", true)
	seedUser(t, store, "bob", "pw", "user", "acme", true)
	seedUser(t, store, "carol", "pw", "user", "other", true)
	handler := handleUsers(store)

	rec := doRequest(handler, http.MethodGet, "/v1/users", nil, ctxWithTenant("acme"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		Items []userView `json:"items"`
		Total int        `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("expected 2 users in tenant acme, got %d", resp.Total)
	}
}

func TestHandleDeleteUser_RejectsSelfDelete(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "admin", "pw", "admin", "acme", true)
	admin, found, _ := store.GetByUsername(context.Background(), "admin")
	if !found {
		t.Fatal("admin not seeded")
	}
	handler := handleUser(store)

	rec := doRequest(handler, http.MethodDelete, "/v1/users/"+admin.ID, nil, ctxWithUserAndTenant(admin.ID, "acme"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for self-delete, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleSetPassword_RotatesHash(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "old-pw", "user", "acme", true)
	u, found, _ := store.GetByUsername(context.Background(), "alice")
	if !found {
		t.Fatal("alice not seeded")
	}
	handler := handleUser(store)

	rec := doRequest(handler, http.MethodPost, "/v1/users/"+u.ID+"/password", setPasswordRequest{Password: "new-pw"}, ctxWithTenant("acme"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if auth.VerifyPassword(u.PasswordHash, "new-pw") {
		t.Fatal("old hash must not verify the new password")
	}
	got, _, _ := store.GetByUsername(context.Background(), "alice")
	if !auth.VerifyPassword(got.PasswordHash, "new-pw") {
		t.Fatal("new hash must verify the new password")
	}
}

func TestHandleSetPassword_RejectsSamePassword(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "old-pw", "user", "acme", true)
	u, found, _ := store.GetByUsername(context.Background(), "alice")
	if !found {
		t.Fatal("alice not seeded")
	}
	handler := handleUser(store)

	rec := doRequest(handler, http.MethodPost, "/v1/users/"+u.ID+"/password", setPasswordRequest{Password: "old-pw"}, ctxWithTenant("acme"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for same password, got %d: %s", rec.Code, rec.Body.String())
	}
	if !auth.VerifyPassword(u.PasswordHash, "old-pw") {
		t.Fatal("hash must be unchanged after rejected reset")
	}
}

func TestHandleUserRejectsLifecycleChangesForSCIMUser(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "unused", "readonly", "acme", true)
	user, found, _ := store.GetByUsername(context.Background(), "alice")
	if !found {
		t.Fatal("alice not seeded")
	}
	user.Origin = userstore.OriginSCIM
	store.byID[user.ID], store.byName["alice"] = user, user
	handler := handleUser(store)
	for _, request := range []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPut, "/v1/users/" + user.ID, updateUserRequest{Active: boolPtr(false)}},
		{http.MethodDelete, "/v1/users/" + user.ID, nil},
		{http.MethodPost, "/v1/users/" + user.ID + "/password", setPasswordRequest{Password: "new-password"}},
	} {
		recorder := doRequest(handler, request.method, request.path, request.body, ctxWithTenant("acme"))
		if recorder.Code != http.StatusConflict {
			t.Fatalf("%s %s: expected 409, got %d: %s", request.method, request.path, recorder.Code, recorder.Body.String())
		}
	}
}

func boolPtr(b bool) *bool { return &b }

// Regression tests for the missing password-length policy.
//
// Before the fix, a password longer than bcrypt's 72-byte limit reached
// HashPassword, which returns ErrPasswordTooLong, which the handler reported as
// 500 "failed to hash password". The user saw a server fault for typing too
// much -- and nothing in the codebase rejected a 1-character password either.
// The assertions below pin the status code, because that is the part the user
// actually experiences; a test that only checked "an error was returned" would
// have passed on the broken code too.

func TestHandleSetPassword_RejectsPasswordPastBcryptLimit(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "alice-password", userstore.RoleUser, "t1", true)
	u, found, _ := store.GetByUsername(context.Background(), "alice")
	if !found {
		t.Fatal("seedUser did not create alice")
	}
	before := u.PasswordHash

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleSetPassword(w, r, store, u.ID)
	})
	rec := doRequest(handler, http.MethodPost, "/v1/users/"+u.ID+"/password",
		map[string]string{"password": strings.Repeat("a", auth.MaxPasswordBytes+1)},
		ctxWithTenant("t1"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (a too-long password is a client error, not a server fault); body=%s",
			rec.Code, rec.Body.String())
	}
	// A rejected request must not half-apply: the old hash has to survive.
	after, _, _ := store.GetByUsername(context.Background(), "alice")
	if after.PasswordHash != before {
		t.Error("password hash changed even though the request was rejected")
	}
}

// TestHandleSetPassword_AcceptsAShortPassword pins a decision, not a preference.
//
// There is no minimum password length on the administrator user API, and that is
// deliberate: these endpoints have always accepted any non-empty password, so
// adding a floor would change a shipped contract rather than fix a bug. The only
// check on this path is the storage constraint (auth.ValidatePasswordStorage),
// which exists because bcrypt cannot represent more than 72 bytes.
//
// If someone later decides to add a floor here, this test is where the change
// gets recorded: delete it and lengthen the short-password fixtures in this
// file, rather than discovering the breakage somewhere downstream.
func TestHandleSetPassword_AcceptsAShortPassword(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "alice-password", userstore.RoleUser, "t1", true)
	u, found, _ := store.GetByUsername(context.Background(), "alice")
	if !found {
		t.Fatal("seedUser did not create alice")
	}

	short := "a"
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleSetPassword(w, r, store, u.ID)
	})
	rec := doRequest(handler, http.MethodPost, "/v1/users/"+u.ID+"/password",
		map[string]string{"password": short}, ctxWithTenant("t1"))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 -- the administrator API accepts any non-empty "+
			"password by design; body=%s", rec.Code, rec.Body.String())
	}
	after, _, _ := store.GetByUsername(context.Background(), "alice")
	if !auth.VerifyPassword(after.PasswordHash, short) {
		t.Error("the short password must be usable afterwards")
	}
}

func TestHandleCreateUser_RejectsPasswordPastBcryptLimit(t *testing.T) {
	store := newFakeUserStore()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleCreateUser(w, r, store)
	})
	rec := doRequest(handler, http.MethodPost, "/v1/users",
		map[string]string{
			"username": "bob",
			"password": strings.Repeat("a", auth.MaxPasswordBytes+1),
			"role":     userstore.RoleUser,
		},
		ctxWithTenant("t1"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	// The user must not exist: a rejected password cannot leave a half-created
	// account with an empty or unset hash behind.
	if _, found, _ := store.GetByUsername(context.Background(), "bob"); found {
		t.Error("bob was created even though the password was rejected")
	}
}

// TestHandleSetPassword_AcceptsBoundaryPassword is the guard against fixing the
// 500 by simply tightening the limit past what bcrypt accepts. The longest
// password bcrypt allows must still work end to end.
func TestHandleSetPassword_AcceptsBoundaryPassword(t *testing.T) {
	store := newFakeUserStore()
	seedUser(t, store, "alice", "alice-password", userstore.RoleUser, "t1", true)
	u, found, _ := store.GetByUsername(context.Background(), "alice")
	if !found {
		t.Fatal("seedUser did not create alice")
	}

	boundary := strings.Repeat("b", auth.MaxPasswordBytes)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handleSetPassword(w, r, store, u.ID)
	})
	rec := doRequest(handler, http.MethodPost, "/v1/users/"+u.ID+"/password",
		map[string]string{"password": boundary}, ctxWithTenant("t1"))

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 for a password at the limit; body=%s", rec.Code, rec.Body.String())
	}
	after, _, _ := store.GetByUsername(context.Background(), "alice")
	if !auth.VerifyPassword(after.PasswordHash, boundary) {
		t.Error("the boundary-length password must be usable afterwards")
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ai-etl-pipeline/internal/auth"
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

	rec := doRequest(handler, http.MethodPost, "/v1/users", createUserRequest{
		Username: "bob", Password: "pw-123", Role: "user", TenantID: "acme", Active: boolPtr(true),
	}, ctxWithTenant("default"))
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

func boolPtr(b bool) *bool { return &b }

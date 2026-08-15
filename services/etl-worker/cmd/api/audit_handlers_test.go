package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"ai-etl-pipeline/internal/audit"
)

func seedAudit(store *fakeAuditStore, tenantID, action string) {
	_ = store.Record(context.Background(), audit.Entry{
		TenantID: tenantID, ActorUserID: "u-1", ActorRole: "admin",
		Action: action, Result: audit.ResultSuccess,
	})
}

func TestHandleAuditList_TenantScoped(t *testing.T) {
	store := newFakeAuditStore()
	seedAudit(store, "acme", "login")
	seedAudit(store, "acme", "upload")
	seedAudit(store, "other", "login") // another tenant must not appear
	handler := handleAuditList(store)

	rec := doRequest(handler, http.MethodGet, "/v1/audit", nil, ctxWithTenant("acme"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		Items []auditView `json:"items"`
		Total int         `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("expected 2 audit entries for tenant acme, got %d", resp.Total)
	}
}

func TestHandleAuditList_ActionFilter(t *testing.T) {
	store := newFakeAuditStore()
	seedAudit(store, "acme", "login")
	seedAudit(store, "acme", "upload")
	handler := handleAuditList(store)

	rec := doRequest(handler, http.MethodGet, "/v1/audit?action=upload", nil, ctxWithTenant("acme"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		Items []auditView `json:"items"`
		Total int         `json:"total"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Total != 1 || resp.Items[0].Action != "upload" {
		t.Fatalf("expected only upload entries, got %d: %+v", resp.Total, resp.Items)
	}
}

package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/docstore"
)

func ctxWithRole(tenant, role string, scopes ...string) context.Context {
	ctx := context.WithValue(context.Background(), auth.CtxTenantID, tenant)
	ctx = context.WithValue(ctx, auth.CtxPermission, role)
	ctx = context.WithValue(ctx, auth.CtxScopes, scopes)
	return ctx
}

func seedDoc(store *fakeDocStore, tenantID, docID, permission string) {
	_ = store.Upsert(context.Background(), docstore.Document{
		TenantID:   tenantID,
		DocID:      docID,
		FileName:   docID + ".pdf",
		Permission: permission,
		Status:     docstore.StatusCompleted,
		Metadata:   map[string]string{},
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	})
}

func TestHandleDocuments_TenantScopedAndRoleFiltered(t *testing.T) {
	store := newFakeDocStore()
	seedDoc(store, "acme", "public-1", "public")
	seedDoc(store, "acme", "internal-1", "internal")
	seedDoc(store, "acme", "confidential-1", "confidential")
	seedDoc(store, "other", "public-other", "public")
	handler := handleDocuments(store, testQueryService())

	// A user role sees public + internal, but not confidential, and never
	// another tenant's docs.
	rec := doRequest(handler, http.MethodGet, "/v1/documents", nil, ctxWithRole("acme", "user", "query"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		Items []documentView `json:"items"`
		Total int            `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Total != 2 {
		t.Fatalf("expected 2 visible docs for user role, got %d", resp.Total)
	}
}

func TestHandleDocuments_RequestedPermissionIntersectsRole(t *testing.T) {
	store := newFakeDocStore()
	seedDoc(store, "acme", "internal-1", "internal")
	handler := handleDocuments(store, testQueryService())

	// readonly cannot widen the filter to confidential via the query param.
	rec := doRequest(handler, http.MethodGet, "/v1/documents?permission=confidential", nil, ctxWithRole("acme", "readonly", "query"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var resp struct {
		Total int `json:"total"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Total != 0 {
		t.Fatalf("readonly must not see confidential via filter, got total=%d", resp.Total)
	}
}

func TestHandleDocument_GetRoleFiltered(t *testing.T) {
	store := newFakeDocStore()
	seedDoc(store, "acme", "doc-1", "internal")
	handler := handleDocument(testAuthConfig(), testQueryService(), noopObjectStore{}, store, nil)

	rec := doRequest(handler, http.MethodGet, "/v1/documents/doc-1", nil, ctxWithRole("acme", "user", "query"))
	if rec.Code != http.StatusOK {
		t.Fatalf("user role should read internal doc, got %d: %s", rec.Code, rec.Body.String())
	}
	var view documentView
	_ = json.Unmarshal(rec.Body.Bytes(), &view)
	if view.DocID != "doc-1" {
		t.Fatalf("unexpected doc view: %+v", view)
	}

	// readonly cannot see internal → indistinguishable 404.
	rec = doRequest(handler, http.MethodGet, "/v1/documents/doc-1", nil, ctxWithRole("acme", "readonly", "query"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("readonly should 404 on internal doc, got %d", rec.Code)
	}
}

func TestHandleDocument_AdminPublishesCompletedDraft(t *testing.T) {
	store := newFakeDocStore()
	seedDoc(store, "acme", "doc-1", "internal")
	handler := handleDocument(testAuthConfig(), testQueryService(), noopObjectStore{}, store, nil)
	rec := doRequest(handler, http.MethodPatch, "/v1/documents/doc-1", map[string]string{"publication_status": "published"}, ctxWithRole("acme", "admin", "admin"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	doc, _, _ := store.Get(context.Background(), "acme", "doc-1")
	if doc.PublicationStatus != "published" {
		t.Fatalf("expected published, got %q", doc.PublicationStatus)
	}
}

func TestHandleDocument_ManagedDraftRequiresGovernanceWorkflow(t *testing.T) {
	store := newFakeDocStore()
	if err := store.Upsert(context.Background(), docstore.Document{
		TenantID: "acme", DocID: "doc-1", FileName: "policy.pdf", Permission: "internal",
		Status: docstore.StatusCompleted, DocStatus: docstore.DocStatusActive,
		KnowledgeSpaceID: "policies", PublicationStatus: "draft",
	}); err != nil {
		t.Fatalf("seed managed document: %v", err)
	}
	handler := handleDocument(testAuthConfig(), testQueryService(), noopObjectStore{}, store, nil)
	rec := doRequest(handler, http.MethodPatch, "/v1/documents/doc-1", map[string]string{"publication_status": "published"}, ctxWithRole("acme", "admin", "admin"))
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	doc, _, _ := store.Get(context.Background(), "acme", "doc-1")
	if doc.PublicationStatus != "draft" {
		t.Fatalf("managed document bypassed governance: %q", doc.PublicationStatus)
	}
}

func TestHandleDocument_NonAdminCannotPublish(t *testing.T) {
	store := newFakeDocStore()
	seedDoc(store, "acme", "doc-1", "internal")
	handler := handleDocument(testAuthConfig(), testQueryService(), noopObjectStore{}, store, nil)
	rec := doRequest(handler, http.MethodPatch, "/v1/documents/doc-1", map[string]string{"publication_status": "published"}, ctxWithRole("acme", "user", "upload"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleDocument_CrossTenantGet404(t *testing.T) {
	store := newFakeDocStore()
	seedDoc(store, "other", "doc-1", "public")
	handler := handleDocument(testAuthConfig(), testQueryService(), noopObjectStore{}, store, nil)

	rec := doRequest(handler, http.MethodGet, "/v1/documents/doc-1", nil, ctxWithRole("acme", "admin", "query"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("another tenant's doc must 404, got %d", rec.Code)
	}
}

func TestHandleDocument_DeleteRequiresUploadScope(t *testing.T) {
	store := newFakeDocStore()
	seedDoc(store, "acme", "doc-1", "internal")
	handler := handleDocument(testAuthConfig(), testQueryService(), noopObjectStore{}, store, nil)

	// query scope alone cannot delete.
	rec := doRequest(handler, http.MethodDelete, "/v1/documents/doc-1", nil, ctxWithRole("acme", "user", "query"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without upload scope, got %d", rec.Code)
	}
	if _, found, _ := store.Get(context.Background(), "acme", "doc-1"); !found {
		t.Fatal("doc must still exist after forbidden delete")
	}
}

// testDeleteConfig returns a config with mocked Qdrant/ES so the cascade delete
// backend calls succeed. Qdrant answers the collection existence check and any
// points/delete; ES answers _delete_by_query.
func testDeleteConfig(t *testing.T) config.Config {
	t.Helper()
	cfg := testAuthConfig()
	qdrantSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"result":{}}`))
	}))
	esSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"took":1}`))
	}))
	t.Cleanup(func() {
		qdrantSrv.Close()
		esSrv.Close()
	})
	cfg.StoreEndpoint = qdrantSrv.URL
	cfg.StoreCollection = "documents"
	cfg.ESAddress = esSrv.URL
	cfg.ESIndex = "documents_text"
	return cfg
}

func TestHandleDocument_DeleteSuccessRemovesRegistry(t *testing.T) {
	store := newFakeDocStore()
	seedDoc(store, "acme", "doc-1", "internal")
	handler := handleDocument(testDeleteConfig(t), testQueryService(), noopObjectStore{}, store, nil)

	rec := doRequest(handler, http.MethodDelete, "/v1/documents/doc-1", nil, ctxWithRole("acme", "user", "query", "upload"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if _, found, _ := store.Get(context.Background(), "acme", "doc-1"); found {
		t.Fatal("doc must be removed from registry after delete")
	}
}

func TestHandleDocument_DeleteCrossTenant404(t *testing.T) {
	store := newFakeDocStore()
	seedDoc(store, "other", "doc-1", "public")
	handler := handleDocument(testDeleteConfig(t), testQueryService(), noopObjectStore{}, store, nil)

	rec := doRequest(handler, http.MethodDelete, "/v1/documents/doc-1", nil, ctxWithRole("acme", "admin", "query", "upload"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant delete must 404, got %d", rec.Code)
	}
	if _, found, _ := store.Get(context.Background(), "other", "doc-1"); !found {
		t.Fatal("another tenant's doc must survive")
	}
}

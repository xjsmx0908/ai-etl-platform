package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"ai-etl-pipeline/internal/releasecenter"
)

type releaseRequestListerStub struct {
	requests []releasecenter.ReleaseRequest
	tenantID string
	err      error
}

type releaseOverviewStub struct {
	items    []releasecenter.OverviewItem
	tenantID string
}

func (s *releaseOverviewStub) ListOverview(_ context.Context, tenantID string, _ int) ([]releasecenter.OverviewItem, error) {
	s.tenantID = tenantID
	return s.items, nil
}

func (s *releaseRequestListerStub) ListRequests(_ context.Context, tenantID string, _ int) ([]releasecenter.ReleaseRequest, error) {
	s.tenantID = tenantID
	return s.requests, s.err
}

func TestReleaseCenterQueueIsAdminAndTenantScoped(t *testing.T) {
	store := &releaseRequestListerStub{requests: []releasecenter.ReleaseRequest{{
		ID: "request-1", TenantID: "acme", DocumentID: "doc-1",
		State: releasecenter.RequestApprovalPending, RequiredApprovals: 1,
		CreatedAt: time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC),
	}}}
	handler := handleReleaseCenterRequests(store)

	denied := doRequest(handler, http.MethodGet, "/v1/release-center/requests", nil, ctxWithRole("acme", "user", "query"))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("user status=%d body=%s", denied.Code, denied.Body.String())
	}

	allowed := doRequest(handler, http.MethodGet, "/v1/release-center/requests", nil, ctxWithRole("acme", "admin", "admin"))
	if allowed.Code != http.StatusOK || store.tenantID != "acme" {
		t.Fatalf("admin status=%d tenant=%q body=%s", allowed.Code, store.tenantID, allowed.Body.String())
	}
	var body struct {
		Items []releasecenter.ReleaseRequest `json:"items"`
	}
	if err := json.NewDecoder(allowed.Body).Decode(&body); err != nil || len(body.Items) != 1 || body.Items[0].ID != "request-1" {
		t.Fatalf("response=%+v err=%v", body, err)
	}
}

func TestReleaseCenterOverviewIsAdminAndTenantScoped(t *testing.T) {
	store := &releaseOverviewStub{items: []releasecenter.OverviewItem{{DocumentID: "doc-1", State: "approval_pending", ApprovedDecisions: 1, RequiredApprovals: 2}}}
	handler := handleReleaseCenterOverview(store)
	denied := doRequest(handler, http.MethodGet, "/v1/release-center/overview", nil, ctxWithRole("acme", "user", "query"))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("user status=%d body=%s", denied.Code, denied.Body.String())
	}
	allowed := doRequest(handler, http.MethodGet, "/v1/release-center/overview", nil, ctxWithRole("acme", "admin", "admin"))
	if allowed.Code != http.StatusOK || store.tenantID != "acme" {
		t.Fatalf("admin status=%d tenant=%q body=%s", allowed.Code, store.tenantID, allowed.Body.String())
	}
	var body struct{ Items []releasecenter.OverviewItem `json:"items"` }
	if err := json.NewDecoder(allowed.Body).Decode(&body); err != nil || len(body.Items) != 1 || body.Items[0].State != "approval_pending" {
		t.Fatalf("response=%+v err=%v", body, err)
	}
}

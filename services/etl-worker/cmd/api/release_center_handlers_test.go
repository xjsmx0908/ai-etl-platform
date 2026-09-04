package main

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"ai-etl-pipeline/internal/agent"
	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/publicationworkflow"
	"ai-etl-pipeline/internal/releasecenter"
	"ai-etl-pipeline/internal/store"
)

type releaseReviewServiceStub struct {
	runID      string
	assessment publicationworkflow.Assessment
	err        error
}

func (s releaseReviewServiceStub) ReviewPublication(_ context.Context, _ agent.Actor, _ string) (string, publicationworkflow.Assessment, error) {
	return s.runID, s.assessment, s.err
}

type releaseChunkReaderStub struct {
	chunks []store.StoredChunk
	err    error
}

func (s releaseChunkReaderStub) ListChunksByDoc(context.Context, string, string, []string) ([]store.StoredChunk, error) {
	return s.chunks, s.err
}

func TestReleaseCenterReviewerAnalyzesOnlyExactCandidateChunks(t *testing.T) {
	candidate := publicationworkflow.Candidate{DocumentID: "doc-1", DocumentVersionID: "job-new", GenerationID: "gen-new", ExpectedChunkCount: 1, ExpectedChunkDigest: "sha256:x", ReleaseRevision: 2}
	reviewer := releaseCenterReviewer{
		service:   releaseReviewServiceStub{runID: "run-1", assessment: publicationworkflow.Assessment{Ready: true, Candidate: &candidate}},
		documents: &fakeDocStore{docs: map[string]docstore.Document{"acme/doc-1": {TenantID: "acme", DocID: "doc-1", Permission: "internal"}}},
		chunks: releaseChunkReaderStub{chunks: []store.StoredChunk{
			{ChunkID: "old", DocumentVersionID: "job-old", GenerationID: "gen-old", Content: "password=legacy"},
			{ChunkID: "new", DocumentVersionID: "job-new", GenerationID: "gen-new", Content: "正常内容"},
		}},
	}
	got, err := reviewer.Review(context.Background(), publicationworkflow.Actor{TenantID: "acme"}, "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" || got.Recommendation != "publish" || len(got.Findings) != 0 {
		t.Fatalf("old generation leaked into review: %+v", got)
	}
}

func TestReleaseCenterReviewerFailsClosedWhenExactCandidateContentMissing(t *testing.T) {
	candidate := publicationworkflow.Candidate{DocumentID: "doc-1", DocumentVersionID: "job-new", GenerationID: "gen-new", ExpectedChunkCount: 1, ExpectedChunkDigest: "sha256:x", ReleaseRevision: 2}
	reviewer := releaseCenterReviewer{
		service:   releaseReviewServiceStub{runID: "run-2", assessment: publicationworkflow.Assessment{Ready: true, Candidate: &candidate}},
		documents: &fakeDocStore{docs: map[string]docstore.Document{"acme/doc-1": {TenantID: "acme", DocID: "doc-1", Permission: "internal"}}},
		chunks:    releaseChunkReaderStub{chunks: []store.StoredChunk{{ChunkID: "old", DocumentVersionID: "job-old", GenerationID: "gen-old", Content: "正常内容"}}},
	}
	got, err := reviewer.Review(context.Background(), publicationworkflow.Actor{TenantID: "acme"}, "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.Recommendation != "manual_review" || got.RiskLevel != releasecenter.RiskHigh {
		t.Fatalf("missing exact candidate content was not failed closed: %+v", got)
	}
}

func TestReleaseCenterReviewerEscalatesConfidentialSensitiveContent(t *testing.T) {
	candidate := publicationworkflow.Candidate{DocumentID: "doc-1", DocumentVersionID: "job-1", GenerationID: "gen-1", ExpectedChunkCount: 1, ExpectedChunkDigest: "sha256:x", ReleaseRevision: 1}
	reviewer := releaseCenterReviewer{
		service:   releaseReviewServiceStub{runID: "run-3", assessment: publicationworkflow.Assessment{Ready: true, Candidate: &candidate}},
		documents: &fakeDocStore{docs: map[string]docstore.Document{"acme/doc-1": {TenantID: "acme", DocID: "doc-1", Permission: "confidential"}}},
		chunks:    releaseChunkReaderStub{chunks: []store.StoredChunk{{ChunkID: "c1", DocumentVersionID: "job-1", GenerationID: "gen-1", Content: "手机号 13800138000"}}},
	}
	got, err := reviewer.Review(context.Background(), publicationworkflow.Actor{TenantID: "acme"}, "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "completed" || got.Recommendation != "publish" || got.RiskLevel != releasecenter.RiskHigh || len(got.Findings) != 1 {
		t.Fatalf("confidential sensitive content handling incorrect: %+v", got)
	}
}

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
	var body struct {
		Items []releasecenter.OverviewItem `json:"items"`
	}
	if err := json.NewDecoder(allowed.Body).Decode(&body); err != nil || len(body.Items) != 1 || body.Items[0].State != "approval_pending" {
		t.Fatalf("response=%+v err=%v", body, err)
	}
}

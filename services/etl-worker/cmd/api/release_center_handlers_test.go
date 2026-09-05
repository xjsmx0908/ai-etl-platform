package main

import (
	"context"
	"encoding/json"
	"errors"
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

type semanticReviewerStub struct {
	result releasecenter.SemanticReviewResult
	err    error
}

func (s semanticReviewerStub) Review(context.Context, releasecenter.SemanticReviewInput) (releasecenter.SemanticReviewResult, error) {
	return s.result, s.err
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

func TestReleaseCenterReviewerMergesValidatedSemanticReview(t *testing.T) {
	candidate := publicationworkflow.Candidate{DocumentID: "doc-1", DocumentVersionID: "job-1", GenerationID: "gen-1", ExpectedChunkCount: 1, ExpectedChunkDigest: "sha256:x", ReleaseRevision: 1}
	reviewer := releaseCenterReviewer{
		service:   releaseReviewServiceStub{runID: "run-semantic", assessment: publicationworkflow.Assessment{Ready: true, Candidate: &candidate}},
		documents: &fakeDocStore{docs: map[string]docstore.Document{"acme/doc-1": {TenantID: "acme", DocID: "doc-1", Permission: "internal", KnowledgeSpaceID: "production"}}},
		chunks:    releaseChunkReaderStub{chunks: []store.StoredChunk{{ChunkID: "c1", DocumentVersionID: "job-1", GenerationID: "gen-1", Content: "业务内容"}}},
		semantic:  semanticReviewerStub{result: releasecenter.SemanticReviewResult{Status: "completed", Recommendation: "needs_info", RiskLevel: releasecenter.RiskHigh, Summary: "缺少合规说明", Model: "review-model", PromptVersion: "semantic-v2", Findings: []releasecenter.Finding{{Code: "compliance_gap", Severity: "high", Summary: "缺少合规说明", EvidenceRef: "c1"}}}},
	}
	got, err := reviewer.Review(context.Background(), publicationworkflow.Actor{TenantID: "acme"}, "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Recommendation != "needs_info" || got.RiskLevel != releasecenter.RiskHigh || got.Model != "review-model" || got.PromptVersion != "semantic-v2" || len(got.Findings) != 1 {
		t.Fatalf("semantic result was not merged: %+v", got)
	}
}

func TestReleaseCenterReviewerFailsClosedOnSemanticReviewError(t *testing.T) {
	candidate := publicationworkflow.Candidate{DocumentID: "doc-1", DocumentVersionID: "job-1", GenerationID: "gen-1", ExpectedChunkCount: 1, ExpectedChunkDigest: "sha256:x", ReleaseRevision: 1}
	reviewer := releaseCenterReviewer{
		service:   releaseReviewServiceStub{runID: "run-semantic-error", assessment: publicationworkflow.Assessment{Ready: true, Candidate: &candidate}},
		documents: &fakeDocStore{docs: map[string]docstore.Document{"acme/doc-1": {TenantID: "acme", DocID: "doc-1", Permission: "internal", KnowledgeSpaceID: "production"}}},
		chunks:    releaseChunkReaderStub{chunks: []store.StoredChunk{{ChunkID: "c1", DocumentVersionID: "job-1", GenerationID: "gen-1", Content: "业务内容"}}},
		semantic:  semanticReviewerStub{err: errors.New("model unavailable")},
	}
	got, err := reviewer.Review(context.Background(), publicationworkflow.Actor{TenantID: "acme"}, "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "failed" || got.Recommendation != "manual_review" || got.RiskLevel != releasecenter.RiskHigh {
		t.Fatalf("semantic failure was not failed closed: %+v", got)
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

type releaseReviewStoreStub struct {
	review   releasecenter.ReviewReport
	tenantID string
	reviewID string
}

func (s *releaseReviewStoreStub) GetReview(_ context.Context, tenantID, reviewID string) (releasecenter.ReviewReport, error) {
	s.tenantID, s.reviewID = tenantID, reviewID
	return s.review, nil
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

func TestReleaseCenterReviewReportIsAdminAndTenantScoped(t *testing.T) {
	store := &releaseReviewStoreStub{review: releasecenter.ReviewReport{ID: "review-1", TenantID: "acme", DocumentID: "doc-1", Status: "completed", Recommendation: "publish", RiskLevel: releasecenter.RiskLow}}
	handler := handleReleaseCenterReviewReport(store)
	denied := doRequest(handler, http.MethodGet, "/v1/release-center/review-reports/review-1", nil, ctxWithRole("acme", "user", "query"))
	if denied.Code != http.StatusForbidden {
		t.Fatalf("user status=%d body=%s", denied.Code, denied.Body.String())
	}
	allowed := doRequest(handler, http.MethodGet, "/v1/release-center/review-reports/review-1", nil, ctxWithRole("acme", "admin", "admin"))
	if allowed.Code != http.StatusOK || store.tenantID != "acme" || store.reviewID != "review-1" {
		t.Fatalf("status=%d tenant=%q review=%q body=%s", allowed.Code, store.tenantID, store.reviewID, allowed.Body.String())
	}
	var body struct {
		Review releasecenter.ReviewReport `json:"review"`
	}
	if err := json.NewDecoder(allowed.Body).Decode(&body); err != nil || body.Review.ID != "review-1" {
		t.Fatalf("response=%+v err=%v", body, err)
	}
}

type approvalPolicyManagerStub struct {
	groups                                []releasecenter.ApprovalGroup
	policies                              []releasecenter.ApprovalPolicy
	group                                 releasecenter.ApprovalGroup
	policy                                releasecenter.ApprovalPolicy
	memberTenant, memberGroup, memberUser string
}

func (s *approvalPolicyManagerStub) ResolveApprovalPolicy(context.Context, string, string, string, releasecenter.RiskLevel) (releasecenter.ApprovalPolicy, bool, error) {
	return releasecenter.ApprovalPolicy{}, false, nil
}
func (s *approvalPolicyManagerStub) IsApprovalGroupMember(context.Context, string, string, string) (bool, error) {
	return false, nil
}
func (s *approvalPolicyManagerStub) PutGroup(_ context.Context, group releasecenter.ApprovalGroup) error {
	s.group = group
	return nil
}
func (s *approvalPolicyManagerStub) SetGroupMember(_ context.Context, tenantID, groupID, userID string, _ bool) error {
	s.memberTenant, s.memberGroup, s.memberUser = tenantID, groupID, userID
	return nil
}
func (s *approvalPolicyManagerStub) PutPolicy(_ context.Context, policy releasecenter.ApprovalPolicy) error {
	s.policy = policy
	return nil
}
func (s *approvalPolicyManagerStub) ListGroups(context.Context, string) ([]releasecenter.ApprovalGroup, error) {
	return s.groups, nil
}
func (s *approvalPolicyManagerStub) ListPolicies(context.Context, string) ([]releasecenter.ApprovalPolicy, error) {
	return s.policies, nil
}

func TestReleaseCenterApprovalPolicyHandlersAreTenantScoped(t *testing.T) {
	manager := &approvalPolicyManagerStub{}
	groupsHandler := handleReleaseCenterApprovalGroups(manager)
	created := doRequest(groupsHandler, http.MethodPost, "/v1/release-center/approval-groups", map[string]any{"group_id": "legal", "name": "Legal"}, ctxWithRole("acme", "admin", "admin"))
	if created.Code != http.StatusCreated || manager.group.TenantID != "acme" || manager.group.ID != "legal" || !manager.group.Active {
		t.Fatalf("status=%d group=%+v body=%s", created.Code, manager.group, created.Body.String())
	}
	member := doRequest(groupsHandler, http.MethodPost, "/v1/release-center/approval-groups/legal/members", map[string]any{"user_id": "alice"}, ctxWithRole("acme", "admin", "admin"))
	if member.Code != http.StatusOK || manager.memberTenant != "acme" || manager.memberGroup != "legal" || manager.memberUser != "alice" {
		t.Fatalf("status=%d tenant=%q group=%q user=%q", member.Code, manager.memberTenant, manager.memberGroup, manager.memberUser)
	}
	policiesHandler := handleReleaseCenterApprovalPolicies(manager)
	policy := doRequest(policiesHandler, http.MethodPost, "/v1/release-center/approval-policies", map[string]any{"policy_id": "legal-policy", "approver_group_id": "legal", "required_approvals": 2}, ctxWithRole("acme", "admin", "admin"))
	if policy.Code != http.StatusCreated || manager.policy.TenantID != "acme" || manager.policy.RequiredApprovals != 2 {
		t.Fatalf("status=%d policy=%+v body=%s", policy.Code, manager.policy, policy.Body.String())
	}
}

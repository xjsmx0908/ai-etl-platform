package releasecenter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/notification"
	"ai-etl-pipeline/internal/publicationworkflow"
)

type workflowStub struct {
	assessment publicationworkflow.Assessment
	published  publicationworkflow.Candidate
	key        string
}

func (s *workflowStub) Assess(context.Context, publicationworkflow.Actor, string) (publicationworkflow.Assessment, error) {
	return s.assessment, nil
}

func (s *workflowStub) PublishApproved(_ context.Context, _ publicationworkflow.Actor, candidate publicationworkflow.Candidate, key string) (publicationworkflow.PublicationResult, error) {
	s.published, s.key = candidate, key
	return publicationworkflow.PublicationResult{DocumentID: candidate.DocumentID, PublicationStatus: "published"}, nil
}

type documentStub struct{ document docstore.Document }

func (s documentStub) Get(context.Context, string, string) (docstore.Document, bool, error) {
	return s.document, true, nil
}

type reviewerStub struct {
	review AgentReview
	err    error
}

func (s reviewerStub) Review(context.Context, publicationworkflow.Actor, string) (AgentReview, error) {
	return s.review, s.err
}

type memoryStore struct {
	reviews   map[string]ReviewReport
	requests  map[string]ReleaseRequest
	decisions map[string][]Decision
	stale     map[string]bool
}

func newMemoryStore() *memoryStore {
	return &memoryStore{map[string]ReviewReport{}, map[string]ReleaseRequest{}, map[string][]Decision{}, map[string]bool{}}
}

func (s *memoryStore) SaveReview(_ context.Context, report ReviewReport) error {
	s.reviews[report.ID] = report
	return nil
}
func (s *memoryStore) GetReview(_ context.Context, _, id string) (ReviewReport, error) {
	report, ok := s.reviews[id]
	if !ok {
		return ReviewReport{}, ErrReviewNotFound
	}
	return report, nil
}
func (s *memoryStore) SaveRequest(_ context.Context, request ReleaseRequest) error {
	if existing, ok := s.requests[request.ID]; ok {
		if existing.State == RequestPublished || existing.State == RequestRejected {
			return fmt.Errorf("release request cannot be updated")
		}
		if existing.Candidate != request.Candidate {
			return fmt.Errorf("release request cannot be updated")
		}
	}
	s.requests[request.ID] = request
	return nil
}
func (s *memoryStore) GetRequest(_ context.Context, _, id string) (ReleaseRequest, error) {
	request, ok := s.requests[id]
	if !ok {
		return ReleaseRequest{}, ErrRequestNotFound
	}
	return request, nil
}
func (s *memoryStore) ListRequests(context.Context, string, int) ([]ReleaseRequest, error) {
	return nil, nil
}
func (s *memoryStore) ListReviewJobs(context.Context, int) ([]ReviewJob, error) {
	var jobs []ReviewJob
	for _, request := range s.requests {
		if request.State != RequestNeedsInfo {
			continue
		}
		report := s.reviews[request.ReviewID]
		if report.Status != "expired" {
			continue
		}
		jobs = append(jobs, ReviewJob{
			TenantID: request.TenantID, DocumentID: request.DocumentID,
			RequestedBy: request.RequestedBy, Candidate: request.Candidate,
		})
	}
	return jobs, nil
}
func (s *memoryStore) RecordDecision(_ context.Context, decision Decision) error {
	for _, existing := range s.decisions[decision.RequestID] {
		if existing.DecidedBy == decision.DecidedBy {
			if existing.Decision == decision.Decision && existing.Reason == decision.Reason {
				return nil
			}
			return ErrDecisionConflict
		}
	}
	s.decisions[decision.RequestID] = append(s.decisions[decision.RequestID], decision)
	return nil
}
func (s *memoryStore) ListDecisions(_ context.Context, _, id string) ([]Decision, error) {
	return append([]Decision(nil), s.decisions[id]...), nil
}
func (s *memoryStore) SetRequestState(_ context.Context, _, id string, state RequestState) error {
	request := s.requests[id]
	request.State, request.UpdatedAt = state, time.Now().UTC()
	s.requests[id] = request
	return nil
}
func (s *memoryStore) ReconcileStaleRequests(context.Context, int) ([]ReleaseRequest, error) {
	var out []ReleaseRequest
	for id, request := range s.requests {
		if !s.stale[id] {
			continue
		}
		if request.State != RequestApprovalPending && request.State != RequestManualException {
			continue
		}
		request.State = RequestNeedsInfo
		s.requests[id] = request
		out = append(out, request)
	}
	return out, nil
}

func (s *memoryStore) ExpireDueReviews(_ context.Context, now time.Time, _ int) ([]ReleaseRequest, error) {
	var out []ReleaseRequest
	for id, request := range s.requests {
		if request.State != RequestApprovalPending && request.State != RequestManualException && request.State != RequestNeedsInfo {
			continue
		}
		report, ok := s.reviews[request.ReviewID]
		if !ok || (report.Status != "completed" && report.Status != "failed") {
			continue
		}
		if report.ExpiresAt.IsZero() || now.Before(report.ExpiresAt) {
			continue
		}
		report.Status = "expired"
		s.reviews[report.ID] = report
		request.State = RequestNeedsInfo
		request.UpdatedAt = now
		s.requests[id] = request
		out = append(out, request)
	}
	return out, nil
}

func (s *memoryStore) PurgeExpiredReviews(_ context.Context, now time.Time, retention time.Duration, _ int) (int, error) {
	if retention <= 0 {
		return 0, nil
	}
	cutoff := now.Add(-retention)
	referenced := map[string]struct{}{}
	for _, request := range s.requests {
		referenced[request.ReviewID] = struct{}{}
	}
	deleted := 0
	for id, report := range s.reviews {
		if report.Status != "expired" || report.ExpiresAt.IsZero() || report.ExpiresAt.After(cutoff) {
			continue
		}
		if _, ok := referenced[id]; ok {
			continue
		}
		delete(s.reviews, id)
		deleted++
	}
	return deleted, nil
}

func (s *memoryStore) ResetDecisions(_ context.Context, _, requestID string) error {
	delete(s.decisions, requestID)
	return nil
}

func readyCandidate() publicationworkflow.Candidate {
	return publicationworkflow.Candidate{DocumentID: "doc-1", DocumentVersionID: "job-1", GenerationID: "gen-1", ExpectedChunkCount: 3, ExpectedChunkDigest: "sha256:ready", ReleaseRevision: 1}
}

func TestCoordinatorPersistsVersionBoundReviewAndRequest(t *testing.T) {
	candidate := readyCandidate()
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newMemoryStore()
	coordinator := NewCoordinator(workflow, documentStub{docstore.Document{TenantID: "acme", DocID: candidate.DocumentID, Permission: "internal", UploadedBy: "uploader"}}, reviewerStub{review: AgentReview{RunID: "run-1", Status: "completed", Recommendation: "publish", RiskLevel: RiskLow, Summary: "checks passed", Model: "governance-agent", PromptVersion: "document-review-v1"}}, store)
	report, request, err := coordinator.StartManagedReview(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "uploader", Role: "admin"}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.GenerationID != candidate.GenerationID || request.ReviewID != report.ID || request.RequiredApprovals != 1 || request.State != RequestApprovalPending {
		t.Fatalf("report=%+v request=%+v", report, request)
	}
	if report.ID == "" || request.ID == "" || len(store.reviews) != 1 || len(store.requests) != 1 {
		t.Fatalf("records were not persisted idempotently: reports=%d requests=%d", len(store.reviews), len(store.requests))
	}
}

func TestCoordinatorCanonicalizesSuccessfulAgentStatus(t *testing.T) {
	candidate := readyCandidate()
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newMemoryStore()
	coordinator := NewCoordinator(workflow, documentStub{docstore.Document{TenantID: "acme", DocID: candidate.DocumentID, Permission: "internal", UploadedBy: "uploader"}}, reviewerStub{review: AgentReview{
		Status: "success", Recommendation: "publish", RiskLevel: RiskLow,
	}}, store)
	report, request, err := coordinator.StartManagedReview(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "uploader", Role: "admin"}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || request.State != RequestApprovalPending {
		t.Fatalf("report=%+v request=%+v", report, request)
	}
}

func TestCoordinatorUsesConfiguredApprovalPolicy(t *testing.T) {
	candidate := readyCandidate()
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	policyStore := NewMemoryApprovalPolicyStore()
	if err := policyStore.PutGroup(nil, ApprovalGroup{ID: "legal-reviewers", TenantID: "acme", Name: "Legal reviewers", Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := policyStore.PutPolicy(nil, ApprovalPolicy{ID: "legal-policy", TenantID: "acme", KnowledgeSpaceID: "production", Permission: "internal", RequiredApprovals: 2, ApproverGroupID: "legal-reviewers", Active: true}); err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore()
	documents := documentStub{docstore.Document{TenantID: "acme", DocID: candidate.DocumentID, Permission: "internal", KnowledgeSpaceID: "production", UploadedBy: "uploader"}}
	coordinator := NewCoordinator(workflow, documents, reviewerStub{review: AgentReview{Status: "completed", Recommendation: "publish", RiskLevel: RiskLow}}, store, policyStore)
	_, request, err := coordinator.StartManagedReview(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "uploader", Role: "admin"}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if request.PolicyID != "legal-policy" || request.ApproverGroupID != "legal-reviewers" || request.RequiredApprovals != 2 {
		t.Fatalf("request=%+v", request)
	}
}

func TestCoordinatorConfiguredPolicyCannotLowerConfidentialApproval(t *testing.T) {
	candidate := readyCandidate()
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	policyStore := NewMemoryApprovalPolicyStore()
	if err := policyStore.PutGroup(nil, ApprovalGroup{ID: "general", TenantID: "acme", Name: "General", Active: true}); err != nil {
		t.Fatal(err)
	}
	if err := policyStore.PutPolicy(nil, ApprovalPolicy{ID: "too-permissive", TenantID: "acme", Permission: "confidential", RequiredApprovals: 1, ApproverGroupID: "general", Active: true}); err != nil {
		t.Fatal(err)
	}
	store := newMemoryStore()
	documents := documentStub{docstore.Document{TenantID: "acme", DocID: candidate.DocumentID, Permission: "confidential", KnowledgeSpaceID: "production", UploadedBy: "uploader"}}
	coordinator := NewCoordinator(workflow, documents, reviewerStub{review: AgentReview{Status: "completed", Recommendation: "publish", RiskLevel: RiskLow}}, store, policyStore)
	_, request, err := coordinator.StartManagedReview(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "uploader", Role: "admin"}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if request.RequiredApprovals != 2 {
		t.Fatalf("configured policy lowered confidential requirement: %+v", request)
	}
}

func TestCoordinatorEscalatesConfidentialAndAgentFailure(t *testing.T) {
	candidate := readyCandidate()
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newMemoryStore()
	coordinator := NewCoordinator(workflow, documentStub{docstore.Document{TenantID: "acme", DocID: candidate.DocumentID, Permission: "confidential", UploadedBy: "uploader"}}, reviewerStub{err: errors.New("agent offline")}, store)
	report, request, err := coordinator.StartManagedReview(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "uploader", Role: "admin"}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "failed" || report.RiskLevel != RiskHigh || request.State != RequestManualException || request.RequiredApprovals != 2 {
		t.Fatalf("report=%+v request=%+v", report, request)
	}
}

func TestCoordinatorTreatsFailedAgentStatusAsManualException(t *testing.T) {
	candidate := readyCandidate()
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newMemoryStore()
	coordinator := NewCoordinator(workflow, documentStub{docstore.Document{TenantID: "acme", DocID: candidate.DocumentID, Permission: "internal", UploadedBy: "uploader"}}, reviewerStub{review: AgentReview{
		Status: "failed", Recommendation: "publish", RiskLevel: RiskLow, Summary: "agent returned a failed status",
	}}, store)
	_, request, err := coordinator.StartManagedReview(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "uploader", Role: "admin"}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if request.State != RequestManualException {
		t.Fatalf("failed Agent status was treated as successful review: request=%+v", request)
	}
}

func TestCoordinatorFailsClosedOnIncompleteAgentEvidence(t *testing.T) {
	candidate := readyCandidate()
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newMemoryStore()
	coordinator := NewCoordinator(workflow, documentStub{docstore.Document{TenantID: "acme", DocID: candidate.DocumentID, Permission: "internal"}}, reviewerStub{review: AgentReview{
		Status: "completed", Recommendation: "", RiskLevel: "untrusted-risk",
	}}, store)
	report, request, err := coordinator.StartManagedReview(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "uploader", Role: "admin"}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "failed" || request.State != RequestManualException {
		t.Fatalf("incomplete Agent evidence was not failed closed: report=%+v request=%+v", report, request)
	}
}

func TestCoordinatorDoesNotCreateApprovalForAgentNonPublishRecommendation(t *testing.T) {
	candidate := readyCandidate()
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newMemoryStore()
	coordinator := NewCoordinator(workflow, documentStub{docstore.Document{TenantID: "acme", DocID: candidate.DocumentID, Permission: "internal"}}, reviewerStub{review: AgentReview{
		Status: "completed", Recommendation: "needs_info", RiskLevel: RiskLow, Summary: "document needs business clarification",
	}}, store)
	report, request, err := coordinator.StartManagedReview(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "uploader", Role: "admin"}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "completed" || request.State != RequestNeedsInfo {
		t.Fatalf("non-publish recommendation entered approval path: report=%+v request=%+v", report, request)
	}
}

func TestApprovalPublishesOnlyAfterRequiredDistinctDecisions(t *testing.T) {
	candidate := readyCandidate()
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newMemoryStore()
	now := time.Now().UTC()
	report := ReviewReport{ID: "review-1", TenantID: "acme", DocumentID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, ReleaseRevision: candidate.ReleaseRevision, Status: "completed", Recommendation: "publish", RiskLevel: RiskHigh, CreatedAt: now}
	request := ReleaseRequest{ID: "request-1", TenantID: "acme", DocumentID: candidate.DocumentID, Candidate: candidate, ReviewID: report.ID, RequiredApprovals: 2, State: RequestApprovalPending, RequestedBy: "uploader", CreatedAt: now, UpdatedAt: now}
	store.reviews[report.ID], store.requests[request.ID] = report, request
	approval := NewApprovalService(workflow, store)

	first, err := approval.Decide(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "admin-1", Role: "admin"}, request.ID, "approved", "")
	if err != nil || first.Request.State != RequestApprovalPending || workflow.key != "" {
		t.Fatalf("first=%+v key=%q err=%v", first, workflow.key, err)
	}
	second, err := approval.Decide(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "admin-2", Role: "admin"}, request.ID, "approved", "")
	if err != nil || second.Request.State != RequestPublished || workflow.key != request.ID {
		t.Fatalf("second=%+v key=%q err=%v", second, workflow.key, err)
	}
}

func TestApprovalFailsClosedWhenConfiguredGroupStoreUnavailable(t *testing.T) {
	candidate := readyCandidate()
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newMemoryStore()
	now := time.Now().UTC()
	report := ReviewReport{ID: "review-group", TenantID: "acme", DocumentID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, ReleaseRevision: candidate.ReleaseRevision, Status: "completed", Recommendation: "publish", RiskLevel: RiskLow, CreatedAt: now}
	request := ReleaseRequest{ID: "request-group", TenantID: "acme", DocumentID: candidate.DocumentID, Candidate: candidate, ReviewID: report.ID, ApproverGroupID: "legal", RequiredApprovals: 1, State: RequestApprovalPending, RequestedBy: "uploader", CreatedAt: now, UpdatedAt: now}
	store.reviews[report.ID], store.requests[request.ID] = report, request
	_, err := NewApprovalService(workflow, store).Decide(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "admin", Role: "admin"}, request.ID, "approved", "")
	if err == nil || workflow.key != "" {
		t.Fatalf("group-constrained approval was not failed closed: err=%v key=%q", err, workflow.key)
	}
}

func TestApprovalRejectsNonAdminAndStaleReview(t *testing.T) {
	candidate := readyCandidate()
	newCandidate := candidate
	newCandidate.GenerationID = "gen-2"
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &newCandidate}}
	store := newMemoryStore()
	now := time.Now().UTC()
	report := ReviewReport{ID: "review-1", TenantID: "acme", DocumentID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, ReleaseRevision: candidate.ReleaseRevision, Status: "completed", Recommendation: "publish", RiskLevel: RiskLow, CreatedAt: now}
	store.reviews[report.ID] = report
	store.requests["request-1"] = ReleaseRequest{ID: "request-1", TenantID: "acme", DocumentID: candidate.DocumentID, Candidate: candidate, ReviewID: report.ID, RequiredApprovals: 1, State: RequestApprovalPending}
	approval := NewApprovalService(workflow, store)
	if _, err := approval.Decide(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "user", Role: "user"}, "request-1", "approved", ""); !errors.Is(err, publicationworkflow.ErrAdminRequired) {
		t.Fatalf("non-admin error=%v", err)
	}
	if _, err := approval.Decide(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "admin", Role: "admin"}, "request-1", "approved", ""); !errors.Is(err, ErrStaleReview) {
		t.Fatalf("stale error=%v", err)
	}
}

func TestManualExceptionRequiresAuditedReason(t *testing.T) {
	candidate := readyCandidate()
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newMemoryStore()
	now := time.Now().UTC()
	report := ReviewReport{ID: "review-manual", TenantID: "acme", DocumentID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, ReleaseRevision: candidate.ReleaseRevision, Status: "failed", Recommendation: "manual_review", RiskLevel: RiskLow, CreatedAt: now}
	store.reviews[report.ID] = report
	store.requests["request-manual"] = ReleaseRequest{ID: "request-manual", TenantID: "acme", DocumentID: candidate.DocumentID, Candidate: candidate, ReviewID: report.ID, RequiredApprovals: 1, State: RequestManualException, RequestedBy: "release-center-agent", CreatedAt: now, UpdatedAt: now}
	approval := NewApprovalService(workflow, store)
	actor := publicationworkflow.Actor{TenantID: "acme", UserID: "admin-1", Role: "admin"}
	if _, err := approval.Decide(context.Background(), actor, "request-manual", "approved", ""); err == nil {
		t.Fatal("manual exception without reason was accepted")
	}
	result, err := approval.Decide(context.Background(), actor, "request-manual", "approved", "Agent 服务不可用，已人工核对索引和治理字段")
	if err != nil || result.Request.State != RequestPublished {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestCoordinatorEnqueuesContentSafeApprovalNotification(t *testing.T) {
	candidate := readyCandidate()
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newMemoryStore()
	notes := notification.NewMemoryStore()
	coordinator := NewCoordinator(workflow, documentStub{docstore.Document{TenantID: "acme", DocID: candidate.DocumentID, Permission: "internal", UploadedBy: "uploader"}}, reviewerStub{review: AgentReview{
		RunID: "run-1", Status: "completed", Recommendation: "publish", RiskLevel: RiskLow,
		Summary: "正文含身份证 110101199001011234", Findings: []Finding{{Code: "sensitive_data_detected", Severity: "high", Summary: "secret finding", EvidenceRef: "chunk:1"}},
		Model: "governance-agent", PromptVersion: "document-review-v1",
	}}, store).WithNotifier(notes)
	_, request, err := coordinator.StartManagedReview(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "uploader", Role: "admin"}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	events := notes.Events()
	if len(events) != 1 || events[0].Type != notification.EventRequestOpened || events[0].SourceID != request.ID {
		t.Fatalf("events=%+v request=%+v", events, request)
	}
	if events[0].Payload["summary"] != "" || events[0].Payload["findings"] != "" || events[0].Payload["state"] != string(RequestApprovalPending) {
		t.Fatalf("payload leaked or missing state: %+v", events[0].Payload)
	}
	if events[0].Payload["decision_path"] != "/v1/release-center/workflow/decision" {
		t.Fatalf("missing decision path: %+v", events[0].Payload)
	}
	if !strings.Contains(events[0].Payload["public_path"], "document="+candidate.DocumentID) || !strings.Contains(events[0].Payload["public_path"], "request="+request.ID) {
		t.Fatalf("public path missing deep link: %+v", events[0].Payload)
	}
}

func TestApprovalServiceNotifiesRejectAndPublish(t *testing.T) {
	candidate := readyCandidate()
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newMemoryStore()
	now := time.Now().UTC()
	report := ReviewReport{ID: "review-1", TenantID: "acme", DocumentID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, ReleaseRevision: candidate.ReleaseRevision, Status: "completed", Recommendation: "publish", RiskLevel: RiskLow, CreatedAt: now}
	store.reviews[report.ID] = report
	store.requests["request-1"] = ReleaseRequest{ID: "request-1", TenantID: "acme", DocumentID: candidate.DocumentID, Candidate: candidate, ReviewID: report.ID, RequiredApprovals: 1, State: RequestApprovalPending, RequestedBy: "uploader", CreatedAt: now, UpdatedAt: now}
	notes := notification.NewMemoryStore()
	approval := NewApprovalService(workflow, store).WithNotifier(notes)
	rejected, err := approval.Decide(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "admin-1", Role: "admin"}, "request-1", "rejected", "not ready")
	if err != nil || rejected.Request.State != RequestRejected {
		t.Fatalf("rejected=%+v err=%v", rejected, err)
	}
	types := eventTypes(notes)
	if !containsEvent(types, notification.EventDecisionRecorded) || !containsEvent(types, notification.EventRequestStateChanged) {
		t.Fatalf("reject events=%v", types)
	}

	store.requests["request-2"] = ReleaseRequest{ID: "request-2", TenantID: "acme", DocumentID: candidate.DocumentID, Candidate: candidate, ReviewID: report.ID, RequiredApprovals: 1, State: RequestApprovalPending, RequestedBy: "uploader", CreatedAt: now, UpdatedAt: now}
	notes = notification.NewMemoryStore()
	approval = NewApprovalService(workflow, store).WithNotifier(notes)
	published, err := approval.Decide(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "admin-1", Role: "admin"}, "request-2", "approved", "")
	if err != nil || published.Request.State != RequestPublished {
		t.Fatalf("published=%+v err=%v", published, err)
	}
	types = eventTypes(notes)
	if !containsEvent(types, notification.EventDecisionRecorded) || !containsEvent(types, notification.EventRequestStateChanged) {
		t.Fatalf("publish events=%v", types)
	}
}

func TestCoordinatorNotifiesStaleRequestReconciliation(t *testing.T) {
	store := newMemoryStore()
	notes := notification.NewMemoryStore()
	candidate := readyCandidate()
	now := time.Now().UTC()
	store.stale["request-stale"] = true
	store.requests["request-stale"] = ReleaseRequest{ID: "request-stale", TenantID: "acme", DocumentID: candidate.DocumentID, Candidate: candidate, ReviewID: "review-1", RequiredApprovals: 1, State: RequestApprovalPending, CreatedAt: now, UpdatedAt: now}
	coordinator := NewCoordinator(&workflowStub{}, documentStub{}, reviewerStub{}, store).WithNotifier(notes)
	if err := coordinator.RunPendingReviews(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "system", Role: "admin"}, 10); err != nil {
		t.Fatal(err)
	}
	if store.requests["request-stale"].State != RequestNeedsInfo {
		t.Fatalf("state=%s", store.requests["request-stale"].State)
	}
	events := notes.Events()
	if len(events) != 1 || events[0].Type != notification.EventRequestStateChanged || events[0].Payload["state"] != string(RequestNeedsInfo) {
		t.Fatalf("events=%+v", events)
	}
}

func eventTypes(store *notification.MemoryStore) []string {
	var out []string
	for _, event := range store.Events() {
		out = append(out, event.Type)
	}
	return out
}

func containsEvent(types []string, want string) bool {
	for _, item := range types {
		if item == want {
			return true
		}
	}
	return false
}

type callReviewer struct {
	reviews []AgentReview
	calls   int
}

func (s *callReviewer) Review(context.Context, publicationworkflow.Actor, string) (AgentReview, error) {
	if len(s.reviews) == 0 {
		return AgentReview{}, fmt.Errorf("no review configured")
	}
	idx := s.calls
	if idx >= len(s.reviews) {
		idx = len(s.reviews) - 1
	}
	s.calls++
	return s.reviews[idx], nil
}

func TestCoordinatorSetsReviewExpiryWhenTTLConfigured(t *testing.T) {
	candidate := readyCandidate()
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newMemoryStore()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	coordinator := NewCoordinator(workflow, documentStub{docstore.Document{TenantID: "acme", DocID: candidate.DocumentID, Permission: "internal", UploadedBy: "uploader"}}, reviewerStub{review: AgentReview{Status: "completed", Recommendation: "publish", RiskLevel: RiskLow}}, store).WithReviewTTL(168 * time.Hour)
	coordinator.now = func() time.Time { return now }
	report, _, err := coordinator.StartManagedReview(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "uploader", Role: "admin"}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if report.ExpiresAt.IsZero() || !report.ExpiresAt.Equal(now.Add(168*time.Hour)) {
		t.Fatalf("expires_at=%v", report.ExpiresAt)
	}
}

func TestCoordinatorOmitsExpiryWhenTTLDisabled(t *testing.T) {
	candidate := readyCandidate()
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newMemoryStore()
	coordinator := NewCoordinator(workflow, documentStub{docstore.Document{TenantID: "acme", DocID: candidate.DocumentID, Permission: "internal", UploadedBy: "uploader"}}, reviewerStub{review: AgentReview{Status: "completed", Recommendation: "publish", RiskLevel: RiskLow}}, store)
	report, _, err := coordinator.StartManagedReview(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "uploader", Role: "admin"}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if !report.ExpiresAt.IsZero() {
		t.Fatalf("expires_at=%v", report.ExpiresAt)
	}
}

func TestCoordinatorExpiresDueReviewsAndLeavesPublishedEvidence(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	candidate := readyCandidate()
	pending := ReviewReport{ID: "review-pending", TenantID: "acme", DocumentID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, ReleaseRevision: candidate.ReleaseRevision, Status: "completed", Recommendation: "publish", RiskLevel: RiskLow, CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Minute)}
	published := ReviewReport{ID: "review-published", TenantID: "acme", DocumentID: "doc-published", DocumentVersionID: "job-p", GenerationID: "gen-p", ReleaseRevision: 1, Status: "completed", Recommendation: "publish", RiskLevel: RiskLow, CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Minute)}
	store.reviews[pending.ID] = pending
	store.reviews[published.ID] = published
	store.requests["request-pending"] = ReleaseRequest{ID: "request-pending", TenantID: "acme", DocumentID: candidate.DocumentID, Candidate: candidate, ReviewID: pending.ID, RequiredApprovals: 1, State: RequestApprovalPending, RequestedBy: "uploader", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour)}
	store.requests["request-published"] = ReleaseRequest{ID: "request-published", TenantID: "acme", DocumentID: "doc-published", Candidate: publicationworkflow.Candidate{DocumentID: "doc-published", DocumentVersionID: "job-p", GenerationID: "gen-p", ExpectedChunkCount: 1, ExpectedChunkDigest: "sha256:p", ReleaseRevision: 1}, ReviewID: published.ID, RequiredApprovals: 1, State: RequestPublished, RequestedBy: "uploader", CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now.Add(-time.Hour)}
	expired, err := store.ExpireDueReviews(context.Background(), now, 10)
	if err != nil || len(expired) != 1 || expired[0].ID != "request-pending" {
		t.Fatalf("expired=%+v err=%v", expired, err)
	}
	if store.reviews[pending.ID].Status != "expired" {
		t.Fatalf("pending status=%s", store.reviews[pending.ID].Status)
	}
	if store.requests["request-pending"].State != RequestNeedsInfo {
		t.Fatalf("pending state=%s", store.requests["request-pending"].State)
	}
	if store.reviews[published.ID].Status != "completed" || store.requests["request-published"].State != RequestPublished {
		t.Fatalf("published was mutated: report=%+v request=%+v", store.reviews[published.ID], store.requests["request-published"])
	}
}

func TestCoordinatorRereviewsExpiredCandidate(t *testing.T) {
	candidate := readyCandidate()
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newMemoryStore()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	reviewer := &callReviewer{reviews: []AgentReview{
		{Status: "completed", Recommendation: "publish", RiskLevel: RiskLow, Summary: "first"},
		{Status: "completed", Recommendation: "publish", RiskLevel: RiskLow, Summary: "second"},
	}}
	notes := notification.NewMemoryStore()
	coordinator := NewCoordinator(workflow, documentStub{docstore.Document{TenantID: "acme", DocID: candidate.DocumentID, Permission: "internal", UploadedBy: "uploader"}}, reviewer, store).WithNotifier(notes).WithReviewTTL(time.Hour)
	coordinator.now = func() time.Time { return now }
	report, request, err := coordinator.StartManagedReview(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "uploader", Role: "admin"}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	firstID := report.ID
	if err := store.RecordDecision(context.Background(), Decision{ID: "d1", TenantID: "acme", RequestID: request.ID, DecidedBy: "admin-1", Decision: "approved", DecidedAt: now}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Hour)
	if err := coordinator.RunPendingReviews(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "system", Role: "admin"}, 10); err != nil {
		t.Fatal(err)
	}
	if store.reviews[firstID].Status != "expired" {
		t.Fatalf("old status=%s", store.reviews[firstID].Status)
	}
	updated := store.requests[request.ID]
	if updated.ReviewID == firstID || updated.State != RequestApprovalPending {
		t.Fatalf("updated request=%+v", updated)
	}
	if _, ok := store.reviews[updated.ReviewID]; !ok || len(store.reviews) != 2 {
		t.Fatalf("reviews=%v", store.reviews)
	}
	if updated.ReviewID != nextReviewID(firstID) {
		t.Fatalf("review id=%s want %s", updated.ReviewID, nextReviewID(firstID))
	}
	if len(store.decisions[request.ID]) != 0 {
		t.Fatalf("decisions=%v", store.decisions[request.ID])
	}
	if reviewer.calls != 2 {
		t.Fatalf("reviewer calls=%d", reviewer.calls)
	}
	if store.reviews[updated.ReviewID].Summary != "second" {
		t.Fatalf("new report=%+v", store.reviews[updated.ReviewID])
	}
	types := eventTypes(notes)
	if !containsEvent(types, notification.EventRequestOpened) || !containsEvent(types, notification.EventRequestStateChanged) {
		t.Fatalf("events=%v", types)
	}
}

func TestCoordinatorDoesNotRereviewActiveNeedsInfo(t *testing.T) {
	candidate := readyCandidate()
	store := newMemoryStore()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	report := ReviewReport{ID: "review-info", TenantID: "acme", DocumentID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, ReleaseRevision: candidate.ReleaseRevision, Status: "completed", Recommendation: "needs_info", RiskLevel: RiskMedium, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	store.reviews[report.ID] = report
	store.requests[stableID("request", "acme", candidate)] = ReleaseRequest{ID: stableID("request", "acme", candidate), TenantID: "acme", DocumentID: candidate.DocumentID, Candidate: candidate, ReviewID: report.ID, RequiredApprovals: 1, State: RequestNeedsInfo, RequestedBy: "uploader", CreatedAt: now, UpdatedAt: now}
	reviewer := &callReviewer{reviews: []AgentReview{{Status: "completed", Recommendation: "publish", RiskLevel: RiskLow}}}
	coordinator := NewCoordinator(&workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}, documentStub{docstore.Document{TenantID: "acme", DocID: candidate.DocumentID, Permission: "internal"}}, reviewer, store)
	coordinator.now = func() time.Time { return now }
	if err := coordinator.RunPendingReviews(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "system", Role: "admin"}, 10); err != nil {
		t.Fatal(err)
	}
	if reviewer.calls != 0 {
		t.Fatalf("auto rereviewed active needs_info: calls=%d", reviewer.calls)
	}
	if store.requests[stableID("request", "acme", candidate)].ReviewID != report.ID {
		t.Fatalf("review id changed")
	}
}

func TestCoordinatorDoesNotReopenPublishedRequest(t *testing.T) {
	candidate := readyCandidate()
	store := newMemoryStore()
	now := time.Now().UTC()
	report := ReviewReport{ID: "review-pub", TenantID: "acme", DocumentID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, ReleaseRevision: candidate.ReleaseRevision, Status: "completed", Recommendation: "publish", RiskLevel: RiskLow, CreatedAt: now}
	requestID := stableID("request", "acme", candidate)
	store.reviews[report.ID] = report
	store.requests[requestID] = ReleaseRequest{ID: requestID, TenantID: "acme", DocumentID: candidate.DocumentID, Candidate: candidate, ReviewID: report.ID, RequiredApprovals: 1, State: RequestPublished, RequestedBy: "uploader", CreatedAt: now, UpdatedAt: now}
	reviewer := &callReviewer{reviews: []AgentReview{{Status: "completed", Recommendation: "publish", RiskLevel: RiskLow}}}
	coordinator := NewCoordinator(&workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}, documentStub{docstore.Document{TenantID: "acme", DocID: candidate.DocumentID, Permission: "internal"}}, reviewer, store)
	gotReport, gotRequest, err := coordinator.StartManagedReview(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "uploader", Role: "admin"}, candidate.DocumentID)
	if err != nil {
		t.Fatal(err)
	}
	if reviewer.calls != 0 || gotReport.ID != report.ID || gotRequest.State != RequestPublished {
		t.Fatalf("published request was reopened: calls=%d report=%+v request=%+v", reviewer.calls, gotReport, gotRequest)
	}
}

func TestApprovalRejectsExpiredReview(t *testing.T) {
	candidate := readyCandidate()
	workflow := &workflowStub{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newMemoryStore()
	now := time.Now().UTC()
	report := ReviewReport{ID: "review-expired", TenantID: "acme", DocumentID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, ReleaseRevision: candidate.ReleaseRevision, Status: "expired", Recommendation: "publish", RiskLevel: RiskLow, CreatedAt: now.Add(-2 * time.Hour), ExpiresAt: now.Add(-time.Hour)}
	store.reviews[report.ID] = report
	store.requests["request-1"] = ReleaseRequest{ID: "request-1", TenantID: "acme", DocumentID: candidate.DocumentID, Candidate: candidate, ReviewID: report.ID, RequiredApprovals: 1, State: RequestApprovalPending, RequestedBy: "uploader"}
	approval := NewApprovalService(workflow, store)
	if _, err := approval.Decide(context.Background(), publicationworkflow.Actor{TenantID: "acme", UserID: "admin", Role: "admin"}, "request-1", "approved", ""); !errors.Is(err, ErrStaleReview) {
		t.Fatalf("expired error=%v", err)
	}
}

func TestValidateReviewBindingRejectsExpiredStatusAndTime(t *testing.T) {
	candidate := readyCandidate()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	base := ReviewReport{ID: "review-1", Status: "completed", Recommendation: "publish", DocumentID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, ReleaseRevision: candidate.ReleaseRevision}
	expiredStatus := base
	expiredStatus.Status = "expired"
	if err := ValidateReviewBinding(expiredStatus, candidate, now); !errors.Is(err, ErrStaleReview) {
		t.Fatalf("status error=%v", err)
	}
	expiredTime := base
	expiredTime.ExpiresAt = now
	if err := ValidateReviewBinding(expiredTime, candidate, now); !errors.Is(err, ErrStaleReview) {
		t.Fatalf("time error=%v", err)
	}
}

func TestCoordinatorPurgesUnreferencedExpiredReviews(t *testing.T) {
	store := newMemoryStore()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	old := ReviewReport{ID: "review-old", TenantID: "acme", DocumentID: "doc-1", Status: "expired", Recommendation: "publish", RiskLevel: RiskLow, CreatedAt: now.Add(-200 * time.Hour), ExpiresAt: now.Add(-48 * time.Hour)}
	kept := ReviewReport{ID: "review-kept", TenantID: "acme", DocumentID: "doc-2", Status: "expired", Recommendation: "publish", RiskLevel: RiskLow, CreatedAt: now.Add(-200 * time.Hour), ExpiresAt: now.Add(-48 * time.Hour)}
	store.reviews[old.ID] = old
	store.reviews[kept.ID] = kept
	store.requests["request-kept"] = ReleaseRequest{ID: "request-kept", TenantID: "acme", DocumentID: "doc-2", ReviewID: kept.ID, State: RequestPublished, Candidate: publicationworkflow.Candidate{DocumentID: "doc-2", DocumentVersionID: "job-2", GenerationID: "gen-2", ExpectedChunkCount: 1, ExpectedChunkDigest: "sha256:x", ReleaseRevision: 1}}
	coordinator := NewCoordinator(&workflowStub{}, documentStub{}, reviewerStub{}, store).WithReviewRetention(24 * time.Hour)
	coordinator.now = func() time.Time { return now }
	if err := coordinator.RunPendingReviews(context.Background(), publicationworkflow.Actor{UserID: "system", Role: "admin"}, 10); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.reviews[old.ID]; ok {
		t.Fatal("unreferenced expired review was retained")
	}
	if _, ok := store.reviews[kept.ID]; !ok {
		t.Fatal("referenced expired review was purged")
	}
}

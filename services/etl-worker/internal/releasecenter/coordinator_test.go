package releasecenter

import (
	"context"
	"errors"
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
}

func newMemoryStore() *memoryStore {
	return &memoryStore{map[string]ReviewReport{}, map[string]ReleaseRequest{}, map[string][]Decision{}}
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
func (s *memoryStore) ListReviewJobs(context.Context, int) ([]ReviewJob, error) { return nil, nil }
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
		if request.State != RequestApprovalPending && request.State != RequestManualException {
			continue
		}
		request.State = RequestNeedsInfo
		s.requests[id] = request
		out = append(out, request)
	}
	return out, nil
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

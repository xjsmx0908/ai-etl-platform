package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/publicationworkflow"
	"ai-etl-pipeline/internal/releasecenter"
)

type functionalReleaseWorkflow struct {
	assessment publicationworkflow.Assessment
	published  int
}

func (w *functionalReleaseWorkflow) Assess(context.Context, publicationworkflow.Actor, string) (publicationworkflow.Assessment, error) {
	return w.assessment, nil
}

func (w *functionalReleaseWorkflow) PublishApproved(context.Context, publicationworkflow.Actor, publicationworkflow.Candidate, string) (publicationworkflow.PublicationResult, error) {
	w.published++
	return publicationworkflow.PublicationResult{PublicationStatus: "published"}, nil
}

type functionalReleaseDocuments struct{ document docstore.Document }

func (d functionalReleaseDocuments) Get(context.Context, string, string) (docstore.Document, bool, error) {
	return d.document, true, nil
}

type functionalReleaseReviewer struct {
	review releasecenter.AgentReview
	err    error
}

func (r functionalReleaseReviewer) Review(context.Context, publicationworkflow.Actor, string) (releasecenter.AgentReview, error) {
	return r.review, r.err
}

type functionalReleaseStore struct {
	reviews   map[string]releasecenter.ReviewReport
	requests  map[string]releasecenter.ReleaseRequest
	decisions map[string][]releasecenter.Decision
}

func newFunctionalReleaseStore() *functionalReleaseStore {
	return &functionalReleaseStore{
		reviews:   map[string]releasecenter.ReviewReport{},
		requests:  map[string]releasecenter.ReleaseRequest{},
		decisions: map[string][]releasecenter.Decision{},
	}
}

func (s *functionalReleaseStore) SaveReview(_ context.Context, report releasecenter.ReviewReport) error {
	s.reviews[report.ID] = report
	return nil
}

func (s *functionalReleaseStore) GetReview(_ context.Context, tenantID, reviewID string) (releasecenter.ReviewReport, error) {
	review, ok := s.reviews[reviewID]
	if !ok || review.TenantID != tenantID {
		return releasecenter.ReviewReport{}, releasecenter.ErrReviewNotFound
	}
	return review, nil
}

func (s *functionalReleaseStore) SaveRequest(_ context.Context, request releasecenter.ReleaseRequest) error {
	s.requests[request.ID] = request
	return nil
}

func (s *functionalReleaseStore) GetRequest(_ context.Context, tenantID, requestID string) (releasecenter.ReleaseRequest, error) {
	request, ok := s.requests[requestID]
	if !ok || request.TenantID != tenantID {
		return releasecenter.ReleaseRequest{}, releasecenter.ErrRequestNotFound
	}
	return request, nil
}

func (s *functionalReleaseStore) ListRequests(context.Context, string, int) ([]releasecenter.ReleaseRequest, error) {
	return nil, nil
}

func (s *functionalReleaseStore) ListReviewJobs(context.Context, int) ([]releasecenter.ReviewJob, error) {
	return nil, nil
}

func (s *functionalReleaseStore) RecordDecision(_ context.Context, decision releasecenter.Decision) error {
	for _, existing := range s.decisions[decision.RequestID] {
		if existing.DecidedBy != decision.DecidedBy {
			continue
		}
		if existing.Decision == decision.Decision && existing.Reason == decision.Reason {
			return nil
		}
		return releasecenter.ErrDecisionConflict
	}
	s.decisions[decision.RequestID] = append(s.decisions[decision.RequestID], decision)
	return nil
}

func (s *functionalReleaseStore) ListDecisions(_ context.Context, tenantID, requestID string) ([]releasecenter.Decision, error) {
	request, ok := s.requests[requestID]
	if !ok || request.TenantID != tenantID {
		return nil, releasecenter.ErrRequestNotFound
	}
	return append([]releasecenter.Decision(nil), s.decisions[requestID]...), nil
}

func (s *functionalReleaseStore) SetRequestState(_ context.Context, tenantID, requestID string, state releasecenter.RequestState) error {
	request, err := s.GetRequest(context.Background(), tenantID, requestID)
	if err != nil {
		return err
	}
	request.State = state
	request.UpdatedAt = time.Now().UTC()
	s.requests[requestID] = request
	return nil
}

func (s *functionalReleaseStore) ReconcileStaleRequests(context.Context, int) ([]releasecenter.ReleaseRequest, error) {
	return nil, nil
}

func (s *functionalReleaseStore) ExpireDueReviews(context.Context, time.Time, int) ([]releasecenter.ReleaseRequest, error) {
	return nil, nil
}

func (s *functionalReleaseStore) PurgeExpiredReviews(context.Context, time.Time, time.Duration, int) (int, error) {
	return 0, nil
}

func (s *functionalReleaseStore) ResetDecisions(_ context.Context, tenantID, requestID string) error {
	request, ok := s.requests[requestID]
	if !ok || request.TenantID != tenantID {
		return nil
	}
	delete(s.decisions, requestID)
	return nil
}

func functionalCandidate() publicationworkflow.Candidate {
	return publicationworkflow.Candidate{
		DocumentID:          "doc-functional",
		DocumentVersionID:   "version-1",
		GenerationID:        "generation-1",
		ExpectedChunkCount:  1,
		ExpectedChunkDigest: "sha256:functional",
		ReleaseRevision:     1,
	}
}

func functionalAdminContext(userID string) context.Context {
	ctx := ctxWithRole("tenant-functional", "admin", "admin")
	return context.WithValue(ctx, auth.CtxUserID, userID)
}

func TestReleaseCenterHTTPFailsClosedOnAgentEvidenceFailures(t *testing.T) {
	tests := []struct {
		name   string
		review releasecenter.AgentReview
		err    error
	}{
		{name: "agent error", err: errors.New("agent unavailable")},
		{name: "explicit failed", review: releasecenter.AgentReview{Status: "failed", Recommendation: "publish", RiskLevel: releasecenter.RiskLow}},
		{name: "explicit error status", review: releasecenter.AgentReview{Status: "error", Recommendation: "publish", RiskLevel: releasecenter.RiskLow}},
		{name: "missing status", review: releasecenter.AgentReview{Recommendation: "publish", RiskLevel: releasecenter.RiskLow}},
		{name: "invalid evidence", review: releasecenter.AgentReview{Status: "completed", Recommendation: "", RiskLevel: "unknown"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := functionalCandidate()
			workflow := &functionalReleaseWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
			store := newFunctionalReleaseStore()
			coordinator := releasecenter.NewCoordinator(
				workflow,
				functionalReleaseDocuments{document: docstore.Document{TenantID: "tenant-functional", DocID: candidate.DocumentID, Permission: "internal"}},
				functionalReleaseReviewer{review: test.review, err: test.err},
				store,
			)
			handler := handleReleaseCenterReview(coordinator)
			response := doRequest(handler, http.MethodPost, "/v1/release-center/reviews/"+candidate.DocumentID, nil, functionalAdminContext("requester"))
			if response.Code != http.StatusCreated {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			var body struct {
				Review  releasecenter.ReviewReport   `json:"review"`
				Request releasecenter.ReleaseRequest `json:"request"`
			}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Review.Status != "failed" || body.Review.RiskLevel != releasecenter.RiskHigh || body.Review.Recommendation != "manual_review" || body.Request.State != releasecenter.RequestManualException || body.Request.RequiredApprovals != 2 {
				t.Fatalf("review=%+v request=%+v", body.Review, body.Request)
			}
		})
	}
}

func TestReleaseCenterHTTPInternalHighRiskRequiresTwoAdmins(t *testing.T) {
	candidate := functionalCandidate()
	workflow := &functionalReleaseWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newFunctionalReleaseStore()
	coordinator := releasecenter.NewCoordinator(
		workflow,
		functionalReleaseDocuments{document: docstore.Document{TenantID: "tenant-functional", DocID: candidate.DocumentID, Permission: "internal"}},
		functionalReleaseReviewer{review: releasecenter.AgentReview{Status: "completed", Recommendation: "publish", RiskLevel: releasecenter.RiskHigh}},
		store,
	)
	reviewResponse := doRequest(handleReleaseCenterReview(coordinator), http.MethodPost, "/v1/release-center/reviews/"+candidate.DocumentID, nil, functionalAdminContext("requester"))
	if reviewResponse.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", reviewResponse.Code, reviewResponse.Body.String())
	}
	var created struct {
		Request releasecenter.ReleaseRequest `json:"request"`
	}
	if err := json.NewDecoder(reviewResponse.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Request.RequiredApprovals != 2 || created.Request.State != releasecenter.RequestApprovalPending {
		t.Fatalf("request=%+v", created.Request)
	}

	decisionHandler := handleReleaseCenterDecision(store, workflow, nil)
	first := doRequest(decisionHandler, http.MethodPost, "/v1/release-center/requests/"+created.Request.ID+"/decision", map[string]string{"decision": "approved"}, functionalAdminContext("admin-1"))
	if first.Code != http.StatusOK || workflow.published != 0 {
		t.Fatalf("first status=%d published=%d body=%s", first.Code, workflow.published, first.Body.String())
	}
	var firstResult struct {
		Request releasecenter.ReleaseRequest `json:"request"`
	}
	if err := json.NewDecoder(first.Body).Decode(&firstResult); err != nil || firstResult.Request.State != releasecenter.RequestApprovalPending {
		t.Fatalf("first result=%+v err=%v", firstResult, err)
	}
	second := doRequest(decisionHandler, http.MethodPost, "/v1/release-center/requests/"+created.Request.ID+"/decision", map[string]string{"decision": "approved"}, functionalAdminContext("admin-2"))
	if second.Code != http.StatusOK || workflow.published != 1 {
		t.Fatalf("second status=%d published=%d body=%s", second.Code, workflow.published, second.Body.String())
	}
	var secondResult struct {
		Request releasecenter.ReleaseRequest `json:"request"`
	}
	if err := json.NewDecoder(second.Body).Decode(&secondResult); err != nil || secondResult.Request.State != releasecenter.RequestPublished {
		t.Fatalf("second result=%+v err=%v", secondResult, err)
	}
}

func TestReleaseCenterHTTPSelfReviewIsRejected(t *testing.T) {
	candidate := functionalCandidate()
	workflow := &functionalReleaseWorkflow{assessment: publicationworkflow.Assessment{DocumentID: candidate.DocumentID, Ready: true, Candidate: &candidate}}
	store := newFunctionalReleaseStore()
	review := releasecenter.ReviewReport{ID: "review-self", TenantID: "tenant-functional", DocumentID: candidate.DocumentID, DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID, ReleaseRevision: candidate.ReleaseRevision, Status: "completed", Recommendation: "publish", RiskLevel: releasecenter.RiskLow}
	request := releasecenter.ReleaseRequest{ID: "request-self", TenantID: "tenant-functional", DocumentID: candidate.DocumentID, Candidate: candidate, ReviewID: review.ID, RequiredApprovals: 1, State: releasecenter.RequestApprovalPending, RequestedBy: "admin-self"}
	store.reviews[review.ID] = review
	store.requests[request.ID] = request

	response := doRequest(handleReleaseCenterDecision(store, workflow, nil), http.MethodPost, "/v1/release-center/requests/"+request.ID+"/decision", map[string]string{"decision": "approved"}, functionalAdminContext("admin-self"))
	if response.Code != http.StatusConflict || workflow.published != 0 || len(store.decisions[request.ID]) != 0 {
		t.Fatalf("status=%d published=%d decisions=%d body=%s", response.Code, workflow.published, len(store.decisions[request.ID]), response.Body.String())
	}
}

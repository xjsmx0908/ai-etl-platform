package releasecenter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/publicationworkflow"

	"github.com/google/uuid"
)

var (
	ErrReviewNotFound   = errors.New("release center: review not found")
	ErrRequestNotFound  = errors.New("release center: request not found")
	ErrDecisionConflict = errors.New("release center: decision conflict")
)

// AgentReview is the untrusted result returned by the pre-review adapter.
type AgentReview struct {
	RunID          string
	Status         string
	Recommendation string
	RiskLevel      RiskLevel
	Summary        string
	Findings       []Finding
	Model          string
	PromptVersion  string
}

type ReviewAdapter interface {
	Review(context.Context, publicationworkflow.Actor, string) (AgentReview, error)
}

type reviewDocumentReader interface {
	Get(context.Context, string, string) (docstore.Document, bool, error)
}

type coordinatorStore interface {
	Store
	ListReviewJobs(context.Context, int) ([]ReviewJob, error)
	ListDecisions(context.Context, string, string) ([]Decision, error)
	SetRequestState(context.Context, string, string, RequestState) error
}

type ReviewJob struct {
	TenantID    string
	DocumentID  string
	RequestedBy string
	Candidate   publicationworkflow.Candidate
}

type Coordinator struct {
	workflow  PublicationWorkflow
	documents reviewDocumentReader
	reviewer  ReviewAdapter
	store     coordinatorStore
	now       func() time.Time
}

type PublicationWorkflow interface {
	Assess(context.Context, publicationworkflow.Actor, string) (publicationworkflow.Assessment, error)
	PublishApproved(context.Context, publicationworkflow.Actor, publicationworkflow.Candidate, string) (publicationworkflow.PublicationResult, error)
}

func NewCoordinator(workflow PublicationWorkflow, documents reviewDocumentReader, reviewer ReviewAdapter, store coordinatorStore) *Coordinator {
	return &Coordinator{workflow: workflow, documents: documents, reviewer: reviewer, store: store, now: time.Now}
}

func (c *Coordinator) StartManagedReview(ctx context.Context, actor publicationworkflow.Actor, documentID string) (ReviewReport, ReleaseRequest, error) {
	if c == nil || c.workflow == nil || c.documents == nil || c.reviewer == nil || c.store == nil {
		return ReviewReport{}, ReleaseRequest{}, fmt.Errorf("release center coordinator is not configured")
	}
	assessment, err := c.workflow.Assess(ctx, actor, documentID)
	if err != nil {
		return ReviewReport{}, ReleaseRequest{}, err
	}
	if !assessment.Ready || assessment.Candidate == nil {
		return ReviewReport{}, ReleaseRequest{}, publicationworkflow.ErrNotReady
	}
	candidate := *assessment.Candidate
	doc, found, err := c.documents.Get(ctx, actor.TenantID, documentID)
	if err != nil || !found {
		if err != nil {
			return ReviewReport{}, ReleaseRequest{}, err
		}
		return ReviewReport{}, ReleaseRequest{}, docstore.ErrNotFound
	}
	now := c.now().UTC()
	reportID := stableID("review", actor.TenantID, candidate)
	reviewResult, reviewErr := c.reviewer.Review(ctx, actor, documentID)
	status := strings.TrimSpace(reviewResult.Status)
	if status == "" {
		status = "completed"
	}
	if reviewErr == nil && (strings.EqualFold(status, "failed") || strings.EqualFold(status, "error")) {
		reviewErr = fmt.Errorf("agent review returned status %q", status)
	}
	if reviewErr == nil {
		recommendation := strings.ToLower(strings.TrimSpace(reviewResult.Recommendation))
		validStatus := strings.EqualFold(status, "completed") || strings.EqualFold(status, "success")
		validRisk := reviewResult.RiskLevel == RiskLow || reviewResult.RiskLevel == RiskMedium || reviewResult.RiskLevel == RiskHigh || reviewResult.RiskLevel == RiskCritical
		if !validStatus || !validRisk || recommendation == "" || (recommendation != "publish" && recommendation != "needs_info" && recommendation != "reject" && recommendation != "manual_review") {
			reviewErr = fmt.Errorf("agent review returned incomplete evidence")
		}
	}
	if reviewErr != nil {
		status = "failed"
		reviewResult.Recommendation = "manual_review"
		reviewResult.Summary = reviewErr.Error()
		// An unavailable or malformed review is uncertainty, not a low-risk
		// result. Keep the failure path visibly high risk so policy and UI do
		// not imply that the document passed pre-review.
		reviewResult.RiskLevel = RiskHigh
	}
	if reviewResult.Recommendation == "" {
		reviewResult.Recommendation = "review"
	}
	switch reviewResult.RiskLevel {
	case RiskLow, RiskMedium, RiskHigh, RiskCritical:
	default:
		reviewResult.RiskLevel = RiskLow
	}
	report := ReviewReport{ID: reportID, TenantID: actor.TenantID, DocumentID: documentID,
		DocumentVersionID: candidate.DocumentVersionID, GenerationID: candidate.GenerationID,
		ReleaseRevision: candidate.ReleaseRevision, RunID: reviewResult.RunID, Status: status,
		Recommendation: reviewResult.Recommendation, RiskLevel: reviewResult.RiskLevel,
		Summary: reviewResult.Summary, Findings: reviewResult.Findings, Model: reviewResult.Model,
		PromptVersion: reviewResult.PromptVersion, CreatedAt: now}
	if err := c.store.SaveReview(ctx, report); err != nil {
		return ReviewReport{}, ReleaseRequest{}, err
	}
	if reviewErr == nil && report.Recommendation != "publish" {
		state := RequestNeedsInfo
		if report.Recommendation == "manual_review" {
			state = RequestManualException
		}
		request := ReleaseRequest{ID: stableID("request", actor.TenantID, candidate), TenantID: actor.TenantID, DocumentID: documentID, Candidate: candidate, ReviewID: report.ID, RequiredApprovals: 1, State: state, RequestedBy: "release-center-agent", CreatedAt: now, UpdatedAt: now}
		if err := c.store.SaveRequest(ctx, request); err != nil {
			return ReviewReport{}, ReleaseRequest{}, err
		}
		return report, request, nil
	}
	policy := EvaluatePolicy(PolicyInput{Permission: doc.Permission, Risk: report.RiskLevel, AgentAvailable: reviewErr == nil})
	request := ReleaseRequest{ID: stableID("request", actor.TenantID, candidate), TenantID: actor.TenantID,
		DocumentID: documentID, Candidate: candidate, ReviewID: report.ID, RequiredApprovals: policy.RequiredApprovals,
		State: policy.State, RequestedBy: "release-center-agent", CreatedAt: now, UpdatedAt: now}
	if err := c.store.SaveRequest(ctx, request); err != nil {
		return ReviewReport{}, ReleaseRequest{}, err
	}
	return report, request, nil
}

func stableID(prefix, tenant string, candidate publicationworkflow.Candidate) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00%d", tenant, candidate.DocumentID, candidate.DocumentVersionID, candidate.GenerationID, candidate.ExpectedChunkCount, candidate.ExpectedChunkDigest, candidate.ReleaseRevision)
	return prefix + "-" + hex.EncodeToString(h.Sum(nil))[:32]
}

// RunPendingReviews retries durable jobs after crashes or temporary Agent outages.
func (c *Coordinator) RunPendingReviews(ctx context.Context, actor publicationworkflow.Actor, limit int) error {
	if _, err := c.store.ReconcileStaleRequests(ctx, limit); err != nil {
		return err
	}
	jobs, err := c.store.ListReviewJobs(ctx, limit)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		requestedBy := strings.TrimSpace(job.RequestedBy)
		if requestedBy == "" {
			requestedBy = actor.UserID
		}
		if _, _, err := c.StartManagedReview(ctx, publicationworkflow.Actor{TenantID: job.TenantID, UserID: requestedBy, Role: actor.Role}, job.DocumentID); err != nil {
			return err
		}
	}
	return nil
}

type ApprovalResult struct {
	Request   ReleaseRequest `json:"request"`
	Decisions []Decision     `json:"decisions"`
}

type ApprovalService struct {
	workflow PublicationWorkflow
	store    coordinatorStore
	now      func() time.Time
}

func NewApprovalService(workflow PublicationWorkflow, store coordinatorStore) *ApprovalService {
	return &ApprovalService{workflow: workflow, store: store, now: time.Now}
}

func (s *ApprovalService) Decide(ctx context.Context, actor publicationworkflow.Actor, requestID, decision, reason string) (ApprovalResult, error) {
	if s == nil || s.workflow == nil || s.store == nil {
		return ApprovalResult{}, fmt.Errorf("release center approval is not configured")
	}
	if !strings.EqualFold(strings.TrimSpace(actor.Role), "admin") {
		return ApprovalResult{}, publicationworkflow.ErrAdminRequired
	}
	request, err := s.store.GetRequest(ctx, actor.TenantID, requestID)
	if err != nil {
		return ApprovalResult{}, err
	}
	if request.RequestedBy == actor.UserID {
		return ApprovalResult{}, fmt.Errorf("approval requires a different administrator")
	}
	if request.State != RequestApprovalPending && request.State != RequestManualException {
		return ApprovalResult{}, fmt.Errorf("release request is not awaiting approval")
	}
	report, err := s.store.GetReview(ctx, actor.TenantID, request.ReviewID)
	if err != nil {
		return ApprovalResult{}, err
	}
	if err := ValidateReviewBinding(report, request.Candidate, s.now().UTC()); err != nil {
		return ApprovalResult{}, err
	}
	decision = strings.ToLower(strings.TrimSpace(decision))
	if decision != "approved" && decision != "rejected" {
		return ApprovalResult{}, fmt.Errorf("invalid release decision")
	}
	if request.State == RequestManualException && strings.TrimSpace(reason) == "" {
		return ApprovalResult{}, fmt.Errorf("manual exception approval requires a reason")
	}
	decisions, err := s.store.ListDecisions(ctx, actor.TenantID, requestID)
	if err != nil {
		return ApprovalResult{}, err
	}
	foundExisting := false
	for _, existing := range decisions {
		if existing.DecidedBy != actor.UserID {
			continue
		}
		if existing.Decision != decision || existing.Reason != strings.TrimSpace(reason) {
			return ApprovalResult{}, ErrDecisionConflict
		}
		foundExisting = true
	}
	if !foundExisting {
		entry := Decision{ID: uuid.NewString(), TenantID: actor.TenantID, RequestID: requestID, DecidedBy: actor.UserID, Decision: decision, Reason: strings.TrimSpace(reason), DecidedAt: s.now().UTC()}
		if err := s.store.RecordDecision(ctx, entry); err != nil {
			return ApprovalResult{}, err
		}
		decisions = append(decisions, entry)
	}
	if decision == "rejected" {
		request.State = RequestRejected
		_ = s.store.SetRequestState(ctx, actor.TenantID, requestID, request.State)
		return ApprovalResult{request, decisions}, nil
	}
	approved := 0
	for _, d := range decisions {
		if d.Decision == "approved" {
			approved++
		}
	}
	if approved < request.RequiredApprovals {
		return ApprovalResult{request, decisions}, nil
	}
	assessment, err := s.workflow.Assess(ctx, actor, request.DocumentID)
	if err != nil {
		return ApprovalResult{}, err
	}
	if !assessment.Ready || assessment.Candidate == nil || *assessment.Candidate != request.Candidate {
		return ApprovalResult{}, ErrStaleReview
	}
	if _, err := s.workflow.PublishApproved(ctx, actor, request.Candidate, request.ID); err != nil {
		return ApprovalResult{}, err
	}
	request.State = RequestPublished
	if err := s.store.SetRequestState(ctx, actor.TenantID, requestID, request.State); err != nil {
		return ApprovalResult{}, err
	}
	return ApprovalResult{request, decisions}, nil
}

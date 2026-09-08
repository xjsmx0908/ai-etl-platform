package releasecenter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"ai-etl-pipeline/internal/docstore"
	"ai-etl-pipeline/internal/notification"
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
	RunID          string                         `json:"run_id,omitempty"`
	Status         string                         `json:"status"`
	Recommendation string                         `json:"recommendation"`
	RiskLevel      RiskLevel                      `json:"risk_level"`
	Summary        string                         `json:"summary"`
	Findings       []Finding                      `json:"findings,omitempty"`
	Model          string                         `json:"model,omitempty"`
	PromptVersion  string                         `json:"prompt_version,omitempty"`
	Candidate      *publicationworkflow.Candidate `json:"candidate,omitempty"`
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
	workflow        PublicationWorkflow
	documents       reviewDocumentReader
	reviewer        ReviewAdapter
	store           coordinatorStore
	policies        ApprovalPolicyStore
	notify          notification.Enqueuer
	now             func() time.Time
	reviewTTL       time.Duration
	reviewRetention time.Duration
}

type PublicationWorkflow interface {
	Assess(context.Context, publicationworkflow.Actor, string) (publicationworkflow.Assessment, error)
	PublishApproved(context.Context, publicationworkflow.Actor, publicationworkflow.Candidate, string) (publicationworkflow.PublicationResult, error)
}

func NewCoordinator(workflow PublicationWorkflow, documents reviewDocumentReader, reviewer ReviewAdapter, store coordinatorStore, policies ...ApprovalPolicyStore) *Coordinator {
	var policyStore ApprovalPolicyStore
	if len(policies) > 0 {
		policyStore = policies[0]
	}
	return &Coordinator{workflow: workflow, documents: documents, reviewer: reviewer, store: store, policies: policyStore, now: time.Now}
}

func (c *Coordinator) WithNotifier(n notification.Enqueuer) *Coordinator {
	if c != nil {
		c.notify = n
	}
	return c
}

func (c *Coordinator) WithReviewTTL(d time.Duration) *Coordinator {
	if c != nil {
		c.reviewTTL = d
	}
	return c
}

func (c *Coordinator) WithReviewRetention(d time.Duration) *Coordinator {
	if c != nil {
		c.reviewRetention = d
	}
	return c
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
	requestedBy := strings.TrimSpace(actor.UserID)
	if requestedBy == "" {
		requestedBy = "release-center-agent"
	}
	requestID := stableID("request", actor.TenantID, candidate)
	existing, existingErr := c.store.GetRequest(ctx, actor.TenantID, requestID)
	if existingErr != nil && !errors.Is(existingErr, ErrRequestNotFound) {
		return ReviewReport{}, ReleaseRequest{}, existingErr
	}
	if existingErr == nil {
		switch existing.State {
		case RequestPublished, RequestRejected:
			report, err := c.store.GetReview(ctx, actor.TenantID, existing.ReviewID)
			if err != nil {
				return ReviewReport{}, ReleaseRequest{}, err
			}
			return report, existing, nil
		}
		if strings.TrimSpace(existing.RequestedBy) != "" {
			requestedBy = existing.RequestedBy
		}
	}
	reportID := stableID("review", actor.TenantID, candidate)
	rereview := false
	if existingErr == nil {
		previous, err := c.store.GetReview(ctx, actor.TenantID, existing.ReviewID)
		if err != nil && !errors.Is(err, ErrReviewNotFound) {
			return ReviewReport{}, ReleaseRequest{}, err
		}
		if err == nil && ReviewIsExpired(previous, now) {
			reportID = nextReviewID(previous.ID)
			rereview = true
		}
	}
	reviewResult, reviewErr := c.reviewer.Review(ctx, actor, documentID)
	if reviewErr == nil && reviewResult.Candidate != nil && *reviewResult.Candidate != candidate {
		reviewErr = fmt.Errorf("agent review returned a different exact candidate")
	}
	status := strings.ToLower(strings.TrimSpace(reviewResult.Status))
	if reviewErr == nil && (strings.EqualFold(status, "failed") || strings.EqualFold(status, "error")) {
		reviewErr = fmt.Errorf("agent review returned status %q", status)
	}
	if reviewErr == nil {
		recommendation := strings.ToLower(strings.TrimSpace(reviewResult.Recommendation))
		validStatus := status == "completed" || status == "success"
		validRisk := reviewResult.RiskLevel == RiskLow || reviewResult.RiskLevel == RiskMedium || reviewResult.RiskLevel == RiskHigh || reviewResult.RiskLevel == RiskCritical
		if !validStatus || !validRisk || recommendation == "" || (recommendation != "publish" && recommendation != "needs_info" && recommendation != "reject" && recommendation != "manual_review") {
			reviewErr = fmt.Errorf("agent review returned incomplete evidence")
		}
		if reviewErr == nil && status == "success" {
			status = "completed"
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
	if c.reviewTTL > 0 {
		report.ExpiresAt = now.Add(c.reviewTTL)
	}
	if err := c.store.SaveReview(ctx, report); err != nil {
		return ReviewReport{}, ReleaseRequest{}, err
	}
	if rereview {
		if err := c.store.ResetDecisions(ctx, actor.TenantID, requestID); err != nil {
			return ReviewReport{}, ReleaseRequest{}, err
		}
	}
	eventType := notification.EventRequestOpened
	if rereview {
		eventType = notification.EventRequestStateChanged
	}
	createdAt := now
	if existingErr == nil && !existing.CreatedAt.IsZero() {
		createdAt = existing.CreatedAt
	}
	if reviewErr == nil && report.Recommendation != "publish" {
		state := RequestNeedsInfo
		if report.Recommendation == "manual_review" {
			state = RequestManualException
		}
		request := ReleaseRequest{ID: requestID, TenantID: actor.TenantID, DocumentID: documentID, Candidate: candidate, ReviewID: report.ID, RequiredApprovals: 1, State: state, RequestedBy: requestedBy, CreatedAt: createdAt, UpdatedAt: now}
		if err := c.persistRequest(ctx, request, eventType, map[string]string{
			"recommendation": report.Recommendation,
			"risk_level":     string(report.RiskLevel),
		}); err != nil {
			return ReviewReport{}, ReleaseRequest{}, err
		}
		return report, request, nil
	}
	policyDecision := EvaluatePolicy(PolicyInput{Permission: doc.Permission, Risk: report.RiskLevel, AgentAvailable: reviewErr == nil})
	var configuredPolicy ApprovalPolicy
	if c.policies != nil && reviewErr == nil {
		resolved, found, err := c.policies.ResolveApprovalPolicy(ctx, actor.TenantID, doc.KnowledgeSpaceID, doc.Permission, report.RiskLevel)
		if err != nil {
			return ReviewReport{}, ReleaseRequest{}, fmt.Errorf("resolve approval policy: %w", err)
		}
		if found {
			configuredPolicy = resolved
			if resolved.RequiredApprovals > policyDecision.RequiredApprovals {
				policyDecision.RequiredApprovals = resolved.RequiredApprovals
			}
			policyDecision.State = RequestApprovalPending
		}
	}
	request := ReleaseRequest{ID: requestID, TenantID: actor.TenantID,
		DocumentID: documentID, Candidate: candidate, ReviewID: report.ID, RequiredApprovals: policyDecision.RequiredApprovals,
		State: policyDecision.State, RequestedBy: requestedBy, CreatedAt: createdAt, UpdatedAt: now,
		PolicyID: configuredPolicy.ID, ApproverGroupID: configuredPolicy.ApproverGroupID,
		AllowRequesterApproval: configuredPolicy.AllowRequesterApproval}
	if err := c.persistRequest(ctx, request, eventType, map[string]string{
		"recommendation": report.Recommendation,
		"risk_level":     string(report.RiskLevel),
	}); err != nil {
		return ReviewReport{}, ReleaseRequest{}, err
	}
	return report, request, nil
}

func (c *Coordinator) persistRequest(ctx context.Context, request ReleaseRequest, eventType string, extra map[string]string) error {
	if err := c.store.SaveRequest(ctx, request); err != nil {
		return err
	}
	if eventType == "" {
		eventType = notification.EventRequestOpened
	}
	notifyRelease(ctx, c.notify, request, eventType, extra)
	return nil
}

func stableID(prefix, tenant string, candidate publicationworkflow.Candidate) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00%d", tenant, candidate.DocumentID, candidate.DocumentVersionID, candidate.GenerationID, candidate.ExpectedChunkCount, candidate.ExpectedChunkDigest, candidate.ReleaseRevision)
	return prefix + "-" + hex.EncodeToString(h.Sum(nil))[:32]
}

func nextReviewID(previousID string) string {
	sum := sha256.Sum256([]byte(previousID + "\x00rereview"))
	return "review-" + hex.EncodeToString(sum[:])[:16]
}

// RunPendingReviews retries durable jobs after crashes or temporary Agent outages.
func (c *Coordinator) RunPendingReviews(ctx context.Context, actor publicationworkflow.Actor, limit int) error {
	stale, err := c.store.ReconcileStaleRequests(ctx, limit)
	if err != nil {
		return err
	}
	for _, request := range stale {
		notifyRelease(ctx, c.notify, request, notification.EventRequestStateChanged, nil)
	}
	now := c.now().UTC()
	expired, err := c.store.ExpireDueReviews(ctx, now, limit)
	if err != nil {
		return err
	}
	for _, request := range expired {
		notifyRelease(ctx, c.notify, request, notification.EventRequestStateChanged, map[string]string{
			"reason": "review_expired",
		})
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
	if c.reviewRetention > 0 {
		if _, err := c.store.PurgeExpiredReviews(ctx, now, c.reviewRetention, limit); err != nil {
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
	policies ApprovalPolicyStore
	notify   notification.Enqueuer
	now      func() time.Time
}

func NewApprovalService(workflow PublicationWorkflow, store coordinatorStore, policies ...ApprovalPolicyStore) *ApprovalService {
	var policyStore ApprovalPolicyStore
	if len(policies) > 0 {
		policyStore = policies[0]
	}
	return &ApprovalService{workflow: workflow, store: store, policies: policyStore, now: time.Now}
}

func (s *ApprovalService) WithNotifier(n notification.Enqueuer) *ApprovalService {
	if s != nil {
		s.notify = n
	}
	return s
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
	if request.ApproverGroupID != "" {
		if s.policies == nil {
			return ApprovalResult{}, fmt.Errorf("release center approval policy is not configured")
		}
		if err := AuthorizeApproval(ctx, s.policies, actor, request); err != nil {
			return ApprovalResult{}, err
		}
	} else if request.RequestedBy == actor.UserID && !request.AllowRequesterApproval {
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
	entryID := ""
	if !foundExisting {
		entry := Decision{ID: uuid.NewString(), TenantID: actor.TenantID, RequestID: requestID, DecidedBy: actor.UserID, Decision: decision, Reason: strings.TrimSpace(reason), DecidedAt: s.now().UTC()}
		if err := s.store.RecordDecision(ctx, entry); err != nil {
			return ApprovalResult{}, err
		}
		decisions = append(decisions, entry)
		entryID = entry.ID
	} else {
		for _, existing := range decisions {
			if existing.DecidedBy == actor.UserID {
				entryID = existing.ID
				break
			}
		}
	}
	notifyRelease(ctx, s.notify, request, notification.EventDecisionRecorded, map[string]string{
		"decision":           decision,
		"decision_id":        entryID,
		"decided_by":         actor.UserID,
		"approved_decisions": strconv.Itoa(countApproved(decisions)),
	})
	if decision == "rejected" {
		request.State = RequestRejected
		_ = s.store.SetRequestState(ctx, actor.TenantID, requestID, request.State)
		notifyRelease(ctx, s.notify, request, notification.EventRequestStateChanged, map[string]string{
			"decision": decision, "decided_by": actor.UserID,
		})
		return ApprovalResult{request, decisions}, nil
	}
	approved := countApproved(decisions)
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
	notifyRelease(ctx, s.notify, request, notification.EventRequestStateChanged, map[string]string{
		"decision":           "approved",
		"decided_by":         actor.UserID,
		"approved_decisions": strconv.Itoa(approved),
	})
	return ApprovalResult{request, decisions}, nil
}

func countApproved(decisions []Decision) int {
	approved := 0
	for _, item := range decisions {
		if item.Decision == "approved" {
			approved++
		}
	}
	return approved
}

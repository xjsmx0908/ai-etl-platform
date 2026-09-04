// Package releasecenter contains the business-facing managed-document release
// policy. Agent output is deliberately represented as an untrusted input; the
// package owns no model calls and cannot publish a document by itself.
package releasecenter

import (
	"errors"
	"strings"
	"time"

	"ai-etl-pipeline/internal/publicationworkflow"
)

type RiskLevel string

const (
	RiskLow      RiskLevel = "low"
	RiskMedium   RiskLevel = "medium"
	RiskHigh     RiskLevel = "high"
	RiskCritical RiskLevel = "critical"
)

type RequestState string

const (
	RequestApprovalPending RequestState = "approval_pending"
	RequestManualException RequestState = "manual_exception"
	RequestNeedsInfo       RequestState = "needs_info"
	RequestRejected        RequestState = "rejected"
	RequestPublished       RequestState = "published"
)

type PolicyInput struct {
	Permission     string
	Risk           RiskLevel
	AgentAvailable bool
}

type PolicyDecision struct {
	RequiredApprovals int
	State             RequestState
	ManualException   bool
}

// EvaluatePolicy applies fixed server-side rules. Agent risk can escalate the
// count but never reduce the minimum required by document classification.
func EvaluatePolicy(in PolicyInput) PolicyDecision {
	permission := strings.ToLower(strings.TrimSpace(in.Permission))
	required := 1
	if permission == "confidential" {
		required = 2
	}
	if in.Risk == RiskHigh || in.Risk == RiskCritical {
		required = 2
	}
	if !in.AgentAvailable {
		return PolicyDecision{RequiredApprovals: required, State: RequestManualException, ManualException: true}
	}
	return PolicyDecision{RequiredApprovals: required, State: RequestApprovalPending}
}

type ReviewReport struct {
	ID                string    `json:"review_id"`
	TenantID          string    `json:"tenant_id"`
	DocumentID        string    `json:"document_id"`
	DocumentVersionID string    `json:"document_version_id"`
	GenerationID      string    `json:"generation_id"`
	ReleaseRevision   int64     `json:"release_revision"`
	RunID             string    `json:"agent_run_id,omitempty"`
	Status            string    `json:"status"`
	Recommendation    string    `json:"recommendation"`
	RiskLevel         RiskLevel `json:"risk_level"`
	Summary           string    `json:"summary"`
	Findings          []Finding `json:"findings,omitempty"`
	Model             string    `json:"model,omitempty"`
	PromptVersion     string    `json:"prompt_version,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	ExpiresAt         time.Time `json:"expires_at,omitempty"`
}

type Finding struct {
	Code        string `json:"code"`
	Severity    string `json:"severity"`
	Summary     string `json:"summary"`
	EvidenceRef string `json:"evidence_ref,omitempty"`
}

type ReleaseRequest struct {
	ID                string                        `json:"request_id"`
	TenantID          string                        `json:"tenant_id"`
	DocumentID        string                        `json:"document_id"`
	Candidate         publicationworkflow.Candidate `json:"candidate"`
	ReviewID          string                        `json:"review_id"`
	RequiredApprovals int                           `json:"required_approvals"`
	State             RequestState                  `json:"state"`
	RequestedBy       string                        `json:"requested_by"`
	CreatedAt         time.Time                     `json:"created_at"`
	UpdatedAt         time.Time                     `json:"updated_at"`
}

var (
	ErrStaleReview   = errors.New("release center: review is stale")
	ErrInvalidReview = errors.New("release center: invalid review binding")
)

func ValidateReviewBinding(report ReviewReport, candidate publicationworkflow.Candidate, now time.Time) error {
	if report.DocumentID != candidate.DocumentID || report.DocumentVersionID != candidate.DocumentVersionID ||
		report.GenerationID != candidate.GenerationID || report.ReleaseRevision != candidate.ReleaseRevision {
		return ErrStaleReview
	}
	if report.ID == "" || report.Status == "" || report.Recommendation == "" {
		return ErrInvalidReview
	}
	if !report.ExpiresAt.IsZero() && !now.Before(report.ExpiresAt) {
		return ErrStaleReview
	}
	return nil
}

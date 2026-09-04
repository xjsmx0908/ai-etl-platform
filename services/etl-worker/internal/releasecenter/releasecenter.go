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

// OverviewInput is the authoritative read-side snapshot used to project a
// business-facing release-center state. It intentionally contains no mutable
// action fields: publication and approval remain owned by their workflows.
type OverviewInput struct {
	DocumentID           string
	FileName             string
	Permission           string
	IngestionStatus      string
	DocStatus            string
	Owner                string
	EffectiveDatePresent bool
	KnowledgeSpaceID     string
	PublicationStatus    string
	DeletionStatus       string
	CandidateReady       bool
	ReviewStatus         string
	RequestState         RequestState
	RequiredApprovals    int
	ApprovedDecisions    int
	RequestID            string
}

type OverviewItem struct {
	DocumentID        string       `json:"document_id"`
	FileName          string       `json:"file_name"`
	Permission        string       `json:"permission"`
	KnowledgeSpaceID  string       `json:"knowledge_space_id"`
	State             string       `json:"state"`
	Blockers          []string     `json:"blockers,omitempty"`
	RequestID         string       `json:"request_id,omitempty"`
	RequestState      RequestState `json:"request_state,omitempty"`
	RequiredApprovals int          `json:"required_approvals,omitempty"`
	ApprovedDecisions int          `json:"approved_decisions,omitempty"`
}

// ProjectOverview applies deterministic, human-readable state rules to one
// document snapshot. The ordering is deliberate: terminal publication state
// wins, then active approval/review states, then deterministic blockers.
func ProjectOverview(in OverviewInput) OverviewItem {
	item := OverviewItem{DocumentID: in.DocumentID, FileName: in.FileName,
		Permission: in.Permission, KnowledgeSpaceID: in.KnowledgeSpaceID,
		RequestID:    in.RequestID,
		RequestState: in.RequestState, RequiredApprovals: in.RequiredApprovals,
		ApprovedDecisions: in.ApprovedDecisions}
	if in.PublicationStatus == "published" || in.RequestState == RequestPublished {
		item.State = "published"
		return item
	}
	if in.RequestState == RequestRejected {
		item.State = "rejected"
		return item
	}
	if in.IngestionStatus == "queued" || in.IngestionStatus == "processing" {
		item.State = "checking"
		item.Blockers = []string{"ingestion_not_completed"}
		return item
	}
	if in.RequestState == RequestApprovalPending || in.RequestState == RequestManualException {
		item.State = "approval_pending"
		return item
	}
	if in.RequestState == RequestNeedsInfo {
		item.State = "needs_info"
		item.Blockers = []string{"exact_candidate_unavailable"}
		return item
	}
	if in.ReviewStatus == "failed" {
		item.State = "review_blocked"
		item.Blockers = []string{"agent_review_unavailable"}
		return item
	}
	if in.KnowledgeSpaceID == "" || in.KnowledgeSpaceID == "user-uploads" {
		item.Blockers = append(item.Blockers, "managed_space_required")
	}
	if in.IngestionStatus != "completed" {
		item.Blockers = append(item.Blockers, "ingestion_not_completed")
	}
	if in.DocStatus != "" && in.DocStatus != "active" {
		item.Blockers = append(item.Blockers, "document_not_active")
	}
	if in.DeletionStatus == "pending" {
		item.Blockers = append(item.Blockers, "document_deletion_pending")
	}
	if strings.TrimSpace(in.Owner) == "" {
		item.Blockers = append(item.Blockers, "owner_required")
	}
	if !in.EffectiveDatePresent {
		item.Blockers = append(item.Blockers, "effective_date_required")
	}
	if !in.CandidateReady {
		item.Blockers = append(item.Blockers, "exact_candidate_unavailable")
	}
	if len(item.Blockers) > 0 {
		item.State = "needs_info"
	} else {
		item.State = "checking"
	}
	return item
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

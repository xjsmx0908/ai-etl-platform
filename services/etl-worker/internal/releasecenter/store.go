package releasecenter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"ai-etl-pipeline/internal/db"
)

// Store is the durable release-center seam. Implementations must preserve
// tenant scope and exact candidate identity; callers never infer this state
// from Agent Run or search-index records.
type Store interface {
	SaveReview(context.Context, ReviewReport) error
	GetReview(context.Context, string, string) (ReviewReport, error)
	SaveRequest(context.Context, ReleaseRequest) error
	GetRequest(context.Context, string, string) (ReleaseRequest, error)
	ListRequests(context.Context, string, int) ([]ReleaseRequest, error)
	ListReviewJobs(context.Context, int) ([]ReviewJob, error)
	RecordDecision(context.Context, Decision) error
	ListDecisions(context.Context, string, string) ([]Decision, error)
	SetRequestState(context.Context, string, string, RequestState) error
	ReconcileStaleRequests(context.Context, int) (int, error)
}

type Decision struct {
	ID        string    `json:"decision_id"`
	TenantID  string    `json:"tenant_id"`
	RequestID string    `json:"request_id"`
	DecidedBy string    `json:"decided_by"`
	Decision  string    `json:"decision"`
	Reason    string    `json:"reason,omitempty"`
	DecidedAt time.Time `json:"decided_at"`
}

type OverviewStore interface {
	ListOverview(context.Context, string, int) ([]OverviewItem, error)
}

// PostgresStore persists business release records. Publication itself remains
// owned by publicationworkflow and is intentionally not part of this adapter.
type PostgresStore struct{ q db.Querier }

func NewPostgresStore(q db.Querier) *PostgresStore { return &PostgresStore{q: q} }

var _ Store = (*PostgresStore)(nil)

var _ OverviewStore = (*PostgresStore)(nil)

func (s *PostgresStore) ListOverview(ctx context.Context, tenantID string, limit int) ([]OverviewItem, error) {
	if s == nil || s.q == nil {
		return nil, fmt.Errorf("release center store is not configured")
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.q.Query(ctx, `
		SELECT d.doc_id,d.file_name,d.permission,d.status,d.doc_status,d.owner,
		       (d.effective_date IS NOT NULL),d.knowledge_space_id,d.publication_status,d.deletion_status,
		       COALESCE(q.request_id,''),COALESCE(q.state,''),COALESCE(q.required_approvals,0),
		       COALESCE(rv.status,''),
		       (r.resolution_status='resolved' AND m.state='active'
		        AND m.expected_chunk_count > 0 AND m.expected_chunk_digest <> ''
		        AND m.qdrant_count=m.expected_chunk_count AND m.qdrant_digest=m.expected_chunk_digest
		        AND m.elasticsearch_count=m.expected_chunk_count AND m.elasticsearch_digest=m.expected_chunk_digest),
		       COALESCE((SELECT count(*) FROM release_center_decisions dec
		                 WHERE dec.tenant_id=d.tenant_id AND dec.request_id=q.request_id
		                   AND dec.decision='approved'),0)
		FROM documents d
		LEFT JOIN document_releases r ON r.tenant_id=d.tenant_id AND r.document_id=d.doc_id
		LEFT JOIN LATERAL (
			SELECT m1.* FROM index_manifests m1
			WHERE m1.tenant_id=d.tenant_id AND m1.document_id=d.doc_id
			  AND m1.document_version_id=r.current_version_id
			ORDER BY (m1.state='active') DESC, m1.created_at DESC LIMIT 1
		) m ON TRUE
		LEFT JOIN LATERAL (
			SELECT q1.* FROM release_center_requests q1
			WHERE q1.tenant_id=d.tenant_id AND q1.document_id=d.doc_id
			ORDER BY q1.updated_at DESC LIMIT 1
		) q ON TRUE
		LEFT JOIN release_center_reviews rv ON rv.tenant_id=q.tenant_id AND rv.review_id=q.review_id
		WHERE d.tenant_id=$1 AND d.knowledge_space_id<>'' AND d.knowledge_space_id<>'user-uploads'
		ORDER BY d.updated_at DESC LIMIT $2`, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list release overview: %w", err)
	}
	defer rows.Close()
	var out []OverviewItem
	for rows.Next() {
		var in OverviewInput
		var requestState string
		if err := rows.Scan(&in.DocumentID, &in.FileName, &in.Permission, &in.IngestionStatus,
			&in.DocStatus, &in.Owner, &in.EffectiveDatePresent, &in.KnowledgeSpaceID,
			&in.PublicationStatus, &in.DeletionStatus, &in.RequestID, &requestState,
			&in.RequiredApprovals, &in.ReviewStatus, &in.CandidateReady, &in.ApprovedDecisions); err != nil {
			return nil, fmt.Errorf("scan release overview: %w", err)
		}
		in.RequestState = RequestState(requestState)
		out = append(out, ProjectOverview(in))
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate release overview: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) SaveReview(ctx context.Context, report ReviewReport) error {
	if s == nil || s.q == nil {
		return fmt.Errorf("release center store is not configured")
	}
	if strings.TrimSpace(report.ID) == "" || strings.TrimSpace(report.DocumentID) == "" {
		return ErrInvalidReview
	}
	findings, err := json.Marshal(report.Findings)
	if err != nil {
		return fmt.Errorf("encode review findings: %w", err)
	}
	tag, err := s.q.Exec(ctx, `INSERT INTO release_center_reviews (
		review_id, tenant_id, document_id, document_version_id, generation_id,
		release_revision, agent_run_id, status, recommendation, risk_level,
		summary, findings, model, prompt_version, created_at, expires_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
	ON CONFLICT (review_id) DO UPDATE SET
		tenant_id=EXCLUDED.tenant_id, document_id=EXCLUDED.document_id,
		document_version_id=EXCLUDED.document_version_id, generation_id=EXCLUDED.generation_id,
		release_revision=EXCLUDED.release_revision, agent_run_id=EXCLUDED.agent_run_id,
		status=EXCLUDED.status, recommendation=EXCLUDED.recommendation,
		risk_level=EXCLUDED.risk_level, summary=EXCLUDED.summary,
		findings=EXCLUDED.findings, model=EXCLUDED.model,
		prompt_version=EXCLUDED.prompt_version, expires_at=EXCLUDED.expires_at
		WHERE release_center_reviews.tenant_id=EXCLUDED.tenant_id
		  AND release_center_reviews.document_id=EXCLUDED.document_id
		  AND release_center_reviews.document_version_id=EXCLUDED.document_version_id
		  AND release_center_reviews.generation_id=EXCLUDED.generation_id
		  AND release_center_reviews.release_revision=EXCLUDED.release_revision`,
		report.ID, report.TenantID, report.DocumentID, report.DocumentVersionID,
		report.GenerationID, report.ReleaseRevision, report.RunID, report.Status,
		report.Recommendation, report.RiskLevel, report.Summary, findings,
		report.Model, report.PromptVersion, report.CreatedAt, nullableTime(report.ExpiresAt))
	if err != nil {
		return fmt.Errorf("save release review: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("release review id is bound to a different exact candidate")
	}
	return nil
}

func (s *PostgresStore) GetReview(ctx context.Context, tenantID, reviewID string) (ReviewReport, error) {
	if s == nil || s.q == nil {
		return ReviewReport{}, fmt.Errorf("release center store is not configured")
	}
	var report ReviewReport
	var findings []byte
	var expires pgtype.Timestamptz
	err := s.q.QueryRow(ctx, `SELECT review_id,tenant_id,document_id,document_version_id,
		generation_id,release_revision,agent_run_id,status,recommendation,risk_level,
		summary,findings,model,prompt_version,created_at,expires_at
		FROM release_center_reviews WHERE tenant_id=$1 AND review_id=$2`, tenantID, reviewID).Scan(
		&report.ID, &report.TenantID, &report.DocumentID, &report.DocumentVersionID,
		&report.GenerationID, &report.ReleaseRevision, &report.RunID, &report.Status,
		&report.Recommendation, &report.RiskLevel, &report.Summary, &findings,
		&report.Model, &report.PromptVersion, &report.CreatedAt, &expires)
	if err != nil {
		if err == pgx.ErrNoRows {
			return ReviewReport{}, ErrReviewNotFound
		}
		return ReviewReport{}, fmt.Errorf("load release review: %w", err)
	}
	if expires.Valid {
		report.ExpiresAt = expires.Time.UTC()
	}
	if len(findings) > 0 {
		if err := json.Unmarshal(findings, &report.Findings); err != nil {
			return ReviewReport{}, fmt.Errorf("decode release review findings: %w", err)
		}
	}
	return report, nil
}

func (s *PostgresStore) SaveRequest(ctx context.Context, request ReleaseRequest) error {
	if s == nil || s.q == nil {
		return fmt.Errorf("release center store is not configured")
	}
	if strings.TrimSpace(request.ID) == "" || strings.TrimSpace(request.TenantID) == "" ||
		strings.TrimSpace(request.DocumentID) == "" || request.Candidate.DocumentID != request.DocumentID {
		return ErrInvalidReview
	}
	tag, err := s.q.Exec(ctx, `INSERT INTO release_center_requests (
		request_id,tenant_id,document_id,document_version_id,generation_id,
		expected_chunk_count,expected_chunk_digest,release_revision,review_id,
		required_approvals,state,requested_by,created_at,updated_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
	ON CONFLICT (request_id) DO UPDATE SET state=EXCLUDED.state,
		required_approvals=EXCLUDED.required_approvals,updated_at=EXCLUDED.updated_at
		WHERE release_center_requests.tenant_id=EXCLUDED.tenant_id
		  AND release_center_requests.document_id=EXCLUDED.document_id
		  AND release_center_requests.document_version_id=EXCLUDED.document_version_id
		  AND release_center_requests.generation_id=EXCLUDED.generation_id
		  AND release_center_requests.expected_chunk_count=EXCLUDED.expected_chunk_count
		  AND release_center_requests.expected_chunk_digest=EXCLUDED.expected_chunk_digest
		  AND release_center_requests.release_revision=EXCLUDED.release_revision
		  AND release_center_requests.review_id=EXCLUDED.review_id`,
		request.ID, request.TenantID, request.DocumentID, request.Candidate.DocumentVersionID,
		request.Candidate.GenerationID, request.Candidate.ExpectedChunkCount,
		request.Candidate.ExpectedChunkDigest, request.Candidate.ReleaseRevision,
		request.ReviewID, request.RequiredApprovals, request.State, request.RequestedBy,
		request.CreatedAt, request.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save release request: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("release request id is bound to a different exact candidate")
	}
	return nil
}

func (s *PostgresStore) GetRequest(ctx context.Context, tenantID, requestID string) (ReleaseRequest, error) {
	if s == nil || s.q == nil {
		return ReleaseRequest{}, fmt.Errorf("release center store is not configured")
	}
	var request ReleaseRequest
	err := s.q.QueryRow(ctx, `SELECT request_id,tenant_id,document_id,document_version_id,generation_id,
		expected_chunk_count,expected_chunk_digest,release_revision,review_id,
		required_approvals,state,requested_by,created_at,updated_at
		FROM release_center_requests WHERE tenant_id=$1 AND request_id=$2`, tenantID, requestID).Scan(
		&request.ID, &request.TenantID, &request.DocumentID,
		&request.Candidate.DocumentVersionID, &request.Candidate.GenerationID,
		&request.Candidate.ExpectedChunkCount, &request.Candidate.ExpectedChunkDigest,
		&request.Candidate.ReleaseRevision, &request.ReviewID, &request.RequiredApprovals,
		&request.State, &request.RequestedBy, &request.CreatedAt, &request.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReleaseRequest{}, ErrRequestNotFound
	}
	if err != nil {
		return ReleaseRequest{}, fmt.Errorf("load release request: %w", err)
	}
	request.Candidate.DocumentID = request.DocumentID
	return request, nil
}

func (s *PostgresStore) ListReviewJobs(ctx context.Context, limit int) ([]ReviewJob, error) {
	if s == nil || s.q == nil {
		return nil, fmt.Errorf("release center store is not configured")
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.q.Query(ctx, `SELECT d.tenant_id,d.doc_id,d.uploaded_by,r.current_version_id,m.generation_id,
		m.expected_chunk_count,m.expected_chunk_digest,r.revision
		FROM documents d JOIN document_releases r ON r.tenant_id=d.tenant_id AND r.document_id=d.doc_id
		JOIN index_manifests m ON m.tenant_id=r.tenant_id AND m.document_id=r.document_id AND m.document_version_id=r.current_version_id
		WHERE d.status='completed' AND d.doc_status='active' AND d.deletion_status='active'
		  AND d.publication_status='draft' AND d.knowledge_space_id<>'' AND d.knowledge_space_id<>'user-uploads'
		  AND d.owner<>'' AND d.effective_date IS NOT NULL AND r.resolution_status='resolved'
		  AND (r.published_version_id IS NULL OR r.published_version_id<>r.current_version_id)
		  AND m.state='active' AND m.expected_chunk_count>0 AND m.expected_chunk_digest<>'' AND m.last_reconcile_error=''
		  AND m.qdrant_count=m.expected_chunk_count AND m.qdrant_digest=m.expected_chunk_digest
		  AND m.elasticsearch_count=m.expected_chunk_count AND m.elasticsearch_digest=m.expected_chunk_digest
		  AND NOT EXISTS (SELECT 1 FROM release_center_requests q WHERE q.tenant_id=d.tenant_id AND q.document_id=d.doc_id
		    AND q.document_version_id=r.current_version_id AND q.generation_id=m.generation_id AND q.release_revision=r.revision)
		ORDER BY d.completed_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list release review jobs: %w", err)
	}
	defer rows.Close()
	var jobs []ReviewJob
	for rows.Next() {
		var job ReviewJob
		if err := rows.Scan(&job.TenantID, &job.DocumentID, &job.RequestedBy, &job.Candidate.DocumentVersionID,
			&job.Candidate.GenerationID, &job.Candidate.ExpectedChunkCount, &job.Candidate.ExpectedChunkDigest,
			&job.Candidate.ReleaseRevision); err != nil {
			return nil, fmt.Errorf("scan release review job: %w", err)
		}
		job.Candidate.DocumentID = job.DocumentID
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate release review jobs: %w", err)
	}
	return jobs, nil
}

func (s *PostgresStore) ListRequests(ctx context.Context, tenantID string, limit int) ([]ReleaseRequest, error) {
	if s == nil || s.q == nil {
		return nil, fmt.Errorf("release center store is not configured")
	}
	query := `SELECT request_id,tenant_id,document_id,document_version_id,generation_id,
		expected_chunk_count,expected_chunk_digest,release_revision,review_id,
		required_approvals,state,requested_by,created_at,updated_at
		FROM release_center_requests WHERE tenant_id=$1 ORDER BY updated_at DESC`
	args := []any{tenantID}
	if limit > 0 {
		query += " LIMIT $2"
		args = append(args, limit)
	}
	rows, err := s.q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list release requests: %w", err)
	}
	defer rows.Close()
	var out []ReleaseRequest
	for rows.Next() {
		var request ReleaseRequest
		if err := rows.Scan(&request.ID, &request.TenantID, &request.DocumentID,
			&request.Candidate.DocumentVersionID, &request.Candidate.GenerationID,
			&request.Candidate.ExpectedChunkCount, &request.Candidate.ExpectedChunkDigest,
			&request.Candidate.ReleaseRevision, &request.ReviewID, &request.RequiredApprovals,
			&request.State, &request.RequestedBy, &request.CreatedAt, &request.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan release request: %w", err)
		}
		request.Candidate.DocumentID = request.DocumentID
		out = append(out, request)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate release requests: %w", err)
	}
	return out, nil
}

func (s *PostgresStore) RecordDecision(ctx context.Context, decision Decision) error {
	if s == nil || s.q == nil {
		return fmt.Errorf("release center store is not configured")
	}
	if decision.Decision != "approved" && decision.Decision != "rejected" {
		return fmt.Errorf("invalid release decision")
	}
	tag, err := s.q.Exec(ctx, `INSERT INTO release_center_decisions
		(decision_id,tenant_id,request_id,decided_by,decision,reason,decided_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (tenant_id,request_id,decided_by) DO NOTHING`,
		decision.ID, decision.TenantID, decision.RequestID, decision.DecidedBy,
		decision.Decision, decision.Reason, decision.DecidedAt)
	if err != nil {
		return fmt.Errorf("record release decision: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrDecisionConflict
	}
	return nil
}

func (s *PostgresStore) ListDecisions(ctx context.Context, tenantID, requestID string) ([]Decision, error) {
	if s == nil || s.q == nil {
		return nil, fmt.Errorf("release center store is not configured")
	}
	rows, err := s.q.Query(ctx, `SELECT decision_id,tenant_id,request_id,decided_by,decision,reason,decided_at
		FROM release_center_decisions WHERE tenant_id=$1 AND request_id=$2 ORDER BY decided_at`, tenantID, requestID)
	if err != nil {
		return nil, fmt.Errorf("list release decisions: %w", err)
	}
	defer rows.Close()
	var out []Decision
	for rows.Next() {
		var d Decision
		if err := rows.Scan(&d.ID, &d.TenantID, &d.RequestID, &d.DecidedBy, &d.Decision, &d.Reason, &d.DecidedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *PostgresStore) SetRequestState(ctx context.Context, tenantID, requestID string, state RequestState) error {
	if s == nil || s.q == nil {
		return fmt.Errorf("release center store is not configured")
	}
	tag, err := s.q.Exec(ctx, `UPDATE release_center_requests SET state=$3,updated_at=now() WHERE tenant_id=$1 AND request_id=$2`, tenantID, requestID, state)
	if err != nil {
		return fmt.Errorf("update release request state: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrRequestNotFound
	}
	return nil
}

// ReconcileStaleRequests moves pending requests whose exact release candidate
// is no longer current into needs_info. The request and its decisions remain
// immutable audit records; a new candidate will receive a new stable request.
func (s *PostgresStore) ReconcileStaleRequests(ctx context.Context, limit int) (int, error) {
	if s == nil || s.q == nil {
		return 0, fmt.Errorf("release center store is not configured")
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	tag, err := s.q.Exec(ctx, `WITH stale AS (
		SELECT q.request_id
		FROM release_center_requests q
		LEFT JOIN document_releases r ON r.tenant_id=q.tenant_id AND r.document_id=q.document_id
		WHERE q.state IN ('approval_pending','manual_exception')
		  AND (r.document_id IS NULL OR r.resolution_status<>'resolved'
		    OR r.current_version_id<>q.document_version_id OR r.revision<>q.release_revision
		    OR NOT EXISTS (
				SELECT 1 FROM index_manifests m
				WHERE m.tenant_id=q.tenant_id AND m.document_id=q.document_id
				  AND m.document_version_id=q.document_version_id AND m.generation_id=q.generation_id
				  AND m.state='active' AND m.last_reconcile_error=''
				  AND m.expected_chunk_count=q.expected_chunk_count AND m.expected_chunk_digest=q.expected_chunk_digest
				  AND m.qdrant_count=m.expected_chunk_count AND m.qdrant_digest=m.expected_chunk_digest
				  AND m.elasticsearch_count=m.expected_chunk_count AND m.elasticsearch_digest=m.expected_chunk_digest
			))
		ORDER BY q.updated_at ASC
		LIMIT $1
	)
	UPDATE release_center_requests q SET state='needs_info', updated_at=now()
	FROM stale WHERE q.request_id=stale.request_id`, limit)
	if err != nil {
		return 0, fmt.Errorf("reconcile stale release requests: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value
}

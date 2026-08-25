package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"ai-etl-pipeline/internal/db"
)

type PostgresApprovalStore struct {
	q db.Querier
}

func NewPostgresApprovalStore(q db.Querier) *PostgresApprovalStore {
	return &PostgresApprovalStore{q: q}
}

var _ ApprovalStore = (*PostgresApprovalStore)(nil)

func (s *PostgresApprovalStore) CreateApproval(ctx context.Context, approval ApprovalRequest) error {
	if approval.Status == "" {
		approval.Status = ApprovalPending
	}
	if err := validateApprovalForCreate(approval); err != nil {
		return err
	}
	_, err := s.q.Exec(ctx, `INSERT INTO agent_approvals (
		id, run_id, tenant_id, step_index, tool_name, tool_arguments, status,
		requested_by, requested_at
	) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		approval.ID, approval.RunID, approval.TenantID, approval.StepIndex,
		approval.ToolName, approval.ToolArguments, approval.Status,
		approval.RequestedBy, approval.RequestedAt)
	if err != nil {
		return fmt.Errorf("create postgres approval: %w", err)
	}
	return nil
}

func (s *PostgresApprovalStore) LoadApproval(ctx context.Context, tenantID, approvalID string) (ApprovalRequest, error) {
	return scanApproval(s.q.QueryRow(ctx, approvalSelect+` WHERE tenant_id=$1 AND id=$2`, tenantID, approvalID))
}

func (s *PostgresApprovalStore) ListRunApprovals(ctx context.Context, tenantID, runID string) ([]ApprovalRequest, error) {
	rows, err := s.q.Query(ctx, approvalSelect+` WHERE tenant_id=$1 AND run_id=$2 ORDER BY step_index, requested_at`, tenantID, runID)
	if err != nil {
		return nil, fmt.Errorf("list postgres approvals: %w", err)
	}
	defer rows.Close()
	approvals := []ApprovalRequest{}
	for rows.Next() {
		approval, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		approvals = append(approvals, approval)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate postgres approvals: %w", err)
	}
	return approvals, nil
}

func (s *PostgresApprovalStore) DecideApproval(ctx context.Context, tenantID, approvalID string, status ApprovalStatus, decidedBy, reason string, decidedAt time.Time) (ApprovalRequest, error) {
	if status != ApprovalApproved && status != ApprovalRejected {
		return ApprovalRequest{}, fmt.Errorf("unsupported approval decision %q", status)
	}
	approval, err := scanApproval(s.q.QueryRow(ctx, `UPDATE agent_approvals
		SET status=$3, decided_by=$4, reason=$5, decided_at=$6
		WHERE tenant_id=$1 AND id=$2 AND status='pending'
		RETURNING id, run_id, tenant_id, step_index, tool_name, tool_arguments,
		          status, requested_by, requested_at, decided_by, decided_at, reason`,
		tenantID, approvalID, status, decidedBy, reason, decidedAt.UTC()))
	if err != nil {
		return ApprovalRequest{}, fmt.Errorf("decide postgres approval: %w", err)
	}
	return approval, nil
}

const approvalSelect = `SELECT id, run_id, tenant_id, step_index, tool_name,
	tool_arguments, status, requested_by, requested_at, decided_by, decided_at, reason
	FROM agent_approvals`

type approvalScanner interface {
	Scan(dest ...any) error
}

func scanApproval(row approvalScanner) (ApprovalRequest, error) {
	var approval ApprovalRequest
	var decidedAt pgtype.Timestamptz
	if err := row.Scan(&approval.ID, &approval.RunID, &approval.TenantID, &approval.StepIndex,
		&approval.ToolName, &approval.ToolArguments, &approval.Status, &approval.RequestedBy,
		&approval.RequestedAt, &approval.DecidedBy, &decidedAt, &approval.Reason); err != nil {
		if err == pgx.ErrNoRows {
			return ApprovalRequest{}, fmt.Errorf("approval not found")
		}
		return ApprovalRequest{}, fmt.Errorf("scan postgres approval: %w", err)
	}
	if decidedAt.Valid {
		approval.DecidedAt = decidedAt.Time.UTC()
	}
	return approval, nil
}

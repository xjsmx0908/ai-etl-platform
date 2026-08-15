// Package audit persists an immutable audit trail of security-relevant actions
// (login, ingestion, deletion, user management, agent approvals). Rows are
// append-only; there is no update path. The query API writes audit rows as
// best-effort side effects — an audit failure is logged, never allowed to fail
// the primary action.
package audit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"ai-etl-pipeline/internal/db"
)

// ErrNotFound is returned by List/Get when no rows match.
var ErrNotFound = errors.New("audit: not found")

// Result values.
const (
	ResultSuccess = "success"
	ResultFailure = "failure"
)

// Entry is one immutable audit record.
type Entry struct {
	TenantID     string
	ActorUserID  string
	ActorRole    string
	Action       string
	ResourceType string
	ResourceID   string
	Result       string
	Detail       map[string]any
	CreatedAt    time.Time
}

// ListQuery filters audit listing.
type ListQuery struct {
	TenantID string
	Action   string
	Limit    int
	Offset   int
}

// Store is the persistence surface for the audit trail.
type Store interface {
	Record(ctx context.Context, e Entry) error
	List(ctx context.Context, q ListQuery) ([]Entry, int, error)
}

// PgStore implements Store on PostgreSQL.
type PgStore struct {
	q db.Querier
}

// New returns a Store backed by the given querier.
func New(q db.Querier) *PgStore {
	return &PgStore{q: q}
}

var _ Store = (*PgStore)(nil)

// Record appends one audit entry. Best-effort from the caller's perspective:
// callers log-and-continue on error.
func (s *PgStore) Record(ctx context.Context, e Entry) error {
	if e.Result == "" {
		e.Result = ResultSuccess
	}
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	_, err := s.q.Exec(ctx, `
		INSERT INTO audit_logs (
			tenant_id, actor_user_id, actor_role, action, resource_type,
			resource_id, result, detail, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8, now())`,
		e.TenantID, e.ActorUserID, e.ActorRole, e.Action, e.ResourceType,
		e.ResourceID, e.Result, e.Detail,
	)
	if err != nil {
		return fmt.Errorf("record audit: %w", err)
	}
	return nil
}

// List returns audit entries for a tenant, newest first, optionally filtered by
// action, plus the total count.
func (s *PgStore) List(ctx context.Context, q ListQuery) ([]Entry, int, error) {
	where := "WHERE tenant_id=$1"
	args := []any{q.TenantID}
	if q.Action != "" {
		where += " AND action=$2"
		args = append(args, q.Action)
	}

	rows, err := s.q.Query(ctx, `
		SELECT tenant_id, actor_user_id, actor_role, action, resource_type,
		       resource_id, result, detail, created_at
		FROM audit_logs `+where+`
		ORDER BY created_at DESC
		LIMIT $`+fmt.Sprint(len(args)+1)+` OFFSET $`+fmt.Sprint(len(args)+2),
		append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("list audit: %w", err)
	}
	defer rows.Close()

	entries := []Entry{}
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.TenantID, &e.ActorUserID, &e.ActorRole, &e.Action,
			&e.ResourceType, &e.ResourceID, &e.Result, &e.Detail, &e.CreatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan audit: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate audit: %w", err)
	}

	var total int
	if err := s.q.QueryRow(ctx, "SELECT count(*) FROM audit_logs "+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count audit: %w", err)
	}
	return entries, total, nil
}

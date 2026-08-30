package externalidentity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ai-etl-pipeline/internal/audit"
	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	ErrForbidden = errors.New("external identity: administrator required")
	ErrConflict  = errors.New("external identity: binding conflicts with an existing identity")
)

type Binding struct {
	ID string
	ExternalIdentity
	InternalUserID string
	TenantID       string
	CreatedAt      time.Time
}

type BindRequest struct {
	InternalUserID string
	Identity       ExternalIdentity
}

// Manager is the tenant-admin management seam. Implementations must enforce
// tenant ownership and commit each mutation with its audit record atomically.
type Manager interface {
	List(context.Context, auth.Principal, string) ([]Binding, error)
	Bind(context.Context, auth.Principal, BindRequest) (Binding, error)
	Delete(context.Context, auth.Principal, string, string) error
}

type PostgresManager struct{ q db.Querier }

func NewPostgresManager(q db.Querier) *PostgresManager { return &PostgresManager{q: q} }

func (m *PostgresManager) List(ctx context.Context, actor auth.Principal, internalUserID string) ([]Binding, error) {
	if err := validateManagementRequest(m, actor, internalUserID); err != nil {
		return nil, err
	}
	found, err := tenantUserExists(ctx, m.q, actor.TenantID, internalUserID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	rows, err := m.q.Query(ctx, `SELECT id,issuer,external_subject,internal_user_id,tenant_id,created_at
		FROM external_identity_bindings WHERE tenant_id=$1 AND internal_user_id=$2
		ORDER BY created_at,id`, actor.TenantID, internalUserID)
	if err != nil {
		return nil, fmt.Errorf("%w: list bindings", ErrUnavailable)
	}
	defer rows.Close()
	bindings := []Binding{}
	for rows.Next() {
		var binding Binding
		if err := rows.Scan(&binding.ID, &binding.Issuer, &binding.Subject, &binding.InternalUserID, &binding.TenantID, &binding.CreatedAt); err != nil {
			return nil, fmt.Errorf("%w: scan binding", ErrUnavailable)
		}
		bindings = append(bindings, binding)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate bindings", ErrUnavailable)
	}
	return bindings, nil
}

func (m *PostgresManager) Bind(ctx context.Context, actor auth.Principal, request BindRequest) (Binding, error) {
	if err := validateManagementRequest(m, actor, request.InternalUserID); err != nil {
		return Binding{}, err
	}
	identity, err := Normalize(request.Identity)
	if err != nil {
		return Binding{}, err
	}
	tx, err := m.q.Begin(ctx)
	if err != nil {
		return Binding{}, fmt.Errorf("%w: begin binding", ErrUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	found, err := tenantUserExists(ctx, tx, actor.TenantID, request.InternalUserID)
	if err != nil {
		return Binding{}, err
	}
	if !found {
		return Binding{}, ErrNotFound
	}
	binding := Binding{ExternalIdentity: identity, InternalUserID: request.InternalUserID, TenantID: actor.TenantID}
	err = tx.QueryRow(ctx, `INSERT INTO external_identity_bindings
		(issuer,external_subject,internal_user_id,tenant_id,created_by)
		VALUES($1,$2,$3,$4,$5) RETURNING id,created_at`, identity.Issuer, identity.Subject,
		request.InternalUserID, actor.TenantID, actor.SubjectID).Scan(&binding.ID, &binding.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return Binding{}, ErrConflict
		}
		return Binding{}, fmt.Errorf("%w: insert binding", ErrUnavailable)
	}
	if err := audit.New(tx).Record(ctx, audit.Entry{
		TenantID: actor.TenantID, ActorUserID: actor.SubjectID, ActorRole: actor.Role,
		Action: "external_identity.binding.create", ResourceType: "external_identity_binding", ResourceID: binding.ID,
		Result: audit.ResultSuccess, Detail: map[string]any{
			"issuer": identity.Issuer, "internal_user_id": request.InternalUserID,
		},
	}); err != nil {
		return Binding{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Binding{}, fmt.Errorf("%w: commit binding", ErrUnavailable)
	}
	return binding, nil
}

func (m *PostgresManager) Delete(ctx context.Context, actor auth.Principal, internalUserID, bindingID string) error {
	if err := validateManagementRequest(m, actor, internalUserID); err != nil {
		return err
	}
	if _, err := uuid.Parse(bindingID); err != nil {
		return ErrNotFound
	}
	tx, err := m.q.Begin(ctx)
	if err != nil {
		return fmt.Errorf("%w: begin binding deletion", ErrUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var issuer string
	err = tx.QueryRow(ctx, `DELETE FROM external_identity_bindings
		WHERE id=$1 AND tenant_id=$2 AND internal_user_id=$3 RETURNING issuer`,
		bindingID, actor.TenantID, internalUserID).Scan(&issuer)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("%w: delete binding", ErrUnavailable)
	}
	if err := audit.New(tx).Record(ctx, audit.Entry{
		TenantID: actor.TenantID, ActorUserID: actor.SubjectID, ActorRole: actor.Role,
		Action: "external_identity.binding.delete", ResourceType: "external_identity_binding", ResourceID: bindingID,
		Result: audit.ResultSuccess, Detail: map[string]any{
			"issuer": issuer, "internal_user_id": internalUserID,
		},
	}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%w: commit binding deletion", ErrUnavailable)
	}
	return nil
}

func validateManagementRequest(manager *PostgresManager, actor auth.Principal, internalUserID string) error {
	if manager == nil || manager.q == nil {
		return ErrUnavailable
	}
	if actor.Role != "admin" || strings.TrimSpace(actor.TenantID) == "" || strings.TrimSpace(actor.SubjectID) == "" {
		return ErrForbidden
	}
	if _, err := uuid.Parse(internalUserID); err != nil {
		return ErrNotFound
	}
	return nil
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func tenantUserExists(ctx context.Context, q rowQuerier, tenantID, userID string) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE tenant_id=$1 AND id=$2)`, tenantID, userID).Scan(&exists); err != nil {
		return false, fmt.Errorf("%w: verify tenant user", ErrUnavailable)
	}
	return exists, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

var _ Manager = (*PostgresManager)(nil)

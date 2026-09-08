package knowledgecatalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"ai-etl-pipeline/internal/db"
)

type PostgresStore struct {
	q db.Querier
}

func NewPostgresStore(q db.Querier) *PostgresStore {
	return &PostgresStore{q: q}
}

func (s *PostgresStore) Space(ctx context.Context, tenantID, spaceID string) (Space, bool, error) {
	row := s.q.QueryRow(ctx, `SELECT id, tenant_id, name, kind, is_default, active, purpose
		FROM knowledge_spaces WHERE tenant_id=$1 AND id=$2`, tenantID, spaceID)
	var space Space
	if err := row.Scan(&space.ID, &space.TenantID, &space.Name, &space.Kind, &space.IsDefault, &space.Active, &space.Purpose); err != nil {
		return scanSpaceError(err)
	}
	space.Slug = space.ID
	return space, true, nil
}

func (s *PostgresStore) DefaultSpace(ctx context.Context, tenantID, userID string, admin bool) (Space, bool, error) {
	query := `SELECT s.id, s.tenant_id, s.name, s.kind, s.is_default, s.active, s.purpose
		FROM knowledge_spaces s`
	args := []any{tenantID}
	if !admin {
		query += ` JOIN knowledge_space_members m
			ON m.tenant_id=s.tenant_id AND m.space_id=s.id AND m.user_id=$2::uuid`
		args = append(args, userID)
	}
	query += ` WHERE s.tenant_id=$1 AND s.is_default AND s.active AND s.kind='production' LIMIT 1`
	row := s.q.QueryRow(ctx, query, args...)
	var space Space
	if err := row.Scan(&space.ID, &space.TenantID, &space.Name, &space.Kind, &space.IsDefault, &space.Active, &space.Purpose); err != nil {
		return scanSpaceError(err)
	}
	space.Slug = space.ID
	return space, true, nil
}

func (s *PostgresStore) Membership(ctx context.Context, tenantID, spaceID, userID string) (Membership, bool, error) {
	row := s.q.QueryRow(ctx, `SELECT tenant_id, space_id, user_id::text, role
		FROM knowledge_space_members WHERE tenant_id=$1 AND space_id=$2 AND user_id=$3::uuid`, tenantID, spaceID, userID)
	var membership Membership
	if err := row.Scan(&membership.TenantID, &membership.SpaceID, &membership.UserID, &membership.Role); err != nil {
		if isNoRows(err) {
			return Membership{}, false, nil
		}
		return Membership{}, false, err
	}
	return membership, true, nil
}

func (s *PostgresStore) ListSpaces(ctx context.Context, tenantID, userID string, admin bool) ([]Space, error) {
	query := `SELECT DISTINCT s.id, s.tenant_id, s.name, s.kind, s.is_default, s.active, s.purpose
		FROM knowledge_spaces s`
	args := []any{tenantID}
	if !admin {
		query += ` JOIN knowledge_space_members m
			ON m.tenant_id=s.tenant_id AND m.space_id=s.id AND m.user_id=$2::uuid`
		args = append(args, userID)
	}
	query += ` WHERE s.tenant_id=$1 AND s.active ORDER BY s.is_default DESC, s.name`
	rows, err := s.q.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list spaces: %w", err)
	}
	defer rows.Close()
	spaces := []Space{}
	for rows.Next() {
		var space Space
		if err := rows.Scan(&space.ID, &space.TenantID, &space.Name, &space.Kind, &space.IsDefault, &space.Active, &space.Purpose); err != nil {
			return nil, err
		}
		space.Slug = space.ID
		spaces = append(spaces, space)
	}
	return spaces, rows.Err()
}

func (s *PostgresStore) DocumentPolicies(ctx context.Context, tenantID string, docIDs []string) (map[string]DocumentPolicy, error) {
	policies := map[string]DocumentPolicy{}
	if len(docIDs) == 0 {
		return policies, nil
	}
	rows, err := s.q.Query(ctx, `SELECT doc_id, knowledge_space_id, publication_status, doc_status
		FROM documents WHERE tenant_id=$1 AND doc_id=ANY($2::text[])`, tenantID, docIDs)
	if err != nil {
		return nil, fmt.Errorf("load document policies: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var policy DocumentPolicy
		if err := rows.Scan(&policy.DocID, &policy.KnowledgeSpaceID, &policy.PublicationStatus, &policy.DocumentStatus); err != nil {
			return nil, err
		}
		policies[policy.DocID] = policy
	}
	return policies, rows.Err()
}

func (s *PostgresStore) CreateSpace(ctx context.Context, space Space, creatorUserID string) (Space, error) {
	tx, err := s.q.Begin(ctx)
	if err != nil {
		return Space{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = tx.Exec(ctx, `INSERT INTO knowledge_spaces
		(tenant_id,id,name,kind,is_default,active,purpose) VALUES ($1,$2,$3,$4,false,true,$5)`,
		space.TenantID, space.ID, space.Name, space.Kind, space.Purpose)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Space{}, ErrConflict
		}
		return Space{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO knowledge_space_members
		(tenant_id,space_id,user_id,role) VALUES ($1,$2,$3::uuid,'manager')`,
		space.TenantID, space.ID, creatorUserID)
	if err != nil {
		return Space{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Space{}, err
	}
	return space, nil
}

func scanSpaceError(err error) (Space, bool, error) {
	if isNoRows(err) {
		return Space{}, false, nil
	}
	return Space{}, false, err
}

func isNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

func (s *PostgresStore) UpdateSpacePurpose(ctx context.Context, tenantID, spaceID, purpose string) (Space, error) {
	row := s.q.QueryRow(ctx, `UPDATE knowledge_spaces
		SET purpose=$3, updated_at=now()
		WHERE tenant_id=$1 AND id=$2 AND active
		RETURNING id, tenant_id, name, kind, is_default, active, purpose`,
		tenantID, spaceID, purpose)
	var space Space
	if err := row.Scan(&space.ID, &space.TenantID, &space.Name, &space.Kind, &space.IsDefault, &space.Active, &space.Purpose); err != nil {
		space, ok, scanErr := scanSpaceError(err)
		if scanErr != nil {
			return Space{}, scanErr
		}
		if !ok {
			return Space{}, ErrNotFound
		}
		return space, nil
	}
	space.Slug = space.ID
	return space, nil
}

var _ Store = (*PostgresStore)(nil)

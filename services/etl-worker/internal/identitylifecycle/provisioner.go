// Package identitylifecycle owns atomic provisioning, deprovisioning, identity
// binding, session revocation, tombstones, idempotency, and audit.
package identitylifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"ai-etl-pipeline/internal/audit"
	"ai-etl-pipeline/internal/db"
	"ai-etl-pipeline/internal/externalidentity"
	"ai-etl-pipeline/internal/userstore"
)

var (
	ErrInvalid     = errors.New("identity lifecycle: invalid command")
	ErrNotFound    = errors.New("identity lifecycle: resource not found")
	ErrConflict    = errors.New("identity lifecycle: ownership conflict")
	ErrUnavailable = errors.New("identity lifecycle: unavailable")
)

type Operation string

const (
	OperationCreate Operation = "create"
	OperationUpdate Operation = "update"
	OperationDelete Operation = "delete"
)

type ConnectorPolicy struct {
	ID               string
	TenantID         string
	Issuer           string
	SubjectAttribute string
	DefaultRole      string
}

type LifecycleCommand struct {
	Operation          Operation
	ConnectorID        string
	ProviderResourceID string
	Identity           externalidentity.ExternalIdentity
	Username           string
	DisplayName        *string
	Email              *string
	Active             *bool
	SourceVersion      string
	IdempotencyKey     string
}

type LifecycleResult struct {
	ID                 string `json:"id"`
	InternalUserID     string `json:"internal_user_id"`
	ProviderResourceID string `json:"provider_resource_id"`
	TenantID           string `json:"tenant_id"`
	Username           string `json:"username"`
	DisplayName        string `json:"display_name"`
	Email              string `json:"email"`
	Role               string `json:"role"`
	Active             bool   `json:"active"`
	Deleted            bool   `json:"deleted"`
	SourceVersion      string `json:"source_version"`
}

type Provisioner interface {
	Apply(context.Context, LifecycleCommand) (LifecycleResult, error)
}

type ResourceDirectory interface {
	Get(context.Context, string, string) (LifecycleResult, error)
	FindByUsername(context.Context, string, string) ([]LifecycleResult, error)
}

type PostgresProvisioner struct{ q db.Querier }

func NewPostgresProvisioner(q db.Querier) *PostgresProvisioner {
	return &PostgresProvisioner{q: q}
}

func (p *PostgresProvisioner) Get(ctx context.Context, connectorID, providerResourceID string) (LifecycleResult, error) {
	if p == nil || p.q == nil || strings.TrimSpace(connectorID) == "" || strings.TrimSpace(providerResourceID) == "" {
		return LifecycleResult{}, ErrInvalid
	}
	return scanResource(p.q.QueryRow(ctx, resourceSelect+` WHERE r.connector_id=$1 AND r.provider_resource_id=$2`, connectorID, providerResourceID))
}

func (p *PostgresProvisioner) FindByUsername(ctx context.Context, connectorID, username string) ([]LifecycleResult, error) {
	if p == nil || p.q == nil || strings.TrimSpace(connectorID) == "" || strings.TrimSpace(username) == "" {
		return nil, ErrInvalid
	}
	rows, err := p.q.Query(ctx, resourceSelect+` WHERE r.connector_id=$1 AND lower(u.username)=lower($2)
		ORDER BY r.created_at,r.id LIMIT 2`, connectorID, username)
	if err != nil {
		return nil, fmt.Errorf("%w: find resource", ErrUnavailable)
	}
	defer rows.Close()
	results := []LifecycleResult{}
	for rows.Next() {
		result, scanErr := scanResource(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		results = append(results, result)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: find resource", ErrUnavailable)
	}
	return results, nil
}

func EnsureConnector(ctx context.Context, q db.Querier, policy ConnectorPolicy) error {
	identity, err := externalidentity.Normalize(externalidentity.ExternalIdentity{Issuer: policy.Issuer, Subject: "validation"})
	if err != nil || q == nil || strings.TrimSpace(policy.ID) == "" || strings.TrimSpace(policy.ID) != policy.ID ||
		strings.TrimSpace(policy.TenantID) == "" || strings.TrimSpace(policy.SubjectAttribute) == "" ||
		(policy.DefaultRole != userstore.RoleReadonly && policy.DefaultRole != userstore.RoleUser) {
		return ErrInvalid
	}
	tag, err := q.Exec(ctx, `INSERT INTO identity_provisioning_connectors
		(id,tenant_id,issuer,subject_attribute,default_role) VALUES($1,$2,$3,$4,$5)
		ON CONFLICT(id) DO UPDATE SET subject_attribute=excluded.subject_attribute,
		default_role=excluded.default_role,updated_at=now()
		WHERE identity_provisioning_connectors.tenant_id=excluded.tenant_id
		AND identity_provisioning_connectors.issuer=excluded.issuer`,
		policy.ID, policy.TenantID, identity.Issuer, policy.SubjectAttribute, policy.DefaultRole)
	if err != nil {
		return fmt.Errorf("%w: configure connector", ErrUnavailable)
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func (p *PostgresProvisioner) Apply(ctx context.Context, command LifecycleCommand) (LifecycleResult, error) {
	if p == nil || p.q == nil {
		return LifecycleResult{}, ErrUnavailable
	}
	if err := validateCommand(command); err != nil {
		return LifecycleResult{}, err
	}
	hash, err := commandHash(command)
	if err != nil {
		return LifecycleResult{}, ErrInvalid
	}
	tx, err := p.q.Begin(ctx)
	if err != nil {
		return LifecycleResult{}, fmt.Errorf("%w: begin", ErrUnavailable)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, command.ConnectorID+":"+command.IdempotencyKey); err != nil {
		return LifecycleResult{}, fmt.Errorf("%w: lock idempotency", ErrUnavailable)
	}
	if result, found, err := replay(ctx, tx, command.ConnectorID, command.IdempotencyKey, hash); err != nil {
		return LifecycleResult{}, err
	} else if found {
		return result, nil
	}

	policy, err := loadPolicy(ctx, tx, command.ConnectorID)
	if err != nil {
		return LifecycleResult{}, err
	}
	var result LifecycleResult
	switch command.Operation {
	case OperationCreate:
		result, err = applyCreate(ctx, tx, policy, command)
	case OperationUpdate:
		result, err = applyUpdate(ctx, tx, policy, command)
	case OperationDelete:
		result, err = applyDelete(ctx, tx, policy, command)
	}
	if err != nil {
		return LifecycleResult{}, err
	}
	if err := audit.New(tx).Record(ctx, audit.Entry{
		TenantID: result.TenantID, ActorUserID: "connector:" + command.ConnectorID, ActorRole: "provisioner",
		Action: "identity.lifecycle." + string(command.Operation), ResourceType: "identity_lifecycle_resource", ResourceID: result.ID,
		Result: audit.ResultSuccess, Detail: map[string]any{
			"connector_id": command.ConnectorID, "internal_user_id": result.InternalUserID,
			"provider_resource_hash": identifierHash(command.ProviderResourceID), "source_version": result.SourceVersion,
		},
	}); err != nil {
		return LifecycleResult{}, fmt.Errorf("%w: audit", ErrUnavailable)
	}
	response, err := json.Marshal(result)
	if err != nil {
		return LifecycleResult{}, fmt.Errorf("%w: encode response", ErrUnavailable)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO identity_lifecycle_idempotency
		(connector_id,idempotency_key,request_hash,response) VALUES($1,$2,$3,$4)`,
		command.ConnectorID, command.IdempotencyKey, hash, response); err != nil {
		return LifecycleResult{}, mapWriteError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return LifecycleResult{}, fmt.Errorf("%w: commit", ErrUnavailable)
	}
	return result, nil
}

func validateCommand(command LifecycleCommand) error {
	if command.Operation != OperationCreate && command.Operation != OperationUpdate && command.Operation != OperationDelete {
		return ErrInvalid
	}
	if strings.TrimSpace(command.ConnectorID) == "" || command.ConnectorID != strings.TrimSpace(command.ConnectorID) ||
		strings.TrimSpace(command.ProviderResourceID) == "" || command.ProviderResourceID != strings.TrimSpace(command.ProviderResourceID) ||
		strings.TrimSpace(command.IdempotencyKey) == "" || command.IdempotencyKey != strings.TrimSpace(command.IdempotencyKey) {
		return ErrInvalid
	}
	if len(command.ConnectorID) > 128 || len(command.ProviderResourceID) > 256 || len(command.IdempotencyKey) > 256 ||
		len(command.Username) > 256 || stringPointerLength(command.DisplayName) > 512 || stringPointerLength(command.Email) > 320 || len(command.SourceVersion) > 256 ||
		len(command.Identity.Issuer) > 2048 || len(command.Identity.Subject) > 512 {
		return ErrInvalid
	}
	if command.Operation == OperationCreate && (strings.TrimSpace(command.Username) == "" || command.Active == nil) {
		return ErrInvalid
	}
	return nil
}

func commandHash(command LifecycleCommand) (string, error) {
	encoded, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func identifierHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:8])
}

func replay(ctx context.Context, tx pgx.Tx, connectorID, key, hash string) (LifecycleResult, bool, error) {
	var storedHash string
	var response []byte
	err := tx.QueryRow(ctx, `SELECT request_hash,response FROM identity_lifecycle_idempotency
		WHERE connector_id=$1 AND idempotency_key=$2`, connectorID, key).Scan(&storedHash, &response)
	if errors.Is(err, pgx.ErrNoRows) {
		return LifecycleResult{}, false, nil
	}
	if err != nil {
		return LifecycleResult{}, false, fmt.Errorf("%w: read idempotency", ErrUnavailable)
	}
	if storedHash != hash {
		return LifecycleResult{}, false, ErrConflict
	}
	var result LifecycleResult
	if err := json.Unmarshal(response, &result); err != nil {
		return LifecycleResult{}, false, fmt.Errorf("%w: decode replay", ErrUnavailable)
	}
	return result, true, nil
}

func loadPolicy(ctx context.Context, tx pgx.Tx, connectorID string) (ConnectorPolicy, error) {
	var policy ConnectorPolicy
	err := tx.QueryRow(ctx, `SELECT id,tenant_id,issuer,subject_attribute,default_role
		FROM identity_provisioning_connectors WHERE id=$1`, connectorID).
		Scan(&policy.ID, &policy.TenantID, &policy.Issuer, &policy.SubjectAttribute, &policy.DefaultRole)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConnectorPolicy{}, ErrNotFound
	}
	if err != nil {
		return ConnectorPolicy{}, fmt.Errorf("%w: load connector", ErrUnavailable)
	}
	return policy, nil
}

func applyCreate(ctx context.Context, tx pgx.Tx, policy ConnectorPolicy, command LifecycleCommand) (LifecycleResult, error) {
	identity, err := externalidentity.Normalize(command.Identity)
	if err != nil || identity.Issuer != policy.Issuer {
		return LifecycleResult{}, ErrInvalid
	}
	var result LifecycleResult
	result.ProviderResourceID, result.TenantID, result.Username = command.ProviderResourceID, policy.TenantID, strings.TrimSpace(command.Username)
	result.DisplayName, result.Email, result.Role, result.Active, result.SourceVersion = stringPointerValue(command.DisplayName), stringPointerValue(command.Email), policy.DefaultRole, *command.Active, command.SourceVersion
	err = tx.QueryRow(ctx, `INSERT INTO users(username,password_hash,role,tenant_id,active,origin,display_name,email)
		VALUES($1,'', $2,$3,$4,'scim',$5,$6) RETURNING id`, result.Username, result.Role,
		result.TenantID, result.Active, result.DisplayName, result.Email).Scan(&result.InternalUserID)
	if err != nil {
		return LifecycleResult{}, mapWriteError(err)
	}
	var bindingID string
	err = tx.QueryRow(ctx, `INSERT INTO external_identity_bindings
		(issuer,external_subject,internal_user_id,tenant_id,created_by)
		VALUES($1,$2,$3,$4,$5) RETURNING id`, identity.Issuer, identity.Subject,
		result.InternalUserID, result.TenantID, "connector:"+command.ConnectorID).Scan(&bindingID)
	if err != nil {
		return LifecycleResult{}, mapWriteError(err)
	}
	err = tx.QueryRow(ctx, `INSERT INTO identity_lifecycle_resources
		(connector_id,provider_resource_id,internal_user_id,tenant_id,issuer,external_subject,source_version)
		VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING id`, command.ConnectorID, command.ProviderResourceID,
		result.InternalUserID, result.TenantID, identity.Issuer, identity.Subject, command.SourceVersion).Scan(&result.ID)
	if err != nil {
		return LifecycleResult{}, mapWriteError(err)
	}
	return result, nil
}

func applyUpdate(ctx context.Context, tx pgx.Tx, policy ConnectorPolicy, command LifecycleCommand) (LifecycleResult, error) {
	result, issuer, subject, err := loadResource(ctx, tx, policy, command.ProviderResourceID)
	if err != nil {
		return LifecycleResult{}, err
	}
	if command.Identity.Issuer != "" || command.Identity.Subject != "" {
		identity, normalizeErr := externalidentity.Normalize(command.Identity)
		if normalizeErr != nil || identity.Issuer != issuer || identity.Subject != subject {
			return LifecycleResult{}, ErrConflict
		}
	}
	if result.Deleted && (command.Active == nil || !*command.Active) {
		return LifecycleResult{}, ErrConflict
	}
	if result.Deleted && (command.Identity.Issuer == "" || command.Identity.Subject == "") {
		return LifecycleResult{}, ErrConflict
	}
	if command.Username != "" {
		result.Username = strings.TrimSpace(command.Username)
	}
	if command.DisplayName != nil {
		result.DisplayName = *command.DisplayName
	}
	if command.Email != nil {
		result.Email = *command.Email
	}
	if command.Active != nil {
		result.Active = *command.Active
	}
	result.SourceVersion = command.SourceVersion
	_, err = tx.Exec(ctx, `UPDATE users SET username=$2,display_name=$3,email=$4,active=$5,
		token_version=token_version + CASE WHEN active AND NOT $5 THEN 1 ELSE 0 END,updated_at=now()
		WHERE id=$1`, result.InternalUserID, result.Username, result.DisplayName, result.Email, result.Active)
	if err != nil {
		return LifecycleResult{}, mapWriteError(err)
	}
	_, err = tx.Exec(ctx, `UPDATE identity_lifecycle_resources SET source_version=$2,
		deleted_at=CASE WHEN $3 THEN NULL ELSE deleted_at END,updated_at=now() WHERE id=$1`,
		result.ID, result.SourceVersion, result.Active)
	if err != nil {
		return LifecycleResult{}, mapWriteError(err)
	}
	result.Deleted = false
	return result, nil
}

func applyDelete(ctx context.Context, tx pgx.Tx, policy ConnectorPolicy, command LifecycleCommand) (LifecycleResult, error) {
	result, _, _, err := loadResource(ctx, tx, policy, command.ProviderResourceID)
	if err != nil {
		return LifecycleResult{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE users SET active=false,
		token_version=token_version + CASE WHEN active THEN 1 ELSE 0 END,updated_at=now() WHERE id=$1`, result.InternalUserID)
	if err != nil {
		return LifecycleResult{}, mapWriteError(err)
	}
	_, err = tx.Exec(ctx, `UPDATE identity_lifecycle_resources SET deleted_at=COALESCE(deleted_at,now()),updated_at=now() WHERE id=$1`, result.ID)
	if err != nil {
		return LifecycleResult{}, mapWriteError(err)
	}
	result.Active, result.Deleted = false, true
	return result, nil
}

func loadResource(ctx context.Context, tx pgx.Tx, policy ConnectorPolicy, providerResourceID string) (LifecycleResult, string, string, error) {
	var result LifecycleResult
	var issuer, subject string
	var deletedAt any
	err := tx.QueryRow(ctx, resourceSelect+` WHERE r.connector_id=$1 AND r.provider_resource_id=$2 FOR UPDATE`, policy.ID, providerResourceID).
		Scan(&result.ID, &result.InternalUserID, &result.ProviderResourceID, &result.TenantID,
			&result.Username, &result.DisplayName, &result.Email, &result.Role, &result.Active,
			&deletedAt, &result.SourceVersion, &issuer, &subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return LifecycleResult{}, "", "", ErrNotFound
	}
	if err != nil {
		return LifecycleResult{}, "", "", fmt.Errorf("%w: load resource", ErrUnavailable)
	}
	result.Deleted = deletedAt != nil
	return result, issuer, subject, nil
}

const resourceSelect = `SELECT r.id,r.internal_user_id,r.provider_resource_id,r.tenant_id,
	u.username,u.display_name,u.email,u.role,u.active,r.deleted_at,r.source_version,r.issuer,r.external_subject
	FROM identity_lifecycle_resources r JOIN users u ON u.id=r.internal_user_id AND u.tenant_id=r.tenant_id`

type rowScanner interface{ Scan(...any) error }

func scanResource(row rowScanner) (LifecycleResult, error) {
	var result LifecycleResult
	var issuer, subject string
	var deletedAt any
	err := row.Scan(&result.ID, &result.InternalUserID, &result.ProviderResourceID, &result.TenantID,
		&result.Username, &result.DisplayName, &result.Email, &result.Role, &result.Active,
		&deletedAt, &result.SourceVersion, &issuer, &subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return LifecycleResult{}, ErrNotFound
	}
	if err != nil {
		return LifecycleResult{}, fmt.Errorf("%w: scan resource", ErrUnavailable)
	}
	result.Deleted = deletedAt != nil
	return result, nil
}

func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "23503" || pgErr.Code == "23514") {
		return ErrConflict
	}
	return fmt.Errorf("%w: write", ErrUnavailable)
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringPointerLength(value *string) int {
	return len(stringPointerValue(value))
}

var _ Provisioner = (*PostgresProvisioner)(nil)
var _ ResourceDirectory = (*PostgresProvisioner)(nil)

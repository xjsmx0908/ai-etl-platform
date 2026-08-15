// Package userstore persists users and tenants in PostgreSQL. It backs the
// login flow and the admin user-management API. Passwords are stored as bcrypt
// hashes only; the Store never exposes or returns a plaintext password.
package userstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"ai-etl-pipeline/internal/db"
)

// ErrNotFound is returned by Get-by-key methods when no row matches.
var ErrNotFound = errors.New("userstore: not found")

// ErrDuplicate is returned when a create hits a unique constraint (username or
// tenant id), so handlers can map it to HTTP 409 without depending on pgx.
var ErrDuplicate = errors.New("userstore: duplicate")

// Role constants mirror internal/auth claims values.
const (
	RoleAdmin    = "admin"
	RoleUser     = "user"
	RoleReadonly = "readonly"
)

// User is one row of the users table. PasswordHash is the bcrypt hash, never a
// plaintext password. TokenVersion is bumped on password reset to invalidate
// previously-issued JWTs.
type User struct {
	ID           string
	Username     string
	PasswordHash string
	Role         string
	TenantID     string
	Active       bool
	TokenVersion int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Tenant is one row of the tenants table. ID is a short slug.
type Tenant struct {
	ID        string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// UserPatch is an optional-field update for a user. A nil field is left
// unchanged. Password changes go through SetPasswordHash, never through Update,
// so role/tenant edits cannot be mixed with secret rotation.
type UserPatch struct {
	Role     *string
	TenantID *string
	Active   *bool
}

// Store is the persistence surface used by handlers and the bootstrap.
type Store interface {
	GetByUsername(ctx context.Context, username string) (User, bool, error)
	GetByID(ctx context.Context, id string) (User, bool, error)
	List(ctx context.Context, tenantID string, limit, offset int) ([]User, int, error)
	Create(ctx context.Context, u *User) error
	Update(ctx context.Context, id string, patch UserPatch) (User, error)
	SetPasswordHash(ctx context.Context, id, hash string) error
	Delete(ctx context.Context, id string) error
	ListTenants(ctx context.Context) ([]Tenant, error)
	CreateTenant(ctx context.Context, id, name string) error
	CountUsers(ctx context.Context) (int, error)
}

// PgStore implements Store on PostgreSQL.
type PgStore struct {
	q db.Querier
}

// New returns a Store backed by the given querier (a *db.Pool in production, a
// pgxmock pool in tests).
func New(q db.Querier) *PgStore {
	return &PgStore{q: q}
}

var _ Store = (*PgStore)(nil)

const userColumns = "id, username, password_hash, role, tenant_id, active, token_version, created_at, updated_at"

// GetByUsername looks up a user by case-insensitive username.
func (s *PgStore) GetByUsername(ctx context.Context, username string) (User, bool, error) {
	row := s.q.QueryRow(ctx,
		"SELECT "+userColumns+" FROM users WHERE lower(username)=lower($1)", username)
	return scanUser(row)
}

// GetByID looks up a user by primary key. A non-UUID id (e.g. an offline
// test/eval token whose UserID is a plain label) returns not-found instead of
// hitting the DB, where casting the value to the uuid column would error.
func (s *PgStore) GetByID(ctx context.Context, id string) (User, bool, error) {
	if _, err := uuid.Parse(id); err != nil {
		return User{}, false, nil
	}
	row := s.q.QueryRow(ctx,
		"SELECT "+userColumns+" FROM users WHERE id=$1", id)
	return scanUser(row)
}

func scanUser(row pgx.Row) (User, bool, error) {
	var u User
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TenantID,
		&u.Active, &u.TokenVersion, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, fmt.Errorf("scan user: %w", err)
	}
	return u, true, nil
}

// List returns users in a tenant, newest first, plus the total count.
func (s *PgStore) List(ctx context.Context, tenantID string, limit, offset int) ([]User, int, error) {
	rows, err := s.q.Query(ctx,
		"SELECT "+userColumns+" FROM users WHERE tenant_id=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3",
		tenantID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.Role, &u.TenantID,
			&u.Active, &u.TokenVersion, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, 0, fmt.Errorf("scan user row: %w", err)
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate users: %w", err)
	}

	var total int
	if err := s.q.QueryRow(ctx,
		"SELECT count(*) FROM users WHERE tenant_id=$1", tenantID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count users: %w", err)
	}
	return users, total, nil
}

// Create inserts a user and back-fills id/created_at/updated_at on the argument.
func (s *PgStore) Create(ctx context.Context, u *User) error {
	err := s.q.QueryRow(ctx,
		`INSERT INTO users (username, password_hash, role, tenant_id, active)
		 VALUES ($1, $2, $3, $4, $5)
		 RETURNING id, created_at, updated_at`,
		u.Username, u.PasswordHash, u.Role, u.TenantID, u.Active,
	).Scan(&u.ID, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

// Update applies a patch to role/tenant/active and returns the refreshed user.
// An empty patch is a no-op that still returns the current row.
func (s *PgStore) Update(ctx context.Context, id string, patch UserPatch) (User, error) {
	var sets []string
	var args []any
	if patch.Role != nil {
		args = append(args, *patch.Role)
		sets = append(sets, fmt.Sprintf("role=$%d", len(args)))
	}
	if patch.TenantID != nil {
		args = append(args, *patch.TenantID)
		sets = append(sets, fmt.Sprintf("tenant_id=$%d", len(args)))
	}
	if patch.Active != nil {
		args = append(args, *patch.Active)
		sets = append(sets, fmt.Sprintf("active=$%d", len(args)))
	}
	if len(sets) == 0 {
		u, found, err := s.GetByID(ctx, id)
		if err != nil {
			return User{}, err
		}
		if !found {
			return User{}, ErrNotFound
		}
		return u, nil
	}

	sets = append(sets, "updated_at=now()")
	args = append(args, id)
	query := "UPDATE users SET " + strings.Join(sets, ", ") +
		" WHERE id=$" + fmt.Sprint(len(args)) +
		" RETURNING " + userColumns
	row := s.q.QueryRow(ctx, query, args...)
	u, found, err := scanUser(row)
	if err != nil {
		return User{}, err
	}
	if !found {
		return User{}, ErrNotFound
	}
	return u, nil
}

// SetPasswordHash updates the bcrypt hash and bumps token_version so all
// previously-issued JWTs for the user are rejected on their next use.
func (s *PgStore) SetPasswordHash(ctx context.Context, id, hash string) error {
	_, err := s.q.Exec(ctx,
		"UPDATE users SET password_hash=$2, token_version=token_version+1, updated_at=now() WHERE id=$1", id, hash)
	if err != nil {
		return fmt.Errorf("set password hash: %w", err)
	}
	return nil
}

// Delete removes a user by primary key. It is a no-op if the user does not exist.
func (s *PgStore) Delete(ctx context.Context, id string) error {
	_, err := s.q.Exec(ctx, "DELETE FROM users WHERE id=$1", id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}

// ListTenants returns all tenants ordered by id.
func (s *PgStore) ListTenants(ctx context.Context) ([]Tenant, error) {
	rows, err := s.q.Query(ctx,
		"SELECT id, name, created_at, updated_at FROM tenants ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()

	tenants := []Tenant{}
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		tenants = append(tenants, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate tenants: %w", err)
	}
	return tenants, nil
}

// CreateTenant inserts a tenant; returns ErrDuplicate on a duplicate id.
func (s *PgStore) CreateTenant(ctx context.Context, id, name string) error {
	_, err := s.q.Exec(ctx,
		"INSERT INTO tenants (id, name) VALUES ($1, $2)", id, name)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicate
		}
		return fmt.Errorf("insert tenant: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// CountUsers reports the total number of users, used by the first-run bootstrap
// to decide whether to provision the initial admin.
func (s *PgStore) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.q.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

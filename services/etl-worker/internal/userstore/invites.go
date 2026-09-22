package userstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// InviteState is the derived state of an invite at a point in time.
type InviteState string

const (
	InvitePending  InviteState = "pending"
	InviteConsumed InviteState = "consumed"
	InviteRevoked  InviteState = "revoked"
	InviteExpired  InviteState = "expired"
)

// ErrInviteNotUsable is returned when a presented token does not correspond to a
// usable invite: unknown, expired, already consumed, or revoked.
//
// The four cases collapse into one error on purpose. They are distinguished for
// an administrator (see Invite.State) but not for the person holding a token:
// separate responses would turn the accept endpoint into an oracle for which
// tokens once existed.
var ErrInviteNotUsable = errors.New("userstore: invite is not usable")

// Invite is one row of user_invites.
//
// The token is deliberately absent: only its digest is stored, and the plaintext
// exists solely in the response that created it. Nothing in this type can be
// used to reconstruct a link.
type Invite struct {
	ID             string
	TenantID       string
	Username       string
	DisplayName    string
	Email          string
	Role           string
	CreatedBy      string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	ConsumedAt     *time.Time
	ConsumedUserID string
	RevokedAt      *time.Time
	RevokedBy      string
}

// State reports where the invite stands at now.
//
// Derived rather than stored: an expired invite is still an unconsumed row in
// the database, and a status column would need something to run on a schedule
// and rewrite it. Anything that needs to know "can this still be used" asks
// here, so the answer cannot drift from the clock.
func (i Invite) State(now time.Time) InviteState {
	switch {
	case i.RevokedAt != nil:
		return InviteRevoked
	case i.ConsumedAt != nil:
		return InviteConsumed
	case !now.Before(i.ExpiresAt):
		return InviteExpired
	default:
		return InvitePending
	}
}

// InviteStore is the invitation surface.
//
// It is separate from Store rather than folded into it because creating
// invitations and consuming them have nothing else in common with user
// administration, and a narrow seam keeps test fakes small.
type InviteStore interface {
	CreateInvite(ctx context.Context, invite *Invite, digest [32]byte) error
	ListInvites(ctx context.Context, tenantID string, limit, offset int) ([]Invite, int, error)
	RevokeInvite(ctx context.Context, tenantID, id, revokedBy string) error
	GetInviteByDigest(ctx context.Context, digest [32]byte) (Invite, bool, error)
	ConsumeInvite(ctx context.Context, digest [32]byte, passwordHash string) (User, error)
}

var _ InviteStore = (*PgStore)(nil)

// The statements whose WHERE clauses are the security properties of this feature
// are named constants rather than inline strings, so a test can assert they still
// carry their guards.
//
// That test is a tripwire and is labelled as one: pgxmock returns whatever the
// expectation says, so no unit test can prove the guards are *in* the statement.
// What a test can do is fail when someone edits the SQL and drops one -- which is
// the realistic way this breaks. The behaviour itself is verified against a real
// database in the acceptance run.
const (
	// supersedeLiveInviteSQL retires the previous working link for a username.
	supersedeLiveInviteSQL = `
		UPDATE user_invites
		   SET revoked_at = now(), revoked_by = $3
		 WHERE tenant_id = $1 AND lower(username) = lower($2)
		   AND consumed_at IS NULL AND revoked_at IS NULL`

	// claimInviteSQL is the entire validity check for a token. Every clause is
	// load-bearing: without consumed_at IS NULL the token is reusable, without
	// revoked_at IS NULL a revoked link keeps working, and without
	// expires_at > now() an old link never dies.
	claimInviteSQL = `
		UPDATE user_invites
		   SET consumed_at = now()
		 WHERE token_digest = $1
		   AND consumed_at IS NULL
		   AND revoked_at IS NULL
		   AND expires_at > now()
		RETURNING id, tenant_id, username, display_name, email, role`

	// revokeInviteSQL is tenant-scoped: an administrator may not revoke another
	// tenant's invite even knowing its id.
	revokeInviteSQL = `
		UPDATE user_invites
		   SET revoked_at = now(), revoked_by = $3
		 WHERE id = $1 AND tenant_id = $2
		   AND consumed_at IS NULL AND revoked_at IS NULL`
)

// consumed_user_id is rendered as text so a deleted account leaves an empty
// string rather than a scan error: the FK is ON DELETE SET NULL, so the column
// is legitimately NULL while consumed_at is still set.
const inviteColumns = "id, tenant_id, username, display_name, email, role, created_by, " +
	"created_at, expires_at, consumed_at, coalesce(consumed_user_id::text, ''), revoked_at, revoked_by"

func scanInvite(row pgx.Row) (Invite, bool, error) {
	var invite Invite
	err := row.Scan(
		&invite.ID, &invite.TenantID, &invite.Username, &invite.DisplayName, &invite.Email,
		&invite.Role, &invite.CreatedBy, &invite.CreatedAt, &invite.ExpiresAt,
		&invite.ConsumedAt, &invite.ConsumedUserID, &invite.RevokedAt, &invite.RevokedBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Invite{}, false, nil
	}
	if err != nil {
		return Invite{}, false, fmt.Errorf("scan invite: %w", err)
	}
	return invite, true, nil
}

// CreateInvite stores a new pending invite and supersedes any live invite for
// the same username in the same tenant.
//
// Superseding rather than failing: the partial unique index allows one live
// invite per username, and an administrator who re-invites after a link went
// astray should get a working link, not a constraint violation. The previous row
// is marked revoked rather than deleted, so the audit trail keeps it and the old
// link stops working immediately.
func (s *PgStore) CreateInvite(ctx context.Context, invite *Invite, digest [32]byte) error {
	tx, err := s.q.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin create invite: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, supersedeLiveInviteSQL,
		invite.TenantID, invite.Username, invite.CreatedBy); err != nil {
		return fmt.Errorf("supersede previous invite: %w", err)
	}

	if err := tx.QueryRow(ctx, `
		INSERT INTO user_invites
			(tenant_id, username, display_name, email, role, token_digest, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at`,
		invite.TenantID, invite.Username, invite.DisplayName, invite.Email,
		invite.Role, digest[:], invite.CreatedBy, invite.ExpiresAt,
	).Scan(&invite.ID, &invite.CreatedAt); err != nil {
		return fmt.Errorf("insert invite: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit create invite: %w", err)
	}
	return nil
}

// ListInvites returns a tenant's invites, newest first, with the total count so
// the caller can paginate.
func (s *PgStore) ListInvites(ctx context.Context, tenantID string, limit, offset int) ([]Invite, int, error) {
	rows, err := s.q.Query(ctx, `
		SELECT `+inviteColumns+`
		  FROM user_invites
		 WHERE tenant_id = $1
		 ORDER BY created_at DESC, id
		 LIMIT $2 OFFSET $3`, tenantID, limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list invites: %w", err)
	}
	defer rows.Close()

	var invites []Invite
	for rows.Next() {
		invite, _, err := scanInvite(rows)
		if err != nil {
			return nil, 0, err
		}
		invites = append(invites, invite)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate invites: %w", err)
	}

	var total int
	if err := s.q.QueryRow(ctx, `SELECT count(*) FROM user_invites WHERE tenant_id = $1`, tenantID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count invites: %w", err)
	}
	return invites, total, nil
}

// RevokeInvite marks a pending invite as revoked.
//
// Only pending invites can be revoked: consuming and revoking are both terminal,
// and the CHECK constraint enforces it. A row that was already consumed is
// therefore reported as not found -- the caller has nothing to revoke.
func (s *PgStore) RevokeInvite(ctx context.Context, tenantID, id, revokedBy string) error {
	tag, err := s.q.Exec(ctx, revokeInviteSQL, id, tenantID, revokedBy)
	if err != nil {
		return fmt.Errorf("revoke invite: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetInviteByDigest looks up an invite by the digest of a presented token,
// whatever its state. The caller decides what a consumed or revoked invite means
// for the request it is serving; this only reports what is there.
func (s *PgStore) GetInviteByDigest(ctx context.Context, digest [32]byte) (Invite, bool, error) {
	return scanInvite(s.q.QueryRow(ctx,
		`SELECT `+inviteColumns+` FROM user_invites WHERE token_digest = $1`, digest[:]))
}

// ConsumeInvite atomically turns a pending invite into an account and returns the
// created user.
//
// The claim and the insert share one transaction, and the claim is a single
// UPDATE whose WHERE clause is the whole validity check (unconsumed, unrevoked,
// unexpired). That combination is what makes the token single-use under
// concurrency: a second request with the same token blocks on the row lock,
// re-evaluates the predicate after the first commit, matches nothing, and gets
// ErrInviteNotUsable instead of creating a duplicate account.
//
// Expiry is compared against the database clock, not the caller's, so a skewed
// application node cannot extend or shorten a link.
func (s *PgStore) ConsumeInvite(ctx context.Context, digest [32]byte, passwordHash string) (User, error) {
	tx, err := s.q.Begin(ctx)
	if err != nil {
		return User{}, fmt.Errorf("begin consume invite: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var invite Invite
	err = tx.QueryRow(ctx, claimInviteSQL, digest[:]).Scan(
		&invite.ID, &invite.TenantID, &invite.Username, &invite.DisplayName, &invite.Email, &invite.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrInviteNotUsable
	}
	if err != nil {
		return User{}, fmt.Errorf("claim invite: %w", err)
	}

	// The invite decides tenant, role and username. Nothing from the request
	// reaches these fields: the person accepting a link chooses a password and
	// nothing else.
	user := &User{
		Username:     invite.Username,
		PasswordHash: passwordHash,
		Role:         invite.Role,
		TenantID:     invite.TenantID,
		Active:       true,
		Origin:       OriginLocal,
		DisplayName:  invite.DisplayName,
		Email:        invite.Email,
	}
	if err := insertUser(ctx, tx, user); err != nil {
		// A duplicate username is not a server fault: the account appeared
		// between the invite being issued and the link being used.
		if errors.Is(err, ErrDuplicate) {
			return User{}, ErrDuplicate
		}
		return User{}, err
	}

	if _, err := tx.Exec(ctx, `UPDATE user_invites SET consumed_user_id = $2 WHERE id = $1`,
		invite.ID, user.ID); err != nil {
		return User{}, fmt.Errorf("link invite to user: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return User{}, fmt.Errorf("commit consume invite: %w", err)
	}
	return *user, nil
}

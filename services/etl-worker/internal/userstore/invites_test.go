package userstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v5"
)

// The column list the invite queries select. Kept as a substring pattern rather
// than the full SELECT: pgxmock treats the expectation as a regular expression,
// and the column list contains parentheses and quotes that would otherwise have
// to be escaped.
const (
	inviteLookupPattern = "FROM user_invites"
	inviteClaimPattern  = "consumed_at = now"
)

func newInviteMock(t *testing.T) pgxmock.PgxPoolIface {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	t.Cleanup(mock.Close)
	return mock
}

func inviteRow(id, username, role string, createdAt, expiresAt time.Time) *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "tenant_id", "username", "display_name", "email", "role", "created_by",
		"created_at", "expires_at", "consumed_at", "coalesce", "revoked_at", "revoked_by",
	}).AddRow(id, "default", username, "", "", role, "admin-1", createdAt, expiresAt, nil, "", nil, "")
}

// TestInviteState covers the derived state machine, including the precedence
// question the database cannot answer for us: a revoked invite whose expiry has
// also passed is reported as revoked, because that is the terminal state it
// actually reached. Reporting it as expired would hide the fact that an
// administrator acted on it.
func TestInviteState(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	cases := []struct {
		name   string
		invite Invite
		want   InviteState
	}{
		{"live invite is pending", Invite{ExpiresAt: future}, InvitePending},
		{"consumed beats the clock", Invite{ExpiresAt: future, ConsumedAt: &past}, InviteConsumed},
		{"revoked beats the clock", Invite{ExpiresAt: future, RevokedAt: &past}, InviteRevoked},
		{"unconsumed past expiry is expired", Invite{ExpiresAt: past}, InviteExpired},
		{"expired then revoked is revoked", Invite{ExpiresAt: past, RevokedAt: &past}, InviteRevoked},
		// The boundary: expires_at == now is already expired. A link that expires
		// "at" an instant must not be usable at that instant, or the claim query
		// (expires_at > now()) and this helper would disagree.
		{"expiry exactly now is expired", Invite{ExpiresAt: now}, InviteExpired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.invite.State(now); got != tc.want {
				t.Fatalf("State = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestInviteStateAgreesWithTheClaimPredicate pins the helper to the SQL it has to
// agree with.
//
// State() decides what the API reports; the claim inside ConsumeInvite decides
// what the API allows. If they disagree, a link can be shown as usable and then
// refused -- or worse, shown as expired and still work. The claim uses
// expires_at > now(), so an invite is pending exactly when ExpiresAt is strictly
// after now.
func TestInviteStateAgreesWithTheClaimPredicate(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond, time.Hour} {
		invite := Invite{ExpiresAt: now.Add(delta)}
		claimWouldAccept := invite.ExpiresAt.After(now)
		stateSaysUsable := invite.State(now) == InvitePending
		if claimWouldAccept != stateSaysUsable {
			t.Fatalf("at delta %s: claim would accept=%v but State says usable=%v",
				delta, claimWouldAccept, stateSaysUsable)
		}
	}
}

func TestCreateInvite_SupersedesThePreviousLiveInvite(t *testing.T) {
	mock := newInviteMock(t)
	now := time.Now()
	digest := [32]byte{1, 2, 3}

	mock.ExpectBegin()
	mock.ExpectExec("revoked_at = now").
		WithArgs("default", "alice", "admin-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO user_invites").
		WithArgs("default", "alice", "", "", RoleUser, digest[:], "admin-1", now.Add(72*time.Hour)).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow("inv-1", now))
	mock.ExpectCommit()

	invite := &Invite{
		TenantID: "default", Username: "alice", Role: RoleUser,
		CreatedBy: "admin-1", ExpiresAt: now.Add(72 * time.Hour),
	}
	if err := New(mock).CreateInvite(context.Background(), invite, digest); err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	if invite.ID != "inv-1" {
		t.Fatalf("expected the generated id to be backfilled, got %q", invite.ID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestGetInviteByDigest_NotFound(t *testing.T) {
	mock := newInviteMock(t)
	digest := [32]byte{9}
	mock.ExpectQuery(inviteLookupPattern).
		WithArgs(digest[:]).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "tenant_id", "username", "display_name", "email", "role", "created_by",
			"created_at", "expires_at", "consumed_at", "coalesce", "revoked_at", "revoked_by",
		}))

	_, found, err := New(mock).GetInviteByDigest(context.Background(), digest)
	if err != nil {
		t.Fatalf("GetInviteByDigest: %v", err)
	}
	if found {
		t.Fatal("expected not found")
	}
}

func TestRevokeInvite_NotFoundWhenThereIsNothingToRevoke(t *testing.T) {
	mock := newInviteMock(t)
	// Zero rows affected is the answer for an invite that does not exist, belongs
	// to another tenant, was already consumed, or was already revoked. The store
	// collapses all four into ErrNotFound on purpose.
	mock.ExpectExec("revoked_at = now").
		WithArgs("inv-1", "default", "admin-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err := New(mock).RevokeInvite(context.Background(), "default", "inv-1", "admin-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestConsumeInvite_ClaimsAndCreatesInOneTransaction(t *testing.T) {
	mock := newInviteMock(t)
	now := time.Now()
	digest := [32]byte{7}

	mock.ExpectBegin()
	mock.ExpectQuery(inviteClaimPattern).
		WithArgs(digest[:]).
		WillReturnRows(pgxmock.NewRows([]string{"id", "tenant_id", "username", "display_name", "email", "role"}).
			AddRow("inv-1", "default", "alice", "Alice", "alice@example.com", RoleUser))
	mock.ExpectQuery("INSERT INTO users").
		WithArgs("alice", "hash", RoleUser, "default", true, OriginLocal, "Alice", "alice@example.com").
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow("u-9", now, now))
	mock.ExpectExec("UPDATE user_invites SET consumed_user_id").
		WithArgs("inv-1", "u-9").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	user, err := New(mock).ConsumeInvite(context.Background(), digest, "hash")
	if err != nil {
		t.Fatalf("ConsumeInvite: %v", err)
	}
	// Tenant, username and role come from the invite, never from the caller.
	if user.ID != "u-9" || user.TenantID != "default" || user.Role != RoleUser || user.Username != "alice" {
		t.Fatalf("unexpected user: %+v", user)
	}
	if !user.Active || user.Origin != OriginLocal {
		t.Fatalf("a self-service account must be active and local: %+v", user)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestConsumeInvite_NotUsableCreatesNoUser is the regression test for the token's
// single-use property.
//
// The claim UPDATE carries the entire validity check in its WHERE clause, so an
// unknown, expired, revoked or already-used token matches no row. The assertion
// that matters is not just the error: it is that no user insert follows, and that
// the transaction is rolled back rather than left open.
func TestConsumeInvite_NotUsableCreatesNoUser(t *testing.T) {
	mock := newInviteMock(t)
	digest := [32]byte{3}

	mock.ExpectBegin()
	mock.ExpectQuery(inviteClaimPattern).
		WithArgs(digest[:]).
		WillReturnRows(pgxmock.NewRows([]string{"id", "tenant_id", "username", "display_name", "email", "role"}))
	mock.ExpectRollback()

	_, err := New(mock).ConsumeInvite(context.Background(), digest, "hash")
	if !errors.Is(err, ErrInviteNotUsable) {
		t.Fatalf("err = %v, want ErrInviteNotUsable", err)
	}
	// No INSERT INTO users expectation was registered, so if the code tried to
	// create one, pgxmock would report an unexpected call here.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestConsumeInvite_RollsBackWhenTheUsernameIsTaken guards the pairing between the
// claim and the insert.
//
// The invite is marked consumed by the same transaction that creates the account.
// If the insert fails, the claim has to disappear with it -- otherwise a link
// whose username was taken in the meantime would be burned without producing an
// account, and the invitee would have nothing to retry with.
func TestConsumeInvite_RollsBackWhenTheUsernameIsTaken(t *testing.T) {
	mock := newInviteMock(t)
	digest := [32]byte{4}

	mock.ExpectBegin()
	mock.ExpectQuery(inviteClaimPattern).
		WithArgs(digest[:]).
		WillReturnRows(pgxmock.NewRows([]string{"id", "tenant_id", "username", "display_name", "email", "role"}).
			AddRow("inv-1", "default", "alice", "", "", RoleUser))
	mock.ExpectQuery("INSERT INTO users").
		WithArgs("alice", "hash", RoleUser, "default", true, OriginLocal, "", "").
		WillReturnError(&pgconn.PgError{Code: "23505", Message: "duplicate key value"})
	mock.ExpectRollback()

	_, err := New(mock).ConsumeInvite(context.Background(), digest, "hash")
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("err = %v, want ErrDuplicate", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

func TestListInvites_ReturnsItemsAndTotal(t *testing.T) {
	mock := newInviteMock(t)
	now := time.Now()
	mock.ExpectQuery("ORDER BY created_at DESC").
		WithArgs("default", 20, 0).
		WillReturnRows(inviteRow("inv-1", "alice", RoleUser, now, now.Add(time.Hour)))
	mock.ExpectQuery("count").
		WithArgs("default").
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))

	items, total, err := New(mock).ListInvites(context.Background(), "default", 20, 0)
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	if len(items) != 1 || items[0].Username != "alice" || total != 1 {
		t.Fatalf("unexpected result: items=%+v total=%d", items, total)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}

// TestInviteSQLCarriesItsGuards is a tripwire, and it is labelled as one on
// purpose.
//
// pgxmock returns whatever the expectation says, so no unit test can prove that a
// guard is *in* the statement -- mutating "AND tenant_id = $2" out of
// revokeInviteSQL leaves every store test green, which is exactly how this was
// discovered. The handler-level tenant test does not help either: it runs against
// the fake store, so it verifies the handler and the fake, not the SQL.
//
// What this test can do is fail when someone edits the SQL and drops a guard,
// which is the realistic way the property breaks. The behaviour those guards
// produce is verified against a real database in the acceptance run: reuse of a
// consumed token, a revoked link, an expired link, and a cross-tenant revoke.
func TestInviteSQLCarriesItsGuards(t *testing.T) {
	cases := []struct {
		name      string
		statement string
		guards    []string
	}{
		{
			name:      "the claim is what makes a token single-use and mortal",
			statement: claimInviteSQL,
			guards:    []string{"token_digest = $1", "consumed_at IS NULL", "revoked_at IS NULL", "expires_at > now()"},
		},
		{
			name:      "revoke is tenant-scoped and only touches a live invite",
			statement: revokeInviteSQL,
			guards:    []string{"id = $1", "tenant_id = $2", "consumed_at IS NULL", "revoked_at IS NULL"},
		},
		{
			name:      "supersede is tenant-scoped and only touches a live invite",
			statement: supersedeLiveInviteSQL,
			guards:    []string{"tenant_id = $1", "lower(username) = lower($2)", "consumed_at IS NULL", "revoked_at IS NULL"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, guard := range tc.guards {
				if !strings.Contains(tc.statement, guard) {
					t.Errorf("missing %q -- dropping it silently weakens the invite flow", guard)
				}
			}
		})
	}
}

// TestInviteColumnsNeverSelectTheToken is a source-level guard, not a behavioural
// one: the invite queries must never select a token column, because there is none
// -- only the digest is stored. If someone adds one to make the listing
// convenient, this fails and forces the conversation.
func TestInviteColumnsNeverSelectTheToken(t *testing.T) {
	if strings.Contains(inviteColumns, "token") {
		t.Fatalf("inviteColumns must not carry the token or its digest: %q", inviteColumns)
	}
	if !strings.Contains(inviteColumns, "coalesce(consumed_user_id") {
		t.Fatalf("expected consumed_user_id to be selected as text, got %q", inviteColumns)
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/userstore"
)

// fakeInviteStore is an in-memory userstore.InviteStore.
//
// It mirrors the two behaviours the real store guarantees and the handlers rely
// on: a new invite supersedes any live invite for the same username, and
// consuming an invite either creates the account or leaves the invite untouched
// (never one without the other).
type fakeInviteStore struct {
	invites map[string]*fakeInvite // by invite id
	order   []string
	users   *fakeUserStore
	now     time.Time
}

type fakeInvite struct {
	invite userstore.Invite
	digest [32]byte
}

func newFakeInviteStore(users *fakeUserStore) *fakeInviteStore {
	return &fakeInviteStore{
		invites: map[string]*fakeInvite{},
		users:   users,
		now:     time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC),
	}
}

var _ userstore.InviteStore = (*fakeInviteStore)(nil)

func (f *fakeInviteStore) CreateInvite(_ context.Context, invite *userstore.Invite, digest [32]byte) error {
	for _, existing := range f.invites {
		if existing.invite.TenantID == invite.TenantID &&
			strings.EqualFold(existing.invite.Username, invite.Username) &&
			existing.invite.State(f.now) == userstore.InvitePending {
			revoked := f.now
			existing.invite.RevokedAt = &revoked
			existing.invite.RevokedBy = invite.CreatedBy
		}
	}
	invite.ID = fmt.Sprintf("inv-%d", len(f.order)+1)
	invite.CreatedAt = f.now
	f.invites[invite.ID] = &fakeInvite{invite: *invite, digest: digest}
	f.order = append(f.order, invite.ID)
	return nil
}

func (f *fakeInviteStore) ListInvites(_ context.Context, tenantID string, limit, offset int) ([]userstore.Invite, int, error) {
	var matching []userstore.Invite
	for _, id := range f.order {
		if f.invites[id].invite.TenantID == tenantID {
			matching = append(matching, f.invites[id].invite)
		}
	}
	sort.SliceStable(matching, func(i, j int) bool { return matching[i].CreatedAt.After(matching[j].CreatedAt) })
	total := len(matching)
	if offset >= total {
		return nil, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return matching[offset:end], total, nil
}

func (f *fakeInviteStore) RevokeInvite(_ context.Context, tenantID, id, revokedBy string) error {
	entry, ok := f.invites[id]
	if !ok || entry.invite.TenantID != tenantID || entry.invite.State(f.now) != userstore.InvitePending {
		return userstore.ErrNotFound
	}
	revoked := f.now
	entry.invite.RevokedAt = &revoked
	entry.invite.RevokedBy = revokedBy
	return nil
}

func (f *fakeInviteStore) GetInviteByDigest(_ context.Context, digest [32]byte) (userstore.Invite, bool, error) {
	for _, entry := range f.invites {
		if entry.digest == digest {
			return entry.invite, true, nil
		}
	}
	return userstore.Invite{}, false, nil
}

func (f *fakeInviteStore) ConsumeInvite(_ context.Context, digest [32]byte, passwordHash string) (userstore.User, error) {
	for _, entry := range f.invites {
		if entry.digest != digest {
			continue
		}
		if entry.invite.State(f.now) != userstore.InvitePending {
			return userstore.User{}, userstore.ErrInviteNotUsable
		}
		user := &userstore.User{
			Username:     entry.invite.Username,
			PasswordHash: passwordHash,
			Role:         entry.invite.Role,
			TenantID:     entry.invite.TenantID,
			Active:       true,
			Origin:       userstore.OriginLocal,
			DisplayName:  entry.invite.DisplayName,
			Email:        entry.invite.Email,
		}
		// Create first: if the username was taken since the invite was issued,
		// nothing is consumed, exactly as the transactional version behaves.
		if err := f.users.Create(context.Background(), user); err != nil {
			return userstore.User{}, err
		}
		consumed := f.now
		entry.invite.ConsumedAt = &consumed
		entry.invite.ConsumedUserID = user.ID
		return *user, nil
	}
	return userstore.User{}, userstore.ErrInviteNotUsable
}

// doInviteRequest is doRequest plus path values, which the invite routes need for
// {token} and {inviteID}.
func doInviteRequest(t *testing.T, handler http.Handler, method, target string, body any, ctx context.Context, pathValues map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, reader).WithContext(ctx)
	for name, value := range pathValues {
		req.SetPathValue(name, value)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func inviteTestConfig() config.Config {
	return config.Config{InviteTTL: 72 * time.Hour}
}

func TestCreateInvite_ReturnsTheTokenExactlyOnce(t *testing.T) {
	users := newFakeUserStore()
	invites := newFakeInviteStore(users)
	handler := handleInvites(inviteTestConfig(), users, invites, nil)

	rec := doInviteRequest(t, handler, http.MethodPost, "/v1/invites",
		map[string]string{"username": "alice", "role": userstore.RoleUser},
		ctxWithUserAndTenant("admin-1", "default"), nil)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	var created struct {
		Token      string `json:"token"`
		AcceptPath string `json:"accept_path"`
		Invite     struct {
			ID       string `json:"id"`
			Username string `json:"username"`
			Role     string `json:"role"`
			State    string `json:"state"`
		} `json:"invite"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Token == "" {
		t.Fatal("the create response must carry the token: it is the only time it exists")
	}
	if created.AcceptPath != "/accept-invite/"+created.Token {
		t.Fatalf("accept_path = %q, want it to embed the token", created.AcceptPath)
	}
	if created.Invite.State != string(userstore.InvitePending) || created.Invite.Username != "alice" {
		t.Fatalf("unexpected invite view: %+v", created.Invite)
	}

	// The stored row must hold the digest, not the token.
	stored, found, err := invites.GetInviteByDigest(context.Background(), auth.DigestInviteToken(created.Token))
	if err != nil || !found {
		t.Fatalf("stored invite not found by digest: found=%v err=%v", found, err)
	}
	if stored.ID != created.Invite.ID {
		t.Fatalf("digest lookup returned the wrong invite: %q", stored.ID)
	}
}

// TestListInvites_NeverEchoesTheToken is the guard on the property that makes the
// digest-only design worth having: if a listing could hand back tokens, an
// administrator (or anything that can read the audit trail) could re-issue any
// outstanding invite, and storing only digests would buy nothing.
func TestListInvites_NeverEchoesTheToken(t *testing.T) {
	users := newFakeUserStore()
	invites := newFakeInviteStore(users)
	create := handleInvites(inviteTestConfig(), users, invites, nil)

	created := doInviteRequest(t, create, http.MethodPost, "/v1/invites",
		map[string]string{"username": "alice", "role": userstore.RoleUser},
		ctxWithUserAndTenant("admin-1", "default"), nil)
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	listed := doInviteRequest(t, create, http.MethodGet, "/v1/invites", nil,
		ctxWithUserAndTenant("admin-1", "default"), nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", listed.Code, listed.Body.String())
	}
	if strings.Contains(listed.Body.String(), body.Token) {
		t.Fatalf("the listing echoed the invite token: %s", listed.Body.String())
	}
	if strings.Contains(listed.Body.String(), `"token"`) {
		t.Fatalf("the listing has a token field at all: %s", listed.Body.String())
	}
	if !strings.Contains(listed.Body.String(), `"alice"`) {
		t.Fatalf("the listing is missing the invite it should show: %s", listed.Body.String())
	}
}

// TestAcceptInvite_TakesTenantAndRoleFromTheInvite is the security assertion of
// this feature: the accept endpoint is unauthenticated, so a request that carries
// role or tenant of its own must be ignored rather than honoured.
func TestAcceptInvite_TakesTenantAndRoleFromTheInvite(t *testing.T) {
	users := newFakeUserStore()
	invites := newFakeInviteStore(users)
	create := handleInvites(inviteTestConfig(), users, invites, nil)
	accept := handleAcceptInvite(invites, nil)

	created := doInviteRequest(t, create, http.MethodPost, "/v1/invites",
		map[string]string{"username": "alice", "role": userstore.RoleReadonly},
		ctxWithUserAndTenant("admin-1", "default"), nil)
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	rec := doInviteRequest(t, accept, http.MethodPost, "/v1/auth/invites/accept",
		map[string]any{
			"token":    body.Token,
			"password": "a-long-enough-password",
			// Everything below is an attempt to promote the new account. None of
			// it may reach the stored user.
			"role":      userstore.RoleAdmin,
			"tenant_id": "someone-elses-tenant",
			"username":  "mallory",
			"active":    false,
		}, context.Background(), nil)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rec.Code, rec.Body.String())
	}
	user, found, err := users.GetByUsername(context.Background(), "alice")
	if err != nil || !found {
		t.Fatalf("the invitee's account was not created: found=%v err=%v", found, err)
	}
	if user.Role != userstore.RoleReadonly {
		t.Errorf("role = %q, want the invite's %q -- the request must not choose its own", user.Role, userstore.RoleReadonly)
	}
	if user.TenantID != "default" {
		t.Errorf("tenant = %q, want the invite's tenant", user.TenantID)
	}
	if !user.Active {
		t.Error("a self-service account must be active")
	}
	if _, found, _ := users.GetByUsername(context.Background(), "mallory"); found {
		t.Error("the request's username was used instead of the invite's")
	}
}

func TestAcceptInvite_UnknownTokenIsNotFound(t *testing.T) {
	users := newFakeUserStore()
	accept := handleAcceptInvite(newFakeInviteStore(users), nil)

	rec := doInviteRequest(t, accept, http.MethodPost, "/v1/auth/invites/accept",
		map[string]string{"token": "not-a-real-token", "password": "a-long-enough-password"},
		context.Background(), nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
}

// TestAcceptInvite_IsSingleUse is the behavioural half of the single-use
// guarantee: the second presentation of a token must fail, and it must not
// produce a second account.
func TestAcceptInvite_IsSingleUse(t *testing.T) {
	users := newFakeUserStore()
	invites := newFakeInviteStore(users)
	create := handleInvites(inviteTestConfig(), users, invites, nil)
	accept := handleAcceptInvite(invites, nil)

	created := doInviteRequest(t, create, http.MethodPost, "/v1/invites",
		map[string]string{"username": "alice", "role": userstore.RoleUser},
		ctxWithUserAndTenant("admin-1", "default"), nil)
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	first := doInviteRequest(t, accept, http.MethodPost, "/v1/auth/invites/accept",
		map[string]string{"token": body.Token, "password": "a-long-enough-password"},
		context.Background(), nil)
	if first.Code != http.StatusCreated {
		t.Fatalf("first accept: status = %d, want 201; body=%s", first.Code, first.Body.String())
	}

	second := doInviteRequest(t, accept, http.MethodPost, "/v1/auth/invites/accept",
		map[string]string{"token": body.Token, "password": "another-long-password"},
		context.Background(), nil)
	if second.Code != http.StatusNotFound {
		t.Fatalf("second accept: status = %d, want 404; body=%s", second.Code, second.Body.String())
	}

	// The password from the first accept must be the one that stuck.
	user, _, _ := users.GetByUsername(context.Background(), "alice")
	if !auth.VerifyPassword(user.PasswordHash, "a-long-enough-password") {
		t.Error("the second attempt changed the password of an already-created account")
	}
}

// TestAcceptInvite_RejectedPasswordLeavesTheInviteUsable covers the pairing
// between validation and consumption: a password the policy refuses must not burn
// the link, or the invitee is left with nothing to retry with.
func TestAcceptInvite_RejectedPasswordLeavesTheInviteUsable(t *testing.T) {
	users := newFakeUserStore()
	invites := newFakeInviteStore(users)
	create := handleInvites(inviteTestConfig(), users, invites, nil)
	accept := handleAcceptInvite(invites, nil)

	created := doInviteRequest(t, create, http.MethodPost, "/v1/invites",
		map[string]string{"username": "alice", "role": userstore.RoleUser},
		ctxWithUserAndTenant("admin-1", "default"), nil)
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	short := doInviteRequest(t, accept, http.MethodPost, "/v1/auth/invites/accept",
		map[string]string{"token": body.Token, "password": strings.Repeat("a", auth.MinPasswordRunes-1)},
		context.Background(), nil)
	if short.Code != http.StatusBadRequest {
		t.Fatalf("short password: status = %d, want 400; body=%s", short.Code, short.Body.String())
	}
	if _, found, _ := users.GetByUsername(context.Background(), "alice"); found {
		t.Fatal("a rejected password must not create an account")
	}

	long := doInviteRequest(t, accept, http.MethodPost, "/v1/auth/invites/accept",
		map[string]string{"token": body.Token, "password": strings.Repeat("b", auth.MaxPasswordBytes+1)},
		context.Background(), nil)
	if long.Code != http.StatusBadRequest {
		t.Fatalf("long password: status = %d, want 400; body=%s", long.Code, long.Body.String())
	}

	// The link must still work afterwards.
	ok := doInviteRequest(t, accept, http.MethodPost, "/v1/auth/invites/accept",
		map[string]string{"token": body.Token, "password": "a-long-enough-password"},
		context.Background(), nil)
	if ok.Code != http.StatusCreated {
		t.Fatalf("the invite was consumed by a rejected attempt: status = %d; body=%s", ok.Code, ok.Body.String())
	}
}

func TestInviteLookup_ReportsPendingAndHidesEverythingElse(t *testing.T) {
	users := newFakeUserStore()
	invites := newFakeInviteStore(users)
	create := handleInvites(inviteTestConfig(), users, invites, nil)
	lookup := handleInviteLookup(invites)
	accept := handleAcceptInvite(invites, nil)

	created := doInviteRequest(t, create, http.MethodPost, "/v1/invites",
		map[string]string{"username": "alice", "role": userstore.RoleUser},
		ctxWithUserAndTenant("admin-1", "default"), nil)
	var body struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	pending := doInviteRequest(t, lookup, http.MethodGet, "/v1/auth/invites/"+body.Token, nil,
		context.Background(), map[string]string{"token": body.Token})
	if pending.Code != http.StatusOK {
		t.Fatalf("pending lookup: status = %d, want 200; body=%s", pending.Code, pending.Body.String())
	}
	if !strings.Contains(pending.Body.String(), `"alice"`) {
		t.Fatalf("the lookup should tell the invitee whose invite it is: %s", pending.Body.String())
	}
	if strings.Contains(pending.Body.String(), body.Token) {
		t.Fatal("the lookup echoed the token back")
	}

	// Opening the link must not consume it: a refresh or a mail scanner following
	// the URL would otherwise burn the invite before the person ever saw it.
	again := doInviteRequest(t, lookup, http.MethodGet, "/v1/auth/invites/"+body.Token, nil,
		context.Background(), map[string]string{"token": body.Token})
	if again.Code != http.StatusOK {
		t.Fatalf("the lookup consumed the invite: status = %d", again.Code)
	}

	if rec := doInviteRequest(t, accept, http.MethodPost, "/v1/auth/invites/accept",
		map[string]string{"token": body.Token, "password": "a-long-enough-password"},
		context.Background(), nil); rec.Code != http.StatusCreated {
		t.Fatalf("accept after two lookups: status = %d", rec.Code)
	}

	consumed := doInviteRequest(t, lookup, http.MethodGet, "/v1/auth/invites/"+body.Token, nil,
		context.Background(), map[string]string{"token": body.Token})
	if consumed.Code != http.StatusNotFound {
		t.Fatalf("consumed lookup: status = %d, want 404", consumed.Code)
	}
}

func TestRevokeInvite_MakesTheLinkUnusable(t *testing.T) {
	users := newFakeUserStore()
	invites := newFakeInviteStore(users)
	create := handleInvites(inviteTestConfig(), users, invites, nil)
	revoke := handleInvite(invites, nil)
	accept := handleAcceptInvite(invites, nil)

	created := doInviteRequest(t, create, http.MethodPost, "/v1/invites",
		map[string]string{"username": "alice", "role": userstore.RoleUser},
		ctxWithUserAndTenant("admin-1", "default"), nil)
	var body struct {
		Token  string `json:"token"`
		Invite struct {
			ID string `json:"id"`
		} `json:"invite"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	revoked := doInviteRequest(t, revoke, http.MethodDelete, "/v1/invites/"+body.Invite.ID, nil,
		ctxWithUserAndTenant("admin-1", "default"), map[string]string{"inviteID": body.Invite.ID})
	if revoked.Code != http.StatusNoContent {
		t.Fatalf("revoke: status = %d, want 204; body=%s", revoked.Code, revoked.Body.String())
	}

	// Revoking again has nothing left to revoke.
	again := doInviteRequest(t, revoke, http.MethodDelete, "/v1/invites/"+body.Invite.ID, nil,
		ctxWithUserAndTenant("admin-1", "default"), map[string]string{"inviteID": body.Invite.ID})
	if again.Code != http.StatusNotFound {
		t.Fatalf("second revoke: status = %d, want 404", again.Code)
	}

	// A leaked link that was revoked must stop working.
	used := doInviteRequest(t, accept, http.MethodPost, "/v1/auth/invites/accept",
		map[string]string{"token": body.Token, "password": "a-long-enough-password"},
		context.Background(), nil)
	if used.Code != http.StatusNotFound {
		t.Fatalf("revoked token accepted: status = %d; body=%s", used.Code, used.Body.String())
	}
}

// TestRevokeInvite_IsTenantScoped pins that the handler passes the caller's
// tenant rather than anything from the request.
//
// It does NOT cover the SQL that enforces the same boundary -- this runs against
// the fake store. The statement-level guard is checked by
// userstore.TestInviteSQLCarriesItsGuards and, for real, in the acceptance run
// (revoking another tenant's invite must 404).
func TestRevokeInvite_IsTenantScoped(t *testing.T) {
	users := newFakeUserStore()
	invites := newFakeInviteStore(users)
	create := handleInvites(inviteTestConfig(), users, invites, nil)
	revoke := handleInvite(invites, nil)

	created := doInviteRequest(t, create, http.MethodPost, "/v1/invites",
		map[string]string{"username": "alice", "role": userstore.RoleUser},
		ctxWithUserAndTenant("admin-1", "default"), nil)
	var body struct {
		Invite struct {
			ID string `json:"id"`
		} `json:"invite"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	rec := doInviteRequest(t, revoke, http.MethodDelete, "/v1/invites/"+body.Invite.ID, nil,
		ctxWithUserAndTenant("admin-1", "other-tenant"), map[string]string{"inviteID": body.Invite.ID})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant revoke: status = %d, want 404", rec.Code)
	}
}

// TestCreateInvite_RejectsAnExistingUsername keeps the failure where it is
// diagnosable: at creation, not at accept time when the invitee is the one who
// sees it.
func TestCreateInvite_RejectsAnExistingUsername(t *testing.T) {
	users := newFakeUserStore()
	seedUser(t, users, "alice", "alice-password", userstore.RoleUser, "default", true)
	invites := newFakeInviteStore(users)
	handler := handleInvites(inviteTestConfig(), users, invites, nil)

	rec := doInviteRequest(t, handler, http.MethodPost, "/v1/invites",
		map[string]string{"username": "alice", "role": userstore.RoleUser},
		ctxWithUserAndTenant("admin-1", "default"), nil)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	if len(invites.order) != 0 {
		t.Fatal("an invite was stored even though the username was taken")
	}
}

func TestCreateInvite_RejectsAnInvalidRole(t *testing.T) {
	users := newFakeUserStore()
	invites := newFakeInviteStore(users)
	handler := handleInvites(inviteTestConfig(), users, invites, nil)

	rec := doInviteRequest(t, handler, http.MethodPost, "/v1/invites",
		map[string]string{"username": "alice", "role": "superuser"},
		ctxWithUserAndTenant("admin-1", "default"), nil)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// TestCreateInvite_SupersedesThePreviousLink documents what re-inviting does: the
// old link stops working and the audit trail keeps the row that was revoked.
func TestCreateInvite_SupersedesThePreviousLink(t *testing.T) {
	users := newFakeUserStore()
	invites := newFakeInviteStore(users)
	create := handleInvites(inviteTestConfig(), users, invites, nil)
	accept := handleAcceptInvite(invites, nil)

	tokens := make([]string, 0, 2)
	for i := 0; i < 2; i++ {
		rec := doInviteRequest(t, create, http.MethodPost, "/v1/invites",
			map[string]string{"username": "alice", "role": userstore.RoleUser},
			ctxWithUserAndTenant("admin-1", "default"), nil)
		if rec.Code != http.StatusCreated {
			t.Fatalf("create #%d: status = %d; body=%s", i+1, rec.Code, rec.Body.String())
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		tokens = append(tokens, body.Token)
	}

	old := doInviteRequest(t, accept, http.MethodPost, "/v1/auth/invites/accept",
		map[string]string{"token": tokens[0], "password": "a-long-enough-password"},
		context.Background(), nil)
	if old.Code != http.StatusNotFound {
		t.Fatalf("the superseded link still works: status = %d; body=%s", old.Code, old.Body.String())
	}

	fresh := doInviteRequest(t, accept, http.MethodPost, "/v1/auth/invites/accept",
		map[string]string{"token": tokens[1], "password": "a-long-enough-password"},
		context.Background(), nil)
	if fresh.Code != http.StatusCreated {
		t.Fatalf("the replacement link does not work: status = %d; body=%s", fresh.Code, fresh.Body.String())
	}
}

package main

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-etl-pipeline/internal/audit"
	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/userstore"
)

// Invitations are the one path by which an account can come into existence
// without an administrator creating it directly. The design keeps the two halves
// far apart on purpose:
//
//   - Creating an invite is an administrator action. It decides the tenant, the
//     username and the role, and it is the only place a token plaintext exists.
//   - Accepting an invite is unauthenticated. The token is the credential, and
//     the only thing the request contributes is a password.
//
// Nothing from the accept request reaches tenant, username or role: those come
// from the stored invite, so a tampered request cannot promote itself.

// inviteView is the administrator-facing shape of an invite.
//
// There is deliberately no token field. The plaintext is returned exactly once,
// by the response that creates the invite, and is unrecoverable afterwards --
// the database holds only its digest. A listing that could show tokens again
// would defeat that.
type inviteView struct {
	ID          string     `json:"id"`
	Username    string     `json:"username"`
	DisplayName string     `json:"display_name,omitempty"`
	Email       string     `json:"email,omitempty"`
	Role        string     `json:"role"`
	State       string     `json:"state"`
	CreatedBy   string     `json:"created_by"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   time.Time  `json:"expires_at"`
	ConsumedAt  *time.Time `json:"consumed_at,omitempty"`
	RevokedAt   *time.Time `json:"revoked_at,omitempty"`
}

func toInviteView(invite userstore.Invite, now time.Time) inviteView {
	return inviteView{
		ID:          invite.ID,
		Username:    invite.Username,
		DisplayName: invite.DisplayName,
		Email:       invite.Email,
		Role:        invite.Role,
		State:       string(invite.State(now)),
		CreatedBy:   invite.CreatedBy,
		CreatedAt:   invite.CreatedAt,
		ExpiresAt:   invite.ExpiresAt,
		ConsumedAt:  invite.ConsumedAt,
		RevokedAt:   invite.RevokedAt,
	}
}

// acceptPathFor is the path the invitee opens, relative to the web app's origin.
//
// Relative rather than absolute because the API does not know which hostname the
// browser reached it through (behind a proxy, through a tunnel, on localhost).
// Composing the link client-side means it is always right, and it avoids
// inventing a PUBLIC_BASE_URL setting that would be wrong in exactly the
// deployments that are hardest to debug.
func acceptPathFor(token string) string {
	return "/accept-invite/" + token
}

type createInviteRequest struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        string `json:"role"`
}

// handleInvites serves /v1/invites for administrators: GET lists the tenant's
// invites, POST creates one.
func handleInvites(cfg config.Config, users userstore.Store, invites userstore.InviteStore, audits audit.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleListInvites(w, r, invites)
		case http.MethodPost:
			handleCreateInvite(w, r, cfg, users, invites, audits)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func handleListInvites(w http.ResponseWriter, r *http.Request, invites userstore.InviteStore) {
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	tenantID := auth.GetTenantID(r.Context())
	items, total, err := invites.ListInvites(r.Context(), tenantID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list invitations")
		return
	}
	now := time.Now()
	views := make([]inviteView, 0, len(items))
	for _, invite := range items {
		views = append(views, toInviteView(invite, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  views,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

func handleCreateInvite(w http.ResponseWriter, r *http.Request, cfg config.Config, users userstore.Store, invites userstore.InviteStore, audits audit.Store) {
	var req createInviteRequest
	if !decodeJSONBody(w, r, &req, defaultJSONBodyBytes, "invalid JSON body") {
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if req.Username == "" {
		writeError(w, http.StatusBadRequest, "username is required")
		return
	}
	if !validRole(req.Role) {
		writeError(w, http.StatusBadRequest, "role must be admin, user, or readonly")
		return
	}
	// Tenant-scoped: an administrator invites into their own tenant only. The
	// tenant comes from the JWT, never from the request body.
	tenantID := auth.GetTenantID(r.Context())
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant is required")
		return
	}
	actor := auth.GetUserID(r.Context())

	// Refuse to invite a username that already exists. Without this the invite
	// would be issued happily and then fail at accept time with a duplicate-username
	// conflict -- a dead link the administrator has no way to diagnose.
	if _, found, err := users.GetByUsername(r.Context(), req.Username); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to check username")
		return
	} else if found {
		writeError(w, http.StatusConflict, "username already exists")
		return
	}

	token, digest, err := auth.NewInviteToken()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate invitation")
		return
	}
	invite := &userstore.Invite{
		TenantID:    tenantID,
		Username:    req.Username,
		DisplayName: strings.TrimSpace(req.DisplayName),
		Email:       strings.TrimSpace(req.Email),
		Role:        req.Role,
		CreatedBy:   actor,
		ExpiresAt:   time.Now().Add(cfg.InviteTTL),
	}
	if err := invites.CreateInvite(r.Context(), invite, digest); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create invitation")
		return
	}

	recordAudit(r.Context(), audits, audit.Entry{
		TenantID:     tenantID,
		ActorUserID:  actor,
		ActorRole:    auth.GetPermission(r.Context()),
		Action:       "user_invite.create",
		ResourceType: "user_invite",
		ResourceID:   invite.ID,
		Detail: map[string]any{
			"username":   invite.Username,
			"role":       invite.Role,
			"expires_at": invite.ExpiresAt.UTC().Format(time.RFC3339),
		},
	})

	// The only response that ever carries the token.
	view := toInviteView(*invite, time.Now())
	writeJSON(w, http.StatusCreated, map[string]any{
		"invite":      view,
		"token":       token,
		"accept_path": acceptPathFor(token),
		"expires_at":  invite.ExpiresAt,
		"ttl_seconds": int(cfg.InviteTTL.Seconds()),
	})
}

// handleInvite serves DELETE /v1/invites/{inviteID}: revoke a pending invite so
// a link that leaked stops working.
func handleInvite(invites userstore.InviteStore, audits audit.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		id := r.PathValue("inviteID")
		if id == "" {
			writeError(w, http.StatusNotFound, "invitation not found")
			return
		}
		tenantID := auth.GetTenantID(r.Context())
		actor := auth.GetUserID(r.Context())
		if err := invites.RevokeInvite(r.Context(), tenantID, id, actor); err != nil {
			if errors.Is(err, userstore.ErrNotFound) {
				// Also the answer for an invite in another tenant, and for one that
				// was already consumed: none of them can be revoked, and saying
				// which is which would leak cross-tenant existence.
				writeError(w, http.StatusNotFound, "invitation not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to revoke invitation")
			return
		}

		recordAudit(r.Context(), audits, audit.Entry{
			TenantID:     tenantID,
			ActorUserID:  actor,
			ActorRole:    auth.GetPermission(r.Context()),
			Action:       "user_invite.revoke",
			ResourceType: "user_invite",
			ResourceID:   id,
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleInviteLookup serves GET /v1/auth/invites/{token}: what the accept page
// needs in order to render before the person types anything.
//
// It never consumes the token, so opening a link twice (a refresh, a mail
// scanner following it) does not burn it. An unusable invite answers with the
// same 404 as an unknown one.
func handleInviteLookup(invites userstore.InviteStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		token := r.PathValue("token")
		if token == "" {
			writeError(w, http.StatusNotFound, "邀请链接无效或已过期")
			return
		}
		invite, found, err := invites.GetInviteByDigest(r.Context(), auth.DigestInviteToken(token))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to look up invitation")
			return
		}
		if !found || invite.State(time.Now()) != userstore.InvitePending {
			writeError(w, http.StatusNotFound, "邀请链接无效或已过期")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"username":           invite.Username,
			"role":               invite.Role,
			"expires_at":         invite.ExpiresAt,
			"min_password_runes": auth.MinPasswordRunes,
			"max_password_bytes": auth.MaxPasswordBytes,
		})
	}
}

type acceptInviteRequest struct {
	Token    string `json:"token"`
	Password string `json:"password"`
}

// handleAcceptInvite serves POST /v1/auth/invites/accept: it turns a token plus
// a password into an account.
//
// There is no rate limit here, and that is a decision rather than an oversight.
// The token carries 256 bits of entropy, so guessing one is not a threat; and the
// only request that reaches bcrypt is one that already presented a live token
// (an unknown token fails the lookup first). So the expensive path is reachable
// only by the person the invite was issued to, at most once -- the invite is
// consumed by the same transaction that hashes the password.
func handleAcceptInvite(invites userstore.InviteStore, audits audit.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req acceptInviteRequest
		if !decodeJSONBody(w, r, &req, defaultJSONBodyBytes, "invalid JSON body") {
			return
		}
		if strings.TrimSpace(req.Token) == "" {
			writeError(w, http.StatusNotFound, "邀请链接无效或已过期")
			return
		}
		// This is the self-service path, so the full policy applies: minimum
		// length included. The administrator user API deliberately does not
		// enforce the minimum (see rejectUnstorablePassword).
		if rejectWeakPassword(w, req.Password) {
			return
		}
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to hash password")
			return
		}

		user, err := invites.ConsumeInvite(r.Context(), auth.DigestInviteToken(req.Token), hash)
		switch {
		case errors.Is(err, userstore.ErrInviteNotUsable):
			// Unknown, expired, already used, or revoked -- one answer for all
			// four, so the endpoint cannot be used to probe which tokens existed.
			writeError(w, http.StatusNotFound, "邀请链接无效或已过期")
			return
		case errors.Is(err, userstore.ErrDuplicate):
			writeError(w, http.StatusConflict, "该用户名已被占用，请联系管理员重新邀请")
			return
		case err != nil:
			writeError(w, http.StatusInternalServerError, "failed to create account")
			return
		}

		recordAudit(r.Context(), audits, audit.Entry{
			TenantID:     user.TenantID,
			Action:       "user_invite.accept",
			ResourceType: "user",
			ResourceID:   user.ID,
			Detail:       map[string]any{"username": user.Username, "role": user.Role},
		})

		// No token is issued here. The person logs in through the ordinary login
		// path, which is what the acceptance criterion describes ("set a password,
		// then log in") and keeps one code path responsible for issuing sessions.
		writeJSON(w, http.StatusCreated, map[string]any{
			"username": user.Username,
			"role":     user.Role,
			"login":    "/login",
		})
	}
}

// rejectWeakPassword renders a full-policy failure as a 400 the user can act on,
// and reports whether it wrote a response.
//
// This is the self-service path, so the minimum length applies. The
// administrator user API uses rejectUnstorablePassword instead, which enforces
// only what the storage format cannot represent -- see the comment there for why
// the two differ.
func rejectWeakPassword(w http.ResponseWriter, password string) bool {
	err := auth.ValidatePassword(password)
	switch {
	case err == nil:
		return false
	case errors.Is(err, auth.ErrPasswordTooShort):
		writeError(w, http.StatusBadRequest, fmt.Sprintf("密码至少需要 %d 个字符", auth.MinPasswordRunes))
	case errors.Is(err, auth.ErrPasswordTooLong):
		writeError(w, http.StatusBadRequest, fmt.Sprintf("密码不能超过 %d 个字节", auth.MaxPasswordBytes))
	default:
		writeError(w, http.StatusBadRequest, "密码不符合要求")
	}
	return true
}

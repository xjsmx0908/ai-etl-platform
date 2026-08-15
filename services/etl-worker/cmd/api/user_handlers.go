package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/userstore"
)

const defaultPageSize = 20

type userView struct {
	ID        string    `json:"id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	TenantID  string    `json:"tenant_id"`
	Active    bool      `json:"active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type createUserRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
	TenantID string `json:"tenant_id"`
	Active   *bool  `json:"active"`
}

type updateUserRequest struct {
	Role     *string `json:"role"`
	TenantID *string `json:"tenant_id"`
	Active   *bool   `json:"active"`
}

type setPasswordRequest struct {
	Password string `json:"password"`
}

type tenantRequest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func toUserView(u userstore.User) userView {
	return userView{
		ID:        u.ID,
		Username:  u.Username,
		Role:      u.Role,
		TenantID:  u.TenantID,
		Active:    u.Active,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}

func validRole(role string) bool {
	switch role {
	case userstore.RoleAdmin, userstore.RoleUser, userstore.RoleReadonly:
		return true
	}
	return false
}

// handleUsers serves /v1/users: GET lists the caller's tenant users (paginated),
// POST creates a user (admin may target any tenant).
func handleUsers(users userstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handleListUsers(w, r, users)
		case http.MethodPost:
			handleCreateUser(w, r, users)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func handleListUsers(w http.ResponseWriter, r *http.Request, users userstore.Store) {
	limit := defaultPageSize
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	tenantID := auth.GetTenantID(r.Context())
	items, total, err := users.List(r.Context(), tenantID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list users")
		return
	}
	views := make([]userView, 0, len(items))
	for _, u := range items {
		views = append(views, toUserView(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items":  views,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

func handleCreateUser(w http.ResponseWriter, r *http.Request, users userstore.Store) {
	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Username == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "username and password are required")
		return
	}
	if !validRole(req.Role) {
		writeError(w, http.StatusBadRequest, "role must be admin, user, or readonly")
		return
	}
	if req.TenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant_id is required")
		return
	}
	active := true
	if req.Active != nil {
		active = *req.Active
	}

	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}
	u := &userstore.User{
		Username:     req.Username,
		PasswordHash: hash,
		Role:         req.Role,
		TenantID:     req.TenantID,
		Active:       active,
	}
	if err := users.Create(r.Context(), u); err != nil {
		if errors.Is(err, userstore.ErrDuplicate) {
			writeError(w, http.StatusConflict, "username already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to create user")
		return
	}
	writeJSON(w, http.StatusCreated, toUserView(*u))
}

// handleUser serves /v1/users/{id}: GET detail, PUT update, DELETE remove, and
// POST {id}/password to rotate the password separately from profile changes.
func handleUser(users userstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/v1/users/")
		if strings.HasSuffix(path, "/password") {
			if r.Method != http.MethodPost {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			handleSetPassword(w, r, users, strings.TrimSuffix(path, "/password"))
			return
		}
		id := strings.Trim(path, "/")
		if id == "" {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		switch r.Method {
		case http.MethodGet:
			handleGetUser(w, r, users, id)
		case http.MethodPut:
			handleUpdateUser(w, r, users, id)
		case http.MethodDelete:
			handleDeleteUser(w, r, users, id)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func handleGetUser(w http.ResponseWriter, r *http.Request, users userstore.Store, id string) {
	u, found, err := users.GetByID(r.Context(), id)
	if err != nil || !found {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}
	writeJSON(w, http.StatusOK, toUserView(u))
}

func handleUpdateUser(w http.ResponseWriter, r *http.Request, users userstore.Store, id string) {
	var req updateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Role != nil && !validRole(*req.Role) {
		writeError(w, http.StatusBadRequest, "role must be admin, user, or readonly")
		return
	}
	u, err := users.Update(r.Context(), id, userstore.UserPatch{
		Role:     req.Role,
		TenantID: req.TenantID,
		Active:   req.Active,
	})
	if err != nil {
		if err == userstore.ErrNotFound {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update user")
		return
	}
	writeJSON(w, http.StatusOK, toUserView(u))
}

func handleDeleteUser(w http.ResponseWriter, r *http.Request, users userstore.Store, id string) {
	if id == auth.GetUserID(r.Context()) {
		writeError(w, http.StatusBadRequest, "cannot delete your own account")
		return
	}
	if err := users.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete user")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func handleSetPassword(w http.ResponseWriter, r *http.Request, users userstore.Store, id string) {
	var req setPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Password == "" {
		writeError(w, http.StatusBadRequest, "password is required")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to hash password")
		return
	}
	if err := users.SetPasswordHash(r.Context(), id, hash); err != nil {
		if err == userstore.ErrNotFound {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to set password")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTenants serves /v1/tenants: GET lists all tenants, POST creates one so
// the admin can provision a tenant before creating users in it.
func handleTenants(users userstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			tenants, err := users.ListTenants(r.Context())
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to list tenants")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"items": tenants})
		case http.MethodPost:
			var req tenantRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
			if req.ID == "" {
				writeError(w, http.StatusBadRequest, "tenant id is required")
				return
			}
			if err := users.CreateTenant(r.Context(), req.ID, req.Name); err != nil {
				if errors.Is(err, userstore.ErrDuplicate) {
					writeError(w, http.StatusConflict, "tenant already exists")
					return
				}
				writeError(w, http.StatusInternalServerError, "failed to create tenant")
				return
			}
			w.WriteHeader(http.StatusCreated)
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

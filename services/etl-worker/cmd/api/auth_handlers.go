package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/db"
	"ai-etl-pipeline/internal/migrations"
	"ai-etl-pipeline/internal/userstore"
)

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type loginUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	TenantID string `json:"tenant_id"`
	Active   bool   `json:"active"`
}

type loginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	User      loginUser `json:"user"`
}

// openPostgres connects to PostgreSQL and applies pending migrations. The query
// API treats a missing/unhealthy registry as fatal (it owns the registry); the
// worker treats it as optional and warns instead.
func openPostgres(ctx context.Context, cfg config.Config) (*db.Pool, error) {
	return migrations.Open(ctx, cfg.PGDSN)
}

// handleLogin authenticates username + bcrypt password and returns a JWT. It is
// registered on the outer mux (before the JWT middleware chain) so it can be
// reached without a token. Unknown usernames pay a dummy bcrypt compare to blunt
// enumeration.
func handleLogin(cfg config.Config, users userstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req loginRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
		if req.Username == "" || req.Password == "" {
			writeError(w, http.StatusBadRequest, "username and password are required")
			return
		}

		user, found, err := users.GetByUsername(r.Context(), req.Username)
		if err != nil {
			slog.Error("login lookup failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !found {
			auth.VerifyPassword("", req.Password) // constant-time dummy compare
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		if !auth.VerifyPassword(user.PasswordHash, req.Password) {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		if !user.Active {
			writeError(w, http.StatusForbidden, "user is inactive")
			return
		}

		token, expiresAt, err := auth.IssueToken(cfg.JWTSecret, user.ID, user.Username, user.Role, user.TenantID)
		if err != nil {
			slog.Error("token issuance failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		writeJSON(w, http.StatusOK, loginResponse{
			Token:     token,
			ExpiresAt: expiresAt.UTC(),
			User: loginUser{
				ID:       user.ID,
				Username: user.Username,
				Role:     user.Role,
				TenantID: user.TenantID,
				Active:   user.Active,
			},
		})
	}
}

// bootstrapAdmin provisions the initial admin and its tenant on an empty
// database. It is idempotent: once any user exists it is a no-op, and a tenant
// that already exists (e.g. a partially failed first run) is tolerated.
func bootstrapAdmin(ctx context.Context, cfg config.Config, users userstore.Store) error {
	count, err := users.CountUsers(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	password := cfg.BootstrapAdminPassword
	if password == "" {
		if cfg.IsDev() {
			slog.Warn("BOOTSTRAP_ADMIN_PASSWORD unset; using default 'admin' (development only)")
			password = "admin"
		} else {
			return errors.New("BOOTSTRAP_ADMIN_PASSWORD is required to bootstrap the initial admin")
		}
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}

	if err := users.CreateTenant(ctx, cfg.BootstrapAdminTenant, cfg.BootstrapAdminTenant); err != nil {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" { // tolerate existing tenant
			return err
		}
	}

	admin := &userstore.User{
		Username:     cfg.BootstrapAdminUsername,
		PasswordHash: hash,
		Role:         userstore.RoleAdmin,
		TenantID:     cfg.BootstrapAdminTenant,
		Active:       true,
	}
	if err := users.Create(ctx, admin); err != nil {
		return err
	}
	slog.Info("bootstrapped initial admin",
		"username", cfg.BootstrapAdminUsername, "tenant", cfg.BootstrapAdminTenant)
	return nil
}

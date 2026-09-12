package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"ai-etl-pipeline/internal/audit"
	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/config"
	"ai-etl-pipeline/internal/db"
	"ai-etl-pipeline/internal/middleware"
	"ai-etl-pipeline/internal/migrations"
	"ai-etl-pipeline/internal/session"
	"ai-etl-pipeline/internal/userstore"
	"github.com/google/uuid"
)

func passwordLoginEnabled(cfg config.Config) bool {
	return !(cfg.OIDCEnabled && strings.EqualFold(strings.TrimSpace(cfg.Environment), "production"))
}

func demoLoginEnabled(cfg config.Config) bool {
	return cfg.DemoLoginEnabled && !strings.EqualFold(strings.TrimSpace(cfg.Environment), "production")
}

func handleAuthMethods(cfg config.Config) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{
			"password_enabled":   passwordLoginEnabled(cfg),
			"oidc_enabled":       cfg.OIDCEnabled,
			"demo_login_enabled": demoLoginEnabled(cfg),
		})
	})
}

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

func handleCurrentSession(users userstore.Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		principal := auth.GetPrincipal(r.Context())
		if principal.SubjectID == "" || users == nil {
			writeError(w, http.StatusUnauthorized, "session unavailable")
			return
		}
		user, found, err := users.GetByID(r.Context(), principal.SubjectID)
		if err != nil || !found || !user.Active || user.TenantID != principal.TenantID || user.Role != principal.Role {
			writeError(w, http.StatusUnauthorized, "session unavailable")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"user": loginUser{
			ID: user.ID, Username: user.Username, Role: user.Role,
			TenantID: user.TenantID, Active: user.Active,
		}})
	})
}

func newPlatformAuthenticator(cfg config.Config, users userstore.Store) auth.Authenticator {
	policy := auth.IdentityPolicyForEnvironment(cfg.Environment)
	if cfg.OIDCEnabled {
		if strings.EqualFold(strings.TrimSpace(cfg.Environment), "production") {
			policy = auth.FederatedProductionIdentityPolicy()
		} else {
			policy = auth.FederatedMigrationIdentityPolicy()
		}
	}
	return auth.NewVerifierWithPolicy(
		cfg.JWTSecret,
		users,
		policy,
	)
}

// openPostgres connects to PostgreSQL and applies pending migrations. The query
// API treats a missing/unhealthy registry as fatal (it owns the registry); the
// worker treats it as optional and warns instead.
func openPostgres(ctx context.Context, cfg config.Config) (*db.Pool, error) {
	return migrations.Open(ctx, cfg.PGDSN)
}

// handleLogin authenticates username + bcrypt password and returns the configured
// platform credential. It is registered on the outer mux (before authentication
// middleware) so it can be reached without a credential. Unknown usernames pay a
// dummy bcrypt compare to blunt enumeration. Every attempt is written to the audit log.
func handleLogin(cfg config.Config, users userstore.Store, audits audit.Store, sessions *session.Manager) http.HandlerFunc {
	return handleLoginWithGuard(cfg, users, audits, sessions, nil)
}

func handleLoginWithGuard(cfg config.Config, users userstore.Store, audits audit.Store, sessions *session.Manager, guard middleware.LoginGuard) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !passwordLoginEnabled(cfg) {
			writeError(w, http.StatusNotFound, "password login is unavailable")
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req loginRequest
		if !decodeJSONBody(w, r, &req, jsonBodyLimit(cfg), "invalid JSON body") {
			return
		}
		if req.Username == "" || req.Password == "" {
			writeError(w, http.StatusBadRequest, "username and password are required")
			return
		}
		ip := middleware.RequestIP(r)
		if guard != nil {
			if ok, retryAfter := guard.Allow(ip, req.Username); !ok {
				w.Header().Set("Retry-After", middleware.RetryAfterSeconds(retryAfter))
				writeError(w, http.StatusTooManyRequests, "invalid credentials")
				return
			}
		}

		user, found, err := users.GetByUsername(r.Context(), req.Username)
		if err != nil {
			slog.Error("login lookup failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !found {
			auth.VerifyPassword("", req.Password) // constant-time dummy compare
			if guard != nil {
				guard.RecordFailure(ip, req.Username)
			}
			recordAudit(r.Context(), audits, audit.Entry{
				Action: "login", Result: audit.ResultFailure,
				Detail: map[string]any{"username": req.Username, "reason": "invalid_credentials"},
			})
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		if user.Origin != "" && user.Origin != userstore.OriginLocal {
			auth.VerifyPassword("", req.Password)
			if guard != nil {
				guard.RecordFailure(ip, req.Username)
			}
			recordAudit(r.Context(), audits, audit.Entry{
				TenantID: user.TenantID, ActorUserID: user.ID, ActorRole: user.Role,
				Action: "login", Result: audit.ResultFailure,
				Detail: map[string]any{"username": user.Username, "reason": "password_login_not_allowed"},
			})
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		if !auth.VerifyPassword(user.PasswordHash, req.Password) {
			if guard != nil {
				guard.RecordFailure(ip, req.Username)
			}
			recordAudit(r.Context(), audits, audit.Entry{
				TenantID: user.TenantID, Action: "login", Result: audit.ResultFailure,
				Detail: map[string]any{"username": user.Username, "reason": "invalid_credentials"},
			})
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		if !user.Active {
			recordAudit(r.Context(), audits, audit.Entry{
				TenantID: user.TenantID, ActorUserID: user.ID, ActorRole: user.Role,
				Action: "login", Result: audit.ResultFailure,
				Detail: map[string]any{"username": user.Username, "reason": "inactive"},
			})
			writeError(w, http.StatusForbidden, "user is inactive")
			return
		}

		token, expiresAt, issueErr := issuePlatformLoginToken(r.Context(), cfg, sessions, user, "password-login:")
		if issueErr != nil {
			if sessions != nil {
				slog.Error("platform session issuance failed", "error", issueErr)
				recordAudit(r.Context(), audits, audit.Entry{
					TenantID: user.TenantID, ActorUserID: user.ID, ActorRole: user.Role,
					Action: "login", Result: audit.ResultFailure,
					Detail: map[string]any{"username": user.Username, "reason": "session_unavailable"},
				})
				writeError(w, http.StatusServiceUnavailable, "login unavailable")
				return
			}
			slog.Error("token issuance failed", "error", issueErr)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if guard != nil {
			guard.RecordSuccess(ip, req.Username)
		}
		recordAudit(r.Context(), audits, audit.Entry{
			TenantID: user.TenantID, ActorUserID: user.ID, ActorRole: user.Role,
			Action: "login", Result: audit.ResultSuccess,
			Detail: map[string]any{"username": user.Username},
		})
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

type demoLoginRequest struct {
	Account string `json:"account"`
}

func handleDemoLogin(cfg config.Config, users userstore.Store, audits audit.Store, sessions *session.Manager) http.HandlerFunc {
	return handleDemoLoginWithGuard(cfg, users, audits, sessions, nil)
}

func handleDemoLoginWithGuard(cfg config.Config, users userstore.Store, audits audit.Store, sessions *session.Manager, guard middleware.LoginGuard) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !demoLoginEnabled(cfg) {
			writeError(w, http.StatusNotFound, "demo login is unavailable")
			return
		}
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req demoLoginRequest
		if !decodeJSONBody(w, r, &req, jsonBodyLimit(cfg), "invalid JSON body") {
			return
		}
		account := strings.ToLower(strings.TrimSpace(req.Account))
		var username string
		var expectedRole string
		switch account {
		case "user":
			username = cfg.DemoUserUsername
			expectedRole = userstore.RoleUser
		case "admin":
			username = cfg.DemoAdminUsername
			expectedRole = userstore.RoleAdmin
		default:
			writeError(w, http.StatusBadRequest, "account must be user or admin")
			return
		}
		ip := middleware.RequestIP(r)
		if guard != nil {
			if ok, retryAfter := guard.Allow(ip, username); !ok {
				w.Header().Set("Retry-After", middleware.RetryAfterSeconds(retryAfter))
				writeError(w, http.StatusTooManyRequests, "invalid credentials")
				return
			}
		}
		user, found, err := users.GetByUsername(r.Context(), username)
		if err != nil {
			slog.Error("demo login lookup failed", "error", err)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if !found || user.TenantID != cfg.DemoTenantID || user.Role != expectedRole {
			if guard != nil {
				guard.RecordFailure(ip, username)
			}
			recordAudit(r.Context(), audits, audit.Entry{
				Action: "login", Result: audit.ResultFailure,
				Detail: map[string]any{"username": username, "reason": "demo_account_unavailable", "account": account},
			})
			writeError(w, http.StatusServiceUnavailable, "demo login is unavailable")
			return
		}
		if !user.Active {
			recordAudit(r.Context(), audits, audit.Entry{
				TenantID: user.TenantID, ActorUserID: user.ID, ActorRole: user.Role,
				Action: "login", Result: audit.ResultFailure,
				Detail: map[string]any{"username": user.Username, "reason": "inactive", "account": account},
			})
			writeError(w, http.StatusForbidden, "user is inactive")
			return
		}
		token, expiresAt, issueErr := issuePlatformLoginToken(r.Context(), cfg, sessions, user, "demo-login:")
		if issueErr != nil {
			if sessions != nil {
				slog.Error("platform session issuance failed", "error", issueErr)
				recordAudit(r.Context(), audits, audit.Entry{
					TenantID: user.TenantID, ActorUserID: user.ID, ActorRole: user.Role,
					Action: "login", Result: audit.ResultFailure,
					Detail: map[string]any{"username": user.Username, "reason": "session_unavailable", "account": account},
				})
				writeError(w, http.StatusServiceUnavailable, "login unavailable")
				return
			}
			slog.Error("token issuance failed", "error", issueErr)
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		if guard != nil {
			guard.RecordSuccess(ip, username)
		}
		recordAudit(r.Context(), audits, audit.Entry{
			TenantID: user.TenantID, ActorUserID: user.ID, ActorRole: user.Role,
			Action: "login", Result: audit.ResultSuccess,
			Detail: map[string]any{"username": user.Username, "reason": "demo_login", "account": account},
		})
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

func issuePlatformLoginToken(ctx context.Context, cfg config.Config, sessions *session.Manager, user userstore.User, correlationPrefix string) (string, time.Time, error) {
	if sessions != nil {
		credential, err := sessions.Establish(ctx, session.EstablishCommand{
			Principal: auth.Principal{
				TenantID: user.TenantID, SubjectID: user.ID,
				AuthenticationMethod: auth.AuthenticationMethodLocal,
			},
			Evidence: session.AuthenticationEvidence{
				Assurance: session.AssuranceLocalPassword, AuthenticatedAt: time.Now().UTC(),
			},
			CorrelationID: correlationPrefix + uuid.NewString(),
		})
		if err != nil {
			return "", time.Time{}, err
		}
		return platformSessionCredentialPrefix + credential.Token, credential.ExpiresAt, nil
	}
	return auth.IssueToken(cfg.JWTSecret, user.ID, user.Username, user.Role, user.TenantID, user.TokenVersion)
}

func ensureDemoAccounts(ctx context.Context, cfg config.Config, users userstore.Store) error {
	if !demoLoginEnabled(cfg) {
		return nil
	}
	if err := users.CreateTenant(ctx, cfg.DemoTenantID, "演示租户"); err != nil && !errors.Is(err, userstore.ErrDuplicate) {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" {
			return err
		}
	}
	if err := ensureDemoUser(ctx, users, cfg.DemoUserUsername, userstore.RoleUser, cfg.DemoTenantID, "演示用户"); err != nil {
		return err
	}
	return ensureDemoUser(ctx, users, cfg.DemoAdminUsername, userstore.RoleAdmin, cfg.DemoTenantID, "演示管理员")
}

func ensureDemoUser(ctx context.Context, users userstore.Store, username, role, tenantID, displayName string) error {
	existing, found, err := users.GetByUsername(ctx, username)
	if err != nil {
		return err
	}
	if found {
		if existing.TenantID != tenantID || existing.Role != role {
			return fmt.Errorf("demo user %s exists with tenant=%s role=%s", username, existing.TenantID, existing.Role)
		}
		if existing.Active {
			return nil
		}
		active := true
		_, err := users.Update(ctx, existing.ID, userstore.UserPatch{Active: &active})
		return err
	}
	password, err := randomDemoPassword()
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	user := &userstore.User{
		Username:     username,
		PasswordHash: hash,
		Role:         role,
		TenantID:     tenantID,
		Active:       true,
		DisplayName:  displayName,
	}
	if err := users.Create(ctx, user); err != nil {
		if errors.Is(err, userstore.ErrDuplicate) {
			existing, found, lookupErr := users.GetByUsername(ctx, username)
			if lookupErr != nil {
				return lookupErr
			}
			if found && existing.TenantID == tenantID && existing.Role == role {
				return nil
			}
		}
		return err
	}
	slog.Info("provisioned demo account", "username", username, "role", role, "tenant", tenantID)
	return nil
}

func randomDemoPassword() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

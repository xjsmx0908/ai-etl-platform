// Package auth provides JWT authentication and tenant isolation middleware.
package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"ai-etl-pipeline/internal/userstore"
)

// Claims holds JWT token claims for the ETL pipeline.
type Claims struct {
	TenantID             string               `json:"tenant_id"`
	UserID               string               `json:"user_id"`
	Permission           string               `json:"permission"` // "admin" | "user" | "readonly"
	Scopes               []string             `json:"scopes"`     // ["upload", "query", "admin"]
	AuthenticationMethod AuthenticationMethod `json:"auth_method,omitempty"`
	// TokenVersion is the user's users.token_version at issuance. The middleware
	// re-validates it against the DB so a password reset revokes old tokens.
	TokenVersion int `json:"token_version,omitempty"`
	jwt.RegisteredClaims
}

// ContextKey is the type for context values.
type ContextKey string

const (
	// CtxTenantID is the context key for tenant ID.
	CtxTenantID ContextKey = "tenant_id"
	// CtxUserID is the context key for user ID.
	CtxUserID ContextKey = "user_id"
	// CtxPermission is the context key for permission level.
	CtxPermission ContextKey = "permission"
	// CtxScopes is the context key for OAuth-style scopes.
	CtxScopes ContextKey = "scopes"
	// CtxPrincipal is the policy-owned authenticated identity.
	CtxPrincipal ContextKey = "principal"
)

// Verifier validates JWT tokens. When a user store is wired in, the middleware
// also re-validates the token's user_version against the DB so password resets
// revoke outstanding tokens.
type Verifier struct {
	secret []byte
	users  userstore.Store
	policy IdentityPolicy
}

// NewVerifier creates a JWT verifier with the given secret key. Token-version
// revocation is disabled (nil user store).
func NewVerifier(secret string) *Verifier {
	return &Verifier{secret: []byte(secret), policy: NonProductionIdentityPolicy()}
}

// NewVerifierWithStore creates a JWT verifier that additionally re-validates
// each token's token_version claim against the user store.
func NewVerifierWithStore(secret string, users userstore.Store) *Verifier {
	return NewVerifierWithPolicy(secret, users, NonProductionIdentityPolicy())
}

// NewVerifierWithPolicy creates a platform-token adapter under an explicit
// environment identity policy.
func NewVerifierWithPolicy(secret string, users userstore.Store, policy IdentityPolicy) *Verifier {
	return &Verifier{secret: []byte(secret), users: users, policy: policy}
}

// Verify extracts and validates JWT from Authorization header.
func (v *Verifier) Verify(r *http.Request) (*Claims, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return nil, fmt.Errorf("missing authorization header")
	}

	tokenStr := strings.TrimPrefix(auth, "Bearer ")
	if tokenStr == auth {
		return nil, fmt.Errorf("invalid authorization format, expected 'Bearer <token>'")
	}

	token, err := jwt.ParseWithClaims(tokenStr, &Claims{}, func(t *jwt.Token) (interface{}, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return v.secret, nil
	})
	if err != nil {
		return nil, fmt.Errorf("invalid token: %w", err)
	}

	claims, ok := token.Claims.(*Claims)
	if !ok || !token.Valid {
		return nil, fmt.Errorf("invalid token claims")
	}

	return claims, nil
}

// Middleware returns an HTTP middleware that enforces JWT authentication.
func (v *Verifier) Middleware(requiredScopes ...string) func(http.Handler) http.Handler {
	return Middleware(v, requiredScopes...)
}

// Middleware enforces authentication and capabilities for any identity
// adapter implementing the provider-neutral Authenticator interface.
func Middleware(authenticator Authenticator, requiredScopes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if authenticator == nil {
				http.Error(w, `{"error":"unauthorized","message":"identity provider unavailable"}`, http.StatusUnauthorized)
				return
			}
			principal, err := authenticator.Authenticate(r)
			if err != nil {
				if errors.Is(err, ErrReauthenticationRequired) {
					action := ""
					var required ReauthenticationRequiredError
					if errors.As(err, &required) {
						action = required.Action
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					_, _ = fmt.Fprintf(w, `{"error":"reauthentication_required","message":"fresh authentication is required","action":%q}`, action)
					return
				}
				http.Error(w, `{"error":"unauthorized","message":"`+err.Error()+`"}`, http.StatusUnauthorized)
				return
			}

			// Check required scopes
			for _, req := range requiredScopes {
				if !hasScope(principal.Capabilities, req) {
					http.Error(w, `{"error":"forbidden","message":"missing scope: `+req+`"}`, http.StatusForbidden)
					return
				}
			}

			// Inject tenant and user into context
			ctx := context.WithValue(r.Context(), CtxTenantID, principal.TenantID)
			ctx = context.WithValue(ctx, CtxUserID, principal.SubjectID)
			ctx = context.WithValue(ctx, CtxPermission, principal.Role)
			ctx = context.WithValue(ctx, CtxScopes, append([]string(nil), principal.Capabilities...))
			ctx = context.WithValue(ctx, CtxPrincipal, principal)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func hasScope(scopes []string, required string) bool {
	for _, s := range scopes {
		if s == required {
			return true
		}
	}
	return false
}

// GetTenantID extracts tenant ID from context.
func GetTenantID(ctx context.Context) string {
	if v, ok := ctx.Value(CtxTenantID).(string); ok {
		return v
	}
	return ""
}

// GetUserID extracts user ID from context.
func GetUserID(ctx context.Context) string {
	if v, ok := ctx.Value(CtxUserID).(string); ok {
		return v
	}
	return ""
}

// GetPermission extracts user permission level from context.
func GetPermission(ctx context.Context) string {
	if v, ok := ctx.Value(CtxPermission).(string); ok {
		return v
	}
	return ""
}

// GetScopes extracts authenticated scopes from context.
func GetScopes(ctx context.Context) []string {
	if v, ok := ctx.Value(CtxScopes).([]string); ok {
		return append([]string(nil), v...)
	}
	return nil
}

// GetPrincipal extracts a copy of the policy-owned identity from context.
func GetPrincipal(ctx context.Context) Principal {
	principal, ok := ctx.Value(CtxPrincipal).(Principal)
	if !ok {
		return Principal{}
	}
	principal.Capabilities = append([]string(nil), principal.Capabilities...)
	return principal
}

// GenerateTestTokenWithPermission creates a JWT token for testing purposes.
func GenerateTestTokenWithPermission(secret, tenantID, userID, permission string, scopes []string) (string, error) {
	if strings.TrimSpace(permission) == "" {
		permission = "user"
	}
	claims := Claims{
		TenantID:             tenantID,
		UserID:               userID,
		Permission:           permission,
		Scopes:               scopes,
		AuthenticationMethod: AuthenticationMethodTest,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

// GenerateTestToken creates a JWT token for testing purposes.
func GenerateTestToken(secret, tenantID, userID string, scopes []string) (string, error) {
	return GenerateTestTokenWithPermission(secret, tenantID, userID, "user", scopes)
}

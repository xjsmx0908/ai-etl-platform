// Package auth provides JWT authentication and tenant isolation middleware.
package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims holds JWT token claims for the ETL pipeline.
type Claims struct {
	TenantID   string   `json:"tenant_id"`
	UserID     string   `json:"user_id"`
	Permission string   `json:"permission"` // "admin" | "user" | "readonly"
	Scopes     []string `json:"scopes"`     // ["upload", "query", "admin"]
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
)

// Verifier validates JWT tokens.
type Verifier struct {
	secret []byte
}

// NewVerifier creates a JWT verifier with the given secret key.
func NewVerifier(secret string) *Verifier {
	return &Verifier{secret: []byte(secret)}
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
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
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
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, err := v.Verify(r)
			if err != nil {
				http.Error(w, `{"error":"unauthorized","message":"`+err.Error()+`"}`, http.StatusUnauthorized)
				return
			}

			// Check required scopes
			for _, req := range requiredScopes {
				if !hasScope(claims.Scopes, req) {
					http.Error(w, `{"error":"forbidden","message":"missing scope: `+req+`"}`, http.StatusForbidden)
					return
				}
			}

			// Inject tenant and user into context
			ctx := context.WithValue(r.Context(), CtxTenantID, claims.TenantID)
			ctx = context.WithValue(ctx, CtxUserID, claims.UserID)
			ctx = context.WithValue(ctx, CtxPermission, claims.Permission)

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

// GenerateTestToken creates a JWT token for testing purposes.
func GenerateTestToken(secret, tenantID, userID string, scopes []string) (string, error) {
	claims := Claims{
		TenantID:   tenantID,
		UserID:     userID,
		Permission: "user",
		Scopes:     scopes,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(24 * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}

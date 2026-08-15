package auth

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// tokenLifetime is how long a login-issued token stays valid. Tokens are
// stateless (Phase 2 adds token_version for revocation).
const tokenLifetime = 24 * time.Hour

// Scope constants used by route gating.
const (
	ScopeQuery  = "query"
	ScopeUpload = "upload"
	ScopeAgent  = "agent"
	ScopeAdmin  = "admin"
)

// ScopesForRole maps a user role to the OAuth-style scopes it grants. It is the
// single source of truth for what a role may call; the existing route gate
// (requireScopes) enforces exact matches against these.
func ScopesForRole(role string) []string {
	switch role {
	case "admin":
		return []string{ScopeQuery, ScopeUpload, ScopeAgent, ScopeAdmin}
	case "user":
		return []string{ScopeQuery, ScopeUpload, ScopeAgent}
	default: // readonly
		return []string{ScopeQuery}
	}
}

// IssueToken signs a JWT for a verified user using the existing Claims shape and
// HS256, so the token is byte-compatible with everything already consuming it.
// It returns the token and its expiry.
func IssueToken(secret, userID, username, role, tenantID string) (string, time.Time, error) {
	expiresAt := time.Now().Add(tokenLifetime)
	claims := Claims{
		TenantID:   tenantID,
		UserID:     userID,
		Permission: role,
		Scopes:     ScopesForRole(role),
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   username,
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		return "", time.Time{}, err
	}
	return signed, expiresAt, nil
}

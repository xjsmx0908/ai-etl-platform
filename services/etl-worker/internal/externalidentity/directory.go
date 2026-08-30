// Package externalidentity owns durable mappings from provider identifiers to
// internal users. It deliberately does not validate provider credentials.
package externalidentity

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"ai-etl-pipeline/internal/auth"
	"ai-etl-pipeline/internal/db"
	"ai-etl-pipeline/internal/userstore"
	"github.com/jackc/pgx/v5"
)

var (
	ErrInvalidIdentity  = errors.New("external identity: invalid identifier")
	ErrNotFound         = errors.New("external identity: binding not found")
	ErrInactive         = errors.New("external identity: internal user inactive")
	ErrInvalidAuthority = errors.New("external identity: invalid internal authority")
	ErrUnavailable      = errors.New("external identity: directory unavailable")
)

// ExternalIdentity is the stable identifier asserted by a future provider
// adapter. Subject comparison is exact and case-sensitive.
type ExternalIdentity struct {
	Issuer  string
	Subject string
}

// Directory is the provider-neutral identity-mapping seam. A future OIDC
// adapter supplies an already validated issuer/subject pair and receives only
// current, internally authoritative policy data.
type Directory interface {
	Resolve(context.Context, ExternalIdentity) (auth.Principal, error)
}

type PostgresDirectory struct{ q db.Querier }

func NewPostgresDirectory(q db.Querier) *PostgresDirectory { return &PostgresDirectory{q: q} }

func (d *PostgresDirectory) Resolve(ctx context.Context, identity ExternalIdentity) (auth.Principal, error) {
	identity, err := Normalize(identity)
	if err != nil {
		return auth.Principal{}, err
	}
	if d == nil || d.q == nil {
		return auth.Principal{}, ErrUnavailable
	}
	var userID, tenantID, role string
	var active bool
	err = d.q.QueryRow(ctx, `SELECT u.id,u.tenant_id,u.role,u.active
		FROM external_identity_bindings b
		JOIN users u ON u.id=b.internal_user_id AND u.tenant_id=b.tenant_id
		WHERE b.issuer=$1 AND b.external_subject=$2`, identity.Issuer, identity.Subject).
		Scan(&userID, &tenantID, &role, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Principal{}, ErrNotFound
	}
	if err != nil {
		return auth.Principal{}, fmt.Errorf("%w: resolve binding", ErrUnavailable)
	}
	if !active {
		return auth.Principal{}, ErrInactive
	}
	if userID == "" || tenantID == "" || !validRole(role) {
		return auth.Principal{}, ErrInvalidAuthority
	}
	return auth.Principal{
		TenantID: tenantID, SubjectID: userID, Role: role,
		AuthenticationMethod: auth.AuthenticationMethodFederated,
		Capabilities:         auth.ScopesForRole(role),
	}, nil
}

func validRole(role string) bool {
	return role == userstore.RoleAdmin || role == userstore.RoleUser || role == userstore.RoleReadonly
}

// Normalize canonicalizes the case-insensitive URL parts of an issuer while
// preserving its path and the subject's case. Padded or control-bearing
// subjects are rejected rather than silently changing provider identity.
func Normalize(identity ExternalIdentity) (ExternalIdentity, error) {
	issuer := strings.TrimSpace(identity.Issuer)
	parsed, err := url.Parse(issuer)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil ||
		!strings.EqualFold(parsed.Scheme, "https") ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return ExternalIdentity{}, fmt.Errorf("%w: issuer must be an absolute HTTPS URL without userinfo, query, or fragment", ErrInvalidIdentity)
	}
	if !utf8.ValidString(identity.Subject) || identity.Subject == "" ||
		strings.TrimSpace(identity.Subject) != identity.Subject ||
		strings.IndexFunc(identity.Subject, unicode.IsControl) >= 0 {
		return ExternalIdentity{}, fmt.Errorf("%w: subject is empty, padded, or contains control characters", ErrInvalidIdentity)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return ExternalIdentity{Issuer: parsed.String(), Subject: identity.Subject}, nil
}

var _ Directory = (*PostgresDirectory)(nil)

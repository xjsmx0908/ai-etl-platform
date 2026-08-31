package auth

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ErrReauthenticationRequired tells HTTP callers that the credential is valid
// but its authentication evidence is too old for the requested operation.
var ErrReauthenticationRequired = errors.New("reauthentication required")

type ReauthenticationRequiredError struct {
	Action string
}

func (e ReauthenticationRequiredError) Error() string {
	return ErrReauthenticationRequired.Error()
}

func (e ReauthenticationRequiredError) Unwrap() error {
	return ErrReauthenticationRequired
}

// AuthenticationMethod identifies how an internal principal authenticated.
// Authorization never derives tenant, role, or capabilities from this value.
type AuthenticationMethod string

const (
	AuthenticationMethodLegacy    AuthenticationMethod = "legacy"
	AuthenticationMethodLocal     AuthenticationMethod = "local"
	AuthenticationMethodTest      AuthenticationMethod = "test"
	AuthenticationMethodFederated AuthenticationMethod = "federated"
	AuthenticationMethodService   AuthenticationMethod = "service"
)

// IdentityPolicy controls which platform-token identities may cross the
// authentication seam. Provider-specific validation belongs in future
// adapters, not in this policy.
type IdentityPolicy struct {
	RequireKnownSubject bool
	AllowedMethods      map[AuthenticationMethod]bool
}

func NonProductionIdentityPolicy() IdentityPolicy {
	return IdentityPolicy{AllowedMethods: map[AuthenticationMethod]bool{
		AuthenticationMethodLegacy: true,
		AuthenticationMethodLocal:  true,
		AuthenticationMethodTest:   true,
	}}
}

func ProductionIdentityPolicy() IdentityPolicy {
	return IdentityPolicy{RequireKnownSubject: true, AllowedMethods: map[AuthenticationMethod]bool{
		AuthenticationMethodLocal: true,
	}}
}

// FederatedProductionIdentityPolicy rejects ordinary local and test sessions
// once an enterprise OIDC provider is explicitly enabled.
func FederatedProductionIdentityPolicy() IdentityPolicy {
	return IdentityPolicy{RequireKnownSubject: true, AllowedMethods: map[AuthenticationMethod]bool{
		AuthenticationMethodFederated: true,
	}}
}

// FederatedMigrationIdentityPolicy adds federated sessions to the explicit
// non-production migration profile without removing local/evaluator access.
func FederatedMigrationIdentityPolicy() IdentityPolicy {
	policy := NonProductionIdentityPolicy()
	policy.AllowedMethods[AuthenticationMethodFederated] = true
	return policy
}

// IdentityPolicyForEnvironment keeps explicit evaluator compatibility outside
// production and makes production platform-token authentication fail closed.
func IdentityPolicyForEnvironment(environment string) IdentityPolicy {
	if strings.EqualFold(strings.TrimSpace(environment), "production") {
		return ProductionIdentityPolicy()
	}
	return NonProductionIdentityPolicy()
}

// Principal is the policy-owned identity consumed by authorization callers.
// Tenant, role, and capabilities are internal authority, not provider claims.
type Principal struct {
	TenantID             string
	SubjectID            string
	Role                 string
	AuthenticationMethod AuthenticationMethod
	Capabilities         []string
}

// Authenticator is the provider-neutral identity seam used by HTTP middleware.
type Authenticator interface {
	Authenticate(*http.Request) (Principal, error)
}

// Authenticate validates the platform token and resolves its internal
// authority. Known users always derive mutable authorization from the store.
func (v *Verifier) Authenticate(r *http.Request) (Principal, error) {
	claims, err := v.Verify(r)
	if err != nil {
		return Principal{}, err
	}
	method := claims.AuthenticationMethod
	if method == "" {
		method = AuthenticationMethodLegacy
	}
	if !v.policy.AllowedMethods[method] {
		return Principal{}, fmt.Errorf("authentication method is not allowed")
	}
	principal := Principal{
		TenantID:             claims.TenantID,
		SubjectID:            claims.UserID,
		Role:                 claims.Permission,
		AuthenticationMethod: method,
		Capabilities:         append([]string(nil), claims.Scopes...),
	}
	if v.users == nil {
		if v.policy.RequireKnownSubject {
			return Principal{}, fmt.Errorf("internal subject is required")
		}
		return principal, nil
	}
	user, found, err := v.users.GetByID(r.Context(), claims.UserID)
	if err != nil {
		return Principal{}, fmt.Errorf("user lookup failed")
	}
	if !found {
		if v.policy.RequireKnownSubject {
			return Principal{}, fmt.Errorf("unknown internal subject")
		}
		return principal, nil
	}
	if !user.Active || user.TokenVersion != claims.TokenVersion {
		return Principal{}, fmt.Errorf("token revoked")
	}
	principal.TenantID = user.TenantID
	principal.SubjectID = user.ID
	principal.Role = user.Role
	principal.Capabilities = ScopesForRole(user.Role)
	return principal, nil
}

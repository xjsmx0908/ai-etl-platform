package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestScopesForRole(t *testing.T) {
	cases := []struct {
		role   string
		want   []string
		hasAll bool
	}{
		{"admin", []string{ScopeQuery, ScopeUpload, ScopeAgent, ScopeAdmin}, true},
		{"user", []string{ScopeQuery, ScopeUpload, ScopeAgent}, true},
		{"readonly", []string{ScopeQuery}, true},
		{"unknown-role", []string{ScopeQuery}, true},
	}
	for _, c := range cases {
		got := ScopesForRole(c.role)
		if !stringSliceEqual(got, c.want) {
			t.Errorf("ScopesForRole(%q) = %v, want %v", c.role, got, c.want)
		}
	}
	// The readonly default must NOT include upload/admin.
	if got := ScopesForRole("readonly"); contains(got, ScopeUpload) || contains(got, ScopeAdmin) {
		t.Errorf("readonly scopes leaked privileged scopes: %v", got)
	}
}

func TestIssueToken_RoundTripAndClaims(t *testing.T) {
	secret := "test-secret-0123456789abcdef"
	token, exp, err := IssueToken(secret, "u-1", "alice", "admin", "acme")
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	if token == "" {
		t.Fatal("expected a non-empty token")
	}
	if time.Until(exp) < 23*time.Hour {
		t.Errorf("expected ~24h expiry, got %v", exp)
	}

	v := NewVerifier(secret)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	claims, err := v.Verify(req)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.TenantID != "acme" || claims.UserID != "u-1" || claims.Permission != "admin" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
	if !contains(claims.Scopes, ScopeAdmin) {
		t.Errorf("expected admin scope, got %v", claims.Scopes)
	}
}

func TestIssueToken_TokenFormatCompatibleWithTestToken(t *testing.T) {
	// The login-issued token must be parseable the same way as the existing
	// test token path, so nothing downstream changes.
	secret := "shared-secret"
	tok, _, err := IssueToken(secret, "u-2", "bob", "user", "default")
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("expected a 3-part JWT, got %d parts", len(parts))
	}
}

func stringSliceEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

package auth

import (
	"strings"
	"testing"
)

func TestHashAndVerifyPassword(t *testing.T) {
	hash, err := HashPassword("s3cret-password")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "" || strings.Contains(hash, "s3cret-password") {
		t.Fatalf("hash must not contain the plaintext, got %q", hash)
	}
	if !VerifyPassword(hash, "s3cret-password") {
		t.Error("expected correct password to verify")
	}
	if VerifyPassword(hash, "wrong-password") {
		t.Error("expected wrong password to fail")
	}
}

func TestVerifyPassword_EmptyHashUsesDummy(t *testing.T) {
	// An unknown user has no stored hash; verify must still return false without
	// erroring (the bcrypt dummy path).
	if VerifyPassword("", "anything") {
		t.Error("expected empty-hash verify to fail")
	}
}

func TestHashPassword_UniqueSalts(t *testing.T) {
	h1, _ := HashPassword("same-password")
	h2, _ := HashPassword("same-password")
	if h1 == h2 {
		t.Error("expected distinct hashes for the same password (random salts)")
	}
	if !VerifyPassword(h1, "same-password") || !VerifyPassword(h2, "same-password") {
		t.Error("both hashes must verify")
	}
}

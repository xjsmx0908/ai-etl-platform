package auth

import (
	"errors"
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

// TestValidatePasswordStorage_RejectsOnlyWhatCannotBeStored pins the storage
// check to a format constraint, not a policy.
//
// It must accept short passwords -- the administrator user API has always
// allowed them, so enforcing a floor here would change a shipped contract -- and
// reject only inputs bcrypt cannot represent.
func TestValidatePasswordStorage_RejectsOnlyWhatCannotBeStored(t *testing.T) {
	cases := []struct {
		name     string
		password string
		want     error
	}{
		{"one character", "a", nil},
		{"empty", "", nil},
		{"exactly the ceiling", strings.Repeat("a", MaxPasswordBytes), nil},
		{"one byte above the ceiling", strings.Repeat("a", MaxPasswordBytes+1), ErrPasswordTooLong},
		{"far above the ceiling", strings.Repeat("a", 500), ErrPasswordTooLong},
		// Bytes are counted, not runes, so 25 CJK characters (75 bytes) are over
		// the ceiling even though only 25 characters were typed. Pinned because
		// this is the shape a real passphrase takes.
		{"CJK past the byte ceiling", strings.Repeat("密", MaxPasswordBytes/3+1), ErrPasswordTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePasswordStorage(tc.password); !errors.Is(err, tc.want) {
				t.Fatalf("ValidatePasswordStorage(%d bytes) = %v, want %v", len(tc.password), err, tc.want)
			}
		})
	}
}

// TestValidatePasswordStorage_AgreesWithBcrypt pins the check to the storage
// format rather than to its own constant.
//
// The defect this guards against was not "the limit is wrong" but "there is no
// limit": a passphrase over 72 bytes reached HashPassword, which returned
// ErrPasswordTooLong, which the handler reported as HTTP 500. So the assertion
// that matters is not the number 72 -- it is that the validator and HashPassword
// agree on which inputs are acceptable. If x/crypto changes its limit, this
// fails here instead of surfacing as a 500 in production.
func TestValidatePasswordStorage_AgreesWithBcrypt(t *testing.T) {
	cases := []struct {
		name     string
		password string
	}{
		{"one character", "a"},
		{"ceiling", strings.Repeat("a", MaxPasswordBytes)},
		{"past the ceiling", strings.Repeat("a", MaxPasswordBytes+1)},
		{"far past the ceiling", strings.Repeat("a", 200)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			storageErr := ValidatePasswordStorage(tc.password)
			_, hashErr := HashPassword(tc.password)
			if (storageErr == nil) != (hashErr == nil) {
				t.Fatalf("%d bytes: ValidatePasswordStorage = %v but HashPassword = %v -- "+
					"the check and the storage format disagree, so one of them will "+
					"turn a valid password into an error the user cannot act on",
					len(tc.password), storageErr, hashErr)
			}
		})
	}
}

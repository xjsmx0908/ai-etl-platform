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

// TestValidatePassword_Boundaries covers the full self-service policy: a floor
// counted in runes and a ceiling counted in bytes.
func TestValidatePassword_Boundaries(t *testing.T) {
	cases := []struct {
		name     string
		password string
		want     error
	}{
		{"empty", "", ErrPasswordTooShort},
		{"one rune below the floor", strings.Repeat("a", MinPasswordRunes-1), ErrPasswordTooShort},
		{"exactly the floor", strings.Repeat("a", MinPasswordRunes), nil},
		{"exactly the ceiling", strings.Repeat("a", MaxPasswordBytes), nil},
		{"one byte above the ceiling", strings.Repeat("a", MaxPasswordBytes+1), ErrPasswordTooLong},
		// Runes are counted for the floor, so 8 CJK characters (24 bytes) pass.
		{"eight CJK runes", strings.Repeat("密", MinPasswordRunes), nil},
		// Bytes are counted for the ceiling, so 25 CJK runes (75 bytes) fail even
		// though only 25 characters were typed.
		{"CJK runes past the byte ceiling", strings.Repeat("密", MaxPasswordBytes/3+1), ErrPasswordTooLong},
		// Whitespace is not special-cased: an 8-space password is long enough.
		// Pinned so the behaviour is a decision rather than an accident.
		{"eight spaces", strings.Repeat(" ", MinPasswordRunes), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePassword(tc.password)
			if !errors.Is(err, tc.want) {
				t.Fatalf("ValidatePassword(%d bytes, %d runes) = %v, want %v",
					len(tc.password), len([]rune(tc.password)), err, tc.want)
			}
		})
	}
}

// TestValidatePasswordStorage_RejectsOnlyWhatCannotBeStored pins the split
// between the two validators.
//
// The storage check is a format constraint, so it must accept short passwords
// (the administrator API has always allowed them) and reject only inputs bcrypt
// cannot represent. If it ever starts enforcing the length policy, the
// administrator API silently changes contract.
func TestValidatePasswordStorage_RejectsOnlyWhatCannotBeStored(t *testing.T) {
	cases := []struct {
		name     string
		password string
		want     error
	}{
		{"one character", "a", nil},
		{"well below the policy floor", strings.Repeat("a", MinPasswordRunes-1), nil},
		{"empty", "", nil},
		{"exactly the ceiling", strings.Repeat("a", MaxPasswordBytes), nil},
		{"one byte above the ceiling", strings.Repeat("a", MaxPasswordBytes+1), ErrPasswordTooLong},
		{"far above the ceiling", strings.Repeat("a", 500), ErrPasswordTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePasswordStorage(tc.password); !errors.Is(err, tc.want) {
				t.Fatalf("ValidatePasswordStorage(%d bytes) = %v, want %v", len(tc.password), err, tc.want)
			}
		})
	}
}

// TestValidatePassword_AgreesWithBcrypt pins the policy to the storage format
// rather than to its own constants.
//
// The defect this guards against was not "the limit is wrong" but "there is no
// limit": a passphrase over 72 bytes reached HashPassword, which returned
// ErrPasswordTooLong, which the handler reported as HTTP 500. So the assertion
// that matters is not the number 72 -- it is that ValidatePassword and
// HashPassword agree on which inputs are acceptable. If x/crypto changes its
// limit, this fails here instead of surfacing as a 500 in production.
func TestValidatePassword_AgreesWithBcrypt(t *testing.T) {
	cases := []struct {
		name     string
		password string
	}{
		{"floor", strings.Repeat("a", MinPasswordRunes)},
		{"ceiling", strings.Repeat("a", MaxPasswordBytes)},
		{"past the ceiling", strings.Repeat("a", MaxPasswordBytes+1)},
		{"far past the ceiling", strings.Repeat("a", 200)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policyErr := ValidatePassword(tc.password)
			_, hashErr := HashPassword(tc.password)
			if (policyErr == nil) != (hashErr == nil) {
				t.Fatalf("%d bytes: ValidatePassword = %v but HashPassword = %v -- "+
					"the policy and the storage format disagree, so one of them will "+
					"turn a valid password into an error the user cannot act on",
					len(tc.password), policyErr, hashErr)
			}
		})
	}
}

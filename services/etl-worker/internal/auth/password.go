package auth

import (
	"errors"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12

// Password constraints.
//
// MaxPasswordBytes is not a preference: it is a hard property of the storage
// format. bcrypt refuses inputs longer than 72 bytes and x/crypto returns
// ErrPasswordTooLong rather than truncating, so without an explicit check a long
// passphrase reaches HashPassword and comes back as an error the handler reports
// as HTTP 500 -- a server fault, from the user's point of view, for typing too
// much. This is a defect, and every path that writes a password must reject it.
//
// MinPasswordRunes is a product policy (NIST SP 800-63B puts the floor at 8). It
// is deliberately NOT applied to the administrator user API: those endpoints
// have always accepted any non-empty password, so adding a floor there would
// change a shipped contract rather than fix a bug. It applies where an end user
// chooses their own credential, which is the self-service invite path.
const (
	MinPasswordRunes = 8
	MaxPasswordBytes = 72
)

// ErrPasswordTooShort and ErrPasswordTooLong are returned by the validators.
// They are distinct values so callers can render a specific message instead of
// collapsing both into "invalid password".
var (
	ErrPasswordTooShort = errors.New("auth: password is shorter than the minimum length")
	ErrPasswordTooLong  = errors.New("auth: password is longer than bcrypt accepts")
)

// ValidatePasswordStorage rejects only passwords the storage format cannot
// represent. An input that cannot be stored is a client error, never a server
// fault, so every path that writes a password must call this before
// HashPassword.
func ValidatePasswordStorage(password string) error {
	if len(password) > MaxPasswordBytes {
		return ErrPasswordTooLong
	}
	return nil
}

// ValidatePassword applies the full policy for a credential an end user chooses
// for themselves: it must be storable and at least MinPasswordRunes long.
//
// The minimum counts runes (what the person typed) and the maximum counts bytes
// (what bcrypt measures), so a passphrase of CJK characters is not rejected for
// being 3x its visible length.
func ValidatePassword(password string) error {
	if err := ValidatePasswordStorage(password); err != nil {
		return err
	}
	if utf8.RuneCountInString(password) < MinPasswordRunes {
		return ErrPasswordTooShort
	}
	return nil
}

// dummyHash is compared when no stored hash exists so that an unknown username
// costs the same bcrypt time as a known one, blunting username enumeration.
var dummyHash string

func init() {
	h, err := bcrypt.GenerateFromPassword([]byte("dummy-bcrypt-compare"), bcryptCost)
	if err != nil {
		panic(err)
	}
	dummyHash = string(h)
}

// HashPassword returns a bcrypt hash of the password. This is the only place a
// plaintext password becomes a hash; stores never see the plaintext.
func HashPassword(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// VerifyPassword reports whether password matches the bcrypt hash. A missing
// hash (unknown user) falls back to a dummy comparison so timing does not leak
// whether a username exists.
func VerifyPassword(hash, password string) bool {
	if hash == "" {
		hash = dummyHash
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

package auth

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

const bcryptCost = 12

// MaxPasswordBytes is not a preference: it is a hard property of the storage
// format. bcrypt refuses inputs longer than 72 bytes and x/crypto returns
// ErrPasswordTooLong rather than truncating, so without an explicit check a long
// passphrase reaches HashPassword and comes back as an error the handler reports
// as HTTP 500 -- a server fault, from the user's point of view, for typing too
// much. This is a defect, and every path that writes a password must reject it.
const MaxPasswordBytes = 72

// ErrPasswordTooLong is returned by ValidatePasswordStorage. It is a distinct
// value so callers can render a specific message instead of collapsing it into
// "invalid password".
var ErrPasswordTooLong = errors.New("auth: password is longer than bcrypt accepts")

// ValidatePasswordStorage rejects only passwords the storage format cannot
// represent. An input that cannot be stored is a client error, never a server
// fault, so every path that writes a password must call this before
// HashPassword.
//
// There is deliberately no minimum length here. A floor is a product policy
// rather than a storage constraint, and these endpoints have always accepted any
// non-empty password -- adding one would change a shipped contract instead of
// fixing a bug.
func ValidatePasswordStorage(password string) error {
	if len(password) > MaxPasswordBytes {
		return ErrPasswordTooLong
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

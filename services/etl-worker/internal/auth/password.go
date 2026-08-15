package auth

import "golang.org/x/crypto/bcrypt"

const bcryptCost = 12

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

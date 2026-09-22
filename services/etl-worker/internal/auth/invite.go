package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// InviteTokenBytes is the entropy behind a self-service invite link.
//
// The accept endpoint is unauthenticated -- the token *is* the credential -- so
// its width is the only thing standing between an invite and a guess. 32 bytes
// matches the session credential and is far beyond brute force; anything shorter
// would make the link the weakest credential in the system.
const InviteTokenBytes = 32

// NewInviteToken returns a fresh invite token together with its SHA-256 digest.
//
// The plaintext is returned exactly once, to be shown to the administrator who
// created it. Only the digest is stored, so a database reader cannot mint a
// working link -- the same reasoning that keeps platform_sessions from storing
// its credential.
func NewInviteToken() (string, [32]byte, error) {
	buf := make([]byte, InviteTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", [32]byte{}, fmt.Errorf("generate invite token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	return token, DigestInviteToken(token), nil
}

// DigestInviteToken hashes a presented token so it can be looked up.
//
// SHA-256 rather than bcrypt, deliberately: the token is 256 bits of uniform
// randomness, so there is no low-entropy secret to slow an attacker down, and a
// lookup has to be by digest anyway -- a salted hash could not be queried at
// all. Passwords use bcrypt (see HashPassword); tokens use this.
func DigestInviteToken(token string) [32]byte {
	return sha256.Sum256([]byte(token))
}

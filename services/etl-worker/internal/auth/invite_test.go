package auth

import (
	"strings"
	"testing"
)

// TestNewInviteToken_IsUniqueAndUrlSafe covers the properties the invite flow
// depends on.
//
// The accept endpoint is unauthenticated, so this token is the whole credential.
// Three things therefore have to hold: the tokens are unpredictable (a fixed
// sequence would let one invitee forge another), they are unique (a collision
// would silently hand one person another's invite), and they survive a URL path
// without escaping (the link is /accept-invite/<token>).
func TestNewInviteToken_IsUniqueAndUrlSafe(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 64; i++ {
		token, digest, err := NewInviteToken()
		if err != nil {
			t.Fatalf("NewInviteToken: %v", err)
		}
		if seen[token] {
			t.Fatalf("token repeated after %d draws: %q", i, token)
		}
		seen[token] = true

		// 32 bytes of entropy in unpadded base64url is 43 characters.
		if len(token) != 43 {
			t.Fatalf("token length = %d, want 43 (32 bytes base64url)", len(token))
		}
		for _, r := range token {
			if !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_", r) {
				t.Fatalf("token contains %q, which would need escaping in a URL path: %q", r, token)
			}
		}
		if DigestInviteToken(token) != digest {
			t.Fatal("the returned digest does not match the token it was returned with")
		}
	}
}

func TestDigestInviteToken_IsDeterministic(t *testing.T) {
	first := DigestInviteToken("some-token")
	if first != DigestInviteToken("some-token") {
		t.Fatal("the same token must always produce the same digest, or lookups cannot work")
	}
	if first == DigestInviteToken("some-token ") {
		t.Fatal("a different token must produce a different digest")
	}
}

// TestInviteTokenEntropy pins the width. Shrinking it is the one change that
// would quietly weaken the whole flow, because nothing else in the system limits
// how many tokens an attacker may try.
func TestInviteTokenEntropy(t *testing.T) {
	if InviteTokenBytes < 32 {
		t.Fatalf("InviteTokenBytes = %d; an unauthenticated credential must carry at least 256 bits", InviteTokenBytes)
	}
}

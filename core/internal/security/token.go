// Package security holds the primitives the daemon uses to protect its local
// control API: the authentication token, the on-disk state file and log
// redaction of secrets.
//
// Nothing in this package ever returns a secret through a String() method and
// the daemon logger is wired to mask registered secrets, so a stray %v of a
// token cannot leak into daemon.log.
package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
)

// TokenBytes is the entropy of a generated API token. 32 bytes (256 bits) is
// well beyond what a loopback attacker could brute force in the lifetime of a
// process, while staying short enough to live in a state file.
const TokenBytes = 32

// GenerateToken returns a URL-safe random token.
func GenerateToken() (string, error) {
	buf := make([]byte, TokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate api token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// GenerateID returns n cryptographically random bytes encoded as lowercase hex.
// Peer and room identifiers use it so that ids are unguessable by construction.
func GenerateID(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("id length must be positive, got %d", n)
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	const hexDigits = "0123456789abcdef"
	out := make([]byte, 0, n*2)
	for _, b := range buf {
		out = append(out, hexDigits[b>>4], hexDigits[b&0x0f])
	}
	return string(out), nil
}

// ConstantTimeEqual compares two secrets without leaking their contents
// through timing. An empty expected value never matches, so a daemon that
// failed to load its token rejects every client instead of accepting all of
// them.
func ConstantTimeEqual(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

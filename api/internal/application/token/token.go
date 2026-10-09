// Package token makes the opaque tokens used for sessions and invite links.
// The client gets the token; the database only ever stores its SHA-256.
package token

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

const size = 32

// New returns a random 32-byte token, base64url-encoded for the client, and
// the SHA-256 hash to store.
func New() (string, []byte, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("token: %w", err)
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(raw), sum[:], nil
}

// Hash decodes a client token and returns its SHA-256. It returns false for
// anything that is not a well-formed token, so callers reject it before
// touching the database. Each token has exactly one valid spelling: Strict
// rejects non-zero padding bits, and the length check rejects the CR and LF
// that the decoder would otherwise skip.
func Hash(t string) ([]byte, bool) {
	if len(t) != base64.RawURLEncoding.EncodedLen(size) {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(t)
	if err != nil || len(raw) != size {
		return nil, false
	}
	sum := sha256.Sum256(raw)
	return sum[:], true
}

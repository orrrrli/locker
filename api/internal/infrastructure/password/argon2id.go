// Package password hashes and verifies passwords with argon2id (R3.1).
package password

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Params are the argon2id cost parameters. They are stored in every hash, so
// raising them later still verifies old hashes.
type Params struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
}

// DefaultParams is the OWASP minimum for argon2id (19 MiB, t=2, p=1). Higher
// memory does not fit the API's 256 MB container limit once several logins
// hash at the same time.
var DefaultParams = Params{MemoryKiB: 19 * 1024, Time: 2, Threads: 1}

const (
	saltLen = 16
	keyLen  = 32
	// maxConcurrent caps the memory spent hashing: 4 × 19 MiB.
	maxConcurrent = 4
)

var errMalformed = errors.New("password: malformed hash")

// Hasher implements the application's password hasher.
type Hasher struct {
	params Params
	slots  chan struct{}
}

func NewHasher(p Params) *Hasher {
	return &Hasher{params: p, slots: make(chan struct{}, maxConcurrent)}
}

// Hash returns a PHC-format string: $argon2id$v=19$m=…,t=…,p=…$salt$key.
func (h *Hasher) Hash(ctx context.Context, password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: salt: %w", err)
	}
	key, err := h.derive(ctx, password, salt, h.params, keyLen)
	if err != nil {
		return "", err
	}
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, h.params.MemoryKiB, h.params.Time, h.params.Threads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify reports whether password matches encoded, using the parameters
// stored in encoded.
func (h *Hasher) Verify(ctx context.Context, password, encoded string) (bool, error) {
	p, salt, want, err := decode(encoded)
	if err != nil {
		return false, err
	}
	got, err := h.derive(ctx, password, salt, p, uint32(len(want)))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

func (h *Hasher) derive(ctx context.Context, password string, salt []byte, p Params, n uint32) ([]byte, error) {
	select {
	case h.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-h.slots }()
	return argon2.IDKey([]byte(password), salt, p.Time, p.MemoryKiB, p.Threads, n), nil
}

func decode(encoded string) (Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return Params{}, nil, nil, errMalformed
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return Params{}, nil, nil, errMalformed
	}
	var p Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Time, &p.Threads); err != nil {
		return Params{}, nil, nil, errMalformed
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return Params{}, nil, nil, errMalformed
	}
	key, err := b64.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return Params{}, nil, nil, errMalformed
	}
	return p, salt, key, nil
}

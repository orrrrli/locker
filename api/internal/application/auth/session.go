package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

const (
	tokenBytes = 32
	// SessionIdleTimeout invalidates a session unused for this long.
	SessionIdleTimeout = 60 * 24 * time.Hour
	// sessionTouchEvery limits last_used_at writes to one per hour per
	// session; the 60-day idle window does not need more precision.
	sessionTouchEvery = time.Hour
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	// ErrUnauthenticated covers a missing, unknown or expired token.
	ErrUnauthenticated = errors.New("unauthenticated")
)

// Sessions is what auth needs from the session table. It only ever sees
// token hashes.
type Sessions interface {
	Create(ctx context.Context, userID int64, tokenHash []byte, now time.Time) (int64, error)
	// ByTokenHash returns domain.ErrNotFound when no session matches.
	ByTokenHash(ctx context.Context, tokenHash []byte) (domain.Session, error)
	Touch(ctx context.Context, id int64, now time.Time) error
	Delete(ctx context.Context, id int64) error
	// DeleteIdle deletes sessions last used at or before cutoff.
	DeleteIdle(ctx context.Context, cutoff time.Time) (int64, error)
}

// Auth is an authenticated request.
type Auth struct {
	UserID    int64
	SessionID int64
}

// newToken returns an opaque 32-byte random token, base64url-encoded for the
// client, and the SHA-256 hash stored in the session row.
func newToken() (string, []byte, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, fmt.Errorf("auth: token: %w", err)
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(raw), sum[:], nil
}

// hashToken decodes a client token and hashes it. Anything that is not a
// well-formed 32-byte token is rejected before touching the database.
func hashToken(token string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(raw) != tokenBytes {
		return nil, ErrUnauthenticated
	}
	sum := sha256.Sum256(raw)
	return sum[:], nil
}

// createSession stores a new session for userID and returns its token.
func (s *Service) createSession(ctx context.Context, userID int64) (string, error) {
	token, hash, err := newToken()
	if err != nil {
		return "", err
	}
	if _, err := s.sessions.Create(ctx, userID, hash, s.now()); err != nil {
		return "", err
	}
	return token, nil
}

// Login checks an email and password and opens a session. An unknown email
// costs the same argon2id work as a wrong password, so the response time
// does not reveal which emails are registered.
func (s *Service) Login(ctx context.Context, email, password string) (string, error) {
	email, err := NormalizeEmail(email)
	if err != nil {
		return "", ErrInvalidCredentials
	}
	userID, hash, err := s.users.PasswordIdentity(ctx, email)
	if errors.Is(err, domain.ErrNotFound) {
		dummy, err := s.dummyHash(ctx)
		if err != nil {
			return "", err
		}
		// Return Verify's error like the known-email branch does, so a
		// cancelled request looks the same whether or not the email exists.
		if _, err := s.hasher.Verify(ctx, password, dummy); err != nil {
			return "", err
		}
		return "", ErrInvalidCredentials
	}
	if err != nil {
		return "", err
	}
	ok, err := s.hasher.Verify(ctx, password, hash)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrInvalidCredentials
	}
	return s.createSession(ctx, userID)
}

func (s *Service) dummyHash(ctx context.Context) (string, error) {
	s.dummyMu.Lock()
	defer s.dummyMu.Unlock()
	if s.dummy != "" {
		return s.dummy, nil
	}
	// Errors are not cached: one failure must not turn every later
	// unknown-email login into a 500.
	hash, err := s.hasher.Hash(ctx, "not a real password")
	if err != nil {
		return "", err
	}
	s.dummy = hash
	return hash, nil
}

// Authenticate resolves a bearer token to its session. A session unused for
// 60 days is deleted and its token rejected. Tokens are not rotated: a
// rotation makes parallel requests and lost responses log the user out, and
// a token stored in the Keychain gains little from it.
func (s *Service) Authenticate(ctx context.Context, token string) (Auth, error) {
	hash, err := hashToken(token)
	if err != nil {
		return Auth{}, err
	}
	now := s.now()

	sess, err := s.sessions.ByTokenHash(ctx, hash)
	if errors.Is(err, domain.ErrNotFound) {
		return Auth{}, ErrUnauthenticated
	}
	if err != nil {
		return Auth{}, err
	}

	if !now.Before(sess.LastUsedAt.Add(SessionIdleTimeout)) {
		if err := s.sessions.Delete(ctx, sess.ID); err != nil {
			return Auth{}, err
		}
		return Auth{}, ErrUnauthenticated
	}

	if now.Sub(sess.LastUsedAt) >= sessionTouchEvery {
		if err := s.sessions.Touch(ctx, sess.ID, now); err != nil {
			return Auth{}, err
		}
	}
	return Auth{UserID: sess.UserID, SessionID: sess.ID}, nil
}

// DeleteIdleSessions removes sessions unused for SessionIdleTimeout, the
// same rule Authenticate applies, so sessions that are never presented again
// do not pile up. It reports how many it deleted.
func (s *Service) DeleteIdleSessions(ctx context.Context) (int64, error) {
	return s.sessions.DeleteIdle(ctx, s.now().Add(-SessionIdleTimeout))
}

// Logout deletes the session.
func (s *Service) Logout(ctx context.Context, sessionID int64) error {
	return s.sessions.Delete(ctx, sessionID)
}

// sessionState keeps the lazily computed dummy hash used by Login.
type sessionState struct {
	dummyMu sync.Mutex
	dummy   string
}

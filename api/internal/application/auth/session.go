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
	// SessionRotateAfter issues a new token once the current one is this old.
	SessionRotateAfter = 7 * 24 * time.Hour
)

var (
	ErrInvalidCredentials = errors.New("invalid email or password")
	// ErrUnauthenticated covers a missing, unknown, expired or reused token.
	ErrUnauthenticated = errors.New("unauthenticated")
)

// Sessions is what auth needs from the session table. It only ever sees
// token hashes.
type Sessions interface {
	Create(ctx context.Context, userID int64, tokenHash []byte, now time.Time) (int64, error)
	// ByTokenHash and ByPreviousTokenHash return domain.ErrNotFound when no session matches.
	ByTokenHash(ctx context.Context, tokenHash []byte) (domain.Session, error)
	ByPreviousTokenHash(ctx context.Context, tokenHash []byte) (domain.Session, error)
	Touch(ctx context.Context, id int64, now time.Time) error
	// Rotate reports false when the session no longer holds oldHash.
	Rotate(ctx context.Context, id int64, oldHash, newHash []byte, now time.Time) (bool, error)
	Delete(ctx context.Context, id int64) error
}

// Auth is an authenticated request.
type Auth struct {
	UserID    int64
	SessionID int64
	// NewToken is set when the session was rotated on this request; the
	// client must store it and drop the old one.
	NewToken string
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
	email, err := normalizeEmail(email)
	if err != nil {
		return "", ErrInvalidCredentials
	}
	userID, hash, err := s.users.PasswordIdentity(ctx, email)
	if errors.Is(err, domain.ErrNotFound) {
		dummy, err := s.dummyHash(ctx)
		if err != nil {
			return "", err
		}
		_, _ = s.hasher.Verify(ctx, password, dummy)
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
	s.dummyOnce.Do(func() {
		// Not tied to this request: a cancelled first caller must not cache an error.
		s.dummy, s.dummyErr = s.hasher.Hash(context.WithoutCancel(ctx), "not a real password")
	})
	return s.dummy, s.dummyErr
}

// Authenticate resolves a bearer token to its session:
//   - unused for 60 days: the session is deleted and the token rejected;
//   - rotated more than 7 days ago: a new token is issued and the old hash
//     kept as previous_token_hash;
//   - a token that was rotated out is presented again: it was reused, so the
//     session is deleted (reuse detection).
func (s *Service) Authenticate(ctx context.Context, token string) (Auth, error) {
	hash, err := hashToken(token)
	if err != nil {
		return Auth{}, err
	}
	now := s.now()

	sess, err := s.sessions.ByTokenHash(ctx, hash)
	if errors.Is(err, domain.ErrNotFound) {
		return Auth{}, s.detectReuse(ctx, hash)
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

	a := Auth{UserID: sess.UserID, SessionID: sess.ID}
	if !now.Before(sess.RotatedAt.Add(SessionRotateAfter)) {
		newTok, newHash, err := newToken()
		if err != nil {
			return Auth{}, err
		}
		rotated, err := s.sessions.Rotate(ctx, sess.ID, hash, newHash, now)
		if err != nil {
			return Auth{}, err
		}
		if rotated {
			a.NewToken = newTok
			return a, nil
		}
		// A concurrent request rotated it first and delivered the new token.
	}
	if err := s.sessions.Touch(ctx, sess.ID, now); err != nil {
		return Auth{}, err
	}
	return a, nil
}

// detectReuse deletes the session if hash is a token that was rotated out.
// It always returns ErrUnauthenticated unless the lookup itself fails.
func (s *Service) detectReuse(ctx context.Context, hash []byte) error {
	sess, err := s.sessions.ByPreviousTokenHash(ctx, hash)
	if errors.Is(err, domain.ErrNotFound) {
		return ErrUnauthenticated
	}
	if err != nil {
		return err
	}
	if err := s.sessions.Delete(ctx, sess.ID); err != nil {
		return err
	}
	return ErrUnauthenticated
}

// Logout deletes the session.
func (s *Service) Logout(ctx context.Context, sessionID int64) error {
	return s.sessions.Delete(ctx, sessionID)
}

// sessionState keeps the lazily computed dummy hash used by Login.
type sessionState struct {
	dummyOnce sync.Once
	dummy     string
	dummyErr  error
}

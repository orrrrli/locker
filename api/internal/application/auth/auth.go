// Package auth holds the registration, login and session use cases.
package auth

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

// Validation errors returned by Register. Age errors come from domain.CheckAge.
var (
	ErrInvalidName      = errors.New("name must be 1 to 100 characters")
	ErrInvalidEmail     = errors.New("email is not valid")
	ErrPasswordTooShort = errors.New("password must be at least 8 characters")
	ErrPasswordTooLong  = errors.New("password must be at most 128 characters")
	ErrEmailTaken       = errors.New("email is already registered")
)

const (
	maxNameLen     = 100
	minPasswordLen = 8
	maxPasswordLen = 128
	maxEmailLen    = 254
)

// PasswordHasher hashes passwords with argon2id.
type PasswordHasher interface {
	Hash(ctx context.Context, password string) (string, error)
	Verify(ctx context.Context, password, encoded string) (bool, error)
}

// Users is what auth needs from the user and auth_identity tables.
type Users interface {
	CreateUser(ctx context.Context, name, email string, birthDate time.Time) (int64, error)
	// CreateIdentity returns domain.ErrAlreadyExists when (provider, subject) is taken.
	CreateIdentity(ctx context.Context, userID int64, provider domain.Provider, subject string, passwordHash *string) error
	// PasswordIdentity returns domain.ErrNotFound when no password identity has that email.
	PasswordIdentity(ctx context.Context, email string) (userID int64, passwordHash string, err error)
}

type Deps struct {
	Tx       application.TxRunner
	Users    Users
	Sessions Sessions
	Hasher   PasswordHasher
	Now      func() time.Time // nil means time.Now
}

type Service struct {
	tx       application.TxRunner
	users    Users
	sessions Sessions
	hasher   PasswordHasher
	now      func() time.Time
	sessionState
}

func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{tx: d.Tx, users: d.Users, sessions: d.Sessions, hasher: d.Hasher, now: d.Now}
}

type RegisterInput struct {
	Name      string
	Email     string
	Password  string
	BirthDate time.Time // zero when missing
}

// Registered is the result of a successful registration: the new user,
// already signed in.
type Registered struct {
	UserID int64
	Token  string
}

// Register creates a user with a password identity and signs them in. It
// rejects a missing birth date and users younger than 15 (R1.1, R1.2),
// stores the birth date (R1.4) and hashes the password with argon2id (R3.1).
func (s *Service) Register(ctx context.Context, in RegisterInput) (Registered, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > maxNameLen {
		return Registered{}, ErrInvalidName
	}
	email, err := normalizeEmail(in.Email)
	if err != nil {
		return Registered{}, err
	}
	if err := checkPassword(in.Password); err != nil {
		return Registered{}, err
	}
	if err := domain.CheckAge(in.BirthDate, s.now().UTC()); err != nil {
		return Registered{}, err
	}

	// Hash outside the transaction: it is the slow part.
	hash, err := s.hasher.Hash(ctx, in.Password)
	if err != nil {
		return Registered{}, err
	}

	var out Registered
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		id, err := s.users.CreateUser(ctx, name, email, in.BirthDate)
		if err != nil {
			return err
		}
		if err := s.users.CreateIdentity(ctx, id, domain.ProviderPassword, email, &hash); err != nil {
			return err
		}
		token, err := s.createSession(ctx, id)
		if err != nil {
			return err
		}
		out = Registered{UserID: id, Token: token}
		return nil
	})
	if errors.Is(err, domain.ErrAlreadyExists) {
		return Registered{}, ErrEmailTaken
	}
	if err != nil {
		return Registered{}, err
	}
	return out, nil
}

// normalizeEmail lowercases and trims the address, which is also the subject
// of the password identity.
func normalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	if email == "" || len(email) > maxEmailLen {
		return "", ErrInvalidEmail
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return "", ErrInvalidEmail
	}
	return email, nil
}

func checkPassword(pw string) error {
	n := utf8.RuneCountInString(pw)
	if n < minPasswordLen {
		return ErrPasswordTooShort
	}
	if n > maxPasswordLen {
		return ErrPasswordTooLong
	}
	return nil
}

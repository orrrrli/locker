package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

const (
	uniqueViolationCode     = "23505"
	foreignKeyViolationCode = "23503"
)

// Users is the repository for the user and what hangs off it to sign in:
// auth identities, and on account deletion device tokens and sessions.
type Users struct {
	pool *pgxpool.Pool
}

func NewUsers(pool *pgxpool.Pool) *Users {
	return &Users{pool: pool}
}

func (r *Users) CreateUser(ctx context.Context, name, email string, birthDate time.Time) (int64, error) {
	id, err := sqlcdb.New(Conn(ctx, r.pool)).CreateUser(ctx, sqlcdb.CreateUserParams{
		Name:      pgtype.Text{String: name, Valid: true},
		Email:     pgtype.Text{String: email, Valid: true},
		BirthDate: pgtype.Date{Time: birthDate, Valid: true},
	})
	if err != nil {
		return 0, fmt.Errorf("postgres: create user: %w", err)
	}
	return id, nil
}

// CreateIdentity returns domain.ErrAlreadyExists when (provider, subject) is
// taken.
func (r *Users) CreateIdentity(ctx context.Context, userID int64, provider domain.Provider, subject string, passwordHash *string) error {
	params := sqlcdb.CreateAuthIdentityParams{UserID: userID, Provider: string(provider), Subject: subject}
	if passwordHash != nil {
		params.PasswordHash = pgtype.Text{String: *passwordHash, Valid: true}
	}
	err := sqlcdb.New(Conn(ctx, r.pool)).CreateAuthIdentity(ctx, params)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode {
		return domain.ErrAlreadyExists
	}
	if err != nil {
		return fmt.Errorf("postgres: create auth identity: %w", err)
	}
	return nil
}

// PasswordIdentity returns the user and password hash for an email, or
// domain.ErrNotFound.
func (r *Users) PasswordIdentity(ctx context.Context, email string) (int64, string, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).GetPasswordIdentity(ctx, email)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", domain.ErrNotFound
	}
	if err != nil {
		return 0, "", fmt.Errorf("postgres: password identity: %w", err)
	}
	return row.UserID, row.PasswordHash.String, nil
}

// DeletePersonalData runs the user part of account deletion (R5.2): it
// deletes the user's auth identities, device tokens, sessions and email
// tokens, unlinks their venue leads (R25.8), then nulls the personal fields and sets
// deleted_at. The row stays, so the history that
// hangs off it keeps its foreign keys. It returns domain.ErrNotFound when the
// user is unknown or already deleted.
func (r *Users) DeletePersonalData(ctx context.Context, userID int64) error {
	q := sqlcdb.New(Conn(ctx, r.pool))
	if err := q.DeleteAuthIdentitiesOfUser(ctx, userID); err != nil {
		return fmt.Errorf("postgres: delete auth identities: %w", err)
	}
	if err := q.DeleteDeviceTokensOfUser(ctx, userID); err != nil {
		return fmt.Errorf("postgres: delete device tokens: %w", err)
	}
	if err := q.DeleteSessionsOfUser(ctx, userID); err != nil {
		return fmt.Errorf("postgres: delete sessions: %w", err)
	}
	if err := q.DeleteEmailTokensOfUser(ctx, userID); err != nil {
		return fmt.Errorf("postgres: delete email tokens: %w", err)
	}
	if err := q.UnlinkVenueLeadsOfUser(ctx, pgtype.Int8{Int64: userID, Valid: true}); err != nil {
		return fmt.Errorf("postgres: unlink venue leads: %w", err)
	}
	n, err := q.ClearUserPersonalData(ctx, userID)
	if err != nil {
		return fmt.Errorf("postgres: clear user personal data: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// Get returns a live user with the operator flag; domain.ErrNotFound for an
// unknown or deleted user.
func (r *Users) Get(ctx context.Context, userID int64) (domain.User, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).GetUser(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.User{}, fmt.Errorf("postgres: get user: %w", err)
	}
	u := domain.User{
		ID:         row.ID,
		Name:       textPtr(row.Name),
		Email:      textPtr(row.Email),
		PhotoURL:   textPtr(row.PhotoUrl),
		CreatedAt:  row.CreatedAt.Time,
		IsOperator: row.IsOperator,
	}
	if row.EmailVerifiedAt.Valid {
		u.EmailVerifiedAt = &row.EmailVerifiedAt.Time
	}
	if row.BirthDate.Valid {
		u.BirthDate = &row.BirthDate.Time
	}
	if row.ContentRulesAcceptedAt.Valid {
		u.ContentRulesAcceptedAt = &row.ContentRulesAcceptedAt.Time
	}
	return u, nil
}

// AcceptContentRules records when the user accepted the content rules and
// returns it; a repeat keeps the first time. domain.ErrNotFound for an
// unknown or deleted user.
func (r *Users) AcceptContentRules(ctx context.Context, userID int64) (time.Time, error) {
	at, err := sqlcdb.New(Conn(ctx, r.pool)).AcceptContentRules(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, domain.ErrNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("postgres: accept content rules: %w", err)
	}
	return at.Time, nil
}

// SetPassword replaces the user's password hash and returns the identity's
// email, or domain.ErrNotFound when the user has no password identity.
func (r *Users) SetPassword(ctx context.Context, userID int64, passwordHash string) (string, error) {
	email, err := sqlcdb.New(Conn(ctx, r.pool)).SetPasswordHash(ctx, sqlcdb.SetPasswordHashParams{
		UserID: userID, PasswordHash: pgtype.Text{String: passwordHash, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("postgres: set password: %w", err)
	}
	return email, nil
}

// MarkEmailVerified sets email_verified_at, keeping the first time. A deleted
// user is left alone.
func (r *Users) MarkEmailVerified(ctx context.Context, userID int64, now time.Time) error {
	err := sqlcdb.New(Conn(ctx, r.pool)).MarkEmailVerified(ctx, sqlcdb.MarkEmailVerifiedParams{ID: userID, Now: timestamptz(now)})
	if err != nil {
		return fmt.Errorf("postgres: mark email verified: %w", err)
	}
	return nil
}

// IsOperator reports whether the user is the operator (rule 12). It returns
// domain.ErrNotFound for an unknown user.
func (r *Users) IsOperator(ctx context.Context, userID int64) (bool, error) {
	ok, err := sqlcdb.New(Conn(ctx, r.pool)).IsOperator(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, domain.ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("postgres: is operator: %w", err)
	}
	return ok, nil
}

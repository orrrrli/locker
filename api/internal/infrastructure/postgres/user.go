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

const uniqueViolationCode = "23505"

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
// deletes the user's auth identities, device tokens and sessions, then nulls
// the personal fields and sets deleted_at. The row stays, so the history that
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
	n, err := q.ClearUserPersonalData(ctx, userID)
	if err != nil {
		return fmt.Errorf("postgres: clear user personal data: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

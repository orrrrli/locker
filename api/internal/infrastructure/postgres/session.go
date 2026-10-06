package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// Sessions is the session repository. It only ever sees token hashes.
type Sessions struct {
	pool *pgxpool.Pool
}

func NewSessions(pool *pgxpool.Pool) *Sessions {
	return &Sessions{pool: pool}
}

func (r *Sessions) Create(ctx context.Context, userID int64, tokenHash []byte, now time.Time) (int64, error) {
	id, err := sqlcdb.New(Conn(ctx, r.pool)).CreateSession(ctx, sqlcdb.CreateSessionParams{
		UserID: userID, TokenHash: tokenHash, CreatedAt: timestamptz(now),
	})
	if err != nil {
		return 0, fmt.Errorf("postgres: create session: %w", err)
	}
	return id, nil
}

// ByTokenHash returns the session whose current token hashes to tokenHash,
// or domain.ErrNotFound.
func (r *Sessions) ByTokenHash(ctx context.Context, tokenHash []byte) (domain.Session, error) {
	return sessionOrNotFound(sqlcdb.New(Conn(ctx, r.pool)).GetSessionByTokenHash(ctx, tokenHash))
}

// ByPreviousTokenHash returns the session whose token before the last
// rotation hashes to tokenHash, or domain.ErrNotFound.
func (r *Sessions) ByPreviousTokenHash(ctx context.Context, tokenHash []byte) (domain.Session, error) {
	return sessionOrNotFound(sqlcdb.New(Conn(ctx, r.pool)).GetSessionByPreviousTokenHash(ctx, tokenHash))
}

func (r *Sessions) Touch(ctx context.Context, id int64, now time.Time) error {
	err := sqlcdb.New(Conn(ctx, r.pool)).TouchSession(ctx, sqlcdb.TouchSessionParams{ID: id, LastUsedAt: timestamptz(now)})
	if err != nil {
		return fmt.Errorf("postgres: touch session: %w", err)
	}
	return nil
}

// Rotate swaps oldHash for newHash and reports whether it did. It does
// nothing when the session no longer holds oldHash: a concurrent request
// rotated it first.
func (r *Sessions) Rotate(ctx context.Context, id int64, oldHash, newHash []byte, now time.Time) (bool, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).RotateSession(ctx, sqlcdb.RotateSessionParams{
		ID: id, OldTokenHash: oldHash, NewTokenHash: newHash, Now: timestamptz(now),
	})
	if err != nil {
		return false, fmt.Errorf("postgres: rotate session: %w", err)
	}
	return n == 1, nil
}

func (r *Sessions) Delete(ctx context.Context, id int64) error {
	if err := sqlcdb.New(Conn(ctx, r.pool)).DeleteSession(ctx, id); err != nil {
		return fmt.Errorf("postgres: delete session: %w", err)
	}
	return nil
}

func sessionOrNotFound(row sqlcdb.Session, err error) (domain.Session, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Session{}, fmt.Errorf("postgres: get session: %w", err)
	}
	return domain.Session{
		ID:                row.ID,
		UserID:            row.UserID,
		TokenHash:         row.TokenHash,
		PreviousTokenHash: row.PreviousTokenHash,
		CreatedAt:         row.CreatedAt.Time,
		LastUsedAt:        row.LastUsedAt.Time,
		RotatedAt:         row.RotatedAt.Time,
	}, nil
}

func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

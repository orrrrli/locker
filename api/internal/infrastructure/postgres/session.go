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

func (r *Sessions) Touch(ctx context.Context, id int64, now time.Time) error {
	err := sqlcdb.New(Conn(ctx, r.pool)).TouchSession(ctx, sqlcdb.TouchSessionParams{ID: id, LastUsedAt: timestamptz(now)})
	if err != nil {
		return fmt.Errorf("postgres: touch session: %w", err)
	}
	return nil
}

func (r *Sessions) Delete(ctx context.Context, id int64) error {
	if err := sqlcdb.New(Conn(ctx, r.pool)).DeleteSession(ctx, id); err != nil {
		return fmt.Errorf("postgres: delete session: %w", err)
	}
	return nil
}

// DeleteIdle deletes sessions last used at or before cutoff and reports how
// many it deleted.
// ponytail: seq scan on session; add an index on last_used_at if the table grows large.
func (r *Sessions) DeleteIdle(ctx context.Context, cutoff time.Time) (int64, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).DeleteIdleSessions(ctx, timestamptz(cutoff))
	if err != nil {
		return 0, fmt.Errorf("postgres: delete idle sessions: %w", err)
	}
	return n, nil
}

func sessionOrNotFound(row sqlcdb.Session, err error) (domain.Session, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Session{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Session{}, fmt.Errorf("postgres: get session: %w", err)
	}
	return domain.Session{
		ID:         row.ID,
		UserID:     row.UserID,
		TokenHash:  row.TokenHash,
		CreatedAt:  row.CreatedAt.Time,
		LastUsedAt: row.LastUsedAt.Time,
	}, nil
}

func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// EmailTokens is the repository for the single-use tokens sent by email. It
// only ever sees token hashes.
//
// ponytail: expired rows that nobody consumed stay until the account is
// deleted; add a ticker job if the table grows.
type EmailTokens struct {
	pool *pgxpool.Pool
}

func NewEmailTokens(pool *pgxpool.Pool) *EmailTokens {
	return &EmailTokens{pool: pool}
}

func (r *EmailTokens) Create(ctx context.Context, userID int64, purpose string, tokenHash []byte, expiresAt time.Time) error {
	err := sqlcdb.New(Conn(ctx, r.pool)).CreateEmailToken(ctx, sqlcdb.CreateEmailTokenParams{
		UserID: userID, Purpose: purpose, TokenHash: tokenHash, ExpiresAt: timestamptz(expiresAt),
	})
	if err != nil {
		return fmt.Errorf("postgres: create email token: %w", err)
	}
	return nil
}

// CountLive reports how many of the user's tokens for purpose are unexpired
// at now.
func (r *EmailTokens) CountLive(ctx context.Context, userID int64, purpose string, now time.Time) (int, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).CountLiveEmailTokens(ctx, sqlcdb.CountLiveEmailTokensParams{
		UserID: userID, Purpose: purpose, Now: timestamptz(now),
	})
	if err != nil {
		return 0, fmt.Errorf("postgres: count live email tokens: %w", err)
	}
	return int(n), nil
}

// DeleteAllOfUser deletes every token of the user for purpose.
func (r *EmailTokens) DeleteAllOfUser(ctx context.Context, userID int64, purpose string) error {
	err := sqlcdb.New(Conn(ctx, r.pool)).DeleteUserEmailTokens(ctx, sqlcdb.DeleteUserEmailTokensParams{UserID: userID, Purpose: purpose})
	if err != nil {
		return fmt.Errorf("postgres: delete user email tokens: %w", err)
	}
	return nil
}

// Consume deletes the unexpired token for purpose that hashes to tokenHash
// and returns its user, or domain.ErrNotFound when it is unknown, used,
// expired or for another purpose.
func (r *EmailTokens) Consume(ctx context.Context, purpose string, tokenHash []byte, now time.Time) (int64, error) {
	userID, err := sqlcdb.New(Conn(ctx, r.pool)).ConsumeEmailToken(ctx, sqlcdb.ConsumeEmailTokenParams{
		TokenHash: tokenHash, Purpose: purpose, Now: timestamptz(now),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, domain.ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("postgres: consume email token: %w", err)
	}
	return userID, nil
}

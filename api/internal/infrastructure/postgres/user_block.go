package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// Blocks is the repository for one-way user blocks.
type Blocks struct {
	pool *pgxpool.Pool
}

func NewBlocks(pool *pgxpool.Pool) *Blocks {
	return &Blocks{pool: pool}
}

// Block stores the block, or keeps the existing one; domain.ErrNotFound for
// an unknown or deleted blocked user.
func (r *Blocks) Block(ctx context.Context, blockerID, blockedID int64) error {
	_, err := sqlcdb.New(Conn(ctx, r.pool)).BlockUser(ctx, sqlcdb.BlockUserParams{BlockerID: blockerID, BlockedID: blockedID})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("postgres: block user: %w", err)
	}
	return nil
}

// Unblock removes the block; no error when there was none.
func (r *Blocks) Unblock(ctx context.Context, blockerID, blockedID int64) error {
	err := sqlcdb.New(Conn(ctx, r.pool)).UnblockUser(ctx, sqlcdb.UnblockUserParams{BlockerID: blockerID, BlockedID: blockedID})
	if err != nil {
		return fmt.Errorf("postgres: unblock user: %w", err)
	}
	return nil
}

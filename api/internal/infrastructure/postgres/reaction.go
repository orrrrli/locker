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

// Reactions is the repository for post reactions.
type Reactions struct {
	pool *pgxpool.Pool
}

func NewReactions(pool *pgxpool.Pool) *Reactions {
	return &Reactions{pool: pool}
}

// LockVisiblePost holds the post until commit; domain.ErrNotFound when it is
// missing or removed.
func (r *Reactions) LockVisiblePost(ctx context.Context, postID int64) error {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); !ok {
		return errors.New("postgres: lock post for reaction: no transaction in ctx")
	}
	_, err := sqlcdb.New(Conn(ctx, r.pool)).LockVisiblePost(ctx, postID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("postgres: lock post %d: %w", postID, err)
	}
	return nil
}

// Remove deletes the reaction and reports whether there was one.
func (r *Reactions) Remove(ctx context.Context, postID, userID int64, kind domain.ReactionKind) (bool, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).RemoveReaction(ctx, sqlcdb.RemoveReactionParams{
		PostID: postID, UserID: userID, Kind: string(kind),
	})
	if err != nil {
		return false, fmt.Errorf("postgres: remove reaction: %w", err)
	}
	return n > 0, nil
}

// Add stores the reaction; one that already exists is kept.
func (r *Reactions) Add(ctx context.Context, postID, userID int64, kind domain.ReactionKind) error {
	err := sqlcdb.New(Conn(ctx, r.pool)).AddReaction(ctx, sqlcdb.AddReactionParams{
		PostID: postID, UserID: userID, Kind: string(kind),
	})
	if err != nil {
		return fmt.Errorf("postgres: add reaction: %w", err)
	}
	return nil
}

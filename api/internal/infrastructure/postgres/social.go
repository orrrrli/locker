package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// Social is the social-data step of account deletion (R18.13, R20.6).
type Social struct {
	pool *pgxpool.Pool
}

func NewSocial(pool *pgxpool.Pool) *Social {
	return &Social{pool: pool}
}

// DeleteSocialDataOfUser removes the user's social data (R18.13, R20.6, R5):
// it releases the upload rows of the posts the user wrote as a player, so the
// cleanup job deletes their photos from R2 after commit, deletes those posts
// (post_media, their reactions and their reports go with them), then the
// user's reactions, follows, blocks (the ones they made and the ones where
// someone blocked them) and reports. Posts written as a team admin stay, with
// the anonymized user row as the writer.
//
// It runs inside the caller's transaction. The order matters: the posts go
// before the reactions, the order a reaction toggle takes (the post's row,
// then the reaction's), so the two cannot deadlock. The posts' rows are locked
// before their uploads are released, the order a delete takes, so an author
// deleting one of them at the same time cannot deadlock either.
func (r *Social) DeleteSocialDataOfUser(ctx context.Context, userID int64) error {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); !ok {
		return errors.New("postgres: delete social data: no transaction in ctx")
	}
	q := sqlcdb.New(Conn(ctx, r.pool))
	if _, err := q.LockPlayerPostsOfUser(ctx, userID); err != nil {
		return fmt.Errorf("postgres: lock player posts: %w", err)
	}
	if _, err := q.ReleaseUploadsOfPlayerPostsOfUser(ctx, userID); err != nil {
		return fmt.Errorf("postgres: release player post uploads: %w", err)
	}
	if _, err := q.DeletePlayerPostsOfUser(ctx, userID); err != nil {
		return fmt.Errorf("postgres: delete player posts: %w", err)
	}
	if _, err := q.DeleteReactionsOfUser(ctx, userID); err != nil {
		return fmt.Errorf("postgres: delete reactions: %w", err)
	}
	if _, err := q.DeleteFollowsOfUser(ctx, userID); err != nil {
		return fmt.Errorf("postgres: delete follows: %w", err)
	}
	if _, err := q.DeleteBlocksOfUser(ctx, userID); err != nil {
		return fmt.Errorf("postgres: delete blocks: %w", err)
	}
	if _, err := q.DeleteReportsOfUser(ctx, userID); err != nil {
		return fmt.Errorf("postgres: delete reports: %w", err)
	}
	return nil
}

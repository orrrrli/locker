package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/domain"
)

// reactionFixture returns a migrated pool, one user and one post by that user.
func reactionFixture(t *testing.T) (pool *pgxpool.Pool, userID, postID int64) {
	t.Helper()
	pool, _ = testPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, birth_date) VALUES ('Ana', '2000-01-01') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	return pool, userID, insertPost(t, pool, userID)
}

func addReaction(pool *pgxpool.Pool, postID, userID int64, kind string) error {
	_, err := pool.Exec(context.Background(), `INSERT INTO reaction (post_id, user_id, kind) VALUES ($1, $2, $3)`, postID, userID, kind)
	return err
}

func countReactions(t *testing.T, pool *pgxpool.Pool, postID int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM reaction WHERE post_id = $1`, postID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A user stores each kind once per post (R19.2): the same
// (post_id, user_id, kind) twice is a primary key violation.
func TestReactionRejectsTheSameKindTwice(t *testing.T) {
	pool, userID, postID := reactionFixture(t)
	if err := addReaction(pool, postID, userID, string(domain.ReactionFire)); err != nil {
		t.Fatalf("first fire: %v", err)
	}

	err := addReaction(pool, postID, userID, string(domain.ReactionFire))

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != uniqueViolation {
		t.Fatalf("second fire: err = %v, want unique_violation", err)
	}
}

// All three kinds on one post are three rows (R19.2).
func TestReactionAllowsEveryKindOnOnePost(t *testing.T) {
	pool, userID, postID := reactionFixture(t)

	for _, kind := range []domain.ReactionKind{domain.ReactionFire, domain.ReactionBall, domain.ReactionClap} {
		if err := addReaction(pool, postID, userID, string(kind)); err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
	}

	if got := countReactions(t, pool, postID); got != 3 {
		t.Fatalf("reactions on the post = %d, want 3", got)
	}
}

// Only fire, ball and clap exist (R19.1), even from a write that skips the use case.
func TestReactionRejectsAnUnknownKind(t *testing.T) {
	pool, userID, postID := reactionFixture(t)

	err := addReaction(pool, postID, userID, "heart")

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != checkViolation {
		t.Fatalf("heart: err = %v, want check_violation", err)
	}
}

// Deleting a post (Posts.Delete, which runs the sqlc DeletePost) takes its
// reactions with it and leaves the reactions of other posts alone.
func TestPostDeleteRemovesItsReactions(t *testing.T) {
	pool, userID, gone := reactionFixture(t)
	ctx := context.Background()
	kept := insertPost(t, pool, userID)
	for _, postID := range []int64{gone, kept} {
		if err := addReaction(pool, postID, userID, string(domain.ReactionClap)); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	err := NewTxRunner(pool).InTx(ctx, func(ctx context.Context) error { return NewPosts(pool).Delete(ctx, gone, now) })

	if err != nil {
		t.Fatalf("delete a post with reactions: %v", err)
	}
	if got := countReactions(t, pool, gone); got != 0 {
		t.Errorf("reactions of the deleted post = %d, want 0", got)
	}
	if got := countReactions(t, pool, kept); got != 1 {
		t.Errorf("reactions of the other post = %d, want 1", got)
	}
}

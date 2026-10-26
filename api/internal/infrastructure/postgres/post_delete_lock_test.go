package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// waitForLockWait fails after within unless the backend pid is waiting on a
// lock. It is the only thing that proves the delete is inside the removal's
// row lock before the test commits; without it the commit could win the race
// and the test would pass even without FOR UPDATE.
func waitForLockWait(t *testing.T, pool *pgxpool.Pool, pid int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		var waiting bool
		if err := pool.QueryRow(context.Background(),
			`SELECT coalesce(wait_event_type = 'Lock', false) FROM pg_stat_activity WHERE pid = $1`, pid).
			Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the delete never waited on the post row lock within %v", within)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Posts.Delete takes the post's row lock before it releases anything, so an
// operator removal still in flight wins (R23.9): the delete waits on it, then
// reads the committed removal and leaves the post, its photo and its upload
// attached and unreleased. Without FOR UPDATE in LockPostRemoval the delete
// reads removed_at as null while the removal is uncommitted, releases the
// upload to the cleanup job, and deletes the post once the removal commits.
func TestPostDeleteWaitsForAnOperatorRemovalBeforeReleasingUploads(t *testing.T) {
	pool, _ := testPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, birth_date) VALUES ('Ana', '2000-01-01') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	postID := insertPost(t, pool, userID)
	const key = "photos/lock-race"
	if _, err := pool.Exec(ctx, `INSERT INTO post_media (post_id, storage_key, position, content_type, size_bytes)
		VALUES ($1, $2, 0, 'image/jpeg', 1)`, postID, key); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO media_upload (storage_key, user_id, post_id) VALUES ($1, $2, $3)`,
		key, userID, postID); err != nil {
		t.Fatal(err)
	}

	// The operator removal is in flight here: it holds the post's row lock and
	// nothing is committed yet.
	removal, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = removal.Rollback(context.Background()) }() // a no-op after the commit
	if _, err := removal.Exec(ctx,
		`UPDATE post SET removed_at = now(), removal_reason = 'spam' WHERE id = $1`, postID); err != nil {
		t.Fatal(err)
	}

	type result struct {
		deleted bool
		err     error
	}
	done := make(chan result, 1)
	pidCh := make(chan int, 1)
	go func() {
		var res result
		err := NewTxRunner(pool).InTx(ctx, func(ctx context.Context) error {
			var pid int
			if err := Conn(ctx, pool).QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				return err
			}
			pidCh <- pid
			res.deleted, res.err = NewPosts(pool).Delete(ctx, postID, time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
			return res.err
		})
		if err != nil && res.err == nil {
			res.err = err
		}
		done <- res
	}()

	var pid int
	select {
	case pid = <-pidCh:
	case <-time.After(10 * time.Second):
		t.Fatal("the delete never started")
	}
	// Commit only once the delete is waiting on the removal's row lock. An
	// earlier commit would let the delete read the removal on its first
	// statement, when even the broken query sees it.
	waitForLockWait(t, pool, pid, 10*time.Second)
	if err := removal.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var res result
	select {
	case res = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the delete never returned after the removal committed")
	}
	if res.err != nil {
		t.Fatalf("Delete: %v", res.err)
	}
	if res.deleted {
		t.Fatalf("Delete deleted a post the operator removed, want it left alone")
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM post WHERE id = $1`, postID); n != 1 {
		t.Errorf("post rows for %d = %d, want the removed post kept", postID, n)
	}
	if n := count(`SELECT count(*) FROM post_media WHERE post_id = $1 AND storage_key = $2`, postID, key); n != 1 {
		t.Errorf("post_media rows for %q = %d, want the photo kept", key, n)
	}
	if n := count(`SELECT count(*) FROM media_upload WHERE storage_key = $1 AND post_id = $2 AND released_at IS NULL`,
		key, postID); n != 1 {
		t.Errorf("attached, unreleased upload rows for %q = %d, want 1: the cleanup job would delete the photo", key, n)
	}
}

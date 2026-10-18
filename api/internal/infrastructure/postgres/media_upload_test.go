package postgres

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// DeleteUnused removes only keys that no post uses and that are past the
// cutoff, oldest first, up to the limit; Create keeps who each key went to.
func TestMediaUploads(t *testing.T) {
	pool, _ := testPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, birth_date) VALUES ('Ana', '2000-01-01') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	repo := NewMediaUploads(pool)
	cutoff := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, u := range []struct {
		key string
		at  time.Time
	}{
		// Out of age order, so the table order cannot pass for "oldest first".
		{"at-cutoff", cutoff},
		{"older", cutoff.Add(-2 * time.Hour)},
		{"oldest", cutoff.Add(-3 * time.Hour)},
		{"in-post", cutoff.Add(-4 * time.Hour)},
		{"fresh", cutoff.Add(time.Second)},
	} {
		if err := repo.Create(ctx, u.key, userID, u.at); err != nil {
			t.Fatal(err)
		}
	}
	// BE-FEED-2 sets post_id when a post uses the key.
	if _, err := pool.Exec(ctx, `UPDATE media_upload SET post_id = 1 WHERE storage_key = 'in-post'`); err != nil {
		t.Fatal(err)
	}

	var owner int64
	if err := pool.QueryRow(ctx, `SELECT user_id FROM media_upload WHERE storage_key = 'oldest'`).Scan(&owner); err != nil || owner != userID {
		t.Fatalf("owner of oldest = %d, %v; want %d", owner, err, userID)
	}

	// lockUnused runs LockUnused in its own transaction, deletes what it
	// locked, and commits, like one cleanup run.
	lockUnused := func(t *testing.T, limit int) []string {
		t.Helper()
		var keys []string
		if err := NewTxRunner(pool).InTx(ctx, func(ctx context.Context) error {
			var err error
			if keys, err = repo.LockUnused(ctx, cutoff, limit); err != nil {
				return err
			}
			return repo.Delete(ctx, keys)
		}); err != nil {
			t.Fatal(err)
		}
		return keys
	}

	got := lockUnused(t, 2)
	if want := []string{"oldest", "older"}; !slices.Equal(got, want) {
		t.Fatalf("first run took %v, want %v (oldest first, limit 2)", got, want)
	}
	if got = lockUnused(t, 10); !slices.Equal(got, []string{"at-cutoff"}) {
		t.Fatalf("second run took %v, want [at-cutoff]", got)
	}

	rows, err := pool.Query(ctx, `SELECT storage_key FROM media_upload ORDER BY storage_key`)
	if err != nil {
		t.Fatal(err)
	}
	var left []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		left = append(left, k)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"fresh", "in-post"}; !slices.Equal(left, want) {
		t.Fatalf("rows left %v, want %v", left, want)
	}
}

// The cleanup and a post attaching a key never both win: the cleanup skips a
// key a post holds, and a post waits for a key the cleanup holds.
func TestMediaUploadLocks(t *testing.T) {
	pool, _ := testPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var userID int64
	if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, birth_date) VALUES ('Ana', '2000-01-01') RETURNING id`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	repo := NewMediaUploads(pool)
	cutoff := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for _, k := range []string{"a", "b"} {
		if err := repo.Create(ctx, k, userID, cutoff.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	// A post is attaching "a" in an open transaction.
	attach, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = attach.Rollback(ctx) }()
	if _, err := attach.Exec(ctx, `UPDATE media_upload SET post_id = 1 WHERE storage_key = 'a' AND post_id IS NULL`); err != nil {
		t.Fatal(err)
	}

	if err := NewTxRunner(pool).InTx(ctx, func(ctx context.Context) error {
		// Without SKIP LOCKED the cleanup would wait on "a"; fail fast instead.
		if _, err := Conn(ctx, pool).Exec(ctx, `SET LOCAL lock_timeout = '1s'`); err != nil {
			return err
		}
		keys, err := repo.LockUnused(ctx, cutoff, 10)
		if err != nil {
			return err
		}
		if !slices.Equal(keys, []string{"b"}) {
			t.Errorf("cleanup took %v, want [b]: a is being attached", keys)
		}
		// While the cleanup holds "b", attaching it cannot go ahead.
		other, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = other.Rollback(ctx) }()
		if _, err := other.Exec(ctx, `SET LOCAL lock_timeout = '50ms'`); err != nil {
			return err
		}
		_, err = other.Exec(ctx, `UPDATE media_upload SET post_id = 2 WHERE storage_key = 'b' AND post_id IS NULL`)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			t.Errorf("attach of a locked key = %v, want lock_not_available", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

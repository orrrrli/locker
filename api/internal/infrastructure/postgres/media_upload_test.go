package postgres

import (
	"context"
	"slices"
	"testing"
	"time"
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

	got, err := repo.DeleteUnused(ctx, cutoff, 2)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if want := []string{"older", "oldest"}; !slices.Equal(got, want) {
		t.Fatalf("first run deleted %v, want %v (oldest first, limit 2)", got, want)
	}
	if got, err = repo.DeleteUnused(ctx, cutoff, 10); err != nil {
		t.Fatal(err)
	}
	if want := []string{"at-cutoff"}; !slices.Equal(got, want) {
		t.Fatalf("second run deleted %v, want %v", got, want)
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

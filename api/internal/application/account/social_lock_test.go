package account

import (
	"context"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

// TestDeleteWaitsForAPostBeingPublished: deletion takes the user's row lock
// before it reads the user's posts, so a player post being published meanwhile
// either commits first, and the deletion removes it and releases its photos,
// or checks deleted_at after it, and is refused. Without that lock the post
// commits after the delete statement, survives the deletion with its
// media_upload row still attached, and its photos never reach the R2 cleanup
// job, which only takes rows with no post (security audit H1).
func TestDeleteWaitsForAPostBeingPublished(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	u := f.user(t)
	team := f.team(t, "Carrera")
	f.member(t, team, f.user(t), domain.RoleAdmin, domain.MembershipActive)
	f.member(t, team, u, domain.RolePlayer, domain.MembershipActive)

	// Another connection publishes, as Publish does: it holds the user's row
	// lock, and its post and photo are not committed yet.
	publishing, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = publishing.Rollback(context.Background()) }() // a no-op after the commit
	if _, err := publishing.Exec(ctx, `SELECT 1 FROM "user" WHERE id = $1 FOR NO KEY UPDATE`, u); err != nil {
		t.Fatal(err)
	}
	var postID int64
	if err := publishing.QueryRow(ctx, `INSERT INTO post (author_user_id, as_kind, team_id, body, share_token)
		VALUES ($1, 'player', $2, 'hola', 'share-race') RETURNING id`, u, team).Scan(&postID); err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO media_upload (storage_key, user_id, created_at)
			VALUES ($1, $2, now() - interval '1 hour')`, []any{"photos/race", u}},
		{`INSERT INTO post_media (post_id, storage_key, position, content_type, size_bytes)
			VALUES ($1, $2, 0, 'image/jpeg', 100)`, []any{postID, "photos/race"}},
		{`UPDATE media_upload SET post_id = $1 WHERE storage_key = $2`, []any{postID, "photos/race"}},
	} {
		if _, err := publishing.Exec(ctx, q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan error, 1)
	go func() { done <- f.svc.Delete(ctx, u) }()

	// The deletion waits on the lock instead of reading past the in-flight
	// post. The wait only makes this slower on a loaded machine, never wrong.
	select {
	case err := <-done:
		t.Fatalf("Delete finished while a post was being published: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	if err := publishing.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM post WHERE author_user_id = $1 AND as_kind = 'player'`, u); n != 0 {
		t.Errorf("player posts = %d, want 0: the post published meanwhile survived", n)
	}
	if n := f.count(t, `SELECT count(*) FROM media_upload
		WHERE storage_key = 'photos/race' AND post_id IS NULL AND released_at IS NOT NULL`); n != 1 {
		t.Errorf("released upload row = %d, want 1: its photos would never reach the cleanup job", n)
	}
}

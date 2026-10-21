package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

// AcceptContentRules stores when the user accepted the content rules and keeps
// the first time on a repeat (R23.13). Only that user's row changes.
func TestUsersAcceptContentRules(t *testing.T) {
	pool, _ := testPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	newUser := func(t *testing.T, email string) int64 {
		t.Helper()
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email, birth_date) VALUES ('X', $1, '2000-01-01') RETURNING id`, email).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	stored := func(t *testing.T, id int64) *time.Time {
		t.Helper()
		var at *time.Time
		if err := pool.QueryRow(ctx, `SELECT content_rules_accepted_at FROM "user" WHERE id = $1`, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	repo := NewUsers(pool)

	t.Run("sets the column and returns it", func(t *testing.T) {
		id := newUser(t, "ana@example.com")
		if got := stored(t, id); got != nil {
			t.Fatalf("before accepting: column = %v, want NULL", got)
		}

		before := time.Now().Add(-time.Minute)
		got, err := repo.AcceptContentRules(ctx, id)
		if err != nil {
			t.Fatal(err)
		}

		at := stored(t, id)
		if at == nil || !at.Equal(got) {
			t.Fatalf("column = %v, returned %v, want the same time", at, got)
		}
		if got.Before(before) || got.After(time.Now().Add(time.Minute)) {
			t.Fatalf("accepted at %v, want about now", got)
		}
		u, err := repo.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if u.ContentRulesAcceptedAt == nil || !u.ContentRulesAcceptedAt.Equal(got) {
			t.Fatalf("Get content rules time = %v, want %v", u.ContentRulesAcceptedAt, got)
		}
	})

	t.Run("a second call keeps the first time", func(t *testing.T) {
		id := newUser(t, "bea@example.com")
		first, err := repo.AcceptContentRules(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		// Back-date the first acceptance so a second now() cannot match it by luck.
		old := first.Add(-48 * time.Hour).Truncate(time.Microsecond)
		if _, err := pool.Exec(ctx, `UPDATE "user" SET content_rules_accepted_at = $2 WHERE id = $1`, id, old); err != nil {
			t.Fatal(err)
		}

		second, err := repo.AcceptContentRules(ctx, id)
		if err != nil {
			t.Fatal(err)
		}

		if !second.Equal(old) {
			t.Fatalf("second call returned %v, want the first time %v", second, old)
		}
		if at := stored(t, id); at == nil || !at.Equal(old) {
			t.Fatalf("column = %v after the second call, want %v", at, old)
		}
	})

	t.Run("another user stays NULL", func(t *testing.T) {
		id := newUser(t, "cai@example.com")
		other := newUser(t, "dan@example.com")

		if _, err := repo.AcceptContentRules(ctx, id); err != nil {
			t.Fatal(err)
		}

		if got := stored(t, other); got != nil {
			t.Fatalf("other user's column = %v, want NULL", got)
		}
	})

	t.Run("a deleted user is not found and stays NULL", func(t *testing.T) {
		id := newUser(t, "eli@example.com")
		if _, err := pool.Exec(ctx, `UPDATE "user" SET deleted_at = now() WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}

		_, err := repo.AcceptContentRules(ctx, id)

		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if got := stored(t, id); got != nil {
			t.Fatalf("deleted user's column = %v, want NULL", got)
		}
	})

	t.Run("an unknown user is not found", func(t *testing.T) {
		_, err := repo.AcceptContentRules(ctx, 999999)

		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

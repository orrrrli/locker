package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

// The repository rules every email link depends on: a token works once,
// only for its purpose, and only before it expires.
func TestEmailTokens(t *testing.T) {
	pool, _ := testPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var userID, otherID int64
	for _, u := range []struct {
		id    *int64
		email string
	}{{&userID, "ana@example.com"}, {&otherID, "bea@example.com"}} {
		if err := pool.QueryRow(ctx, `INSERT INTO "user" (name, email, birth_date) VALUES ('X', $1, '2000-01-01') RETURNING id`, u.email).Scan(u.id); err != nil {
			t.Fatal(err)
		}
	}
	repo := NewEmailTokens(pool)
	now := time.Date(2026, 10, 6, 15, 0, 0, 0, time.UTC)
	hash := func(b byte) []byte { h := make([]byte, 32); h[0] = b; return h }
	// The helpers take the subtest's t, so a failure is reported there and
	// the other subtests still run.
	create := func(t *testing.T, user int64, b byte, purpose string, expires time.Time) {
		t.Helper()
		if err := repo.Create(ctx, user, purpose, hash(b), expires); err != nil {
			t.Fatal(err)
		}
	}
	consume := func(t *testing.T, b byte, purpose string, at time.Time) error {
		t.Helper()
		got, err := repo.Consume(ctx, purpose, hash(b), at)
		if err == nil && got != userID {
			t.Fatalf("consume %d: user %d, want %d", b, got, userID)
		}
		return err
	}

	t.Run("single use", func(t *testing.T) {
		create(t, userID, 1, "verify_email", now.Add(time.Hour))
		if err := consume(t, 1, "verify_email", now); err != nil {
			t.Fatal(err)
		}
		if err := consume(t, 1, "verify_email", now); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("second use: err %v, want ErrNotFound", err)
		}
	})

	t.Run("expiry is exclusive", func(t *testing.T) {
		create(t, userID, 2, "reset_password", now.Add(time.Hour))
		if err := consume(t, 2, "reset_password", now.Add(time.Hour)); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("at expires_at: err %v, want ErrNotFound", err)
		}
		if err := consume(t, 2, "reset_password", now.Add(time.Hour-time.Second)); err != nil {
			t.Fatalf("a second before expiry: %v", err)
		}
	})

	t.Run("purpose must match", func(t *testing.T) {
		create(t, userID, 3, "verify_email", now.Add(time.Hour))
		if err := consume(t, 3, "reset_password", now); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("wrong purpose: err %v, want ErrNotFound", err)
		}
		if err := consume(t, 3, "verify_email", now); err != nil {
			t.Fatalf("a wrong-purpose attempt used the token up: %v", err)
		}
	})

	t.Run("count live and delete by purpose", func(t *testing.T) {
		create(t, userID, 4, "reset_password", now.Add(time.Hour))
		create(t, userID, 5, "reset_password", now.Add(-time.Minute)) // already expired
		create(t, userID, 6, "verify_email", now.Add(time.Hour))
		// Another user's live link counts for them only, and survives Ana's
		// delete: the cap and the revocation are per account.
		create(t, otherID, 9, "reset_password", now.Add(time.Hour))
		if n, err := repo.CountLive(ctx, userID, "reset_password", now); err != nil || n != 1 {
			t.Fatalf("live reset links = %d, %v; want 1", n, err)
		}
		if err := repo.DeleteAllOfUser(ctx, userID, "reset_password"); err != nil {
			t.Fatal(err)
		}
		if n, err := repo.CountLive(ctx, otherID, "reset_password", now); err != nil || n != 1 {
			t.Fatalf("other user's live reset links = %d, %v; want 1", n, err)
		}
		if err := consume(t, 4, "reset_password", now); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("deleted reset link: err %v, want ErrNotFound", err)
		}
		if err := consume(t, 6, "verify_email", now); err != nil {
			t.Fatalf("deleting reset links removed a verify link: %v", err)
		}
	})

	t.Run("account deletion removes every link", func(t *testing.T) {
		create(t, userID, 7, "verify_email", now.Add(time.Hour))
		create(t, userID, 8, "reset_password", now.Add(time.Hour))
		err := NewTxRunner(pool).InTx(ctx, func(ctx context.Context) error {
			return NewUsers(pool).DeletePersonalData(ctx, userID)
		})
		if err != nil {
			t.Fatal(err)
		}
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM email_token WHERE user_id = $1`, userID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("%d email tokens survived account deletion", n)
		}
	})
}

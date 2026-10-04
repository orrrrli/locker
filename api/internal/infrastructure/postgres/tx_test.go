package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool connects to TEST_DATABASE_URL and creates a fresh table for the test.
// It fails instead of skipping, so a missing database never hides as a pass.
func testPool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Fatal("TEST_DATABASE_URL is not set; start a Postgres and point it there")
	}
	ctx := context.Background()
	pool, err := NewPool(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	table := fmt.Sprintf("tx_test_%d", time.Now().UnixNano())
	if _, err := pool.Exec(ctx, "CREATE TABLE "+table+" (v int)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DROP TABLE "+table)
		pool.Close()
	})
	return pool, table
}

func count(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func insert(ctx context.Context, pool *pgxpool.Pool, table string) error {
	_, err := Conn(ctx, pool).Exec(ctx, "INSERT INTO "+table+" VALUES (1)")
	return err
}

func TestInTxCommits(t *testing.T) {
	pool, table := testPool(t)
	err := NewTxRunner(pool).InTx(context.Background(), func(ctx context.Context) error {
		return insert(ctx, pool, table)
	})
	if err != nil {
		t.Fatal(err)
	}
	if n := count(t, pool, table); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestInTxRollsBackOnError(t *testing.T) {
	pool, table := testPool(t)
	boom := errors.New("boom")
	err := NewTxRunner(pool).InTx(context.Background(), func(ctx context.Context) error {
		if err := insert(ctx, pool, table); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if n := count(t, pool, table); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
}

func TestInTxRollsBackOnPanic(t *testing.T) {
	pool, table := testPool(t)
	func() {
		defer func() { _ = recover() }()
		_ = NewTxRunner(pool).InTx(context.Background(), func(ctx context.Context) error {
			if err := insert(ctx, pool, table); err != nil {
				return err
			}
			panic("boom")
		})
	}()
	if n := count(t, pool, table); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
}

func TestNestedInTxJoinsOuterTransaction(t *testing.T) {
	pool, table := testPool(t)
	runner := NewTxRunner(pool)
	boom := errors.New("boom")
	err := runner.InTx(context.Background(), func(ctx context.Context) error {
		if err := insert(ctx, pool, table); err != nil {
			return err
		}
		if err := runner.InTx(ctx, func(ctx context.Context) error { return insert(ctx, pool, table) }); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
	if n := count(t, pool, table); n != 0 {
		t.Fatalf("rows = %d, want 0: the inner InTx did not join the outer transaction", n)
	}
}

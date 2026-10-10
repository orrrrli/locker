package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/testdb/emptydb"
)

// testPool gives the test its own empty, unmigrated database (no state from
// other tests or earlier runs) with one scratch table. Tests that need the
// schema call Migrate themselves.
func testPool(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	pool, err := NewPool(ctx, emptydb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	const table = "tx_test"
	if _, err := pool.Exec(ctx, "CREATE TABLE "+table+" (v int)"); err != nil {
		t.Fatal(err)
	}
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

// TestInTxPinsReadCommitted: even when the database defaults to REPEATABLE
// READ, InTx runs at READ COMMITTED, which the lock-then-read pattern needs.
func TestInTxPinsReadCommitted(t *testing.T) {
	ctx := context.Background()
	dsn := emptydb.New(t)
	setup, err := NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(ctx, `DO $$ BEGIN
		EXECUTE format('ALTER DATABASE %I SET default_transaction_isolation = %L', current_database(), 'repeatable read');
	END $$`); err != nil {
		t.Fatal(err)
	}
	setup.Close()

	// A new pool, so its sessions start with the new default.
	pool, err := NewPool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var def string
	if err := pool.QueryRow(ctx, "SHOW default_transaction_isolation").Scan(&def); err != nil {
		t.Fatal(err)
	}
	if def != "repeatable read" {
		t.Fatalf("setup: default = %q, want repeatable read", def)
	}

	var got string
	err = NewTxRunner(pool).InTx(ctx, func(ctx context.Context) error {
		return Conn(ctx, pool).QueryRow(ctx, "SHOW transaction_isolation").Scan(&got)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "read committed" {
		t.Fatalf("isolation = %q, want read committed", got)
	}
}

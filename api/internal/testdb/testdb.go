// Package testdb gives integration tests their own migrated Postgres
// database, so packages that run in parallel never share state.
package testdb

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
	"github.com/orrrrli/locker/api/internal/testdb/emptydb"
)

// New creates a fresh database on the server in TEST_DATABASE_URL, applies
// every migration and drops it when the test ends. It fails instead of
// skipping, so a missing database never hides as a pass.
func New(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	pool, err := postgres.NewPool(ctx, emptydb.New(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

// TeamLocked reports whether another transaction holds the team row lock
// that admin writes take (LockTeamForAdminChange). It tries the same lock
// with NOWAIT on a separate connection, so it never blocks. Call it from
// inside a repository write to prove the write runs under that lock.
func TeamLocked(t *testing.T, pool *pgxpool.Pool, teamID int64) bool {
	t.Helper()
	_, err := pool.Exec(context.Background(), `SELECT 1 FROM team WHERE id = $1 FOR NO KEY UPDATE NOWAIT`, teamID)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == lockNotAvailable {
		return true
	}
	if err != nil {
		t.Fatal(err)
	}
	return false
}

const lockNotAvailable = "55P03"

// Package testdb gives integration tests their own migrated Postgres
// database, so packages that run in parallel never share state.
package testdb

import (
	"context"
	"testing"

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

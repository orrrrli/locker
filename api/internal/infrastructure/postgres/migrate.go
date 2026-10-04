package postgres

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/orrrrli/locker/api/sql/migrations"
)

// Migrate applies every pending embedded migration. It runs before the API
// serves requests, so handlers always see the schema they were built for.
//
// ponytail: no advisory lock, the API runs as a single instance; add
// goose.WithSessionLocker if it ever runs more than one replica.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	db := stdlib.OpenDBFromPool(pool) // closing db leaves the pool open
	defer db.Close()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return fmt.Errorf("postgres: migrations: %w", err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("postgres: migrate: %w", err)
	}
	for _, r := range results {
		slog.Info("migration applied", "version", r.Source.Version, "duration", r.Duration)
	}
	return nil
}

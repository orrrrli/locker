package postgres

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/orrrrli/locker/api/sql/migrations"
)

func TestMigrateAppliesAllAndIsIdempotent(t *testing.T) {
	pool, _ := testPool(t)
	ctx := context.Background()

	for i := range 2 {
		if err := Migrate(ctx, pool); err != nil {
			t.Fatalf("run %d: %v", i+1, err)
		}
	}

	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := provider.HasPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if pending {
		t.Fatal("migrations still pending after Migrate")
	}
}

package postgres

import (
	"context"
	"testing"

	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

func TestSqlcQueriesRunOnPool(t *testing.T) {
	pool, _ := testPool(t)
	got, err := sqlcdb.New(pool).Ping(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("Ping = %d, want 1", got)
	}
}

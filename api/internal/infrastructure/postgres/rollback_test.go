package postgres

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/orrrrli/locker/api/sql/migrations"
)

// TestEveryMigrationDownUndoesItsUp walks the migrations one at a time: at
// version N it records the schema, applies N+1, rolls it back and records the
// schema again. Any difference means that Down is incomplete. Migrating all
// the way down and up again (TestMigrationsRollBackAndReapply) cannot catch
// that: an older Down drops the whole table, hiding a no-op Down on a later
// ALTER (#365). New migrations are covered without new test code.
func TestEveryMigrationDownUndoesItsUp(t *testing.T) {
	pool, _ := testPool(t)
	ctx := context.Background()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	for _, src := range provider.ListSources() {
		before := schemaSnapshot(t, pool)
		if _, err := provider.UpByOne(ctx); err != nil {
			t.Fatalf("up to %d: %v", src.Version, err)
		}
		if _, err := provider.Down(ctx); err != nil {
			t.Fatalf("down from %d: %v", src.Version, err)
		}
		after := schemaSnapshot(t, pool)
		if diff := diffLines(before, after); diff != "" {
			t.Errorf("Down of migration %d leaves the schema different from before its Up:\n%s", src.Version, diff)
		}
		if _, err := provider.UpByOne(ctx); err != nil {
			t.Fatalf("up to %d again: %v", src.Version, err)
		}
	}
	if version, err := provider.GetDBVersion(ctx); err != nil {
		t.Fatal(err)
	} else if want := provider.ListSources()[len(provider.ListSources())-1].Version; version != want {
		t.Fatalf("ended at version %d, want %d", version, want)
	}
}

// schemaSnapshot lists every relation, column, constraint, index, trigger and
// function in the public schema, one line each, sorted. The goose version
// table and the tx_test table that testPool creates are left out.
func schemaSnapshot(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	const skip = `c.relname NOT LIKE 'goose_db_version%' AND c.relname <> 'tx_test'`
	queries := []string{
		`SELECT 'relation ' || c.relname || ' ' || c.relkind::text
		 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm', 'S') AND ` + skip,
		`SELECT 'column ' || c.relname || '.' || a.attname || ' ' || format_type(a.atttypid, a.atttypmod)
		     || CASE WHEN a.attnotnull THEN ' NOT NULL' ELSE '' END
		     || CASE WHEN a.attidentity <> '' THEN ' IDENTITY ' || a.attidentity::text ELSE '' END
		     || CASE WHEN a.attgenerated <> '' THEN ' GENERATED ' || a.attgenerated::text ELSE '' END
		     || COALESCE(' DEFAULT ' || pg_get_expr(d.adbin, d.adrelid), '')
		 FROM pg_attribute a
		 JOIN pg_class c ON c.oid = a.attrelid
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p', 'v', 'm') AND a.attnum > 0 AND NOT a.attisdropped AND ` + skip,
		`SELECT 'constraint ' || c.relname || '.' || con.conname || ' ' || pg_get_constraintdef(con.oid)
		 FROM pg_constraint con
		 JOIN pg_class c ON c.oid = con.conrelid
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND ` + skip,
		`SELECT 'index ' || i.indexdef
		 FROM pg_indexes i JOIN pg_class c ON c.relname = i.tablename
		 JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = i.schemaname
		 WHERE i.schemaname = 'public' AND ` + skip,
		`SELECT 'trigger ' || pg_get_triggerdef(tg.oid)
		 FROM pg_trigger tg
		 JOIN pg_class c ON c.oid = tg.tgrelid
		 JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE n.nspname = 'public' AND NOT tg.tgisinternal AND ` + skip,
		`SELECT 'function ' || pg_get_functiondef(p.oid)
		 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		 WHERE n.nspname = 'public'`,
	}
	var lines []string
	ctx := context.Background()
	for _, q := range queries {
		rows, err := pool.Query(ctx, q)
		if err != nil {
			t.Fatalf("snapshot: %v\n%s", err, q)
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			lines = append(lines, line)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	slices.Sort(lines)
	return lines
}

// diffLines reports the lines only in before (prefixed "- ") and only in
// after (prefixed "+ "). Both inputs are sorted.
func diffLines(before, after []string) string {
	var b strings.Builder
	i, j := 0, 0
	for i < len(before) || j < len(after) {
		switch {
		case j == len(after) || (i < len(before) && before[i] < after[j]):
			fmt.Fprintf(&b, "- %s\n", before[i])
			i++
		case i == len(before) || after[j] < before[i]:
			fmt.Fprintf(&b, "+ %s\n", after[j])
			j++
		default:
			i++
			j++
		}
	}
	return b.String()
}

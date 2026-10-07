package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/sql/migrations"
)

const (
	uniqueViolation = "23505"
	checkViolation  = "23514"
)

var phase1Tables = []string{
	"user", "auth_identity", "session", "team", "membership", "invite", "match", "rsvp",
	"attendance", "notification", "notification_recipient", "device_token", "charge", "charge_member",
}

func TestSchemaHasEveryPhase1Table(t *testing.T) {
	pool, _ := testPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, table := range phase1Tables {
		var exists bool
		err := pool.QueryRow(ctx, "SELECT to_regclass($1) IS NOT NULL", `public."`+table+`"`).Scan(&exists)
		if err != nil {
			t.Fatal(err)
		}
		if !exists {
			t.Errorf("table %q does not exist", table)
		}
	}
}

// TestDomainEnumsMatchCheckConstraints keeps the domain constants and the
// CHECK constraints from drifting apart.
func TestDomainEnumsMatchCheckConstraints(t *testing.T) {
	pool, _ := testPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	enums := []struct {
		table, column string
		values        []string
	}{
		{"auth_identity", "provider", []string{string(domain.ProviderApple), string(domain.ProviderPassword)}},
		{"membership", "role", []string{string(domain.RoleAdmin), string(domain.RolePlayer)}},
		{"membership", "status", []string{string(domain.MembershipPending), string(domain.MembershipActive), string(domain.MembershipLeft)}},
		{"rsvp", "answer", []string{string(domain.RSVPGoing), string(domain.RSVPNotGoing)}},
		{"notification", "kind", []string{string(domain.NotificationManual), string(domain.NotificationMatchCreated), string(domain.NotificationMatchChanged), string(domain.NotificationRSVPReminder)}},
		{"charge_member", "status", []string{string(domain.ChargePending), string(domain.ChargePaid)}},
	}
	for _, e := range enums {
		// The column's own CHECK is the one that lists every value with = ANY.
		var def string
		err := pool.QueryRow(ctx, `
			SELECT pg_get_constraintdef(oid) FROM pg_constraint
			WHERE conrelid = to_regclass($1) AND contype = 'c'
			  AND pg_get_constraintdef(oid) LIKE '%(' || $2 || ' = ANY%'`,
			e.table, e.column).Scan(&def)
		if err != nil {
			t.Fatalf("%s.%s: %v", e.table, e.column, err)
		}
		var want []string
		for _, v := range e.values {
			want = append(want, "'"+v+"'::text")
		}
		if !strings.Contains(def, "ARRAY["+strings.Join(want, ", ")+"]") {
			t.Errorf("%s.%s CHECK is %s, want exactly %v in this order", e.table, e.column, def, e.values)
		}
	}
}

func TestMigrationsRollBackAndReapply(t *testing.T) {
	pool, _ := testPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.DownTo(ctx, 0); err != nil {
		t.Fatalf("down: %v", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("up after down: %v", err)
	}
}

// fixture holds one row of each parent table, inserted inside a transaction
// that the test rolls back.
type fixture struct {
	user, team, admin, player, match, notification, charge int64
}

func seed(t *testing.T, ctx context.Context, tx pgx.Tx) fixture {
	t.Helper()
	var f fixture
	insert := func(dest *int64, sql string, args ...any) {
		if err := tx.QueryRow(ctx, sql, args...).Scan(dest); err != nil {
			t.Fatalf("seed %q: %v", sql, err)
		}
	}
	insert(&f.user, `INSERT INTO "user" (name, birth_date) VALUES ('Ana', '2000-01-01') RETURNING id`)
	insert(&f.team, `INSERT INTO team (name, timezone) VALUES ('Halcones', 'America/Mexico_City') RETURNING id`)
	insert(&f.admin, `INSERT INTO membership (team_id, user_id, role, status) VALUES ($1, $2, 'admin', 'active') RETURNING id`, f.team, f.user)
	insert(&f.player, `INSERT INTO membership (team_id, role, status) VALUES ($1, 'player', 'active') RETURNING id`, f.team)
	insert(&f.match, `INSERT INTO match (team_id, starts_at, rival_name, location, created_by) VALUES ($1, now(), 'Rival', 'Cancha 3', $2) RETURNING id`, f.team, f.admin)
	insert(&f.notification, `INSERT INTO notification (team_id, kind, title, body, sent_by) VALUES ($1, 'manual', 't', 'b', $2) RETURNING id`, f.team, f.admin)
	insert(&f.charge, `INSERT INTO charge (team_id, concept, amount_cents, created_by) VALUES ($1, 'Arbitraje', 5000, $2) RETURNING id`, f.team, f.admin)
	return f
}

// TestSchemaConstraints inserts a valid row, then a row the schema must
// reject, and checks the Postgres error code. Each case runs in a savepoint so
// one rejection does not abort the rest.
func TestSchemaConstraints(t *testing.T) {
	pool, _ := testPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	f := seed(t, ctx, tx)

	tests := []struct {
		name  string
		setup string // valid row; empty when the seed is enough
		bad   string
		args  []any
		code  string
	}{
		{"auth_identity UNIQUE (provider, subject)",
			`INSERT INTO auth_identity (user_id, provider, subject) VALUES ($1, 'apple', 's1')`,
			`INSERT INTO auth_identity (user_id, provider, subject) VALUES ($1, 'apple', 's1')`,
			[]any{f.user}, uniqueViolation},
		{"auth_identity provider CHECK", "",
			`INSERT INTO auth_identity (user_id, provider, subject) VALUES ($1, 'google', 's2')`,
			[]any{f.user}, checkViolation},
		{"auth_identity password needs a hash", "",
			`INSERT INTO auth_identity (user_id, provider, subject) VALUES ($1, 'password', 'a@b.c')`,
			[]any{f.user}, checkViolation},
		{"live user needs a birth date", "",
			`INSERT INTO "user" (name) VALUES ('Sin fecha')`,
			nil, checkViolation},
		{"membership UNIQUE (team_id, user_id)", "",
			`INSERT INTO membership (team_id, user_id, role, status) VALUES ($1, $2, 'player', 'pending')`,
			[]any{f.team, f.user}, uniqueViolation},
		{"membership role CHECK", "",
			`INSERT INTO membership (team_id, role, status) VALUES ($1, 'captain', 'active')`,
			[]any{f.team}, checkViolation},
		{"membership status CHECK", "",
			`INSERT INTO membership (team_id, role, status) VALUES ($1, 'player', 'banned')`,
			[]any{f.team}, checkViolation},
		{"rsvp PK (match_id, membership_id)",
			`INSERT INTO rsvp (match_id, membership_id, answer) VALUES ($1, $2, 'going')`,
			`INSERT INTO rsvp (match_id, membership_id, answer) VALUES ($1, $2, 'not_going')`,
			[]any{f.match, f.player}, uniqueViolation},
		{"rsvp answer CHECK", "",
			`INSERT INTO rsvp (match_id, membership_id, answer) VALUES ($1, $2, 'maybe')`,
			[]any{f.match, f.admin}, checkViolation},
		{"attendance PK (match_id, membership_id)",
			`INSERT INTO attendance (match_id, membership_id) VALUES ($1, $2)`,
			`INSERT INTO attendance (match_id, membership_id) VALUES ($1, $2)`,
			[]any{f.match, f.player}, uniqueViolation},
		{"notification kind CHECK", "",
			`INSERT INTO notification (team_id, kind, title, body) VALUES ($1, 'promo', 't', 'b')`,
			[]any{f.team}, checkViolation},
		{"manual notification needs a sender", "",
			`INSERT INTO notification (team_id, kind, title, body) VALUES ($1, 'manual', 't', 'b')`,
			[]any{f.team}, checkViolation},
		{"notification_recipient PK (notification_id, membership_id)",
			`INSERT INTO notification_recipient (notification_id, membership_id) VALUES ($1, $2)`,
			`INSERT INTO notification_recipient (notification_id, membership_id) VALUES ($1, $2)`,
			[]any{f.notification, f.player}, uniqueViolation},
		{"charge_member PK (charge_id, membership_id)",
			`INSERT INTO charge_member (charge_id, membership_id) VALUES ($1, $2)`,
			`INSERT INTO charge_member (charge_id, membership_id) VALUES ($1, $2)`,
			[]any{f.charge, f.player}, uniqueViolation},
		{"charge_member status CHECK", "",
			`INSERT INTO charge_member (charge_id, membership_id, status) VALUES ($1, $2, 'waived')`,
			[]any{f.charge, f.admin}, checkViolation},
		{"paid charge_member records who and when", "",
			`INSERT INTO charge_member (charge_id, membership_id, status) VALUES ($1, $2, 'paid')`,
			[]any{f.charge, f.admin}, checkViolation},
		{"charge amount must be positive", "",
			`INSERT INTO charge (team_id, concept, amount_cents, created_by) VALUES ($1, 'x', 0, $2)`,
			[]any{f.team, f.admin}, checkViolation},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sp, err := tx.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = sp.Rollback(ctx) }()
			if tt.setup != "" {
				if _, err := sp.Exec(ctx, tt.setup, tt.args...); err != nil {
					t.Fatalf("valid row rejected: %v", err)
				}
			}
			_, err = sp.Exec(ctx, tt.bad, tt.args...)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != tt.code {
				t.Fatalf("err = %v, want SQLSTATE %s", err, tt.code)
			}
		})
	}
}

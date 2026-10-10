package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/orrrrli/locker/api/internal/domain"
)

func TestMembershipByTeamAndUser(t *testing.T) {
	pool, _ := testPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// Run inside a rolled-back transaction, as use cases do: the repository
	// must work on the transaction carried by ctx.
	err := NewTxRunner(pool).InTx(ctx, func(ctx context.Context) error {
		db := Conn(ctx, pool)
		var userID, teamID, otherTeamID int64
		for _, q := range []struct {
			dest *int64
			sql  string
		}{
			{&userID, `INSERT INTO "user" (name, birth_date) VALUES ('Ana', '2000-01-01') RETURNING id`},
			{&teamID, `INSERT INTO team (name, timezone) VALUES ('Halcones', 'America/Mexico_City') RETURNING id`},
			{&otherTeamID, `INSERT INTO team (name, timezone) VALUES ('Pumas', 'America/Mexico_City') RETURNING id`},
		} {
			if err := db.QueryRow(ctx, q.sql).Scan(q.dest); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := db.Exec(ctx, `
			INSERT INTO membership (team_id, user_id, role, status, shirt_number, position)
			VALUES ($1, $2, 'admin', 'active', 10, 'delantero')`, teamID, userID); err != nil {
			t.Fatal(err)
		}

		repo := NewMemberships(pool)
		m, err := repo.MembershipByTeamAndUser(ctx, teamID, userID)
		if err != nil {
			t.Fatal(err)
		}
		if m.TeamID != teamID || m.UserID == nil || *m.UserID != userID ||
			m.Role != domain.RoleAdmin || m.Status != domain.MembershipActive ||
			m.ShirtNumber == nil || *m.ShirtNumber != 10 || m.Position == nil || *m.Position != "delantero" ||
			m.DisplayNameOverride != nil || m.CreatedAt.IsZero() {
			t.Fatalf("unexpected membership: %+v", m)
		}

		if _, err := repo.MembershipByTeamAndUser(ctx, otherTeamID, userID); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("other team: err = %v, want domain.ErrNotFound", err)
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatal(err)
	}
}

var errRollback = errors.New("rollback")

func TestLockTeamForAdminChange(t *testing.T) {
	pool, _ := testPool(t)
	ctx := context.Background()
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repo := NewMemberships(pool)

	// The team exists, so only the missing transaction can make it fail.
	var existing int64
	if err := pool.QueryRow(ctx, `INSERT INTO team (name, timezone) VALUES ('Pumas', 'America/Mexico_City') RETURNING id`).Scan(&existing); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.LockTeamForAdminChange(ctx, existing); err == nil {
		t.Fatal("outside a transaction: err = nil, want an error")
	}

	err := NewTxRunner(pool).InTx(ctx, func(ctx context.Context) error {
		db := Conn(ctx, pool)
		var teamID int64
		if err := db.QueryRow(ctx, `INSERT INTO team (name, timezone) VALUES ('Halcones', 'America/Mexico_City') RETURNING id`).Scan(&teamID); err != nil {
			t.Fatal(err)
		}
		// Only the two active admins count: not the player, not the pending
		// or left admins.
		for i, rs := range [][2]string{
			{"admin", "active"}, {"admin", "active"}, {"player", "active"},
			{"admin", "pending"}, {"admin", "left"},
		} {
			var userID int64
			if err := db.QueryRow(ctx, `INSERT INTO "user" (name, birth_date) VALUES ($1, '2000-01-01') RETURNING id`, fmt.Sprint("u", i)).Scan(&userID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(ctx, `INSERT INTO membership (team_id, user_id, role, status) VALUES ($1, $2, $3, $4)`, teamID, userID, rs[0], rs[1]); err != nil {
				t.Fatal(err)
			}
		}

		n, err := repo.LockTeamForAdminChange(ctx, teamID)
		if err != nil {
			t.Fatal(err)
		}
		if n != 2 {
			t.Fatalf("active admins = %d, want 2", n)
		}
		if _, err := repo.LockTeamForAdminChange(ctx, teamID+1); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("unknown team: err = %v, want domain.ErrNotFound", err)
		}
		return errRollback
	})
	if !errors.Is(err, errRollback) {
		t.Fatal(err)
	}
}

package memberships_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/memberships"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
	"github.com/orrrrli/locker/api/internal/testdb"
)

type fixture struct {
	svc  *memberships.Service
	pool *pgxpool.Pool
	n    int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.New(t)
	return &fixture{
		svc: memberships.NewService(memberships.Deps{
			Tx:          postgres.NewTxRunner(pool),
			Memberships: postgres.NewMemberships(pool),
		}),
		pool: pool,
	}
}

func (f *fixture) team(t *testing.T) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO team (name, timezone) VALUES ('Halcones', 'America/Tijuana') RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// member adds a new user to the team with the given role and status and
// returns the membership id.
func (f *fixture) member(t *testing.T, teamID int64, role domain.Role, status domain.MembershipStatus) int64 {
	t.Helper()
	ctx := context.Background()
	f.n++
	var userID, id int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO "user" (name, birth_date) VALUES ($1, '2000-01-01') RETURNING id`,
		fmt.Sprint("u", f.n)).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO membership (team_id, user_id, role, status) VALUES ($1, $2, $3, $4) RETURNING id`,
		teamID, userID, role, status).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) role(t *testing.T, id int64) domain.Role {
	t.Helper()
	var r string
	if err := f.pool.QueryRow(context.Background(), `SELECT role FROM membership WHERE id = $1`, id).Scan(&r); err != nil {
		t.Fatal(err)
	}
	return domain.Role(r)
}

func TestChangeRole(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	team := f.team(t)
	admin := f.member(t, team, domain.RoleAdmin, domain.MembershipActive)
	player := f.member(t, team, domain.RolePlayer, domain.MembershipActive)

	// Promote (R6.2): the team now has two admins (R6.3).
	m, err := f.svc.ChangeRole(ctx, team, admin, player, domain.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != player || m.Role != domain.RoleAdmin || m.Status != domain.MembershipActive {
		t.Fatalf("promoted = %+v", m)
	}

	// Demote while another admin remains.
	if _, err := f.svc.ChangeRole(ctx, team, admin, admin, domain.RolePlayer); err != nil {
		t.Fatal(err)
	}
	if got := f.role(t, admin); got != domain.RolePlayer {
		t.Fatalf("demoted role = %s, want player", got)
	}

	// The only admin left cannot be demoted (R6.4).
	if _, err := f.svc.ChangeRole(ctx, team, player, player, domain.RolePlayer); !errors.Is(err, domain.ErrLastAdmin) {
		t.Fatalf("demote last admin: err = %v, want domain.ErrLastAdmin", err)
	}
	if got := f.role(t, player); got != domain.RoleAdmin {
		t.Fatalf("last admin role = %s, want admin", got)
	}

	// Same role: no change, no error, even for the last admin.
	if m, err := f.svc.ChangeRole(ctx, team, player, player, domain.RoleAdmin); err != nil || m.Role != domain.RoleAdmin {
		t.Fatalf("same role: m = %+v, err = %v", m, err)
	}
}

func TestChangeRoleRejects(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	team, other := f.team(t), f.team(t)
	admin := f.member(t, team, domain.RoleAdmin, domain.MembershipActive)
	player := f.member(t, team, domain.RolePlayer, domain.MembershipActive)
	pending := f.member(t, team, domain.RolePlayer, domain.MembershipPending)
	left := f.member(t, team, domain.RoleAdmin, domain.MembershipLeft)
	elsewhere := f.member(t, other, domain.RolePlayer, domain.MembershipActive)

	for _, tc := range []struct {
		name   string
		team   int64
		caller int64
		id     int64
		role   domain.Role
		want   error
	}{
		{"invalid role", team, admin, pending, "captain", memberships.ErrInvalidRole},
		{"pending membership", team, admin, pending, domain.RoleAdmin, memberships.ErrNotActive},
		{"left membership", team, admin, left, domain.RolePlayer, memberships.ErrNotActive},
		{"membership of another team", team, admin, elsewhere, domain.RoleAdmin, domain.ErrNotFound},
		{"unknown membership", team, admin, 999999, domain.RoleAdmin, domain.ErrNotFound},
		{"unknown team", 999999, admin, elsewhere, domain.RoleAdmin, domain.ErrNotFound},
		{"caller is a player", team, player, player, domain.RoleAdmin, application.ErrForbidden},
		{"caller left the team", team, left, player, domain.RoleAdmin, application.ErrForbidden},
		{"caller from another team", team, elsewhere, player, domain.RoleAdmin, application.ErrForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.svc.ChangeRole(ctx, tc.team, tc.caller, tc.id, tc.role); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	if got := f.role(t, elsewhere); got != domain.RolePlayer {
		t.Fatalf("other team's membership role = %s, want player", got)
	}
}

// probedMemberships records whether the team lock is held while UpdateRole runs.
type probedMemberships struct {
	*postgres.Memberships
	t      *testing.T
	pool   *pgxpool.Pool
	teamID int64
	locked *bool
}

func (p probedMemberships) UpdateRole(ctx context.Context, id int64, role domain.Role) (domain.Membership, error) {
	*p.locked = testdb.TeamLocked(p.t, p.pool, p.teamID)
	return p.Memberships.UpdateRole(ctx, id, role)
}

// TestChangeRoleWritesUnderTheTeamLock: the admin count, the caller check and
// the role write share one transaction and one lock (R6.4).
func TestChangeRoleWritesUnderTheTeamLock(t *testing.T) {
	f := newFixture(t)
	team := f.team(t)
	admin := f.member(t, team, domain.RoleAdmin, domain.MembershipActive)
	player := f.member(t, team, domain.RolePlayer, domain.MembershipActive)
	var locked bool
	svc := memberships.NewService(memberships.Deps{
		Tx:          postgres.NewTxRunner(f.pool),
		Memberships: probedMemberships{Memberships: postgres.NewMemberships(f.pool), t: t, pool: f.pool, teamID: team, locked: &locked},
	})
	if _, err := svc.ChangeRole(context.Background(), team, admin, player, domain.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Fatal("UpdateRole ran without the team lock")
	}
}

package account

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
	"github.com/orrrrli/locker/api/internal/testdb"
)

type fixture struct {
	svc  *Service
	pool *pgxpool.Pool
	n    int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.New(t)
	return &fixture{
		svc: NewService(Deps{
			Tx:          postgres.NewTxRunner(pool),
			Teams:       postgres.NewTeams(pool),
			Memberships: postgres.NewMemberships(pool),
			Users:       postgres.NewUsers(pool),
		}),
		pool: pool,
	}
}

func (f *fixture) exec(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) user(t *testing.T) int64 {
	t.Helper()
	f.n++
	return f.exec(t, `INSERT INTO "user" (name, birth_date) VALUES ($1, '2000-01-01') RETURNING id`, fmt.Sprint("u", f.n))
}

func (f *fixture) team(t *testing.T, name string) int64 {
	t.Helper()
	return f.exec(t, `INSERT INTO team (name, timezone) VALUES ($1, 'America/Tijuana') RETURNING id`, name)
}

func (f *fixture) member(t *testing.T, teamID, userID int64, role domain.Role, status domain.MembershipStatus) int64 {
	t.Helper()
	return f.exec(t, `INSERT INTO membership (team_id, user_id, role, status, joined_at)
		VALUES ($1, $2, $3, $4, CASE WHEN $4 IN ('active', 'left') THEN now() END) RETURNING id`,
		teamID, userID, role, status)
}

func (f *fixture) check(t *testing.T, userID int64) error {
	t.Helper()
	return f.svc.tx.InTx(context.Background(), func(ctx context.Context) error {
		_, err := f.svc.checkLastAdmin(ctx, userID)
		return err
	})
}

// lockProbe records, for each team, whether its lock was held when the
// user's membership in it was read.
type lockProbe struct {
	*postgres.Memberships
	t      *testing.T
	pool   *pgxpool.Pool
	locked map[int64]bool
}

func (p lockProbe) MembershipByTeamAndUser(ctx context.Context, teamID, userID int64) (domain.Membership, error) {
	p.locked[teamID] = testdb.TeamLocked(p.t, p.pool, teamID)
	return p.Memberships.MembershipByTeamAndUser(ctx, teamID, userID)
}

// TestCheckLastAdmin: deletion is blocked by every team where the user is
// the only active admin, the error lists exactly those in id order (R5.4),
// and each of the user's teams is read under its lock, players' too (R6.4).
func TestCheckLastAdmin(t *testing.T) {
	f := newFixture(t)
	probe := lockProbe{Memberships: postgres.NewMemberships(f.pool), t: t, pool: f.pool, locked: map[int64]bool{}}
	f.svc.memberships = probe
	u, other := f.user(t), f.user(t)

	onlyAdmin := f.team(t, "Solo")
	f.member(t, onlyAdmin, u, domain.RoleAdmin, domain.MembershipActive)
	f.member(t, onlyAdmin, other, domain.RolePlayer, domain.MembershipActive)

	coAdmin := f.team(t, "Co")
	f.member(t, coAdmin, u, domain.RoleAdmin, domain.MembershipActive)
	f.member(t, coAdmin, other, domain.RoleAdmin, domain.MembershipActive)

	player := f.team(t, "Jugador")
	f.member(t, player, u, domain.RolePlayer, domain.MembershipActive)
	f.member(t, player, other, domain.RoleAdmin, domain.MembershipActive)

	// A left admin row is not the team's admin any more.
	left := f.team(t, "Ex")
	f.member(t, left, u, domain.RoleAdmin, domain.MembershipLeft)
	f.member(t, left, other, domain.RoleAdmin, domain.MembershipActive)

	alone := f.team(t, "Solo 2")
	f.member(t, alone, u, domain.RoleAdmin, domain.MembershipActive)

	err := f.check(t, u)
	var lae *LastAdminError
	if !errors.As(err, &lae) || !errors.Is(err, domain.ErrLastAdmin) {
		t.Fatalf("err = %v, want *LastAdminError wrapping ErrLastAdmin", err)
	}
	if len(lae.Teams) != 2 || lae.Teams[0].ID != onlyAdmin || lae.Teams[0].Name != "Solo" || lae.Teams[1].ID != alone {
		t.Fatalf("teams = %+v, want %d (Solo) then %d (Solo 2)", lae.Teams, onlyAdmin, alone)
	}
	for _, id := range []int64{onlyAdmin, coAdmin, player, alone} {
		if !probe.locked[id] {
			t.Errorf("team %d: membership read without the team lock", id)
		}
	}

	// other is a player in Solo and a co-admin in Co, but alone in Jugador
	// and Ex.
	if err := f.check(t, other); !errors.As(err, &lae) || len(lae.Teams) != 2 || lae.Teams[0].ID != player || lae.Teams[1].ID != left {
		t.Fatalf("other: err = %v, want %d (Jugador) then %d (Ex)", err, player, left)
	}

	// A second admin in each Solo team unblocks u.
	f.member(t, onlyAdmin, f.user(t), domain.RoleAdmin, domain.MembershipActive)
	f.member(t, alone, f.user(t), domain.RoleAdmin, domain.MembershipActive)
	if err := f.check(t, u); err != nil {
		t.Fatalf("after a second admin: err = %v, want nil", err)
	}
}

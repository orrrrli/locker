package memberships_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
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
	if err := f.pool.QueryRow(ctx, // joined_at as the app sets it: every active or left row has joined.
		`INSERT INTO membership (team_id, user_id, role, status, joined_at)
		 VALUES ($1, $2, $3, $4, CASE WHEN $4 IN ('active', 'left') THEN now() END) RETURNING id`,
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
		{"caller is a player", team, player, player, domain.RoleAdmin, application.ErrNotAdmin},
		{"caller left the team", team, left, player, domain.RoleAdmin, domain.ErrNotFound},
		// Status is checked before role, like requireRole: a pending player
		// is not found, not forbidden.
		{"caller is a pending player", team, pending, player, domain.RoleAdmin, domain.ErrNotFound},
		{"caller from another team", team, elsewhere, player, domain.RoleAdmin, domain.ErrNotFound},
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

func (f *fixture) status(t *testing.T, id int64) (domain.MembershipStatus, bool) {
	t.Helper()
	var s string
	err := f.pool.QueryRow(context.Background(), `SELECT status FROM membership WHERE id = $1`, id).Scan(&s)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return domain.MembershipStatus(s), true
}

func TestApprove(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	team := f.team(t)
	admin := f.member(t, team, domain.RoleAdmin, domain.MembershipActive)
	pending := f.member(t, team, domain.RolePlayer, domain.MembershipPending)

	m, err := f.svc.Approve(ctx, team, admin, pending)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != pending || m.Status != domain.MembershipActive || m.Role != domain.RolePlayer {
		t.Fatalf("approved = %+v, want active player (R7.3)", m)
	}

	var joined *time.Time
	if err := f.pool.QueryRow(ctx, `SELECT joined_at FROM membership WHERE id = $1`, pending).Scan(&joined); err != nil || joined == nil {
		t.Fatalf("approved: joined_at = %v, err = %v; want set", joined, err)
	}

	// A member who comes back keeps the date they first joined.
	returning := f.member(t, team, domain.RolePlayer, domain.MembershipLeft)
	first := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := f.pool.Exec(ctx, `UPDATE membership SET status = 'pending', joined_at = $2 WHERE id = $1`, returning, first); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Approve(ctx, team, admin, returning); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT joined_at FROM membership WHERE id = $1`, returning).Scan(&joined); err != nil || !joined.Equal(first) {
		t.Fatalf("returning member approved: joined_at = %v, err = %v; want %v", joined, err, first)
	}

	// Approval always grants player, whatever role the pending row carries.
	// No path creates a pending admin today (accept and rejoin both use
	// player); this guards the query, not a user flow.
	pendingAdmin := f.member(t, team, domain.RoleAdmin, domain.MembershipPending)
	m, err = f.svc.Approve(ctx, team, admin, pendingAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if m.Role != domain.RolePlayer {
		t.Fatalf("approved pending admin: role = %s, want player", m.Role)
	}
}

func TestReject(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	team := f.team(t)
	admin := f.member(t, team, domain.RoleAdmin, domain.MembershipActive)

	// A new member: the row is deleted, nothing is left behind (R13.6).
	fresh := f.member(t, team, domain.RolePlayer, domain.MembershipPending)
	if err := f.svc.Reject(ctx, team, admin, fresh); err != nil {
		t.Fatal(err)
	}
	if _, exists := f.status(t, fresh); exists {
		t.Fatal("rejected new member: row still exists")
	}

	// A member who left and came back, with a shirt number but no other
	// history pointing at the row: it goes back to left and keeps the row
	// (R8.4), so a later rejoin finds the same number.
	returning := f.member(t, team, domain.RolePlayer, domain.MembershipLeft)
	if _, err := f.pool.Exec(ctx, `UPDATE membership SET shirt_number = 10 WHERE id = $1`, returning); err != nil {
		t.Fatal(err)
	}
	if err := postgres.NewMemberships(f.pool).RejoinMembership(ctx, returning); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Reject(ctx, team, admin, returning); err != nil {
		t.Fatal(err)
	}
	var status string
	var shirt *int
	if err := f.pool.QueryRow(ctx, `SELECT status, shirt_number FROM membership WHERE id = $1`, returning).Scan(&status, &shirt); err != nil {
		t.Fatalf("rejected returning member: %v (row deleted?)", err)
	}
	if status != string(domain.MembershipLeft) || shirt == nil || *shirt != 10 {
		t.Fatalf("rejected returning member: status = %q, shirt = %v; want left, 10", status, shirt)
	}
}

func TestApproveRejectRefuse(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	team, other := f.team(t), f.team(t)
	admin := f.member(t, team, domain.RoleAdmin, domain.MembershipActive)
	player := f.member(t, team, domain.RolePlayer, domain.MembershipActive)
	left := f.member(t, team, domain.RoleAdmin, domain.MembershipLeft)
	pending := f.member(t, team, domain.RolePlayer, domain.MembershipPending)
	elsewhere := f.member(t, other, domain.RolePlayer, domain.MembershipPending)

	for _, tc := range []struct {
		name       string
		caller, id int64
		want       error
	}{
		{"active member", admin, player, memberships.ErrNotPending},
		{"left member", admin, left, memberships.ErrNotPending},
		{"pending of another team", admin, elsewhere, domain.ErrNotFound},
		{"unknown membership", admin, 999999, domain.ErrNotFound},
		{"caller is a player", player, pending, application.ErrNotAdmin},
		{"caller left the team", left, pending, domain.ErrNotFound},
		{"caller is pending", pending, pending, domain.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.svc.Approve(ctx, team, tc.caller, tc.id); !errors.Is(err, tc.want) {
				t.Fatalf("approve: err = %v, want %v", err, tc.want)
			}
			if err := f.svc.Reject(ctx, team, tc.caller, tc.id); !errors.Is(err, tc.want) {
				t.Fatalf("reject: err = %v, want %v", err, tc.want)
			}
		})
	}
	if s, _ := f.status(t, pending); s != domain.MembershipPending {
		t.Fatalf("pending status = %q, want pending: a refused call changed it", s)
	}
	if s, _ := f.status(t, elsewhere); s != domain.MembershipPending {
		t.Fatalf("other team's pending status = %q, want pending", s)
	}
}

func (p probedMemberships) ApprovePending(ctx context.Context, id int64) (domain.Membership, error) {
	*p.locked = testdb.TeamLocked(p.t, p.pool, p.teamID)
	return p.Memberships.ApprovePending(ctx, id)
}

func (p probedMemberships) RejectPending(ctx context.Context, id int64) error {
	*p.locked = testdb.TeamLocked(p.t, p.pool, p.teamID)
	return p.Memberships.RejectPending(ctx, id)
}

// TestApproveRejectWriteUnderTheTeamLock: the caller check and the write share
// one transaction and one lock.
func TestApproveRejectWriteUnderTheTeamLock(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	team := f.team(t)
	admin := f.member(t, team, domain.RoleAdmin, domain.MembershipActive)
	for _, op := range []string{"approve", "reject"} {
		t.Run(op, func(t *testing.T) {
			pending := f.member(t, team, domain.RolePlayer, domain.MembershipPending)
			var locked bool
			svc := memberships.NewService(memberships.Deps{
				Tx:          postgres.NewTxRunner(f.pool),
				Memberships: probedMemberships{Memberships: postgres.NewMemberships(f.pool), t: t, pool: f.pool, teamID: team, locked: &locked},
			})
			var err error
			if op == "approve" {
				_, err = svc.Approve(ctx, team, admin, pending)
			} else {
				err = svc.Reject(ctx, team, admin, pending)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !locked {
				t.Fatalf("%s wrote without the team lock", op)
			}
		})
	}
}

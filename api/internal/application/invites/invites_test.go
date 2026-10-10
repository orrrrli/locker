package invites_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/invites"
	"github.com/orrrrli/locker/api/internal/application/teams"
	"github.com/orrrrli/locker/api/internal/application/token"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
	"github.com/orrrrli/locker/api/internal/testdb"
)

var start = time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)

type fixture struct {
	pool    *pgxpool.Pool
	svc     *invites.Service
	now     *time.Time
	members *postgres.Memberships
	teamID  int64
	adminID int64 // the admin's membership id
	admin   int64 // the admin's user id
}

func setup(t *testing.T) fixture {
	t.Helper()
	ctx := context.Background()
	pool := testdb.New(t)
	now := start
	members := postgres.NewMemberships(pool)
	f := fixture{
		pool:    pool,
		now:     &now,
		members: members,
		svc: invites.NewService(invites.Deps{
			Tx:          postgres.NewTxRunner(pool),
			Invites:     postgres.NewInvites(pool),
			Memberships: members,
			// The trailing slash must not double up in the link.
			PublicBaseURL: "https://api.example.test/",
			Now:           func() time.Time { return now },
		}),
	}
	admin := newUser(t, pool, "admin@example.com")
	team, err := teams.NewService(teams.Deps{
		Tx: postgres.NewTxRunner(pool), Teams: postgres.NewTeams(pool), Memberships: members,
	}).Create(ctx, admin, "Pumas", "America/Tijuana")
	if err != nil {
		t.Fatal(err)
	}
	m, err := members.MembershipByTeamAndUser(ctx, team.ID, admin)
	if err != nil {
		t.Fatal(err)
	}
	f.teamID, f.adminID, f.admin = team.ID, m.ID, admin
	return f
}

func newUser(t *testing.T, pool *pgxpool.Pool, email string) int64 {
	t.Helper()
	id, err := postgres.NewUsers(pool).CreateUser(context.Background(), "Ana", email, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func (f fixture) invite(t *testing.T) invites.Created {
	t.Helper()
	inv, err := f.svc.Create(context.Background(), f.teamID, f.adminID)
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func TestCreateReturnsLinkAndStoresOnlyTheHash(t *testing.T) {
	f := setup(t)
	inv := f.invite(t)

	if inv.Token == "" || inv.URL != "https://api.example.test/i/"+inv.Token {
		t.Fatalf("invite = %+v", inv)
	}
	if !inv.ExpiresAt.Equal(start.Add(7 * 24 * time.Hour)) {
		t.Fatalf("expires_at = %v, want 7 days after %v (R7.1)", inv.ExpiresAt, start)
	}
	hash, ok := token.Hash(inv.Token)
	if !ok {
		t.Fatal("token does not decode")
	}
	var stored []byte
	if err := f.pool.QueryRow(context.Background(), "SELECT token_hash FROM invite WHERE id = $1", inv.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(hash) || strings.Contains(string(stored), inv.Token) {
		t.Fatal("invite row does not hold exactly the token's SHA-256")
	}
}

func TestAcceptCreatesPendingPlayer(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	user := newUser(t, f.pool, "player@example.com")
	inv := f.invite(t)

	got, err := f.svc.Accept(ctx, user, inv.Token)
	if err != nil {
		t.Fatal(err)
	}
	if got != (invites.Accepted{TeamID: f.teamID, TeamName: "Pumas", Status: domain.MembershipPending}) {
		t.Fatalf("accepted = %+v", got)
	}
	m, err := f.members.MembershipByTeamAndUser(ctx, f.teamID, user)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != domain.MembershipPending || m.Role != domain.RolePlayer {
		t.Fatalf("membership %s/%s, want pending/player (R7.2)", m.Status, m.Role)
	}

	// The same link again: already pending, nothing changes.
	var already *invites.AlreadyMemberError
	if _, err := f.svc.Accept(ctx, user, inv.Token); !errors.As(err, &already) || already.Status != domain.MembershipPending {
		t.Fatalf("second accept err = %v, want already member (pending)", err)
	}
}

func TestAcceptByActiveMemberIsRejected(t *testing.T) {
	f := setup(t)
	var already *invites.AlreadyMemberError
	if _, err := f.svc.Accept(context.Background(), f.admin, f.invite(t).Token); !errors.As(err, &already) || already.Status != domain.MembershipActive {
		t.Fatalf("err = %v, want already member (active)", err)
	}
}

func TestAcceptExpiresAfterSevenDays(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	inv := f.invite(t)

	*f.now = inv.ExpiresAt.Add(-time.Second)
	if _, err := f.svc.Accept(ctx, newUser(t, f.pool, "early@example.com"), inv.Token); err != nil {
		t.Fatalf("one second before expiry: %v", err)
	}
	*f.now = inv.ExpiresAt
	if _, err := f.svc.Accept(ctx, newUser(t, f.pool, "late@example.com"), inv.Token); !errors.Is(err, invites.ErrInviteExpired) {
		t.Fatalf("at expiry: err = %v, want ErrInviteExpired (R7.5)", err)
	}
}

func TestRevokedInviteIsRejected(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	inv := f.invite(t)

	if err := f.svc.Revoke(ctx, f.teamID, f.adminID, inv.ID); err != nil {
		t.Fatal(err)
	}
	*f.now = start.Add(time.Hour)
	if err := f.svc.Revoke(ctx, f.teamID, f.adminID, inv.ID); err != nil {
		t.Fatal(err)
	}
	var revokedAt time.Time
	if err := f.pool.QueryRow(ctx, "SELECT revoked_at FROM invite WHERE id = $1", inv.ID).Scan(&revokedAt); err != nil {
		t.Fatal(err)
	}
	if !revokedAt.Equal(start) {
		t.Fatalf("revoked_at = %v, want the first revocation %v", revokedAt, start)
	}

	user := newUser(t, f.pool, "player@example.com")
	if _, err := f.svc.Accept(ctx, user, inv.Token); !errors.Is(err, invites.ErrInviteRevoked) {
		t.Fatalf("err = %v, want ErrInviteRevoked (R7.5)", err)
	}
	if _, err := f.members.MembershipByTeamAndUser(ctx, f.teamID, user); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a rejected accept left a membership: %v", err)
	}
}

func TestUnknownOrMalformedTokenIsNotFound(t *testing.T) {
	f := setup(t)
	user := newUser(t, f.pool, "player@example.com")
	unknown, _, err := token.New()
	if err != nil {
		t.Fatal(err)
	}
	for _, tok := range []string{"", "not-a-token", unknown} {
		if _, err := f.svc.Accept(context.Background(), user, tok); !errors.Is(err, invites.ErrInviteNotFound) {
			t.Fatalf("%q: err = %v, want ErrInviteNotFound", tok, err)
		}
	}
}

// A member who left gets the same row back as pending, with their number and
// position (R8.4).
func TestAcceptAfterLeavingReusesMembership(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	user := newUser(t, f.pool, "back@example.com")
	var oldID int64
	if err := f.pool.QueryRow(ctx, `
		INSERT INTO membership (team_id, user_id, role, status, shirt_number, position)
		VALUES ($1, $2, 'admin', 'left', 10, 'delantero') RETURNING id`, f.teamID, user).Scan(&oldID); err != nil {
		t.Fatal(err)
	}

	if _, err := f.svc.Accept(ctx, user, f.invite(t).Token); err != nil {
		t.Fatal(err)
	}
	m, err := f.members.MembershipByTeamAndUser(ctx, f.teamID, user)
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != oldID || m.Status != domain.MembershipPending || m.Role != domain.RolePlayer ||
		m.ShirtNumber == nil || *m.ShirtNumber != 10 || m.Position == nil || *m.Position != "delantero" {
		t.Fatalf("membership = %+v, want row %d back as pending player with number 10", m, oldID)
	}
}

// Revoke only touches the given team's invites: an id from another team, or
// one that does not exist, is not found and changes nothing.
func TestRevokeIsScopedToTheTeam(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	inv := f.invite(t)
	otherTeam, err := teams.NewService(teams.Deps{
		Tx: postgres.NewTxRunner(f.pool), Teams: postgres.NewTeams(f.pool), Memberships: f.members,
	}).Create(ctx, f.admin, "Halcones", "America/Tijuana")
	if err != nil {
		t.Fatal(err)
	}
	// The same user is admin of both teams, so only the invite's team differs.
	otherAdmin, err := f.members.MembershipByTeamAndUser(ctx, otherTeam.ID, f.admin)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name                   string
		teamID, caller, invite int64
	}{
		{"invite of another team", otherTeam.ID, otherAdmin.ID, inv.ID},
		{"unknown invite", f.teamID, f.adminID, inv.ID + 1000},
	} {
		if err := f.svc.Revoke(ctx, tc.teamID, tc.caller, tc.invite); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("%s: err = %v, want domain.ErrNotFound", tc.name, err)
		}
	}
	var revoked bool
	if err := f.pool.QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM invite WHERE id = $1", inv.ID).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if revoked {
		t.Fatal("a revoke scoped to another team revoked the invite")
	}
}

// staleMembers answers the first membership read with a stale value, as if a
// concurrent accept or approval committed right after it.
type staleMembers struct {
	*postgres.Memberships
	stale *domain.Membership // nil: the first read says there is no membership
	read  bool
}

func (s *staleMembers) MembershipByTeamAndUser(ctx context.Context, teamID, userID int64) (domain.Membership, error) {
	if !s.read {
		s.read = true
		if s.stale == nil {
			return domain.Membership{}, domain.ErrNotFound
		}
		return *s.stale, nil
	}
	return s.Memberships.MembershipByTeamAndUser(ctx, teamID, userID)
}

func (f fixture) withMembers(m invites.Memberships) *invites.Service {
	return invites.NewService(invites.Deps{
		Tx: postgres.NewTxRunner(f.pool), Invites: postgres.NewInvites(f.pool), Memberships: m,
		PublicBaseURL: "https://api.example.test", Now: func() time.Time { return *f.now },
	})
}

// Two accepts by the same user race past the membership check: the unique
// constraint stops the second insert, and it reads as "already pending".
func TestAcceptLosingInsertRaceIsAlreadyMember(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	user := newUser(t, f.pool, "player@example.com")
	if _, err := f.members.CreateMembership(ctx, f.teamID, user, domain.RolePlayer, domain.MembershipPending); err != nil {
		t.Fatal(err)
	}
	svc := f.withMembers(&staleMembers{Memberships: f.members})
	var already *invites.AlreadyMemberError
	if _, err := svc.Accept(ctx, user, f.invite(t).Token); !errors.As(err, &already) || already.Status != domain.MembershipPending {
		t.Fatalf("err = %v, want already member (pending)", err)
	}
}

// The accept read the row as `left`, but an admin approved it before the
// rejoin wrote: the rejoin must not demote it, and reports it as active.
func TestAcceptNeverDemotesARowThatStoppedBeingLeft(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	stale, err := f.members.MembershipByTeamAndUser(ctx, f.teamID, f.admin)
	if err != nil {
		t.Fatal(err)
	}
	stale.Status = domain.MembershipLeft
	svc := f.withMembers(&staleMembers{Memberships: f.members, stale: &stale})

	var already *invites.AlreadyMemberError
	if _, err := svc.Accept(ctx, f.admin, f.invite(t).Token); !errors.As(err, &already) || already.Status != domain.MembershipActive {
		t.Fatalf("err = %v, want already member (active)", err)
	}
	m, err := f.members.MembershipByTeamAndUser(ctx, f.teamID, f.admin)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != domain.MembershipActive || m.Role != domain.RoleAdmin {
		t.Fatalf("membership became %s/%s, want active/admin", m.Status, m.Role)
	}
}

// TestAdminActionsRecheckTheCaller: Create and Revoke check the caller again
// under the team lock, so a player, or an admin who has since been demoted
// or left, cannot create or revoke an invite.
func TestAdminActionsRecheckTheCaller(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	inv := f.invite(t)
	for _, tc := range []struct{ name, role, status string }{
		{"player", "player", "active"},
		{"admin who left", "admin", "left"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			user := newUser(t, f.pool, tc.name+"@example.com")
			var caller int64
			if err := f.pool.QueryRow(ctx,
				`INSERT INTO membership (team_id, user_id, role, status) VALUES ($1, $2, $3, $4) RETURNING id`,
				f.teamID, user, tc.role, tc.status).Scan(&caller); err != nil {
				t.Fatal(err)
			}
			if _, err := f.svc.Create(ctx, f.teamID, caller); !errors.Is(err, application.ErrForbidden) {
				t.Fatalf("create: err = %v, want application.ErrForbidden", err)
			}
			if err := f.svc.Revoke(ctx, f.teamID, caller, inv.ID); !errors.Is(err, application.ErrForbidden) {
				t.Fatalf("revoke: err = %v, want application.ErrForbidden", err)
			}
		})
	}
	var invites int
	var revoked bool
	if err := f.pool.QueryRow(ctx, `SELECT count(*), bool_or(revoked_at IS NOT NULL) FROM invite WHERE team_id = $1`, f.teamID).Scan(&invites, &revoked); err != nil {
		t.Fatal(err)
	}
	if invites != 1 || revoked {
		t.Fatalf("invites = %d, revoked = %v; want 1 untouched invite", invites, revoked)
	}
}

// probedInvites records whether the team lock is held while each write runs.
type probedInvites struct {
	*postgres.Invites
	t      *testing.T
	pool   *pgxpool.Pool
	locked map[string]bool
}

func (p probedInvites) CreateInvite(ctx context.Context, teamID int64, tokenHash []byte, createdBy int64, expiresAt time.Time) (int64, error) {
	p.locked["create"] = testdb.TeamLocked(p.t, p.pool, teamID)
	return p.Invites.CreateInvite(ctx, teamID, tokenHash, createdBy, expiresAt)
}

func (p probedInvites) RevokeInvite(ctx context.Context, teamID, id int64, now time.Time) error {
	p.locked["revoke"] = testdb.TeamLocked(p.t, p.pool, teamID)
	return p.Invites.RevokeInvite(ctx, teamID, id, now)
}

// TestAdminWritesRunUnderTheTeamLock: the caller check and each write share
// one transaction and one lock, so a demotion cannot commit between them.
func TestAdminWritesRunUnderTheTeamLock(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	locked := map[string]bool{}
	svc := invites.NewService(invites.Deps{
		Tx:            postgres.NewTxRunner(f.pool),
		Invites:       probedInvites{Invites: postgres.NewInvites(f.pool), t: t, pool: f.pool, locked: locked},
		Memberships:   f.members,
		PublicBaseURL: "https://api.example.test",
	})
	inv, err := svc.Create(ctx, f.teamID, f.adminID)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Revoke(ctx, f.teamID, f.adminID, inv.ID); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"create", "revoke"} {
		if !locked[op] {
			t.Errorf("%s ran without the team lock", op)
		}
	}
}

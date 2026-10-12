package invites_test

import (
	"context"
	"errors"
	"testing"

	"github.com/orrrrli/locker/api/internal/application/invites"
	"github.com/orrrrli/locker/api/internal/application/teams"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
	"github.com/orrrrli/locker/api/internal/testdb"
)

func TestRequestToJoinCreatesPendingPlayer(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	user := newUser(t, f.pool, "player@example.com")

	got, created, err := f.svc.RequestToJoin(ctx, f.teamID, user)
	if err != nil {
		t.Fatal(err)
	}
	if !created || got != (invites.Accepted{TeamID: f.teamID, TeamName: "Pumas", Status: domain.MembershipPending}) {
		t.Fatalf("got %+v, created %v", got, created)
	}
	m, err := f.members.MembershipByTeamAndUser(ctx, f.teamID, user)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != domain.MembershipPending || m.Role != domain.RolePlayer {
		t.Fatalf("membership %s/%s, want pending/player (R13.1)", m.Status, m.Role)
	}
}

// A dissolved team takes no join requests (R6.10), and answers like an
// unknown one.
func TestRequestToJoinDissolvedOrUnknownTeamIsNotFound(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	user := newUser(t, f.pool, "player@example.com")

	if _, _, err := f.svc.RequestToJoin(ctx, f.teamID+1000, user); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown team: err = %v, want ErrNotFound", err)
	}
	if err := teams.NewService(teams.Deps{
		Tx: postgres.NewTxRunner(f.pool), Teams: postgres.NewTeams(f.pool), Memberships: f.members,
	}).Dissolve(ctx, f.teamID, f.adminID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.RequestToJoin(ctx, f.teamID, user); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("dissolved team: err = %v, want ErrNotFound", err)
	}
	if _, err := f.members.MembershipByTeamAndUser(ctx, f.teamID, user); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("membership after request to a dissolved team: err = %v, want ErrNotFound", err)
	}
}

// probedMembers records whether the team lock is held while the join request
// reads dissolved_at and writes the membership.
type probedMembers struct {
	*postgres.Memberships
	t      *testing.T
	f      fixture
	locked map[string]bool
}

func (p probedMembers) TeamForJoin(ctx context.Context, teamID int64) (string, bool, error) {
	p.locked["read"] = testdb.TeamLocked(p.t, p.f.pool, teamID)
	return p.Memberships.TeamForJoin(ctx, teamID)
}

func (p probedMembers) CreateMembership(ctx context.Context, teamID, userID int64, role domain.Role, status domain.MembershipStatus) (int64, error) {
	p.locked["create"] = testdb.TeamLocked(p.t, p.f.pool, teamID)
	return p.Memberships.CreateMembership(ctx, teamID, userID, role, status)
}

// TestRequestToJoinRunsUnderTheTeamLock: the dissolve holds the team lock
// until commit, so reading dissolved_at and writing under the same lock
// leaves no window for a dissolve to miss the new pending row (R6.8, R6.10).
func TestRequestToJoinRunsUnderTheTeamLock(t *testing.T) {
	f := setup(t)
	locked := map[string]bool{}
	svc := f.withMembers(probedMembers{Memberships: f.members, t: t, f: f, locked: locked})
	if _, _, err := svc.RequestToJoin(context.Background(), f.teamID, newUser(t, f.pool, "player@example.com")); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"read", "create"} {
		if !locked[op] {
			t.Errorf("%s ran without the team lock", op)
		}
	}
}

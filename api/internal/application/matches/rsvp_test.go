package matches_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/matches"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
)

func playerOf(t *testing.T, f fixture) domain.Membership {
	t.Helper()
	id := addMember(t, f, "player@example.com", "player", "active")
	m, err := postgres.NewMemberships(f.pool).MembershipByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func storedAnswer(t *testing.T, f fixture, matchID, membershipID int64) string {
	t.Helper()
	var answer string
	err := f.pool.QueryRow(context.Background(), `SELECT answer FROM rsvp WHERE match_id = $1 AND membership_id = $2`, matchID, membershipID).Scan(&answer)
	if err != nil {
		t.Fatal(err)
	}
	return answer
}

// A member answers and changes the answer while the match has not started
// (R9.4); from kickoff on, both a first answer and a change are rejected
// and nothing changes (R9.5).
func TestAnswerChangesUntilKickoffThenLocks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.svc.Create(ctx, f.team, f.admin, kickoff, "Halcones", "Cancha 3")
	if err != nil {
		t.Fatal(err)
	}
	player := playerOf(t, f)

	a, err := f.svc.Answer(ctx, player, m.ID, domain.RSVPGoing)
	if err != nil {
		t.Fatal(err)
	}
	if a.MatchID != m.ID || a.MembershipID != player.ID || a.Answer != domain.RSVPGoing || a.UpdatedAt.IsZero() {
		t.Fatalf("rsvp = %+v", a)
	}
	*f.now = kickoff.Add(-time.Second)
	if _, err := f.svc.Answer(ctx, player, m.ID, domain.RSVPNotGoing); err != nil {
		t.Fatalf("change one second before kickoff: %v", err)
	}
	if got := storedAnswer(t, f, m.ID, player.ID); got != "not_going" {
		t.Fatalf("answer = %s, want not_going", got)
	}

	for _, at := range []time.Time{kickoff, kickoff.Add(time.Hour)} {
		*f.now = at
		if _, err := f.svc.Answer(ctx, player, m.ID, domain.RSVPGoing); !errors.Is(err, matches.ErrMatchStarted) {
			t.Fatalf("at %v: err = %v, want ErrMatchStarted", at, err)
		}
		// A member with no answer yet is locked out too.
		if _, err := f.svc.Answer(ctx, domain.Membership{ID: f.admin, TeamID: f.team}, m.ID, domain.RSVPGoing); !errors.Is(err, matches.ErrMatchStarted) {
			t.Fatalf("first answer at %v: err = %v, want ErrMatchStarted", at, err)
		}
	}
	if got := storedAnswer(t, f, m.ID, player.ID); got != "not_going" {
		t.Fatalf("answer after kickoff = %s, want not_going unchanged", got)
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM rsvp`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rsvp rows = %d, want 1", n)
	}
}

func TestAnswerRejectsInvalidInput(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.svc.Create(ctx, f.team, f.admin, kickoff, "Halcones", "Cancha 3")
	if err != nil {
		t.Fatal(err)
	}
	player := playerOf(t, f)

	// Only going or not_going (R9.3).
	for _, bad := range []domain.RSVPAnswer{"", "maybe", "GOING"} {
		if _, err := f.svc.Answer(ctx, player, m.ID, bad); !errors.Is(err, matches.ErrInvalidAnswer) {
			t.Fatalf("%q: err = %v, want ErrInvalidAnswer", bad, err)
		}
	}
	// A match of another team is not found, even with a valid answer.
	other := domain.Membership{ID: player.ID, TeamID: f.team + 1}
	if _, err := f.svc.Answer(ctx, other, m.ID, domain.RSVPGoing); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("other team: err = %v, want ErrNotFound", err)
	}
	if _, err := f.svc.Answer(ctx, player, m.ID+1, domain.RSVPGoing); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown match: err = %v, want ErrNotFound", err)
	}
}

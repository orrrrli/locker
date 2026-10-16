package matches_test

import (
	"context"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

// userOf returns the user id behind a membership.
func userOf(t *testing.T, f fixture, membershipID int64) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(), `SELECT user_id FROM membership WHERE id = $1`, membershipID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// otherTeam creates a second team with the user in it, in the role and
// status given, and returns the team id.
func otherTeam(t *testing.T, f fixture, user int64, role, status string) int64 {
	t.Helper()
	ctx := context.Background()
	var team int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO team (name, timezone) VALUES ('Tigres', 'America/Tijuana') RETURNING id`).Scan(&team); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO membership (team_id, user_id, role, status) VALUES ($1, $2, $3, $4)`,
		team, user, role, status); err != nil {
		t.Fatal(err)
	}
	return team
}

// insertMatch inserts a match directly, so any team can get one without its admin.
func insertMatch(t *testing.T, f fixture, team int64, startsAt time.Time, rival string) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO match (team_id, starts_at, rival_name, location, created_by)
		 VALUES ($1, $2, $3, 'Cancha 3', (SELECT id FROM membership WHERE team_id = $1 LIMIT 1)) RETURNING id`,
		team, startsAt, rival).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// Matches from every active team come soonest first, each with team, role,
// own answer and counts (R14.1, R14.2). Counts take only members active in
// the team now: a member who left, or a pending one, keeps the RSVP row but
// is not counted.
func TestUpcomingAcrossTeamsWithCountsRoleAndOwnAnswer(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	user := userOf(t, f, f.admin)
	tigres := otherTeam(t, f, user, "player", "active")

	later := insertMatch(t, f, f.team, kickoff.Add(time.Hour), "Halcones")
	sooner := insertMatch(t, f, tigres, kickoff, "Águilas")

	rsvpDirect(t, f, later, f.admin, "going")
	rsvpDirect(t, f, later, addMember(t, f, "p1@example.com", "player", "active"), "going")
	rsvpDirect(t, f, later, addMember(t, f, "p2@example.com", "player", "active"), "not_going")
	rsvpDirect(t, f, later, addMember(t, f, "left@example.com", "player", "left"), "going")
	rsvpDirect(t, f, later, addMember(t, f, "left2@example.com", "player", "left"), "not_going")
	rsvpDirect(t, f, later, addMember(t, f, "pending@example.com", "player", "pending"), "going")

	got, err := f.svc.Upcoming(ctx, user)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != sooner || got[1].ID != later {
		t.Fatalf("upcoming = %+v, want [%d %d]", got, sooner, later)
	}

	s := got[0]
	if s.TeamID != tigres || s.TeamName != "Tigres" || s.Role != domain.RolePlayer || s.RivalName != "Águilas" ||
		s.Location != "Cancha 3" || !s.StartsAt.Equal(kickoff) || s.StartsAt.Location() != time.UTC ||
		s.Answer != nil || s.GoingCount != 0 || s.NotGoingCount != 0 {
		t.Fatalf("sooner = %+v", s)
	}
	l := got[1]
	if l.TeamID != f.team || l.CreatedBy == nil || l.TeamName != "Pumas" || l.Role != domain.RoleAdmin ||
		l.Answer == nil || *l.Answer != domain.RSVPGoing || l.GoingCount != 2 || l.NotGoingCount != 1 {
		t.Fatalf("later = %+v (answer %v), want admin, own going, 2 going, 1 not going", l, l.Answer)
	}
}

// Teams where the user's membership is pending or left show no matches
// (R14.3); only the active team's match comes back.
func TestUpcomingSkipsPendingAndLeftTeams(t *testing.T) {
	f := newFixture(t)
	user := userOf(t, f, f.admin)
	active := insertMatch(t, f, f.team, kickoff, "Halcones")
	insertMatch(t, f, otherTeam(t, f, user, "player", "pending"), kickoff, "Pending FC")
	insertMatch(t, f, otherTeam(t, f, user, "admin", "left"), kickoff, "Left FC")

	got, err := f.svc.Upcoming(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != active {
		t.Fatalf("upcoming = %+v, want only match %d", got, active)
	}
}

// A match has started at its kickoff instant (R14.4, as the RSVP lock in
// R9.5): with the clock on kickoff, that match and earlier ones are gone and
// one a second later stays.
func TestUpcomingSkipsStartedMatches(t *testing.T) {
	f := newFixture(t)
	user := userOf(t, f, f.admin)
	insertMatch(t, f, f.team, kickoff.Add(-time.Hour), "Pasado")
	insertMatch(t, f, f.team, kickoff, "Ahora")
	next := insertMatch(t, f, f.team, kickoff.Add(time.Second), "Siguiente")
	*f.now = kickoff

	got, err := f.svc.Upcoming(context.Background(), user)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != next {
		t.Fatalf("upcoming = %+v, want only match %d", got, next)
	}
}

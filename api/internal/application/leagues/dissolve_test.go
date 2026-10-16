package leagues_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/leagues"
)

// TestDissolveCancelsFutureFixtures (#421): dissolving a team takes it out of
// every league and cancels its future fixtures on both sides; past ones
// stay. The rival is told; the dissolved team has nobody left to tell.
func TestDissolveCancelsFutureFixtures(t *testing.T) {
	l := setupLeague(t)
	ctx := context.Background()
	other := l.create(t, "Otra liga")
	for _, team := range []int64{l.home, l.away} {
		if _, err := l.svc.Enroll(ctx, other.ID, team); err != nil {
			t.Fatal(err)
		}
	}
	past, _ := l.newFixture(t, tuesdayAt2030(0))
	future, _ := l.newFixture(t, tuesdayAt2030(1))
	elsewhere, _, err := l.svc.CreateFixture(ctx, other.ID, leagues.NewFixture{HomeTeamID: l.away, AwayTeamID: l.home, CourtID: l.court, StartsAt: tuesdayAt2030(2)})
	if err != nil {
		t.Fatal(err)
	}
	*l.now = tuesdayAt2030(0).Add(3 * time.Hour) // the first fixture is over

	if err := l.teams.Dissolve(ctx, l.home, l.homeAdmin); err != nil {
		t.Fatal(err)
	}
	if n := l.count(t, `SELECT count(*) FROM league_team WHERE team_id = $1`, l.home); n != 0 {
		t.Errorf("%d enrollments left, want 0", n)
	}
	for _, tc := range []struct {
		name    string
		fixture int64
		want    int // cancelled matches
	}{{"future", future.ID, 2}, {"other league", elsewhere.ID, 2}, {"past", past.ID, 0}} {
		if n := l.count(t, `SELECT count(*) FROM match WHERE fixture_id = $1 AND cancelled_at IS NOT NULL`, tc.fixture); n != tc.want {
			t.Errorf("%s fixture: %d cancelled matches, want %d", tc.name, n, tc.want)
		}
	}
	// One cancel notice per side per fixture; only the rival's reaches anyone.
	if n := l.count(t, `SELECT count(*) FROM notification WHERE title = 'Partido cancelado: vs. Rayos'`); n != 2 {
		t.Errorf("%d notices to the rival, want 2", n)
	}
	if n := l.count(t, `SELECT count(*) FROM notification n JOIN notification_recipient r ON r.notification_id = n.id
		WHERE n.team_id = $1 AND n.title LIKE 'Partido cancelado%'`, l.home); n != 0 {
		t.Errorf("the dissolved team's cancel notices reached %d members, want 0", n)
	}
	// The rival stays enrolled.
	if n := l.count(t, `SELECT count(*) FROM league_team WHERE team_id = $1`, l.away); n != 2 {
		t.Errorf("rival in %d leagues, want 2", n)
	}
}

// TestDissolveRacesFixtureCreation: a dissolve and a new fixture for the same
// team, at once, many times. Both take the team lock before the league lock,
// so neither deadlocks, and the team never ends with a live future fixture.
func TestDissolveRacesFixtureCreation(t *testing.T) {
	for round := range 10 {
		l := setupLeague(t)
		ctx := context.Background()
		var wg sync.WaitGroup
		var dissolveErr, createErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			dissolveErr = l.teams.Dissolve(ctx, l.home, l.homeAdmin)
		}()
		go func() {
			defer wg.Done()
			_, _, createErr = l.svc.CreateFixture(ctx, l.id, leagues.NewFixture{HomeTeamID: l.home, AwayTeamID: l.away, CourtID: l.court, StartsAt: tuesday2030})
		}()
		wg.Wait()
		if dissolveErr != nil {
			t.Fatalf("round %d: dissolve: %v", round, dissolveErr)
		}
		if createErr != nil && !errors.Is(createErr, leagues.ErrTeamNotEnrolled) {
			t.Fatalf("round %d: create: %v", round, createErr)
		}
		if n := l.count(t, `SELECT count(*) FROM match m JOIN fixture f ON f.id = m.fixture_id
			WHERE m.cancelled_at IS NULL AND (f.home_team_id = $1 OR f.away_team_id = $1)`, l.home); n != 0 {
			t.Fatalf("round %d: %d live matches of the dissolved team's fixtures", round, n)
		}
	}
}

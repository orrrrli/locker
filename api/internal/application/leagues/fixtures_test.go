package leagues_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/leagues"
	"github.com/orrrrli/locker/api/internal/application/venues"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
)

var tijuana, _ = time.LoadLocation("America/Tijuana")

// tuesday2030 is inside the test league's window: Tuesday 20:00 to 23:00 in
// America/Tijuana.
var tuesday2030 = tuesdayAt2030(0)

// tuesdayAt2030 is 20:30 in Tijuana on the Tuesday weeks after 20 October
// 2026. Built from the wall clock, not by adding hours: Tijuana leaves
// daylight saving time on 1 November.
func tuesdayAt2030(weeks int) time.Time {
	return time.Date(2026, 10, 20+7*weeks, 20, 30, 0, 0, tijuana)
}

type league struct {
	fixture
	id         int64
	home, away int64 // enrolled teams
	homeAdmin  int64 // the home team's admin membership
}

// setupLeague is setup plus a league with two enrolled teams, Rayos at home
// and Truenos away.
func setupLeague(t *testing.T) league {
	t.Helper()
	f := setup(t)
	ctx := context.Background()
	l := f.create(t, "Libre")
	home, homeAdmin := f.namedTeam(t, "rayos@example.com", "Rayos")
	away, _ := f.namedTeam(t, "truenos@example.com", "Truenos")
	for _, team := range []int64{home, away} {
		if _, err := f.svc.Enroll(ctx, l.ID, team); err != nil {
			t.Fatal(err)
		}
	}
	return league{fixture: f, id: l.ID, home: home, away: away, homeAdmin: homeAdmin}
}

func (f fixture) namedTeam(t *testing.T, email, name string) (teamID, adminMembership int64) {
	t.Helper()
	teamID, admin := f.newTeam(t, email)
	if _, err := f.teams.Update(context.Background(), teamID, admin, &name, nil); err != nil {
		t.Fatal(err)
	}
	return teamID, admin
}

func (l league) newFixture(t *testing.T, at time.Time) (domain.Fixture, []domain.Match) {
	t.Helper()
	fx, ms, err := l.svc.CreateFixture(context.Background(), l.id, leagues.NewFixture{HomeTeamID: l.home, AwayTeamID: l.away, CourtID: l.court, StartsAt: at})
	if err != nil {
		t.Fatal(err)
	}
	return fx, ms
}

// notice is one stored notice, with its recipients' team.
type notice struct {
	team              int64
	kind, title, body string
	matchID           int64
	recipients        int
}

func (f fixture) notices(t *testing.T) []notice {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `
		SELECT n.team_id, n.kind, n.title, n.body, n.match_id, (SELECT count(*) FROM notification_recipient r WHERE r.notification_id = n.id)
		FROM notification n WHERE n.kind <> 'manual' ORDER BY n.id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []notice
	for rows.Next() {
		var n notice
		if err := rows.Scan(&n.team, &n.kind, &n.title, &n.body, &n.matchID, &n.recipients); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func (f fixture) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestCreateFixtureWritesBothMatchesAndNotices(t *testing.T) {
	l := setupLeague(t)
	fx, ms := l.newFixture(t, tuesday2030)

	if fx.ID == 0 || fx.LeagueID != l.id || fx.HomeTeamID != l.home || fx.AwayTeamID != l.away || fx.CourtID != l.court ||
		!fx.StartsAt.Equal(tuesday2030) || fx.CancelledAt != nil {
		t.Fatalf("fixture = %+v", fx)
	}
	rivals := map[int64]string{l.home: "Truenos", l.away: "Rayos"}
	if len(ms) != 2 {
		t.Fatalf("matches = %+v, want 2", ms)
	}
	for _, m := range ms {
		if m.FixtureID == nil || *m.FixtureID != fx.ID || m.CourtID == nil || *m.CourtID != l.court || m.CreatedBy != nil ||
			!m.StartsAt.Equal(tuesday2030) || m.RivalName != rivals[m.TeamID] || m.Location != "Unidad Deportiva · Cancha 1" {
			t.Errorf("match = %+v", m)
		}
	}
	got := l.notices(t)
	if len(got) != 2 {
		t.Fatalf("notices = %+v, want one per team", got)
	}
	for i, n := range got {
		m := ms[i]
		want := notice{m.TeamID, "match_created", "Partido nuevo: vs. " + rivals[m.TeamID], "Mar 20 · 20:30 · Unidad Deportiva · Cancha 1.", m.ID, 1}
		if n != want {
			t.Errorf("notice %d = %+v, want %+v", i, n, want)
		}
	}
}

// TestFixtureRivalFollowsTheOtherTeam: a fixture match stores no rival name;
// it shows the other team's current name (design.md resolved decision 7).
func TestFixtureRivalFollowsTheOtherTeam(t *testing.T) {
	l := setupLeague(t)
	_, ms := l.newFixture(t, tuesday2030)
	name := "Rayos FC"
	if _, err := l.teams.Update(context.Background(), l.home, l.homeAdmin, &name, nil); err != nil {
		t.Fatal(err)
	}
	m, err := postgres.NewMatches(l.pool).MatchByID(context.Background(), ms[1].ID) // the away team's
	if err != nil {
		t.Fatal(err)
	}
	if m.RivalName != "Rayos FC" {
		t.Fatalf("rival = %q, want the renamed home team", m.RivalName)
	}
}

func TestCreateFixtureRejects(t *testing.T) {
	l := setupLeague(t)
	ctx := context.Background()
	stranger, _ := l.namedTeam(t, "otro@example.com", "Sin Liga")
	dissolved, dissolvedAdmin := l.namedTeam(t, "disuelto@example.com", "Disuelto")
	if _, err := l.svc.Enroll(ctx, l.id, dissolved); err != nil {
		t.Fatal(err)
	}
	if err := l.teams.Dissolve(ctx, dissolved, dissolvedAdmin); err != nil {
		t.Fatal(err)
	}
	lat, lng := 32.5, -117.0
	other, err := venues.NewService(venues.Deps{Tx: postgres.NewTxRunner(l.pool), Venues: postgres.NewVenues(l.pool)}).Create(ctx, venues.NewVenue{
		Name: "Otra", Address: "Calle 1", Latitude: &lat, Longitude: &lng, Timezone: "America/Tijuana", Courts: []string{"Cancha 1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	ok := leagues.NewFixture{HomeTeamID: l.home, AwayTeamID: l.away, CourtID: l.court, StartsAt: tuesday2030}
	for _, tc := range []struct {
		name   string
		league int64
		change func(*leagues.NewFixture)
		want   error
	}{
		{"same team twice", l.id, func(f *leagues.NewFixture) { f.AwayTeamID = l.home }, leagues.ErrSameTeam},
		{"unenrolled team", l.id, func(f *leagues.NewFixture) { f.AwayTeamID = stranger }, leagues.ErrTeamNotEnrolled},
		{"unknown team", l.id, func(f *leagues.NewFixture) { f.AwayTeamID = dissolved + 100 }, leagues.ErrTeamNotEnrolled},
		{"dissolved team", l.id, func(f *leagues.NewFixture) { f.AwayTeamID = dissolved }, leagues.ErrTeamNotEnrolled},
		{"court of another venue", l.id, func(f *leagues.NewFixture) { f.CourtID = other.Courts[0].ID }, leagues.ErrInvalidCourt},
		{"wrong weekday", l.id, func(f *leagues.NewFixture) { f.StartsAt = tuesday2030.Add(24 * time.Hour) }, leagues.ErrOutsideWindow},
		{"before the window", l.id, func(f *leagues.NewFixture) { f.StartsAt = tuesday2030.Add(-31 * time.Minute) }, leagues.ErrOutsideWindow},
		{"at the window's end", l.id, func(f *leagues.NewFixture) { f.StartsAt = tuesday2030.Add(150 * time.Minute) }, leagues.ErrOutsideWindow},
		{"right weekday in UTC only", l.id, func(f *leagues.NewFixture) {
			f.StartsAt = time.Date(2026, 10, 20, 21, 0, 0, 0, time.UTC) // Tuesday 14:00 in Tijuana
		}, leagues.ErrOutsideWindow},
		{"no time", l.id, func(f *leagues.NewFixture) { f.StartsAt = time.Time{} }, leagues.ErrInvalidStartsAt},
		{"unknown league", l.id + 100, func(*leagues.NewFixture) {}, domain.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := ok
			tc.change(&in)
			if _, _, err := l.svc.CreateFixture(ctx, tc.league, in); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	if n := l.count(t, `SELECT (SELECT count(*) FROM fixture) + (SELECT count(*) FROM match) + (SELECT count(*) FROM notification)`); n != 0 {
		t.Fatalf("%d fixture, match and notice rows after rejected creates, want 0", n)
	}
	// The window's first minute is in.
	in := ok
	in.StartsAt = tuesday2030.Add(-30 * time.Minute)
	if _, _, err := l.svc.CreateFixture(ctx, l.id, in); err != nil {
		t.Fatalf("at 20:00: %v", err)
	}
}

func TestUpdateFixtureMovesBothMatches(t *testing.T) {
	l := setupLeague(t)
	ctx := context.Background()
	fx, ms := l.newFixture(t, tuesday2030)
	court2, err := venues.NewService(venues.Deps{Tx: postgres.NewTxRunner(l.pool), Venues: postgres.NewVenues(l.pool)}).AddCourt(ctx, l.venue, "Cancha 2")
	if err != nil {
		t.Fatal(err)
	}

	later := tuesday2030.Add(time.Hour)
	got, moved, err := l.svc.UpdateFixture(ctx, l.id, fx.ID, leagues.FixtureChange{StartsAt: &later, CourtID: &court2.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !got.StartsAt.Equal(later) || got.CourtID != court2.ID {
		t.Fatalf("fixture = %+v", got)
	}
	for _, m := range moved {
		if !m.StartsAt.Equal(later) || *m.CourtID != court2.ID || m.Location != "Unidad Deportiva · Cancha 2" {
			t.Errorf("match = %+v", m)
		}
	}
	ns := l.notices(t)
	if len(ns) != 4 || ns[2].kind != "match_changed" || ns[2].title != "Cambio de partido: vs. Truenos" ||
		ns[2].body != "Mar 20 · 21:30 · Unidad Deportiva · Cancha 2." || ns[3].matchID != ms[1].ID {
		t.Fatalf("notices = %+v, want two match_changed after the two match_created", ns)
	}

	// Sending the current values changes nothing and notifies nobody.
	if _, _, err := l.svc.UpdateFixture(ctx, l.id, fx.ID, leagues.FixtureChange{StartsAt: &later}); err != nil {
		t.Fatal(err)
	}
	if n := len(l.notices(t)); n != 4 {
		t.Fatalf("%d notices after a no-op change, want 4", n)
	}

	wrongDay := later.Add(24 * time.Hour)
	for _, tc := range []struct {
		name    string
		league  int64
		fixture int64
		c       leagues.FixtureChange
		want    error
	}{
		{"nothing", l.id, fx.ID, leagues.FixtureChange{}, leagues.ErrNothingToMove},
		{"outside the window", l.id, fx.ID, leagues.FixtureChange{StartsAt: &wrongDay}, leagues.ErrOutsideWindow},
		{"court of no venue", l.id, fx.ID, leagues.FixtureChange{CourtID: ptrInt(court2.ID + 100)}, leagues.ErrInvalidCourt},
		{"fixture of another league", l.id + 100, fx.ID, leagues.FixtureChange{StartsAt: &later}, domain.ErrNotFound},
	} {
		if _, _, err := l.svc.UpdateFixture(ctx, tc.league, tc.fixture, tc.c); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}

	// Once a team saved attendance, the time is fixed; the court can still move.
	if _, err := l.pool.Exec(ctx, `UPDATE match SET attendance_closed_at = now() WHERE id = $1`, ms[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.svc.UpdateFixture(ctx, l.id, fx.ID, leagues.FixtureChange{StartsAt: &tuesday2030}); !errors.Is(err, leagues.ErrFixtureAttendance) {
		t.Fatalf("move after attendance: err = %v, want ErrFixtureAttendance", err)
	}
	if _, _, err := l.svc.UpdateFixture(ctx, l.id, fx.ID, leagues.FixtureChange{CourtID: &l.court}); err != nil {
		t.Fatalf("court after attendance: %v", err)
	}
}

func ptrInt(v int64) *int64 { return &v }

func TestCancelFixtureCancelsBothMatches(t *testing.T) {
	l := setupLeague(t)
	ctx := context.Background()
	fx, ms := l.newFixture(t, tuesday2030)

	if err := l.svc.CancelFixture(ctx, l.id, fx.ID); err != nil {
		t.Fatal(err)
	}
	if n := l.count(t, `SELECT count(*) FROM match WHERE fixture_id = $1 AND cancelled_at IS NOT NULL`, fx.ID); n != 2 {
		t.Fatalf("%d cancelled matches, want 2", n)
	}
	if n := l.count(t, `SELECT count(*) FROM fixture WHERE id = $1 AND cancelled_at IS NOT NULL`, fx.ID); n != 1 {
		t.Fatal("fixture not cancelled")
	}
	ns := l.notices(t)
	if len(ns) != 4 || ns[2].title != "Partido cancelado: vs. Truenos" || ns[3].title != "Partido cancelado: vs. Rayos" ||
		ns[2].kind != "match_changed" || ns[2].matchID != ms[0].ID {
		t.Fatalf("notices = %+v", ns)
	}
	if err := l.svc.CancelFixture(ctx, l.id, fx.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("cancel twice: err = %v, want not found", err)
	}
	if _, _, err := l.svc.UpdateFixture(ctx, l.id, fx.ID, leagues.FixtureChange{CourtID: &l.court}); !errors.Is(err, leagues.ErrFixtureCancelled) {
		t.Fatalf("move a cancelled fixture: err = %v, want ErrFixtureCancelled", err)
	}

	// A fixture that has started stays.
	started, _ := l.newFixture(t, tuesdayAt2030(1))
	*l.now = started.StartsAt
	if err := l.svc.CancelFixture(ctx, l.id, started.ID); !errors.Is(err, leagues.ErrFixtureStarted) {
		t.Fatalf("cancel at kickoff: err = %v, want ErrFixtureStarted", err)
	}
}

// TestUnenrollCancelsFutureFixtures (R16.8, open question 27): removing a
// team cancels its future fixtures in the league and both sides' matches;
// a past fixture keeps its matches.
func TestUnenrollCancelsFutureFixtures(t *testing.T) {
	l := setupLeague(t)
	ctx := context.Background()
	past, _ := l.newFixture(t, tuesday2030)
	future, _ := l.newFixture(t, tuesdayAt2030(1))
	other := l.create(t, "Otra liga")
	for _, team := range []int64{l.home, l.away} {
		if _, err := l.svc.Enroll(ctx, other.ID, team); err != nil {
			t.Fatal(err)
		}
	}
	elsewhere, _, err := l.svc.CreateFixture(ctx, other.ID, leagues.NewFixture{HomeTeamID: l.home, AwayTeamID: l.away, CourtID: l.court, StartsAt: tuesdayAt2030(2)})
	if err != nil {
		t.Fatal(err)
	}
	*l.now = tuesday2030.Add(3 * time.Hour) // the first fixture is over

	if err := l.svc.Unenroll(ctx, l.id, l.away); err != nil {
		t.Fatal(err)
	}
	cancelled := func(fixtureID int64) string {
		return fmt.Sprintf("%d%d", l.count(t, `SELECT count(*) FROM fixture WHERE id = $1 AND cancelled_at IS NOT NULL`, fixtureID),
			l.count(t, `SELECT count(*) FROM match WHERE fixture_id = $1 AND cancelled_at IS NOT NULL`, fixtureID))
	}
	if got := cancelled(future.ID); got != "12" {
		t.Errorf("future fixture: cancelled fixture and matches = %s, want 1 and 2", got)
	}
	if got := cancelled(past.ID); got != "00" {
		t.Errorf("past fixture: cancelled fixture and matches = %s, want none", got)
	}
	if got := cancelled(elsewhere.ID); got != "00" {
		t.Errorf("other league's fixture: cancelled = %s, want none", got)
	}
	if n := l.count(t, `SELECT count(*) FROM notification WHERE title LIKE 'Partido cancelado%'`); n != 2 {
		t.Fatalf("%d cancel notices, want one per team", n)
	}
	// The team is out: no new fixture for it.
	if _, _, err := l.svc.CreateFixture(ctx, l.id, leagues.NewFixture{HomeTeamID: l.home, AwayTeamID: l.away, CourtID: l.court, StartsAt: tuesdayAt2030(3)}); !errors.Is(err, leagues.ErrTeamNotEnrolled) {
		t.Fatalf("fixture after unenroll: err = %v, want ErrTeamNotEnrolled", err)
	}
}

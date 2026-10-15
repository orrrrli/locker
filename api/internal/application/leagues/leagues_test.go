package leagues_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/application/leagues"
	"github.com/orrrrli/locker/api/internal/application/teams"
	"github.com/orrrrli/locker/api/internal/application/venues"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
	"github.com/orrrrli/locker/api/internal/testdb"
)

type fixture struct {
	svc   *leagues.Service
	teams *teams.Service
	pool  *pgxpool.Pool
	venue int64
}

func setup(t *testing.T) fixture {
	t.Helper()
	pool := testdb.New(t)
	ctx := context.Background()
	lat, lng := 31.8667, -116.5964
	v, err := venues.NewService(venues.Deps{Tx: postgres.NewTxRunner(pool), Venues: postgres.NewVenues(pool)}).Create(ctx, venues.NewVenue{
		Name: "Unidad Deportiva", Address: "Av. Reforma s/n", Latitude: &lat, Longitude: &lng,
		Timezone: "America/Tijuana", Courts: []string{"Cancha 1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	memberships := postgres.NewMemberships(pool)
	return fixture{
		svc: leagues.NewService(leagues.Deps{Tx: postgres.NewTxRunner(pool), Leagues: postgres.NewLeagues(pool), Teams: memberships}),
		teams: teams.NewService(teams.Deps{
			Tx: postgres.NewTxRunner(pool), Teams: postgres.NewTeams(pool), Memberships: memberships,
		}),
		pool:  pool,
		venue: v.ID,
	}
}

func (f fixture) newLeague(name string) leagues.NewLeague {
	return leagues.NewLeague{
		VenueID: f.venue, Name: name, Weekday: "tuesday", StartTime: "20:00", EndTime: "23:00",
		SeasonName: "Otoño 2026", SeasonStartsOn: "2026-09-01", SeasonEndsOn: "2026-12-15",
	}
}

func (f fixture) create(t *testing.T, name string) domain.League {
	t.Helper()
	l, err := f.svc.Create(context.Background(), f.newLeague(name))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// newTeam creates a team with a fresh user as its admin; the admin's
// membership id lets the test dissolve it.
func (f fixture) newTeam(t *testing.T, email string) (teamID, adminMembership int64) {
	t.Helper()
	ctx := context.Background()
	userID, err := postgres.NewUsers(f.pool).CreateUser(ctx, "Ana", email, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	team, err := f.teams.Create(ctx, userID, "Rayos", "America/Tijuana")
	if err != nil {
		t.Fatal(err)
	}
	m, err := postgres.NewMemberships(f.pool).MembershipByTeamAndUser(ctx, team.ID, userID)
	if err != nil {
		t.Fatal(err)
	}
	return team.ID, m.ID
}

func ptr(s string) *string { return &s }

func TestCreateStoresLeague(t *testing.T) {
	f := setup(t)
	in := f.newLeague("  Liga Martes Libre  ")
	l, err := f.svc.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	day := func(s string) time.Time { d, _ := time.Parse(time.DateOnly, s); return d }
	if l.ID == 0 || l.VenueID != f.venue || l.Name != "Liga Martes Libre" || l.Weekday != 2 ||
		l.StartTime != 20*60 || l.EndTime != 23*60 || l.SeasonName != "Otoño 2026" ||
		!l.SeasonStartsOn.Equal(day("2026-09-01")) || !l.SeasonEndsOn.Equal(day("2026-12-15")) || l.CreatedAt.IsZero() {
		t.Fatalf("league = %+v", l)
	}
}

func TestCreateAcceptsLeaguesOnTheSameWeekday(t *testing.T) {
	f := setup(t)
	for _, name := range []string{"Libre", "Veteranos", "Femenil"} {
		f.create(t, name) // R16.5: same venue, same weekday, same window
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	f := setup(t)
	for _, tc := range []struct {
		name   string
		change func(*leagues.NewLeague)
		want   error
	}{
		{"empty name", func(l *leagues.NewLeague) { l.Name = " " }, leagues.ErrInvalidName},
		{"name of 61", func(l *leagues.NewLeague) { l.Name = strings.Repeat("a", 61) }, leagues.ErrInvalidName},
		{"no weekday", func(l *leagues.NewLeague) { l.Weekday = "" }, leagues.ErrInvalidWeekday},
		{"weekday in Spanish", func(l *leagues.NewLeague) { l.Weekday = "martes" }, leagues.ErrInvalidWeekday},
		{"capitalized weekday", func(l *leagues.NewLeague) { l.Weekday = "Tuesday" }, leagues.ErrInvalidWeekday},
		{"end equals start (R16.2)", func(l *leagues.NewLeague) { l.EndTime = "20:00" }, leagues.ErrInvalidWindow},
		{"end before start (R16.2)", func(l *leagues.NewLeague) { l.EndTime = "19:59" }, leagues.ErrInvalidWindow},
		{"past midnight", func(l *leagues.NewLeague) { l.StartTime, l.EndTime = "22:00", "01:00" }, leagues.ErrInvalidWindow},
		{"no start time", func(l *leagues.NewLeague) { l.StartTime = "" }, leagues.ErrInvalidWindow},
		{"hour 24", func(l *leagues.NewLeague) { l.EndTime = "24:00" }, leagues.ErrInvalidWindow},
		{"seconds", func(l *leagues.NewLeague) { l.EndTime = "23:00:00" }, leagues.ErrInvalidWindow},
		{"one-digit hour", func(l *leagues.NewLeague) { l.StartTime = "8:00" }, leagues.ErrInvalidWindow},
		{"empty season name", func(l *leagues.NewLeague) { l.SeasonName = "" }, leagues.ErrInvalidSeasonName},
		{"season ends before it starts (R16.3)", func(l *leagues.NewLeague) { l.SeasonEndsOn = "2026-08-31" }, leagues.ErrInvalidSeason},
		{"no season start", func(l *leagues.NewLeague) { l.SeasonStartsOn = "" }, leagues.ErrInvalidSeason},
		{"not a date", func(l *leagues.NewLeague) { l.SeasonEndsOn = "2026-02-30" }, leagues.ErrInvalidSeason},
		{"year 0", func(l *leagues.NewLeague) { l.SeasonStartsOn = "0000-01-01" }, leagues.ErrInvalidSeason},
		{"unknown venue", func(l *leagues.NewLeague) { l.VenueID++ }, leagues.ErrUnknownVenue},
		{"no venue", func(l *leagues.NewLeague) { l.VenueID = 0 }, leagues.ErrUnknownVenue},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := f.newLeague("Libre")
			tc.change(&in)
			if _, err := f.svc.Create(context.Background(), in); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	// A one-day season is fine.
	in := f.newLeague("Relámpago")
	in.SeasonEndsOn = in.SeasonStartsOn
	if _, err := f.svc.Create(context.Background(), in); err != nil {
		t.Fatalf("one-day season: %v", err)
	}
}

func TestUpdateChecksTheMergedLeague(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	l := f.create(t, "Libre")

	got, err := f.svc.Update(ctx, l.ID, leagues.Change{Name: ptr("Libre A"), Weekday: ptr("sunday"), EndTime: ptr("22:30")})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Libre A" || got.Weekday != 7 || got.StartTime != 20*60 || got.EndTime != 22*60+30 || got.VenueID != f.venue {
		t.Fatalf("updated = %+v", got)
	}

	for _, tc := range []struct {
		name string
		c    leagues.Change
		want error
	}{
		{"nothing", leagues.Change{}, leagues.ErrNothingToUpdate},
		{"end before the stored start", leagues.Change{EndTime: ptr("19:00")}, leagues.ErrInvalidWindow},
		{"start after the stored end", leagues.Change{StartTime: ptr("23:00")}, leagues.ErrInvalidWindow},
		{"season end before the stored start", leagues.Change{SeasonEndsOn: ptr("2026-08-01")}, leagues.ErrInvalidSeason},
		{"bad weekday", leagues.Change{Weekday: ptr("someday")}, leagues.ErrInvalidWeekday},
	} {
		if _, err := f.svc.Update(ctx, l.ID, tc.c); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	// Moving both ends at once is checked together.
	if _, err := f.svc.Update(ctx, l.ID, leagues.Change{StartTime: ptr("07:00"), EndTime: ptr("09:00")}); err != nil {
		t.Fatalf("move the window: %v", err)
	}
	if _, err := f.svc.Update(ctx, l.ID+1, leagues.Change{Name: ptr("X")}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown league: err = %v, want not found", err)
	}
}

func TestEnrollment(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	libre := f.create(t, "Libre")
	veteranos := f.create(t, "Veteranos")
	team, _ := f.newTeam(t, "ana@example.com")

	e, err := f.svc.Enroll(ctx, libre.ID, team)
	if err != nil {
		t.Fatal(err)
	}
	if e.LeagueID != libre.ID || e.TeamID != team || e.EnrolledAt.IsZero() {
		t.Fatalf("enrollment = %+v", e)
	}
	// R16.7: once per league.
	if _, err := f.svc.Enroll(ctx, libre.ID, team); !errors.Is(err, leagues.ErrAlreadyEnrolled) {
		t.Fatalf("enroll twice: err = %v, want ErrAlreadyEnrolled", err)
	}
	// R16.9: several leagues.
	if _, err := f.svc.Enroll(ctx, veteranos.ID, team); err != nil {
		t.Fatalf("second league: %v", err)
	}
	if _, err := f.svc.Enroll(ctx, veteranos.ID+1, team); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown league: err = %v, want not found", err)
	}
	if _, err := f.svc.Enroll(ctx, libre.ID, team+1); !errors.Is(err, leagues.ErrInvalidTeam) {
		t.Fatalf("unknown team: err = %v, want ErrInvalidTeam", err)
	}

	// R16.8: the enrollment goes, and can come back.
	if err := f.svc.Unenroll(ctx, libre.ID, team); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Unenroll(ctx, libre.ID, team); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unenroll twice: err = %v, want not found", err)
	}
	if _, err := f.svc.Enroll(ctx, libre.ID, team); err != nil {
		t.Fatalf("enroll again: %v", err)
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM league_team WHERE team_id = $1`, team).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("%d enrollments, want 2", n)
	}
}

// TestEnrollRejectsADissolvedTeam (R6.10).
func TestEnrollRejectsADissolvedTeam(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	l := f.create(t, "Libre")
	team, admin := f.newTeam(t, "ana@example.com")
	if err := f.teams.Dissolve(ctx, team, admin); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Enroll(ctx, l.ID, team); !errors.Is(err, leagues.ErrInvalidTeam) {
		t.Fatalf("enroll a dissolved team: err = %v, want ErrInvalidTeam", err)
	}
}

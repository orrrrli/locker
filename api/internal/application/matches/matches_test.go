package matches_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/matches"
	"github.com/orrrrli/locker/api/internal/application/notifications"
	"github.com/orrrrli/locker/api/internal/application/teams"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
	"github.com/orrrrli/locker/api/internal/testdb"
)

var kickoff = time.Date(2026, 10, 20, 19, 0, 0, 0, time.FixedZone("PDT", -7*60*60))

type fixture struct {
	svc   *matches.Service
	pool  *pgxpool.Pool
	team  int64
	admin int64 // the creator's membership
	// now is the service's clock; tests move it.
	now *time.Time
}

// newFixture wires the service on a fresh database with one team and its
// admin. The clock starts a day before kickoff.
func newFixture(t *testing.T) fixture {
	t.Helper()
	pool := testdb.New(t)
	ctx := context.Background()
	members := postgres.NewMemberships(pool)
	user := newUser(t, pool, "ana@example.com")
	team, err := teams.NewService(teams.Deps{Tx: postgres.NewTxRunner(pool), Teams: postgres.NewTeams(pool), Memberships: members}).
		Create(ctx, user, "Pumas", "America/Tijuana")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := members.MembershipByTeamAndUser(ctx, team.ID, user)
	if err != nil {
		t.Fatal(err)
	}
	now := kickoff.Add(-24 * time.Hour)
	notifier := notifications.NewService(notifications.Deps{
		Tx: postgres.NewTxRunner(pool), Notifications: postgres.NewNotifications(pool),
		Memberships: members, Teams: postgres.NewTeams(pool),
	})
	svc := matches.NewService(matches.Deps{
		Tx: postgres.NewTxRunner(pool), Matches: postgres.NewMatches(pool), Memberships: members,
		Notifier: notifier, Now: func() time.Time { return now },
	})
	return fixture{svc: svc, pool: pool, team: team.ID, admin: admin.ID, now: &now}
}

func newUser(t *testing.T, pool *pgxpool.Pool, email string) int64 {
	t.Helper()
	id, err := postgres.NewUsers(pool).CreateUser(context.Background(), "Ana", email, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// addMember inserts a membership of the team directly, in any role and status.
func addMember(t *testing.T, f fixture, email, role, status string) int64 {
	t.Helper()
	user := newUser(t, f.pool, email)
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO membership (team_id, user_id, role, status) VALUES ($1, $2, $3, $4) RETURNING id`,
		f.team, user, role, status).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// The time is kept as an instant and comes back in UTC (R9.1); the text
// fields are trimmed.
func TestCreateStoresUTC(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	m, err := f.svc.Create(ctx, f.team, f.admin, kickoff, "  Halcones  ", " Cancha 3 ")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 10, 21, 2, 0, 0, 0, time.UTC)
	if !m.StartsAt.Equal(want) || m.StartsAt.Location() != time.UTC {
		t.Fatalf("starts_at = %v, want %v in UTC", m.StartsAt, want)
	}
	if m.RivalName != "Halcones" || m.Location != "Cancha 3" || m.CreatedBy != f.admin || m.TeamID != f.team {
		t.Fatalf("match = %+v", m)
	}
	list, err := f.svc.List(ctx, f.team)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].StartsAt.Equal(want) || list[0].StartsAt.Location() != time.UTC {
		t.Fatalf("list = %+v", list)
	}
}

func TestCreateRejectsInvalidInput(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name            string
		at              time.Time
		rival, location string
		want            error
	}{
		{"zero time", time.Time{}, "Halcones", "Cancha 3", matches.ErrInvalidStartsAt},
		{"year 10000 in UTC", mustParse("9999-12-31T23:30:00-23:59"), "Halcones", "Cancha 3", matches.ErrInvalidStartsAt},
		{"year 0 in UTC", mustParse("0000-01-01T00:00:00+23:59"), "Halcones", "Cancha 3", matches.ErrInvalidStartsAt},
		{"empty rival", kickoff, "   ", "Cancha 3", matches.ErrInvalidRivalName},
		{"rival of 61", kickoff, strings.Repeat("ñ", 61), "Cancha 3", matches.ErrInvalidRivalName},
		{"NUL in rival", kickoff, "Hal\x00cones", "Cancha 3", matches.ErrInvalidRivalName},
		{"bidi override in rival", kickoff, "\u202eselacnaH", "Cancha 3", matches.ErrInvalidRivalName},
		{"zero-width space in location", kickoff, "Halcones", "Cancha\u200b 3", matches.ErrInvalidLocation},
		{"empty location", kickoff, "Halcones", "", matches.ErrInvalidLocation},
		{"location of 121", kickoff, "Halcones", strings.Repeat("a", 121), matches.ErrInvalidLocation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.svc.Create(ctx, f.team, f.admin, tc.at, tc.rival, tc.location); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	// The limits count characters, not bytes, and a compound emoji keeps
	// its zero-width joiner.
	if _, err := f.svc.Create(ctx, f.team, f.admin, kickoff, strings.Repeat("ñ", 60), strings.Repeat("é", 120)); err != nil {
		t.Fatalf("60 and 120 characters: %v", err)
	}
	if _, err := f.svc.Create(ctx, f.team, f.admin, kickoff, "Familia \U0001F468\u200d\U0001F469", "Cancha 3"); err != nil {
		t.Fatalf("compound emoji: %v", err)
	}
}

func mustParse(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// Once attendance is saved the match cannot move to another time: the saved
// list belongs to the date it was played. Rival and place still change, and
// sending the same time again is not a move.
func TestUpdateRejectsNewTimeOnceAttendanceClosed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.svc.Create(ctx, f.team, f.admin, kickoff, "Halcones", "Cancha 3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE match SET attendance_closed_at = now() WHERE id = $1`, m.ID); err != nil {
		t.Fatal(err)
	}

	later := kickoff.Add(24 * time.Hour)
	if _, err := f.svc.Update(ctx, f.team, f.admin, m.ID, &later, nil, nil); !errors.Is(err, matches.ErrAttendanceClosed) {
		t.Fatalf("move: err = %v, want ErrAttendanceClosed", err)
	}
	same := kickoff
	rival := "Tigres"
	got, err := f.svc.Update(ctx, f.team, f.admin, m.ID, &same, &rival, nil)
	if err != nil {
		t.Fatalf("same time and rival: %v", err)
	}
	if got.RivalName != "Tigres" || !got.StartsAt.Equal(kickoff) {
		t.Fatalf("match = %+v, want the rival changed and the time kept", got)
	}
}

// Create and Update check the caller under the team lock: a player, or an
// admin who left, cannot schedule or change a match.
func TestWritesRecheckTheCaller(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.svc.Create(ctx, f.team, f.admin, kickoff, "Halcones", "Cancha 3")
	if err != nil {
		t.Fatal(err)
	}
	for i, tc := range []struct {
		name, role, status string
		want               error
	}{
		{"player", "player", "active", application.ErrNotAdmin},
		{"admin who left", "admin", "left", domain.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller := addMember(t, f, fmt.Sprintf("u%d@example.com", i), tc.role, tc.status)
			if _, err := f.svc.Create(ctx, f.team, caller, kickoff, "Tigres", "Cancha 1"); !errors.Is(err, tc.want) {
				t.Fatalf("create: err = %v, want %v", err, tc.want)
			}
			rival := "Tigres"
			if _, err := f.svc.Update(ctx, f.team, caller, m.ID, nil, &rival, nil); !errors.Is(err, tc.want) {
				t.Fatalf("update: err = %v, want %v", err, tc.want)
			}
		})
	}
	list, err := f.svc.List(ctx, f.team)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].RivalName != "Halcones" {
		t.Fatalf("list = %+v, want the one unchanged match", list)
	}
}

func TestUpdateIsPartialAndScopedToTheTeam(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m, err := f.svc.Create(ctx, f.team, f.admin, kickoff, "Halcones", "Cancha 3")
	if err != nil {
		t.Fatal(err)
	}

	location := "Cancha 1"
	got, err := f.svc.Update(ctx, f.team, f.admin, m.ID, nil, nil, &location)
	if err != nil {
		t.Fatal(err)
	}
	if got.Location != "Cancha 1" || got.RivalName != "Halcones" || !got.StartsAt.Equal(kickoff) {
		t.Fatalf("match = %+v, want only the location changed", got)
	}
	later := kickoff.Add(time.Hour)
	got, err = f.svc.Update(ctx, f.team, f.admin, m.ID, &later, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.StartsAt.Equal(later) || got.StartsAt.Location() != time.UTC || got.Location != "Cancha 1" {
		t.Fatalf("match = %+v, want only starts_at changed, in UTC", got)
	}

	if _, err := f.svc.Update(ctx, f.team, f.admin, m.ID, nil, nil, nil); !errors.Is(err, matches.ErrNothingToUpdate) {
		t.Fatalf("empty update: err = %v, want ErrNothingToUpdate", err)
	}
	if _, err := f.svc.Update(ctx, f.team, f.admin, m.ID+1, nil, nil, &location); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown match: err = %v, want ErrNotFound", err)
	}

	// A match of another team is not found, even for an admin of this one.
	var other int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO team (name, timezone) VALUES ('Tigres', 'America/Tijuana') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	var otherMatch int64
	if err := f.pool.QueryRow(ctx,
		`INSERT INTO match (team_id, starts_at, rival_name, location, created_by) VALUES ($1, $2, 'x', 'y', $3) RETURNING id`,
		other, kickoff, f.admin).Scan(&otherMatch); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Update(ctx, f.team, f.admin, otherMatch, nil, nil, &location); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("other team's match: err = %v, want ErrNotFound", err)
	}
}

// notice is one row of notification plus its recipients.
type notice struct {
	kind, title, body string
	matchID           *int64
	recipients        []int64
}

// notices returns the team's notices, oldest first, with their recipients.
func notices(t *testing.T, f fixture) []notice {
	t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT n.kind, n.title, n.body, n.match_id,
		        coalesce(array_agg(r.membership_id ORDER BY r.membership_id) FILTER (WHERE r.membership_id IS NOT NULL), '{}')
		 FROM notification n LEFT JOIN notification_recipient r ON r.notification_id = n.id
		 WHERE n.team_id = $1 GROUP BY n.id ORDER BY n.id`, f.team)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []notice
	for rows.Next() {
		var n notice
		if err := rows.Scan(&n.kind, &n.title, &n.body, &n.matchID, &n.recipients); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

// Scheduling a match tells every active member, the creator included and
// unread (R11.4): an automatic notice has no sender. The kickoff is written
// in the team's timezone (Tijuana, UTC-7 in October).
func TestCreateSendsMatchCreatedNotice(t *testing.T) {
	f := newFixture(t)
	player := addMember(t, f, "player@example.com", "player", "active")
	addMember(t, f, "pending@example.com", "player", "pending")
	m := newMatch(t, f)
	ns := notices(t, f)
	if len(ns) != 1 {
		t.Fatalf("notices = %+v, want one", ns)
	}
	n := ns[0]
	if n.kind != "match_created" || n.title != "Nuevo partido vs Halcones" || n.body != "20/10/2026 19:00 en Cancha 3" ||
		n.matchID == nil || *n.matchID != m.ID || !slices.Equal(n.recipients, []int64{f.admin, player}) {
		t.Fatalf("notice = %+v", n)
	}
	var unread int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM notification_recipient WHERE read_at IS NULL`).Scan(&unread); err != nil || unread != 2 {
		t.Fatalf("unread copies = %d (%v), want 2: an automatic notice has no sender", unread, err)
	}
}

// A new time or place sends one match_changed notice; a new rival name alone
// sends nothing, and a refused update sends nothing (R11.4).
func TestUpdateNotifiesOnlyForTimeOrLocation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := newMatch(t, f)
	str := func(s string) *string { return &s }
	later := kickoff.Add(time.Hour)

	if _, err := f.svc.Update(ctx, f.team, f.admin, m.ID, nil, str("Águilas"), nil); err != nil {
		t.Fatal(err)
	}
	if ns := notices(t, f); len(ns) != 1 {
		t.Fatalf("after a rival change, notices = %d, want only the creation", len(ns))
	}
	if _, err := f.svc.Update(ctx, f.team, f.admin, m.ID, &later, nil, nil); err != nil {
		t.Fatal(err)
	}
	ns := notices(t, f)
	if len(ns) != 2 || ns[1].kind != "match_changed" || ns[1].title != "Cambio en el partido vs Águilas" ||
		ns[1].body != "Ahora 20/10/2026 20:00 en Cancha 3" || ns[1].matchID == nil || *ns[1].matchID != m.ID {
		t.Fatalf("after a time change, notices = %+v", ns)
	}
	if _, err := f.svc.Update(ctx, f.team, f.admin, m.ID, nil, nil, str("Cancha 5")); err != nil {
		t.Fatal(err)
	}
	if ns := notices(t, f); len(ns) != 3 || ns[2].body != "Ahora 20/10/2026 20:00 en Cancha 5" {
		t.Fatalf("after a location change, notices = %+v", ns)
	}
	// The same values again, and a time change with both fields, count as
	// one change each; a refused update sends nothing.
	if _, err := f.svc.Update(ctx, f.team, f.admin, m.ID, &later, nil, str("Cancha 5")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Update(ctx, f.team, f.admin, m.ID, &kickoff, str("Halcones"), str("Cancha 3")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Update(ctx, f.team, f.admin, m.ID, &later, nil, str("")); !errors.Is(err, matches.ErrInvalidLocation) {
		t.Fatalf("invalid update: %v", err)
	}
	if ns := notices(t, f); len(ns) != 4 {
		t.Fatalf("notices = %d, want 4: unchanged values and refused updates send nothing", len(ns))
	}
}

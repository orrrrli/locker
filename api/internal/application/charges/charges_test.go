package charges_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/charges"
	"github.com/orrrrli/locker/api/internal/application/matches"
	"github.com/orrrrli/locker/api/internal/application/teams"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
	"github.com/orrrrli/locker/api/internal/testdb"
)

var kickoff = time.Date(2026, 10, 20, 19, 0, 0, 0, time.UTC)

type fixture struct {
	svc     *charges.Service
	matches *matches.Service
	pool    *pgxpool.Pool
	team    int64
	admin   int64 // the creator's membership
	// now is the match service's clock; tests move it past kickoff to save
	// attendance.
	now *time.Time
}

// newFixture wires the charge and match services on a fresh database with
// one team and its admin. The clock starts a day before kickoff.
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
	matchRepo := postgres.NewMatches(pool)
	matchSvc := matches.NewService(matches.Deps{
		Tx: postgres.NewTxRunner(pool), Matches: matchRepo, Memberships: members,
		Now: func() time.Time { return now },
	})
	svc := charges.NewService(charges.Deps{
		Tx: postgres.NewTxRunner(pool), Charges: postgres.NewCharges(pool), Matches: matchRepo, Memberships: members,
	})
	return fixture{svc: svc, matches: matchSvc, pool: pool, team: team.ID, admin: admin.ID, now: &now}
}

func newUser(t *testing.T, pool *pgxpool.Pool, email string) int64 {
	t.Helper()
	id, err := postgres.NewUsers(pool).CreateUser(context.Background(), "Ana", email, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// addMember inserts a membership of the team directly, in any role and
// status. An active or left row has joined, as the app sets it.
func addMember(t *testing.T, f fixture, email, role, status string) int64 {
	t.Helper()
	user := newUser(t, f.pool, email)
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO membership (team_id, user_id, role, status, joined_at)
		 VALUES ($1, $2, $3, $4, CASE WHEN $4 IN ('active', 'left') THEN now() END) RETURNING id`,
		f.team, user, role, status).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// playedMatch creates a match and saves ids as its attendance, moving the
// clock past kickoff to do so.
func playedMatch(t *testing.T, f fixture, ids ...int64) domain.Match {
	t.Helper()
	ctx := context.Background()
	m, err := f.matches.Create(ctx, f.team, f.admin, kickoff, "Halcones", "Cancha 3")
	if err != nil {
		t.Fatal(err)
	}
	*f.now = kickoff.Add(time.Hour)
	if _, err := f.matches.SaveAttendance(ctx, f.team, f.admin, m.ID, ids); err != nil {
		t.Fatal(err)
	}
	return m
}

func memberIDs(ms []domain.ChargeMember) []int64 {
	ids := make([]int64, len(ms))
	for i, m := range ms {
		ids[i] = m.MembershipID
	}
	return ids
}

func countRows(t *testing.T, f fixture, table string) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

var fee = charges.Input{Concept: "  Arbitraje  ", AmountCents: 5000, Target: charges.TargetActiveMembers}

// Every active member owes, whatever their role; pending and left members
// do not (R12.2). The concept is trimmed and the currency is MXN (R12.1).
func TestCreateForActiveMembers(t *testing.T) {
	f := newFixture(t)
	player := addMember(t, f, "player@example.com", "player", "active")
	addMember(t, f, "pending@example.com", "player", "pending")
	addMember(t, f, "left@example.com", "player", "left")

	c, err := f.svc.Create(context.Background(), f.team, f.admin, fee)
	if err != nil {
		t.Fatal(err)
	}
	if c.TeamID != f.team || c.Concept != "Arbitraje" || c.AmountCents != 5000 || c.Currency != "MXN" ||
		c.MatchID != nil || c.CreatedBy != f.admin || c.CreatedAt.IsZero() || c.CreatedAt.Location() != time.UTC {
		t.Fatalf("charge = %+v", c.Charge)
	}
	if got, want := memberIDs(c.Members), []int64{f.admin, player}; !slices.Equal(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
	for _, m := range c.Members {
		if m.ChargeID != c.ID || m.Status != domain.ChargePending || m.PaidMarkedBy != nil || m.PaidMarkedAt != nil {
			t.Fatalf("entry = %+v, want pending with nothing marked", m)
		}
	}
}

// Attendees come from the saved attendance, not from RSVP (R10.5): a member
// who said going but did not play owes nothing, and a member who left but
// played still owes. The charge then locks the attendance (R10.4).
func TestCreateForAttendeesUsesAttendanceNotRSVP(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	played := addMember(t, f, "played@example.com", "player", "active")
	saidGoing := addMember(t, f, "going@example.com", "player", "active")
	gone := addMember(t, f, "gone@example.com", "player", "left")
	m := playedMatch(t, f, played, gone)
	if _, err := f.pool.Exec(ctx, `INSERT INTO rsvp (match_id, membership_id, answer) VALUES ($1, $2, 'going')`, m.ID, saidGoing); err != nil {
		t.Fatal(err)
	}

	in := charges.Input{Concept: "Cancha", AmountCents: 12000, Target: charges.TargetMatchAttendees, MatchID: &m.ID}
	c, err := f.svc.Create(ctx, f.team, f.admin, in)
	if err != nil {
		t.Fatal(err)
	}
	if c.MatchID == nil || *c.MatchID != m.ID {
		t.Fatalf("match_id = %v, want %d", c.MatchID, m.ID)
	}
	if got, want := memberIDs(c.Members), []int64{played, gone}; !slices.Equal(got, want) {
		t.Fatalf("members = %v, want %v", got, want)
	}
	if _, err := f.matches.SaveAttendance(ctx, f.team, f.admin, m.ID, []int64{played}); !errors.Is(err, matches.ErrAttendanceLocked) {
		t.Fatalf("SaveAttendance after the charge: %v, want ErrAttendanceLocked", err)
	}
}

// A match whose attendance is still open, one with nobody in it, and one of
// another team are refused, and nothing is written.
func TestCreateForAttendeesRejects(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	open, err := f.matches.Create(ctx, f.team, f.admin, kickoff, "Halcones", "Cancha 3")
	if err != nil {
		t.Fatal(err)
	}
	empty := playedMatch(t, f)
	var other int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO team (name, timezone) VALUES ('Otros', 'America/Mexico_City') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	var foreign int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO match (team_id, starts_at, rival_name, location, created_by, attendance_closed_at)
		VALUES ($1, now(), 'x', 'y', $2, now()) RETURNING id`, other, f.admin).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		match int64
		want  error
	}{
		{"attendance open", open.ID, charges.ErrAttendanceOpen},
		{"nobody attended", empty.ID, charges.ErrNoAttendees},
		{"match of another team", foreign, charges.ErrInvalidMatch},
		{"unknown match", 999_999, charges.ErrInvalidMatch},
	} {
		in := charges.Input{Concept: "Cancha", AmountCents: 100, Target: charges.TargetMatchAttendees, MatchID: &tc.match}
		if _, err := f.svc.Create(ctx, f.team, f.admin, in); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	if n := countRows(t, f, "charge"); n != 0 {
		t.Fatalf("%d charge rows after refused requests, want 0", n)
	}
}

func TestCreateValidatesInput(t *testing.T) {
	f := newFixture(t)
	one := int64(1)
	tests := []struct {
		name string
		in   charges.Input
		want error
	}{
		{"empty concept", charges.Input{Concept: "  ", AmountCents: 1, Target: charges.TargetActiveMembers}, charges.ErrInvalidConcept},
		{"long concept", charges.Input{Concept: string(slices.Repeat([]byte("a"), 81)), AmountCents: 1, Target: charges.TargetActiveMembers}, charges.ErrInvalidConcept},
		{"invisible character", charges.Input{Concept: "Arbi​traje", AmountCents: 1, Target: charges.TargetActiveMembers}, charges.ErrInvalidConcept},
		{"zero amount", charges.Input{Concept: "x", AmountCents: 0, Target: charges.TargetActiveMembers}, charges.ErrInvalidAmount},
		{"negative amount", charges.Input{Concept: "x", AmountCents: -1, Target: charges.TargetActiveMembers}, charges.ErrInvalidAmount},
		{"too large", charges.Input{Concept: "x", AmountCents: charges.MaxAmountCents + 1, Target: charges.TargetActiveMembers}, charges.ErrInvalidAmount},
		{"no target", charges.Input{Concept: "x", AmountCents: 1}, charges.ErrInvalidTarget},
		{"unknown target", charges.Input{Concept: "x", AmountCents: 1, Target: "everyone"}, charges.ErrInvalidTarget},
		{"match_id with active_members", charges.Input{Concept: "x", AmountCents: 1, Target: charges.TargetActiveMembers, MatchID: &one}, charges.ErrInvalidTarget},
		{"match_attendees without match_id", charges.Input{Concept: "x", AmountCents: 1, Target: charges.TargetMatchAttendees}, charges.ErrInvalidTarget},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := f.svc.Create(context.Background(), f.team, f.admin, tt.in); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
	if n := countRows(t, f, "charge"); n != 0 {
		t.Fatalf("%d charge rows after invalid input, want 0", n)
	}
	// The largest amount is accepted.
	if _, err := f.svc.Create(context.Background(), f.team, f.admin, charges.Input{Concept: "x", AmountCents: charges.MaxAmountCents, Target: charges.TargetActiveMembers}); err != nil {
		t.Fatal(err)
	}
}

// The caller is checked under the team lock, like every admin write: a
// player is refused, and a member who left is not found.
func TestCreateRechecksTheCaller(t *testing.T) {
	f := newFixture(t)
	player := addMember(t, f, "player@example.com", "player", "active")
	left := addMember(t, f, "left@example.com", "admin", "left")
	if _, err := f.svc.Create(context.Background(), f.team, player, fee); !errors.Is(err, application.ErrNotAdmin) {
		t.Fatalf("player: %v, want ErrNotAdmin", err)
	}
	if _, err := f.svc.Create(context.Background(), f.team, left, fee); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("left admin: %v, want ErrNotFound", err)
	}
	if _, err := f.svc.Create(context.Background(), f.team+1, f.admin, fee); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown team: %v, want ErrNotFound", err)
	}
	if n := countRows(t, f, "charge"); n != 0 {
		t.Fatalf("%d charge rows, want 0", n)
	}
}

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
	"github.com/orrrrli/locker/api/internal/application/notifications"
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
	notifier := notifications.NewService(notifications.Deps{
		Tx: postgres.NewTxRunner(pool), Notifications: postgres.NewNotifications(pool),
		Memberships: members, Teams: postgres.NewTeams(pool), Now: func() time.Time { return now },
	})
	svc := charges.NewService(charges.Deps{
		Tx: postgres.NewTxRunner(pool), Charges: postgres.NewCharges(pool), Matches: matchRepo, Memberships: members,
		Notifier: notifier, Now: func() time.Time { return now },
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

// An admin marks an entry paid, which records who and when (R12.3), or
// pending again, which clears both. Repeating a status changes nothing: the
// first marker stays. A member who left still has an entry to mark (R12.6).
func TestSetStatus(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	player := addMember(t, f, "player@example.com", "player", "active")
	gone := addMember(t, f, "gone@example.com", "player", "left")
	m := playedMatch(t, f, player, gone)
	c, err := f.svc.Create(ctx, f.team, f.admin, charges.Input{Concept: "Cancha", AmountCents: 100, Target: charges.TargetMatchAttendees, MatchID: &m.ID})
	if err != nil {
		t.Fatal(err)
	}

	paid, err := f.svc.SetStatus(ctx, f.team, f.admin, c.ID, gone, domain.ChargePaid)
	if err != nil {
		t.Fatal(err)
	}
	want := f.now.UTC()
	if paid.ChargeID != c.ID || paid.MembershipID != gone || paid.Status != domain.ChargePaid ||
		paid.PaidMarkedBy == nil || *paid.PaidMarkedBy != f.admin || paid.PaidMarkedAt == nil || !paid.PaidMarkedAt.Equal(want) {
		t.Fatalf("paid = %+v, want marked by %d at %v", paid, f.admin, want)
	}
	// A second "paid" later, by another admin, keeps the first marker.
	other := addMember(t, f, "other@example.com", "admin", "active")
	*f.now = f.now.Add(time.Hour)
	again, err := f.svc.SetStatus(ctx, f.team, other, c.ID, gone, domain.ChargePaid)
	if err != nil {
		t.Fatal(err)
	}
	if *again.PaidMarkedBy != f.admin || !again.PaidMarkedAt.Equal(want) {
		t.Fatalf("repeated paid = %+v, want the first marker kept", again)
	}
	// The other entry is untouched.
	if e, err := f.svc.SetStatus(ctx, f.team, f.admin, c.ID, player, domain.ChargePending); err != nil || e.Status != domain.ChargePending || e.PaidMarkedBy != nil {
		t.Fatalf("pending on a pending entry = %+v, %v", e, err)
	}
	// Back to pending clears the marker.
	back, err := f.svc.SetStatus(ctx, f.team, f.admin, c.ID, gone, domain.ChargePending)
	if err != nil {
		t.Fatal(err)
	}
	if back.Status != domain.ChargePending || back.PaidMarkedBy != nil || back.PaidMarkedAt != nil {
		t.Fatalf("pending = %+v, want cleared", back)
	}

	var otherTeam int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO team (name, timezone) VALUES ('Otros', 'America/Mexico_City') RETURNING id`).Scan(&otherTeam); err != nil {
		t.Fatal(err)
	}
	var foreign int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO charge (team_id, concept, amount_cents, created_by) VALUES ($1, 'x', 1, $2) RETURNING id`, otherTeam, f.admin).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	notInCharge := addMember(t, f, "late@example.com", "player", "active")
	for _, tc := range []struct {
		name               string
		caller, charge, id int64
		status             domain.ChargeStatus
		want               error
	}{
		{"invalid status", f.admin, c.ID, player, "waived", charges.ErrInvalidStatus},
		{"player caller", player, c.ID, player, domain.ChargePaid, application.ErrNotAdmin},
		{"charge of another team", f.admin, foreign, player, domain.ChargePaid, domain.ErrNotFound},
		{"unknown charge", f.admin, 999_999, player, domain.ChargePaid, domain.ErrNotFound},
		{"membership not in the charge", f.admin, c.ID, notInCharge, domain.ChargePaid, domain.ErrNotFound},
	} {
		if _, err := f.svc.SetStatus(ctx, f.team, tc.caller, tc.charge, tc.id, tc.status); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
}

// An admin lists every charge with every entry, members who left included
// (R12.6); a player lists only the charges they are in, each with only their
// own entry (R12.4). Newest first.
func TestListByRole(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	player := addMember(t, f, "player@example.com", "player", "active")
	gone := addMember(t, f, "gone@example.com", "player", "left")
	m := playedMatch(t, f, f.admin, gone)
	matchFee, err := f.svc.Create(ctx, f.team, f.admin, charges.Input{Concept: "Cancha", AmountCents: 100, Target: charges.TargetMatchAttendees, MatchID: &m.ID})
	if err != nil {
		t.Fatal(err)
	}
	*f.now = f.now.Add(time.Minute)
	teamFee, err := f.svc.Create(ctx, f.team, f.admin, fee)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetStatus(ctx, f.team, f.admin, teamFee.ID, player, domain.ChargePaid); err != nil {
		t.Fatal(err)
	}
	members := postgres.NewMemberships(f.pool)
	adminM, err := members.MembershipInTeam(ctx, f.team, f.admin)
	if err != nil {
		t.Fatal(err)
	}
	playerM, err := members.MembershipInTeam(ctx, f.team, player)
	if err != nil {
		t.Fatal(err)
	}

	asAdmin, err := f.svc.List(ctx, adminM)
	if err != nil {
		t.Fatal(err)
	}
	if len(asAdmin) != 2 || asAdmin[0].ID != teamFee.ID || asAdmin[1].ID != matchFee.ID {
		t.Fatalf("admin list = %+v, want the team fee then the match fee", asAdmin)
	}
	if got, want := memberIDs(asAdmin[0].Members), []int64{f.admin, player}; !slices.Equal(got, want) {
		t.Fatalf("team fee members = %v, want %v", got, want)
	}
	if got, want := memberIDs(asAdmin[1].Members), []int64{f.admin, gone}; !slices.Equal(got, want) {
		t.Fatalf("match fee members = %v, want %v (the member who left still owes)", got, want)
	}

	asPlayer, err := f.svc.List(ctx, playerM)
	if err != nil {
		t.Fatal(err)
	}
	if len(asPlayer) != 1 || asPlayer[0].ID != teamFee.ID {
		t.Fatalf("player list = %+v, want only the team fee", asPlayer)
	}
	if got, want := memberIDs(asPlayer[0].Members), []int64{player}; !slices.Equal(got, want) {
		t.Fatalf("player sees entries %v, want only own %v", got, want)
	}
	if e := asPlayer[0].Members[0]; e.Status != domain.ChargePaid || e.PaidMarkedBy == nil || *e.PaidMarkedBy != f.admin {
		t.Fatalf("player's entry = %+v, want paid by the admin", e)
	}
}

// reminderRecipients returns the membership ids with a copy of the notice.
func reminderRecipients(t *testing.T, f fixture, notificationID int64) []int64 {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT membership_id FROM notification_recipient WHERE notification_id = $1 ORDER BY membership_id`, notificationID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// A reminder reaches only the active members with a pending entry (R12.5):
// not the one who paid, not the one who left, not a member outside the
// charge. Once everyone paid there is nobody to remind.
func TestRemindOnlyPendingMembers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	owes := addMember(t, f, "owes@example.com", "player", "active")
	paid := addMember(t, f, "paid@example.com", "player", "active")
	gone := addMember(t, f, "gone@example.com", "player", "active")
	c, err := f.svc.Create(ctx, f.team, f.admin, charges.Input{Concept: "Arbitraje", AmountCents: 5050, Target: charges.TargetActiveMembers})
	if err != nil {
		t.Fatal(err)
	}
	addMember(t, f, "late@example.com", "player", "active") // joined after the charge
	if _, err := f.svc.SetStatus(ctx, f.team, f.admin, c.ID, paid, domain.ChargePaid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetStatus(ctx, f.team, f.admin, c.ID, f.admin, domain.ChargePaid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE membership SET status = 'left' WHERE id = $1`, gone); err != nil {
		t.Fatal(err)
	}

	n, err := f.svc.Remind(ctx, f.team, f.admin, c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != domain.NotificationManual || n.SentBy == nil || *n.SentBy != f.admin ||
		n.Title != "Recordatorio de pago" || n.Body != "Tienes pendiente Arbitraje: $50.50 MXN." {
		t.Fatalf("notice = %+v", n)
	}
	if got := reminderRecipients(t, f, n.ID); !slices.Equal(got, []int64{owes}) {
		t.Fatalf("recipients = %v, want only the member who still owes", got)
	}

	if _, err := f.svc.SetStatus(ctx, f.team, f.admin, c.ID, owes, domain.ChargePaid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Remind(ctx, f.team, f.admin, c.ID); !errors.Is(err, notifications.ErrNoRecipients) {
		t.Fatalf("all paid: %v, want ErrNoRecipients", err)
	}
	// The admin owing alone is nobody to remind either.
	if _, err := f.svc.SetStatus(ctx, f.team, f.admin, c.ID, f.admin, domain.ChargePending); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Remind(ctx, f.team, f.admin, c.ID); !errors.Is(err, notifications.ErrNoRecipients) {
		t.Fatalf("only the sender owes: %v, want ErrNoRecipients", err)
	}
}

// A reminder is a manual notice: it counts against the team's daily limit
// (R11.3), and a refused one counts nothing. Players and other teams'
// charges are refused.
func TestRemindCountsAgainstTheDailyLimit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	player := addMember(t, f, "player@example.com", "player", "active")
	c, err := f.svc.Create(ctx, f.team, f.admin, fee)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < notifications.MaxManualPerDay; i++ {
		if _, err := f.svc.Remind(ctx, f.team, f.admin, c.ID); err != nil {
			t.Fatalf("reminder %d: %v", i+1, err)
		}
	}
	if _, err := f.svc.Remind(ctx, f.team, f.admin, c.ID); !errors.Is(err, notifications.ErrDailyLimit) {
		t.Fatalf("11th reminder: %v, want ErrDailyLimit", err)
	}
	if _, err := f.svc.Remind(ctx, f.team, player, c.ID); !errors.Is(err, application.ErrNotAdmin) {
		t.Fatalf("player: %v, want ErrNotAdmin", err)
	}
	if _, err := f.svc.Remind(ctx, f.team, f.admin, 999_999); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown charge: %v, want ErrNotFound", err)
	}
	if n := countRows(t, f, "notification"); n != notifications.MaxManualPerDay {
		t.Fatalf("%d notices stored, want %d", n, notifications.MaxManualPerDay)
	}
}

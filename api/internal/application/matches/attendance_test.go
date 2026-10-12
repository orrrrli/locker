package matches_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/matches"
	"github.com/orrrrli/locker/api/internal/domain"
)

// rsvpDirect inserts an answer straight into rsvp, bypassing the kickoff lock.
func rsvpDirect(t *testing.T, f fixture, matchID, membershipID int64, answer string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO rsvp (match_id, membership_id, answer) VALUES ($1, $2, $3)`, matchID, membershipID, answer); err != nil {
		t.Fatal(err)
	}
}

func attendees(t *testing.T, f fixture, matchID int64) []int64 {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT membership_id FROM attendance WHERE match_id = $1 ORDER BY membership_id`, matchID)
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

func closedAt(t *testing.T, f fixture, matchID int64) *time.Time {
	t.Helper()
	var at *time.Time
	if err := f.pool.QueryRow(context.Background(), `SELECT attendance_closed_at FROM match WHERE id = $1`, matchID).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

// addJoined inserts a membership that joined the team at some point, in the
// given status: left, or pending again after leaving.
func addJoined(t *testing.T, f fixture, email, status string) int64 {
	t.Helper()
	user := newUser(t, f.pool, email)
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO membership (team_id, user_id, role, status, joined_at) VALUES ($1, $2, 'player', $3, now()) RETURNING id`,
		f.team, user, status).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func addCharge(t *testing.T, f fixture, matchID int64) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO charge (team_id, concept, amount_cents, match_id, created_by) VALUES ($1, 'Arbitraje', 5000, $2, $3)`,
		f.team, matchID, f.admin); err != nil {
		t.Fatal(err)
	}
}

func newMatch(t *testing.T, f fixture) domain.Match {
	t.Helper()
	m, err := f.svc.Create(context.Background(), f.team, f.admin, kickoff, "Halcones", "Cancha 3")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// The job saves the going answers one hour after kickoff, exactly once
// (R10.1): a second tick, even with a new going answer, changes nothing.
func TestAutoSaveRunsOnceAfterOneHour(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := newMatch(t, f)
	going := addMember(t, f, "going@example.com", "player", "active")
	notGoing := addMember(t, f, "not@example.com", "player", "active")
	addMember(t, f, "silent@example.com", "player", "active")
	rsvpDirect(t, f, m.ID, going, "going")
	rsvpDirect(t, f, m.ID, notGoing, "not_going")

	*f.now = kickoff.Add(59 * time.Minute)
	if n, err := f.svc.CloseDueAttendance(ctx); err != nil || n != 0 {
		t.Fatalf("59 minutes after kickoff: closed %d, err %v; want 0", n, err)
	}
	if closedAt(t, f, m.ID) != nil {
		t.Fatal("closed before the hour")
	}

	*f.now = kickoff.Add(time.Hour)
	if n, err := f.svc.CloseDueAttendance(ctx); err != nil || n != 1 {
		t.Fatalf("one hour after kickoff: closed %d, err %v; want 1", n, err)
	}
	if got := attendees(t, f, m.ID); !slices.Equal(got, []int64{going}) {
		t.Fatalf("attendance = %v, want [%d]", got, going)
	}
	first := closedAt(t, f, m.ID)
	if first == nil || !first.Equal(kickoff.Add(time.Hour)) {
		t.Fatalf("closed_at = %v, want kickoff + 1h", first)
	}

	// A late going answer and a later tick: nothing changes.
	rsvpDirect(t, f, m.ID, f.admin, "going")
	*f.now = kickoff.Add(2 * time.Hour)
	if n, err := f.svc.CloseDueAttendance(ctx); err != nil || n != 0 {
		t.Fatalf("second tick: closed %d, err %v; want 0", n, err)
	}
	if got := attendees(t, f, m.ID); !slices.Equal(got, []int64{going}) {
		t.Fatalf("attendance after second tick = %v, want [%d]", got, going)
	}
	if got := closedAt(t, f, m.ID); !got.Equal(*first) {
		t.Fatalf("closed_at changed to %v", got)
	}
}

// The job counts only current active members: a going answer from someone
// who left, or who is pending again after leaving, is not copied.
func TestAutoSaveCopiesOnlyActiveMembers(t *testing.T) {
	f := newFixture(t)
	m := newMatch(t, f)
	active := addMember(t, f, "active@example.com", "player", "active")
	left := addJoined(t, f, "left@example.com", "left")
	rejoined := addJoined(t, f, "rejoined@example.com", "pending")
	for _, id := range []int64{active, left, rejoined} {
		rsvpDirect(t, f, m.ID, id, "going")
	}
	*f.now = kickoff.Add(time.Hour)
	if n, err := f.svc.CloseDueAttendance(context.Background()); err != nil || n != 1 {
		t.Fatalf("closed %d, err %v; want 1", n, err)
	}
	if got := attendees(t, f, m.ID); !slices.Equal(got, []int64{active}) {
		t.Fatalf("attendance = %v, want only the active member [%d]", got, active)
	}
}

// A match a charge already targets is locked for the job too (R10.4): it
// stays open and untouched.
func TestAutoSaveSkipsAChargedMatch(t *testing.T) {
	f := newFixture(t)
	m := newMatch(t, f)
	going := addMember(t, f, "going@example.com", "player", "active")
	rsvpDirect(t, f, m.ID, going, "going")
	addCharge(t, f, m.ID)
	*f.now = kickoff.Add(time.Hour)
	if n, err := f.svc.CloseDueAttendance(context.Background()); err != nil || n != 0 {
		t.Fatalf("closed %d, err %v; want 0", n, err)
	}
	if attendees(t, f, m.ID) != nil || closedAt(t, f, m.ID) != nil {
		t.Fatal("a charged match was auto-saved")
	}
}

// "Exactly once" under concurrency: a match another transaction holds (an
// admin saving it) is skipped, not waited for, and gets the next tick.
// Without SKIP LOCKED the job would block until the timeout.
func TestAutoSaveSkipsAMatchHeldByAnotherTransaction(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := newMatch(t, f)
	*f.now = kickoff.Add(time.Hour)

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `SELECT id FROM match WHERE id = $1 FOR UPDATE`, m.ID); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if n, err := f.svc.CloseDueAttendance(short); err != nil || n != 0 {
		t.Fatalf("while held: closed %d, err %v; want 0 and no error", n, err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := f.svc.CloseDueAttendance(ctx); err != nil || n != 1 {
		t.Fatalf("after release: closed %d, err %v; want 1", n, err)
	}
}

// A match with no going answers still closes, so the job never picks it again.
func TestAutoSaveClosesAMatchNobodyAnswered(t *testing.T) {
	f := newFixture(t)
	m := newMatch(t, f)
	*f.now = kickoff.Add(time.Hour)
	if n, err := f.svc.CloseDueAttendance(context.Background()); err != nil || n != 1 {
		t.Fatalf("closed %d, err %v; want 1", n, err)
	}
	if attendees(t, f, m.ID) != nil || closedAt(t, f, m.ID) == nil {
		t.Fatal("want no attendees and a closed match")
	}
}

// An admin who saved before the job keeps their list: the job skips the
// match (R10.2).
func TestAdminSaveBeforeTheJobIsKept(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := newMatch(t, f)
	going := addMember(t, f, "going@example.com", "player", "active")
	rsvpDirect(t, f, m.ID, going, "going")

	*f.now = kickoff.Add(10 * time.Minute)
	a, err := f.svc.SaveAttendance(ctx, f.team, f.admin, m.ID, []int64{f.admin, f.admin})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(a.MembershipIDs, []int64{f.admin}) || a.ClosedAt == nil || !a.ClosedAt.Equal(kickoff.Add(10*time.Minute)) || a.Locked {
		t.Fatalf("attendance = %+v", a)
	}

	*f.now = kickoff.Add(time.Hour)
	if n, err := f.svc.CloseDueAttendance(ctx); err != nil || n != 0 {
		t.Fatalf("job after an admin save: closed %d, err %v; want 0", n, err)
	}
	if got := attendees(t, f, m.ID); !slices.Equal(got, []int64{f.admin}) {
		t.Fatalf("attendance = %v, want the admin's list [%d]", got, f.admin)
	}
}

// A correction after the auto-save replaces the whole list (R10.3) and keeps
// the first close time.
func TestAdminCorrectionReplacesTheList(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := newMatch(t, f)
	going := addMember(t, f, "going@example.com", "player", "active")
	played := addMember(t, f, "played@example.com", "player", "active")
	rsvpDirect(t, f, m.ID, going, "going")

	*f.now = kickoff.Add(time.Hour)
	if _, err := f.svc.CloseDueAttendance(ctx); err != nil {
		t.Fatal(err)
	}
	*f.now = kickoff.Add(2 * time.Hour)
	a, err := f.svc.SaveAttendance(ctx, f.team, f.admin, m.ID, []int64{played, f.admin})
	if err != nil {
		t.Fatal(err)
	}
	if got := attendees(t, f, m.ID); !slices.Equal(got, []int64{f.admin, played}) || !slices.Equal(a.MembershipIDs, got) {
		t.Fatalf("attendance = %v (returned %v), want [%d %d]", got, a.MembershipIDs, f.admin, played)
	}
	if got := closedAt(t, f, m.ID); !got.Equal(kickoff.Add(time.Hour)) {
		t.Fatalf("closed_at = %v, want the auto-save time kept", got)
	}
	// Nobody played: an empty list is a valid correction.
	if _, err := f.svc.SaveAttendance(ctx, f.team, f.admin, m.ID, nil); err != nil {
		t.Fatal(err)
	}
	if got := attendees(t, f, m.ID); got != nil {
		t.Fatalf("attendance = %v, want none", got)
	}
}

// Someone who played, left and asked to rejoin is pending again but did
// join once, so the admin can keep them in the list.
func TestAdminKeepsARejoinedMemberWhoPlayed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := newMatch(t, f)
	rejoined := addJoined(t, f, "rejoined@example.com", "pending")
	left := addJoined(t, f, "left@example.com", "left")
	*f.now = kickoff.Add(2 * time.Hour)
	a, err := f.svc.SaveAttendance(ctx, f.team, f.admin, m.ID, []int64{rejoined, left})
	if err != nil {
		t.Fatal(err)
	}
	if got := attendees(t, f, m.ID); !slices.Equal(got, a.MembershipIDs) || len(got) != 2 {
		t.Fatalf("attendance = %v, want [%d %d]", got, rejoined, left)
	}
}

func TestSaveAttendanceRejections(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	m := newMatch(t, f)
	player := addMember(t, f, "player@example.com", "player", "active")
	pending := addMember(t, f, "pending@example.com", "player", "pending")
	var otherTeam, otherMember int64
	if err := f.pool.QueryRow(ctx, `INSERT INTO team (name, timezone) VALUES ('Tigres', 'America/Tijuana') RETURNING id`).Scan(&otherTeam); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `INSERT INTO membership (team_id, role, status) VALUES ($1, 'player', 'active') RETURNING id`, otherTeam).Scan(&otherMember); err != nil {
		t.Fatal(err)
	}

	if _, err := f.svc.SaveAttendance(ctx, f.team, f.admin, m.ID, []int64{player}); !errors.Is(err, matches.ErrMatchNotStarted) {
		t.Fatalf("before kickoff: err = %v, want ErrMatchNotStarted", err)
	}
	*f.now = kickoff.Add(time.Minute)
	for _, tc := range []struct {
		name   string
		caller int64
		ids    []int64
		want   error
	}{
		{"player caller", player, []int64{player}, application.ErrNotAdmin},
		{"attendee who never joined", f.admin, []int64{player, pending}, matches.ErrInvalidAttendee},
		{"other team's member", f.admin, []int64{otherMember}, matches.ErrInvalidAttendee},
		{"unknown id", f.admin, []int64{999_999}, matches.ErrInvalidAttendee},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := f.svc.SaveAttendance(ctx, f.team, tc.caller, m.ID, tc.ids); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
	if _, err := f.svc.SaveAttendance(ctx, f.team, f.admin, m.ID+1, []int64{player}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown match: err = %v, want ErrNotFound", err)
	}
	if attendees(t, f, m.ID) != nil || closedAt(t, f, m.ID) != nil {
		t.Fatal("a rejected save changed the match")
	}

	// Once a charge targets the match, the list is locked (R10.4).
	if _, err := f.svc.SaveAttendance(ctx, f.team, f.admin, m.ID, []int64{player}); err != nil {
		t.Fatal(err)
	}
	addCharge(t, f, m.ID)
	if _, err := f.svc.SaveAttendance(ctx, f.team, f.admin, m.ID, []int64{f.admin}); !errors.Is(err, matches.ErrAttendanceLocked) {
		t.Fatalf("with a charge: err = %v, want ErrAttendanceLocked", err)
	}
	if got := attendees(t, f, m.ID); !slices.Equal(got, []int64{player}) {
		t.Fatalf("attendance = %v, want [%d] unchanged", got, player)
	}
	a, err := f.svc.GetAttendance(ctx, f.team, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Locked || !slices.Equal(a.MembershipIDs, []int64{player}) || a.ClosedAt == nil {
		t.Fatalf("get = %+v, want locked with [%d]", a, player)
	}
}

package matches_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application/matches"
	"github.com/orrrrli/locker/api/internal/application/notifications"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
)

// recorder is the real notice service, but Deliver records each notice and
// whether it was committed by then, as seen from another connection.
type recorder struct {
	*notifications.Service
	f         fixture
	delivered []domain.Notification
	committed []bool
}

func (r *recorder) Deliver(ctx context.Context, n domain.Notification) {
	var found bool
	if err := r.f.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notification WHERE id = $1)`, n.ID).Scan(&found); err != nil {
		panic(err)
	}
	r.delivered = append(r.delivered, n)
	r.committed = append(r.committed, found)
}

func withRecorder(t *testing.T, f fixture) (*matches.Service, *recorder) {
	t.Helper()
	r := &recorder{
		Service: notifications.NewService(notifications.Deps{
			Tx: postgres.NewTxRunner(f.pool), Notifications: postgres.NewNotifications(f.pool),
		}),
		f: f,
	}
	svc := matches.NewService(matches.Deps{
		Tx: postgres.NewTxRunner(f.pool), Matches: postgres.NewMatches(f.pool), Memberships: postgres.NewMemberships(f.pool),
		Teams: postgres.NewTeams(f.pool), Notices: r, Now: func() time.Time { return *f.now },
	})
	return svc, r
}

type storedNotice struct {
	kind, title, body string
	matchID           int64
	recipients        []int64
}

// notices returns the team's automatic notices, oldest first, with who got
// each one.
func notices(t *testing.T, f fixture) []storedNotice {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `
		SELECT n.kind, n.title, n.body, n.match_id, array_agg(r.membership_id ORDER BY r.membership_id)
		FROM notification n JOIN notification_recipient r ON r.notification_id = n.id
		WHERE n.team_id = $1 GROUP BY n.id ORDER BY n.id`, f.team)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []storedNotice
	for rows.Next() {
		var n storedNotice
		if err := rows.Scan(&n.kind, &n.title, &n.body, &n.matchID, &n.recipients); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestCreateSendsMatchCreated: every active member gets a match_created
// notice in the team's zone, written with the match and pushed only after
// commit (R11.4). Pending and left members get nothing.
func TestCreateSendsMatchCreated(t *testing.T) {
	f := newFixture(t)
	svc, rec := withRecorder(t, f)
	player := addMember(t, f, "beto@example.com", "player", "active")
	addMember(t, f, "caro@example.com", "player", "pending")
	addMember(t, f, "dani@example.com", "player", "left")

	m, err := svc.Create(context.Background(), f.team, f.admin, kickoff, "Halcones", "Cancha 3")
	if err != nil {
		t.Fatal(err)
	}
	got := notices(t, f)
	want := storedNotice{"match_created", "Partido nuevo: vs. Halcones", "Mar 20 · 19:00 · Cancha 3.", m.ID, []int64{f.admin, player}}
	if len(got) != 1 || got[0].kind != want.kind || got[0].title != want.title || got[0].body != want.body ||
		got[0].matchID != want.matchID || !slices.Equal(got[0].recipients, want.recipients) {
		t.Fatalf("notices = %+v, want [%+v]", got, want)
	}
	if len(rec.delivered) != 1 || !rec.committed[0] || rec.delivered[0].Title != want.title {
		t.Fatalf("delivered %+v (committed %v), want the notice once, after commit", rec.delivered, rec.committed)
	}
}

// TestUpdateNoticeOnlyForTimeOrPlace: a new time or place sends
// match_changed with the new values (R11.4); the rival alone, or resending
// the current values, sends nothing.
func TestUpdateNoticeOnlyForTimeOrPlace(t *testing.T) {
	f := newFixture(t)
	svc, rec := withRecorder(t, f)
	ctx := context.Background()
	m, err := svc.Create(ctx, f.team, f.admin, kickoff, "Halcones", "Cancha 3")
	if err != nil {
		t.Fatal(err)
	}
	str := func(s string) *string { return &s }
	at := func(t time.Time) *time.Time { return &t }

	steps := []struct {
		name     string
		startsAt *time.Time
		rival    *string
		location *string
		want     *storedNotice // nil: no new notice
	}{
		{"rival only", nil, str("Tigres"), nil, nil},
		{"same time and place resent", at(kickoff.UTC()), nil, str(" Cancha 3 "), nil},
		{"new time", at(kickoff.Add(26 * time.Hour)), nil, nil,
			&storedNotice{kind: "match_changed", title: "Cambio de partido: vs. Tigres", body: "Mié 21 · 21:00 · Cancha 3."}},
		{"new place", nil, nil, str("Deportivo Vicente Suárez, cancha 2."), // a trailing period is not doubled
			&storedNotice{kind: "match_changed", title: "Cambio de partido: vs. Tigres", body: "Mié 21 · 21:00 · Deportivo Vicente Suárez, cancha 2."}},
		{"rival and time", at(kickoff), str("Pumas"), nil,
			&storedNotice{kind: "match_changed", title: "Cambio de partido: vs. Pumas", body: "Mar 20 · 19:00 · Deportivo Vicente Suárez, cancha 2."}},
	}
	count := 1 // the match_created notice
	for _, s := range steps {
		if _, err := svc.Update(ctx, f.team, f.admin, m.ID, s.startsAt, s.rival, s.location); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		got := notices(t, f)
		if s.want == nil {
			if len(got) != count || len(rec.delivered) != count {
				t.Fatalf("%s: %d notices, %d delivered; want %d", s.name, len(got), len(rec.delivered), count)
			}
			continue
		}
		count++
		if len(got) != count {
			t.Fatalf("%s: %d notices, want %d", s.name, len(got), count)
		}
		last := got[count-1]
		if last.kind != s.want.kind || last.title != s.want.title || last.body != s.want.body || last.matchID != m.ID {
			t.Fatalf("%s: notice = %+v, want %+v", s.name, last, *s.want)
		}
		if len(rec.delivered) != count || !rec.committed[count-1] {
			t.Fatalf("%s: delivered %d (committed %v), want %d after commit", s.name, len(rec.delivered), rec.committed, count)
		}
	}
}

// TestRejectedUpdateSendsNothing: a change the use case refuses stores no
// notice and pushes nothing.
func TestRejectedUpdateSendsNothing(t *testing.T) {
	f := newFixture(t)
	svc, rec := withRecorder(t, f)
	ctx := context.Background()
	m, err := svc.Create(ctx, f.team, f.admin, kickoff, "Halcones", "Cancha 3")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE match SET attendance_closed_at = now() WHERE id = $1`, m.ID); err != nil {
		t.Fatal(err)
	}
	later := kickoff.Add(time.Hour)
	if _, err := svc.Update(ctx, f.team, f.admin, m.ID, &later, nil, nil); err == nil {
		t.Fatal("moving a match with saved attendance succeeded")
	}
	if got := notices(t, f); len(got) != 1 || len(rec.delivered) != 1 {
		t.Fatalf("%d notices, %d delivered; want only the match_created one", len(got), len(rec.delivered))
	}
}

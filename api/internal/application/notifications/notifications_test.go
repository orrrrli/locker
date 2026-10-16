package notifications_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/notifications"
	"github.com/orrrrli/locker/api/internal/application/teams"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
	"github.com/orrrrli/locker/api/internal/testdb"
)

type fixture struct {
	pool  *pgxpool.Pool
	team  int64
	admin int64 // the creator's membership
	user  int64 // the creator
}

// newFixture builds a fresh database with one team and its admin.
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
	return fixture{pool: pool, team: team.ID, admin: admin.ID, user: user}
}

func (f fixture) service(push notifications.Pusher) *notifications.Service {
	return f.serviceWith(postgres.NewNotifications(f.pool), push)
}

func (f fixture) serviceWith(repo notifications.Notifications, push notifications.Pusher) *notifications.Service {
	return notifications.NewService(notifications.Deps{Tx: postgres.NewTxRunner(f.pool), Notifications: repo, Push: push})
}

func newUser(t *testing.T, pool *pgxpool.Pool, email string) int64 {
	t.Helper()
	id, err := postgres.NewUsers(pool).CreateUser(context.Background(), "Ana", email, time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// addMember inserts a player of the team in the given status and returns
// the membership and its user.
func addMember(t *testing.T, f fixture, email, status string) (membership, user int64) {
	t.Helper()
	user = newUser(t, f.pool, email)
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO membership (team_id, user_id, role, status, joined_at) VALUES ($1, $2, 'player', $3, now()) RETURNING id`,
		f.team, user, status).Scan(&membership); err != nil {
		t.Fatal(err)
	}
	return membership, user
}

func addToken(t *testing.T, f fixture, user int64, token string) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO device_token (user_id, token) VALUES ($1, $2)`, user, token); err != nil {
		t.Fatal(err)
	}
}

// recipients returns the membership ids with a copy of the notice, read or
// not as asked.
func recipients(t *testing.T, f fixture, notificationID int64, read bool) []int64 {
	t.Helper()
	rows, err := f.pool.Query(context.Background(),
		`SELECT membership_id FROM notification_recipient WHERE notification_id = $1 AND (read_at IS NOT NULL) = $2 ORDER BY membership_id`, notificationID, read)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// fakePusher records the push. With pool set it also checks, from outside
// any transaction, whether the notice is already committed.
type fakePusher struct {
	pool      *pgxpool.Pool
	calls     int
	n         domain.Notification
	tokens    []string
	committed bool
	err       error // from the committed check
}

func (p *fakePusher) Push(ctx context.Context, n domain.Notification, tokens []string) {
	p.calls++
	p.n, p.tokens = n, tokens
	if p.pool != nil {
		p.err = p.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notification_recipient WHERE notification_id = $1)`, n.ID).Scan(&p.committed)
	}
}

func manual(f fixture) domain.Notification {
	return domain.Notification{TeamID: f.team, Kind: domain.NotificationManual, Title: "Cancha 3", Body: "Hoy cambiamos de cancha", SentBy: &f.admin}
}

// Every active member gets an unread copy, except the sender, whose copy
// starts read; pending and left members get nothing (R11.1, R11.2).
func TestSendStoresOneRecipientPerActiveMember(t *testing.T) {
	f := newFixture(t)
	beto, _ := addMember(t, f, "beto@example.com", "active")
	addMember(t, f, "carlos@example.com", "pending")
	addMember(t, f, "diego@example.com", "left")

	n, err := f.service(nil).Send(context.Background(), manual(f))
	if err != nil {
		t.Fatal(err)
	}
	if n.ID == 0 || n.TeamID != f.team || n.Kind != domain.NotificationManual || n.SentBy == nil || *n.SentBy != f.admin || n.CreatedAt.IsZero() {
		t.Fatalf("notification = %+v", n)
	}
	if got, want := recipients(t, f, n.ID, false), []int64{beto}; !slices.Equal(got, want) {
		t.Fatalf("unread recipients = %v, want %v", got, want)
	}
	if got, want := recipients(t, f, n.ID, true), []int64{f.admin}; !slices.Equal(got, want) {
		t.Fatalf("read recipients = %v, want the sender %v", got, want)
	}
}

// After commit (checked by the pusher from outside the transaction), push goes
// to every device of the recipients who have not muted the team, and to
// nobody else: not the sender, not a muted or pending member.
func TestSendPushesToUnmutedRecipients(t *testing.T) {
	f := newFixture(t)
	addToken(t, f, f.user, "ana-iphone") // the sender
	_, eva := addMember(t, f, "eva@example.com", "active")
	addToken(t, f, eva, "eva-iphone")
	addToken(t, f, eva, "eva-ipad")
	muted, mutedUser := addMember(t, f, "beto@example.com", "active")
	addToken(t, f, mutedUser, "beto-iphone")
	if _, err := f.pool.Exec(context.Background(), `UPDATE membership SET push_muted = true WHERE id = $1`, muted); err != nil {
		t.Fatal(err)
	}
	_, pendingUser := addMember(t, f, "carlos@example.com", "pending")
	addToken(t, f, pendingUser, "carlos-iphone")

	push := &fakePusher{pool: f.pool}
	n, err := f.service(push).Send(context.Background(), manual(f))
	if err != nil {
		t.Fatal(err)
	}
	if push.err != nil {
		t.Fatal(push.err)
	}
	if !push.committed {
		t.Fatal("push ran before the notice was committed")
	}
	if push.calls != 1 || push.n.ID != n.ID {
		t.Fatalf("push calls = %d with %+v, want 1 with notification %d", push.calls, push.n, n.ID)
	}
	if want := []string{"eva-iphone", "eva-ipad"}; !slices.Equal(push.tokens, want) {
		t.Fatalf("tokens = %v, want %v", push.tokens, want)
	}
	// Muting stops push only; the muted member still has it in the inbox (R11.6).
	if got := recipients(t, f, n.ID, false); !slices.Contains(got, muted) {
		t.Fatalf("recipients = %v, want muted member %d included", got, muted)
	}
}

// With no device to reach, the pusher is not called at all.
func TestSendSkipsPushWithoutTokens(t *testing.T) {
	f := newFixture(t)
	push := &fakePusher{}
	if _, err := f.service(push).Send(context.Background(), manual(f)); err != nil {
		t.Fatal(err)
	}
	if push.calls != 0 {
		t.Fatalf("push calls = %d, want 0", push.calls)
	}
}

// failAfterInsert runs the real insert, notice and recipients, then fails.
type failAfterInsert struct{ *postgres.Notifications }

var errAfterInsert = errors.New("fail after insert")

func (r failAfterInsert) InsertNotification(ctx context.Context, n domain.Notification) (domain.Notification, error) {
	if _, err := r.Notifications.InsertNotification(ctx, n); err != nil {
		return domain.Notification{}, err
	}
	return domain.Notification{}, errAfterInsert
}

// The notice and its recipients go in one transaction: a failure after both
// are written leaves neither behind, and nothing is pushed (R11.1).
func TestSendRollsBackOnFailure(t *testing.T) {
	f := newFixture(t)
	_, beto := addMember(t, f, "beto@example.com", "active")
	addToken(t, f, beto, "beto-iphone")

	push := &fakePusher{}
	svc := f.serviceWith(failAfterInsert{postgres.NewNotifications(f.pool)}, push)
	if _, err := svc.Send(context.Background(), manual(f)); !errors.Is(err, errAfterInsert) {
		t.Fatalf("err = %v, want errAfterInsert", err)
	}
	var count int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM notification) + (SELECT count(*) FROM notification_recipient)`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 || push.calls != 0 {
		t.Fatalf("rows = %d, push calls = %d, want 0 and 0", count, push.calls)
	}
}

// An automatic notice has no sender and can point at its match. With no
// sender, every active member gets an unread copy and the push: the sender
// filter must treat a null sent_by as "nobody to leave out".
func TestSendAutomaticNotice(t *testing.T) {
	f := newFixture(t)
	var match int64
	if err := f.pool.QueryRow(context.Background(), `INSERT INTO match (team_id, starts_at, rival_name, location, created_by)
		VALUES ($1, now(), 'Halcones', 'Cancha 3', $2) RETURNING id`, f.team, f.admin).Scan(&match); err != nil {
		t.Fatal(err)
	}
	addToken(t, f, f.user, "ana-iphone")
	push := &fakePusher{}
	n, err := f.service(push).Send(context.Background(), domain.Notification{
		TeamID: f.team, Kind: domain.NotificationMatchCreated, Title: "Nuevo partido", Body: "vs Halcones", MatchID: &match,
	})
	if err != nil {
		t.Fatal(err)
	}
	if n.SentBy != nil || n.MatchID == nil || *n.MatchID != match || n.Kind != domain.NotificationMatchCreated {
		t.Fatalf("notification = %+v", n)
	}
	if got := recipients(t, f, n.ID, false); !slices.Equal(got, []int64{f.admin}) {
		t.Fatalf("recipients = %v, want [%d]", got, f.admin)
	}
	if want := []string{"ana-iphone"}; !slices.Equal(push.tokens, want) {
		t.Fatalf("tokens = %v, want %v", push.tokens, want)
	}
}

// manualService wires SendManual's extra dependencies on the fixture with a
// clock the test moves.
func (f fixture) manualService(now *time.Time, push notifications.Pusher) *notifications.Service {
	return notifications.NewService(notifications.Deps{
		Tx: postgres.NewTxRunner(f.pool), Notifications: postgres.NewNotifications(f.pool), Push: push,
		Memberships: postgres.NewMemberships(f.pool), Teams: postgres.NewTeams(f.pool),
		Now: func() time.Time { return *now },
	})
}

// A manual notice from an admin reaches every active member, the sender's
// copy read, and is pushed after commit like any notice (R11.2, R11.6).
func TestSendManualGoesToActiveMembers(t *testing.T) {
	f := newFixture(t)
	player, playerUser := addMember(t, f, "player@example.com", "active")
	addMember(t, f, "pending@example.com", "pending")
	addToken(t, f, playerUser, "tok-player")
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	push := &fakePusher{pool: f.pool}

	n, err := f.manualService(&now, push).SendManual(context.Background(), f.team, f.admin, "  Cancha 3 ", "Hoy cambiamos\r\nde cancha")
	if err != nil {
		t.Fatal(err)
	}
	if n.TeamID != f.team || n.Kind != domain.NotificationManual || n.Title != "Cancha 3" || n.Body != "Hoy cambiamos\nde cancha" ||
		n.SentBy == nil || *n.SentBy != f.admin || n.MatchID != nil {
		t.Fatalf("notice = %+v", n)
	}
	if got := recipients(t, f, n.ID, false); !slices.Equal(got, []int64{player}) {
		t.Fatalf("unread recipients = %v, want the player only", got)
	}
	if got := recipients(t, f, n.ID, true); !slices.Equal(got, []int64{f.admin}) {
		t.Fatalf("read recipients = %v, want the sender only", got)
	}
	if push.calls != 1 || !slices.Equal(push.tokens, []string{"tok-player"}) || !push.committed || push.err != nil {
		t.Fatalf("push = %+v, want one committed push to the player", push)
	}
}

// The team sends 10 manual notices per local day in its timezone (R11.3).
// Pumas is in America/Tijuana (UTC-7 in October): at 23:30 local the day's
// budget is spent, and at 00:30 local, still the same UTC day, it is new.
// Automatic notices never count.
func TestSendManualDailyLimitInTeamTimezone(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	// 2026-10-20 06:30 UTC is 2026-10-19 23:30 in Tijuana.
	now := time.Date(2026, 10, 20, 6, 30, 0, 0, time.UTC)
	svc := f.manualService(&now, nil)
	if _, err := svc.Send(ctx, domain.Notification{TeamID: f.team, Kind: domain.NotificationMatchCreated, Title: "Partido", Body: "vs Halcones"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < notifications.MaxManualPerDay; i++ {
		if _, err := svc.SendManual(ctx, f.team, f.admin, "Aviso", "x"); err != nil {
			t.Fatalf("notice %d: %v", i+1, err)
		}
	}
	if _, err := svc.SendManual(ctx, f.team, f.admin, "Aviso", "x"); !errors.Is(err, notifications.ErrDailyLimit) {
		t.Fatalf("11th notice: %v, want ErrDailyLimit", err)
	}
	// 23:59:59 local: still today.
	now = time.Date(2026, 10, 20, 6, 59, 59, 0, time.UTC)
	if _, err := svc.SendManual(ctx, f.team, f.admin, "Aviso", "x"); !errors.Is(err, notifications.ErrDailyLimit) {
		t.Fatalf("at 23:59:59 local: %v, want ErrDailyLimit", err)
	}
	// 00:00 local: a new day, though the UTC date has not changed.
	now = time.Date(2026, 10, 20, 7, 0, 0, 0, time.UTC)
	if _, err := svc.SendManual(ctx, f.team, f.admin, "Aviso", "x"); err != nil {
		t.Fatalf("at 00:00 local: %v, want a new day's budget", err)
	}
	// The limit is per team: another team is untouched.
	otherUser := newUser(t, f.pool, "bob@example.com")
	other, err := teams.NewService(teams.Deps{Tx: postgres.NewTxRunner(f.pool), Teams: postgres.NewTeams(f.pool), Memberships: postgres.NewMemberships(f.pool)}).
		Create(ctx, otherUser, "Otros", "America/Tijuana")
	if err != nil {
		t.Fatal(err)
	}
	otherAdmin, err := postgres.NewMemberships(f.pool).MembershipByTeamAndUser(ctx, other.ID, otherUser)
	if err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 10, 20, 6, 30, 0, 0, time.UTC)
	if _, err := svc.SendManual(ctx, other.ID, otherAdmin.ID, "Aviso", "x"); err != nil {
		t.Fatalf("other team: %v", err)
	}
}

func TestSendManualValidatesAndRechecksTheCaller(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	player, _ := addMember(t, f, "player@example.com", "active")
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	svc := f.manualService(&now, nil)
	long := strings.Repeat("a", 1001)
	for _, tc := range []struct {
		name, title, body string
		caller            int64
		want              error
	}{
		{"empty title", " ", "x", f.admin, notifications.ErrInvalidTitle},
		{"long title", strings.Repeat("a", 81), "x", f.admin, notifications.ErrInvalidTitle},
		{"title with a line break", "a\nb", "x", f.admin, notifications.ErrInvalidTitle},
		{"empty body", "x", "\n\n", f.admin, notifications.ErrInvalidBody},
		{"long body", "x", long, f.admin, notifications.ErrInvalidBody},
		{"body with a control character", "x", "a\x00b", f.admin, notifications.ErrInvalidBody},
		{"body with an invisible character", "x", "a​b", f.admin, notifications.ErrInvalidBody},
		{"player caller", "x", "x", player, application.ErrNotAdmin},
		{"unknown caller", "x", "x", 999_999, domain.ErrNotFound},
	} {
		if _, err := svc.SendManual(ctx, f.team, tc.caller, tc.title, tc.body); !errors.Is(err, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	var n int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM notification`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d notices stored after refused sends (%v), want 0", n, err)
	}
}

// reminderService wires SendDueReminders' dependencies on the fixture with a
// clock the test moves.
func (f fixture) reminderService(now *time.Time, push notifications.Pusher) *notifications.Service {
	return notifications.NewService(notifications.Deps{
		Tx: postgres.NewTxRunner(f.pool), Notifications: postgres.NewNotifications(f.pool), Push: push,
		Memberships: postgres.NewMemberships(f.pool), Teams: postgres.NewTeams(f.pool), Matches: postgres.NewMatches(f.pool),
		Now: func() time.Time { return *now },
	})
}

// addMatch inserts a match of the team kicking off at startsAt.
func addMatch(t *testing.T, f fixture, startsAt time.Time) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`INSERT INTO match (team_id, starts_at, rival_name, location, created_by) VALUES ($1, $2, 'Halcones', 'Cancha 3', $3) RETURNING id`,
		f.team, startsAt, f.admin).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func rsvp(t *testing.T, f fixture, matchID, membershipID int64) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO rsvp (match_id, membership_id, answer) VALUES ($1, $2, 'going')`, matchID, membershipID); err != nil {
		t.Fatal(err)
	}
}

// reminders returns the reminder notices about the match, oldest first.
func reminders(t *testing.T, f fixture, matchID int64) []int64 {
	t.Helper()
	rows, err := f.pool.Query(context.Background(), `SELECT id FROM notification WHERE match_id = $1 AND kind = 'rsvp_reminder' ORDER BY id`, matchID)
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

func reminderSentAt(t *testing.T, f fixture, matchID int64) *time.Time {
	t.Helper()
	var at *time.Time
	if err := f.pool.QueryRow(context.Background(), `SELECT reminder_sent_at FROM match WHERE id = $1`, matchID).Scan(&at); err != nil {
		t.Fatal(err)
	}
	return at
}

// The job reminds the active members who have not answered a match that
// kicks off within 24 hours, once (R11.5): a second run changes nothing,
// members who answered, pending and left members get nothing, and matches
// further out or already started are left alone.
func TestSendDueRemindersOnlyToMembersWithoutRSVP(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	answered, _ := addMember(t, f, "answered@example.com", "active")
	silent, silentUser := addMember(t, f, "silent@example.com", "active")
	addMember(t, f, "pending@example.com", "pending")
	addMember(t, f, "left@example.com", "left")
	addToken(t, f, silentUser, "tok-silent")
	soon := addMatch(t, f, now.Add(2*time.Hour))
	later := addMatch(t, f, now.Add(30*time.Hour))
	started := addMatch(t, f, now.Add(-time.Minute))
	rsvp(t, f, soon, answered)
	push := &fakePusher{pool: f.pool}
	svc := f.reminderService(&now, push)

	n, err := svc.SendDueReminders(ctx)
	if err != nil || n != 1 {
		t.Fatalf("SendDueReminders = %d, %v; want 1 match", n, err)
	}
	ids := reminders(t, f, soon)
	if len(ids) != 1 {
		t.Fatalf("reminders = %v, want one", ids)
	}
	if got := recipients(t, f, ids[0], false); !slices.Equal(got, []int64{f.admin, silent}) {
		t.Fatalf("recipients = %v, want the admin and the silent member, unread", got)
	}
	var title, body string
	if err := f.pool.QueryRow(ctx, `SELECT title, body FROM notification WHERE id = $1`, ids[0]).Scan(&title, &body); err != nil {
		t.Fatal(err)
	}
	// 12:00 UTC is 05:00 in Tijuana.
	if title != "¿Vas al partido vs Halcones?" || body != "20/10/2026 07:00 en Cancha 3. Aún no has respondido." {
		t.Fatalf("title = %q, body = %q", title, body)
	}
	if at := reminderSentAt(t, f, soon); at == nil || !at.Equal(now) {
		t.Fatalf("reminder_sent_at = %v, want %v", at, now)
	}
	if push.calls != 1 || !slices.Equal(push.tokens, []string{"tok-silent"}) || !push.committed {
		t.Fatalf("push = %+v, want one committed push to the silent member", push)
	}
	if reminderSentAt(t, f, later) != nil || reminderSentAt(t, f, started) != nil || len(reminders(t, f, later)) != 0 || len(reminders(t, f, started)) != 0 {
		t.Fatal("a match more than 24 hours out, or already started, was reminded")
	}

	// A second tick, even after a new silent member joins, changes nothing.
	addMember(t, f, "late@example.com", "active")
	now = now.Add(time.Minute)
	if n, err := svc.SendDueReminders(ctx); err != nil || n != 0 {
		t.Fatalf("second run = %d, %v; want 0", n, err)
	}
	if len(reminders(t, f, soon)) != 1 || push.calls != 1 {
		t.Fatal("the second run reminded again")
	}
	// Once inside the window, the later match is reminded too; everyone has
	// answered it, so it is marked with no notice.
	now = now.Add(7 * time.Hour)
	rsvp(t, f, later, f.admin)
	rsvp(t, f, later, answered)
	rsvp(t, f, later, silent)
	rsvp(t, f, later, f.lateMember(t))
	if n, err := svc.SendDueReminders(ctx); err != nil || n != 1 {
		t.Fatalf("later run = %d, %v; want 1", n, err)
	}
	if at := reminderSentAt(t, f, later); at == nil || len(reminders(t, f, later)) != 0 {
		t.Fatalf("all-answered match: reminder_sent_at = %v, reminders = %v; want marked with no notice", at, reminders(t, f, later))
	}
}

// lateMember returns the membership of late@example.com.
func (f fixture) lateMember(t *testing.T) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(),
		`SELECT m.id FROM membership m JOIN "user" u ON u.id = m.user_id WHERE m.team_id = $1 AND u.email = 'late@example.com'`, f.team).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// Two runners at once (two ticks, two API instances) send exactly one
// reminder per match: SKIP LOCKED hands each due match to one of them, and
// the holder marks it before it commits.
func TestSendDueRemindersExactlyOnceUnderConcurrency(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	addMember(t, f, "silent@example.com", "active")
	const matches = 5
	var due []int64
	for i := 0; i < matches; i++ {
		due = append(due, addMatch(t, f, now.Add(time.Duration(i+1)*time.Hour)))
	}
	const runners = 8
	var wg sync.WaitGroup
	var marked atomic.Int64
	start := make(chan struct{})
	for i := 0; i < runners; i++ {
		svc := f.reminderService(&now, nil)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			n, err := svc.SendDueReminders(ctx)
			if err != nil {
				t.Error(err)
			}
			marked.Add(n)
		}()
	}
	close(start)
	wg.Wait()
	if marked.Load() != matches {
		t.Fatalf("matches marked across runners = %d, want %d", marked.Load(), matches)
	}
	for _, m := range due {
		if ids := reminders(t, f, m); len(ids) != 1 {
			t.Fatalf("match %d has %d reminders, want exactly 1", m, len(ids))
		}
	}
}

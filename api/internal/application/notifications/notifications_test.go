package notifications_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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

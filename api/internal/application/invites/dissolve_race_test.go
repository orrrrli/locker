package invites_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/application/invites"
	"github.com/orrrrli/locker/api/internal/application/teams"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
)

// pausedInvites stops Accept right after it reads the invite, holding FOR
// SHARE on it, until release is closed.
type pausedInvites struct {
	*postgres.Invites
	read    chan struct{}
	release chan struct{}
}

func (p pausedInvites) InviteByTokenHash(ctx context.Context, tokenHash []byte) (domain.Invite, string, error) {
	inv, name, err := p.Invites.InviteByTokenHash(ctx, tokenHash)
	close(p.read)
	<-p.release
	return inv, name, err
}

// waitForRevokeWait polls until the dissolve's invite UPDATE waits on a lock
// in this test's database, or fails after five seconds. Only that statement
// counts: a dissolve blocked anywhere else (the team lock) would let the test
// pass without proving the statement order. A dissolve that returns first
// fails the test with its own error.
func waitForRevokeWait(t *testing.T, pool *pgxpool.Pool, dissolveErr <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-dissolveErr:
			t.Fatalf("dissolve returned before waiting on the invite: %v", err)
		default:
		}
		var n int
		if err := pool.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity
			 WHERE datname = current_database() AND wait_event_type = 'Lock'
			   AND query LIKE '%UPDATE invite%'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the dissolve's invite revoke never waited on a lock within 5s")
}

// TestAcceptRacingDissolveLeavesNoPending: an accept that read the invite
// before the dissolve started still commits its pending row, and the dissolve
// rejects it, so a dissolved team never keeps a pending member (R6.8). The
// dissolve's revoke waits on the accept's FOR SHARE, and its later statements
// see the committed row; revoking after the pending statements would miss it.
func TestAcceptRacingDissolveLeavesNoPending(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	inv := f.invite(t)
	joiner := newUser(t, f.pool, "joiner@example.com")

	paused := pausedInvites{Invites: postgres.NewInvites(f.pool), read: make(chan struct{}), release: make(chan struct{})}
	accepting := invites.NewService(invites.Deps{
		Tx:          postgres.NewTxRunner(f.pool),
		Invites:     paused,
		Memberships: f.members,
		Now:         func() time.Time { return *f.now },
	})
	// Release the accept on any exit, before the test database is dropped.
	var once sync.Once
	release := func() { once.Do(func() { close(paused.release) }) }
	t.Cleanup(release)
	dissolving := teams.NewService(teams.Deps{
		Tx: postgres.NewTxRunner(f.pool), Teams: postgres.NewTeams(f.pool), Memberships: f.members,
	})

	acceptErr := make(chan error, 1)
	go func() {
		_, err := accepting.Accept(ctx, joiner, inv.Token)
		acceptErr <- err
	}()
	select {
	case <-paused.read:
	case <-time.After(5 * time.Second):
		t.Fatal("accept never read the invite")
	}

	dissolveErr := make(chan error, 1)
	go func() { dissolveErr <- dissolving.Dissolve(ctx, f.teamID, f.adminID) }()
	waitForRevokeWait(t, f.pool, dissolveErr)
	release()

	if err := <-acceptErr; err != nil {
		t.Fatalf("accept: %v", err)
	}
	if err := <-dissolveErr; err != nil {
		t.Fatalf("dissolve: %v", err)
	}
	if _, err := f.members.MembershipByTeamAndUser(ctx, f.teamID, joiner); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("joiner's membership after dissolve: err = %v, want ErrNotFound (rejected)", err)
	}
}

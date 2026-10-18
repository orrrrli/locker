package application_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
	"github.com/orrrrli/locker/api/internal/testdb"
)

type fixture struct {
	pool *pgxpool.Pool
	tx   *postgres.TxRunner
	repo *postgres.Memberships
	n    int
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.New(t)
	return &fixture{pool: pool, tx: postgres.NewTxRunner(pool), repo: postgres.NewMemberships(pool)}
}

func (f *fixture) id(t *testing.T, sql string, args ...any) int64 {
	t.Helper()
	var id int64
	if err := f.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f *fixture) user(t *testing.T) int64 {
	t.Helper()
	f.n++
	return f.id(t, `INSERT INTO "user" (name, birth_date) VALUES ($1, '2000-01-01') RETURNING id`, fmt.Sprint("u", f.n))
}

func (f *fixture) team(t *testing.T) int64 {
	t.Helper()
	return f.id(t, `INSERT INTO team (name, timezone) VALUES ('Halcones', 'America/Tijuana') RETURNING id`)
}

// member adds userID to the team and returns the membership id.
func (f *fixture) member(t *testing.T, teamID, userID int64, role domain.Role, status domain.MembershipStatus) int64 {
	t.Helper()
	return f.id(t, `INSERT INTO membership (team_id, user_id, role, status, joined_at)
		VALUES ($1, $2, $3, $4, CASE WHEN $4 IN ('active', 'left') THEN now() END) RETURNING id`,
		teamID, userID, role, status)
}

func (f *fixture) activeAdmins(t *testing.T, team int64) int64 {
	t.Helper()
	return f.id(t, `SELECT count(*) FROM membership WHERE team_id = $1 AND role = 'admin' AND status = 'active'`, team)
}

// barrier holds each caller until two have arrived or wait runs out.
type barrier struct {
	mu   sync.Mutex
	n    int
	all  chan struct{}
	wait time.Duration
}

func newBarrier(wait time.Duration) *barrier {
	return &barrier{all: make(chan struct{}), wait: wait}
}

func (b *barrier) arrive() {
	b.mu.Lock()
	if b.n++; b.n == 2 {
		close(b.all)
	}
	b.mu.Unlock()
	select {
	case <-b.all:
	case <-time.After(b.wait):
	}
}

// pausedRepo stops every transaction right after it locks a team and counts
// its admins, until another one has counted too. With the lock the second is
// still blocked, so the first goes on after the wait; without it both count
// the same admins and both write.
type pausedRepo struct {
	*postgres.Memberships
	b *barrier
}

func (r pausedRepo) LockTeamForAdminChange(ctx context.Context, teamID int64) (int, error) {
	n, err := r.Memberships.LockTeamForAdminChange(ctx, teamID)
	if err == nil {
		r.b.arrive()
	}
	return n, err
}

// TestWithTeamLockConcurrentStepDownsKeepAnAdmin: the team's two admins step
// down at the same time and exactly one wins (R6.4). The loser is the last
// admin once it gets the lock.
func TestWithTeamLockConcurrentStepDownsKeepAnAdmin(t *testing.T) {
	f := newFixture(t)
	team := f.team(t)
	admins := [2]int64{
		f.member(t, team, f.user(t), domain.RoleAdmin, domain.MembershipActive),
		f.member(t, team, f.user(t), domain.RoleAdmin, domain.MembershipActive),
	}
	repo := pausedRepo{Memberships: f.repo, b: newBarrier(300 * time.Millisecond)}

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, id := range admins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lock := application.TeamLock{TargetID: id, LosesAdmin: true, NoAdminCaller: true}
			errs[i] = application.WithTeamLock(context.Background(), f.tx, repo, team, lock, func(ctx context.Context, l application.Locked) error {
				return f.repo.SetLeft(ctx, l.Target.ID)
			})
		}()
	}
	wg.Wait()

	if n := f.activeAdmins(t, team); n != 1 {
		t.Fatalf("active admins = %d, want 1 (errs: %v)", n, errs)
	}
	ok, lost := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrLastAdmin):
			lost++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || lost != 1 {
		t.Fatalf("errs = %v, want one success and one ErrLastAdmin", errs)
	}
}

// gatedRepo holds the transaction before it takes the team lock until gate
// is closed: the window between the pre-lock caller check and the lock. It
// closes arrived when the transaction reaches that window.
type gatedRepo struct {
	*postgres.Memberships
	arrived chan<- struct{}
	gate    <-chan struct{}
}

func (r gatedRepo) LockTeamForAdminChange(ctx context.Context, teamID int64) (int, error) {
	close(r.arrived)
	<-r.gate
	return r.Memberships.LockTeamForAdminChange(ctx, teamID)
}

// TestWithTeamLockDemotedCallerCannotFinish: the caller passes the admin
// check before the lock, then steps down in another request before taking
// it. The re-check under the lock stops the write.
func TestWithTeamLockDemotedCallerCannotFinish(t *testing.T) {
	for _, tc := range []struct {
		name     string
		stepDown string
		want     error
	}{
		{"demoted", `UPDATE membership SET role = 'player' WHERE id = $1`, application.ErrNotAdmin},
		// A caller who left is not an active member: not found.
		{"left", `UPDATE membership SET status = 'left' WHERE id = $1`, domain.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			team := f.team(t)
			f.member(t, team, f.user(t), domain.RoleAdmin, domain.MembershipActive) // Ana stays admin
			beto := f.member(t, team, f.user(t), domain.RoleAdmin, domain.MembershipActive)

			arrived := make(chan struct{})
			gate := make(chan struct{})
			// Close the gate on every path: a goroutine left waiting holds a
			// pool connection and makes the pool's cleanup hang.
			release := sync.OnceFunc(func() { close(gate) })
			t.Cleanup(release)
			repo := gatedRepo{Memberships: f.repo, arrived: arrived, gate: gate}
			ran := false
			done := make(chan error, 1)
			go func() {
				done <- application.WithTeamLock(context.Background(), f.tx, repo, team, application.TeamLock{AdminID: beto},
					func(context.Context, application.Locked) error {
						ran = true
						return nil
					})
			}()

			<-arrived
			if _, err := f.pool.Exec(context.Background(), tc.stepDown, beto); err != nil {
				t.Fatal(err)
			}
			release()

			if err := <-done; !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if ran {
				t.Fatal("the write ran for a caller who stepped down")
			}
		})
	}
}

// TestWithTeamLockRejectsCallerBeforeTheLock: a caller who is not an active
// admin fails before the team lock, so with the lock held elsewhere it
// answers at once instead of waiting and failing with ErrTeamBusy. Only an
// active member learns the team exists (ErrNotAdmin).
func TestWithTeamLockRejectsCallerBeforeTheLock(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	team := f.team(t)
	f.member(t, team, f.user(t), domain.RoleAdmin, domain.MembershipActive)
	player := f.member(t, team, f.user(t), domain.RolePlayer, domain.MembershipActive)
	pendingAdmin := f.member(t, team, f.user(t), domain.RoleAdmin, domain.MembershipPending)
	leftAdmin := f.member(t, team, f.user(t), domain.RoleAdmin, domain.MembershipLeft)
	elsewhere := f.member(t, f.team(t), f.user(t), domain.RoleAdmin, domain.MembershipActive)

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT 1 FROM team WHERE id = $1 FOR NO KEY UPDATE`, team); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name   string
		caller int64
		want   error
	}{
		{"player", player, application.ErrNotAdmin},
		{"pending admin", pendingAdmin, domain.ErrNotFound},
		{"left admin", leftAdmin, domain.ErrNotFound},
		{"other team's admin", elsewhere, domain.ErrNotFound},
		{"unknown", elsewhere + 1000, domain.ErrNotFound},
		{"zero caller without NoAdminCaller", 0, domain.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			err := application.WithTeamLock(ctx, f.tx, f.repo, team, application.TeamLock{AdminID: tc.caller},
				func(context.Context, application.Locked) error {
					t.Error("the write ran")
					return nil
				})
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if d := time.Since(start); d > time.Second {
				t.Fatalf("took %s: waited on the team lock", d)
			}
		})
	}
}

// TestWithTeamsLockedConcurrentKeepAnAdmin: two users who are the only admins
// of the same two teams leave both at once, passing the teams in opposite
// orders. Locking in id order makes the second wait for the first instead of
// deadlocking, and it then sees itself as the last admin of each team (R6.4).
func TestWithTeamsLockedConcurrentKeepAnAdmin(t *testing.T) {
	f := newFixture(t)
	a, b := f.user(t), f.user(t)
	teams := []int64{f.team(t), f.team(t)}
	for _, team := range teams {
		f.member(t, team, a, domain.RoleAdmin, domain.MembershipActive)
		f.member(t, team, b, domain.RoleAdmin, domain.MembershipActive)
	}
	repo := pausedRepo{Memberships: f.repo, b: newBarrier(300 * time.Millisecond)}
	orders := [2][]int64{{teams[0], teams[1]}, {teams[1], teams[0]}}

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, u := range []int64{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = application.WithTeamsLocked(context.Background(), f.tx, repo, u, orders[i], func(ctx context.Context, locked []application.LockedTeam) error {
				for _, lt := range locked {
					if lt.LastAdmin {
						return domain.ErrLastAdmin
					}
				}
				for _, lt := range locked {
					if err := f.repo.SetLeft(ctx, lt.Member.ID); err != nil {
						return err
					}
				}
				return nil
			})
		}()
	}
	wg.Wait()

	ok, lost := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrLastAdmin):
			lost++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || lost != 1 {
		t.Fatalf("errs = %v, want one success and one ErrLastAdmin", errs)
	}
	for _, team := range teams {
		if n := f.activeAdmins(t, team); n != 1 {
			t.Errorf("team %d: active admins = %d, want 1", team, n)
		}
	}
}

// TestWithTeamLockRejectsLosesAdminWithoutTarget: asking for the R6.4 check
// with no target to check it on is a programming error, so it fails before
// the write instead of silently skipping the rule.
func TestWithTeamLockRejectsLosesAdminWithoutTarget(t *testing.T) {
	f := newFixture(t)
	team := f.team(t)
	admin := f.member(t, team, f.user(t), domain.RoleAdmin, domain.MembershipActive)
	err := application.WithTeamLock(context.Background(), f.tx, f.repo, team,
		application.TeamLock{AdminID: admin, LosesAdmin: true},
		func(context.Context, application.Locked) error {
			t.Error("the write ran")
			return nil
		})
	if err == nil {
		t.Fatal("err = nil, want an error")
	}
}

// targetProbe counts the reads of the target membership, and those made
// without the team lock. It keys on the target id: the caller check reads the
// caller with the same method before the lock.
type targetProbe struct {
	*postgres.Memberships
	t      *testing.T
	pool   *pgxpool.Pool
	target int64
	reads  *int
	before *int
}

func (p targetProbe) MembershipInTeam(ctx context.Context, teamID, id int64) (domain.Membership, error) {
	if id == p.target {
		*p.reads++
		if !testdb.TeamLocked(p.t, p.pool, teamID) {
			*p.before++
		}
	}
	return p.Memberships.MembershipInTeam(ctx, teamID, id)
}

// TestWithTeamLockReadsTargetUnderTheLock: the target is read after the team
// lock is taken. A read before it can be stale and let the last admin go
// (R6.4).
func TestWithTeamLockReadsTargetUnderTheLock(t *testing.T) {
	f := newFixture(t)
	team := f.team(t)
	admin := f.member(t, team, f.user(t), domain.RoleAdmin, domain.MembershipActive)
	player := f.member(t, team, f.user(t), domain.RolePlayer, domain.MembershipActive)

	var reads, before int
	probe := targetProbe{Memberships: f.repo, t: t, pool: f.pool, target: player, reads: &reads, before: &before}
	err := application.WithTeamLock(context.Background(), f.tx, probe, team,
		application.TeamLock{AdminID: admin, TargetID: player},
		func(_ context.Context, l application.Locked) error {
			if l.Target.ID != player {
				t.Errorf("target = %d, want %d", l.Target.ID, player)
			}
			return nil
		})
	if err != nil {
		t.Fatal(err)
	}
	if reads == 0 || before > 0 {
		t.Errorf("target read %d times, %d of them without the team lock", reads, before)
	}
}

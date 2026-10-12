package memberships_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/memberships"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres"
)

// barrier holds each caller until both have arrived or wait runs out.
type barrier struct {
	mu   sync.Mutex
	n    int
	all  chan struct{}
	wait time.Duration
}

func (b *barrier) arrive() {
	b.mu.Lock()
	b.n++
	if b.n == 2 {
		close(b.all)
	}
	b.mu.Unlock()
	select {
	case <-b.all:
	case <-time.After(b.wait):
	}
}

// pausedRepo stops every transaction right after it counts the admins, until
// the other one has counted too. With the team lock the second one is still
// blocked on the lock, so the first goes on after the wait. Without it both
// count 2 and both demote, as long as both reach the count within the wait:
// a broken lock is caught on every run that is not slower than that.
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

func (f *fixture) activeAdmins(t *testing.T, team int64) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM membership WHERE team_id = $1 AND role = 'admin' AND status = 'active'`, team).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestConcurrentStepDownsKeepAnAdmin: the team's two admins each demote
// themselves or leave at the same time, and exactly one wins (R6.4). The loser
// is the last admin once it gets the lock (ErrLastAdmin). Every run must end
// with one active admin. Only self changes can race: an admin cannot demote
// or remove another admin (R6.12).
func TestConcurrentStepDownsKeepAnAdmin(t *testing.T) {
	demote := func(svc *memberships.Service, team, id int64) error {
		_, err := svc.ChangeRole(context.Background(), team, id, id, domain.RolePlayer)
		return err
	}
	leave := func(svc *memberships.Service, team, id int64) error {
		return svc.Leave(context.Background(), team, id, id)
	}
	type op func(svc *memberships.Service, team, id int64) error
	for _, tc := range []struct {
		name string
		ops  [2]op
	}{
		{"each demotes themselves", [2]op{demote, demote}},
		{"each leaves", [2]op{leave, leave}},
		{"one demotes, one leaves", [2]op{demote, leave}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			team := f.team(t)
			admins := [2]int64{
				f.member(t, team, domain.RoleAdmin, domain.MembershipActive),
				f.member(t, team, domain.RoleAdmin, domain.MembershipActive),
			}

			svc := memberships.NewService(memberships.Deps{
				Tx: postgres.NewTxRunner(f.pool),
				Memberships: pausedRepo{
					Memberships: postgres.NewMemberships(f.pool),
					b:           &barrier{all: make(chan struct{}), wait: 300 * time.Millisecond},
				},
			})

			errs := make([]error, 2)
			var wg sync.WaitGroup
			for i, id := range admins {
				wg.Add(1)
				go func() {
					defer wg.Done()
					errs[i] = tc.ops[i](svc, team, id)
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
		})
	}
}

// gatedRepo holds the transaction before it takes the team lock until gate
// is closed, so a test can commit another change in between: the window
// between the pre-lock caller check and the lock. It closes arrived when the
// transaction reaches that window.
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

// TestDemotedCallerCannotFinishInFlightChange: Beto's request passed the
// admin check before the lock (PATCH and DELETE /memberships/{id} sit behind
// requireActiveMember, so the use case checks the role). He steps down in
// another request before the first one takes the team lock (Ana stays admin,
// so R6.4 allows it). His request must fail and change nothing.
func TestDemotedCallerCannotFinishInFlightChange(t *testing.T) {
	promote := func(target func(beto, carlos int64) int64) func(*memberships.Service, int64, int64, int64) error {
		return func(svc *memberships.Service, team, beto, carlos int64) error {
			_, err := svc.ChangeRole(context.Background(), team, beto, target(beto, carlos), domain.RoleAdmin)
			return err
		}
	}
	for _, tc := range []struct {
		name     string
		inFlight func(svc *memberships.Service, team, beto, carlos int64) error
		stepDown func(svc *memberships.Service, team, beto int64) error
		want     error
		beto     domain.MembershipStatus
	}{
		{
			"promote someone else, after demoting himself",
			promote(func(_, carlos int64) int64 { return carlos }),
			demoteSelf, application.ErrNotAdmin, domain.MembershipActive,
		},
		{
			"promote himself back, after demoting himself",
			promote(func(beto, _ int64) int64 { return beto }),
			demoteSelf, application.ErrNotAdmin, domain.MembershipActive,
		},
		{
			// A caller who left is not an active member: not found (requireRole's answer).
			"remove a player, after leaving",
			func(svc *memberships.Service, team, beto, carlos int64) error {
				return svc.Leave(context.Background(), team, beto, carlos)
			},
			func(svc *memberships.Service, team, beto int64) error {
				return svc.Leave(context.Background(), team, beto, beto)
			},
			domain.ErrNotFound, domain.MembershipLeft,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			team := f.team(t)
			f.member(t, team, domain.RoleAdmin, domain.MembershipActive) // Ana
			beto := f.member(t, team, domain.RoleAdmin, domain.MembershipActive)
			carlos := f.member(t, team, domain.RolePlayer, domain.MembershipActive)

			arrived := make(chan struct{})
			gate := make(chan struct{})
			// Close the gate on every path: a goroutine left waiting holds a
			// pool connection and makes the pool's cleanup hang.
			release := sync.OnceFunc(func() { close(gate) })
			t.Cleanup(release)
			betoSvc := memberships.NewService(memberships.Deps{
				Tx:          postgres.NewTxRunner(f.pool),
				Memberships: gatedRepo{Memberships: postgres.NewMemberships(f.pool), arrived: arrived, gate: gate},
			})
			done := make(chan error, 1)
			go func() { done <- tc.inFlight(betoSvc, team, beto, carlos) }()

			// Beto steps down once his first request has passed the pre-lock
			// check and waits before the lock.
			<-arrived
			if err := tc.stepDown(f.svc, team, beto); err != nil {
				t.Fatal(err)
			}
			release()

			if err := <-done; !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if got := f.role(t, carlos); got != domain.RolePlayer {
				t.Fatalf("carlos role = %s, want player", got)
			}
			if s, _ := f.status(t, carlos); s != domain.MembershipActive {
				t.Fatalf("carlos status = %s, want active", s)
			}
			if s, _ := f.status(t, beto); s != tc.beto {
				t.Fatalf("beto status = %s, want %s", s, tc.beto)
			}
			if tc.beto == domain.MembershipActive {
				if got := f.role(t, beto); got != domain.RolePlayer {
					t.Fatalf("beto role = %s, want player", got)
				}
			}
		})
	}
}

func demoteSelf(svc *memberships.Service, team, id int64) error {
	_, err := svc.ChangeRole(context.Background(), team, id, id, domain.RolePlayer)
	return err
}

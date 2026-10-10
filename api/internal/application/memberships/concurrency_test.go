package memberships_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

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

// TestConcurrentDemotesKeepAnAdmin: two admins demote each other at the same
// time. Exactly one wins; the other gets ErrLastAdmin (R6.4).
func TestConcurrentDemotesKeepAnAdmin(t *testing.T) {
	f := newFixture(t)
	team := f.team(t)
	a := f.member(t, team, domain.RoleAdmin, domain.MembershipActive)
	b := f.member(t, team, domain.RoleAdmin, domain.MembershipActive)

	svc := memberships.NewService(memberships.Deps{
		Tx: postgres.NewTxRunner(f.pool),
		Memberships: pausedRepo{
			Memberships: postgres.NewMemberships(f.pool),
			b:           &barrier{all: make(chan struct{}), wait: 300 * time.Millisecond},
		},
	})

	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, target := range []int64{b, a} { // a demotes b, b demotes a
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = svc.ChangeRole(context.Background(), team, target, domain.RolePlayer)
		}()
	}
	wg.Wait()

	var admins int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM membership WHERE team_id = $1 AND role = 'admin' AND status = 'active'`, team).Scan(&admins); err != nil {
		t.Fatal(err)
	}
	if admins != 1 {
		t.Fatalf("active admins = %d, want 1 (errs: %v)", admins, errs)
	}
	ok, last := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrLastAdmin):
			last++
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if ok != 1 || last != 1 {
		t.Fatalf("errs = %v, want one success and one ErrLastAdmin", errs)
	}
}

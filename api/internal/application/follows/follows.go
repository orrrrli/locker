// Package follows lets a user follow teams and leagues (R20). Following a
// team grants no access to its member-only content (R20.5): nothing here
// touches memberships.
package follows

import (
	"context"

	"github.com/orrrrli/locker/api/internal/domain"
)

// Kind is what a follow points at.
type Kind string

const (
	KindTeam   Kind = "team"
	KindLeague Kind = "league"
)

// Follows is what the use cases need from the follow table.
type Follows interface {
	// Follow stores the follow, or keeps the existing one; domain.ErrNotFound
	// for an unknown target or a dissolved team.
	Follow(ctx context.Context, userID int64, kind Kind, id int64) error
	// Unfollow removes the follow; no error when there was none.
	Unfollow(ctx context.Context, userID int64, kind Kind, id int64) error
}

type Service struct {
	follows Follows
}

func NewService(f Follows) *Service {
	return &Service{follows: f}
}

// Follow makes userID follow the team or league (R20.1, R20.2); a dissolved
// team cannot be followed (R6.10). An unknown kind is domain.ErrNotFound.
func (s *Service) Follow(ctx context.Context, userID int64, kind Kind, id int64) error {
	if !kind.valid() {
		return domain.ErrNotFound
	}
	return s.follows.Follow(ctx, userID, kind, id)
}

// Unfollow removes userID's follow, if any (R20.3).
func (s *Service) Unfollow(ctx context.Context, userID int64, kind Kind, id int64) error {
	if !kind.valid() {
		return domain.ErrNotFound
	}
	return s.follows.Unfollow(ctx, userID, kind, id)
}

func (k Kind) valid() bool { return k == KindTeam || k == KindLeague }

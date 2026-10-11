// Package account holds the use cases on the caller's own account: deleting
// it (R5).
package account

import (
	"context"
	"fmt"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

// LastAdminError rejects deleting an account that is the only active admin
// of Teams, which need a new admin first (R5.4). errors.Is matches it to
// domain.ErrLastAdmin.
type LastAdminError struct {
	Teams []domain.Team
}

func (e *LastAdminError) Error() string {
	return fmt.Sprintf("%s: %d team(s)", domain.ErrLastAdmin, len(e.Teams))
}

func (e *LastAdminError) Unwrap() error { return domain.ErrLastAdmin }

// Teams is what the account use cases need from the team table.
type Teams interface {
	// ListActiveForUser returns the user's teams with an active membership,
	// ordered by id. checkLastAdmin locks them in that order.
	ListActiveForUser(ctx context.Context, userID int64) ([]domain.Team, error)
}

// Memberships is what the account use cases need from the membership table.
type Memberships interface {
	// LockTeamForAdminChange locks the team and returns its active admin
	// count; domain.ErrNotFound for an unknown team.
	LockTeamForAdminChange(ctx context.Context, teamID int64) (int, error)
	// MembershipByTeamAndUser returns domain.ErrNotFound when the user has no
	// membership in the team.
	MembershipByTeamAndUser(ctx context.Context, teamID, userID int64) (domain.Membership, error)
}

type Deps struct {
	Tx          application.TxRunner
	Teams       Teams
	Memberships Memberships
}

type Service struct {
	tx          application.TxRunner
	teams       Teams
	memberships Memberships
}

func NewService(d Deps) *Service {
	return &Service{tx: d.Tx, teams: d.Teams, memberships: d.Memberships}
}

// checkLastAdmin returns a *LastAdminError listing every team where the user
// is the only active admin (R5.4). It must run inside the deletion's
// transaction: it locks each of the user's teams and keeps the locks, so no
// admin leaves or is demoted before the deletion commits (R6.4).
//
// It locks every active team of the user, not only those where they are an
// admin: a role read before the lock can be stale. The teams come ordered by
// id, so two deletions that share teams lock them in the same order and
// cannot deadlock.
func (s *Service) checkLastAdmin(ctx context.Context, userID int64) error {
	teams, err := s.teams.ListActiveForUser(ctx, userID)
	if err != nil {
		return err
	}
	var blocked []domain.Team
	for _, t := range teams {
		admins, err := s.memberships.LockTeamForAdminChange(ctx, t.ID)
		if err != nil {
			return err
		}
		m, err := s.memberships.MembershipByTeamAndUser(ctx, t.ID, userID)
		if err != nil {
			return err
		}
		if domain.CheckAdminLoss(m, admins) != nil {
			blocked = append(blocked, t)
		}
	}
	if len(blocked) > 0 {
		return &LastAdminError{Teams: blocked}
	}
	return nil
}

// Package memberships holds the use cases that change a membership: for now,
// promoting and demoting (R6.2-R6.4).
package memberships

import (
	"context"
	"errors"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

var (
	ErrInvalidRole = errors.New("role must be admin or player")
	// ErrNotActive rejects a role change on a pending or left membership:
	// approving and rejoining have their own flows (R7, R8).
	ErrNotActive = errors.New("only an active membership can change role")
)

// Memberships is what the membership use cases need from the membership table.
type Memberships interface {
	// LockTeamForAdminChange locks the team and returns its active admin
	// count; domain.ErrNotFound for an unknown team.
	LockTeamForAdminChange(ctx context.Context, teamID int64) (int, error)
	// MembershipInTeam returns domain.ErrNotFound when the id is not in the team.
	MembershipInTeam(ctx context.Context, teamID, id int64) (domain.Membership, error)
	UpdateRole(ctx context.Context, id int64, role domain.Role) (domain.Membership, error)
}

type Deps struct {
	Tx          application.TxRunner
	Memberships Memberships
}

type Service struct {
	tx          application.TxRunner
	memberships Memberships
}

func NewService(d Deps) *Service {
	return &Service{tx: d.Tx, memberships: d.Memberships}
}

// ChangeRole promotes or demotes a membership of the team (R6.2, R6.3). It
// rejects a demotion that would leave the team with no active admin, checked
// under the team lock in the same transaction as the write (R6.4). Callers
// must have checked the user is an admin of the team.
func (s *Service) ChangeRole(ctx context.Context, teamID, membershipID int64, role domain.Role) (domain.Membership, error) {
	if role != domain.RoleAdmin && role != domain.RolePlayer {
		return domain.Membership{}, ErrInvalidRole
	}
	var out domain.Membership
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		// Lock first, then read the target: a target read before the lock
		// can be stale and let the last admin go.
		admins, err := s.memberships.LockTeamForAdminChange(ctx, teamID)
		if err != nil {
			return err
		}
		m, err := s.memberships.MembershipInTeam(ctx, teamID, membershipID)
		if err != nil {
			return err
		}
		if m.Status != domain.MembershipActive {
			return ErrNotActive
		}
		if m.Role == role {
			out = m
			return nil
		}
		if role == domain.RolePlayer {
			if err := domain.CheckAdminLoss(m, admins); err != nil {
				return err
			}
		}
		out, err = s.memberships.UpdateRole(ctx, m.ID, role)
		return err
	})
	if err != nil {
		return domain.Membership{}, err
	}
	return out, nil
}

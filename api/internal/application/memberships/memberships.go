// Package memberships holds the use cases that change a membership:
// promoting and demoting (R6.2-R6.4), approving and rejecting (R7.3, R7.4),
// and the roster that lists them (R8.1).
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
	// ErrNotPending rejects approving or rejecting a membership that is not
	// waiting for approval.
	ErrNotPending = errors.New("only a pending membership can be approved or rejected")
)

// Memberships is what the membership use cases need from the membership table.
type Memberships interface {
	application.AdminLocker
	// ApprovePending and RejectPending return domain.ErrNotFound when the row
	// is no longer pending.
	ApprovePending(ctx context.Context, id int64) (domain.Membership, error)
	RejectPending(ctx context.Context, id int64) error
	UpdateRole(ctx context.Context, id int64, role domain.Role) (domain.Membership, error)
	Roster(ctx context.Context, teamID int64, withPending bool) ([]domain.RosterMember, error)
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

// ChangeRole lets the admin callerID promote or demote a membership of the
// team (R6.2, R6.3). It rejects a demotion that would leave the team with no
// active admin, checked under the team lock in the same transaction as the
// write (R6.4). callerID is the caller's membership, already checked by
// requireRole; it is checked again under the lock.
func (s *Service) ChangeRole(ctx context.Context, teamID, callerID, membershipID int64, role domain.Role) (domain.Membership, error) {
	if role != domain.RoleAdmin && role != domain.RolePlayer {
		return domain.Membership{}, ErrInvalidRole
	}
	var out domain.Membership
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		// Lock first, then read the target: a target read before the lock
		// can be stale and let the last admin go.
		admins, err := application.LockTeamAsAdmin(ctx, s.memberships, teamID, callerID)
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

// Approve lets the admin callerID make a pending membership of the team an
// active player (R7.3), whether it came from an invite or a join request.
func (s *Service) Approve(ctx context.Context, teamID, callerID, membershipID int64) (domain.Membership, error) {
	var out domain.Membership
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.lockAndCheckPending(ctx, teamID, callerID, membershipID); err != nil {
			return err
		}
		m, err := s.memberships.ApprovePending(ctx, membershipID)
		out = m
		return err
	})
	if err != nil {
		return domain.Membership{}, err
	}
	return out, nil
}

// Reject lets the admin callerID discard a pending membership of the team
// (R7.4). The user can ask to join again (R13.6).
func (s *Service) Reject(ctx context.Context, teamID, callerID, membershipID int64) error {
	return s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.lockAndCheckPending(ctx, teamID, callerID, membershipID); err != nil {
			return err
		}
		return s.memberships.RejectPending(ctx, membershipID)
	})
}

// lockAndCheckPending checks the caller under the team lock, then that the
// target is a pending membership of the team.
func (s *Service) lockAndCheckPending(ctx context.Context, teamID, callerID, membershipID int64) error {
	if _, err := application.LockTeamAsAdmin(ctx, s.memberships, teamID, callerID); err != nil {
		return err
	}
	m, err := s.memberships.MembershipInTeam(ctx, teamID, membershipID)
	if err != nil {
		return err
	}
	if m.Status != domain.MembershipPending {
		return ErrNotPending
	}
	return nil
}

// Roster lists the team of caller, an active member: its active members
// (R8.1), plus the pending ones when caller is an admin, so the admin's
// approval list comes from the same call (R13.8).
func (s *Service) Roster(ctx context.Context, caller domain.Membership) ([]domain.RosterMember, error) {
	return s.memberships.Roster(ctx, caller.TeamID, caller.Role == domain.RoleAdmin)
}

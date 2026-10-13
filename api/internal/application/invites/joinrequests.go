package invites

import (
	"context"
	"errors"

	"github.com/orrrrli/locker/api/internal/domain"
)

// RequestToJoin is a join request from the team's public page (R13). It
// leaves the user with a pending membership the same way an accepted invite
// does (R13.1): a new row, or their `left` row back (R13.4). An active member
// gets an AlreadyMemberError (R13.2). A pending one gets their row back
// unchanged with isNew false (R13.3), so only isNew true is a new request. An
// unknown or dissolved team is domain.ErrNotFound (R6.10).
//
// The answers that write nothing (unknown or dissolved team, active, pending)
// come from a read without the lock, so a stranger looping on them cannot
// queue connections behind the team lock. A stale read there is harmless: a
// dissolve is final, and the write path checks again. Only a write takes the
// team lock, like a member leaving, and reads dissolved_at and the membership
// again under it: a dissolve holds that lock from its admin check to commit,
// so either it commits first and this sees dissolved_at, or this commits first
// and the dissolve rejects the new pending row (R6.8).
func (s *Service) RequestToJoin(ctx context.Context, teamID, userID int64) (out Accepted, isNew bool, err error) {
	name, err := s.openTeam(ctx, teamID)
	if err == nil {
		err = s.notMemberYet(ctx, teamID, userID)
	}
	if err == nil {
		err = s.tx.InTx(ctx, func(ctx context.Context) error {
			if _, err := s.memberships.LockTeamForAdminChange(ctx, teamID); err != nil {
				return err
			}
			if _, err := s.openTeam(ctx, teamID); err != nil {
				return err
			}
			return s.joinAsPending(ctx, teamID, userID)
		})
		isNew = err == nil
	}
	out = Accepted{TeamID: teamID, TeamName: name, Status: domain.MembershipPending}
	var already *AlreadyMemberError
	if errors.As(err, &already) && already.Status == domain.MembershipPending {
		return out, false, nil
	}
	if err != nil {
		return Accepted{}, false, err
	}
	return out, isNew, nil
}

// openTeam returns the team's name, or domain.ErrNotFound when the team is
// unknown or dissolved.
func (s *Service) openTeam(ctx context.Context, teamID int64) (string, error) {
	name, dissolved, err := s.memberships.TeamForJoin(ctx, teamID)
	if err == nil && dissolved {
		return "", domain.ErrNotFound
	}
	return name, err
}

// notMemberYet is an AlreadyMemberError when the user is active or pending in
// the team, and nil when they have no row or a left one.
func (s *Service) notMemberYet(ctx context.Context, teamID, userID int64) error {
	m, err := s.memberships.MembershipByTeamAndUser(ctx, teamID, userID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return nil
	case err != nil:
		return err
	case m.Status == domain.MembershipLeft:
		return nil
	default:
		return &AlreadyMemberError{Status: m.Status}
	}
}

// JoinRequests lists the user's pending memberships, from a join request or
// an invite, for the pending section of "Mis equipos" (R24.5).
func (s *Service) JoinRequests(ctx context.Context, userID int64) ([]domain.PendingMembership, error) {
	return s.memberships.PendingOfUser(ctx, userID)
}

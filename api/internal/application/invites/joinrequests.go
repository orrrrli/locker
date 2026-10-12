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
// unchanged with created false (R13.3), so only created true is a new
// request. An unknown or dissolved team is domain.ErrNotFound (R6.10).
//
// It takes the team lock before reading dissolved_at, like a member leaving:
// a dissolve holds that lock from its admin check to commit, so either it
// commits first and this sees dissolved_at, or this commits first and the
// dissolve rejects the new pending row (R6.8).
func (s *Service) RequestToJoin(ctx context.Context, teamID, userID int64) (out Accepted, created bool, err error) {
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.memberships.LockTeamForAdminChange(ctx, teamID); err != nil {
			return err
		}
		name, dissolved, err := s.memberships.TeamForJoin(ctx, teamID)
		if err != nil {
			return err
		}
		if dissolved {
			return domain.ErrNotFound
		}
		out = Accepted{TeamID: teamID, TeamName: name, Status: domain.MembershipPending}
		return s.joinAsPending(ctx, teamID, userID)
	})
	var already *AlreadyMemberError
	if errors.As(err, &already) && already.Status == domain.MembershipPending {
		return out, false, nil
	}
	if err != nil {
		return Accepted{}, false, err
	}
	return out, true, nil
}

package application

import (
	"context"
	"errors"

	"github.com/orrrrli/locker/api/internal/domain"
)

// ErrForbidden rejects a caller who is not an active admin of the team under
// the team lock: usually one demoted after requireRole let the request in.
var ErrForbidden = errors.New("the caller is not an active admin of the team")

// AdminLocker is what LockTeamAsAdmin needs from the membership table.
type AdminLocker interface {
	// LockTeamForAdminChange locks the team and returns its active admin
	// count; domain.ErrNotFound for an unknown team.
	LockTeamForAdminChange(ctx context.Context, teamID int64) (int, error)
	// MembershipInTeam returns domain.ErrNotFound when the id is not in the team.
	MembershipInTeam(ctx context.Context, teamID, id int64) (domain.Membership, error)
}

// LockTeamAsAdmin locks the team, then checks callerID is still an active
// admin of it, and returns the active admin count. It must run inside the
// use case's transaction.
//
// requireRole reads the caller's role before the transaction, so a demotion
// that commits in between would let a former admin finish the request. Every
// admin use case that writes calls this first, so the write and the check
// share the lock that every role change takes. Use LockTeamForAdminChange
// directly only for writes with no admin caller (a member leaving).
func LockTeamAsAdmin(ctx context.Context, m AdminLocker, teamID, callerID int64) (int, error) {
	admins, err := m.LockTeamForAdminChange(ctx, teamID)
	if err != nil {
		return 0, err
	}
	c, err := m.MembershipInTeam(ctx, teamID, callerID)
	if errors.Is(err, domain.ErrNotFound) {
		return 0, ErrForbidden
	}
	if err != nil {
		return 0, err
	}
	if c.Role != domain.RoleAdmin || c.Status != domain.MembershipActive {
		return 0, ErrForbidden
	}
	return admins, nil
}

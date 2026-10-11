package application

import (
	"context"
	"errors"

	"github.com/orrrrli/locker/api/internal/domain"
)

// ErrNotAdmin rejects an active member of the team who is not an admin.
var ErrNotAdmin = errors.New("the caller is not an admin of the team")

// AdminLocker is what LockTeamAsAdmin needs from the membership table.
type AdminLocker interface {
	// LockTeamForAdminChange locks the team and returns its active admin
	// count; domain.ErrNotFound for an unknown team.
	LockTeamForAdminChange(ctx context.Context, teamID int64) (int, error)
	// MembershipInTeam returns domain.ErrNotFound when the id is not in the team.
	MembershipInTeam(ctx context.Context, teamID, id int64) (domain.Membership, error)
}

// LockTeamAsAdmin checks callerID is an active admin of the team, locks the
// team, checks again, and returns the active admin count. It must run inside
// the use case's transaction. It answers like requireRole: a caller who is
// not an active member (left, pending, unknown) is domain.ErrNotFound, so the
// team stays hidden; an active member who is not an admin is ErrNotAdmin. A
// team whose lock is held too long is domain.ErrTeamBusy.
//
// The check before the lock keeps players from taking it: PATCH
// /memberships/{id} lets any active member in, so this is the only admin
// gate there. The check under the lock catches a demotion that commits while
// the request waits, so a former admin cannot finish it. Every admin use
// case that writes calls this first. Use LockTeamForAdminChange directly only
// for writes with no admin caller (a member leaving).
func LockTeamAsAdmin(ctx context.Context, m AdminLocker, teamID, callerID int64) (int, error) {
	if err := checkAdmin(ctx, m, teamID, callerID); err != nil {
		return 0, err
	}
	admins, err := m.LockTeamForAdminChange(ctx, teamID)
	if err != nil {
		return 0, err
	}
	if err := checkAdmin(ctx, m, teamID, callerID); err != nil {
		return 0, err
	}
	return admins, nil
}

func checkAdmin(ctx context.Context, m AdminLocker, teamID, callerID int64) error {
	c, err := m.MembershipInTeam(ctx, teamID, callerID)
	if err != nil {
		return err
	}
	if c.Status != domain.MembershipActive {
		return domain.ErrNotFound
	}
	if c.Role != domain.RoleAdmin {
		return ErrNotAdmin
	}
	return nil
}

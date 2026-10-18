package application

import (
	"context"
	"errors"
	"slices"

	"github.com/orrrrli/locker/api/internal/domain"
)

// ErrNotAdmin rejects an active member of the team who is not an admin.
var ErrNotAdmin = errors.New("the caller is not an admin of the team")

// TeamLocker is what WithTeamLock needs from the membership table.
type TeamLocker interface {
	// LockTeamForAdminChange locks the team and returns its active admin
	// count; domain.ErrNotFound for an unknown team, domain.ErrTeamBusy when
	// the lock stays held too long. It must run inside a transaction.
	LockTeamForAdminChange(ctx context.Context, teamID int64) (int, error)
	// MembershipInTeam returns domain.ErrNotFound when the id is not in the team.
	MembershipInTeam(ctx context.Context, teamID, id int64) (domain.Membership, error)
}

// TeamsLocker is what WithTeamsLocked needs from the membership table.
type TeamsLocker interface {
	// LockTeamForAdminChange locks the team and returns its active admin
	// count; domain.ErrNotFound for an unknown team, domain.ErrTeamBusy when
	// the lock stays held too long. It must run inside a transaction.
	LockTeamForAdminChange(ctx context.Context, teamID int64) (int, error)
	// MembershipByTeamAndUser returns domain.ErrNotFound when the user has no
	// membership in the team.
	MembershipByTeamAndUser(ctx context.Context, teamID, userID int64) (domain.Membership, error)
}

// TeamLock says what WithTeamLock checks under the team lock.
type TeamLock struct {
	// AdminID is the caller's membership. When set, it must be an active
	// admin of the team, checked before the lock (so a player never takes
	// it) and again under it (so a demotion that commits while the request
	// waits stops it). 0 only together with NoAdminCaller.
	AdminID int64
	// NoAdminCaller says the write has no admin caller (a member leaving, a
	// join request), so AdminID 0 is meant. Without it, AdminID 0 is
	// domain.ErrNotFound: a forgotten AdminID fails closed.
	NoAdminCaller bool
	// TargetID is the membership the write acts on, read under the lock: a
	// read before the lock can be stale. 0 when there is none.
	TargetID int64
	// LosesAdmin says the write makes Target stop being an active admin
	// (demote, leave, remove). The team is then rejected with
	// domain.ErrLastAdmin when Target is its only active admin (R6.4).
	// It needs TargetID, or WithTeamLock fails before taking the lock.
	LosesAdmin bool
}

var errLosesAdminNeedsTarget = errors.New("application: TeamLock.LosesAdmin needs TargetID")

// Locked is what WithTeamLock hands the write.
type Locked struct {
	// Target is the membership read under the lock; zero when
	// TeamLock.TargetID is 0.
	Target domain.Membership
}

// WithTeamLock runs fn in a transaction holding the team lock, after the
// checks l asks for. It joins the transaction ctx already carries. Every
// change that can remove an admin, or that needs the team's members to hold
// still until commit, goes through it, so those changes run one at a time per
// team. A caller who is not an active member of the team (left, pending,
// unknown) is domain.ErrNotFound, so the team stays hidden; an active member
// who is not an admin is ErrNotAdmin; a team whose lock is held too long is
// domain.ErrTeamBusy.
func WithTeamLock(ctx context.Context, tx TxRunner, m TeamLocker, teamID int64, l TeamLock, fn func(ctx context.Context, locked Locked) error) error {
	if l.AdminID == 0 && !l.NoAdminCaller {
		return domain.ErrNotFound
	}
	if l.LosesAdmin && l.TargetID == 0 {
		return errLosesAdminNeedsTarget
	}
	return tx.InTx(ctx, func(ctx context.Context) error {
		if l.AdminID != 0 {
			if err := checkCaller(ctx, m, teamID, l.AdminID); err != nil {
				return err
			}
		}
		admins, err := m.LockTeamForAdminChange(ctx, teamID)
		if err != nil {
			return err
		}
		if l.AdminID != 0 {
			if err := checkCaller(ctx, m, teamID, l.AdminID); err != nil {
				return err
			}
		}
		var locked Locked
		if l.TargetID != 0 {
			if locked.Target, err = m.MembershipInTeam(ctx, teamID, l.TargetID); err != nil {
				return err
			}
			if l.LosesAdmin {
				if err := domain.CheckAdminLoss(locked.Target, admins); err != nil {
					return err
				}
			}
		}
		return fn(ctx, locked)
	})
}

// LockedTeam is one team WithTeamsLocked locked for a user.
type LockedTeam struct {
	TeamID int64
	// Member is the user's membership in the team, read under the lock.
	Member domain.Membership
	// LastAdmin is true when Member is the team's only active admin (R6.4).
	LastAdmin bool
}

// WithTeamsLocked runs fn in a transaction holding the lock of every team in
// teamIDs, with the user's membership in each read under its lock. It joins
// the transaction ctx already carries. It locks in id order whatever order
// teamIDs come in, so two calls that share teams cannot deadlock. Every team
// must have a membership of the user, or it is domain.ErrNotFound. fn gets
// the teams in that same id order.
func WithTeamsLocked(ctx context.Context, tx TxRunner, m TeamsLocker, userID int64, teamIDs []int64, fn func(ctx context.Context, teams []LockedTeam) error) error {
	ids := slices.Compact(slices.Sorted(slices.Values(teamIDs)))
	return tx.InTx(ctx, func(ctx context.Context) error {
		teams := make([]LockedTeam, 0, len(ids))
		for _, id := range ids {
			admins, err := m.LockTeamForAdminChange(ctx, id)
			if err != nil {
				return err
			}
			member, err := m.MembershipByTeamAndUser(ctx, id, userID)
			if err != nil {
				return err
			}
			teams = append(teams, LockedTeam{
				TeamID:    id,
				Member:    member,
				LastAdmin: domain.CheckAdminLoss(member, admins) != nil,
			})
		}
		return fn(ctx, teams)
	})
}

func checkCaller(ctx context.Context, m TeamLocker, teamID, callerID int64) error {
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

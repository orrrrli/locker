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
	// AnonymizeForUser deletes the user's never-joined pending rows and
	// turns the rest into left players named "Ex-jugador #<id>" with no
	// user; an active row only in lockedTeamIDs. It returns how many rows
	// still point at the user.
	AnonymizeForUser(ctx context.Context, userID int64, lockedTeamIDs []int64) (int, error)
}

// Users is what the account use cases need from the user table.
type Users interface {
	// DeletePersonalData deletes identities, device tokens and sessions and
	// nulls the personal fields; domain.ErrNotFound when already deleted.
	DeletePersonalData(ctx context.Context, userID int64) error
}

type Deps struct {
	Tx          application.TxRunner
	Teams       Teams
	Memberships Memberships
	Users       Users
}

type Service struct {
	tx          application.TxRunner
	teams       Teams
	memberships Memberships
	users       Users
}

func NewService(d Deps) *Service {
	return &Service{tx: d.Tx, teams: d.Teams, memberships: d.Memberships, users: d.Users}
}

// Delete deletes the user's account in one transaction (R5). It is rejected
// with a *LastAdminError while the user is the only active admin of any team
// (R5.4). Otherwise every membership becomes a left "Ex-jugador #<id>" with
// no user, keeping attendance and fees (R5.3); never-joined pending rows are
// deleted like a reject; then identities, device tokens and sessions are
// deleted and the personal fields nulled (R5.2).
//
// A membership that turns active in a team the check did not lock (the user
// joined one meanwhile) fails it with domain.ErrTeamBusy, so that team is
// never left without its admin; the user tries again.
//
// ponytail: a request that authenticated before this commit but writes after
// it still attaches to the deleted user: a login's new session, a created
// team, an accepted invite or join request. Such a session never
// authenticates (GetSessionByTokenHash skips deleted users); the team and
// membership writes still need to check user.deleted_at.
func (s *Service) Delete(ctx context.Context, userID int64) error {
	return s.tx.InTx(ctx, func(ctx context.Context) error {
		locked, err := s.checkLastAdmin(ctx, userID)
		if err != nil {
			return err
		}
		stillLinked, err := s.memberships.AnonymizeForUser(ctx, userID, locked)
		if err != nil {
			return err
		}
		if stillLinked > 0 {
			return domain.ErrTeamBusy
		}
		return s.users.DeletePersonalData(ctx, userID)
	})
}

// checkLastAdmin returns the ids of the teams it locked, or a *LastAdminError
// listing every team where the user is the only active admin (R5.4). It must
// run inside the deletion's
// transaction: it locks each of the user's teams and keeps the locks, so no
// admin leaves or is demoted before the deletion commits (R6.4).
//
// It locks every active team of the user, not only those where they are an
// admin: a role read before the lock can be stale. The teams come ordered by
// id, so two deletions that share teams lock them in the same order and
// cannot deadlock.
func (s *Service) checkLastAdmin(ctx context.Context, userID int64) ([]int64, error) {
	teams, err := s.teams.ListActiveForUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	locked := make([]int64, 0, len(teams))
	var blocked []domain.Team
	for _, t := range teams {
		admins, err := s.memberships.LockTeamForAdminChange(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		locked = append(locked, t.ID)
		m, err := s.memberships.MembershipByTeamAndUser(ctx, t.ID, userID)
		if err != nil {
			return nil, err
		}
		if domain.CheckAdminLoss(m, admins) != nil {
			blocked = append(blocked, t)
		}
	}
	if len(blocked) > 0 {
		return nil, &LastAdminError{Teams: blocked}
	}
	return locked, nil
}

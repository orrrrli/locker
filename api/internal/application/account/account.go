// Package account holds the use cases on the caller's own account: reading
// it and deleting it (R5).
package account

import (
	"context"
	"fmt"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

// LastAdminError rejects deleting an account that is the only active admin
// of Teams, which need a new admin first, or, when the user is the team's
// only active member, a dissolve (R5.4, R6.8). errors.Is matches it to
// domain.ErrLastAdmin.
type LastAdminError struct {
	Teams []BlockedTeam
}

// BlockedTeam is a team that blocks the deletion. SoleMember tells the client
// to offer dissolving it instead of promoting someone (R6.8).
type BlockedTeam struct {
	domain.Team
	SoleMember bool
}

func (e *LastAdminError) Error() string {
	return fmt.Sprintf("%s: %d team(s)", domain.ErrLastAdmin, len(e.Teams))
}

func (e *LastAdminError) Unwrap() error { return domain.ErrLastAdmin }

// Teams is what the account use cases need from the team table.
type Teams interface {
	// ListActiveForUser returns the user's teams with an active membership.
	ListActiveForUser(ctx context.Context, userID int64) ([]domain.Team, error)
	CountActiveMembers(ctx context.Context, teamID int64) (int, error)
}

// Memberships is what the account use cases need from the membership table.
type Memberships interface {
	application.TeamsLocker
	// AnonymizeForUser deletes the user's never-joined pending rows and
	// turns the rest into left players named "Ex-jugador #<id>" with no
	// user; an active row only in lockedTeamIDs. It returns how many rows
	// still point at the user.
	AnonymizeForUser(ctx context.Context, userID int64, lockedTeamIDs []int64) (int, error)
}

// Users is what the account use cases need from the user table.
type Users interface {
	// Get returns a live user with the operator flag; domain.ErrNotFound for
	// an unknown or deleted user.
	Get(ctx context.Context, userID int64) (domain.User, error)
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

// Me returns the caller's own account, including whether they are the
// operator, so a client can keep the operator console closed to everyone
// else before calling /operator/* (BE-CONSOLE-1). domain.ErrNotFound when
// the account was deleted since the request authenticated.
func (s *Service) Me(ctx context.Context, userID int64) (domain.User, error) {
	return s.users.Get(ctx, userID)
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
		teams, err := s.teams.ListActiveForUser(ctx, userID)
		if err != nil {
			return err
		}
		byID := make(map[int64]domain.Team, len(teams))
		ids := make([]int64, 0, len(teams))
		for _, t := range teams {
			byID[t.ID] = t
			ids = append(ids, t.ID)
		}
		// It locks every active team of the user, not only those where they
		// are an admin: a role read before the lock can be stale. The locks
		// hold until the deletion commits, so no admin leaves or is demoted
		// meanwhile (R6.4).
		return application.WithTeamsLocked(ctx, s.tx, s.memberships, userID, ids, func(ctx context.Context, locked []application.LockedTeam) error {
			if err := s.checkLastAdmin(ctx, byID, locked); err != nil {
				return err
			}
			stillLinked, err := s.memberships.AnonymizeForUser(ctx, userID, ids)
			if err != nil {
				return err
			}
			if stillLinked > 0 {
				return domain.ErrTeamBusy
			}
			return s.users.DeletePersonalData(ctx, userID)
		})
	})
}

// checkLastAdmin returns a *LastAdminError listing every locked team where
// the user is the only active admin (R5.4), each marked when the user is its
// only active member (R6.8).
func (s *Service) checkLastAdmin(ctx context.Context, byID map[int64]domain.Team, locked []application.LockedTeam) error {
	var blocked []BlockedTeam
	for _, t := range locked {
		if !t.LastAdmin {
			continue
		}
		n, err := s.teams.CountActiveMembers(ctx, t.TeamID)
		if err != nil {
			return err
		}
		blocked = append(blocked, BlockedTeam{Team: byID[t.TeamID], SoleMember: n == 1})
	}
	if len(blocked) > 0 {
		return &LastAdminError{Teams: blocked}
	}
	return nil
}

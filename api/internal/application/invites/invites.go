// Package invites holds the invite link use cases (create, list, revoke,
// accept) and join requests, the other way to a pending membership (R13).
package invites

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/application/token"
	"github.com/orrrrli/locker/api/internal/domain"
)

const (
	// Lifetime is how long an invite link works (R7.1).
	Lifetime = 7 * 24 * time.Hour
	// MaxLive caps a team's live invites, so one admin cannot fill the table.
	MaxLive = 20
	// keepExpired is how long an expired invite stays before DeleteExpired
	// removes it, so the app can still answer "expired" for a while.
	keepExpired = 30 * 24 * time.Hour
)

var (
	// ErrTooManyInvites rejects a new invite while the team has MaxLive.
	ErrTooManyInvites = errors.New("too many live invites; revoke one or wait for one to expire")
	// ErrInviteNotFound covers a malformed or unknown token, and an invite id
	// the team does not have.
	ErrInviteNotFound = errors.New("invite not found")
	ErrInviteExpired  = errors.New("invite expired")
	ErrInviteRevoked  = errors.New("invite revoked")
)

// AlreadyMemberError rejects an accept from someone who is already active
// or pending in the team. Status says which, so the app can tell them.
type AlreadyMemberError struct {
	Status domain.MembershipStatus
}

func (e *AlreadyMemberError) Error() string {
	return fmt.Sprintf("already a member (%s)", e.Status)
}

// Invites is what the invite use cases need from the invite table.
type Invites interface {
	CreateInvite(ctx context.Context, teamID int64, tokenHash []byte, createdBy int64, expiresAt time.Time) (int64, error)
	// InviteByTokenHash returns domain.ErrNotFound for an unknown hash.
	InviteByTokenHash(ctx context.Context, tokenHash []byte) (domain.Invite, string, error)
	// RevokeInvite returns domain.ErrNotFound when the team has no such invite.
	RevokeInvite(ctx context.Context, teamID, id int64, now time.Time) error
	CountLiveInvites(ctx context.Context, teamID int64, now time.Time) (int, error)
	// ListInvites returns the team's invites not expired at now, revoked ones
	// included, newest first.
	ListInvites(ctx context.Context, teamID int64, now time.Time) ([]domain.Invite, error)
	// DeleteExpired deletes invites expired at or before cutoff.
	DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error)
}

// Memberships is what the invite use cases need from the membership table.
type Memberships interface {
	application.TeamLocker
	// MembershipByTeamAndUser returns domain.ErrNotFound when there is none.
	MembershipByTeamAndUser(ctx context.Context, teamID, userID int64) (domain.Membership, error)
	// CreateMembership returns domain.ErrAlreadyExists when the user already
	// has a row in the team.
	CreateMembership(ctx context.Context, teamID, userID int64, role domain.Role, status domain.MembershipStatus) (int64, error)
	// RejoinMembership turns a `left` row into a pending player; it returns
	// domain.ErrNotFound when the row is no longer `left`.
	RejoinMembership(ctx context.Context, id int64) error
	// TeamForJoin returns the team's name and whether it is dissolved, or
	// domain.ErrNotFound for an unknown team.
	TeamForJoin(ctx context.Context, teamID int64) (name string, dissolved bool, err error)
	// PendingOfUser lists the user's pending memberships, newest first.
	PendingOfUser(ctx context.Context, userID int64) ([]domain.PendingMembership, error)
}

type Deps struct {
	Tx          application.TxRunner
	Invites     Invites
	Memberships Memberships
	// PublicBaseURL is where the API is reached, like https://api.locker.center.
	// Invite links open at PublicBaseURL/i/<token>, the Universal Link landing.
	PublicBaseURL string
	Now           func() time.Time // nil means time.Now
}

type Service struct {
	tx          application.TxRunner
	invites     Invites
	memberships Memberships
	linkBase    string
	now         func() time.Time
}

func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{
		tx: d.Tx, invites: d.Invites, memberships: d.Memberships, now: d.Now,
		linkBase: strings.TrimSuffix(d.PublicBaseURL, "/") + "/i/",
	}
}

// Created is a new invite. Token and URL are only ever returned here: the
// database keeps the hash.
type Created struct {
	ID        int64
	Token     string
	URL       string
	ExpiresAt time.Time
}

// Create makes an invite link for the team that expires in 7 days (R7.1).
// createdBy is the caller's membership, checked by requireRole and again
// under the team lock. The team lock also makes the MaxLive count exact.
func (s *Service) Create(ctx context.Context, teamID, createdBy int64) (Created, error) {
	t, hash, err := token.New()
	if err != nil {
		return Created{}, err
	}
	now := s.now()
	expires := now.Add(Lifetime)
	var id int64
	err = application.WithTeamLock(ctx, s.tx, s.memberships, teamID, application.TeamLock{AdminID: createdBy}, func(ctx context.Context, _ application.Locked) error {
		live, err := s.invites.CountLiveInvites(ctx, teamID, now)
		if err != nil {
			return err
		}
		if live >= MaxLive {
			return ErrTooManyInvites
		}
		created, err := s.invites.CreateInvite(ctx, teamID, hash, createdBy, expires)
		id = created
		return err
	})
	if err != nil {
		return Created{}, err
	}
	return Created{ID: id, Token: t, URL: s.linkBase + t, ExpiresAt: expires}, nil
}

// Revoke lets the admin callerID stop one of the team's invites from being
// accepted (R7.5). callerID is checked by requireRole and again under the
// team lock. An invite of another team is ErrInviteNotFound, so a check on
// the wrong team fails closed.
func (s *Service) Revoke(ctx context.Context, teamID, callerID, inviteID int64) error {
	return application.WithTeamLock(ctx, s.tx, s.memberships, teamID, application.TeamLock{AdminID: callerID}, func(ctx context.Context, _ application.Locked) error {
		err := s.invites.RevokeInvite(ctx, teamID, inviteID, s.now())
		if errors.Is(err, domain.ErrNotFound) {
			return ErrInviteNotFound
		}
		return err
	})
}

// List returns the team's unexpired invites, revoked ones included, newest
// first, so an admin can find and revoke a leaked link (R7.5, #184). Callers
// must have checked the caller is an admin of the team (requireRole). Now
// is the clock CountLiveInvites uses, so both agree on what has expired.
func (s *Service) List(ctx context.Context, teamID int64) ([]domain.Invite, error) {
	return s.invites.ListInvites(ctx, teamID, s.now())
}

// Accepted is the membership an accepted invite left the user with.
type Accepted struct {
	TeamID   int64
	TeamName string
	Status   domain.MembershipStatus
}

// Accept turns a valid invite into a pending membership (R7.2), never an
// active one: an admin approves it later. A user who left the team gets the
// same row back as pending, keeping their history (R8.4).
func (s *Service) Accept(ctx context.Context, userID int64, rawToken string) (Accepted, error) {
	hash, ok := token.Hash(rawToken)
	if !ok {
		return Accepted{}, ErrInviteNotFound
	}
	var out Accepted
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		inv, teamName, err := s.invites.InviteByTokenHash(ctx, hash)
		if errors.Is(err, domain.ErrNotFound) {
			return ErrInviteNotFound
		}
		if err != nil {
			return err
		}
		if inv.RevokedAt != nil {
			return ErrInviteRevoked
		}
		if !s.now().Before(inv.ExpiresAt) {
			return ErrInviteExpired
		}

		if err := s.joinAsPending(ctx, inv.TeamID, userID); err != nil {
			return err
		}
		out = Accepted{TeamID: inv.TeamID, TeamName: teamName, Status: domain.MembershipPending}
		return nil
	})
	if err != nil {
		return Accepted{}, err
	}
	return out, nil
}

// joinAsPending gives the user a pending player membership in the team: a
// new row, or their `left` row back, keeping its history (R8.4). An active
// or pending row is an AlreadyMemberError. Invite accept and join requests
// both go through it (R7.2, R13.1). It must run in the caller's transaction.
func (s *Service) joinAsPending(ctx context.Context, teamID, userID int64) error {
	m, err := s.memberships.MembershipByTeamAndUser(ctx, teamID, userID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		_, err = s.memberships.CreateMembership(ctx, teamID, userID, domain.RolePlayer, domain.MembershipPending)
		if errors.Is(err, domain.ErrAlreadyExists) {
			// A concurrent accept or join request by the same user won the insert.
			return &AlreadyMemberError{Status: domain.MembershipPending}
		}
		return err
	case err != nil:
		return err
	case m.Status == domain.MembershipLeft:
		err = s.memberships.RejoinMembership(ctx, m.ID)
		if errors.Is(err, domain.ErrNotFound) {
			// The row changed after the read: report what it is now.
			return s.alreadyMember(ctx, teamID, userID)
		}
		return err
	default:
		return &AlreadyMemberError{Status: m.Status}
	}
}

// DeleteExpired removes invites expired for more than 30 days, so rows of
// links nobody can use do not pile up. It reports how many it deleted.
func (s *Service) DeleteExpired(ctx context.Context) (int64, error) {
	return s.invites.DeleteExpired(ctx, s.now().Add(-keepExpired))
}

// alreadyMember re-reads the membership a concurrent write changed and
// reports its current status.
func (s *Service) alreadyMember(ctx context.Context, teamID, userID int64) error {
	m, err := s.memberships.MembershipByTeamAndUser(ctx, teamID, userID)
	if err != nil {
		return err
	}
	return &AlreadyMemberError{Status: m.Status}
}

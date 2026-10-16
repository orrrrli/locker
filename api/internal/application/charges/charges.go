// Package charges holds the fee use cases (R12). Locker takes no payments in
// Phase 1: a charge records who owes what, and admins mark who paid in cash.
package charges

import (
	"context"
	"errors"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

var (
	ErrInvalidConcept = errors.New("concept must be 1 to 80 characters, with no invisible characters")
	ErrInvalidAmount  = errors.New("amount_cents must be a whole number of cents from 1 to 100000000")
	ErrInvalidTarget  = errors.New("target must be active_members or match_attendees, and match_id goes only with match_attendees")
	// ErrInvalidMatch rejects a match_id that is not a match of the team. It
	// is not domain.ErrNotFound: the team exists and the caller sees it; the
	// request is what is wrong.
	ErrInvalidMatch = errors.New("match_id is not a match of this team")
	// ErrAttendanceOpen rejects charging a match's attendees before its
	// attendance is saved: entries come from attendance, never RSVP (R10.5).
	ErrAttendanceOpen = errors.New("the match's attendance is not saved yet")
	// ErrNoAttendees rejects charging a match nobody attended: a charge with
	// no entries is nothing to collect.
	ErrNoAttendees = errors.New("the match has no attendees to charge")
)

// Target says who owes the charge (R12.1).
type Target string

const (
	// TargetActiveMembers charges every active member of the team.
	TargetActiveMembers Target = "active_members"
	// TargetMatchAttendees charges the real attendees of one match (R10.5).
	TargetMatchAttendees Target = "match_attendees"
)

const (
	maxConceptLen = 80
	// MaxAmountCents is 1,000,000.00 MXN: far above any amateur team fee,
	// low enough that a typo cannot make a member owe billions.
	MaxAmountCents = 100_000_000
	// Currency is the only one in Phase 1 (R12.1).
	Currency = "MXN"
)

// Charges is what the fee use cases need from the charge tables.
type Charges interface {
	CreateCharge(ctx context.Context, teamID int64, concept string, amountCents int64, currency string, matchID *int64, createdBy int64) (domain.Charge, error)
	// AddActiveMembers inserts one pending entry per active membership of
	// the team and returns how many it inserted.
	AddActiveMembers(ctx context.Context, chargeID, teamID int64) (int, error)
	// AddAttendees inserts one pending entry per attendee of the match and
	// returns how many it inserted.
	AddAttendees(ctx context.Context, chargeID, matchID int64) (int, error)
	ListMembers(ctx context.Context, chargeID int64) ([]domain.ChargeMember, error)
}

// Matches is what the fee use cases need from the match table.
type Matches interface {
	// LockMatchInTeam returns the team's match, holding the row until the
	// transaction ends, or domain.ErrNotFound.
	LockMatchInTeam(ctx context.Context, teamID, id int64) (domain.Match, error)
}

type Deps struct {
	Tx          application.TxRunner
	Charges     Charges
	Matches     Matches
	Memberships application.AdminLocker
}

type Service struct {
	tx          application.TxRunner
	charges     Charges
	matches     Matches
	memberships application.AdminLocker
}

func NewService(d Deps) *Service {
	return &Service{tx: d.Tx, charges: d.Charges, matches: d.Matches, memberships: d.Memberships}
}

// Input is a new charge. MatchID is required with TargetMatchAttendees and
// must be absent otherwise.
type Input struct {
	Concept     string
	AmountCents int64
	Target      Target
	MatchID     *int64
}

// Created is a charge with its entries, one per targeted membership.
type Created struct {
	domain.Charge
	Members []domain.ChargeMember
}

// Create records a charge for the team with one pending entry per targeted
// membership (R12.1, R12.2). callerID is the admin's membership, checked by
// requireRole and again under the team lock, and recorded as the creator.
//
// Attendees come from the match's saved attendance, never from RSVP (R10.5),
// so the attendance must be closed first. The match row is held for the
// transaction, as SaveAttendance holds it: the list cannot change between
// the read and the insert, and once the charge exists the attendance is
// locked for good (R10.4).
func (s *Service) Create(ctx context.Context, teamID, callerID int64, in Input) (Created, error) {
	concept, ok := application.CleanText(in.Concept, maxConceptLen)
	if !ok {
		return Created{}, ErrInvalidConcept
	}
	if in.AmountCents < 1 || in.AmountCents > MaxAmountCents {
		return Created{}, ErrInvalidAmount
	}
	switch in.Target {
	case TargetActiveMembers:
		if in.MatchID != nil {
			return Created{}, ErrInvalidTarget
		}
	case TargetMatchAttendees:
		if in.MatchID == nil {
			return Created{}, ErrInvalidTarget
		}
	default:
		return Created{}, ErrInvalidTarget
	}
	var out Created
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := application.LockTeamAsAdmin(ctx, s.memberships, teamID, callerID); err != nil {
			return err
		}
		if in.Target == TargetMatchAttendees {
			m, err := s.matches.LockMatchInTeam(ctx, teamID, *in.MatchID)
			if errors.Is(err, domain.ErrNotFound) {
				return ErrInvalidMatch
			}
			if err != nil {
				return err
			}
			if m.AttendanceClosedAt == nil {
				return ErrAttendanceOpen
			}
		}
		c, err := s.charges.CreateCharge(ctx, teamID, concept, in.AmountCents, Currency, in.MatchID, callerID)
		if err != nil {
			return err
		}
		var n int
		if in.Target == TargetMatchAttendees {
			n, err = s.charges.AddAttendees(ctx, c.ID, *in.MatchID)
		} else {
			n, err = s.charges.AddActiveMembers(ctx, c.ID, teamID)
		}
		if err != nil {
			return err
		}
		if n == 0 {
			// The caller is an active member, so only a match can be empty.
			return ErrNoAttendees
		}
		members, err := s.charges.ListMembers(ctx, c.ID)
		if err != nil {
			return err
		}
		out = Created{Charge: c, Members: members}
		return nil
	})
	if err != nil {
		return Created{}, err
	}
	return out, nil
}

// Package charges holds the fee use cases (R12). Locker takes no payments in
// Phase 1: a charge records who owes what, and admins mark who paid in cash.
package charges

import (
	"context"
	"errors"
	"fmt"
	"time"

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
	ErrNoAttendees   = errors.New("the match has no attendees to charge")
	ErrInvalidStatus = errors.New("status must be paid or pending")
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
	// ChargeInTeam returns domain.ErrNotFound when the team has no such charge.
	ChargeInTeam(ctx context.Context, teamID, id int64) (domain.Charge, error)
	// ListCharges returns the team's charges, newest first.
	ListCharges(ctx context.Context, teamID int64) ([]domain.Charge, error)
	// ListMembersOfTeam returns every entry of the team's charges, or only
	// membershipID's when it is not nil, by charge then membership id.
	ListMembersOfTeam(ctx context.Context, teamID int64, membershipID *int64) ([]domain.ChargeMember, error)
	// GetMember and SetMemberStatus return domain.ErrNotFound when the
	// membership has no entry in the charge.
	GetMember(ctx context.Context, chargeID, membershipID int64) (domain.ChargeMember, error)
	SetMemberStatus(ctx context.Context, chargeID, membershipID int64, status domain.ChargeStatus, markedBy *int64, markedAt *time.Time) (domain.ChargeMember, error)
}

// Notifier sends the fee reminder as a manual notice (R12.5).
type Notifier interface {
	// SendChargeReminder reaches only the active members with a pending
	// entry in chargeID and counts against the team's daily limit.
	SendChargeReminder(ctx context.Context, teamID, callerID, chargeID int64, title, body string) (domain.Notification, error)
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
	Notifier    Notifier         // serves Remind
	Now         func() time.Time // nil means time.Now
}

type Service struct {
	tx          application.TxRunner
	charges     Charges
	matches     Matches
	memberships application.AdminLocker
	notifier    Notifier
	now         func() time.Time
}

func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{tx: d.Tx, charges: d.Charges, matches: d.Matches, memberships: d.Memberships, notifier: d.Notifier, now: d.Now}
}

// Remind sends the members who still owe the charge a manual notice (R12.5):
// "Tienes pendiente Arbitraje: $50.00 MXN." Only active members with a
// pending entry receive it; the admin check, the team's daily limit and the
// push are the manual notice's. A charge of another team is
// domain.ErrNotFound.
func (s *Service) Remind(ctx context.Context, teamID, callerID, chargeID int64) (domain.Notification, error) {
	c, err := s.charges.ChargeInTeam(ctx, teamID, chargeID)
	if err != nil {
		return domain.Notification{}, err
	}
	body := fmt.Sprintf("Tienes pendiente %s: $%d.%02d %s.", c.Concept, c.AmountCents/100, c.AmountCents%100, c.Currency)
	return s.notifier.SendChargeReminder(ctx, teamID, callerID, c.ID, "Recordatorio de pago", body)
}

// Input is a new charge. MatchID is required with TargetMatchAttendees and
// must be absent otherwise.
type Input struct {
	Concept     string
	AmountCents int64
	Target      Target
	MatchID     *int64
}

// Detail is a charge with its entries, one per targeted membership, by
// membership id.
type Detail struct {
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
func (s *Service) Create(ctx context.Context, teamID, callerID int64, in Input) (Detail, error) {
	concept, ok := application.CleanText(in.Concept, maxConceptLen)
	if !ok {
		return Detail{}, ErrInvalidConcept
	}
	if in.AmountCents < 1 || in.AmountCents > MaxAmountCents {
		return Detail{}, ErrInvalidAmount
	}
	switch in.Target {
	case TargetActiveMembers:
		if in.MatchID != nil {
			return Detail{}, ErrInvalidTarget
		}
	case TargetMatchAttendees:
		if in.MatchID == nil {
			return Detail{}, ErrInvalidTarget
		}
	default:
		return Detail{}, ErrInvalidTarget
	}
	var out Detail
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
		out = Detail{Charge: c, Members: members}
		return nil
	})
	if err != nil {
		return Detail{}, err
	}
	return out, nil
}

// List returns the team's charges, newest first, as the caller may see them
// (R12.4): an admin gets every charge with every entry, members who left
// included (R12.6); a player gets only the charges they are in, each with
// only their own entry. Callers must have checked the caller is an active
// member of the team.
//
// ponytail: no pagination; a season of weekly fees is a few dozen rows.
func (s *Service) List(ctx context.Context, caller domain.Membership) ([]Detail, error) {
	var only *int64
	if caller.Role != domain.RoleAdmin {
		only = &caller.ID
	}
	all, err := s.charges.ListCharges(ctx, caller.TeamID)
	if err != nil {
		return nil, err
	}
	entries, err := s.charges.ListMembersOfTeam(ctx, caller.TeamID, only)
	if err != nil {
		return nil, err
	}
	byCharge := make(map[int64][]domain.ChargeMember, len(all))
	for _, e := range entries {
		byCharge[e.ChargeID] = append(byCharge[e.ChargeID], e)
	}
	out := make([]Detail, 0, len(all))
	for _, c := range all {
		members := byCharge[c.ID]
		if only != nil && len(members) == 0 {
			continue
		}
		out = append(out, Detail{Charge: c, Members: members})
	}
	return out, nil
}

// SetStatus lets the admin callerID mark a member's entry paid in cash, which
// records who marked it and when (R12.3), or back to pending, which clears
// both. It is idempotent: an entry already in that status is returned as it
// is, so a repeated "paid" keeps the first marker. The membership need not
// be active: a member who left still pays what they owe (R12.6).
func (s *Service) SetStatus(ctx context.Context, teamID, callerID, chargeID, membershipID int64, status domain.ChargeStatus) (domain.ChargeMember, error) {
	if status != domain.ChargePaid && status != domain.ChargePending {
		return domain.ChargeMember{}, ErrInvalidStatus
	}
	var out domain.ChargeMember
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := application.LockTeamAsAdmin(ctx, s.memberships, teamID, callerID); err != nil {
			return err
		}
		if _, err := s.charges.ChargeInTeam(ctx, teamID, chargeID); err != nil {
			return err
		}
		cur, err := s.charges.GetMember(ctx, chargeID, membershipID)
		if err != nil {
			return err
		}
		if cur.Status == status {
			out = cur
			return nil
		}
		var by *int64
		var at *time.Time
		if status == domain.ChargePaid {
			now := s.now().UTC()
			by, at = &callerID, &now
		}
		out, err = s.charges.SetMemberStatus(ctx, chargeID, membershipID, status, by, at)
		return err
	})
	if err != nil {
		return domain.ChargeMember{}, err
	}
	return out, nil
}

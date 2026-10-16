package matches

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
)

var (
	// ErrMatchNotStarted rejects saving attendance before kickoff (R10.3).
	ErrMatchNotStarted = errors.New("the match has not started; attendance is saved after kickoff")
	// ErrAttendanceLocked rejects a correction once a charge targets the
	// match's attendees (R10.4).
	ErrAttendanceLocked = errors.New("a charge targets this match; attendance is locked")
	// ErrInvalidAttendee rejects an id that is not a membership of the team,
	// or one that never joined it.
	ErrInvalidAttendee = errors.New("every attendee must be a member of the team")
)

// Attendance is a match's real attendee list (R10). Locked means a charge
// targets it, so it can no longer change.
type Attendance struct {
	MatchID       int64
	MembershipIDs []int64
	ClosedAt      *time.Time
	Locked        bool
}

// CloseDueAttendance is the ticker job: for every match that started an hour
// or more ago and is still open, it saves the going RSVPs of current active
// members as attendance and closes it, exactly once (R10.1). A match an admin
// already saved is closed, so it is skipped (R10.2); so is one a charge
// already targets (R10.4). It reports how many matches it closed.
func (s *Service) CloseDueAttendance(ctx context.Context) (int64, error) {
	// One SQL statement, so one transaction: no InTx needed.
	return s.matches.CloseDueAttendance(ctx, s.now().UTC())
}

// SaveAttendance lets the admin callerID replace the match's attendee list
// after kickoff (R10.3), until a charge targets the match (R10.4). The charge
// check and the write share the transaction and hold the match row: the
// auto-save skips it, and a charge inserted meanwhile waits on the row's
// foreign key. A charge flow that reads attendance before inserting must
// take the row too (LockMatchInTeam), or it can read a list this call is
// about to replace. An empty list is allowed: nobody played. Attendees are
// memberships that joined the team at some point (active, left, or pending
// again); one that never joined is rejected.
func (s *Service) SaveAttendance(ctx context.Context, teamID, callerID, matchID int64, ids []int64) (Attendance, error) {
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	var out Attendance
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := application.LockTeamAsAdmin(ctx, s.memberships, teamID, callerID); err != nil {
			return err
		}
		m, err := s.matches.LockMatchInTeam(ctx, teamID, matchID)
		if err != nil {
			return err
		}
		if m.CancelledAt != nil {
			return ErrMatchCancelled
		}
		now := s.now().UTC()
		if now.Before(m.StartsAt) {
			return ErrMatchNotStarted
		}
		locked, err := s.matches.ChargeExistsForMatch(ctx, m.ID)
		if err != nil {
			return err
		}
		if locked {
			return ErrAttendanceLocked
		}
		n, err := s.matches.CountTeamMembersIn(ctx, teamID, ids)
		if err != nil {
			return err
		}
		if n != len(ids) {
			return ErrInvalidAttendee
		}
		if err := s.matches.ReplaceAttendance(ctx, m.ID, ids, now); err != nil {
			return err
		}
		closed := now
		if m.AttendanceClosedAt != nil {
			closed = *m.AttendanceClosedAt
		}
		out = Attendance{MatchID: m.ID, MembershipIDs: ids, ClosedAt: &closed}
		return nil
	})
	if err != nil {
		return Attendance{}, err
	}
	return out, nil
}

// GetAttendance returns the match's attendee list and whether it is locked.
// Callers must have checked the user is an active member of teamID.
func (s *Service) GetAttendance(ctx context.Context, teamID, matchID int64) (Attendance, error) {
	m, err := s.matches.MatchInTeam(ctx, teamID, matchID)
	if err != nil {
		return Attendance{}, err
	}
	ids, err := s.matches.ListAttendance(ctx, m.ID)
	if err != nil {
		return Attendance{}, err
	}
	locked, err := s.matches.ChargeExistsForMatch(ctx, m.ID)
	if err != nil {
		return Attendance{}, err
	}
	return Attendance{MatchID: m.ID, MembershipIDs: ids, ClosedAt: m.AttendanceClosedAt, Locked: locked}, nil
}

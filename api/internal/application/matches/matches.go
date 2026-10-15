// Package matches holds the match use cases: create, list and update (R9.1,
// R9.2). A match is the only event type.
package matches

import (
	"context"
	"errors"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

var (
	ErrInvalidStartsAt  = errors.New("starts_at is required and must fall in years 1 to 9999")
	ErrInvalidRivalName = errors.New("rival_name must be 1 to 60 characters, with no invisible characters")
	ErrInvalidLocation  = errors.New("location must be 1 to 120 characters, with no invisible characters")
	ErrNothingToUpdate  = errors.New("send starts_at, rival_name, location or any of them")
	// ErrAttendanceClosed rejects moving a match once its attendance is
	// saved: the list would belong to a date that no longer exists and the
	// auto-save would never run again for the new one (R10.1).
	ErrAttendanceClosed = errors.New("attendance is already saved; schedule a new match instead of moving this one")
)

const (
	maxRivalLen    = 60
	maxLocationLen = 120
)

// Matches is what the match use cases need from the match table.
type Matches interface {
	CreateMatch(ctx context.Context, teamID int64, startsAt time.Time, rival, location string, createdBy int64) (domain.Match, error)
	ListMatches(ctx context.Context, teamID int64) ([]domain.Match, error)
	ListUpcomingForUser(ctx context.Context, userID int64, now time.Time) ([]domain.UpcomingMatch, error)
	// UpdateMatch, MatchInTeam and LockMatchInTeam return domain.ErrNotFound
	// when the team has no such match. LockMatchInTeam holds the row for the
	// transaction, so the attendance auto-save skips it meanwhile.
	UpdateMatch(ctx context.Context, teamID, id int64, startsAt *time.Time, rival, location *string) (domain.Match, error)
	MatchInTeam(ctx context.Context, teamID, id int64) (domain.Match, error)
	LockMatchInTeam(ctx context.Context, teamID, id int64) (domain.Match, error)
	UpsertRSVP(ctx context.Context, matchID, membershipID int64, answer domain.RSVPAnswer) (domain.RSVP, error)
	// Attendance (R10). ReplaceAttendance stores the whole list and closes
	// the attendance if it was open.
	CloseDueAttendance(ctx context.Context, now time.Time) (int64, error)
	ChargeExistsForMatch(ctx context.Context, matchID int64) (bool, error)
	CountTeamMembersIn(ctx context.Context, teamID int64, ids []int64) (int, error)
	ReplaceAttendance(ctx context.Context, matchID int64, ids []int64, now time.Time) error
	ListAttendance(ctx context.Context, matchID int64) ([]int64, error)
}

// Notifier sends the automatic notices about a match (R11.4). It runs after
// the match committed, so the notice never goes out for a rolled-back write.
type Notifier interface {
	MatchCreated(ctx context.Context, m domain.Match) error
	MatchChanged(ctx context.Context, m domain.Match) error
}

type Deps struct {
	Tx          application.TxRunner
	Matches     Matches
	Memberships application.AdminLocker
	Notifier    Notifier         // nil means no automatic notices
	Now         func() time.Time // nil means time.Now
}

type Service struct {
	tx          application.TxRunner
	matches     Matches
	memberships application.AdminLocker
	notifier    Notifier
	now         func() time.Time
}

func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{tx: d.Tx, matches: d.Matches, memberships: d.Memberships, notifier: d.Notifier, now: d.Now}
}

// notify sends an automatic notice once the match committed. Its error is
// dropped: the match is saved, so failing the request would make the client
// retry and schedule it twice; the notice alone is what is lost.
//
// ponytail: unlogged, the application layer has no logger. Log it here once
// one exists. A notice inside the match's transaction would be atomic with
// it, but Send pushes after its own commit and must not join another.
func (s *Service) notify(send func() error) {
	if s.notifier == nil {
		return
	}
	_ = send()
}

// Create schedules a match for the team (R9.1) and tells every active
// member (R11.4). startsAt is stored in UTC. callerID is the admin's
// membership, checked by requireRole and again under the team lock; it is
// also recorded as the creator.
func (s *Service) Create(ctx context.Context, teamID, callerID int64, startsAt time.Time, rival, location string) (domain.Match, error) {
	if !validStartsAt(startsAt) {
		return domain.Match{}, ErrInvalidStartsAt
	}
	rival, ok := application.CleanText(rival, maxRivalLen)
	if !ok {
		return domain.Match{}, ErrInvalidRivalName
	}
	location, ok = application.CleanText(location, maxLocationLen)
	if !ok {
		return domain.Match{}, ErrInvalidLocation
	}
	var out domain.Match
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := application.LockTeamAsAdmin(ctx, s.memberships, teamID, callerID); err != nil {
			return err
		}
		m, err := s.matches.CreateMatch(ctx, teamID, startsAt.UTC(), rival, location, callerID)
		out = m
		return err
	})
	if err != nil {
		return domain.Match{}, err
	}
	s.notify(func() error { return s.notifier.MatchCreated(ctx, out) })
	return out, nil
}

// List returns the team's matches, soonest first. Callers must have checked
// the user is an active member.
func (s *Service) List(ctx context.Context, teamID int64) ([]domain.Match, error) {
	return s.matches.ListMatches(ctx, teamID)
}

// Upcoming returns the matches that start after now in every team where the
// user is active, soonest first (R14). A match at its kickoff instant has
// started, as the RSVP lock counts it (R9.5).
// ponytail: no pagination; a user in a few teams has a few dozen rows at
// most. Add a limit or cursor when league fixtures (R16) land.
func (s *Service) Upcoming(ctx context.Context, userID int64) ([]domain.UpcomingMatch, error) {
	return s.matches.ListUpcomingForUser(ctx, userID, s.now())
}

// Update lets the admin callerID change the time, the rival, the place or
// any of them. A nil field keeps its value. Once attendance is saved the
// time cannot move (ErrAttendanceClosed); the rest still can. A change of
// time or place tells every active member (R11.4); a new rival name alone
// does not.
func (s *Service) Update(ctx context.Context, teamID, callerID, matchID int64, startsAt *time.Time, rival, location *string) (domain.Match, error) {
	if startsAt == nil && rival == nil && location == nil {
		return domain.Match{}, ErrNothingToUpdate
	}
	if startsAt != nil {
		if !validStartsAt(*startsAt) {
			return domain.Match{}, ErrInvalidStartsAt
		}
		u := startsAt.UTC()
		startsAt = &u
	}
	if rival != nil {
		r, ok := application.CleanText(*rival, maxRivalLen)
		if !ok {
			return domain.Match{}, ErrInvalidRivalName
		}
		rival = &r
	}
	if location != nil {
		l, ok := application.CleanText(*location, maxLocationLen)
		if !ok {
			return domain.Match{}, ErrInvalidLocation
		}
		location = &l
	}
	var out, cur domain.Match
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := application.LockTeamAsAdmin(ctx, s.memberships, teamID, callerID); err != nil {
			return err
		}
		var err error
		cur, err = s.matches.LockMatchInTeam(ctx, teamID, matchID)
		if err != nil {
			return err
		}
		if startsAt != nil && cur.AttendanceClosedAt != nil && !cur.StartsAt.Equal(*startsAt) {
			return ErrAttendanceClosed
		}
		m, err := s.matches.UpdateMatch(ctx, teamID, matchID, startsAt, rival, location)
		out = m
		return err
	})
	if err != nil {
		return domain.Match{}, err
	}
	if !cur.StartsAt.Equal(out.StartsAt) || cur.Location != out.Location {
		s.notify(func() error { return s.notifier.MatchChanged(ctx, out) })
	}
	return out, nil
}

// validStartsAt rejects a zero time and one outside years 1 to 9999: Postgres
// stores those, but encoding/json cannot write them back, so one such match
// would empty the whole team's list.
func validStartsAt(t time.Time) bool {
	y := t.UTC().Year()
	return !t.IsZero() && y >= 1 && y <= 9999
}

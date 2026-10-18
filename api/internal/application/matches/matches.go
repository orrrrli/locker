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
	// ErrMatchCancelled rejects any change to a cancelled match: its RSVPs,
	// attendance and details stay as they were.
	ErrMatchCancelled = errors.New("the match is cancelled")
	// ErrFixtureMatch rejects a team admin's change to a league match: the
	// operator owns its date, time, court and rival (open question 26).
	ErrFixtureMatch = errors.New("this is a league match; only the league can change it")
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

// Teams gives the team's timezone, for the time in a notice.
type Teams interface {
	GetTeam(ctx context.Context, id int64) (domain.Team, error)
}

// Notices writes the automatic notices (R11.4): Store inside the use case's
// transaction, Deliver after it commits. notifications.Service is one.
type Notices interface {
	Store(ctx context.Context, n domain.Notification) (domain.Notification, error)
	Deliver(ctx context.Context, n domain.Notification)
}

type Deps struct {
	Tx          application.TxRunner
	Matches     Matches
	Memberships application.TeamLocker
	Teams       Teams
	Notices     Notices
	Now         func() time.Time // nil means time.Now
}

type Service struct {
	tx          application.TxRunner
	matches     Matches
	memberships application.TeamLocker
	teams       Teams
	notices     Notices
	now         func() time.Time
}

func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{tx: d.Tx, matches: d.Matches, memberships: d.Memberships, teams: d.Teams, notices: d.Notices, now: d.Now}
}

// Create schedules a match for the team (R9.1). startsAt is stored in UTC.
// callerID is the admin's membership, checked by requireRole and again under
// the team lock; it is also recorded as the creator. A match_created notice
// for every active member is stored with the match and pushed after commit
// (R11.4).
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
	var notice domain.Notification
	err := application.WithTeamLock(ctx, s.tx, s.memberships, teamID, application.TeamLock{AdminID: callerID}, func(ctx context.Context, _ application.Locked) error {
		m, err := s.matches.CreateMatch(ctx, teamID, startsAt.UTC(), rival, location, callerID)
		if err != nil {
			return err
		}
		out = m
		notice, err = s.storeNotice(ctx, domain.NotificationMatchCreated, m)
		return err
	})
	if err != nil {
		return domain.Match{}, err
	}
	s.notices.Deliver(ctx, notice)
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
// time cannot move (ErrAttendanceClosed); the rest still can. A cancelled
// match (ErrMatchCancelled) and a league match (ErrFixtureMatch) take no
// change here. When the time
// or the place actually changes, a match_changed notice is stored with the
// change and pushed after commit (R11.4); the rival alone sends none.
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
	var out domain.Match
	var notice *domain.Notification
	err := application.WithTeamLock(ctx, s.tx, s.memberships, teamID, application.TeamLock{AdminID: callerID}, func(ctx context.Context, _ application.Locked) error {
		cur, err := s.matches.LockMatchInTeam(ctx, teamID, matchID)
		if err != nil {
			return err
		}
		if cur.CancelledAt != nil {
			return ErrMatchCancelled
		}
		if cur.FixtureID != nil {
			return ErrFixtureMatch
		}
		if startsAt != nil && cur.AttendanceClosedAt != nil && !cur.StartsAt.Equal(*startsAt) {
			return ErrAttendanceClosed
		}
		m, err := s.matches.UpdateMatch(ctx, teamID, matchID, startsAt, rival, location)
		if err != nil {
			return err
		}
		out = m
		if m.StartsAt.Equal(cur.StartsAt) && m.Location == cur.Location {
			return nil
		}
		n, err := s.storeNotice(ctx, domain.NotificationMatchChanged, m)
		notice = &n
		return err
	})
	if err != nil {
		return domain.Match{}, err
	}
	if notice != nil {
		s.notices.Deliver(ctx, *notice)
	}
	return out, nil
}

// storeNotice writes the automatic notice about m inside the caller's
// transaction, with the time in the team's zone.
func (s *Service) storeNotice(ctx context.Context, kind domain.NotificationKind, m domain.Match) (domain.Notification, error) {
	team, err := s.teams.GetTeam(ctx, m.TeamID)
	if err != nil {
		return domain.Notification{}, err
	}
	title := application.MatchCreatedTitle
	if kind == domain.NotificationMatchChanged {
		title = application.MatchChangedTitle
	}
	return s.notices.Store(ctx, application.MatchNotice(kind, title, m, team.Timezone))
}

// validStartsAt rejects a zero time and one outside years 1 to 9999: Postgres
// stores those, but encoding/json cannot write them back, so one such match
// would empty the whole team's list.
func validStartsAt(t time.Time) bool {
	y := t.UTC().Year()
	return !t.IsZero() && y >= 1 && y <= 9999
}

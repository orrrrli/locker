package leagues

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

var (
	ErrSameTeam          = errors.New("home_team_id and away_team_id must be different teams")
	ErrTeamNotEnrolled   = errors.New("both teams must be enrolled in the league")
	ErrInvalidCourt      = errors.New("court_id must be a court of the league's venue")
	ErrInvalidStartsAt   = errors.New("starts_at is required and must fall in years 1 to 9999")
	ErrOutsideWindow     = errors.New("starts_at must fall on the league's weekday, inside its time window, in the venue's timezone")
	ErrFixtureStarted    = errors.New("the fixture has started; it can no longer be cancelled")
	ErrFixtureCancelled  = errors.New("the fixture is cancelled")
	ErrFixtureAttendance = errors.New("a team already saved attendance for this fixture; its time can no longer change")
	ErrNothingToMove     = errors.New("send starts_at, court_id or both")
)

// Fixtures is what the fixture use cases need from storage, on top of
// Leagues. Locks hold until the transaction ends.
type Fixtures interface {
	CountEnrolled(ctx context.Context, leagueID int64, teamIDs []int64) (int, error)
	// CourtInVenue returns domain.ErrNotFound when the court is not one of
	// the venue's.
	CourtInVenue(ctx context.Context, courtID, venueID int64) (domain.FixtureCourt, error)
	CreateFixture(ctx context.Context, f domain.Fixture) (domain.Fixture, error)
	// LockFixture returns domain.ErrNotFound when the league has no such
	// fixture.
	LockFixture(ctx context.Context, leagueID, id int64) (domain.Fixture, error)
	UpdateFixture(ctx context.Context, id int64, startsAt time.Time, courtID int64) (domain.Fixture, error)
	CancelFixture(ctx context.Context, id int64, now time.Time) ([]domain.Match, error)
	CreateFixtureMatch(ctx context.Context, teamID, fixtureID, courtID int64, startsAt time.Time, location string) (domain.Match, error)
	LockFixtureMatches(ctx context.Context, fixtureID int64) ([]domain.Match, error)
	UpdateFixtureMatches(ctx context.Context, fixtureID int64, startsAt time.Time, courtID int64, location string) ([]domain.Match, error)
	FutureFixturesOfTeam(ctx context.Context, leagueID, teamID int64, now time.Time) ([]domain.Fixture, error)
}

// Notices writes the automatic match notices: Store inside the use case's
// transaction, Deliver after it commits. notifications.Service is one.
type Notices interface {
	Store(ctx context.Context, n domain.Notification) (domain.Notification, error)
	Deliver(ctx context.Context, n domain.Notification)
}

// NewFixture is a fixture to create.
type NewFixture struct {
	HomeTeamID int64
	AwayTeamID int64
	CourtID    int64
	StartsAt   time.Time
}

// FixtureChange moves a fixture; a nil field keeps its value.
type FixtureChange struct {
	StartsAt *time.Time
	CourtID  *int64
}

// CreateFixture schedules a game of the league (design.md fixture creation
// flow). Home and away differ and are both enrolled; the court is one of the
// league's venue; starts_at is on the league's weekday inside its window in
// the venue's timezone (open question 28). In one transaction it writes the
// fixture, one match per team (no rival_name, no creator) and a
// match_created notice for each team (R11.4); the notices are pushed after
// commit. Locks go teams first, then the league, the order dissolving takes
// them in: the team locks keep a dissolve from committing meanwhile, the
// league lock an unenroll.
func (s *Service) CreateFixture(ctx context.Context, leagueID int64, in NewFixture) (domain.Fixture, []domain.Match, error) {
	if in.HomeTeamID == in.AwayTeamID {
		return domain.Fixture{}, nil, ErrSameTeam
	}
	if !validInstant(in.StartsAt) {
		return domain.Fixture{}, nil, ErrInvalidStartsAt
	}
	startsAt := in.StartsAt.UTC()
	var fixture domain.Fixture
	var matches []domain.Match
	var notices []domain.Notification
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		teams, err := s.lockTeams(ctx, in.HomeTeamID, in.AwayTeamID)
		if err != nil {
			return err
		}
		league, err := s.leagues.LockLeague(ctx, leagueID)
		if err != nil {
			return err
		}
		court, err := s.courtOf(ctx, league, in.CourtID)
		if err != nil {
			return err
		}
		if !inWindow(league, court.Timezone, startsAt) {
			return ErrOutsideWindow
		}
		if n, err := s.leagues.CountEnrolled(ctx, leagueID, teams); err != nil {
			return err
		} else if n != len(teams) {
			return ErrTeamNotEnrolled
		}
		fixture, err = s.leagues.CreateFixture(ctx, domain.Fixture{
			LeagueID: leagueID, HomeTeamID: in.HomeTeamID, AwayTeamID: in.AwayTeamID, CourtID: in.CourtID, StartsAt: startsAt,
		})
		if err != nil {
			return err
		}
		for _, team := range []int64{in.HomeTeamID, in.AwayTeamID} {
			m, err := s.leagues.CreateFixtureMatch(ctx, team, fixture.ID, in.CourtID, startsAt, place(court))
			if err != nil {
				return err
			}
			matches = append(matches, m)
		}
		notices, err = s.storeNotices(ctx, domain.NotificationMatchCreated, application.MatchCreatedTitle, matches, court.Timezone)
		return err
	})
	if err != nil {
		return domain.Fixture{}, nil, err
	}
	s.deliver(ctx, notices)
	return fixture, matches, nil
}

// UpdateFixture moves the fixture to a new time, court or both, and both
// linked matches with it, in one transaction; each team gets match_changed
// (open question 26). The new time is checked against the league window. A
// team that saved attendance already played: the time can no longer move.
func (s *Service) UpdateFixture(ctx context.Context, leagueID, fixtureID int64, c FixtureChange) (domain.Fixture, []domain.Match, error) {
	if c.StartsAt == nil && c.CourtID == nil {
		return domain.Fixture{}, nil, ErrNothingToMove
	}
	if c.StartsAt != nil && !validInstant(*c.StartsAt) {
		return domain.Fixture{}, nil, ErrInvalidStartsAt
	}
	var fixture domain.Fixture
	var matches []domain.Match
	var notices []domain.Notification
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		league, err := s.leagues.LockLeague(ctx, leagueID)
		if err != nil {
			return err
		}
		cur, err := s.leagues.LockFixture(ctx, leagueID, fixtureID)
		if err != nil {
			return err
		}
		if cur.CancelledAt != nil {
			return ErrFixtureCancelled
		}
		startsAt, courtID := cur.StartsAt, cur.CourtID
		if c.StartsAt != nil {
			startsAt = c.StartsAt.UTC()
		}
		if c.CourtID != nil {
			courtID = *c.CourtID
		}
		court, err := s.courtOf(ctx, league, courtID)
		if err != nil {
			return err
		}
		if !inWindow(league, court.Timezone, startsAt) {
			return ErrOutsideWindow
		}
		before, err := s.leagues.LockFixtureMatches(ctx, fixtureID)
		if err != nil {
			return err
		}
		moved := !startsAt.Equal(cur.StartsAt)
		if moved && slices.ContainsFunc(before, func(m domain.Match) bool { return m.AttendanceClosedAt != nil }) {
			return ErrFixtureAttendance
		}
		if fixture, err = s.leagues.UpdateFixture(ctx, fixtureID, startsAt, courtID); err != nil {
			return err
		}
		if matches, err = s.leagues.UpdateFixtureMatches(ctx, fixtureID, startsAt, courtID, place(court)); err != nil {
			return err
		}
		if !moved && courtID == cur.CourtID {
			return nil
		}
		notices, err = s.storeNotices(ctx, domain.NotificationMatchChanged, application.MatchChangedTitle, matches, court.Timezone)
		return err
	})
	if err != nil {
		return domain.Fixture{}, nil, err
	}
	s.deliver(ctx, notices)
	return fixture, matches, nil
}

// CancelFixture deletes a fixture that has not started: the fixture and both
// linked matches are cancelled in one transaction and each team gets a
// match_changed notice (open question 26). The rows stay, marked cancelled,
// so the notices still point at their match.
func (s *Service) CancelFixture(ctx context.Context, leagueID, fixtureID int64) error {
	var notices []domain.Notification
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		league, err := s.leagues.LockLeague(ctx, leagueID)
		if err != nil {
			return err
		}
		f, err := s.leagues.LockFixture(ctx, leagueID, fixtureID)
		if err != nil {
			return err
		}
		if f.CancelledAt != nil {
			return domain.ErrNotFound // already deleted
		}
		if !s.now().Before(f.StartsAt) {
			return ErrFixtureStarted
		}
		notices, err = s.cancel(ctx, league, f)
		return err
	})
	if err != nil {
		return err
	}
	s.deliver(ctx, notices)
	return nil
}

// cancel cancels the fixture and its matches and stores a notice for each
// team, inside the caller's transaction.
func (s *Service) cancel(ctx context.Context, league domain.League, f domain.Fixture) ([]domain.Notification, error) {
	if _, err := s.leagues.LockFixtureMatches(ctx, f.ID); err != nil {
		return nil, err
	}
	matches, err := s.leagues.CancelFixture(ctx, f.ID, s.now().UTC())
	if err != nil {
		return nil, err
	}
	court, err := s.leagues.CourtInVenue(ctx, f.CourtID, league.VenueID)
	if err != nil {
		return nil, err
	}
	return s.storeNotices(ctx, domain.NotificationMatchChanged, application.MatchCancelledTitle, matches, court.Timezone)
}

// courtOf returns the court when it is one of the league's venue.
func (s *Service) courtOf(ctx context.Context, league domain.League, courtID int64) (domain.FixtureCourt, error) {
	court, err := s.leagues.CourtInVenue(ctx, courtID, league.VenueID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.FixtureCourt{}, ErrInvalidCourt
	}
	return court, err
}

// lockTeams locks the teams, in id order so two fixtures of the same teams
// cannot deadlock, checks none is dissolved, and returns the sorted ids.
func (s *Service) lockTeams(ctx context.Context, teams ...int64) ([]int64, error) {
	sorted := slices.Sorted(slices.Values(teams))
	for _, id := range sorted {
		if _, err := s.teams.LockTeamForAdminChange(ctx, id); errors.Is(err, domain.ErrNotFound) {
			return nil, ErrTeamNotEnrolled
		} else if err != nil {
			return nil, err
		}
		if _, dissolved, err := s.teams.TeamForJoin(ctx, id); err != nil {
			return nil, err
		} else if dissolved {
			return nil, ErrTeamNotEnrolled
		}
	}
	return sorted, nil
}

// storeNotices stores one notice per match, inside the caller's transaction.
// A fixture's time is shown in the venue's timezone, where it is played.
func (s *Service) storeNotices(ctx context.Context, kind domain.NotificationKind, title string, matches []domain.Match, tz string) ([]domain.Notification, error) {
	out := make([]domain.Notification, 0, len(matches))
	for _, m := range matches {
		n, err := s.notices.Store(ctx, application.MatchNotice(kind, title, m, tz))
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

// deliver pushes stored notices; call it after the transaction commits.
func (s *Service) deliver(ctx context.Context, notices []domain.Notification) {
	for _, n := range notices {
		s.notices.Deliver(ctx, n)
	}
}

// place is the location text of a fixture match: "Unidad Deportiva ·
// Cancha 1".
func place(c domain.FixtureCourt) string {
	return c.VenueName + " · " + c.CourtName
}

// inWindow reports whether t falls on the league's weekday, at or after its
// start time and before its end time, in the venue's timezone.
func inWindow(l domain.League, tz string, t time.Time) bool {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return false
	}
	local := t.In(loc)
	weekday := domain.Weekday((int(local.Weekday())+6)%7 + 1) // time.Sunday is 0; ISO Sunday is 7
	minute := domain.TimeOfDay(local.Hour()*60 + local.Minute())
	return weekday == l.Weekday && minute >= l.StartTime && minute < l.EndTime
}

// validInstant rejects a zero time and one outside years 1 to 9999, which
// encoding/json cannot write back.
func validInstant(t time.Time) bool {
	y := t.UTC().Year()
	return !t.IsZero() && y >= 1 && y <= 9999
}

// Package leagues holds the operator's league and enrollment use cases
// (R16). The router lets only the operator reach them (rule 12); they do no
// authorization of their own.
package leagues

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

var (
	ErrInvalidName       = errors.New("league name must be 1 to 60 characters, with no invisible characters")
	ErrInvalidWeekday    = errors.New("weekday must be monday, tuesday, wednesday, thursday, friday, saturday or sunday")
	ErrInvalidWindow     = errors.New("start_time and end_time must be HH:MM, with end_time after start_time")
	ErrInvalidSeasonName = errors.New("season_name must be 1 to 40 characters, with no invisible characters")
	ErrInvalidSeason     = errors.New("season_starts_on and season_ends_on must be YYYY-MM-DD, with the end not before the start")
	ErrUnknownVenue      = errors.New("venue_id is not a venue")
	ErrNothingToUpdate   = errors.New("send at least one field to change")
	ErrInvalidTeam       = errors.New("team_id is not a team, or the team is dissolved")
	ErrAlreadyEnrolled   = errors.New("the team is already enrolled in the league")
)

const (
	maxNameLen       = 60
	maxSeasonNameLen = 40
)

// Leagues is what the use cases need from the league and league_team tables.
type Leagues interface {
	// CreateLeague returns domain.ErrNotFound for an unknown venue.
	CreateLeague(ctx context.Context, l domain.League) (domain.League, error)
	// LockLeague holds the row until commit and must run in a transaction.
	// LockLeague, GetLeague and UpdateLeague return domain.ErrNotFound for an
	// unknown league.
	LockLeague(ctx context.Context, id int64) (domain.League, error)
	GetLeague(ctx context.Context, id int64) (domain.League, error)
	UpdateLeague(ctx context.Context, l domain.League) (domain.League, error)
	// EnrollTeam returns domain.ErrAlreadyExists for a team already in the
	// league; UnenrollTeam returns domain.ErrNotFound for one that is not.
	EnrollTeam(ctx context.Context, leagueID, teamID int64) (domain.Enrollment, error)
	UnenrollTeam(ctx context.Context, leagueID, teamID int64) error
}

// Teams is what enrollment needs to see a team and keep it from being
// dissolved meanwhile. LockTeamForAdminChange takes the team lock that
// dissolving takes too (domain.ErrNotFound for an unknown team,
// domain.ErrTeamBusy on timeout); its admin count is not used here.
type Teams interface {
	LockTeamForAdminChange(ctx context.Context, teamID int64) (int, error)
	TeamForJoin(ctx context.Context, teamID int64) (name string, dissolved bool, err error)
}

type Deps struct {
	Tx      application.TxRunner
	Leagues Leagues
	Teams   Teams
}

type Service struct {
	tx      application.TxRunner
	leagues Leagues
	teams   Teams
}

func NewService(d Deps) *Service {
	return &Service{tx: d.Tx, leagues: d.Leagues, teams: d.Teams}
}

// NewLeague is a league to create, as the client sent it: the weekday is
// "monday" to "sunday", times are "HH:MM" and dates "YYYY-MM-DD".
type NewLeague struct {
	VenueID        int64
	Name           string
	Weekday        string
	StartTime      string
	EndTime        string
	SeasonName     string
	SeasonStartsOn string
	SeasonEndsOn   string
}

// Change is a partial update of a league, in the same formats as NewLeague;
// a nil field keeps its value. The venue cannot change.
type Change struct {
	Name           *string
	Weekday        *string
	StartTime      *string
	EndTime        *string
	SeasonName     *string
	SeasonStartsOn *string
	SeasonEndsOn   *string
}

// Create stores a league at a venue (R16.1, R16.3). Leagues at the same
// venue on the same weekday are all accepted (R16.5).
func (s *Service) Create(ctx context.Context, in NewLeague) (domain.League, error) {
	l := domain.League{VenueID: in.VenueID}
	err := apply(&l, Change{
		Name: &in.Name, Weekday: &in.Weekday, StartTime: &in.StartTime, EndTime: &in.EndTime,
		SeasonName: &in.SeasonName, SeasonStartsOn: &in.SeasonStartsOn, SeasonEndsOn: &in.SeasonEndsOn,
	})
	if err != nil {
		return domain.League{}, err
	}
	created, err := s.leagues.CreateLeague(ctx, l)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.League{}, ErrUnknownVenue
	}
	return created, err
}

// Update changes the fields sent. The window and season are checked on the
// merged league, read under its lock, so changing only the end time is
// checked against the stored start time (R16.2, R16.3).
func (s *Service) Update(ctx context.Context, id int64, c Change) (domain.League, error) {
	if c == (Change{}) {
		return domain.League{}, ErrNothingToUpdate
	}
	var league domain.League
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		l, err := s.leagues.LockLeague(ctx, id)
		if err != nil {
			return err
		}
		if err := apply(&l, c); err != nil {
			return err
		}
		league, err = s.leagues.UpdateLeague(ctx, l)
		return err
	})
	if err != nil {
		return domain.League{}, err
	}
	return league, nil
}

// Enroll puts a team in the league (R16.6). A team can be in several leagues
// (R16.9), once per league (R16.7). A dissolved team cannot be enrolled
// (R6.10): the team lock keeps a dissolve from committing in between.
func (s *Service) Enroll(ctx context.Context, leagueID, teamID int64) (domain.Enrollment, error) {
	var e domain.Enrollment
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := s.leagues.GetLeague(ctx, leagueID); err != nil {
			return err
		}
		if _, err := s.teams.LockTeamForAdminChange(ctx, teamID); errors.Is(err, domain.ErrNotFound) {
			return ErrInvalidTeam
		} else if err != nil {
			return err
		}
		if _, dissolved, err := s.teams.TeamForJoin(ctx, teamID); err != nil {
			return err
		} else if dissolved {
			return ErrInvalidTeam
		}
		enrolled, err := s.leagues.EnrollTeam(ctx, leagueID, teamID)
		if errors.Is(err, domain.ErrAlreadyExists) {
			return ErrAlreadyEnrolled
		}
		e = enrolled
		return err
	})
	if err != nil {
		return domain.Enrollment{}, err
	}
	return e, nil
}

// Unenroll takes the team out of the league (R16.8). Fixtures do not exist
// yet; once BE-VENUE-5 adds them, the team's future fixtures in the league
// and both sides' linked matches are cancelled here, in this transaction
// (BE-VENUE-4-T5). Past matches keep their attendance and fees.
func (s *Service) Unenroll(ctx context.Context, leagueID, teamID int64) error {
	return s.tx.InTx(ctx, func(ctx context.Context) error {
		return s.leagues.UnenrollTeam(ctx, leagueID, teamID)
	})
}

// apply validates every non-nil field of c and sets it on l, then checks the
// window and season of the result.
func apply(l *domain.League, c Change) error {
	if c.Name != nil {
		n, ok := application.CleanText(*c.Name, maxNameLen)
		if !ok {
			return ErrInvalidName
		}
		l.Name = n
	}
	if c.Weekday != nil {
		w, ok := domain.ParseWeekday(*c.Weekday)
		if !ok {
			return ErrInvalidWeekday
		}
		l.Weekday = w
	}
	for _, f := range []struct {
		raw *string
		dst *domain.TimeOfDay
	}{{c.StartTime, &l.StartTime}, {c.EndTime, &l.EndTime}} {
		if f.raw == nil {
			continue
		}
		t, ok := parseTimeOfDay(*f.raw)
		if !ok {
			return ErrInvalidWindow
		}
		*f.dst = t
	}
	if l.EndTime <= l.StartTime {
		return ErrInvalidWindow
	}
	if c.SeasonName != nil {
		n, ok := application.CleanText(*c.SeasonName, maxSeasonNameLen)
		if !ok {
			return ErrInvalidSeasonName
		}
		l.SeasonName = n
	}
	for _, f := range []struct {
		raw *string
		dst *time.Time
	}{{c.SeasonStartsOn, &l.SeasonStartsOn}, {c.SeasonEndsOn, &l.SeasonEndsOn}} {
		if f.raw == nil {
			continue
		}
		d, err := time.Parse(time.DateOnly, *f.raw)
		if err != nil || d.Year() < 1 { // Postgres has no year 0
			return ErrInvalidSeason
		}
		*f.dst = d
	}
	if l.SeasonEndsOn.Before(l.SeasonStartsOn) {
		return ErrInvalidSeason
	}
	return nil
}

var hhmm = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)

// parseTimeOfDay reads "HH:MM", 00:00 to 23:59.
func parseTimeOfDay(s string) (domain.TimeOfDay, bool) {
	m := hhmm.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	h := int(m[1][0]-'0')*10 + int(m[1][1]-'0')
	min := int(m[2][0]-'0')*10 + int(m[2][1]-'0')
	return domain.TimeOfDay(h*60 + min), true
}

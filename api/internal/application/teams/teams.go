// Package teams holds the team use cases.
package teams

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

var (
	ErrInvalidName     = errors.New("team name must be 1 to 35 characters, with no invisible characters")
	ErrInvalidTimezone = errors.New("timezone must be an IANA zone, like America/Tijuana")
	ErrNothingToUpdate = errors.New("send a name, a timezone or both")
)

const maxNameLen = 35

// Teams is what the team use cases need from the team table.
type Teams interface {
	CreateTeam(ctx context.Context, name, timezone string) (domain.Team, error)
	ListActiveForUser(ctx context.Context, userID int64) ([]domain.Team, error)
	// GetTeam and UpdateTeam return domain.ErrNotFound for an unknown id.
	GetTeam(ctx context.Context, id int64) (domain.Team, error)
	UpdateTeam(ctx context.Context, id int64, name, timezone *string) (domain.Team, error)
}

// Memberships is what the team use cases need from the membership table.
type Memberships interface {
	CreateMembership(ctx context.Context, teamID, userID int64, role domain.Role, status domain.MembershipStatus) (int64, error)
}

type Deps struct {
	Tx          application.TxRunner
	Teams       Teams
	Memberships Memberships
}

type Service struct {
	tx          application.TxRunner
	teams       Teams
	memberships Memberships
}

func NewService(d Deps) *Service {
	return &Service{tx: d.Tx, teams: d.Teams, memberships: d.Memberships}
}

// Create creates a team and makes the creator an active admin of it, in one
// transaction (R6.1).
func (s *Service) Create(ctx context.Context, userID int64, name, timezone string) (domain.Team, error) {
	name, err := checkName(name)
	if err != nil {
		return domain.Team{}, err
	}
	if err := checkTimezone(timezone); err != nil {
		return domain.Team{}, err
	}

	var team domain.Team
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		t, err := s.teams.CreateTeam(ctx, name, timezone)
		if err != nil {
			return err
		}
		if _, err := s.memberships.CreateMembership(ctx, t.ID, userID, domain.RoleAdmin, domain.MembershipActive); err != nil {
			return err
		}
		team = t
		return nil
	})
	if err != nil {
		return domain.Team{}, err
	}
	return team, nil
}

// List returns the teams where the user is an active member. Pending and left
// memberships do not count (R7.6, R8.5).
func (s *Service) List(ctx context.Context, userID int64) ([]domain.Team, error) {
	return s.teams.ListActiveForUser(ctx, userID)
}

// Get returns a team. Callers must have checked the user is an active member.
func (s *Service) Get(ctx context.Context, teamID int64) (domain.Team, error) {
	return s.teams.GetTeam(ctx, teamID)
}

// Update changes the name, the timezone or both (R6.7). A nil field keeps
// its value. Callers must have checked the user is an admin of the team.
func (s *Service) Update(ctx context.Context, teamID int64, name, timezone *string) (domain.Team, error) {
	if name == nil && timezone == nil {
		return domain.Team{}, ErrNothingToUpdate
	}
	if name != nil {
		n, err := checkName(*name)
		if err != nil {
			return domain.Team{}, err
		}
		name = &n
	}
	if timezone != nil {
		if err := checkTimezone(*timezone); err != nil {
			return domain.Team{}, err
		}
	}
	return s.teams.UpdateTeam(ctx, teamID, name, timezone)
}

// checkName trims the name and counts characters, not bytes, so accents and
// ñ count as one. It rejects control characters (NUL makes Postgres fail)
// and invisible format characters (bidi overrides, zero-width spaces) that
// can disguise a name in the roster and in pushes. The zero-width joiner
// stays: compound emoji need it (a family emoji is man, ZWJ, woman, ZWJ,
// girl).
func checkName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > maxNameLen {
		return "", ErrInvalidName
	}
	for _, r := range name {
		if unicode.IsControl(r) || (unicode.Is(unicode.Cf, r) && r != zeroWidthJoiner) {
			return "", ErrInvalidName
		}
	}
	return name, nil
}

const zeroWidthJoiner = '\u200d'

// ianaZone is the shape of a canonical zone name: Area/Location, maybe with
// a sub-location (America/Argentina/Buenos_Aires). LoadLocation alone also
// accepts "", "Local", "posixrules", "Factory" and uncleaned paths like
// "America/./Tijuana", which Postgres's AT TIME ZONE rejects.
var ianaZone = regexp.MustCompile(`^[A-Z][A-Za-z]*(/[A-Za-z0-9_+-]+){1,2}$`)

// checkTimezone accepts only IANA zone names that load (R6.7).
func checkTimezone(tz string) error {
	if !ianaZone.MatchString(tz) {
		return ErrInvalidTimezone
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return ErrInvalidTimezone
	}
	return nil
}

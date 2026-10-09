// Package teams holds the team use cases.
package teams

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

var (
	ErrInvalidName     = errors.New("team name must be 1 to 35 characters")
	ErrInvalidTimezone = errors.New("timezone must be an IANA zone, like America/Tijuana")
)

const maxNameLen = 35

// Teams is what the team use cases need from the team table.
type Teams interface {
	CreateTeam(ctx context.Context, name, timezone string) (domain.Team, error)
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

// checkName trims the name and counts characters, not bytes, so accents and
// ñ count as one.
func checkName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" || utf8.RuneCountInString(name) > maxNameLen {
		return "", ErrInvalidName
	}
	return name, nil
}

// checkTimezone accepts only IANA zone names (R6.7). LoadLocation also
// accepts "" (UTC) and "Local" (the server's zone), which are not.
func checkTimezone(tz string) error {
	if tz == "" || tz == "Local" {
		return ErrInvalidTimezone
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return ErrInvalidTimezone
	}
	return nil
}

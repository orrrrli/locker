// Package matches holds the match use cases: create, list and update (R9.1,
// R9.2). A match is the only event type.
package matches

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

var (
	ErrInvalidStartsAt  = errors.New("starts_at is required")
	ErrInvalidRivalName = errors.New("rival_name must be 1 to 60 characters, with no control characters")
	ErrInvalidLocation  = errors.New("location must be 1 to 120 characters, with no control characters")
	ErrNothingToUpdate  = errors.New("send starts_at, rival_name, location or any of them")
)

const (
	maxRivalLen    = 60
	maxLocationLen = 120
)

// Matches is what the match use cases need from the match table.
type Matches interface {
	CreateMatch(ctx context.Context, teamID int64, startsAt time.Time, rival, location string, createdBy int64) (domain.Match, error)
	ListMatches(ctx context.Context, teamID int64) ([]domain.Match, error)
	// UpdateMatch returns domain.ErrNotFound when the team has no such match.
	UpdateMatch(ctx context.Context, teamID, id int64, startsAt *time.Time, rival, location *string) (domain.Match, error)
}

type Deps struct {
	Tx          application.TxRunner
	Matches     Matches
	Memberships application.AdminLocker
}

type Service struct {
	tx          application.TxRunner
	matches     Matches
	memberships application.AdminLocker
}

func NewService(d Deps) *Service {
	return &Service{tx: d.Tx, matches: d.Matches, memberships: d.Memberships}
}

// Create schedules a match for the team (R9.1). startsAt is stored in UTC.
// callerID is the admin's membership, checked by requireRole and again under
// the team lock; it is also recorded as the creator.
func (s *Service) Create(ctx context.Context, teamID, callerID int64, startsAt time.Time, rival, location string) (domain.Match, error) {
	if startsAt.IsZero() {
		return domain.Match{}, ErrInvalidStartsAt
	}
	rival, ok := cleanText(rival, maxRivalLen)
	if !ok {
		return domain.Match{}, ErrInvalidRivalName
	}
	location, ok = cleanText(location, maxLocationLen)
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
	return out, nil
}

// List returns the team's matches, soonest first. Callers must have checked
// the user is an active member.
func (s *Service) List(ctx context.Context, teamID int64) ([]domain.Match, error) {
	return s.matches.ListMatches(ctx, teamID)
}

// Update lets the admin callerID change the time, the rival, the place or
// any of them. A nil field keeps its value.
func (s *Service) Update(ctx context.Context, teamID, callerID, matchID int64, startsAt *time.Time, rival, location *string) (domain.Match, error) {
	if startsAt == nil && rival == nil && location == nil {
		return domain.Match{}, ErrNothingToUpdate
	}
	if startsAt != nil {
		if startsAt.IsZero() {
			return domain.Match{}, ErrInvalidStartsAt
		}
		u := startsAt.UTC()
		startsAt = &u
	}
	if rival != nil {
		r, ok := cleanText(*rival, maxRivalLen)
		if !ok {
			return domain.Match{}, ErrInvalidRivalName
		}
		rival = &r
	}
	if location != nil {
		l, ok := cleanText(*location, maxLocationLen)
		if !ok {
			return domain.Match{}, ErrInvalidLocation
		}
		location = &l
	}
	var out domain.Match
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := application.LockTeamAsAdmin(ctx, s.memberships, teamID, callerID); err != nil {
			return err
		}
		m, err := s.matches.UpdateMatch(ctx, teamID, matchID, startsAt, rival, location)
		out = m
		return err
	})
	if err != nil {
		return domain.Match{}, err
	}
	return out, nil
}

// cleanText trims free text and checks it has 1 to max characters (not
// bytes) and no control characters, which Postgres rejects (NUL) or that
// break a push line.
func cleanText(raw string, max int) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" || utf8.RuneCountInString(s) > max {
		return "", false
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", false
		}
	}
	return s, true
}

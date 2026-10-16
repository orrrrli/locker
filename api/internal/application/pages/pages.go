// Package pages holds the public team, venue and league pages (R17). Any
// signed-in user may read them; they show nothing a team keeps to its
// members (R17.5, NFR 1).
package pages

import (
	"context"
	"errors"
	"time"

	"github.com/orrrrli/locker/api/internal/domain"
)

// Pages reads the public pages. Team, Venue and League return
// domain.ErrNotFound for an unknown id; Team also for a dissolved team.
type Pages interface {
	Team(ctx context.Context, id int64) (domain.PublicTeam, error)
	Venue(ctx context.Context, id int64) (domain.VenuePage, error)
	League(ctx context.Context, id int64) (domain.LeaguePage, error)
	LeagueTeams(ctx context.Context, leagueID int64) ([]domain.LeagueTeam, error)
	NextFixtureDay(ctx context.Context, leagueID int64, now time.Time) (domain.FixtureDay, error)
}

// Memberships finds the viewer's own membership in the team, whatever its
// status, or domain.ErrNotFound.
type Memberships interface {
	MembershipByTeamAndUser(ctx context.Context, teamID, userID int64) (domain.Membership, error)
}

type Deps struct {
	Pages       Pages
	Memberships Memberships
	Now         func() time.Time // nil means time.Now
}

type Service struct {
	pages       Pages
	memberships Memberships
	now         func() time.Time
}

func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{pages: d.Pages, memberships: d.Memberships, now: d.Now}
}

// Team returns the team's public page as viewerID sees it (R17.1-R17.6): an
// active or pending viewer gets their status, so the app offers no join
// request, or shows the request pending (R13.7).
func (s *Service) Team(ctx context.Context, viewerID, teamID int64) (domain.PublicTeam, error) {
	t, err := s.pages.Team(ctx, teamID)
	if err != nil {
		return domain.PublicTeam{}, err
	}
	m, err := s.memberships.MembershipByTeamAndUser(ctx, teamID, viewerID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
	case err != nil:
		return domain.PublicTeam{}, err
	case m.Status == domain.MembershipActive || m.Status == domain.MembershipPending:
		t.ViewerStatus = &m.Status
	}
	return t, nil
}

// Venue returns the venue's page (R17.7).
func (s *Service) Venue(ctx context.Context, id int64) (domain.VenuePage, error) {
	return s.pages.Venue(ctx, id)
}

// League returns the league's page (R17.8).
func (s *Service) League(ctx context.Context, id int64) (domain.LeaguePage, error) {
	return s.pages.League(ctx, id)
}

// LeagueTeams returns the league's enrolled teams; domain.ErrNotFound for an
// unknown league.
func (s *Service) LeagueTeams(ctx context.Context, leagueID int64) ([]domain.LeagueTeam, error) {
	if _, err := s.pages.League(ctx, leagueID); err != nil {
		return nil, err
	}
	return s.pages.LeagueTeams(ctx, leagueID)
}

// NextFixtures returns the fixtures of the league's earliest date, in the
// venue's timezone, that has one starting after now; weeks without fixtures
// are skipped (R17.8, open question 29). domain.ErrNotFound for an unknown
// league.
func (s *Service) NextFixtures(ctx context.Context, leagueID int64) (domain.FixtureDay, error) {
	if _, err := s.pages.League(ctx, leagueID); err != nil {
		return domain.FixtureDay{}, err
	}
	return s.pages.NextFixtureDay(ctx, leagueID, s.now().UTC())
}

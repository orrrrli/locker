package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// Pages reads the public team, venue and league pages (R17).
type Pages struct {
	pool *pgxpool.Pool
}

func NewPages(pool *pgxpool.Pool) *Pages {
	return &Pages{pool: pool}
}

// Team returns the team's page without the viewer's status, or
// domain.ErrNotFound for an unknown or dissolved team.
func (r *Pages) Team(ctx context.Context, id int64) (domain.PublicTeam, error) {
	q := sqlcdb.New(Conn(ctx, r.pool))
	row, err := q.GetPublicTeam(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PublicTeam{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.PublicTeam{}, fmt.Errorf("postgres: public team: %w", err)
	}
	captains, err := q.ListTeamCaptains(ctx, id)
	if err != nil {
		return domain.PublicTeam{}, fmt.Errorf("postgres: team captains: %w", err)
	}
	leagues, err := q.ListTeamLeagues(ctx, id)
	if err != nil {
		return domain.PublicTeam{}, fmt.Errorf("postgres: team leagues: %w", err)
	}
	t := domain.PublicTeam{
		ID: row.ID, Name: row.Name, Initials: domain.Initials(row.Name), ActiveMembers: int(row.ActiveMembers),
		Followers: int(row.Followers),
		Captains:  captains, Leagues: make([]domain.TeamLeague, len(leagues)),
	}
	for i, l := range leagues {
		t.Leagues[i] = domain.TeamLeague{
			ID: l.ID, Name: l.Name, Weekday: domain.Weekday(l.Weekday),
			StartTime: timeOfDay(l.StartTime.Microseconds), EndTime: timeOfDay(l.EndTime.Microseconds),
			VenueID: l.VenueID, VenueName: l.VenueName,
		}
	}
	return t, nil
}

// Venue returns the venue's page, or domain.ErrNotFound.
func (r *Pages) Venue(ctx context.Context, id int64) (domain.VenuePage, error) {
	q := sqlcdb.New(Conn(ctx, r.pool))
	row, err := q.GetVenue(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.VenuePage{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.VenuePage{}, fmt.Errorf("postgres: get venue: %w", err)
	}
	courts, err := NewVenues(r.pool).ListCourts(ctx, id)
	if err != nil {
		return domain.VenuePage{}, err
	}
	leagues, err := q.ListVenueLeagues(ctx, id)
	if err != nil {
		return domain.VenuePage{}, fmt.Errorf("postgres: venue leagues: %w", err)
	}
	p := domain.VenuePage{Venue: venueFromRow(sqlcdb.Venue(row)), Leagues: make([]domain.League, len(leagues))}
	p.Courts = courts
	for i, l := range leagues {
		p.Leagues[i] = domain.League{
			ID: l.ID, VenueID: id, Name: l.Name, Weekday: domain.Weekday(l.Weekday),
			StartTime: timeOfDay(l.StartTime.Microseconds), EndTime: timeOfDay(l.EndTime.Microseconds),
		}
	}
	return p, nil
}

// League returns the league's page, or domain.ErrNotFound.
func (r *Pages) League(ctx context.Context, id int64) (domain.LeaguePage, error) {
	l, err := NewLeagues(r.pool).GetLeague(ctx, id)
	if err != nil {
		return domain.LeaguePage{}, err
	}
	q := sqlcdb.New(Conn(ctx, r.pool))
	v, err := q.GetLeagueVenue(ctx, id)
	if err != nil {
		return domain.LeaguePage{}, fmt.Errorf("postgres: league venue: %w", err)
	}
	followers, err := q.CountLeagueFollowers(ctx, pgInt8(id))
	if err != nil {
		return domain.LeaguePage{}, fmt.Errorf("postgres: league followers: %w", err)
	}
	return domain.LeaguePage{
		League: l, Venue: domain.Venue{ID: v.ID, Name: v.Name, Address: v.Address, Timezone: v.Timezone}, Followers: int(followers),
	}, nil
}

// LeagueTeams returns the league's teams that are not dissolved, by name.
func (r *Pages) LeagueTeams(ctx context.Context, leagueID int64) ([]domain.LeagueTeam, error) {
	rows, err := sqlcdb.New(Conn(ctx, r.pool)).ListLeagueTeams(ctx, leagueID)
	if err != nil {
		return nil, fmt.Errorf("postgres: league teams: %w", err)
	}
	out := make([]domain.LeagueTeam, len(rows))
	for i, row := range rows {
		out[i] = domain.LeagueTeam{ID: row.ID, Name: row.Name, Initials: domain.Initials(row.Name)}
	}
	return out, nil
}

// NextFixtureDay returns the fixtures of the league's next date with one
// starting after now, in the venue's timezone.
func (r *Pages) NextFixtureDay(ctx context.Context, leagueID int64, now time.Time) (domain.FixtureDay, error) {
	rows, err := sqlcdb.New(Conn(ctx, r.pool)).ListNextFixtureDay(ctx, sqlcdb.ListNextFixtureDayParams{LeagueID: leagueID, Now: timestamptz(now)})
	if err != nil {
		return domain.FixtureDay{}, fmt.Errorf("postgres: next fixture day: %w", err)
	}
	out := domain.FixtureDay{Fixtures: make([]domain.FixtureLine, len(rows))}
	for i, row := range rows {
		if i == 0 {
			d := row.Day.Time
			out.Day = &d
		}
		out.Fixtures[i] = domain.FixtureLine{
			ID: row.ID, StartsAt: row.StartsAt.Time.UTC(), CourtID: row.CourtID, CourtName: row.CourtName,
			HomeTeamID: row.HomeTeamID, HomeTeamName: row.HomeTeamName, AwayTeamID: row.AwayTeamID, AwayTeamName: row.AwayTeamName,
		}
	}
	return out, nil
}

func timeOfDay(microseconds int64) domain.TimeOfDay {
	return domain.TimeOfDay(microseconds / int64(time.Minute/time.Microsecond))
}

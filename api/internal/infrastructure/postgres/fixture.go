package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// Fixture queries live on the Leagues repository: a fixture belongs to its
// league, and writing one writes its two matches in the same transaction.

// CountEnrolled counts how many of teamIDs are enrolled in the league.
func (r *Leagues) CountEnrolled(ctx context.Context, leagueID int64, teamIDs []int64) (int, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).CountEnrolled(ctx, sqlcdb.CountEnrolledParams{LeagueID: leagueID, TeamIds: teamIDs})
	if err != nil {
		return 0, fmt.Errorf("postgres: count enrolled: %w", err)
	}
	return int(n), nil
}

// CourtInVenue returns the court when it belongs to the venue, or
// domain.ErrNotFound.
func (r *Leagues) CourtInVenue(ctx context.Context, courtID, venueID int64) (domain.FixtureCourt, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).GetCourtInVenue(ctx, sqlcdb.GetCourtInVenueParams{ID: courtID, VenueID: venueID})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.FixtureCourt{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.FixtureCourt{}, fmt.Errorf("postgres: court in venue: %w", err)
	}
	return domain.FixtureCourt{CourtName: row.CourtName, VenueName: row.VenueName, Timezone: row.Timezone}, nil
}

func (r *Leagues) CreateFixture(ctx context.Context, f domain.Fixture) (domain.Fixture, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).CreateFixture(ctx, sqlcdb.CreateFixtureParams{
		LeagueID: f.LeagueID, HomeTeamID: f.HomeTeamID, AwayTeamID: f.AwayTeamID, CourtID: f.CourtID, StartsAt: timestamptz(f.StartsAt),
	})
	if err != nil {
		return domain.Fixture{}, fmt.Errorf("postgres: create fixture: %w", err)
	}
	return fixtureFromRow(row), nil
}

// LockFixture returns the league's fixture, held until the transaction
// ends, or domain.ErrNotFound.
func (r *Leagues) LockFixture(ctx context.Context, leagueID, id int64) (domain.Fixture, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).LockFixture(ctx, sqlcdb.LockFixtureParams{LeagueID: leagueID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Fixture{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Fixture{}, fmt.Errorf("postgres: lock fixture: %w", err)
	}
	return fixtureFromRow(row), nil
}

// UpdateFixture sets the fixture's time and court.
func (r *Leagues) UpdateFixture(ctx context.Context, id int64, startsAt time.Time, courtID int64) (domain.Fixture, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).UpdateFixture(ctx, sqlcdb.UpdateFixtureParams{ID: id, StartsAt: timestamptz(startsAt), CourtID: courtID})
	if err != nil {
		return domain.Fixture{}, fmt.Errorf("postgres: update fixture: %w", err)
	}
	return fixtureFromRow(row), nil
}

// CancelFixture cancels the fixture and its matches that are not cancelled
// yet, and returns the matches as they are now.
func (r *Leagues) CancelFixture(ctx context.Context, id int64, now time.Time) ([]domain.Match, error) {
	q := sqlcdb.New(Conn(ctx, r.pool))
	if err := q.CancelFixture(ctx, sqlcdb.CancelFixtureParams{ID: id, CancelledAt: timestamptz(now)}); err != nil {
		return nil, fmt.Errorf("postgres: cancel fixture: %w", err)
	}
	if err := q.CancelFixtureMatches(ctx, sqlcdb.CancelFixtureMatchesParams{FixtureID: pgInt8(id), CancelledAt: timestamptz(now)}); err != nil {
		return nil, fmt.Errorf("postgres: cancel fixture matches: %w", err)
	}
	return r.FixtureMatches(ctx, id)
}

// CreateFixtureMatch creates the fixture's match for one of its teams.
func (r *Leagues) CreateFixtureMatch(ctx context.Context, teamID, fixtureID, courtID int64, startsAt time.Time, location string) (domain.Match, error) {
	q := sqlcdb.New(Conn(ctx, r.pool))
	id, err := q.CreateFixtureMatch(ctx, sqlcdb.CreateFixtureMatchParams{
		TeamID: teamID, StartsAt: timestamptz(startsAt), Location: location, FixtureID: pgInt8(fixtureID), CourtID: pgInt8(courtID),
	})
	if err != nil {
		return domain.Match{}, fmt.Errorf("postgres: create fixture match: %w", err)
	}
	row, err := q.GetMatch(ctx, id)
	return matchOrNotFound(row, err, "read fixture match")
}

// LockFixtureMatches holds the fixture's matches until the transaction ends
// and returns them, oldest first.
func (r *Leagues) LockFixtureMatches(ctx context.Context, fixtureID int64) ([]domain.Match, error) {
	if _, err := sqlcdb.New(Conn(ctx, r.pool)).LockFixtureMatches(ctx, pgInt8(fixtureID)); err != nil {
		return nil, fmt.Errorf("postgres: lock fixture matches: %w", err)
	}
	return r.FixtureMatches(ctx, fixtureID)
}

// FixtureMatches returns the fixture's matches, oldest first.
func (r *Leagues) FixtureMatches(ctx context.Context, fixtureID int64) ([]domain.Match, error) {
	rows, err := sqlcdb.New(Conn(ctx, r.pool)).ListFixtureMatches(ctx, pgInt8(fixtureID))
	if err != nil {
		return nil, fmt.Errorf("postgres: list fixture matches: %w", err)
	}
	out := make([]domain.Match, len(rows))
	for i, row := range rows {
		out[i] = matchFromRow(row)
	}
	return out, nil
}

// UpdateFixtureMatches moves the fixture's matches that are not cancelled to
// the new time and court, and returns them all.
func (r *Leagues) UpdateFixtureMatches(ctx context.Context, fixtureID int64, startsAt time.Time, courtID int64, location string) ([]domain.Match, error) {
	err := sqlcdb.New(Conn(ctx, r.pool)).UpdateFixtureMatches(ctx, sqlcdb.UpdateFixtureMatchesParams{
		FixtureID: pgInt8(fixtureID), StartsAt: timestamptz(startsAt), CourtID: pgInt8(courtID), Location: location,
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: update fixture matches: %w", err)
	}
	return r.FixtureMatches(ctx, fixtureID)
}

// FutureFixturesOfTeam returns the team's fixtures in the league that start
// after now and are not cancelled, held until the transaction ends.
func (r *Leagues) FutureFixturesOfTeam(ctx context.Context, leagueID, teamID int64, now time.Time) ([]domain.Fixture, error) {
	rows, err := sqlcdb.New(Conn(ctx, r.pool)).ListFutureFixturesOfTeam(ctx, sqlcdb.ListFutureFixturesOfTeamParams{
		LeagueID: leagueID, TeamID: teamID, Now: timestamptz(now),
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: list future fixtures of team: %w", err)
	}
	out := make([]domain.Fixture, len(rows))
	for i, row := range rows {
		out[i] = fixtureFromRow(row)
	}
	return out, nil
}

func fixtureFromRow(row sqlcdb.Fixture) domain.Fixture {
	return domain.Fixture{
		ID:          row.ID,
		LeagueID:    row.LeagueID,
		HomeTeamID:  row.HomeTeamID,
		AwayTeamID:  row.AwayTeamID,
		CourtID:     row.CourtID,
		StartsAt:    row.StartsAt.Time.UTC(),
		CancelledAt: utcPtr(row.CancelledAt),
		CreatedAt:   row.CreatedAt.Time,
	}
}

func pgInt8(v int64) pgtype.Int8 { return pgtype.Int8{Int64: v, Valid: true} }

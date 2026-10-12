package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// Matches is the match repository.
type Matches struct {
	pool *pgxpool.Pool
}

func NewMatches(pool *pgxpool.Pool) *Matches {
	return &Matches{pool: pool}
}

func (r *Matches) CreateMatch(ctx context.Context, teamID int64, startsAt time.Time, rival, location string, createdBy int64) (domain.Match, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).CreateMatch(ctx, sqlcdb.CreateMatchParams{
		TeamID:    teamID,
		StartsAt:  timestamptz(startsAt),
		RivalName: rival,
		Location:  location,
		CreatedBy: createdBy,
	})
	if err != nil {
		return domain.Match{}, fmt.Errorf("postgres: create match: %w", err)
	}
	return matchFromRow(row), nil
}

// ListMatches returns every match of the team, soonest first.
func (r *Matches) ListMatches(ctx context.Context, teamID int64) ([]domain.Match, error) {
	rows, err := sqlcdb.New(Conn(ctx, r.pool)).ListMatches(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list matches: %w", err)
	}
	out := make([]domain.Match, len(rows))
	for i, row := range rows {
		out[i] = matchFromRow(row)
	}
	return out, nil
}

// MatchByID returns the match in any team, or domain.ErrNotFound. Routes on
// /matches/{id} use it to find the team.
func (r *Matches) MatchByID(ctx context.Context, id int64) (domain.Match, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).GetMatch(ctx, id)
	return matchOrNotFound(row, err, "match by id")
}

// UpdateMatch sets the fields that are not nil on the team's match and
// returns it, or domain.ErrNotFound when the team has no such match.
func (r *Matches) UpdateMatch(ctx context.Context, teamID, id int64, startsAt *time.Time, rival, location *string) (domain.Match, error) {
	arg := sqlcdb.UpdateMatchParams{ID: id, TeamID: teamID, RivalName: optText(rival), Location: optText(location)}
	if startsAt != nil {
		arg.StartsAt = timestamptz(*startsAt)
	}
	row, err := sqlcdb.New(Conn(ctx, r.pool)).UpdateMatch(ctx, arg)
	return matchOrNotFound(row, err, "update match")
}

func matchOrNotFound(row sqlcdb.Match, err error, op string) (domain.Match, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Match{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Match{}, fmt.Errorf("postgres: %s: %w", op, err)
	}
	return matchFromRow(row), nil
}

// matchFromRow keeps every timestamp in UTC (R9.1), whatever zone the driver
// decoded it in.
func matchFromRow(row sqlcdb.Match) domain.Match {
	return domain.Match{
		ID:                 row.ID,
		TeamID:             row.TeamID,
		StartsAt:           row.StartsAt.Time.UTC(),
		RivalName:          row.RivalName,
		Location:           row.Location,
		ReminderSentAt:     utcPtr(row.ReminderSentAt),
		AttendanceClosedAt: utcPtr(row.AttendanceClosedAt),
		CreatedBy:          row.CreatedBy,
	}
}

func utcPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

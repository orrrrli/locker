package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// Teams is the team repository.
type Teams struct {
	pool *pgxpool.Pool
}

func NewTeams(pool *pgxpool.Pool) *Teams {
	return &Teams{pool: pool}
}

func (r *Teams) CreateTeam(ctx context.Context, name, timezone string) (domain.Team, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).CreateTeam(ctx, sqlcdb.CreateTeamParams{Name: name, Timezone: timezone})
	if err != nil {
		return domain.Team{}, fmt.Errorf("postgres: create team: %w", err)
	}
	return teamFromRow(row), nil
}

// ListActiveForUser returns the teams where the user has an active
// membership, oldest first.
func (r *Teams) ListActiveForUser(ctx context.Context, userID int64) ([]domain.Team, error) {
	rows, err := sqlcdb.New(Conn(ctx, r.pool)).ListActiveTeamsForUser(ctx, pgtype.Int8{Int64: userID, Valid: true})
	if err != nil {
		return nil, fmt.Errorf("postgres: list teams for user: %w", err)
	}
	out := make([]domain.Team, len(rows))
	for i, row := range rows {
		out[i] = teamFromRow(row)
	}
	return out, nil
}

// GetTeam returns the team or domain.ErrNotFound.
func (r *Teams) GetTeam(ctx context.Context, id int64) (domain.Team, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).GetTeam(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Team{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Team{}, fmt.Errorf("postgres: get team: %w", err)
	}
	return teamFromRow(row), nil
}

// UpdateTeam sets the fields that are not nil and returns the team, or
// domain.ErrNotFound.
func (r *Teams) UpdateTeam(ctx context.Context, id int64, name, timezone *string) (domain.Team, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).UpdateTeam(ctx, sqlcdb.UpdateTeamParams{
		ID:       id,
		Name:     optText(name),
		Timezone: optText(timezone),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Team{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Team{}, fmt.Errorf("postgres: update team: %w", err)
	}
	return teamFromRow(row), nil
}

func optText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

func teamFromRow(row sqlcdb.Team) domain.Team {
	t := domain.Team{
		ID:        row.ID,
		Name:      row.Name,
		Timezone:  row.Timezone,
		CreatedAt: row.CreatedAt.Time,
	}
	if row.CaptainMembershipID.Valid {
		t.CaptainMembershipID = &row.CaptainMembershipID.Int64
	}
	return t
}

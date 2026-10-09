package postgres

import (
	"context"
	"fmt"

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

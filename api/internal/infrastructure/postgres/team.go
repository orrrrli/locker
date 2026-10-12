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
	return teamFromRow(sqlcdb.GetTeamRow(row)), nil
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
		out[i] = teamFromRow(sqlcdb.GetTeamRow(row))
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
	return teamFromRow(sqlcdb.GetTeamRow(row)), nil
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
	return teamFromRow(sqlcdb.GetTeamRow(row)), nil
}

// CountActiveMembers counts the team's active memberships.
func (r *Teams) CountActiveMembers(ctx context.Context, teamID int64) (int, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).CountActiveMembers(ctx, teamID)
	if err != nil {
		return 0, fmt.Errorf("postgres: count active members: %w", err)
	}
	return int(n), nil
}

// Dissolve revokes the team's invites, rejects its pending memberships, sets
// the caller's membership left and marks the team dissolved (R6.8). It must
// run inside the use case's transaction, under the team lock.
//
// Separate statements, in this order: revoking waits for an invite accept
// that holds FOR SHARE on the invite, and each later statement takes a new
// snapshot under READ COMMITTED, so it rejects the pending row that accept
// just committed. One CTE would share one snapshot and miss it.
func (r *Teams) Dissolve(ctx context.Context, teamID, callerID int64) error {
	q := sqlcdb.New(Conn(ctx, r.pool))
	if err := q.RevokeTeamInvites(ctx, teamID); err != nil {
		return fmt.Errorf("postgres: revoke team invites: %w", err)
	}
	if err := q.DeleteNeverJoinedPendingMembershipsOfTeam(ctx, teamID); err != nil {
		return fmt.Errorf("postgres: delete pending of team: %w", err)
	}
	if err := q.SetReturningPendingMembershipsOfTeamLeft(ctx, teamID); err != nil {
		return fmt.Errorf("postgres: set returning pending of team left: %w", err)
	}
	if n, err := q.SetMembershipLeft(ctx, callerID); err != nil {
		return fmt.Errorf("postgres: set caller left: %w", err)
	} else if n == 0 {
		return domain.ErrNotFound
	}
	if n, err := q.SetTeamDissolved(ctx, teamID); err != nil {
		return fmt.Errorf("postgres: set team dissolved: %w", err)
	} else if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func optText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{}
	}
	return pgtype.Text{String: *s, Valid: true}
}

// teamFromRow takes GetTeamRow: every team query names the same columns, so
// each query's row type converts to it.
func teamFromRow(row sqlcdb.GetTeamRow) domain.Team {
	return domain.Team{
		ID:        row.ID,
		Name:      row.Name,
		Timezone:  row.Timezone,
		CreatedAt: row.CreatedAt.Time,
	}
}

package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// Leagues is the repository for leagues and their enrollments.
type Leagues struct {
	pool *pgxpool.Pool
}

func NewLeagues(pool *pgxpool.Pool) *Leagues {
	return &Leagues{pool: pool}
}

// CreateLeague stores l (its ID and CreatedAt are ignored). It returns
// domain.ErrNotFound when l.VenueID is not a venue.
func (r *Leagues) CreateLeague(ctx context.Context, l domain.League) (domain.League, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).CreateLeague(ctx, sqlcdb.CreateLeagueParams{
		VenueID:        l.VenueID,
		Name:           l.Name,
		Weekday:        int16(l.Weekday),
		StartTime:      pgTime(l.StartTime),
		EndTime:        pgTime(l.EndTime),
		SeasonName:     l.SeasonName,
		SeasonStartsOn: pgDate(l.SeasonStartsOn),
		SeasonEndsOn:   pgDate(l.SeasonEndsOn),
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolationCode {
		return domain.League{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.League{}, fmt.Errorf("postgres: create league: %w", err)
	}
	return leagueFromRow(row), nil
}

// LockLeague returns the league and holds its row until the transaction
// ends, or domain.ErrNotFound. It must run inside a transaction.
func (r *Leagues) LockLeague(ctx context.Context, id int64) (domain.League, error) {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); !ok {
		return domain.League{}, errors.New("postgres: lock league: no transaction in ctx")
	}
	row, err := sqlcdb.New(Conn(ctx, r.pool)).LockLeague(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.League{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.League{}, fmt.Errorf("postgres: lock league: %w", err)
	}
	return leagueFromRow(row), nil
}

// GetLeague returns the league or domain.ErrNotFound.
func (r *Leagues) GetLeague(ctx context.Context, id int64) (domain.League, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).GetLeague(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.League{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.League{}, fmt.Errorf("postgres: get league: %w", err)
	}
	return leagueFromRow(row), nil
}

// UpdateLeague writes every field of l but the venue, or returns
// domain.ErrNotFound.
func (r *Leagues) UpdateLeague(ctx context.Context, l domain.League) (domain.League, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).UpdateLeague(ctx, sqlcdb.UpdateLeagueParams{
		ID:             l.ID,
		Name:           l.Name,
		Weekday:        int16(l.Weekday),
		StartTime:      pgTime(l.StartTime),
		EndTime:        pgTime(l.EndTime),
		SeasonName:     l.SeasonName,
		SeasonStartsOn: pgDate(l.SeasonStartsOn),
		SeasonEndsOn:   pgDate(l.SeasonEndsOn),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.League{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.League{}, fmt.Errorf("postgres: update league: %w", err)
	}
	return leagueFromRow(row), nil
}

// EnrollTeam returns domain.ErrAlreadyExists when the team is already in the
// league (R16.7).
func (r *Leagues) EnrollTeam(ctx context.Context, leagueID, teamID int64) (domain.Enrollment, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).EnrollTeam(ctx, sqlcdb.EnrollTeamParams{LeagueID: leagueID, TeamID: teamID})
	if isUniqueViolation(err) {
		return domain.Enrollment{}, domain.ErrAlreadyExists
	}
	if err != nil {
		return domain.Enrollment{}, fmt.Errorf("postgres: enroll team: %w", err)
	}
	return domain.Enrollment{LeagueID: row.LeagueID, TeamID: row.TeamID, EnrolledAt: row.EnrolledAt.Time}, nil
}

// UnenrollTeam returns domain.ErrNotFound when the team is not in the league.
func (r *Leagues) UnenrollTeam(ctx context.Context, leagueID, teamID int64) error {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).UnenrollTeam(ctx, sqlcdb.UnenrollTeamParams{LeagueID: leagueID, TeamID: teamID})
	if err != nil {
		return fmt.Errorf("postgres: unenroll team: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func pgTime(t domain.TimeOfDay) pgtype.Time {
	return pgtype.Time{Microseconds: int64(t) * int64(time.Minute/time.Microsecond), Valid: true}
}

func pgDate(d time.Time) pgtype.Date {
	return pgtype.Date{Time: d, Valid: true}
}

func leagueFromRow(row sqlcdb.League) domain.League {
	return domain.League{
		ID:             row.ID,
		VenueID:        row.VenueID,
		Name:           row.Name,
		Weekday:        domain.Weekday(row.Weekday),
		StartTime:      domain.TimeOfDay(row.StartTime.Microseconds / int64(time.Minute/time.Microsecond)),
		EndTime:        domain.TimeOfDay(row.EndTime.Microseconds / int64(time.Minute/time.Microsecond)),
		SeasonName:     row.SeasonName,
		SeasonStartsOn: row.SeasonStartsOn.Time,
		SeasonEndsOn:   row.SeasonEndsOn.Time,
		CreatedAt:      row.CreatedAt.Time,
	}
}

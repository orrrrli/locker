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
	q := sqlcdb.New(Conn(ctx, r.pool))
	id, err := q.CreateMatch(ctx, sqlcdb.CreateMatchParams{
		TeamID:    teamID,
		StartsAt:  timestamptz(startsAt),
		RivalName: pgtype.Text{String: rival, Valid: true},
		Location:  location,
		CreatedBy: pgtype.Int8{Int64: createdBy, Valid: true},
	})
	if err != nil {
		return domain.Match{}, fmt.Errorf("postgres: create match: %w", err)
	}
	row, err := q.GetMatch(ctx, id)
	return matchOrNotFound(row, err, "read created match")
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

// ListUpcomingForUser returns the matches that start after now in every
// team where the user is active, soonest first (R14).
func (r *Matches) ListUpcomingForUser(ctx context.Context, userID int64, now time.Time) ([]domain.UpcomingMatch, error) {
	rows, err := sqlcdb.New(Conn(ctx, r.pool)).ListUpcomingMatchesForUser(ctx, sqlcdb.ListUpcomingMatchesForUserParams{
		UserID: pgtype.Int8{Int64: userID, Valid: true},
		Now:    timestamptz(now),
	})
	if err != nil {
		return nil, fmt.Errorf("postgres: list upcoming matches for user: %w", err)
	}
	out := make([]domain.UpcomingMatch, len(rows))
	for i, row := range rows {
		u := domain.UpcomingMatch{
			Match:         matchFromRow(row.MatchView),
			TeamName:      row.TeamName,
			Role:          domain.Role(row.Role),
			GoingCount:    int(row.Going),
			NotGoingCount: int(row.NotGoing),
		}
		if row.OwnAnswer.Valid {
			a := domain.RSVPAnswer(row.OwnAnswer.String)
			u.Answer = &a
		}
		out[i] = u
	}
	return out, nil
}

// MatchByID returns the match in any team, or domain.ErrNotFound. Routes on
// /matches/{id} use it to find the team.
func (r *Matches) MatchByID(ctx context.Context, id int64) (domain.Match, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).GetMatch(ctx, id)
	return matchOrNotFound(row, err, "match by id")
}

// MatchInTeam returns the team's match with that id, or domain.ErrNotFound
// when the id belongs to another team or to none.
func (r *Matches) MatchInTeam(ctx context.Context, teamID, id int64) (domain.Match, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).GetMatchInTeam(ctx, sqlcdb.GetMatchInTeamParams{TeamID: teamID, ID: id})
	return matchOrNotFound(row, err, "match in team")
}

// UpsertRSVP stores the member's answer for the match, replacing any
// earlier one, and returns the row.
func (r *Matches) UpsertRSVP(ctx context.Context, matchID, membershipID int64, answer domain.RSVPAnswer) (domain.RSVP, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).UpsertRSVP(ctx, sqlcdb.UpsertRSVPParams{
		MatchID:      matchID,
		MembershipID: membershipID,
		Answer:       string(answer),
	})
	if err != nil {
		return domain.RSVP{}, fmt.Errorf("postgres: upsert rsvp: %w", err)
	}
	return domain.RSVP{
		MatchID:      row.MatchID,
		MembershipID: row.MembershipID,
		Answer:       domain.RSVPAnswer(row.Answer),
		UpdatedAt:    row.UpdatedAt.Time.UTC(),
	}, nil
}

// UpdateMatch sets the fields that are not nil on the team's match and
// returns it, or domain.ErrNotFound when the team has no such match.
func (r *Matches) UpdateMatch(ctx context.Context, teamID, id int64, startsAt *time.Time, rival, location *string) (domain.Match, error) {
	arg := sqlcdb.UpdateMatchParams{ID: id, TeamID: teamID, RivalName: optText(rival), Location: optText(location)}
	if startsAt != nil {
		arg.StartsAt = timestamptz(*startsAt)
	}
	q := sqlcdb.New(Conn(ctx, r.pool))
	id, err := q.UpdateMatch(ctx, arg)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Match{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Match{}, fmt.Errorf("postgres: update match: %w", err)
	}
	row, err := q.GetMatch(ctx, id)
	return matchOrNotFound(row, err, "read updated match")
}

// CloseDueAttendance saves attendance from going RSVPs for every match that
// started an hour or more before now and is still open, and closes it, in
// one atomic statement (R10.1). It returns how many matches it closed.
func (r *Matches) CloseDueAttendance(ctx context.Context, now time.Time) (int64, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).CloseDueAttendance(ctx, timestamptz(now))
	if err != nil {
		return 0, fmt.Errorf("postgres: close due attendance: %w", err)
	}
	return n, nil
}

// LockMatchInTeam returns the team's match with that id, holding the row
// until the transaction ends, or domain.ErrNotFound.
func (r *Matches) LockMatchInTeam(ctx context.Context, teamID, id int64) (domain.Match, error) {
	q := sqlcdb.New(Conn(ctx, r.pool))
	if _, err := q.LockMatchInTeam(ctx, sqlcdb.LockMatchInTeamParams{TeamID: teamID, ID: id}); errors.Is(err, pgx.ErrNoRows) {
		return domain.Match{}, domain.ErrNotFound
	} else if err != nil {
		return domain.Match{}, fmt.Errorf("postgres: lock match in team: %w", err)
	}
	row, err := q.GetMatch(ctx, id)
	return matchOrNotFound(row, err, "read locked match")
}

// ChargeExistsForMatch reports whether any charge targets the match (R10.4).
func (r *Matches) ChargeExistsForMatch(ctx context.Context, matchID int64) (bool, error) {
	exists, err := sqlcdb.New(Conn(ctx, r.pool)).ChargeExistsForMatch(ctx, pgtype.Int8{Int64: matchID, Valid: true})
	if err != nil {
		return false, fmt.Errorf("postgres: charge exists for match: %w", err)
	}
	return exists, nil
}

// CountTeamMembersIn counts how many of ids are memberships of the team
// that joined it at some point.
func (r *Matches) CountTeamMembersIn(ctx context.Context, teamID int64, ids []int64) (int, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).CountTeamMembersIn(ctx, sqlcdb.CountTeamMembersInParams{TeamID: teamID, Ids: ids})
	if err != nil {
		return 0, fmt.Errorf("postgres: count team members in: %w", err)
	}
	return int(n), nil
}

// ReplaceAttendance stores ids as the match's whole attendee list and closes
// the attendance if it was open. Callers hold the transaction.
func (r *Matches) ReplaceAttendance(ctx context.Context, matchID int64, ids []int64, now time.Time) error {
	q := sqlcdb.New(Conn(ctx, r.pool))
	if err := q.DeleteAttendance(ctx, matchID); err != nil {
		return fmt.Errorf("postgres: delete attendance: %w", err)
	}
	if err := q.InsertAttendance(ctx, sqlcdb.InsertAttendanceParams{MatchID: matchID, Ids: ids}); err != nil {
		return fmt.Errorf("postgres: insert attendance: %w", err)
	}
	if err := q.CloseAttendance(ctx, sqlcdb.CloseAttendanceParams{ID: matchID, Now: timestamptz(now)}); err != nil {
		return fmt.Errorf("postgres: close attendance: %w", err)
	}
	return nil
}

// ListAttendance returns the match's attendee membership ids, ascending.
func (r *Matches) ListAttendance(ctx context.Context, matchID int64) ([]int64, error) {
	ids, err := sqlcdb.New(Conn(ctx, r.pool)).ListAttendance(ctx, matchID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list attendance: %w", err)
	}
	return ids, nil
}

func matchOrNotFound(row sqlcdb.MatchView, err error, op string) (domain.Match, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Match{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Match{}, fmt.Errorf("postgres: %s: %w", op, err)
	}
	return matchFromRow(row), nil
}

// matchFromRow keeps every timestamp in UTC (R9.1), whatever zone the driver
// decoded it in. Every match query reads match_view, so every row is this
// type.
func matchFromRow(row sqlcdb.MatchView) domain.Match {
	return domain.Match{
		ID:                 row.ID,
		TeamID:             row.TeamID,
		StartsAt:           row.StartsAt.Time.UTC(),
		RivalName:          row.RivalName,
		Location:           row.Location,
		ReminderSentAt:     utcPtr(row.ReminderSentAt),
		AttendanceClosedAt: utcPtr(row.AttendanceClosedAt),
		CreatedBy:          int8Ptr(row.CreatedBy),
		FixtureID:          int8Ptr(row.FixtureID),
		CourtID:            int8Ptr(row.CourtID),
		CancelledAt:        utcPtr(row.CancelledAt),
	}
}

func int8Ptr(v pgtype.Int8) *int64 {
	if !v.Valid {
		return nil
	}
	return &v.Int64
}

func utcPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	u := t.Time.UTC()
	return &u
}

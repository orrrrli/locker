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

// Charges is the repository for charges and their per-member entries (R12).
type Charges struct {
	pool *pgxpool.Pool
}

func NewCharges(pool *pgxpool.Pool) *Charges {
	return &Charges{pool: pool}
}

// CreateCharge inserts the charge without entries; AddActiveMembers or
// AddAttendees fills them. Callers hold the transaction.
func (r *Charges) CreateCharge(ctx context.Context, teamID int64, concept string, amountCents int64, currency string, matchID *int64, createdBy int64) (domain.Charge, error) {
	params := sqlcdb.CreateChargeParams{
		TeamID:      teamID,
		Concept:     concept,
		AmountCents: amountCents,
		Currency:    currency,
		CreatedBy:   createdBy,
	}
	if matchID != nil {
		params.MatchID = pgtype.Int8{Int64: *matchID, Valid: true}
	}
	row, err := sqlcdb.New(Conn(ctx, r.pool)).CreateCharge(ctx, params)
	if err != nil {
		return domain.Charge{}, fmt.Errorf("postgres: create charge: %w", err)
	}
	return chargeFromRow(row), nil
}

// AddActiveMembers inserts one pending entry per active membership of the
// team and returns how many it inserted.
func (r *Charges) AddActiveMembers(ctx context.Context, chargeID, teamID int64) (int, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).InsertChargeMembersForActive(ctx, sqlcdb.InsertChargeMembersForActiveParams{ChargeID: chargeID, TeamID: teamID})
	if err != nil {
		return 0, fmt.Errorf("postgres: insert charge members for active: %w", err)
	}
	return int(n), nil
}

// AddAttendees inserts one pending entry per attendee of the match and
// returns how many it inserted.
func (r *Charges) AddAttendees(ctx context.Context, chargeID, matchID int64) (int, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).InsertChargeMembersForAttendance(ctx, sqlcdb.InsertChargeMembersForAttendanceParams{ChargeID: chargeID, MatchID: matchID})
	if err != nil {
		return 0, fmt.Errorf("postgres: insert charge members for attendance: %w", err)
	}
	return int(n), nil
}

// ListMembers returns the charge's entries by membership id, ascending.
func (r *Charges) ListMembers(ctx context.Context, chargeID int64) ([]domain.ChargeMember, error) {
	rows, err := sqlcdb.New(Conn(ctx, r.pool)).ListChargeMembers(ctx, chargeID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list charge members: %w", err)
	}
	out := make([]domain.ChargeMember, len(rows))
	for i, row := range rows {
		out[i] = chargeMemberFromRow(row)
	}
	return out, nil
}

func chargeFromRow(row sqlcdb.Charge) domain.Charge {
	return domain.Charge{
		ID:          row.ID,
		TeamID:      row.TeamID,
		Concept:     row.Concept,
		AmountCents: row.AmountCents,
		Currency:    row.Currency,
		MatchID:     int64Ptr(row.MatchID),
		CreatedBy:   row.CreatedBy,
		CreatedAt:   row.CreatedAt.Time.UTC(),
	}
}

func chargeMemberFromRow(row sqlcdb.ChargeMember) domain.ChargeMember {
	return domain.ChargeMember{
		ChargeID:     row.ChargeID,
		MembershipID: row.MembershipID,
		Status:       domain.ChargeStatus(row.Status),
		PaidMarkedBy: int64Ptr(row.PaidMarkedBy),
		PaidMarkedAt: utcPtr(row.PaidMarkedAt),
	}
}

// ChargeByID returns the charge in any team, or domain.ErrNotFound. Routes
// on /charges/{id} use it to find the team.
func (r *Charges) ChargeByID(ctx context.Context, id int64) (domain.Charge, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).GetCharge(ctx, id)
	return chargeOrNotFound(row, err, "charge by id")
}

// ChargeInTeam returns the team's charge with that id, or domain.ErrNotFound
// when the id belongs to another team or to none.
func (r *Charges) ChargeInTeam(ctx context.Context, teamID, id int64) (domain.Charge, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).GetChargeInTeam(ctx, sqlcdb.GetChargeInTeamParams{TeamID: teamID, ID: id})
	return chargeOrNotFound(row, err, "charge in team")
}

// ListCharges returns the team's charges, newest first.
func (r *Charges) ListCharges(ctx context.Context, teamID int64) ([]domain.Charge, error) {
	rows, err := sqlcdb.New(Conn(ctx, r.pool)).ListChargesOfTeam(ctx, teamID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list charges: %w", err)
	}
	out := make([]domain.Charge, len(rows))
	for i, row := range rows {
		out[i] = chargeFromRow(row)
	}
	return out, nil
}

// ListMembersOfTeam returns every entry of the team's charges, or only
// membershipID's when it is not nil, by charge then membership id.
func (r *Charges) ListMembersOfTeam(ctx context.Context, teamID int64, membershipID *int64) ([]domain.ChargeMember, error) {
	params := sqlcdb.ListChargeMembersOfTeamParams{TeamID: teamID}
	if membershipID != nil {
		params.MembershipID = pgtype.Int8{Int64: *membershipID, Valid: true}
	}
	rows, err := sqlcdb.New(Conn(ctx, r.pool)).ListChargeMembersOfTeam(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("postgres: list charge members of team: %w", err)
	}
	out := make([]domain.ChargeMember, len(rows))
	for i, row := range rows {
		out[i] = chargeMemberFromRow(row)
	}
	return out, nil
}

// GetMember returns one entry, or domain.ErrNotFound when the membership has
// no entry in the charge.
func (r *Charges) GetMember(ctx context.Context, chargeID, membershipID int64) (domain.ChargeMember, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).GetChargeMember(ctx, sqlcdb.GetChargeMemberParams{ChargeID: chargeID, MembershipID: membershipID})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChargeMember{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.ChargeMember{}, fmt.Errorf("postgres: get charge member: %w", err)
	}
	return chargeMemberFromRow(row), nil
}

// SetMemberStatus marks the entry paid by markedBy at markedAt, or pending
// with both cleared, and returns it; domain.ErrNotFound when the membership
// has no entry in the charge.
func (r *Charges) SetMemberStatus(ctx context.Context, chargeID, membershipID int64, status domain.ChargeStatus, markedBy *int64, markedAt *time.Time) (domain.ChargeMember, error) {
	params := sqlcdb.SetChargeMemberStatusParams{ChargeID: chargeID, MembershipID: membershipID, Status: string(status)}
	if markedBy != nil {
		params.PaidMarkedBy = pgtype.Int8{Int64: *markedBy, Valid: true}
	}
	if markedAt != nil {
		params.PaidMarkedAt = timestamptz(*markedAt)
	}
	row, err := sqlcdb.New(Conn(ctx, r.pool)).SetChargeMemberStatus(ctx, params)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ChargeMember{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.ChargeMember{}, fmt.Errorf("postgres: set charge member status: %w", err)
	}
	return chargeMemberFromRow(row), nil
}

func chargeOrNotFound(row sqlcdb.Charge, err error, op string) (domain.Charge, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Charge{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Charge{}, fmt.Errorf("postgres: %s: %w", op, err)
	}
	return chargeFromRow(row), nil
}

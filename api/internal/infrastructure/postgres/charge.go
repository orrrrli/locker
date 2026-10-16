package postgres

import (
	"context"
	"fmt"

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

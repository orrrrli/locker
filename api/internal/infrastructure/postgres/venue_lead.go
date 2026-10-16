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

// VenueLeads is the repository for venue owners' contact requests.
type VenueLeads struct {
	pool *pgxpool.Pool
}

func NewVenueLeads(pool *pgxpool.Pool) *VenueLeads {
	return &VenueLeads{pool: pool}
}

// LockUser holds the user's row until the transaction ends, so the daily
// count stays exact. domain.ErrNotFound for an unknown or deleted user.
func (r *VenueLeads) LockUser(ctx context.Context, userID int64) error {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); !ok {
		return errors.New("postgres: lock user for lead: no transaction in ctx")
	}
	_, err := sqlcdb.New(Conn(ctx, r.pool)).LockUserForLead(ctx, userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("postgres: lock user for lead: %w", err)
	}
	return nil
}

// CountSince counts the user's leads created after since.
func (r *VenueLeads) CountSince(ctx context.Context, userID int64, since time.Time) (int, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).CountLeadsSince(ctx, sqlcdb.CountLeadsSinceParams{UserID: pgInt8(userID), Since: timestamptz(since)})
	if err != nil {
		return 0, fmt.Errorf("postgres: count leads: %w", err)
	}
	return int(n), nil
}

// Create stores l (its ID and HandledAt are ignored).
func (r *VenueLeads) Create(ctx context.Context, l domain.VenueLead) (domain.VenueLead, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).CreateVenueLead(ctx, sqlcdb.CreateVenueLeadParams{
		UserID: optInt8(l.UserID), ContactName: l.ContactName, VenueName: l.VenueName, City: l.City,
		Phone: optText(l.Phone), Email: optText(l.Email), Message: optText(l.Message), CreatedAt: timestamptz(l.CreatedAt),
	})
	if err != nil {
		return domain.VenueLead{}, fmt.Errorf("postgres: create venue lead: %w", err)
	}
	return leadFromRow(row), nil
}

// List returns up to limit leads, unhandled first, then newest first.
func (r *VenueLeads) List(ctx context.Context, limit int) ([]domain.VenueLead, error) {
	rows, err := sqlcdb.New(Conn(ctx, r.pool)).ListVenueLeads(ctx, int32(limit))
	if err != nil {
		return nil, fmt.Errorf("postgres: list venue leads: %w", err)
	}
	out := make([]domain.VenueLead, len(rows))
	for i, row := range rows {
		out[i] = leadFromRow(row)
	}
	return out, nil
}

// SetHandled marks the lead handled at now, keeping an earlier time, or
// clears it; domain.ErrNotFound for an unknown lead.
func (r *VenueLeads) SetHandled(ctx context.Context, id int64, handled bool, now time.Time) (domain.VenueLead, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).SetVenueLeadHandled(ctx, sqlcdb.SetVenueLeadHandledParams{ID: id, Handled: handled, Now: timestamptz(now)})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.VenueLead{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.VenueLead{}, fmt.Errorf("postgres: set venue lead handled: %w", err)
	}
	return leadFromRow(row), nil
}

func leadFromRow(row sqlcdb.VenueLead) domain.VenueLead {
	return domain.VenueLead{
		ID: row.ID, UserID: int8Ptr(row.UserID), ContactName: row.ContactName, VenueName: row.VenueName, City: row.City,
		Phone: textPtr(row.Phone), Email: textPtr(row.Email), Message: textPtr(row.Message),
		CreatedAt: row.CreatedAt.Time.UTC(), HandledAt: utcPtr(row.HandledAt),
	}
}

package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// Memberships is the membership repository.
type Memberships struct {
	pool *pgxpool.Pool
}

func NewMemberships(pool *pgxpool.Pool) *Memberships {
	return &Memberships{pool: pool}
}

// MembershipByTeamAndUser returns the user's membership in the team, whatever
// its status, or domain.ErrNotFound.
func (r *Memberships) MembershipByTeamAndUser(ctx context.Context, teamID, userID int64) (domain.Membership, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).GetMembershipByTeamAndUser(ctx, sqlcdb.GetMembershipByTeamAndUserParams{
		TeamID: teamID,
		UserID: pgtype.Int8{Int64: userID, Valid: true},
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Membership{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Membership{}, fmt.Errorf("postgres: membership by team and user: %w", err)
	}
	return membershipFromRow(row), nil
}

func (r *Memberships) CreateMembership(ctx context.Context, teamID, userID int64, role domain.Role, status domain.MembershipStatus) (int64, error) {
	id, err := sqlcdb.New(Conn(ctx, r.pool)).CreateMembership(ctx, sqlcdb.CreateMembershipParams{
		TeamID: teamID,
		UserID: pgtype.Int8{Int64: userID, Valid: true},
		Role:   string(role),
		Status: string(status),
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolationCode {
		return 0, domain.ErrAlreadyExists
	}
	if err != nil {
		return 0, fmt.Errorf("postgres: create membership: %w", err)
	}
	return id, nil
}

// RejoinMembership turns a `left` membership back into a pending player,
// keeping the row and its history (R8.4). It returns domain.ErrNotFound when
// the row is no longer `left`.
func (r *Memberships) RejoinMembership(ctx context.Context, id int64) error {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).RejoinMembership(ctx, id)
	if err != nil {
		return fmt.Errorf("postgres: rejoin membership: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func membershipFromRow(row sqlcdb.Membership) domain.Membership {
	m := domain.Membership{
		ID:                  row.ID,
		TeamID:              row.TeamID,
		Role:                domain.Role(row.Role),
		Status:              domain.MembershipStatus(row.Status),
		Position:            textPtr(row.Position),
		DisplayNameOverride: textPtr(row.DisplayNameOverride),
		PushMuted:           row.PushMuted,
		CreatedAt:           row.CreatedAt.Time,
	}
	if row.UserID.Valid {
		m.UserID = &row.UserID.Int64
	}
	if row.ShirtNumber.Valid {
		n := int(row.ShirtNumber.Int32)
		m.ShirtNumber = &n
	}
	return m
}

func textPtr(t pgtype.Text) *string {
	if !t.Valid {
		return nil
	}
	return &t.String
}

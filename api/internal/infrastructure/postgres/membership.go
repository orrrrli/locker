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

// MembershipByID returns the membership with that id, in any team and status,
// or domain.ErrNotFound.
func (r *Memberships) MembershipByID(ctx context.Context, id int64) (domain.Membership, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).GetMembership(ctx, id)
	return membershipOrNotFound(row, err, "membership by id")
}

// MembershipInTeam returns the team's membership with that id, or
// domain.ErrNotFound when the id belongs to another team or to none.
func (r *Memberships) MembershipInTeam(ctx context.Context, teamID, id int64) (domain.Membership, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).GetMembershipInTeam(ctx, sqlcdb.GetMembershipInTeamParams{TeamID: teamID, ID: id})
	return membershipOrNotFound(row, err, "membership in team")
}

// UpdateRole sets the membership's role and returns the updated row, or
// domain.ErrNotFound.
func (r *Memberships) UpdateRole(ctx context.Context, id int64, role domain.Role) (domain.Membership, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).UpdateMembershipRole(ctx, sqlcdb.UpdateMembershipRoleParams{ID: id, Role: string(role)})
	return membershipOrNotFound(row, err, "update membership role")
}

func membershipOrNotFound(row sqlcdb.Membership, err error, op string) (domain.Membership, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Membership{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Membership{}, fmt.Errorf("postgres: %s: %w", op, err)
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

// lockTimeout caps the wait for the team lock. An admin change holds it for
// milliseconds, so waiting this long means a transaction is stuck; failing
// with domain.ErrTeamBusy beats queueing every admin write of the team behind
// it. It is SET LOCAL, so it also caps any later lock wait in the same
// transaction; only the team lock maps to ErrTeamBusy, a later timeout is a
// plain error and the transaction rolls back. A var so tests can shorten it.
var lockTimeout = 5 * time.Second

const lockNotAvailableCode = "55P03"

// LockTeamForAdminChange locks the team row and returns its active admin
// count, which stays exact until the transaction ends: every change that can
// remove an admin calls this first, so they run one at a time per team (R6.4).
// It must run inside a transaction, or the lock would be released at once.
// It returns domain.ErrNotFound for an unknown team, and domain.ErrTeamBusy
// when the lock stays held past lockTimeout.
//
// Lock and count are two statements on purpose. In READ COMMITTED each
// statement takes a new snapshot, so the count runs after the lock is granted
// and sees the change of the transaction it waited for. A single statement
// would count from the snapshot it took before waiting.
func (r *Memberships) LockTeamForAdminChange(ctx context.Context, teamID int64) (int, error) {
	if _, ok := ctx.Value(txKey{}).(pgx.Tx); !ok {
		return 0, errors.New("postgres: lock team for admin change: no transaction in ctx")
	}
	q := sqlcdb.New(Conn(ctx, r.pool))
	// LOCAL: the timeout ends with this transaction. SET takes no parameters.
	if _, err := Conn(ctx, r.pool).Exec(ctx, fmt.Sprintf("SET LOCAL lock_timeout = '%dms'", lockTimeout.Milliseconds())); err != nil {
		return 0, fmt.Errorf("postgres: set lock timeout: %w", err)
	}
	var pgErr *pgconn.PgError
	if _, err := q.LockTeam(ctx, teamID); errors.Is(err, pgx.ErrNoRows) {
		return 0, domain.ErrNotFound
	} else if errors.As(err, &pgErr) && pgErr.Code == lockNotAvailableCode {
		return 0, domain.ErrTeamBusy
	} else if err != nil {
		return 0, fmt.Errorf("postgres: lock team: %w", err)
	}
	n, err := q.CountActiveAdmins(ctx, teamID)
	if err != nil {
		return 0, fmt.Errorf("postgres: count active admins: %w", err)
	}
	return int(n), nil
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

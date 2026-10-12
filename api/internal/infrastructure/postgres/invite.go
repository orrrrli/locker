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

// Invites is the invite repository. It only ever sees token hashes.
type Invites struct {
	pool *pgxpool.Pool
}

func NewInvites(pool *pgxpool.Pool) *Invites {
	return &Invites{pool: pool}
}

func (r *Invites) CreateInvite(ctx context.Context, teamID int64, tokenHash []byte, createdBy int64, expiresAt time.Time) (int64, error) {
	id, err := sqlcdb.New(Conn(ctx, r.pool)).CreateInvite(ctx, sqlcdb.CreateInviteParams{
		TeamID:    teamID,
		TokenHash: tokenHash,
		CreatedBy: createdBy,
		ExpiresAt: timestamptz(expiresAt),
	})
	if err != nil {
		return 0, fmt.Errorf("postgres: create invite: %w", err)
	}
	return id, nil
}

// InviteByTokenHash returns the invite and its team's name, or
// domain.ErrNotFound.
func (r *Invites) InviteByTokenHash(ctx context.Context, tokenHash []byte) (domain.Invite, string, error) {
	row, err := sqlcdb.New(Conn(ctx, r.pool)).GetInviteByTokenHash(ctx, tokenHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Invite{}, "", domain.ErrNotFound
	}
	if err != nil {
		return domain.Invite{}, "", fmt.Errorf("postgres: invite by token hash: %w", err)
	}
	inv := domain.Invite{ID: row.ID, TeamID: row.TeamID, ExpiresAt: row.ExpiresAt.Time}
	if row.RevokedAt.Valid {
		inv.RevokedAt = &row.RevokedAt.Time
	}
	return inv, row.TeamName, nil
}

// ListInvites returns the team's invites not expired at now, newest first.
// TokenHash stays empty: the query never reads it.
func (r *Invites) ListInvites(ctx context.Context, teamID int64, now time.Time) ([]domain.Invite, error) {
	rows, err := sqlcdb.New(Conn(ctx, r.pool)).ListUnexpiredInvites(ctx, sqlcdb.ListUnexpiredInvitesParams{TeamID: teamID, Now: timestamptz(now)})
	if err != nil {
		return nil, fmt.Errorf("postgres: list invites: %w", err)
	}
	out := make([]domain.Invite, len(rows))
	for i, row := range rows {
		out[i] = domain.Invite{ID: row.ID, TeamID: row.TeamID, CreatedBy: row.CreatedBy, ExpiresAt: row.ExpiresAt.Time}
		if row.RevokedAt.Valid {
			out[i].RevokedAt = &row.RevokedAt.Time
		}
	}
	return out, nil
}

// RevokeInvite marks the team's invite revoked; a second call keeps the
// first time. It returns domain.ErrNotFound when the team has no such invite.
func (r *Invites) RevokeInvite(ctx context.Context, teamID, id int64, now time.Time) error {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).RevokeInvite(ctx, sqlcdb.RevokeInviteParams{ID: id, TeamID: teamID, Now: timestamptz(now)})
	if err != nil {
		return fmt.Errorf("postgres: revoke invite: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// CountLiveInvites counts the team's invites that are neither revoked nor
// expired at now.
func (r *Invites) CountLiveInvites(ctx context.Context, teamID int64, now time.Time) (int, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).CountLiveInvites(ctx, sqlcdb.CountLiveInvitesParams{TeamID: teamID, Now: timestamptz(now)})
	if err != nil {
		return 0, fmt.Errorf("postgres: count live invites: %w", err)
	}
	return int(n), nil
}

// DeleteExpired deletes invites that expired at or before cutoff and reports
// how many it deleted.
// ponytail: seq scan on invite; add an index on expires_at if the table grows large.
func (r *Invites) DeleteExpired(ctx context.Context, cutoff time.Time) (int64, error) {
	n, err := sqlcdb.New(Conn(ctx, r.pool)).DeleteExpiredInvites(ctx, timestamptz(cutoff))
	if err != nil {
		return 0, fmt.Errorf("postgres: delete expired invites: %w", err)
	}
	return n, nil
}

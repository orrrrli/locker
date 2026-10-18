package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/orrrrli/locker/api/internal/application/follows"
	"github.com/orrrrli/locker/api/internal/domain"
	"github.com/orrrrli/locker/api/internal/infrastructure/postgres/sqlcdb"
)

// Follows is the repository for follows of teams and leagues.
type Follows struct {
	pool *pgxpool.Pool
}

func NewFollows(pool *pgxpool.Pool) *Follows {
	return &Follows{pool: pool}
}

// Follow stores the follow, or keeps the existing one; domain.ErrNotFound for
// an unknown target or a dissolved team.
func (r *Follows) Follow(ctx context.Context, userID int64, kind follows.Kind, id int64) error {
	q := sqlcdb.New(Conn(ctx, r.pool))
	var err error
	switch kind {
	case follows.KindTeam:
		_, err = q.FollowTeam(ctx, sqlcdb.FollowTeamParams{UserID: userID, TeamID: id})
	case follows.KindLeague:
		_, err = q.FollowLeague(ctx, sqlcdb.FollowLeagueParams{UserID: userID, LeagueID: id})
	default:
		return domain.ErrNotFound
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("postgres: follow %s: %w", kind, err)
	}
	return nil
}

// Unfollow removes the follow; no error when there was none.
func (r *Follows) Unfollow(ctx context.Context, userID int64, kind follows.Kind, id int64) error {
	q := sqlcdb.New(Conn(ctx, r.pool))
	var err error
	switch kind {
	case follows.KindTeam:
		err = q.UnfollowTeam(ctx, sqlcdb.UnfollowTeamParams{UserID: userID, TeamID: pgInt8(id)})
	case follows.KindLeague:
		err = q.UnfollowLeague(ctx, sqlcdb.UnfollowLeagueParams{UserID: userID, LeagueID: pgInt8(id)})
	default:
		return domain.ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("postgres: unfollow %s: %w", kind, err)
	}
	return nil
}

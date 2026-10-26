package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Social is the social-data step of account deletion (R18.13, R20.6).
type Social struct {
	pool *pgxpool.Pool
}

func NewSocial(pool *pgxpool.Pool) *Social {
	return &Social{pool: pool}
}

// DeleteSocialDataOfUser removes the user's social data.
//
// ponytail: stub. Phase 3 replaces the body: release the user's player
// posts' uploads, delete those posts, then delete reactions, follows, blocks
// (both directions) and reports.
func (r *Social) DeleteSocialDataOfUser(ctx context.Context, userID int64) error {
	return errors.New("postgres: delete social data of user: not implemented")
}

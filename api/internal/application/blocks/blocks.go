// Package blocks holds one-way user blocks (R23): the blocker stops seeing
// the blocked user's posts. The blocked user is not told, and no membership
// changes (R23.6): blocking touches only the user_block table.
package blocks

import (
	"context"
	"errors"
)

var ErrCannotBlockSelf = errors.New("you cannot block yourself")

// Blocks is what the use cases need from the user_block table.
type Blocks interface {
	// Block stores the block, or keeps the existing one; domain.ErrNotFound
	// for an unknown or deleted blocked user.
	Block(ctx context.Context, blockerID, blockedID int64) error
	// Unblock removes the block; no error when there was none.
	Unblock(ctx context.Context, blockerID, blockedID int64) error
}

type Service struct {
	blocks Blocks
}

func NewService(b Blocks) *Service {
	return &Service{blocks: b}
}

// Block makes userID stop seeing blockedID's posts (R23.5). A user cannot
// block themself (R23.8).
func (s *Service) Block(ctx context.Context, userID, blockedID int64) error {
	if userID == blockedID {
		return ErrCannotBlockSelf
	}
	return s.blocks.Block(ctx, userID, blockedID)
}

// Unblock removes userID's block on blockedID, if any (R23.7).
func (s *Service) Unblock(ctx context.Context, userID, blockedID int64) error {
	return s.blocks.Unblock(ctx, userID, blockedID)
}

// Package reactions lets a signed-in user set or clear fire, ball and clap on
// a post (R19).
package reactions

import (
	"context"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

// Reactions is what the use case needs from the reaction table.
type Reactions interface {
	// LockVisiblePost locks the post row; domain.ErrNotFound when it is
	// missing or removed.
	LockVisiblePost(ctx context.Context, postID int64) error
	// Remove deletes the reaction and reports whether there was one.
	Remove(ctx context.Context, postID, userID int64, kind domain.ReactionKind) (removed bool, err error)
	Add(ctx context.Context, postID, userID int64, kind domain.ReactionKind) error
}

type Service struct {
	tx        application.TxRunner
	reactions Reactions
}

func NewService(tx application.TxRunner, r Reactions) *Service {
	return &Service{tx: tx, reactions: r}
}

// Toggle clears the user's reaction of this kind on the post, or sets it when
// there was none, and reports whether it is set now (R19.3). Any signed-in
// user reacts, the author included (R19.5). An unknown kind, or a missing or
// removed post, is domain.ErrNotFound (R19.1, R19.6).
func (s *Service) Toggle(ctx context.Context, userID, postID int64, kind domain.ReactionKind) (reacted bool, err error) {
	switch kind {
	case domain.ReactionFire, domain.ReactionBall, domain.ReactionClap:
	default:
		return false, domain.ErrNotFound
	}
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.reactions.LockVisiblePost(ctx, postID); err != nil {
			return err
		}
		removed, err := s.reactions.Remove(ctx, postID, userID, kind)
		if err != nil || removed {
			return err
		}
		reacted = true
		return s.reactions.Add(ctx, postID, userID, kind)
	})
	if err != nil {
		return false, err
	}
	return reacted, nil
}

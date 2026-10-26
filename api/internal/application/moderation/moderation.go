// Package moderation is the operator's moderation console (R23.4, R23.9-R23.12):
// the queue of open reports, removing a post with a reason, and suspending or
// reinstating a user. Every route that reaches it sits behind requireOperator.
package moderation

import (
	"context"
	"fmt"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

// MaxReasonLen is the longest removal reason the operator may write.
// ponytail: same limit as a report note (reports.MaxNoteLen); change both
// together when the spec sets one.
const MaxReasonLen = 500

// ErrInvalidReason rejects a removal whose reason is missing, blank, longer
// than MaxReasonLen or carries invisible characters (R23.9).
var ErrInvalidReason = fmt.Errorf("the reason must be at most %d characters, with no invisible characters", MaxReasonLen)

// Reports is what the queue and the removal need from the report table.
type Reports interface {
	// ListOpen returns every open report, oldest first, with the post it is
	// about and how many open reports that post has (R23.4).
	ListOpen(ctx context.Context) ([]domain.QueuedReport, error)
	// ResolveOpenOfPost marks the post's open reports resolved at now and
	// returns how many it resolved (R23.9).
	ResolveOpenOfPost(ctx context.Context, postID int64, now time.Time) (int, error)
}

// Posts is what the removal needs from the post table.
type Posts interface {
	// Remove hides the post, recording by, reason and now, and returns the
	// record. A repeat keeps the first removal. domain.ErrNotFound for an
	// unknown post.
	Remove(ctx context.Context, postID, by int64, reason string, now time.Time) (domain.PostRemoval, error)
}

// Users is what the suspension needs from the user table.
type Users interface {
	// Suspend sets suspended_at and returns it, keeping the first time;
	// domain.ErrNotFound for an unknown or deleted user.
	Suspend(ctx context.Context, userID int64, now time.Time) (time.Time, error)
	// Unsuspend clears suspended_at; domain.ErrNotFound for an unknown or
	// deleted user.
	Unsuspend(ctx context.Context, userID int64) error
}

type Deps struct {
	Tx      application.TxRunner
	Reports Reports
	Posts   Posts
	Users   Users
	Now     func() time.Time // nil means time.Now
}

type Service struct {
	tx      application.TxRunner
	reports Reports
	posts   Posts
	users   Users
	now     func() time.Time
}

func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{tx: d.Tx, reports: d.Reports, posts: d.Posts, users: d.Users, now: d.Now}
}

// ListReports returns the operator's open-report queue, oldest first (R23.4).
// It reads no lock: the queue is a snapshot, and acting on a report re-reads
// the post inside the removal's transaction.
func (s *Service) ListReports(ctx context.Context) ([]domain.QueuedReport, error) {
	return s.reports.ListOpen(ctx)
}

// RemovePost hides postID on the operator's behalf and resolves its open
// reports in the same transaction, so the queue drops them together (R23.9).
// The post row and its photos stay; the removal is recorded with its author,
// the reason and the time. domain.ErrNotFound for an unknown post.
func (s *Service) RemovePost(ctx context.Context, operatorID, postID int64, reason string) (domain.PostRemoval, error) {
	clean, ok := application.CleanParagraph(reason, MaxReasonLen)
	if !ok {
		return domain.PostRemoval{}, ErrInvalidReason
	}
	now := s.now().UTC()
	var out domain.PostRemoval
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.posts.Remove(ctx, postID, operatorID, clean, now)
		if err != nil {
			return err
		}
		_, err = s.reports.ResolveOpenOfPost(ctx, postID, now)
		return err
	})
	if err != nil {
		return domain.PostRemoval{}, err
	}
	return out, nil
}

// Suspend records the suspension and returns its time; a repeat keeps the first
// one (R23.10). Posting is refused from then on and the user's player posts are
// hidden, while team features keep working.
func (s *Service) Suspend(ctx context.Context, userID int64) (time.Time, error) {
	return s.users.Suspend(ctx, userID, s.now().UTC())
}

// Unsuspend lifts the suspension, so the user's player posts show again except
// the ones the operator removed (R23.11). Lifting one that is not there is not
// an error.
func (s *Service) Unsuspend(ctx context.Context, userID int64) error {
	return s.users.Unsuspend(ctx, userID)
}

// Package reports lets a signed-in user report a post (R23.1-R23.3): one
// report per user and post, never on one's own post, at most 10 a day.
package reports

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

const (
	// MaxNoteLen is the longest note a report may carry. R23.1 makes the note
	// optional free text and NFR 9 sets no length: 500 is what a post body
	// gets. ponytail: borrowed limit, change it when the spec sets one.
	MaxNoteLen = 500
	// MaxPerDay is how many reports one user may send in 24 hours (NFR 9).
	MaxPerDay = 10
)

var (
	// ErrInvalidReason rejects a reason outside the fixed list (R23.1).
	ErrInvalidReason = errors.New("reason must be spam, inappropriate, harassment or other")
	// ErrInvalidNote rejects a note over MaxNoteLen characters, or one with
	// invisible characters.
	ErrInvalidNote = fmt.Errorf("the note must be at most %d characters, with no invisible characters", MaxNoteLen)
	// ErrOwnPost rejects reporting one's own post (R23.3).
	ErrOwnPost = errors.New("you cannot report your own post")
	// ErrTooManyReports rejects a new report once the day's quota is spent
	// (NFR 9).
	ErrTooManyReports = fmt.Errorf("you already sent %d reports in the last 24 hours", MaxPerDay)
)

// Reports is what the use case needs from the report table.
type Reports interface {
	// LockUser holds the user's row until commit, so the daily count stays
	// exact when two reports arrive at once (NFR 9); domain.ErrNotFound for an
	// unknown or deleted user.
	LockUser(ctx context.Context, userID int64) error
	// CountSince counts the reports the user sent after since.
	CountSince(ctx context.Context, userID int64, since time.Time) (int, error)
	// ByPostAndReporter returns the user's report of the post, or
	// domain.ErrNotFound when there is none.
	ByPostAndReporter(ctx context.Context, postID, reporterID int64) (domain.Report, error)
	// Create stores reporterID's report r; domain.ErrAlreadyExists when
	// another request stored the same user and post first.
	Create(ctx context.Context, reporterID int64, r domain.Report) (domain.Report, error)
}

// Posts is what the author check needs from the post table.
type Posts interface {
	// PostAuthor returns the post without locking it; domain.ErrNotFound for an
	// unknown post. RemovedAt is set when the operator removed it.
	PostAuthor(ctx context.Context, id int64) (domain.Post, error)
}

type Deps struct {
	Tx      application.TxRunner
	Reports Reports
	Posts   Posts
	Now     func() time.Time // nil means time.Now
}

type Service struct {
	tx      application.TxRunner
	reports Reports
	posts   Posts
	now     func() time.Time
}

func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{tx: d.Tx, reports: d.Reports, posts: d.Posts, now: d.Now}
}

// Report stores userID's report of postID and says whether it is new (R23.1).
// A repeat of the same post is not new and answers with the stored report,
// keeping its first reason and note (R23.2); it is resolved before the daily
// limit, so asking twice never costs quota.
//
// Everything runs in one transaction. The reason and the note need no lookup.
// Inside it: the post must exist and not be removed, the post must not be the
// caller's own (R23.3), then, under the user's row lock, the report already
// there, the daily limit (NFR 9) and the insert. The lock makes the count
// exact under two parallel reports; the unique key settles a race that slips
// past it.
func (s *Service) Report(ctx context.Context, userID, postID int64, reason domain.ReportReason, note *string) (domain.Report, bool, error) {
	if !reason.Valid() {
		return domain.Report{}, false, ErrInvalidReason
	}
	clean, err := cleanNote(note)
	if err != nil {
		return domain.Report{}, false, err
	}
	now := s.now().UTC()
	var out domain.Report
	var isNew bool
	err = s.tx.InTx(ctx, func(ctx context.Context) error {
		p, err := s.posts.PostAuthor(ctx, postID)
		if err != nil {
			return err
		}
		if p.RemovedAt != nil {
			return domain.ErrNotFound
		}
		if p.AuthorUserID == userID {
			return ErrOwnPost
		}
		if err := s.reports.LockUser(ctx, userID); err != nil {
			return err
		}
		out, err = s.reports.ByPostAndReporter(ctx, postID, userID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		n, err := s.reports.CountSince(ctx, userID, now.Add(-24*time.Hour))
		if err != nil {
			return err
		}
		if n >= MaxPerDay {
			return ErrTooManyReports
		}
		out, err = s.reports.Create(ctx, userID, domain.Report{PostID: postID, Reason: reason, Note: clean, CreatedAt: now})
		if errors.Is(err, domain.ErrAlreadyExists) {
			// Two of the user's reports raced past the read: the other won.
			out, err = s.reports.ByPostAndReporter(ctx, postID, userID)
			return err
		}
		if err != nil {
			return err
		}
		isNew = true
		return nil
	})
	if err != nil {
		return domain.Report{}, false, err
	}
	return out, isNew, nil
}

// cleanNote trims the optional note, so an empty one is stored as null, and
// checks its length and characters.
func cleanNote(note *string) (*string, error) {
	if note == nil || strings.TrimSpace(*note) == "" {
		return nil, nil
	}
	s, ok := application.CleanParagraph(*note, MaxNoteLen)
	if !ok {
		return nil, ErrInvalidNote
	}
	return &s, nil
}

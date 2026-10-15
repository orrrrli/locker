// Package notifications holds the notice use cases (R11). The inbox is the
// source of truth; push is only delivery.
package notifications

import (
	"context"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

// Notifications is what the notice use cases need from storage.
type Notifications interface {
	// InsertNotification stores the notice plus one recipient per active
	// membership of its team; the sender's copy starts read.
	InsertNotification(ctx context.Context, n domain.Notification) (domain.Notification, error)
	// PushTokens returns the device tokens of the recipients who have not
	// muted the team, leaving out the sender.
	PushTokens(ctx context.Context, notificationID int64) ([]string, error)
	// Inbox returns up to limit of the user's notices from teams where they
	// are active, newest first, with ids below before when it is set.
	Inbox(ctx context.Context, userID int64, before *int64, limit int) ([]domain.InboxItem, error)
	// MarkRead returns domain.ErrNotFound when the notice is not in the
	// user's inbox.
	MarkRead(ctx context.Context, userID, notificationID int64) error
	// RegisterDevice stores the push token for the user and the session that
	// registered it, replacing that session's previous token; a token already
	// stored moves to them. A session already ended is domain.ErrNotFound.
	RegisterDevice(ctx context.Context, userID, sessionID int64, token string) error
	// UnregisterDevice deletes the user's own token, if they have it.
	UnregisterDevice(ctx context.Context, userID int64, token string) error
	// TrimDevices deletes the user's tokens beyond the keep most recently seen.
	TrimDevices(ctx context.Context, userID int64, keep int) error
	// NotificationByID returns domain.ErrNotFound for an unknown notice.
	NotificationByID(ctx context.Context, id int64) (domain.Notification, error)
	// Receipts returns every recipient of the notice with their read time.
	Receipts(ctx context.Context, notificationID int64) ([]domain.Receipt, error)
}

// MaxDevicesPerUser caps the device tokens one account keeps. A person has a
// few devices; without a cap one account could register tokens without end
// and make every notice to its team push to all of them.
const MaxDevicesPerUser = 10

// PageSize is how many notices one inbox page holds.
const PageSize = 50

// Pusher delivers a stored notice to devices. It returns nothing: the notice
// is already in the inbox, so a failed push is the pusher's to log, never a
// failed send. It also removes tokens APNs rejects (R11.7).
type Pusher interface {
	Push(ctx context.Context, n domain.Notification, tokens []string)
}

type Deps struct {
	Tx            application.TxRunner
	Notifications Notifications
	Push          Pusher // nil means inbox only, until the APNs sender (BE-NOTIF-2)
}

type Service struct {
	tx            application.TxRunner
	notifications Notifications
	push          Pusher
}

func NewService(d Deps) *Service {
	return &Service{tx: d.Tx, notifications: d.Notifications, push: d.Push}
}

// Send stores the notice for every active member of n.TeamID in one
// transaction (R11.1, R11.2), then, after commit, pushes it to the members
// who have not muted the team (R11.6). The sender keeps a copy in their
// inbox, already read, and gets no push of their own notice. Callers validate
// the text and check who may send. A manual notice needs SentBy; an automatic
// one must not have it.
//
// Never call Send inside another InTx: it would join that transaction and
// push before the caller commits, so a rollback leaves a push with no notice.
func (s *Service) Send(ctx context.Context, n domain.Notification) (domain.Notification, error) {
	var out domain.Notification
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		var err error
		out, err = s.notifications.InsertNotification(ctx, n)
		return err
	})
	if err != nil {
		return domain.Notification{}, err
	}
	if s.push == nil {
		return out, nil
	}
	tokens, err := s.notifications.PushTokens(ctx, out.ID)
	if err != nil {
		// The notice is stored; only delivery is lost, so the send succeeds.
		// ponytail: the error is dropped unlogged; the application layer has
		// no logger. Log it here once BE-NOTIF-2 brings one for push failures.
		return out, nil
	}
	if len(tokens) > 0 {
		s.push.Push(ctx, out, tokens)
	}
	return out, nil
}

// Inbox returns one page of the user's notices from every team where they are
// active (R24.6), newest first. before is the next value from the previous
// page, nil for the first. next is nil on the last page.
func (s *Service) Inbox(ctx context.Context, userID int64, before *int64) (items []domain.InboxItem, next *int64, err error) {
	// One extra row tells whether another page exists, without a count.
	items, err = s.notifications.Inbox(ctx, userID, before, PageSize+1)
	if err != nil {
		return nil, nil, err
	}
	if len(items) > PageSize {
		items = items[:PageSize]
		next = &items[PageSize-1].ID
	}
	return items, next, nil
}

// Receipts is "opened by X of Y" plus who has not opened yet (R11.10). On a
// manual notice the sender is left out of both: their copy is marked read
// at send time, so it would inflate the count.
type Receipts struct {
	NotificationID int64
	OpenedCount    int
	RecipientCount int
	NotOpened      []domain.Receipt
}

// Receipts returns the read receipts of a notice of the team, manual or
// automatic (R11.9, R11.10). A notice of another team is domain.ErrNotFound,
// so ids reveal nothing. Callers check the caller is an admin of teamID.
func (s *Service) Receipts(ctx context.Context, teamID, notificationID int64) (Receipts, error) {
	n, err := s.notifications.NotificationByID(ctx, notificationID)
	if err != nil {
		return Receipts{}, err
	}
	if n.TeamID != teamID {
		return Receipts{}, domain.ErrNotFound
	}
	all, err := s.notifications.Receipts(ctx, n.ID)
	if err != nil {
		return Receipts{}, err
	}
	out := Receipts{NotificationID: n.ID, NotOpened: []domain.Receipt{}}
	for _, r := range all {
		if n.SentBy != nil && r.MembershipID == *n.SentBy {
			continue
		}
		out.RecipientCount++
		if r.ReadAt != nil {
			out.OpenedCount++
		} else {
			out.NotOpened = append(out.NotOpened, r)
		}
	}
	return out, nil
}

// MarkRead records that the user opened the notice, from the inbox or the
// push (R11.8, R11.9). Only the first open counts. A notice outside the
// user's inbox is domain.ErrNotFound.
func (s *Service) MarkRead(ctx context.Context, userID, notificationID int64) error {
	return s.notifications.MarkRead(ctx, userID, notificationID)
}

// RegisterDevice records the device the user is signed in on, one row per
// device (R11.7), tied to sessionID: ending that session stops pushes to the
// device. A device that signs in as someone else moves to them, so the
// previous user stops getting pushes there. In the same transaction it keeps
// only the user's MaxDevicesPerUser most recently seen devices; parallel
// registrations can leave a few more until the next one trims them. A
// session that ended meanwhile is domain.ErrNotFound. The handler validates
// token.
//
// ponytail: the cap is soft under concurrency; lock the user row first if an
// exact cap ever matters.
func (s *Service) RegisterDevice(ctx context.Context, userID, sessionID int64, token string) error {
	return s.tx.InTx(ctx, func(ctx context.Context) error {
		if err := s.notifications.RegisterDevice(ctx, userID, sessionID, token); err != nil {
			return err
		}
		return s.notifications.TrimDevices(ctx, userID, MaxDevicesPerUser)
	})
}

// UnregisterDevice stops pushes to one of the user's devices without ending
// its session. Only the user's own token goes; an unknown token or another
// user's is a no-op, so a retry is safe and nothing is revealed.
func (s *Service) UnregisterDevice(ctx context.Context, userID int64, token string) error {
	return s.notifications.UnregisterDevice(ctx, userID, token)
}

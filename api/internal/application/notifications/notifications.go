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
	// membership of its team.
	InsertNotification(ctx context.Context, n domain.Notification) (domain.Notification, error)
	// PushTokens returns the device tokens of the recipients who have not
	// muted the team.
	PushTokens(ctx context.Context, notificationID int64) ([]string, error)
}

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
// who have not muted the team (R11.6). Callers validate the text and check
// who may send. A manual notice needs SentBy; an automatic one must not have it.
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

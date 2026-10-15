// Package notifications holds the notice use cases (R11). The inbox is the
// source of truth; push is only delivery.
package notifications

import (
	"context"
	"errors"
	"time"

	"github.com/orrrrli/locker/api/internal/application"
	"github.com/orrrrli/locker/api/internal/domain"
)

const (
	// MaxManualPerDay caps the manual notices a team sends between two local
	// midnights in its timezone (R11.3), so an admin cannot flood the team.
	MaxManualPerDay = 10
	maxTitleLen     = 80
	maxBodyLen      = 1000
)

var (
	ErrInvalidTitle = errors.New("title must be 1 to 80 characters, with no invisible characters")
	ErrInvalidBody  = errors.New("body must be 1 to 1000 characters, with no invisible characters")
	// ErrDailyLimit rejects a manual notice once the team sent
	// MaxManualPerDay since local midnight (R11.3).
	ErrDailyLimit = errors.New("the team already sent its 10 notices for today")
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
	// CountManualSince counts the team's manual notices created at or after
	// since.
	CountManualSince(ctx context.Context, teamID int64, since time.Time) (int, error)
}

// Teams is what the notice use cases need from the team table.
type Teams interface {
	// GetTeam returns domain.ErrNotFound for an unknown team.
	GetTeam(ctx context.Context, id int64) (domain.Team, error)
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
	// Memberships, Teams and Now serve SendManual: the admin check under the
	// team lock, the team's timezone, and the clock (nil means time.Now).
	Memberships application.AdminLocker
	Teams       Teams
	Now         func() time.Time
}

type Service struct {
	tx            application.TxRunner
	notifications Notifications
	push          Pusher
	memberships   application.AdminLocker
	teams         Teams
	now           func() time.Time
}

func NewService(d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	return &Service{tx: d.Tx, notifications: d.Notifications, push: d.Push, memberships: d.Memberships, teams: d.Teams, now: d.Now}
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
	s.pushStored(ctx, out)
	return out, nil
}

// SendManual stores a manual notice from the admin callerID to every active
// member of the team (R11.2) and pushes it after commit, like Send. The
// team sends at most MaxManualPerDay since local midnight in its timezone
// (R11.3); the count runs under the team lock, so parallel sends cannot
// pass it together. The title is one line; the body may span lines.
func (s *Service) SendManual(ctx context.Context, teamID, callerID int64, title, body string) (domain.Notification, error) {
	title, ok := application.CleanText(title, maxTitleLen)
	if !ok {
		return domain.Notification{}, ErrInvalidTitle
	}
	body, ok = application.CleanParagraph(body, maxBodyLen)
	if !ok {
		return domain.Notification{}, ErrInvalidBody
	}
	var out domain.Notification
	err := s.tx.InTx(ctx, func(ctx context.Context) error {
		if _, err := application.LockTeamAsAdmin(ctx, s.memberships, teamID, callerID); err != nil {
			return err
		}
		team, err := s.teams.GetTeam(ctx, teamID)
		if err != nil {
			return err
		}
		loc, err := time.LoadLocation(team.Timezone)
		if err != nil {
			return err
		}
		now := s.now()
		local := now.In(loc)
		midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
		sent, err := s.notifications.CountManualSince(ctx, teamID, midnight)
		if err != nil {
			return err
		}
		if sent >= MaxManualPerDay {
			return ErrDailyLimit
		}
		out, err = s.notifications.InsertNotification(ctx, domain.Notification{
			TeamID: teamID, Kind: domain.NotificationManual, Title: title, Body: body, SentBy: &callerID, CreatedAt: now.UTC(),
		})
		return err
	})
	if err != nil {
		return domain.Notification{}, err
	}
	s.pushStored(ctx, out)
	return out, nil
}

// MatchCreated stores a match_created notice about m for every active member
// of its team and pushes it (R11.4). The kickoff is written in the team's
// timezone. Call it after the match committed, never inside its transaction.
func (s *Service) MatchCreated(ctx context.Context, m domain.Match) error {
	return s.sendMatchNotice(ctx, domain.NotificationMatchCreated, m)
}

// MatchChanged is MatchCreated for a match whose time or place changed.
func (s *Service) MatchChanged(ctx context.Context, m domain.Match) error {
	return s.sendMatchNotice(ctx, domain.NotificationMatchChanged, m)
}

func (s *Service) sendMatchNotice(ctx context.Context, kind domain.NotificationKind, m domain.Match) error {
	team, err := s.teams.GetTeam(ctx, m.TeamID)
	if err != nil {
		return err
	}
	loc, err := time.LoadLocation(team.Timezone)
	if err != nil {
		return err
	}
	when := m.StartsAt.In(loc).Format("02/01/2006 15:04")
	var title, body string
	switch kind {
	case domain.NotificationMatchCreated:
		title = "Nuevo partido vs " + m.RivalName
		body = when + " en " + m.Location
	default:
		title = "Cambio en el partido vs " + m.RivalName
		body = "Ahora " + when + " en " + m.Location
	}
	_, err = s.Send(ctx, domain.Notification{TeamID: m.TeamID, Kind: kind, Title: title, Body: body, MatchID: &m.ID})
	return err
}

// pushStored delivers a committed notice to the recipients who have not
// muted the team (R11.6). It runs after the transaction, so a rollback never
// leaves a push with no notice.
func (s *Service) pushStored(ctx context.Context, out domain.Notification) {
	if s.push == nil {
		return
	}
	tokens, err := s.notifications.PushTokens(ctx, out.ID)
	if err != nil {
		// The notice is stored; only delivery is lost, so the send succeeds.
		// ponytail: the error is dropped unlogged; the application layer has
		// no logger. Log it here once BE-NOTIF-2 brings one for push failures.
		return
	}
	if len(tokens) > 0 {
		s.push.Push(ctx, out, tokens)
	}
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

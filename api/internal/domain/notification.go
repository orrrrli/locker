package domain

import "time"

type NotificationKind string

const (
	NotificationManual       NotificationKind = "manual"
	NotificationMatchCreated NotificationKind = "match_created"
	NotificationMatchChanged NotificationKind = "match_changed"
	NotificationRSVPReminder NotificationKind = "rsvp_reminder"
)

// Notification is an inbox entry, the source of truth; push is only
// delivery (R11.1). SentBy is nil for automatic notices.
type Notification struct {
	ID        int64
	TeamID    int64
	Kind      NotificationKind
	Title     string
	Body      string
	MatchID   *int64
	SentBy    *int64 // membership
	CreatedAt time.Time
}

// NotificationRecipient tracks one member's copy; ReadAt means "opened the
// notice" (R11.8, R11.11).
type NotificationRecipient struct {
	NotificationID int64
	MembershipID   int64
	ReadAt         *time.Time
}

// InboxItem is a notice as one member sees it in the inbox (R24.6).
type InboxItem struct {
	Notification
	TeamName string
	ReadAt   *time.Time // nil until the member opens it
}

// Receipt is one recipient's read state as the admin sees it (R11.10).
type Receipt struct {
	MembershipID int64
	Name         string
	ReadAt       *time.Time
}

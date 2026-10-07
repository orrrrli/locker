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

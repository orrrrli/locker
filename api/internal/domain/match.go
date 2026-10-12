package domain

import "time"

// Match is the only event type in Phase 1 (R9.2).
type Match struct {
	ID                 int64
	TeamID             int64
	StartsAt           time.Time // UTC (R9.1)
	RivalName          string
	Location           string
	ReminderSentAt     *time.Time
	AttendanceClosedAt *time.Time
	CreatedBy          int64 // membership
}

type RSVPAnswer string

const (
	RSVPGoing    RSVPAnswer = "going"
	RSVPNotGoing RSVPAnswer = "not_going"
)

// Valid reports whether a is going or not_going (R9.3); a CHECK constraint
// backs it in the database.
func (a RSVPAnswer) Valid() bool {
	return a == RSVPGoing || a == RSVPNotGoing
}

// RSVP is an intention. Attendance, not RSVP, feeds attendance-based
// features (R10.5).
type RSVP struct {
	MatchID      int64
	MembershipID int64
	Answer       RSVPAnswer
	UpdatedAt    time.Time
}

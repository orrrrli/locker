package domain

import "time"

// Match is the only event type in Phase 1 (R9.2). A league fixture creates
// one for each of its teams: FixtureID and CourtID are set, RivalName is the
// other team's current name and CreatedBy is nil (the operator made it).
type Match struct {
	ID                 int64
	TeamID             int64
	StartsAt           time.Time // UTC (R9.1)
	RivalName          string
	Location           string
	ReminderSentAt     *time.Time
	AttendanceClosedAt *time.Time
	CreatedBy          *int64 // membership; nil on a fixture match
	FixtureID          *int64
	CourtID            *int64
	CancelledAt        *time.Time // set once; a cancelled match takes no more writes
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

// UpcomingMatch is one row of the user's upcoming matches across teams
// (R14.2). GoingCount and NotGoingCount count only members active in the
// team now.
type UpcomingMatch struct {
	Match
	TeamName      string
	Role          Role        // the user's role in the team
	Answer        *RSVPAnswer // the user's own answer; nil when none
	GoingCount    int
	NotGoingCount int
}

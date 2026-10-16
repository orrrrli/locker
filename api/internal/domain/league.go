package domain

import (
	"fmt"
	"time"
)

// Weekday is an ISO 8601 day of the week: 1 is Monday, 7 is Sunday, so
// leagues sort Monday first (R17.7).
type Weekday int

var weekdayNames = [...]string{"", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}

// ParseWeekday reads a lowercase English day name, "monday" to "sunday".
func ParseWeekday(s string) (Weekday, bool) {
	for i := 1; i < len(weekdayNames); i++ {
		if weekdayNames[i] == s {
			return Weekday(i), true
		}
	}
	return 0, false
}

// Valid reports whether w is 1 to 7; a CHECK constraint backs it.
func (w Weekday) Valid() bool { return w >= 1 && w <= 7 }

func (w Weekday) String() string {
	if !w.Valid() {
		return fmt.Sprintf("Weekday(%d)", int(w))
	}
	return weekdayNames[w]
}

// TimeOfDay is a wall-clock time in minutes after midnight, 0 to 1439.
type TimeOfDay int

// String writes "HH:MM".
func (t TimeOfDay) String() string { return fmt.Sprintf("%02d:%02d", int(t)/60, int(t)%60) }

// League plays at one venue on one weekday, from StartTime to EndTime in the
// venue's timezone, for one season (R16.1, R16.3). EndTime is after
// StartTime (R16.2) and SeasonEndsOn is not before SeasonStartsOn.
type League struct {
	ID             int64
	VenueID        int64
	Name           string
	Weekday        Weekday
	StartTime      TimeOfDay
	EndTime        TimeOfDay
	SeasonName     string
	SeasonStartsOn time.Time // a date: midnight UTC
	SeasonEndsOn   time.Time // a date: midnight UTC
	CreatedAt      time.Time
}

// Enrollment puts a team in a league (R16.6).
type Enrollment struct {
	LeagueID   int64
	TeamID     int64
	EnrolledAt time.Time
}

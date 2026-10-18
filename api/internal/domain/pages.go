package domain

import (
	"strings"
	"time"
	"unicode"
)

// PublicTeam is a team's public page (R17.1-R17.4): what any signed-in user
// may see. It never carries members, shirt numbers, matches, RSVP,
// attendance, fees or notices (R17.5, NFR 1).
type PublicTeam struct {
	ID            int64
	Name          string
	Initials      string
	ActiveMembers int
	Followers     int      // R20.4: the count, never who
	Captains      []string // the active admins' names, shown as "Capitán"
	Leagues       []TeamLeague
	// ViewerStatus is the viewer's own membership, active or pending; nil
	// when they have none, or left. It decides whether the page offers a
	// join request (R17.6, R13.7).
	ViewerStatus *MembershipStatus
}

// TeamLeague is a league on a team's page, with its venue.
type TeamLeague struct {
	ID        int64
	Name      string
	Weekday   Weekday
	StartTime TimeOfDay
	EndTime   TimeOfDay
	VenueID   int64
	VenueName string
}

// VenuePage is a venue's public page (R17.7): the venue with its courts, and
// its leagues Monday first.
type VenuePage struct {
	Venue
	Leagues []League // only ID, Name, Weekday, StartTime and EndTime are set
}

// LeaguePage is a league's public page (R17.8) with its venue.
type LeaguePage struct {
	League
	Venue     Venue // only ID, Name, Address and Timezone are set
	Followers int   // R20.4: the count, never who
}

// LeagueTeam is a team on a league's page.
type LeagueTeam struct {
	ID       int64
	Name     string
	Initials string
}

// FixtureDay is the league's next date with fixtures, in the venue's
// timezone; Day is nil when none is left (R17.8).
type FixtureDay struct {
	Day      *time.Time // a date: midnight UTC
	Fixtures []FixtureLine
}

// FixtureLine is one fixture on a league's page.
type FixtureLine struct {
	ID           int64
	StartsAt     time.Time // UTC
	CourtID      int64
	CourtName    string
	HomeTeamID   int64
	HomeTeamName string
	AwayTeamID   int64
	AwayTeamName string
}

// Initials are the two letters a team shows in place of a crest: the first
// letters of its first two words ("Atlético Narvarte" is "AN"), or the
// first two letters of a one-word name ("Pumas" is "PU"), uppercased.
func Initials(name string) string {
	words := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var out []rune
	switch {
	case len(words) >= 2:
		out = []rune{[]rune(words[0])[0], []rune(words[1])[0]}
	case len(words) == 1:
		out = []rune(words[0])
		if len(out) > 2 {
			out = out[:2]
		}
	}
	return strings.ToUpper(string(out))
}

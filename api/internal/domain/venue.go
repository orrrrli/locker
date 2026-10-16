package domain

import "time"

// Venue is a place with courts where leagues play, curated by the operator
// (R15, rule 12).
type Venue struct {
	ID        int64
	Name      string
	Address   string
	Latitude  float64
	Longitude float64
	Timezone  string // IANA zone (R15.2)
	CreatedAt time.Time
	Courts    []Court // oldest first; never empty once stored (R15.3)
}

// Court is one field of a venue.
type Court struct {
	ID      int64
	VenueID int64
	Name    string
}

// VenueChange is a partial update of a venue (R15.4): a nil field keeps its
// value.
type VenueChange struct {
	Name      *string
	Address   *string
	Latitude  *float64
	Longitude *float64
	Timezone  *string
}

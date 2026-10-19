package domain

import "time"

// PostKind is who a post speaks for (R18.2): a team (written by an admin), a
// player (linked to one of the player's teams), a league or a venue (written
// by the operator).
type PostKind string

const (
	PostAsTeam   PostKind = "team"
	PostAsPlayer PostKind = "player"
	PostAsLeague PostKind = "league"
	PostAsVenue  PostKind = "venue"
)

// ReactionKind is one of the three reactions a user can set on a post (R19.1).
type ReactionKind string

const (
	ReactionFire ReactionKind = "fire"
	ReactionBall ReactionKind = "ball"
	ReactionClap ReactionKind = "clap"
)

// Post is a feed entry with text, photos or both (R18.7). Exactly one of
// TeamID, LeagueID and VenueID is set, by AsKind; a player post sets TeamID.
type Post struct {
	ID           int64
	AuthorUserID int64 // the writer
	AsKind       PostKind
	TeamID       *int64
	LeagueID     *int64
	VenueID      *int64
	Body         *string
	ShareToken   string // the /p/{token} link (R21)
	CreatedAt    time.Time
	Media        []PostMedia // in position order
}

// PostMedia is one photo of a post. StorageKey is the R2 base of the photo's
// two objects, {StorageKey}/large and {StorageKey}/small; SizeBytes is the
// large one's.
type PostMedia struct {
	StorageKey  string
	Position    int
	ContentType string
	SizeBytes   int64
}

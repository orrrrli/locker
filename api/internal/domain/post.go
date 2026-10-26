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

// FeedFilter picks which posts a feed holds (R22.3): "all", "teams" (the
// "My teams" filter) or "following".
type FeedFilter string

const (
	FeedAll       FeedFilter = "all"
	FeedTeams     FeedFilter = "teams"
	FeedFollowing FeedFilter = "following"

	// FeedNearby and FeedOfficial are the two feeds of a user without a team
	// (R22.7, R22.8). Internal: the use case picks them, the client never sends them.
	FeedNearby   FeedFilter = "nearby"
	FeedOfficial FeedFilter = "official"
)

// LatLng is a point on Earth in degrees. It lives for one request only.
type LatLng struct{ Latitude, Longitude float64 }

// FeedCursor is the last post of the previous page: the next page holds the
// posts strictly older than it, ordered by (created_at, id) descending.
type FeedCursor struct {
	CreatedAt time.Time
	ID        int64
}

// ReactionCounts is how many users set each reaction kind on a post.
type ReactionCounts struct{ Fire, Ball, Clap int }

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
	// RemovedAt is when the operator removed the post (R23.9); nil otherwise.
	// A removed post shows nowhere and cannot be deleted through the API: that
	// removal is final, and only account deletion (R18.13) or dissolving the
	// team end it.
	RemovedAt *time.Time
	Media     []PostMedia // in position order

	// Feed-only: filled by the feed query (R22.12).
	AuthorName     string  // team, player (as in the linked team), league or venue name
	TeamName       *string // linked team of a team or player post; nil otherwise
	ReactionCounts ReactionCounts
	MyReactions    []ReactionKind // the viewer's own; empty, never nil, when none
}

// PostRemoval is what the operator's removal recorded (R23.9). The post row and
// its photos stay; the post shows nowhere.
type PostRemoval struct {
	PostID        int64
	RemovedAt     time.Time
	RemovalReason string
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

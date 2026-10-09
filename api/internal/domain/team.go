package domain

import "time"

type Team struct {
	ID                  int64
	Name                string
	Timezone            string // IANA zone (R6.7)
	CaptainMembershipID *int64 // a label only; grants no permission (R6.6)
	CreatedAt           time.Time
}

// Role grants permissions. The captain is not a role.
type Role string

const (
	RoleAdmin  Role = "admin"
	RolePlayer Role = "player"
)

type MembershipStatus string

const (
	MembershipPending MembershipStatus = "pending"
	MembershipActive  MembershipStatus = "active"
	MembershipLeft    MembershipStatus = "left"
)

// Membership links a user to a team. All team history hangs off it, so
// UserID is nil once the user deletes the account (R5.3).
type Membership struct {
	ID                  int64
	TeamID              int64
	UserID              *int64
	Role                Role
	Status              MembershipStatus
	ShirtNumber         *int
	Position            *string
	DisplayNameOverride *string
	PushMuted           bool
	CreatedAt           time.Time
}

type Invite struct {
	ID        int64
	TeamID    int64
	TokenHash []byte // SHA-256; the token itself is never stored
	CreatedBy int64  // membership
	ExpiresAt time.Time
	RevokedAt *time.Time
}

package domain

import (
	"errors"
	"time"
)

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

// RosterMember is one line of the team's member list (R8.1).
type RosterMember struct {
	ID          int64
	Name        string
	Role        Role
	Status      MembershipStatus
	ShirtNumber *int
	Position    *string
}

type Invite struct {
	ID        int64
	TeamID    int64
	TokenHash []byte // SHA-256; the token itself is never stored
	CreatedBy int64  // membership
	ExpiresAt time.Time
	RevokedAt *time.Time
}

// ErrLastAdmin rejects a change that would leave the team with no active
// admin (R6.4).
var ErrLastAdmin = errors.New("the team needs another active admin first")

// CheckAdminLoss rejects demoting, removing or deleting m when m is the
// team's only active admin (R6.4). In one transaction, callers lock the team,
// then read m, then check, then write: an m read before the lock can be stale
// and let the last admin go. activeAdmins is the count taken under that lock.
func CheckAdminLoss(m Membership, activeAdmins int) error {
	if m.Role == RoleAdmin && m.Status == MembershipActive && activeAdmins <= 1 {
		return ErrLastAdmin
	}
	return nil
}

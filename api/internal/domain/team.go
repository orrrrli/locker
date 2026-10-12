package domain

import (
	"errors"
	"time"
)

type Team struct {
	ID        int64
	Name      string
	Timezone  string // IANA zone (R6.7)
	CreatedAt time.Time
}

// Role grants permissions.
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

// Position is where a member plays (R8.2).
type Position string

const (
	PositionGoalkeeper Position = "goalkeeper"
	PositionDefender   Position = "defender"
	PositionMidfielder Position = "midfielder"
	PositionForward    Position = "forward"
)

// Valid reports whether p is one of the four positions; the use case checks
// it before a write, and a CHECK constraint backs it in the database.
func (p Position) Valid() bool {
	switch p {
	case PositionGoalkeeper, PositionDefender, PositionMidfielder, PositionForward:
		return true
	}
	return false
}

// Membership links a user to a team. All team history hangs off it, so
// UserID is nil once the user deletes the account (R5.3).
type Membership struct {
	ID                  int64
	TeamID              int64
	UserID              *int64
	Role                Role
	Status              MembershipStatus
	ShirtNumber         *int
	Position            *Position
	DisplayNameOverride *string
	PushMuted           bool
	CreatedAt           time.Time
}

// ProfileChange sets shirt number and position (R8.2). A field is changed
// only when its Set flag is true; a nil value clears it.
type ProfileChange struct {
	SetShirtNumber bool
	ShirtNumber    *int
	SetPosition    bool
	Position       *Position
}

// RosterMember is one line of the team's member list (R8.1).
type RosterMember struct {
	ID          int64
	Name        string
	Role        Role
	Status      MembershipStatus
	ShirtNumber *int
	Position    *Position
}

// PendingMembership is one of the user's own pending memberships, shown in
// the pending section of "Mis equipos" (R24.5). PendingSince is when it last
// became pending: a member who left and asked again gets the new date.
type PendingMembership struct {
	ID           int64
	TeamID       int64
	TeamName     string
	PendingSince time.Time
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

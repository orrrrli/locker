package domain

import "time"

// User is a person with a Locker account. After account deletion the
// personal fields are nil and DeletedAt is set; team history stays on
// Membership (R5.2, R5.3).
type User struct {
	ID              int64
	Name            *string
	Email           *string
	EmailVerifiedAt *time.Time
	BirthDate       *time.Time
	PhotoURL        *string
	DeletedAt       *time.Time
	CreatedAt       time.Time
}

type Provider string

const (
	ProviderApple    Provider = "apple"
	ProviderPassword Provider = "password"
)

// AuthIdentity is one way to sign in as a User (R4.1). PasswordHash is set
// only for ProviderPassword.
type AuthIdentity struct {
	ID           int64
	UserID       int64
	Provider     Provider
	Subject      string
	PasswordHash *string
	CreatedAt    time.Time
}

// Session is an opaque login token; only hashes are stored.
type Session struct {
	ID                int64
	UserID            int64
	TokenHash         []byte
	PreviousTokenHash []byte
	CreatedAt         time.Time
	LastUsedAt        time.Time
	RotatedAt         time.Time
}

// DeviceToken is an APNs token for one of the user's devices (R11.7).
type DeviceToken struct {
	ID         int64
	UserID     int64
	Token      string
	CreatedAt  time.Time
	LastSeenAt time.Time
}

package domain

import "time"

// VenueLead is a venue owner's request to run their leagues on Locker
// (R25.1). Phone or Email is set, or both (R25.3). UserID is nil once the
// sender deletes the account (R25.8).
type VenueLead struct {
	ID          int64
	UserID      *int64
	ContactName string
	VenueName   string
	City        string
	Phone       *string
	Email       *string
	Message     *string
	CreatedAt   time.Time
	HandledAt   *time.Time // set by the operator (R25.6)
}

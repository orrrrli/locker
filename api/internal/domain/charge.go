package domain

import "time"

// Charge is a fee record; Locker takes no payments. MatchID is set when the
// charge targets a match's real attendees (R12.1).
type Charge struct {
	ID          int64
	TeamID      int64
	Concept     string
	AmountCents int64
	Currency    string
	MatchID     *int64
	CreatedBy   int64 // membership
	CreatedAt   time.Time
}

type ChargeStatus string

const (
	ChargePending ChargeStatus = "pending"
	ChargePaid    ChargeStatus = "paid"
)

// ChargeMember is one member's entry in a charge. PaidMarkedBy and
// PaidMarkedAt are set when an admin marks it paid (R12.3).
type ChargeMember struct {
	ChargeID     int64
	MembershipID int64
	Status       ChargeStatus
	PaidMarkedBy *int64 // membership
	PaidMarkedAt *time.Time
}

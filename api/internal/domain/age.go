package domain

import (
	"errors"
	"time"
)

// MinAge is the youngest a user can be to register. Users aged 15-17 are
// treated the same as adults (R1.3).
const MinAge = 15

var (
	ErrBirthDateRequired = errors.New("birth date is required")
	ErrBirthDateInFuture = errors.New("birth date is in the future")
	ErrUnderage          = errors.New("user is younger than 15")
)

// CheckAge applies the registration age gate (R1.1, R1.2). Both dates are
// compared as calendar dates; a user born on 29 February turns 15 on 1 March
// in non-leap years.
func CheckAge(birthDate, today time.Time) error {
	if birthDate.IsZero() {
		return ErrBirthDateRequired
	}
	birth := dateOf(birthDate)
	now := dateOf(today)
	if birth.After(now) {
		return ErrBirthDateInFuture
	}
	if birth.AddDate(MinAge, 0, 0).After(now) {
		return ErrUnderage
	}
	return nil
}

func dateOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

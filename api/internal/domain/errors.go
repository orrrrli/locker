package domain

import "errors"

var (
	// ErrNotFound reports that the requested entity does not exist.
	ErrNotFound = errors.New("not found")
	// ErrAlreadyExists reports that a uniqueness rule rejected the write.
	ErrAlreadyExists = errors.New("already exists")
	// ErrTeamBusy reports that the team lock stayed held past the lock
	// timeout. The request changed nothing and can be retried.
	ErrTeamBusy = errors.New("team busy, try again")
)

package domain

import "errors"

var (
	// ErrNotFound reports that the requested entity does not exist.
	ErrNotFound = errors.New("not found")
	// ErrAlreadyExists reports that a uniqueness rule rejected the write.
	ErrAlreadyExists = errors.New("already exists")
)

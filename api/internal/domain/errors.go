package domain

import "errors"

// ErrNotFound reports that the requested entity does not exist.
var ErrNotFound = errors.New("not found")

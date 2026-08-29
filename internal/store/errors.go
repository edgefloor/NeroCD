package store

import "errors"

var (
	// ErrNotFound reports that the requested record does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict reports that a requested mutation conflicts with persisted state.
	ErrConflict = errors.New("conflict")
)

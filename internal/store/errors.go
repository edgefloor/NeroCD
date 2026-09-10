package store

import "errors"

var (
	// ErrNotFound reports that the requested record does not exist.
	ErrNotFound = errors.New("not found")
	// ErrConflict reports that a requested mutation conflicts with persisted state.
	ErrConflict = errors.New("conflict")
	// ErrLastAdministrator reports that offboarding would remove the final
	// active human recovery administrator.
	ErrLastAdministrator = errors.New("last active system administrator")
)

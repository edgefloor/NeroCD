package app

import "errors"

// ErrInvalidInput identifies errors caused by invalid application input.
var ErrInvalidInput = errors.New("invalid input")

// InvalidInput marks err as invalid application input while preserving its identity.
func InvalidInput(err error) error {
	if err == nil {
		return nil
	}
	return invalidInputError{err: err}
}

type invalidInputError struct {
	err error
}

func (e invalidInputError) Error() string {
	return e.err.Error()
}

func (e invalidInputError) Unwrap() error {
	return e.err
}

func (e invalidInputError) Is(target error) bool {
	return target == ErrInvalidInput
}

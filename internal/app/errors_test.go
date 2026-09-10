package app

import (
	"errors"
	"fmt"
	"testing"
)

type invalidInputCause struct {
	message string
}

func (e *invalidInputCause) Error() string {
	return e.message
}

func TestInvalidInputNil(t *testing.T) {
	if got := InvalidInput(nil); got != nil {
		t.Fatalf("InvalidInput(nil) = %v, want nil", got)
	}
}

func TestInvalidInputPreservesCauseIdentity(t *testing.T) {
	cause := &invalidInputCause{message: "field must be present"}
	wrapped := InvalidInput(cause)

	if got := wrapped.Error(); got != cause.Error() {
		t.Errorf("InvalidInput().Error() = %q, want %q", got, cause.Error())
	}
	if !errors.Is(wrapped, ErrInvalidInput) {
		t.Error("InvalidInput() does not match ErrInvalidInput")
	}
	if !errors.Is(wrapped, cause) {
		t.Error("InvalidInput() does not match its direct cause")
	}

	contextual := fmt.Errorf("request validation: %w", wrapped)
	if !errors.Is(contextual, cause) {
		t.Error("wrapped InvalidInput() does not match its cause")
	}
	var gotCause *invalidInputCause
	if !errors.As(contextual, &gotCause) || gotCause != cause {
		t.Fatalf("errors.As() cause = %v, want %v", gotCause, cause)
	}
}

package store

import (
	"fmt"
	"regexp"

	"nerocd/internal/secretcrypto"
)

// bootstrapEnrollmentBinding cannot collide with an ordinary PostgreSQL TEXT
// secret ID: its purpose namespace includes NUL and AAD is length-prefixed.
func bootstrapEnrollmentBinding(project, operation, enrollment string) (secretcrypto.Binding, error) {
	b := secretcrypto.Binding{ProjectID: project, SecretID: "nerocd/server-bootstrap/enrollment/v1\x00" + operation + "\x00" + enrollment, Version: 1}
	if project == "" || !regexp.MustCompile(`^sbo_[0-9a-f]{32}$`).MatchString(operation) || !regexp.MustCompile(`^enroll_[0-9a-f]{32}$`).MatchString(enrollment) || len(project) > 256 || len(b.SecretID) > 256 {
		return secretcrypto.Binding{}, fmt.Errorf("bootstrap enrollment binding: %w", secretcrypto.ErrInvalidInput)
	}
	return b, nil
}

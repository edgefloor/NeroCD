package store

import (
	"bytes"
	"errors"
	"nerocd/internal/secretcrypto"
	"strings"
	"testing"
)

func TestBootstrapEnrollmentBindingPurposeProjectAndRotation(t *testing.T) {
	op := "sbo_" + strings.Repeat("a", 32)
	enrollment := "enroll_" + strings.Repeat("b", 32)
	binding, err := bootstrapEnrollmentBinding(strings.Repeat("p", 256), op, enrollment)
	if err != nil {
		t.Fatal(err)
	}
	old := bytes.Repeat([]byte{1}, 32)
	next := bytes.Repeat([]byte{2}, 32)
	ring, err := secretcrypto.New("old", map[string][]byte{"old": old})
	if err != nil {
		t.Fatal(err)
	}
	e, err := ring.Seal(binding, []byte("private_enrollment"))
	if err != nil {
		t.Fatal(err)
	}
	rotated, _ := secretcrypto.New("new", map[string][]byte{"old": old, "new": next})
	if _, err = rotated.Open(binding, e); err != nil {
		t.Fatal(err)
	}
	wrong, _ := secretcrypto.New("old", map[string][]byte{"old": next})
	if _, err = wrong.Open(binding, e); !errors.Is(err, secretcrypto.ErrDecrypt) {
		t.Fatal("wrong key", err)
	}
	for _, other := range []secretcrypto.Binding{{ProjectID: "other", SecretID: binding.SecretID, Version: 1}, {ProjectID: binding.ProjectID, SecretID: enrollment, Version: 1}, {ProjectID: binding.ProjectID, SecretID: strings.Replace(binding.SecretID, enrollment, "enroll_"+strings.Repeat("c", 32), 1), Version: 1}, {ProjectID: binding.ProjectID, SecretID: strings.Replace(binding.SecretID, "enrollment/v1", "ssh/v1", 1), Version: 1}} {
		if _, err = rotated.Open(other, e); !errors.Is(err, secretcrypto.ErrDecrypt) {
			t.Fatal("binding substitution", err)
		}
	}
	if _, err = bootstrapEnrollmentBinding("project", op+"\x00", enrollment); err == nil {
		t.Fatal("delimiter alias")
	}
}

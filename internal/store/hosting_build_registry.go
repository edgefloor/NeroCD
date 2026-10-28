package store

import (
	"context"
	"fmt"
	"io"

	"nerocd/internal/domain"
)

// HostingBuildRegistryAuthorization contains encrypted material after audited live authorization.
type HostingBuildRegistryAuthorization struct {
	Window    domain.HostingSourceCredentialWindow
	ProjectID string
	Ref       domain.HostingRegistryCredentialRef
	Record    ManagedSecretRecord
}

// MarshalJSON excludes encrypted material from accidental serialization.
func (HostingBuildRegistryAuthorization) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// Format redacts encrypted authorization material.
func (HostingBuildRegistryAuthorization) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted build registry authorization]")
}

// HostingBuildRegistryRepository authorizes the immutable selected reference for a live build.
type HostingBuildRegistryRepository interface {
	AuthorizeHostingBuildRegistryCredential(context.Context, string, domain.HostingBuildRegistryCredentialRequest) (HostingBuildRegistryAuthorization, error)
}

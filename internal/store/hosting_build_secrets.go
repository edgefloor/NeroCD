package store

import (
	"context"
	"fmt"
	"io"
	"nerocd/internal/domain"
)

// HostingBuildSecretAuthorization pairs the immutable selection with its encrypted record.
type HostingBuildSecretAuthorization struct {
	Ref    domain.HostingBuildSecretReference
	Record ManagedSecretRecord
}

// HostingBuildSecretsAuthorization contains material only after committed live authorization.
type HostingBuildSecretsAuthorization struct {
	Window    domain.HostingSourceCredentialWindow
	ProjectID string
	Secrets   []HostingBuildSecretAuthorization
}

// MarshalJSON excludes encrypted authorization material.
func (HostingBuildSecretsAuthorization) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// Format redacts encrypted authorization material.
func (HostingBuildSecretsAuthorization) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted build secrets authorization]")
}

// HostingBuildSecretsRepository authorizes only immutable selected references for live builds.
type HostingBuildSecretsRepository interface {
	AuthorizeHostingBuildSecrets(context.Context, string, domain.HostingBuildSecretsRequest) (HostingBuildSecretsAuthorization, error)
}

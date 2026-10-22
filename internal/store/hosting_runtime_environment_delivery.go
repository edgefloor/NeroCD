package store

import (
	"context"
	"fmt"
	"io"

	"nerocd/internal/domain"
)

// HostingRuntimeEnvironmentAuthorization contains encrypted versions only after audit commit.
type HostingRuntimeEnvironmentAuthorization struct {
	Window    domain.HostingRuntimeEnvironmentWindow                       `json:"-"`
	ProjectID string                                                       `json:"-"`
	Document  domain.HostingRuntimeDocument                                `json:"-"`
	Records   map[domain.HostingRuntimeSecretReference]ManagedSecretRecord `json:"-"`
}

// Format suppresses authorization contents for every formatting verb.
func (HostingRuntimeEnvironmentAuthorization) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted runtime environment authorization]")
}

// MarshalJSON excludes all authorization contents.
func (HostingRuntimeEnvironmentAuthorization) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// Close clears owned encrypted buffers after use or failed authorization.
func (a *HostingRuntimeEnvironmentAuthorization) Close() {
	for _, r := range a.Records {
		clear(r.Envelope.Data)
	}
	*a = HostingRuntimeEnvironmentAuthorization{}
}

// HostingRuntimeEnvironmentRepository never decrypts or authorizes historical replay.
type HostingRuntimeEnvironmentRepository interface {
	AuthorizeHostingRuntimeEnvironment(context.Context, string, domain.HostingRuntimeEnvironmentRequest) (HostingRuntimeEnvironmentAuthorization, error)
}

var _ HostingRuntimeEnvironmentRepository = (*PostgresStore)(nil)

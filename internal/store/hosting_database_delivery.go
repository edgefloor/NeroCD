package store

import (
	"context"
	"fmt"
	"io"

	"nerocd/internal/domain"
	"nerocd/internal/hostingdatabaseprotocol"
)

// HostingDatabaseCredentialAuthorization contains encrypted records after audit commit.
type HostingDatabaseCredentialAuthorization struct {
	Metadata  hostingdatabaseprotocol.Metadata                            `json:"-"`
	ProjectID string                                                      `json:"-"`
	Records   map[domain.HostingDatabaseCredentialRef]ManagedSecretRecord `json:"-"`
}

// Format suppresses authorization contents for every formatting verb.
func (HostingDatabaseCredentialAuthorization) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted database credential authorization]")
}

// MarshalJSON excludes authorization contents.
func (HostingDatabaseCredentialAuthorization) MarshalJSON() ([]byte, error) { return []byte("{}"), nil }

// Close clears owned encrypted buffers.
func (a *HostingDatabaseCredentialAuthorization) Close() {
	for _, r := range a.Records {
		clear(r.Envelope.Data)
	}
	*a = HostingDatabaseCredentialAuthorization{}
}

// HostingDatabaseCredentialRepository authorizes before decryption and rechecks
// original authority after decryption without extending the original deadline.
type HostingDatabaseCredentialRepository interface {
	AuthorizeHostingDatabaseCredentials(context.Context, string, string, domain.HostingDatabaseCredentialRequest) (HostingDatabaseCredentialAuthorization, error)
	RevalidateHostingDatabaseCredentials(context.Context, string, string, domain.HostingDatabaseCredentialRequest, hostingdatabaseprotocol.Metadata) error
}

var _ HostingDatabaseCredentialRepository = (*PostgresStore)(nil)

package store

import (
	"context"
	"fmt"
	"io"

	"nerocd/internal/domain"
)

// HostingSourceCredentialAuthorization carries storage-only encrypted material.
// Its successful return proves the authorization audit committed, not delivery.
// JSON and formatting deliberately expose none of its contents.
type HostingSourceCredentialAuthorization struct {
	Window        domain.HostingSourceCredentialWindow `json:"-"`
	ProjectID     string                               `json:"-"`
	SecretID      string                               `json:"-"`
	SecretVersion int64                                `json:"-"`
	Kind          string                               `json:"-"`
	Record        ManagedSecretRecord                  `json:"-"`
}

// Format redacts all encrypted authorization data for every formatting verb.
func (HostingSourceCredentialAuthorization) Format(s fmt.State, _ rune) {
	_, _ = io.WriteString(s, "[redacted source credential authorization]")
}

// HostingSourceCredentialRepository authorizes only the authenticated runner's
// current sourcing attempt and returns an encrypted record after audit commit.
type HostingSourceCredentialRepository interface {
	AuthorizeHostingSourceCredential(context.Context, string, domain.HostingSourceCredentialRequest) (HostingSourceCredentialAuthorization, error)
}

var _ HostingSourceCredentialRepository = (*PostgresStore)(nil)

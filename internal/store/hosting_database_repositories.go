package store

import (
	"context"
	"errors"

	"nerocd/internal/domain"
)

// ErrHostingDatabaseUnavailable requires the dedicated durable metadata schema.
var ErrHostingDatabaseUnavailable = errors.New("database metadata requires compatible PostgreSQL storage")

// HostingDatabaseCredentialRecord carries only encrypted storage data internally.
type HostingDatabaseCredentialRecord struct {
	Reference domain.HostingDatabaseCredentialRef `json:"-"`
	Record    ManagedSecretRecord                 `json:"-"`
}

// HostingDatabaseRepository owns atomic database metadata and credential creation.
// Replay rechecks current project owner and destination grant authority.
type HostingDatabaseRepository interface {
	CreateHostingDatabase(context.Context, string, domain.HostingDatabaseCreateInput, []HostingDatabaseCredentialRecord, domain.AuditEvent) (domain.HostingDatabase, error)
	GetHostingDatabase(context.Context, string, string) (domain.HostingDatabase, error)
}

var _ HostingDatabaseRepository = (*PostgresStore)(nil)

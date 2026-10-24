package store

import (
	"context"
	"errors"

	"nerocd/internal/domain"
)

// ErrHostingDatabaseBindingUnavailable requires the durable pending-binding schema.
var ErrHostingDatabaseBindingUnavailable = errors.New("database binding storage requires PostgreSQL")

// HostingDatabaseBindingRepository saves desired references only. Connectivity
// remains pending; each method rechecks current persisted project authority.
type HostingDatabaseBindingRepository interface {
	PutHostingDatabaseBinding(context.Context, string, string, domain.HostingDatabaseBindingInput, domain.AuditEvent) (domain.HostingDatabaseBinding, error)
	GetHostingDatabaseBinding(context.Context, string, string) (domain.HostingDatabaseBinding, error)
}

var _ HostingDatabaseBindingRepository = (*PostgresStore)(nil)

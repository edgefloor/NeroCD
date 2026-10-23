package store

import (
	"context"
	"errors"
	"nerocd/internal/domain"
)

// ErrHostingStorageUnavailable requires compatible durable storage metadata.
var ErrHostingStorageUnavailable = errors.New("hosting storage requires compatible PostgreSQL metadata")

// HostingStorageRepository creates desired metadata only, with current persisted
// project and destination authority checked transactionally, including retries.
// Get returns ErrNotFound when an authorized binding has no configured storage.
type HostingStorageRepository interface {
	GetHostingStorage(context.Context, string, string) (domain.HostingStorage, error)
	CreateHostingStorage(context.Context, string, string, domain.HostingStorageInput, domain.AuditEvent) (domain.HostingStorage, error)
}

var _ HostingStorageRepository = (*PostgresStore)(nil)

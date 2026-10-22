package store

import (
	"context"
	"errors"

	"nerocd/internal/domain"
)

// ErrHostingRuntimeConfigurationUnavailable requires durable configuration storage.
var ErrHostingRuntimeConfigurationUnavailable = errors.New("hosting runtime configuration requires PostgreSQL")

// HostingRuntimeConfigurationRepository stores reference-only desired documents.
// Each operation verifies current persisted project authority inside its transaction.
type HostingRuntimeConfigurationRepository interface {
	GetHostingRuntimeConfiguration(context.Context, string, string) (domain.HostingRuntimeConfiguration, error)
	PutHostingRuntimeConfiguration(context.Context, string, string, domain.HostingRuntimeConfigurationInput, domain.AuditEvent) (domain.HostingRuntimeConfiguration, error)
}

var _ HostingRuntimeConfigurationRepository = (*PostgresStore)(nil)

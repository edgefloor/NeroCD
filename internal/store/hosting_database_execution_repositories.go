package store

import (
	"context"

	"nerocd/internal/domain"
)

// HostingDatabaseExecutionRepository admits private database operations through
// the durable hosting queue. It does not qualify any runner implementation.
type HostingDatabaseExecutionRepository interface {
	RequestHostingDatabaseDeployment(context.Context, string, string, string, domain.HostingDatabaseDeploymentInput, domain.AuditEvent) (domain.HostingOperation, error)
}

var _ HostingDatabaseExecutionRepository = (*PostgresStore)(nil)

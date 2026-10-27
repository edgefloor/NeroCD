package store

import (
	"context"
	"nerocd/internal/domain"
)

// HostingStackLifecycleRepository owns whole-stack lifecycle admission and evidence.
type HostingStackLifecycleRepository interface {
	HostingStackLifecycle(context.Context, string, string) (domain.HostingStackLifecycle, error)
	RequestHostingStackStop(context.Context, string, string, string, domain.HostingStackStopInput, domain.AuditEvent) (domain.HostingOperation, error)
}

var _ HostingStackLifecycleRepository = (*PostgresStore)(nil)

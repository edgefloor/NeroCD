package store

import (
	"context"

	"nerocd/internal/domain"
)

// HostingStorageRouteRecoveryRepository queues only unlinked continuity after
// verifying the exact successful deployment and current operator authority.
type HostingStorageRouteRecoveryRepository interface {
	RecoverHostingStorageRouteContinuity(context.Context, string, string, string, domain.HostingStorageRouteRecoveryInput, domain.AuditEvent) (domain.HostingStorageRouteContinuityStatus, error)
}

var _ HostingStorageRouteRecoveryRepository = (*PostgresStore)(nil)

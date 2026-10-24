package store

import (
	"context"
	"nerocd/internal/domain"
)

// HostingDatabaseUpgradeRepository admits a controller-derived empty-network
// upgrade using current authorization and an immutable historical origin proof.
type HostingDatabaseUpgradeRepository interface {
	RequestHostingDatabaseNetworkUpgrade(ctx context.Context, operationID, databaseID, actorID string, input domain.HostingDatabaseUpgradeInput, audit domain.AuditEvent) (domain.HostingOperation, error)
}

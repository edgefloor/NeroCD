package store

import (
	"context"

	"nerocd/internal/domain"
)

// HostingStorageDeploymentReadRepository returns authorized resource-only views
// from consistent read-only snapshots, without admitting or reserving work.
type HostingStorageDeploymentReadRepository interface {
	HostingStorageDeploymentOptions(context.Context, string, string) (domain.HostingStorageDeploymentOptions, error)
	HostingStorageRouteContinuityStatus(context.Context, string, string, string) (domain.HostingStorageRouteContinuityStatus, error)
}

var _ HostingStorageDeploymentReadRepository = (*PostgresStore)(nil)

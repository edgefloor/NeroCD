package store

import (
	"context"
	"nerocd/internal/domain"
)

// HostingStorageMaterializationRepository owns irreversible volume admission and
// evidence only. It never performs Docker I/O or returns replayable create permission.
type HostingStorageMaterializationRepository interface {
	HostingStorageCreateBegin(context.Context, string, domain.HostingStorageCreateRequest) (domain.HostingStorageCreateAck, error)
	HostingStorageMaterialized(context.Context, string, domain.HostingStorageMaterializedRequest) (domain.HostingStorageMaterializedAck, error)
}

var _ HostingStorageMaterializationRepository = (*PostgresStore)(nil)

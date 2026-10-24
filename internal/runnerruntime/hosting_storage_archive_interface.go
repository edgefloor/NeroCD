package runnerruntime

import (
	"context"

	"nerocd/internal/domain"
)

// The coordinator records and acknowledges every intent before calling this
// engine. Mutation callbacks must run after inspection and immediately before
// each daemon mutation. A helper ID is never inferred from a name or a label.
type storageArchiveExecutionEngine interface {
	Close() error
	Prepare(context.Context, domain.HostingStorageArchivePlan) error
	InspectWriters(context.Context, domain.HostingStorageArchivePlan) ([]domain.HostingStorageArchiveWriterState, error)
	EnsureRestoreVolume(context.Context, hostingOwnedVolumeRequest, func(context.Context) error) (domain.HostingStorageMaterialization, error)
	StopWriter(context.Context, domain.HostingStorageArchivePlan, domain.HostingStorageArchiveWriterState, func(context.Context) error) error
	ResumeWriter(context.Context, domain.HostingStorageArchivePlan, domain.HostingStorageArchiveWriterState, func(context.Context) error) error
	CreateHelper(context.Context, domain.HostingStorageArchivePlan, string, func(context.Context) error) (string, error)
	Backup(context.Context, domain.HostingStorageArchivePlan, string, string, func(context.Context) error) (domain.HostingStorageArchiveBlob, error)
	Restore(context.Context, domain.HostingStorageArchivePlan, string, string, func(context.Context) error) error
	CleanupHelper(context.Context, domain.HostingStorageArchivePlan, string, string, func(context.Context) error) error
}

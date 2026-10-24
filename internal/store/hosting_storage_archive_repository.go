package store

import (
	"context"
	"errors"
	"nerocd/internal/domain"
)

// ErrStorageArchiveUnavailable means qualified configuration or schema is absent.
var ErrStorageArchiveUnavailable = errors.New("storage archive requires qualified configuration and compatible PostgreSQL metadata")

// HostingStorageArchiveRepository owns archive admission and durable authority.
type HostingStorageArchiveRepository interface {
	RequestStorageBackup(context.Context, string, string, string, domain.HostingStorageBackupInput, domain.AuditEvent) (domain.HostingStorageBackup, error)
	GetStorageBackup(context.Context, string, string) (domain.HostingStorageBackup, error)
	ListStorageBackups(context.Context, string, string, Page) (PageResult[domain.HostingStorageBackup], error)
	RequestStorageRestore(context.Context, string, string, string, string, string, domain.HostingStorageRestoreInput, domain.AuditEvent) (domain.HostingStorageRestore, error)
	GetStorageRestore(context.Context, string, string) (domain.HostingStorageRestore, error)
	RequestStorageArchiveRecovery(context.Context, string, string, string, domain.HostingStorageArchiveRecoveryInput, domain.AuditEvent) (domain.HostingOperation, error)
	StorageArchivePlan(context.Context, string, domain.HostingAuthority) (domain.HostingOperation, error)
	StorageArchivePrepared(context.Context, string, domain.HostingStorageArchivePreparedRequest) (domain.HostingOperation, error)
	StorageArchiveApplyBegin(context.Context, string, domain.HostingStorageArchiveApplyBeginRequest) (domain.HostingOperation, error)
	StorageArchiveProgress(context.Context, string, domain.HostingStorageArchiveProgress) (domain.HostingOperation, error)
	StorageArchiveComplete(context.Context, string, domain.HostingStorageArchiveCompleteRequest) (domain.HostingOperation, error)
}

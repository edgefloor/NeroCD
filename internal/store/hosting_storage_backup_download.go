package store

import (
	"context"
	"errors"
	"nerocd/internal/domain"
	"time"
)

// ErrStorageDownloadUnavailable reports missing qualified download execution.
var ErrStorageDownloadUnavailable = errors.New("storage archive download unavailable")

// HostingStorageBackupDownloadRepository persists authority metadata, never archive bytes.
type HostingStorageBackupDownloadRepository interface {
	StorageBackupDownloadOptions(context.Context, string, string) (domain.HostingStorageBackupDownloadOptions, error)
	CreateStorageBackupDownload(context.Context, string, string, string, time.Time) (domain.HostingStorageBackupDownloadPlan, error)
	StorageBackupDownloadPlan(context.Context, string, domain.HostingAuthority) (domain.HostingStorageBackupDownloadPlan, error)
	BeginStorageBackupDownload(context.Context, string, domain.HostingStorageBackupDownloadStream) (domain.HostingStorageBackupDownloadPlan, error)
	CheckStorageBackupDownload(context.Context, string, domain.HostingAuthority) (domain.HostingStorageBackupDownloadPlan, error)
	CloseStorageBackupDownload(context.Context, string, bool) error
}

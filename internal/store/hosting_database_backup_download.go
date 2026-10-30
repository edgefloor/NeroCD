package store

import (
	"context"
	"errors"
	"time"

	"nerocd/internal/domain"
)

// ErrDatabaseDownloadUnavailable reports missing qualified download execution.
var ErrDatabaseDownloadUnavailable = errors.New("database backup download unavailable")

// HostingDatabaseBackupDownloadRepository persists authority metadata, never archive bytes.
type HostingDatabaseBackupDownloadRepository interface {
	DatabaseBackupDownloadOptions(context.Context, string, string) (domain.HostingDatabaseBackupDownloadOptions, error)
	CreateDatabaseBackupDownload(context.Context, string, string, string, time.Time) (domain.HostingDatabaseBackupDownloadPlan, error)
	DatabaseBackupDownloadPlan(context.Context, string, domain.HostingAuthority) (domain.HostingDatabaseBackupDownloadPlan, error)
	BeginDatabaseBackupDownload(context.Context, string, domain.HostingDatabaseBackupDownloadStream) (domain.HostingDatabaseBackupDownloadPlan, error)
	CheckDatabaseBackupDownload(context.Context, string, domain.HostingAuthority) (domain.HostingDatabaseBackupDownloadPlan, error)
	CloseDatabaseBackupDownload(context.Context, string, bool) error
}

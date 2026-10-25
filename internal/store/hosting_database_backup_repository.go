package store

import (
	"context"

	"nerocd/internal/domain"
)

// HostingDatabaseBackupRepository keeps hosted logical backups independent of
// controller backups and upload archives. Only encrypted records cross admission.
type HostingDatabaseBackupRepository interface {
	RequestDatabaseBackup(context.Context, string, string, string, domain.HostingDatabaseBackupInput, domain.AuditEvent) (domain.HostingDatabaseBackupOperation, error)
	RequestDatabaseBackupRestore(context.Context, string, string, string, domain.HostingDatabaseBackupRestoreInput, domain.HostingDatabaseCreateInput, []HostingDatabaseCredentialRecord, domain.AuditEvent) (domain.HostingDatabaseBackupOperation, error)
	GetDatabaseBackup(context.Context, string, string) (domain.HostingDatabaseBackupOperation, error)
	ListDatabaseBackups(context.Context, string, string) ([]domain.HostingDatabaseBackupOperation, error)
	DatabaseBackupPlan(context.Context, string, domain.HostingAuthority) (domain.HostingDatabaseBackupPlan, error)
	DatabaseBackupMutation(context.Context, string, domain.HostingDatabaseBackupMutation) (domain.HostingDatabaseBackupOperation, error)
}

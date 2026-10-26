package store

import (
	"context"

	"nerocd/internal/domain"
)

// BackupRemoteCredentialRecords contains only encrypted envelopes. Compare is invoked under the same
// admission transaction on a replay, so concurrent requests cannot silently
// accept different credentials under one request identity.
type BackupRemoteCredentialRecords struct {
	Access ManagedSecretRecord
	Secret ManagedSecretRecord
}

// BackupRemoteCredentialCompare checks whether replayed encrypted records match the supplied
// credentials.
type BackupRemoteCredentialCompare func(BackupRemoteCredentialRecords) error

// HostingDatabaseBackupRemoteRepository defines persistence operations for database backup
// remote.
type HostingDatabaseBackupRemoteRepository interface {
	SaveDatabaseBackupTarget(context.Context, string, string, string, string, domain.HostingDatabaseBackupTargetInput, *BackupRemoteCredentialRecords, BackupRemoteCredentialCompare) (domain.HostingDatabaseBackupTarget, error)
	ListDatabaseBackupTargets(context.Context, string, string) ([]domain.HostingDatabaseBackupTarget, error)
	ValidateDatabaseBackupTarget(context.Context, string, string, string, string, domain.HostingDatabaseBackupTargetValidationInput) (domain.HostingDatabaseBackupTransfer, error)
	ListDatabaseBackupTransfers(context.Context, string, string, bool) ([]domain.HostingDatabaseBackupTransfer, error)
	GetDatabaseBackupTransfer(context.Context, string, string) (domain.HostingDatabaseBackupTransfer, error)
	RetryDatabaseBackupTransfer(context.Context, string, string, string, domain.HostingDatabaseBackupTransferRetryInput) (domain.HostingDatabaseBackupTransfer, error)
	DatabaseBackupTransferPlan(context.Context, string, domain.HostingAuthority) (domain.HostingDatabaseBackupTransferPlan, error)
	DatabaseBackupTransferMutation(context.Context, string, domain.HostingDatabaseBackupTransferMutation) (domain.HostingDatabaseBackupTransfer, error)
}

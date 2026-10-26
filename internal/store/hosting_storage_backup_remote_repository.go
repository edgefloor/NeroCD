package store

import (
	"context"

	"nerocd/internal/domain"
)

type HostingStorageBackupRemoteRepository interface {
	ValidateStorageBackupTarget(context.Context, string, string, string, string, domain.HostingDatabaseBackupTargetValidationInput) (domain.HostingStorageBackupTransfer, error)
	ListStorageBackupTransfers(context.Context, string, string, bool) ([]domain.HostingStorageBackupTransfer, error)
	GetStorageBackupTransfer(context.Context, string, string) (domain.HostingStorageBackupTransfer, error)
	RetryStorageBackupTransfer(context.Context, string, string, string, domain.HostingStorageBackupTransferRetryInput) (domain.HostingStorageBackupTransfer, error)
	StorageBackupTransferPlan(context.Context, string, domain.HostingAuthority) (domain.HostingStorageBackupTransferPlan, error)
	StorageBackupTransferMutation(context.Context, string, domain.HostingStorageBackupTransferMutation) (domain.HostingStorageBackupTransfer, error)
}
type HostingStorageBackupRemoteCredentialRepository interface {
	AuthorizeStorageRemoteCredentials(context.Context, string, string, domain.BackupRemoteCredentialRequest) (BackupRemoteCredentialAuthorization, error)
	RevalidateStorageRemoteCredentials(context.Context, string, string, domain.BackupRemoteCredentialRequest, domain.BackupRemoteCredentialMetadata) error
}

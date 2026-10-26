package store

import (
	"context"

	"nerocd/internal/domain"
)

type HostingStorageBackupDeletionRepository interface {
	PreviewStorageBackupDeletion(context.Context, string, string, string, string, domain.HostingStorageBackupDeletionPreviewInput) (domain.HostingStorageBackupRetentionPreview, error)
	ExecuteStorageBackupDeletion(context.Context, string, string, string, string, domain.HostingStorageBackupDeletionInput) (domain.HostingStorageBackupRetentionExecution, error)
	ListStorageBackupDeletions(context.Context, string, string, string) ([]domain.HostingStorageBackupRetentionExecution, error)
	GetStorageBackupDeletion(context.Context, string, string, string, string) (domain.HostingStorageBackupRetentionExecution, error)
}

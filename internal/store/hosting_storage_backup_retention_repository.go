package store

import (
	"context"

	"nerocd/internal/domain"
)

type HostingStorageBackupRetentionRepository interface {
	StorageBackupRetentionRetirement(context.Context, string, domain.HostingStorageBackupRetentionRetirementInput) (domain.HostingStorageBackupRetentionRetirement, error)
	GetStorageBackupRetention(context.Context, string, string) (*domain.HostingStorageBackupRetentionPolicy, error)
	SaveStorageBackupRetention(context.Context, string, string, domain.HostingStorageBackupRetentionInput) (domain.HostingStorageBackupRetentionPolicy, error)
	PreviewStorageBackupRetention(context.Context, string, string, string, domain.HostingStorageBackupRetentionPreviewInput) (domain.HostingStorageBackupRetentionPreview, error)
	ExecuteStorageBackupRetention(context.Context, string, string, string, domain.HostingStorageBackupRetentionExecutionInput) (domain.HostingStorageBackupRetentionExecution, error)
	ListStorageBackupRetentionExecutions(context.Context, string, string) ([]domain.HostingStorageBackupRetentionExecution, error)
	GetStorageBackupRetentionExecution(context.Context, string, string, string) (domain.HostingStorageBackupRetentionExecution, error)
	StorageBackupRetentionPlan(context.Context, string, domain.HostingAuthority) (domain.HostingStorageBackupRetentionPlan, error)
	StorageBackupRetentionMutation(context.Context, string, domain.HostingStorageBackupRetentionMutation) (domain.HostingStorageBackupRetentionExecution, error)
}

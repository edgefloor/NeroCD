package store

import (
	"context"

	"nerocd/internal/domain"
)

type HostingDatabaseBackupRetentionRepository interface {
	DatabaseBackupRetentionRetirement(context.Context, string, domain.HostingDatabaseBackupRetentionRetirementInput) (domain.HostingDatabaseBackupRetentionRetirement, error)
	GetDatabaseBackupRetention(context.Context, string, string) (*domain.HostingDatabaseBackupRetentionPolicy, error)
	SaveDatabaseBackupRetention(context.Context, string, string, domain.HostingDatabaseBackupRetentionInput) (domain.HostingDatabaseBackupRetentionPolicy, error)
	PreviewDatabaseBackupRetention(context.Context, string, string, string, domain.HostingDatabaseBackupRetentionPreviewInput) (domain.HostingDatabaseBackupRetentionPreview, error)
	ExecuteDatabaseBackupRetention(context.Context, string, string, string, domain.HostingDatabaseBackupRetentionExecutionInput) (domain.HostingDatabaseBackupRetentionExecution, error)
	ListDatabaseBackupRetentionExecutions(context.Context, string, string) ([]domain.HostingDatabaseBackupRetentionExecution, error)
	GetDatabaseBackupRetentionExecution(context.Context, string, string, string) (domain.HostingDatabaseBackupRetentionExecution, error)
	DatabaseBackupRetentionPlan(context.Context, string, domain.HostingAuthority) (domain.HostingDatabaseBackupRetentionPlan, error)
	DatabaseBackupRetentionMutation(context.Context, string, domain.HostingDatabaseBackupRetentionMutation) (domain.HostingDatabaseBackupRetentionExecution, error)
}

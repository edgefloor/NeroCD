package store

import (
	"context"

	"nerocd/internal/domain"
)

// HostingDatabaseImportRepository keeps external input separate from owned backups.
type HostingDatabaseImportRepository interface {
	RequestDatabaseImportStage(context.Context, string, string, string, domain.HostingDatabaseImportStageInput) (domain.HostingDatabaseImportStage, error)
	GetDatabaseImportStage(context.Context, string, string) (domain.HostingDatabaseImportStage, error)
	RequestDatabaseImport(context.Context, string, string, string, domain.HostingDatabaseImportInput) (domain.HostingDatabaseImportOperation, error)
	GetDatabaseImport(context.Context, string, string) (domain.HostingDatabaseImportOperation, error)
	DatabaseImportStagePlan(context.Context, string, domain.HostingAuthority) (domain.HostingDatabaseImportStagePlan, error)
	DatabaseImportPlan(context.Context, string, domain.HostingAuthority) (domain.HostingDatabaseImportPlan, error)
	DatabaseImportMutation(context.Context, string, domain.HostingDatabaseImportMutation) (domain.HostingDatabaseImportReceiptResult, error)
	AuthorizeDatabaseImportCredentials(context.Context, string, string, domain.BackupRemoteCredentialRequest) (BackupRemoteCredentialAuthorization, error)
	RevalidateDatabaseImportCredentials(context.Context, string, string, domain.BackupRemoteCredentialRequest, domain.BackupRemoteCredentialMetadata) error
}

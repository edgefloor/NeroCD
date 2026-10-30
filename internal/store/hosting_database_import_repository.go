package store

import (
	"context"

	"nerocd/internal/domain"
)

// HostingDatabaseImportRepository keeps external input separate from owned backups.
type HostingDatabaseImportRepository interface {
	RequestDatabaseImportUploadStage(context.Context, string, string, string, domain.HostingDatabaseImportUploadInput) (domain.HostingDatabaseImportStage, error)
	DatabaseImportUpload(context.Context, string, string, string, bool) (domain.HostingDatabaseImportStagePlan, domain.HostingDatabaseImportStage, bool, error)
	RevalidateDatabaseImportUpload(context.Context, string, domain.HostingDatabaseImportStagePlan) error
	RequestDatabaseImportStage(context.Context, string, string, string, domain.HostingDatabaseImportStageInput) (domain.HostingDatabaseImportStage, error)
	GetDatabaseImportStage(context.Context, string, string) (domain.HostingDatabaseImportStage, error)
	RequestDatabaseImport(context.Context, string, string, string, domain.HostingDatabaseImportInput) (domain.HostingDatabaseImportOperation, error)
	GetDatabaseImport(context.Context, string, string) (domain.HostingDatabaseImportOperation, error)
	DatabaseImportStagePlan(context.Context, string, domain.HostingAuthority) (domain.HostingDatabaseImportStagePlan, error)
	DatabaseImportPlan(context.Context, string, domain.HostingAuthority) (domain.HostingDatabaseImportPlan, error)
	DatabaseImportMutation(context.Context, string, domain.HostingDatabaseImportMutation) (domain.HostingDatabaseImportReceiptResult, error)
	AuthorizeDatabaseImportCredentials(context.Context, string, string, domain.BackupRemoteCredentialRequest) (BackupRemoteCredentialAuthorization, error)
	RevalidateDatabaseImportCredentials(context.Context, string, string, domain.BackupRemoteCredentialRequest, domain.BackupRemoteCredentialMetadata) error
	AuthorizeDatabaseImportStageCleanup(context.Context, string, domain.HostingDatabaseImportStageCleanup) (domain.HostingDatabaseImportStageCleanupResult, error)
}

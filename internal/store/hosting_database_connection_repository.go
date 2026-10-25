package store

import (
	"context"
	"errors"

	"nerocd/internal/domain"
)

// ErrHostingDatabaseConnectionUnavailable reports an unsupported controller backend.
var ErrHostingDatabaseConnectionUnavailable = errors.New("hosting_database_connection_unavailable")

// HostingDatabaseConnectionRepository owns atomic admission and durable workflow.
// Every identity and reference is reauthorized in the persistence transaction.
type HostingDatabaseConnectionRepository interface {
	ConnectHostingApplicationDatabase(context.Context, string, string, string, domain.HostingDatabaseConnectionInput, domain.AuditEvent) (domain.HostingDatabaseConnection, error)
	RequestHostingApplicationDatabaseDeployment(context.Context, string, string, string, domain.HostingDeploymentInput, domain.AuditEvent) (domain.HostingOperation, error)
	RequestHostingDatabaseMaintenance(context.Context, string, string, string, domain.HostingDatabaseMaintenanceInput, domain.AuditEvent) (domain.HostingOperation, error)
	GetHostingDatabaseConnection(context.Context, string, string) (domain.HostingDatabaseConnection, error)
	ListEligibleHostingDatabases(context.Context, string, string) ([]domain.HostingDatabaseConnectionOption, error)
	RequestHostingDatabaseConnectionRecovery(context.Context, string, string, string, domain.HostingDatabaseConnectionRecoveryInput, domain.AuditEvent) (domain.HostingOperation, error)
}

// HostingApplicationDatabaseRunnerRepository separates immutable acknowledgements
// and read-only state from explicit, current effect authorization.
type HostingApplicationDatabaseRunnerRepository interface {
	HostingApplicationDatabaseStage(context.Context, string, string, domain.HostingApplicationDatabaseStageRequest) (domain.HostingOperation, error)
	HostingApplicationDatabaseRetirementPlan(context.Context, string, string, domain.HostingAuthority) (domain.HostingDatabaseBindingEndpointPlan, error)
	HostingApplicationDatabaseRetirement(context.Context, string, string, domain.HostingApplicationDatabaseRetirementRequest) (domain.HostingOperation, error)
	AuthorizeHostingApplicationDatabaseEffect(context.Context, string, string, domain.HostingApplicationDatabaseEffectRequest) (domain.HostingApplicationDatabaseEffectAuthorization, error)
	GetHostingApplicationDatabaseState(context.Context, string, string, domain.HostingAuthority) (domain.HostingApplicationDatabaseExecutionState, error)
}

// HostingApplicationDatabaseYieldRepository closes prepare under the original tuple.
type HostingApplicationDatabaseYieldRepository interface {
	HostingApplicationDatabaseYield(context.Context, string, string, domain.HostingApplicationDatabaseYieldRequest) (domain.HostingOperation, error)
}

// HostingApplicationDatabaseStorageRunnerRepository admits only the composed writer protocol.
type HostingApplicationDatabaseStorageRunnerRepository interface {
	HostingApplicationDatabaseStorageWriter(context.Context, string, string, domain.HostingApplicationDatabaseStorageWriterRequest) (domain.HostingOperation, error)
	AuthorizeHostingApplicationDatabaseStorageEffect(context.Context, string, string, domain.HostingApplicationDatabaseStorageEffectRequest) (domain.HostingApplicationDatabaseStorageEffectAuthorization, error)
	HostingApplicationDatabaseStorageProxyPlan(context.Context, string, string, domain.HostingAuthority) (domain.HostingApplicationDatabaseStorageProxyPlan, error)
}

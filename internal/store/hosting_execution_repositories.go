package store

import (
	"context"
	"time"

	"nerocd/internal/domain"
)

// HostingExecutionRepository owns the transactional image control plane. Its
// PostgreSQL implementation enqueues real River work in the request transaction.
// Memory mode deliberately does not simulate external deployment authority.
type HostingExecutionRepository interface {
	RequestHostingReleaseCleanup(context.Context, string, string, string, string, domain.HostingCleanupInput, domain.AuditEvent) (domain.HostingOperation, error)
	ListHostingRetainedReleases(context.Context, string) ([]domain.HostingRetainedRelease, error)
	ListHostingOperations(context.Context, string, Page) (PageResult[domain.HostingOperation], error)
	ClaimHostingObservation(context.Context, string, domain.HostingAuthority, time.Duration) (domain.HostingOperation, error)
	RequestHostingDeployment(context.Context, string, string, string, domain.HostingDeploymentInput, domain.AuditEvent) (domain.HostingOperation, error)
	GetHostingOperation(context.Context, string) (domain.HostingOperation, error)
	CancelHostingOperation(context.Context, string, domain.AuditEvent) (domain.HostingOperation, error)
	RequestHostingReconciliation(context.Context, string, domain.AuditEvent) (domain.HostingOperation, error)
	HostingStatus(context.Context, string, domain.HostingStatusRequest) (domain.HostingOperation, error)
	HostingRunnerPlan(context.Context, string, domain.HostingAuthority) (domain.HostingOperation, error)
	HostingPrepared(context.Context, string, domain.HostingPreparedRequest) (domain.HostingOperation, error)
	HostingApplyBegin(context.Context, string, domain.HostingAuthority) (domain.HostingOperation, error)
	HostingComplete(context.Context, string, domain.HostingCompleteRequest) (domain.HostingOperation, error)
	HostingReconciliation(context.Context, string, domain.HostingObservation) (domain.HostingOperation, error)
}

var _ HostingExecutionRepository = (*PostgresStore)(nil)

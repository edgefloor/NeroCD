package store

import (
	"context"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/observability"
)

type ProjectRepository interface {
	ListProjects(context.Context) ([]domain.Project, error)
	CreateProject(context.Context, domain.Project) (domain.Project, error)
	CreateProjectWithOwner(context.Context, domain.Project, domain.ProjectMember, domain.AuditEvent) (domain.Project, error)
	UpdateProject(context.Context, domain.Project) (domain.Project, error)
	UpdateProjectWithAudit(context.Context, domain.Project, domain.AuditEvent) (domain.Project, error)
	ArchiveProject(context.Context, string, time.Time) (domain.Project, error)
	ArchiveProjectWithAudit(context.Context, string, time.Time, domain.AuditEvent) (domain.Project, error)
}

type ProjectMemberRepository interface {
	ListProjectMembers(context.Context, string) ([]domain.ProjectMember, error)
	UpsertProjectMember(context.Context, domain.ProjectMember) (domain.ProjectMember, error)
	UpsertProjectMemberWithAudit(context.Context, domain.ProjectMember, domain.AuditEvent) (domain.ProjectMember, error)
}

type TemplateRepository interface {
	ListTemplates(context.Context, string) ([]domain.TaskTemplate, error)
	GetTemplate(context.Context, string) (domain.TaskTemplate, error)
	CreateTemplate(context.Context, domain.TaskTemplate) (domain.TaskTemplate, error)
	CreateTemplateWithAudit(context.Context, domain.TaskTemplate, domain.AuditEvent) (domain.TaskTemplate, error)
	UpdateTemplate(context.Context, domain.TaskTemplate) (domain.TaskTemplate, error)
	UpdateTemplateWithAudit(context.Context, domain.TaskTemplate, domain.AuditEvent) (domain.TaskTemplate, error)
}

type SourceRepository interface {
	ListRepositories(context.Context, string) ([]domain.Repository, error)
	CreateRepository(context.Context, domain.Repository) (domain.Repository, error)
	CreateRepositoryWithAudit(context.Context, domain.Repository, domain.AuditEvent) (domain.Repository, error)
	ConfigureRepositoryPolicy(context.Context, RepositoryPolicyConfiguration) (domain.Repository, error)
	ListAccessKeys(context.Context, string) ([]domain.AccessKey, error)
	CreateAccessKey(context.Context, domain.AccessKey) (domain.AccessKey, error)
	CreateAccessKeyWithAudit(context.Context, domain.AccessKey, domain.AuditEvent) (domain.AccessKey, error)
	ListInventories(context.Context, string) ([]domain.Inventory, error)
	CreateInventory(context.Context, domain.Inventory) (domain.Inventory, error)
	CreateInventoryWithAudit(context.Context, domain.Inventory, domain.AuditEvent) (domain.Inventory, error)
}

// RepositoryPolicyConfiguration is the one-shot, idempotent policy admission
// operation. PolicyHash is the SHA-256 of its canonical JSON representation;
// the policy itself is never retained in the receipt table.
type RepositoryPolicyConfiguration struct {
	RepositoryID    string
	ProjectID       string
	ActorID         string
	ConfigurationID string
	Policy          domain.RepositoryPolicy
	PolicyHash      string
	Audit           domain.AuditEvent
}

type RunRepository interface {
	ListRuns(context.Context, string) ([]domain.TaskRun, error)
	ListRunsPage(context.Context, string, Page) (PageResult[domain.TaskRun], error)
	ListRunLogs(context.Context, string) ([]domain.RunLog, error)
	ListRunLogsPage(context.Context, string, Page) (PageResult[domain.RunLog], error)
	ListArtifacts(context.Context, string) ([]domain.ArtifactRecord, error)
	ListArtifactsPage(context.Context, string, Page) (PageResult[domain.ArtifactRecord], error)
	CreateRun(context.Context, domain.TaskRun) (domain.TaskRun, error)
	CreateRunRequest(context.Context, domain.TaskRun, domain.RunLog, *domain.Approval, domain.AuditEvent) (domain.TaskRun, error)
	UpdateRunStatus(context.Context, string, string, *time.Time) (domain.TaskRun, error)
	UpdateRunWorkflowState(context.Context, string, domain.WorkflowState) (domain.TaskRun, error)
	CreateRunLog(context.Context, domain.RunLog) error
	CreateArtifact(context.Context, domain.ArtifactRecord) error
	CreateRunLogForLease(context.Context, domain.RunLog, string, string, int, string, time.Time) (domain.RunLog, error)
	CreateRunLogsForLease(context.Context, []domain.RunLog, string, string, string, int, string, time.Time) ([]domain.RunLog, error)
	CreateArtifactForLease(context.Context, domain.ArtifactRecord, string, int, string, time.Time) error
}

// RunLogRetentionRepository is a deep manual-maintenance seam.  Its adapters
// own DB-clock cutoffs, terminal/lease admission, replay receipts, batching,
// and the single immutable deletion audit; callers never enumerate logs.
type RunLogRetentionRepository interface {
	GetRunLogRetentionPolicy(context.Context) (domain.RunLogRetentionPolicy, error)
	UpdateRunLogRetentionPolicy(context.Context, domain.RunLogRetentionPolicy) (domain.RunLogRetentionPolicy, error)
	UpdateRunLogRetentionPolicyWithAudit(context.Context, domain.RunLogRetentionPolicy, domain.AuditEvent) (domain.RunLogRetentionPolicy, error)
	PreviewRunLogRetention(context.Context) (domain.RunLogRetentionPreview, error)
	ExecuteRunLogRetention(context.Context, string, string, domain.AuditEvent) (domain.RunLogRetentionExecution, error)
}

type RunnerRepository interface {
	ListRunners(context.Context) ([]domain.Runner, error)
	GetRunnerByID(context.Context, string) (domain.Runner, error)
	RegisterRunner(context.Context, domain.Runner) (domain.Runner, error)
	RegisterRunnerWithAudit(context.Context, domain.Runner, domain.AuditEvent) (domain.Runner, error)
	UpdateRunnerToken(context.Context, string, string, string, time.Time) (domain.Runner, error)
	UpdateRunnerTokenWithAudit(context.Context, string, string, string, time.Time, domain.AuditEvent) (domain.Runner, error)
	GetRunnerByTokenHash(context.Context, string) (domain.Runner, error)
	HeartbeatRunner(context.Context, string, time.Time) (domain.Runner, error)
	ClaimRun(context.Context, string, time.Time, time.Duration) (domain.ClaimedRun, error)
	// ClaimRunWithAudit records the successful claim and its evidence under the
	// same store transaction/lock.  A zero audit is useful to low-level callers
	// that deliberately do not create operator evidence.
	ClaimRunWithAudit(context.Context, string, time.Time, time.Duration, domain.AuditEvent) (domain.ClaimedRun, error)
	ExpireLeases(context.Context, time.Time) error
	RenewLease(context.Context, string, string, string, int, time.Time, time.Duration) (domain.RunLease, error)
	CompleteLeaseRequest(context.Context, string, string, string, int, string, string, time.Time, string, *time.Time, *domain.WorkflowState, []domain.RunLog, domain.AuditEvent) (domain.RunLease, error)
	CancelRunRequest(context.Context, string, time.Time, domain.RunLog, domain.AuditEvent) (domain.TaskRun, error)
	ActiveLeaseForRun(context.Context, string) (domain.RunLease, error)
	GetLeaseForRunner(context.Context, string, string) (domain.RunLease, error)
	GetLeaseForCompletion(context.Context, string, string, int, string) (domain.RunLease, error)
	AuthorizeSecretAccess(context.Context, domain.SecretAccessRequest) (domain.SecretAccessGrant, error)
	CreateRunnerEnrollment(context.Context, domain.RunnerEnrollment, domain.AuditEvent) (domain.RunnerEnrollment, error)
	RevokeRunnerEnrollment(context.Context, string, domain.AuditEvent) (domain.RunnerEnrollment, error)
	ConsumeRunnerEnrollment(context.Context, domain.RunnerEnrollmentConsume, domain.AuditEvent) (domain.Runner, error)
}

type UserRepository interface {
	GetUserByEmail(context.Context, string) (domain.User, error)
	UpdatePasswordHash(context.Context, string, string, string) error
}

// BootstrapRepository performs the one-time identity creation and its audit in
// one store operation. It intentionally has no update or reset behavior.
type BootstrapRepository interface {
	BootstrapAdmin(context.Context, domain.User, domain.AuditEvent) error
	BootstrapComplete(context.Context) (bool, error)
}

type SessionRepository interface {
	CreateSession(context.Context, domain.Session, string) error
	CreateSessionWithAudit(context.Context, domain.Session, string, domain.AuditEvent) error
	GetPrincipalBySessionTokenHash(context.Context, string, time.Time) (domain.User, error)
	RevokeSessionByTokenHash(context.Context, string, time.Time) error
	RevokeSessionByTokenHashWithAudit(context.Context, string, time.Time, domain.AuditEvent) error
	ListSessions(context.Context) ([]domain.Session, error)
	RevokeSessionByID(context.Context, string, time.Time) (domain.Session, error)
	RevokeSessionByIDWithAudit(context.Context, string, time.Time, domain.AuditEvent) (domain.Session, error)
}

// SessionLastSeenUpdateInterval bounds session activity writes. Authentication
// remains valid on every request; only durable activity telemetry is sampled.
const SessionLastSeenUpdateInterval = 5 * time.Minute

type APITokenRepository interface {
	CreateAPIToken(context.Context, domain.APIToken) (domain.APIToken, error)
	CreateAPITokenWithAudit(context.Context, domain.APIToken, domain.AuditEvent) (domain.APIToken, error)
	GetAPITokenByHash(context.Context, string, time.Time) (domain.APIToken, error)
	RevokeAPIToken(context.Context, string, time.Time) (domain.APIToken, error)
	RevokeAPITokenWithAudit(context.Context, string, time.Time, domain.AuditEvent) (domain.APIToken, error)
}

type ApprovalRepository interface {
	ListApprovals(context.Context, string) ([]domain.Approval, error)
	CreateApproval(context.Context, domain.Approval) (domain.Approval, error)
	ApproveRun(context.Context, string, string, time.Time) (domain.Approval, error)
	ApproveRunWithAudit(context.Context, string, string, time.Time, domain.AuditEvent) (domain.Approval, error)
	RejectRun(context.Context, string, string, time.Time) (domain.Approval, error)
	RejectRunWithAudit(context.Context, string, string, time.Time, domain.AuditEvent) (domain.Approval, error)
}

type AuditRepository interface {
	ListAuditEvents(context.Context) ([]domain.AuditEvent, error)
	ListAuditEventsPage(context.Context, Page) (PageResult[domain.AuditEvent], error)
	CreateAuditEvent(context.Context, domain.AuditEvent) error
}

type DeploymentRepository interface {
	ListServices(context.Context, string) ([]domain.Service, error)
	GetService(context.Context, string) (domain.Service, error)
	CreateService(context.Context, domain.Service) (domain.Service, error)
	CreateServiceWithAudit(context.Context, domain.Service, domain.AuditEvent) (domain.Service, error)
	ListEnvironments(context.Context, string) ([]domain.Environment, error)
	GetEnvironment(context.Context, string) (domain.Environment, error)
	CreateEnvironment(context.Context, domain.Environment) (domain.Environment, error)
	CreateEnvironmentWithAudit(context.Context, domain.Environment, domain.AuditEvent) (domain.Environment, error)
	ListRevisions(context.Context, string) ([]domain.Revision, error)
	CreateRevision(context.Context, domain.Revision) (domain.Revision, error)
	CreateRevisionWithAudit(context.Context, domain.Revision, domain.AuditEvent) (domain.Revision, error)
	ListDeployments(context.Context, string) ([]domain.Deployment, error)
	GetDeployment(context.Context, string) (domain.Deployment, error)
	CreateDeploymentRequest(context.Context, domain.Deployment, domain.TaskRun, domain.AuditEvent) (domain.Deployment, error)
	ConfirmDeployment(context.Context, string, string, domain.AuditEvent) (domain.Deployment, error)
	FailPreAssignmentDeployment(context.Context, string, string, domain.AuditEvent) (domain.Deployment, error)
	TransitionDeploymentAttempt(context.Context, domain.DeploymentTransitionRequest, domain.AuditEvent) (domain.Deployment, error)
	FailDeploymentAndCreateRollback(context.Context, domain.DeploymentFailureRollbackRequest, domain.AuditEvent, domain.AuditEvent) (domain.DeploymentFailureRollbackResult, error)
	CancelDeploymentRequest(context.Context, domain.DeploymentCancelRequest, domain.AuditEvent) (domain.Deployment, error)
	DeploymentPlan(context.Context, string, string, string, string, int, string) (domain.DeploymentPlan, error)
	ResolveRevisionProvenance(context.Context, string, string, string, string, int, string, string, string, string, []string, domain.AuditEvent) (domain.Revision, error)
}

// OperationalSnapshotRepository is a read-only aggregate boundary. It returns
// no operator identifiers or arbitrary metadata, which keeps metrics labels
// fixed even when the underlying database contains unbounded user data.
type OperationalSnapshotRepository interface {
	OperationalSnapshot(context.Context) (observability.Snapshot, error)
}

type OperationalObservationWriter interface {
	RecordRunnerOperationalObservation(context.Context, string, int, int, int) error
}

// RunnerOperationalObservation is the bounded latest state that may be shown
// to a global runner administrator. It intentionally has no runner supplied
// labels, diagnostic text, paths, or credential material.
type RunnerOperationalObservation struct {
	ObservedAt    time.Time
	JournalDepth  int
	RetryCount    int
	RenewFailures int
}

type OperationalObservationReader interface {
	RunnerOperationalObservation(context.Context, string) (RunnerOperationalObservation, error)
}

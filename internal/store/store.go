package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/observability"
)

// Claiming examines at most this many candidates per request. Workflow
// readiness lives in Go, so bounded keyset scans retain that exact behavior
// without allowing an incompatible queue prefix to starve later work.
const (
	claimCandidateBatchSize  = 32
	claimCandidateMaxBatches = 8
	claimCandidateLimit      = claimCandidateBatchSize * claimCandidateMaxBatches
	// MaxPageLimit is enforced below the HTTP boundary too, so callers such as
	// workers and future transports cannot accidentally turn a list operation
	// into an unbounded database read.
	MaxPageLimit  = 100
	MaxPageOffset = 100_000
)

type memoryClaimCursor struct {
	claimOrderAt time.Time
	runID        string
}

func stringPointer(value string) *string { return &value }

// auditMetadata copies caller metadata before adding store-authoritative facts.
// It prevents a caller from retaining a map that later appears to change under
// it, while ensuring lease and approval identifiers come from the committed
// state rather than an API request.
func auditMetadata(metadata map[string]any, authoritative map[string]any) map[string]any {
	result := make(map[string]any, len(metadata)+len(authoritative))
	for key, value := range metadata {
		result[key] = value
	}
	for key, value := range authoritative {
		result[key] = value
	}
	return result
}

func (s *MemoryStore) auditIDAvailableLocked(id string) bool {
	if id == "" {
		return true
	}
	for _, event := range s.auditEvents {
		if event.ID == id {
			return false
		}
	}
	return true
}

func immutableGitCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

type Page struct {
	Limit   int
	Offset  int
	Enabled bool
}

type PageResult[T any] struct {
	Items  []T
	Limit  int
	Offset int
	Total  int
}

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

type MemoryStore struct {
	mu                    sync.RWMutex
	users                 []domain.User
	sessions              []domain.Session
	apiTokens             []domain.APIToken
	tokenHashBySessionID  map[string]string
	projects              []domain.Project
	templates             []domain.TaskTemplate
	repositories          []domain.Repository
	accessKeys            []domain.AccessKey
	inventories           []domain.Inventory
	projectMembers        []domain.ProjectMember
	runs                  []domain.TaskRun
	runners               []domain.Runner
	runnerEnrollments     []domain.RunnerEnrollment
	leases                []domain.RunLease
	claimCursors          map[string]memoryClaimCursor
	claimOrderByRun       map[string]time.Time
	nextAttemptByRun      map[string]int
	logs                  []domain.RunLog
	artifacts             []domain.ArtifactRecord
	approvals             []domain.Approval
	auditEvents           []domain.AuditEvent
	services              []domain.Service
	serviceByID           map[string]int
	environments          []domain.Environment
	environmentByID       map[string]int
	revisions             []domain.Revision
	deployments           []domain.Deployment
	deploymentByID        map[string]int
	deploymentAttempts    []domain.DeploymentAttempt
	deploymentTransitions map[string]domain.DeploymentTransitionRequest
	deploymentCancels     map[string]domain.DeploymentCancelRequest
	provenanceReplays     map[string]memoryProvenanceReplay
	policyConfigurations  map[string]memoryPolicyConfiguration
	runnerObservations    map[string]memoryRunnerObservation
	retentionPolicy       domain.RunLogRetentionPolicy
	retentionReceipts     map[string]domain.RunLogRetentionExecution
}

type memoryPolicyConfiguration struct{ actorID, policyHash string }
type memoryRunnerObservation struct {
	observedAt                              time.Time
	journalDepth, retryCount, renewFailures int
}

type memoryProvenanceReplay struct {
	deploymentID, resolutionID, runID, leaseID, runnerID, fence string
	attempt                                                     int
	commit, hash                                                string
	digests                                                     []string
	revision                                                    domain.Revision
}

// NewMemoryStore deliberately starts empty. It is useful for isolated tests
// and explicitly requested disposable development sessions, but it never
// creates an administrator or any credentials on its own.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		tokenHashBySessionID:  map[string]string{},
		claimCursors:          map[string]memoryClaimCursor{},
		claimOrderByRun:       map[string]time.Time{},
		policyConfigurations:  map[string]memoryPolicyConfiguration{},
		nextAttemptByRun:      map[string]int{},
		deploymentTransitions: map[string]domain.DeploymentTransitionRequest{},
		deploymentCancels:     map[string]domain.DeploymentCancelRequest{},
		provenanceReplays:     map[string]memoryProvenanceReplay{},
		runnerObservations:    map[string]memoryRunnerObservation{},
		retentionReceipts:     map[string]domain.RunLogRetentionExecution{},
		retentionPolicy:       domain.RunLogRetentionPolicy{KeepDays: 30, BatchSize: 1000, Version: 1},
		serviceByID:           map[string]int{},
		environmentByID:       map[string]int{},
		deploymentByID:        map[string]int{},
	}
}

// OperationalSnapshot mirrors the PostgreSQL aggregate contract for explicit
// development/test memory stores. It deliberately reads under one lock so a
// scrape cannot combine queue and lease state from different mutations.
func (s *MemoryStore) OperationalSnapshot(_ context.Context) (observability.Snapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()
	result := observability.Snapshot{
		CollectedAt:          now,
		TerminalRuns:         map[string]observability.DurationAggregate{},
		Deployments:          map[string]int64{},
		BackupOutcome:        observability.BackupNone,
		BackupReason:         "none",
		BackupScheduleStatus: "disabled",
	}
	var oldestQueue *time.Time
	for _, run := range s.runs {
		switch run.Status {
		case domain.RunQueued, domain.RunWaitingApproval:
			result.QueueDepth++
			if oldestQueue == nil || run.StartedAt.Before(*oldestQueue) {
				v := run.StartedAt
				oldestQueue = &v
			}
		case domain.RunSucceeded, domain.RunFailed, domain.RunCanceled:
			v := result.TerminalRuns[run.Status]
			v.Count++
			if run.FinishedAt != nil && run.FinishedAt.After(run.StartedAt) {
				v.SumSeconds += run.FinishedAt.Sub(run.StartedAt).Seconds()
			}
			result.TerminalRuns[run.Status] = v
		}
	}
	if oldestQueue != nil {
		result.QueueOldestAgeSeconds = nonNegativeAge(now, *oldestQueue)
	}
	var oldestHeartbeat *time.Time
	for _, runner := range s.runners {
		if oldestHeartbeat == nil || runner.LastHeartbeatAt.Before(*oldestHeartbeat) {
			v := runner.LastHeartbeatAt
			oldestHeartbeat = &v
		}
	}
	if oldestHeartbeat != nil {
		result.OldestRunnerHeartbeatSecond = nonNegativeAge(now, *oldestHeartbeat)
	}
	for _, lease := range s.leases {
		if lease.Status == domain.LeaseActive {
			result.ActiveLeases++
		}
		if lease.Status == domain.LeaseExpired {
			result.ExpiredLeases++
		}
	}
	for _, deployment := range s.deployments {
		result.Deployments[string(deployment.Status)]++
		if deployment.HealthPassed != nil {
			if *deployment.HealthPassed {
				result.DeploymentHealthPassed++
			} else {
				result.DeploymentHealthFailed++
			}
		}
		if deployment.RollbackOfID != nil {
			if deployment.Status == "rolled_back" {
				result.RollbackSucceeded++
			}
			if deployment.Status == "rollback_failed" {
				result.RollbackFailed++
			}
		}
	}
	for _, observation := range s.runnerObservations {
		result.RunnerJournalDepth += int64(observation.journalDepth)
		result.RunnerRetryCount += int64(observation.retryCount)
		result.RunnerRenewFailures += int64(observation.renewFailures)
	}
	return result, nil
}

func (s *MemoryStore) RecordRunnerOperationalObservation(_ context.Context, runnerID string, journalDepth, retryCount, renewFailures int) error {
	if runnerID == "" || journalDepth < 0 || journalDepth > 8192 || retryCount < 0 || retryCount > 100000 || renewFailures < 0 || renewFailures > 100000 {
		return ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, r := range s.runners {
		if r.ID == runnerID {
			found = true
			break
		}
	}
	if !found {
		return ErrNotFound
	}
	s.runnerObservations[runnerID] = memoryRunnerObservation{observedAt: time.Now().UTC(), journalDepth: journalDepth, retryCount: retryCount, renewFailures: renewFailures}
	return nil
}

func (s *MemoryStore) RunnerOperationalObservation(_ context.Context, runnerID string) (RunnerOperationalObservation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	observation, ok := s.runnerObservations[runnerID]
	if !ok {
		return RunnerOperationalObservation{}, ErrNotFound
	}
	return RunnerOperationalObservation{ObservedAt: observation.observedAt, JournalDepth: observation.journalDepth, RetryCount: observation.retryCount, RenewFailures: observation.renewFailures}, nil
}

func nonNegativeAge(now, then time.Time) float64 {
	if then.IsZero() || now.Before(then) {
		return 0
	}
	return now.Sub(then).Seconds()
}

func (s *MemoryStore) GetUserByEmail(_ context.Context, email string) (domain.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, user := range s.users {
		if user.Email == email {
			return user, nil
		}
	}
	return domain.User{}, ErrNotFound
}

func (s *MemoryStore) UpdatePasswordHash(_ context.Context, userID, previousHash, passwordHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.users {
		if s.users[i].ID == userID && s.users[i].PasswordHash == previousHash {
			s.users[i].PasswordHash = passwordHash
			return nil
		}
	}
	return ErrConflict
}

func (s *MemoryStore) BootstrapAdmin(_ context.Context, user domain.User, audit domain.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.users) != 0 {
		s.auditEvents = append([]domain.AuditEvent{{ID: audit.ID + "-denied", ActorID: "system", Action: "identity.bootstrap_admin.denied", TargetID: "bootstrap-admin", Metadata: map[string]any{"reason": "already_completed"}, CreatedAt: audit.CreatedAt}}, s.auditEvents...)
		return ErrConflict
	}
	s.users = append(s.users, user)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return nil
}

// BootstrapComplete intentionally exposes only the one fixed lifecycle bit.
// It is safe to use before authentication and never reveals an administrator
// identity, address, timestamp, or migration detail.
func (s *MemoryStore) BootstrapComplete(_ context.Context) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.users) != 0, nil
}

func (s *MemoryStore) CreateSession(_ context.Context, session domain.Session, tokenHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, session)
	s.tokenHashBySessionID[session.ID] = tokenHash
	return nil
}

func (s *MemoryStore) CreateSessionWithAudit(_ context.Context, session domain.Session, tokenHash string, audit domain.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions = append(s.sessions, session)
	s.tokenHashBySessionID[session.ID] = tokenHash
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return nil
}

func (s *MemoryStore) GetPrincipalBySessionTokenHash(_ context.Context, tokenHash string, now time.Time) (domain.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, session := range s.sessions {
		if s.tokenHashBySessionID[session.ID] != tokenHash {
			continue
		}
		if session.RevokedAt != nil || !session.ExpiresAt.After(now) {
			return domain.User{}, ErrNotFound
		}
		threshold := now.Add(-SessionLastSeenUpdateInterval)
		if session.LastSeenAt == nil || !session.LastSeenAt.After(threshold) {
			seen := now
			session.LastSeenAt = &seen
			s.sessions[i] = session
		}
		for _, user := range s.users {
			if user.ID == session.UserID && user.Status == domain.UserActive {
				return user, nil
			}
		}
		return domain.User{}, ErrNotFound
	}
	return domain.User{}, ErrNotFound
}

func (s *MemoryStore) RevokeSessionByTokenHash(_ context.Context, tokenHash string, revokedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, session := range s.sessions {
		if s.tokenHashBySessionID[session.ID] == tokenHash {
			session.RevokedAt = &revokedAt
			s.sessions[i] = session
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) RevokeSessionByTokenHashWithAudit(_ context.Context, tokenHash string, revokedAt time.Time, audit domain.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, session := range s.sessions {
		if s.tokenHashBySessionID[session.ID] == tokenHash && session.RevokedAt == nil {
			session.RevokedAt = &revokedAt
			s.sessions[i] = session
			s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
			return nil
		}
	}
	return ErrNotFound
}

func (s *MemoryStore) ListSessions(_ context.Context) ([]domain.Session, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := append([]domain.Session(nil), s.sessions...)
	for i := range result {
		if result[i].LastSeenAt != nil {
			value := *result[i].LastSeenAt
			result[i].LastSeenAt = &value
		}
		if result[i].RevokedAt != nil {
			value := *result[i].RevokedAt
			result[i].RevokedAt = &value
		}
	}
	return result, nil
}

func (s *MemoryStore) RevokeSessionByID(_ context.Context, id string, revokedAt time.Time) (domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.sessions {
		if s.sessions[i].ID == id && s.sessions[i].RevokedAt == nil {
			s.sessions[i].RevokedAt = &revokedAt
			return s.sessions[i], nil
		}
	}
	return domain.Session{}, ErrNotFound
}

func (s *MemoryStore) RevokeSessionByIDWithAudit(_ context.Context, id string, revokedAt time.Time, audit domain.AuditEvent) (domain.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, session := range s.sessions {
		if session.ID == id && session.RevokedAt == nil {
			session.RevokedAt = &revokedAt
			s.sessions[i] = session
			s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
			return session, nil
		}
	}
	return domain.Session{}, ErrNotFound
}

func (s *MemoryStore) CreateAPIToken(_ context.Context, token domain.APIToken) (domain.APIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apiTokens = append(s.apiTokens, token)
	return token, nil
}

func (s *MemoryStore) CreateAPITokenWithAudit(_ context.Context, token domain.APIToken, audit domain.AuditEvent) (domain.APIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.apiTokens = append(s.apiTokens, token)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return token, nil
}

func (s *MemoryStore) GetAPITokenByHash(_ context.Context, tokenHash string, now time.Time) (domain.APIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, token := range s.apiTokens {
		if token.TokenHash != tokenHash || token.Status != domain.TokenActive || token.RevokedAt != nil {
			continue
		}
		if token.ExpiresAt != nil && !token.ExpiresAt.After(now) {
			return domain.APIToken{}, ErrNotFound
		}
		token.LastUsedAt = &now
		s.apiTokens[i] = token
		return token, nil
	}
	return domain.APIToken{}, ErrNotFound
}

func (s *MemoryStore) RevokeAPIToken(_ context.Context, tokenID string, revokedAt time.Time) (domain.APIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, token := range s.apiTokens {
		if token.ID == tokenID && token.RevokedAt == nil {
			token.Status = domain.TokenRevoked
			token.RevokedAt = &revokedAt
			s.apiTokens[i] = token
			return token, nil
		}
	}
	return domain.APIToken{}, ErrNotFound
}

func (s *MemoryStore) RevokeAPITokenWithAudit(_ context.Context, tokenID string, revokedAt time.Time, audit domain.AuditEvent) (domain.APIToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, token := range s.apiTokens {
		if token.ID == tokenID && token.RevokedAt == nil {
			token.Status, token.RevokedAt = domain.TokenRevoked, &revokedAt
			s.apiTokens[i] = token
			s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
			return token, nil
		}
	}
	return domain.APIToken{}, ErrNotFound
}

func (s *MemoryStore) ListProjects(context.Context) ([]domain.Project, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Project, 0, len(s.projects))
	for _, project := range s.projects {
		if project.ArchivedAt == nil {
			out = append(out, project)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateProject(_ context.Context, project domain.Project) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projects = append(s.projects, project)
	return project, nil
}

func (s *MemoryStore) CreateProjectWithOwner(_ context.Context, project domain.Project, owner domain.ProjectMember, audit domain.AuditEvent) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.projects {
		if existing.ID == project.ID {
			return domain.Project{}, ErrConflict
		}
	}
	if owner.ProjectID != project.ID || owner.UserID == "" || owner.Role != domain.RoleOwner {
		return domain.Project{}, ErrConflict
	}
	s.projects = append(s.projects, project)
	s.projectMembers = append(s.projectMembers, owner)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return project, nil
}

func (s *MemoryStore) UpdateProject(_ context.Context, project domain.Project) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.projects {
		if existing.ID == project.ID {
			project.CreatedAt = existing.CreatedAt
			project.ArchivedAt = existing.ArchivedAt
			s.projects[i] = project
			return project, nil
		}
	}
	return domain.Project{}, ErrNotFound
}

func (s *MemoryStore) UpdateProjectWithAudit(_ context.Context, project domain.Project, audit domain.AuditEvent) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.projects {
		if existing.ID == project.ID && existing.ArchivedAt == nil {
			project.CreatedAt = existing.CreatedAt
			s.projects[i] = project
			s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
			return project, nil
		}
	}
	return domain.Project{}, ErrNotFound
}

func (s *MemoryStore) ArchiveProject(_ context.Context, id string, archivedAt time.Time) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, project := range s.projects {
		if project.ID == id {
			project.ArchivedAt = &archivedAt
			s.projects[i] = project
			return project, nil
		}
	}
	return domain.Project{}, ErrNotFound
}

func (s *MemoryStore) ArchiveProjectWithAudit(_ context.Context, id string, archivedAt time.Time, audit domain.AuditEvent) (domain.Project, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, project := range s.projects {
		if project.ID == id && project.ArchivedAt == nil {
			project.ArchivedAt = &archivedAt
			s.projects[i] = project
			s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
			return project, nil
		}
	}
	return domain.Project{}, ErrNotFound
}

func (s *MemoryStore) ListProjectMembers(_ context.Context, projectID string) ([]domain.ProjectMember, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.ProjectMember, 0, len(s.projectMembers))
	for _, member := range s.projectMembers {
		if projectID == "" || member.ProjectID == projectID {
			out = append(out, member)
		}
	}
	return out, nil
}

func (s *MemoryStore) UpsertProjectMember(_ context.Context, member domain.ProjectMember) (domain.ProjectMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.projectMembers {
		if existing.ProjectID == member.ProjectID && existing.UserID == member.UserID {
			member.ID = existing.ID
			member.CreatedAt = existing.CreatedAt
			s.projectMembers[i] = member
			return member, nil
		}
	}
	s.projectMembers = append(s.projectMembers, member)
	return member, nil
}

func (s *MemoryStore) UpsertProjectMemberWithAudit(_ context.Context, member domain.ProjectMember, audit domain.AuditEvent) (domain.ProjectMember, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.projectMembers {
		if existing.ProjectID == member.ProjectID && existing.UserID == member.UserID {
			member.ID, member.CreatedAt = existing.ID, existing.CreatedAt
			s.projectMembers[i] = member
			s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
			return member, nil
		}
	}
	s.projectMembers = append(s.projectMembers, member)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return member, nil
}

func (s *MemoryStore) ListTemplates(_ context.Context, projectID string) ([]domain.TaskTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.TaskTemplate, 0, len(s.templates))
	for _, template := range s.templates {
		if projectID == "" || template.ProjectID == projectID {
			out = append(out, template)
		}
	}
	return out, nil
}

func (s *MemoryStore) GetTemplate(_ context.Context, id string) (domain.TaskTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, template := range s.templates {
		if template.ID == id {
			return template, nil
		}
	}
	return domain.TaskTemplate{}, ErrNotFound
}

func (s *MemoryStore) CreateTemplate(_ context.Context, template domain.TaskTemplate) (domain.TaskTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.templates = append(s.templates, template)
	return template, nil
}
func (s *MemoryStore) CreateTemplateWithAudit(_ context.Context, template domain.TaskTemplate, audit domain.AuditEvent) (domain.TaskTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.templates = append(s.templates, template)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return template, nil
}

func (s *MemoryStore) UpdateTemplate(_ context.Context, template domain.TaskTemplate) (domain.TaskTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.templates {
		if s.templates[i].ID == template.ID {
			s.templates[i] = template
			return template, nil
		}
	}
	return domain.TaskTemplate{}, ErrNotFound
}
func (s *MemoryStore) UpdateTemplateWithAudit(_ context.Context, template domain.TaskTemplate, audit domain.AuditEvent) (domain.TaskTemplate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, old := range s.templates {
		if old.ID == template.ID {
			s.templates[i] = template
			s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
			return template, nil
		}
	}
	return domain.TaskTemplate{}, ErrNotFound
}

func (s *MemoryStore) ListRepositories(_ context.Context, projectID string) ([]domain.Repository, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Repository, 0, len(s.repositories))
	for _, repository := range s.repositories {
		if projectID == "" || repository.ProjectID == projectID {
			out = append(out, repository)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateRepository(_ context.Context, repository domain.Repository) (domain.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repositories = append(s.repositories, repository)
	return repository, nil
}
func (s *MemoryStore) CreateRepositoryWithAudit(_ context.Context, repository domain.Repository, audit domain.AuditEvent) (domain.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repositories = append(s.repositories, repository)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return repository, nil
}
func (s *MemoryStore) ConfigureRepositoryPolicy(_ context.Context, request RepositoryPolicyConfiguration) (domain.Repository, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	authorized := false
	for _, user := range s.users {
		if user.ID == request.ActorID && user.GlobalRole == domain.RoleSystemAdmin {
			authorized = true
			break
		}
	}
	if !authorized {
		for _, member := range s.projectMembers {
			if member.ProjectID == request.ProjectID && member.UserID == request.ActorID && (member.Role == domain.RoleOwner || member.Role == domain.RoleMaintainer) {
				authorized = true
				break
			}
		}
	}
	if !authorized {
		return domain.Repository{}, ErrNotFound
	}
	key := request.RepositoryID + "\x00" + request.ConfigurationID
	if receipt, ok := s.policyConfigurations[key]; ok {
		if receipt.actorID != request.ActorID || receipt.policyHash != request.PolicyHash {
			return domain.Repository{}, ErrConflict
		}
		for _, repository := range s.repositories {
			if repository.ID == request.RepositoryID && repository.ProjectID == request.ProjectID {
				return repository, nil
			}
		}
		return domain.Repository{}, ErrNotFound
	}
	for i := range s.repositories {
		if s.repositories[i].ID == request.RepositoryID && s.repositories[i].ProjectID == request.ProjectID {
			if s.repositories[i].Policy.State == "configured" {
				return domain.Repository{}, ErrConflict
			}
			s.repositories[i].Policy = request.Policy
			s.auditEvents = append(s.auditEvents, request.Audit)
			s.policyConfigurations[key] = memoryPolicyConfiguration{actorID: request.ActorID, policyHash: request.PolicyHash}
			return s.repositories[i], nil
		}
	}
	return domain.Repository{}, ErrNotFound
}

func (s *MemoryStore) ListAccessKeys(_ context.Context, projectID string) ([]domain.AccessKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.AccessKey, 0, len(s.accessKeys))
	for _, key := range s.accessKeys {
		if projectID == "" || key.ProjectID == projectID {
			out = append(out, key)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateAccessKey(_ context.Context, key domain.AccessKey) (domain.AccessKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accessKeys = append(s.accessKeys, key)
	return key, nil
}
func (s *MemoryStore) CreateAccessKeyWithAudit(_ context.Context, key domain.AccessKey, audit domain.AuditEvent) (domain.AccessKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accessKeys = append(s.accessKeys, key)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return key, nil
}

func (s *MemoryStore) ListInventories(_ context.Context, projectID string) ([]domain.Inventory, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Inventory, 0, len(s.inventories))
	for _, inventory := range s.inventories {
		if projectID == "" || inventory.ProjectID == projectID {
			out = append(out, inventory)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateInventory(_ context.Context, inventory domain.Inventory) (domain.Inventory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inventories = append(s.inventories, inventory)
	return inventory, nil
}
func (s *MemoryStore) CreateInventoryWithAudit(_ context.Context, inventory domain.Inventory, audit domain.AuditEvent) (domain.Inventory, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.inventories = append(s.inventories, inventory)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return inventory, nil
}

func (s *MemoryStore) ListRuns(_ context.Context, projectID string) ([]domain.TaskRun, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.TaskRun, 0, len(s.runs))
	for _, run := range s.runs {
		if projectID == "" || run.ProjectID == projectID {
			out = append(out, run)
		}
	}
	return out, nil
}

func (s *MemoryStore) ListRunsPage(ctx context.Context, projectID string, page Page) (PageResult[domain.TaskRun], error) {
	runs, err := s.ListRuns(ctx, projectID)
	if err != nil {
		return PageResult[domain.TaskRun]{}, err
	}
	return paginateSlice(runs, page), nil
}

func (s *MemoryStore) ListRunLogs(_ context.Context, runID string) ([]domain.RunLog, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.RunLog, 0, len(s.logs))
	for _, log := range s.logs {
		if runID == "" || log.RunID == runID {
			out = append(out, log)
		}
	}
	return out, nil
}

func (s *MemoryStore) ListRunLogsPage(ctx context.Context, runID string, page Page) (PageResult[domain.RunLog], error) {
	logs, err := s.ListRunLogs(ctx, runID)
	if err != nil {
		return PageResult[domain.RunLog]{}, err
	}
	return paginateSlice(logs, page), nil
}

func (s *MemoryStore) ListArtifacts(_ context.Context, runID string) ([]domain.ArtifactRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.ArtifactRecord, 0, len(s.artifacts))
	for _, artifact := range s.artifacts {
		if runID == "" || artifact.RunID == runID {
			out = append(out, artifact)
		}
	}
	return out, nil
}

func (s *MemoryStore) ListArtifactsPage(ctx context.Context, runID string, page Page) (PageResult[domain.ArtifactRecord], error) {
	artifacts, err := s.ListArtifacts(ctx, runID)
	if err != nil {
		return PageResult[domain.ArtifactRecord]{}, err
	}
	return paginateSlice(artifacts, page), nil
}

func (s *MemoryStore) CreateRun(_ context.Context, run domain.TaskRun) (domain.TaskRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimOrderByRun == nil {
		s.claimOrderByRun = make(map[string]time.Time)
	}
	s.claimOrderByRun[run.ID] = run.StartedAt
	s.runs = append([]domain.TaskRun{run}, s.runs...)
	return run, nil
}

func (s *MemoryStore) CreateRunRequest(_ context.Context, run domain.TaskRun, log domain.RunLog, approval *domain.Approval, audit domain.AuditEvent) (domain.TaskRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimOrderByRun == nil {
		s.claimOrderByRun = make(map[string]time.Time)
	}
	s.claimOrderByRun[run.ID] = run.StartedAt
	s.runs = append([]domain.TaskRun{run}, s.runs...)
	s.logs = append(s.logs, log)
	if approval != nil {
		s.approvals = append([]domain.Approval{*approval}, s.approvals...)
	}
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return run, nil
}

func (s *MemoryStore) UpdateRunStatus(_ context.Context, id string, status string, finishedAt *time.Time) (domain.TaskRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deploymentBackedRunLocked(id) {
		return domain.TaskRun{}, ErrConflict
	}
	for i, run := range s.runs {
		if run.ID == id {
			run.Status = status
			run.FinishedAt = finishedAt
			if status == domain.RunQueued {
				if s.claimOrderByRun == nil {
					s.claimOrderByRun = make(map[string]time.Time)
				}
				s.claimOrderByRun[run.ID] = time.Now().UTC()
			}
			s.runs[i] = run
			return run, nil
		}
	}
	return domain.TaskRun{}, ErrNotFound
}

func (s *MemoryStore) UpdateRunWorkflowState(_ context.Context, id string, workflowState domain.WorkflowState) (domain.TaskRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deploymentBackedRunLocked(id) {
		return domain.TaskRun{}, ErrConflict
	}
	for i, run := range s.runs {
		if run.ID == id {
			run.WorkflowState = workflowState
			s.runs[i] = run
			return run, nil
		}
	}
	return domain.TaskRun{}, ErrNotFound
}

func (s *MemoryStore) ListRunners(context.Context) ([]domain.Runner, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Runner, len(s.runners))
	copy(out, s.runners)
	return out, nil
}

func (s *MemoryStore) GetRunnerByID(_ context.Context, id string) (domain.Runner, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, runner := range s.runners {
		if runner.ID == id {
			return runner, nil
		}
	}
	return domain.Runner{}, ErrNotFound
}

func (s *MemoryStore) RegisterRunner(_ context.Context, runner domain.Runner) (domain.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.runners {
		if existing.ID == runner.ID {
			return domain.Runner{}, ErrConflict
		}
	}
	s.runners = append(s.runners, runner)
	return runner, nil
}
func (s *MemoryStore) RegisterRunnerWithAudit(_ context.Context, runner domain.Runner, audit domain.AuditEvent) (domain.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.runners {
		if r.ID == runner.ID {
			return domain.Runner{}, ErrConflict
		}
	}
	s.runners = append(s.runners, runner)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return runner, nil
}

func (s *MemoryStore) CreateRunnerEnrollment(_ context.Context, enrollment domain.RunnerEnrollment, audit domain.AuditEvent) (domain.RunnerEnrollment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.runnerEnrollments {
		if existing.ID == enrollment.ID || existing.TokenHash == enrollment.TokenHash || existing.RunnerID == enrollment.RunnerID {
			return domain.RunnerEnrollment{}, ErrConflict
		}
	}
	s.runnerEnrollments = append(s.runnerEnrollments, enrollment)
	s.auditEvents = append(s.auditEvents, audit)
	return enrollment, nil
}

func (s *MemoryStore) RevokeRunnerEnrollment(_ context.Context, enrollmentID string, audit domain.AuditEvent) (domain.RunnerEnrollment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for i, enrollment := range s.runnerEnrollments {
		if enrollment.ID != enrollmentID || enrollment.RevokedAt != nil || enrollment.UsedAt != nil {
			continue
		}
		enrollment.RevokedAt = &now
		s.runnerEnrollments[i] = enrollment
		s.auditEvents = append(s.auditEvents, audit)
		return enrollment, nil
	}
	return domain.RunnerEnrollment{}, ErrNotFound
}

func (s *MemoryStore) ConsumeRunnerEnrollment(_ context.Context, consume domain.RunnerEnrollmentConsume, audit domain.AuditEvent) (domain.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for i, enrollment := range s.runnerEnrollments {
		if enrollment.TokenHash != consume.TokenHash {
			continue
		}
		if enrollment.UsedAt != nil {
			if enrollment.ConsumeRequestID == nil || enrollment.CredentialHash == nil || *enrollment.ConsumeRequestID != consume.RequestID || *enrollment.CredentialHash != consume.CredentialHash {
				return domain.Runner{}, ErrConflict
			}
			for _, registered := range s.runners {
				if registered.ID == enrollment.RunnerID && registered.TokenHash == consume.CredentialHash {
					return registered, nil
				}
			}
			return domain.Runner{}, ErrConflict
		}
		if enrollment.RevokedAt != nil || !enrollment.ExpiresAt.After(now) {
			return domain.Runner{}, ErrNotFound
		}
		for _, registered := range s.runners {
			if registered.ID == enrollment.RunnerID {
				return domain.Runner{}, ErrConflict
			}
		}
		runner := domain.Runner{ID: enrollment.RunnerID, Name: enrollment.RunnerName, Tags: append([]string(nil), enrollment.Tags...), Capabilities: append([]string(nil), enrollment.Capabilities...), TokenHash: consume.CredentialHash, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}
		s.runners = append(s.runners, runner)
		enrollment.UsedAt = &now
		enrollment.ConsumeRequestID = stringPointer(consume.RequestID)
		enrollment.CredentialHash = stringPointer(consume.CredentialHash)
		s.runnerEnrollments[i] = enrollment
		audit.ActorID = enrollment.RunnerID
		audit.TargetID = enrollment.RunnerID
		audit.Metadata = map[string]any{"enrollment_id": enrollment.ID, "runner_id": enrollment.RunnerID}
		s.auditEvents = append(s.auditEvents, audit)
		return runner, nil
	}
	return domain.Runner{}, ErrNotFound
}

func (s *MemoryStore) UpdateRunnerToken(_ context.Context, runnerID string, tokenHash string, status string, updatedAt time.Time) (domain.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, runner := range s.runners {
		if runner.ID != runnerID {
			continue
		}
		runner.TokenHash = tokenHash
		runner.Status = status
		runner.LastHeartbeatAt = updatedAt
		s.runners[i] = runner
		return runner, nil
	}
	return domain.Runner{}, ErrNotFound
}
func (s *MemoryStore) UpdateRunnerTokenWithAudit(_ context.Context, runnerID string, tokenHash string, status string, updatedAt time.Time, audit domain.AuditEvent) (domain.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.runners {
		if r.ID == runnerID {
			r.TokenHash, r.Status, r.LastHeartbeatAt = tokenHash, status, updatedAt
			s.runners[i] = r
			s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
			return r, nil
		}
	}
	return domain.Runner{}, ErrNotFound
}

func (s *MemoryStore) GetRunnerByTokenHash(_ context.Context, tokenHash string) (domain.Runner, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, runner := range s.runners {
		if runner.TokenHash == tokenHash && runner.Status == domain.RunnerActive {
			return runner, nil
		}
	}
	return domain.Runner{}, ErrNotFound
}

func (s *MemoryStore) HeartbeatRunner(_ context.Context, id string, heartbeatAt time.Time) (domain.Runner, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, runner := range s.runners {
		if runner.ID == id {
			runner.Status = domain.RunnerActive
			runner.LastHeartbeatAt = heartbeatAt
			s.runners[i] = runner
			return runner, nil
		}
	}
	return domain.Runner{}, ErrNotFound
}

func (s *MemoryStore) ClaimRun(ctx context.Context, runnerID string, now time.Time, ttl time.Duration) (domain.ClaimedRun, error) {
	return s.ClaimRunWithAudit(ctx, runnerID, now, ttl, domain.AuditEvent{})
}

func (s *MemoryStore) ClaimRunWithAudit(_ context.Context, runnerID string, now time.Time, ttl time.Duration, audit domain.AuditEvent) (domain.ClaimedRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auditIDAvailableLocked(audit.ID) {
		return domain.ClaimedRun{}, ErrConflict
	}
	s.expireLeasesLocked(now)
	staleBefore := now.Add(-2 * ttl)
	var runner domain.Runner
	foundRunner := false
	for i, candidate := range s.runners {
		if candidate.ID == runnerID {
			if candidate.Status == domain.RunnerActive && candidate.LastHeartbeatAt.Before(staleBefore) {
				candidate.Status = domain.RunnerStale
				s.runners[i] = candidate
			}
			runner = candidate
			foundRunner = true
			break
		}
	}
	if !foundRunner || runner.Status != domain.RunnerActive || runner.LastHeartbeatAt.Before(now.Add(-2*ttl)) {
		return domain.ClaimedRun{}, ErrNotFound
	}
	if s.claimCursors == nil {
		s.claimCursors = make(map[string]memoryClaimCursor)
	}
	if s.claimOrderByRun == nil {
		s.claimOrderByRun = make(map[string]time.Time)
	}
	storedCursor, hasCursor := s.claimCursors[runner.ID]
	candidateIndexes := make([]int, 0)
	for i, run := range s.runs {
		if run.Status != domain.RunQueued {
			continue
		}
		claimOrderAt, ok := s.claimOrderByRun[run.ID]
		if !ok {
			claimOrderAt = run.StartedAt
			s.claimOrderByRun[run.ID] = claimOrderAt
		}
		if hasCursor && (claimOrderAt.Before(storedCursor.claimOrderAt) || (claimOrderAt.Equal(storedCursor.claimOrderAt) && run.ID <= storedCursor.runID)) {
			continue
		}
		candidateIndexes = append(candidateIndexes, i)
	}
	sort.Slice(candidateIndexes, func(i, j int) bool {
		left, right := s.runs[candidateIndexes[i]], s.runs[candidateIndexes[j]]
		leftOrder, rightOrder := s.claimOrderByRun[left.ID], s.claimOrderByRun[right.ID]
		if leftOrder.Equal(rightOrder) {
			return left.ID < right.ID
		}
		return leftOrder.Before(rightOrder)
	})
	examineCount := len(candidateIndexes)
	if examineCount > claimCandidateLimit {
		examineCount = claimCandidateLimit
	}
	for _, runIndex := range candidateIndexes[:examineCount] {
		run := s.runs[runIndex]
		cursor := memoryClaimCursor{claimOrderAt: s.claimOrderByRun[run.ID], runID: run.ID}
		if !covers(runner.Tags, run.RunnerTags) || !contains(runner.Capabilities, claimRunType(run)) {
			storedCursor = cursor
			continue
		}
		run.Status = domain.RunRunning
		run.RunnerID = &runner.ID
		if s.nextAttemptByRun == nil {
			s.nextAttemptByRun = make(map[string]int)
		}
		attempt := s.nextAttemptByRun[run.ID]
		if attempt < 1 {
			attempt = 1
		}
		leaseID, err := newLeaseToken("lease")
		if err != nil {
			return domain.ClaimedRun{}, err
		}
		fence, err := newLeaseToken("fence")
		if err != nil {
			return domain.ClaimedRun{}, err
		}
		s.nextAttemptByRun[run.ID] = attempt + 1
		s.claimCursors[runner.ID] = cursor
		s.runs[runIndex] = run
		lease := domain.RunLease{ID: leaseID, RunID: run.ID, RunnerID: runner.ID, Status: domain.LeaseActive, ExpiresAt: now.Add(ttl), CreatedAt: now, Attempt: attempt, Fence: fence}
		s.leases = append(s.leases, lease)
		for i := range s.deployments {
			if s.deployments[i].TaskRunID == nil || *s.deployments[i].TaskRunID != run.ID {
				continue
			}
			s.deploymentAttempts = append(s.deploymentAttempts, domain.DeploymentAttempt{DeploymentID: s.deployments[i].ID, RunID: run.ID, LeaseID: lease.ID, RunnerID: runner.ID, Attempt: attempt, Fence: fence, Status: "active", CreatedAt: now})
			if s.deployments[i].Status == domain.DeploymentQueued {
				s.deploymentAttempts[len(s.deploymentAttempts)-1].CreatedAt = now
				s.deployments[i].Status = domain.DeploymentAssigned
				s.deployments[i].UpdatedAt = now
			}
		}
		if audit.ID != "" {
			audit.TargetID = run.ID
			audit.Metadata = auditMetadata(audit.Metadata, map[string]any{"runner_id": runner.ID, "lease_id": lease.ID, "attempt": lease.Attempt, "fence": lease.Fence})
			s.auditEvents = append(s.auditEvents, audit)
		}
		return domain.ClaimedRun{Lease: lease, Run: run, PrimitivePlan: primitivePlanForRun(run)}, nil
	}
	// As in PostgreSQL, a full-size page cannot prove it reached the queue tail;
	// retain its last key so the next bounded call can confirm/reset the wrap.
	if examineCount == claimCandidateLimit {
		s.claimCursors[runner.ID] = storedCursor
	} else {
		delete(s.claimCursors, runner.ID)
	}
	return domain.ClaimedRun{}, ErrNotFound
}

func (s *MemoryStore) RenewLease(_ context.Context, runnerID, leaseID, fence string, attempt int, now time.Time, ttl time.Duration) (domain.RunLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, lease := range s.leases {
		if lease.ID != leaseID || lease.RunnerID != runnerID || lease.Fence != fence || lease.Attempt != attempt || lease.Status != domain.LeaseActive || !lease.ExpiresAt.After(now) {
			continue
		}
		lease.ExpiresAt = now.Add(ttl)
		s.leases[i] = lease
		return lease, nil
	}
	return domain.RunLease{}, ErrNotFound
}

func (s *MemoryStore) expireLeasesLocked(now time.Time) {
	for i, lease := range s.leases {
		if lease.Status != domain.LeaseActive || lease.ExpiresAt.After(now) {
			continue
		}
		lease.Status = domain.LeaseExpired
		lease.CompletedAt = &now
		s.leases[i] = lease
		// A reclaimed run receives a fresh lease and deployment attempt.  The
		// attempt bound to the expired fence must therefore be terminal as well;
		// otherwise it remains falsely active alongside the replacement attempt.
		for j := range s.deploymentAttempts {
			attempt := &s.deploymentAttempts[j]
			if attempt.LeaseID == lease.ID && attempt.Status == "active" {
				attempt.Status = "failed"
				attempt.FinishedAt = &now
			}
		}
		for j, run := range s.runs {
			if run.ID == lease.RunID && run.Status == domain.RunRunning {
				run.Status = domain.RunQueued
				run.RunnerID = nil
				run.FinishedAt = nil
				s.runs[j] = run
				if s.claimOrderByRun == nil {
					s.claimOrderByRun = make(map[string]time.Time)
				}
				s.claimOrderByRun[run.ID] = now
				break
			}
		}
	}
}

func (s *MemoryStore) ExpireLeases(_ context.Context, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLeasesLocked(now)
	return nil
}

func (s *MemoryStore) CompleteLeaseRequest(_ context.Context, leaseID string, runnerID string, status string, attempt int, fence string, completionKey string, completedAt time.Time, runStatus string, finishedAt *time.Time, workflowState *domain.WorkflowState, logs []domain.RunLog, audit domain.AuditEvent) (domain.RunLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, lease := range s.leases {
		if lease.ID != leaseID || lease.RunnerID != runnerID || lease.Attempt != attempt || lease.Fence != fence {
			continue
		}
		for _, deployment := range s.deployments {
			if deployment.TaskRunID != nil && *deployment.TaskRunID == lease.RunID {
				return domain.RunLease{}, ErrConflict
			}
		}
		if lease.CompletionKey != "" {
			if lease.CompletionKey == completionKey && lease.Status == status {
				return lease, nil
			}
			return domain.RunLease{}, ErrConflict
		}
		if lease.Status != domain.LeaseActive || !lease.ExpiresAt.After(completedAt) {
			return domain.RunLease{}, ErrNotFound
		}
		lease.Status = status
		lease.CompletedAt = &completedAt
		lease.CompletionKey = completionKey
		s.leases[i] = lease
		for j, run := range s.runs {
			if run.ID != lease.RunID {
				continue
			}
			run.Status = runStatus
			run.FinishedAt = finishedAt
			if workflowState != nil {
				run.WorkflowState = *workflowState
			}
			s.runs[j] = run
			break
		}
		if runStatus == domain.RunQueued {
			if s.claimOrderByRun == nil {
				s.claimOrderByRun = make(map[string]time.Time)
			}
			s.claimOrderByRun[lease.RunID] = completedAt
		}
		for _, log := range logs {
			s.createRunLogLocked(log)
		}
		s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
		return lease, nil
	}
	return domain.RunLease{}, ErrNotFound
}

func (s *MemoryStore) CancelRunRequest(_ context.Context, runID string, canceledAt time.Time, log domain.RunLog, audit domain.AuditEvent) (domain.TaskRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deploymentBackedRunLocked(runID) {
		return domain.TaskRun{}, ErrConflict
	}
	for i, run := range s.runs {
		if run.ID != runID || domain.IsTerminalRunStatus(run.Status) {
			continue
		}
		for j, lease := range s.leases {
			if lease.RunID == runID && lease.Status == domain.LeaseActive {
				lease.Status = domain.RunCanceled
				lease.CompletedAt = &canceledAt
				s.leases[j] = lease
			}
		}
		run.Status = domain.RunCanceled
		run.RunnerID = nil
		run.FinishedAt = &canceledAt
		s.runs[i] = run
		s.createRunLogLocked(log)
		s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
		return run, nil
	}
	return domain.TaskRun{}, ErrNotFound
}

func (s *MemoryStore) ActiveLeaseForRun(_ context.Context, runID string) (domain.RunLease, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, lease := range s.leases {
		if lease.RunID == runID && lease.Status == domain.LeaseActive && lease.ExpiresAt.After(time.Now().UTC()) {
			return lease, nil
		}
	}
	return domain.RunLease{}, ErrNotFound
}

func (s *MemoryStore) GetLeaseForRunner(_ context.Context, leaseID string, runnerID string) (domain.RunLease, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, lease := range s.leases {
		if lease.ID == leaseID && lease.RunnerID == runnerID && lease.Status == domain.LeaseActive && lease.ExpiresAt.After(time.Now().UTC()) {
			return lease, nil
		}
	}
	return domain.RunLease{}, ErrNotFound
}

func (s *MemoryStore) GetLeaseForCompletion(_ context.Context, leaseID, runnerID string, attempt int, fence string) (domain.RunLease, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, lease := range s.leases {
		if lease.ID == leaseID && lease.RunnerID == runnerID && lease.Attempt == attempt && lease.Fence == fence {
			return lease, nil
		}
	}
	return domain.RunLease{}, ErrNotFound
}

func (s *MemoryStore) AuthorizeSecretAccess(_ context.Context, request domain.SecretAccessRequest) (domain.SecretAccessGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, lease := range s.leases {
		if lease.ID != request.LeaseID || lease.RunID != request.RunID || lease.RunnerID != request.RunnerID || lease.Attempt != request.Attempt || lease.Fence != request.Fence || lease.Status != domain.LeaseActive || !lease.ExpiresAt.After(request.RequestedAt) {
			continue
		}
		expected := secretAccessAudit(request, request.RequestedAt)
		var replay *domain.AuditEvent
		for _, existing := range s.auditEvents {
			if existing.ID != request.AccessID {
				continue
			}
			if !secretAccessAuditsEqual(existing, expected) {
				return domain.SecretAccessGrant{}, ErrConflict
			}
			copy := existing
			replay = &copy
			break
		}
		var targetRun domain.TaskRun
		for _, run := range s.runs {
			if run.ID == request.RunID {
				targetRun = run
				break
			}
		}
		if !runAuthorizesSecretAccess(targetRun, request) {
			return domain.SecretAccessGrant{}, ErrNotFound
		}
		if replay != nil {
			return secretAccessGrant(request, replay.CreatedAt), nil
		}
		s.auditEvents = append(s.auditEvents, expected)
		return secretAccessGrant(request, expected.CreatedAt), nil
	}
	return domain.SecretAccessGrant{}, ErrNotFound
}

func (s *MemoryStore) CreateRunLog(_ context.Context, log domain.RunLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deploymentBackedRunLocked(log.RunID) {
		return ErrConflict
	}
	s.createRunLogLocked(log)
	return nil
}

func (s *MemoryStore) createRunLogLocked(log domain.RunLog) {
	for {
		conflict := false
		for _, existing := range s.logs {
			if existing.RunID == log.RunID && existing.Sequence == log.Sequence {
				conflict = true
				log.Sequence++
				break
			}
		}
		if !conflict {
			break
		}
	}
	s.logs = append(s.logs, log)
}

func (s *MemoryStore) CreateArtifact(_ context.Context, artifact domain.ArtifactRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deploymentBackedRunLocked(artifact.RunID) {
		return ErrConflict
	}
	s.artifacts = append(s.artifacts, artifact)
	return nil
}

func (s *MemoryStore) CreateRunLogForLease(_ context.Context, log domain.RunLog, runnerID, leaseID string, attempt int, fence string, now time.Time) (domain.RunLog, error) {
	if log.EventKey != "" {
		logs, err := s.CreateRunLogsForLease(context.Background(), []domain.RunLog{log}, log.RunID, runnerID, leaseID, attempt, fence, now)
		if err != nil {
			return domain.RunLog{}, err
		}
		return logs[0], nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, lease := range s.leases {
		if lease.ID == leaseID && lease.RunID == log.RunID && lease.RunnerID == runnerID && lease.Attempt == attempt && lease.Fence == fence && lease.Status == domain.LeaseActive && lease.ExpiresAt.After(now) {
			s.createRunLogLocked(log)
			return s.logs[len(s.logs)-1], nil
		}
	}
	return domain.RunLog{}, ErrNotFound
}

func (s *MemoryStore) CreateRunLogsForLease(_ context.Context, logs []domain.RunLog, runID, runnerID, leaseID string, attempt int, fence string, now time.Time) ([]domain.RunLog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	identityMatches := false
	for _, lease := range s.leases {
		if lease.ID == leaseID && lease.RunID == runID && lease.RunnerID == runnerID && lease.Attempt == attempt && lease.Fence == fence {
			identityMatches = true
			break
		}
	}
	if !identityMatches {
		return nil, ErrNotFound
	}
	results := make([]domain.RunLog, len(logs))
	newEvents := make([]bool, len(logs))
	hasNew := false
	for i, log := range logs {
		if log.RunID != runID || log.EventKey == "" || log.RequestedSequence <= 0 || log.LeaseID != leaseID || log.Attempt != attempt {
			return nil, ErrConflict
		}
		found := false
		for _, existing := range s.logs {
			if existing.RunID != runID || existing.EventKey != log.EventKey {
				continue
			}
			found = true
			if existing.LeaseID != leaseID || existing.Attempt != attempt || existing.RequestedSequence != log.RequestedSequence || existing.Stream != log.Stream || existing.Message != log.Message {
				return nil, ErrConflict
			}
			results[i] = existing
			break
		}
		if !found {
			newEvents[i] = true
			hasNew = true
		}
	}
	if hasNew {
		authorized := false
		for _, lease := range s.leases {
			if lease.ID == leaseID && lease.RunID == runID && lease.RunnerID == runnerID && lease.Attempt == attempt && lease.Fence == fence && lease.Status == domain.LeaseActive && lease.ExpiresAt.After(now) {
				authorized = true
				break
			}
		}
		if !authorized {
			return nil, ErrNotFound
		}
	}
	for i, log := range logs {
		if !newEvents[i] {
			continue
		}
		s.createRunLogLocked(log)
		results[i] = s.logs[len(s.logs)-1]
	}
	return results, nil
}

func (s *MemoryStore) CreateArtifactForLease(_ context.Context, artifact domain.ArtifactRecord, runnerID string, attempt int, fence string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, lease := range s.leases {
		if lease.ID == artifact.LeaseID && lease.RunID == artifact.RunID && lease.RunnerID == runnerID && lease.Attempt == attempt && lease.Fence == fence && lease.Status == domain.LeaseActive && lease.ExpiresAt.After(now) {
			s.artifacts = append(s.artifacts, artifact)
			return nil
		}
	}
	return ErrNotFound
}

func covers(values []string, required []string) bool {
	for _, item := range required {
		if !contains(values, item) {
			return false
		}
	}
	return true
}

func claimRunType(run domain.TaskRun) string {
	if len(run.Workflow.Steps) == 0 {
		return run.RunSpec.Type
	}
	statusByID := map[string]string{}
	for _, step := range run.WorkflowState.Steps {
		statusByID[step.ID] = step.Status
	}
	for _, step := range run.Workflow.Steps {
		status := statusByID[step.ID]
		if status == "" {
			status = domain.WorkflowPending
		}
		if status != domain.WorkflowPending {
			continue
		}
		ready := true
		for _, dependency := range step.DependsOn {
			if statusByID[dependency] != domain.RunSucceeded {
				ready = false
				break
			}
		}
		if ready {
			return step.RunSpec.Type
		}
	}
	return run.RunSpec.Type
}

func contains(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func primitivePlanForRun(run domain.TaskRun) domain.RunnerPrimitivePlan {
	plan := domain.RunnerPrimitivePlan{RunID: run.ID, Process: run.RunSpec.Process, Artifacts: run.RunSpec.Artifacts, Secrets: run.RunSpec.Secrets}
	if run.RunSpec.Repository != nil {
		dest := run.RunSpec.Repository.Path
		if dest == "" {
			dest = "workspace"
		}
		plan.Checkout = &domain.CheckoutPlan{Repository: *run.RunSpec.Repository, DestPath: dest}
	}
	return plan
}

func newLeaseToken(prefix string) (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(buf), nil
}

func (s *MemoryStore) ListApprovals(_ context.Context, status string) ([]domain.Approval, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.Approval, 0, len(s.approvals))
	for _, approval := range s.approvals {
		if status == "" || approval.Status == status {
			out = append(out, approval)
		}
	}
	return out, nil
}

func (s *MemoryStore) CreateApproval(_ context.Context, approval domain.Approval) (domain.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deploymentBackedRunLocked(approval.RunID) {
		return domain.Approval{}, ErrConflict
	}
	s.approvals = append(s.approvals, approval)
	return approval, nil
}

func (s *MemoryStore) ApproveRun(ctx context.Context, runID string, actorID string, approvedAt time.Time) (domain.Approval, error) {
	return s.ApproveRunWithAudit(ctx, runID, actorID, approvedAt, domain.AuditEvent{})
}

func (s *MemoryStore) ApproveRunWithAudit(_ context.Context, runID string, actorID string, approvedAt time.Time, audit domain.AuditEvent) (domain.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auditIDAvailableLocked(audit.ID) {
		return domain.Approval{}, ErrConflict
	}
	if s.deploymentBackedRunLocked(runID) {
		return domain.Approval{}, ErrConflict
	}
	for i, approval := range s.approvals {
		if approval.RunID == runID && approval.Status == domain.ApprovalPending {
			runIndex := -1
			for j, run := range s.runs {
				if run.ID == runID && run.Status == domain.RunWaitingApproval {
					runIndex = j
					break
				}
			}
			if runIndex < 0 {
				return domain.Approval{}, ErrNotFound
			}
			approval.Status = domain.ApprovalApproved
			approval.ApprovedBy = &actorID
			approval.ApprovedAt = &approvedAt
			s.approvals[i] = approval
			run := s.runs[runIndex]
			run.Status = domain.RunQueued
			s.runs[runIndex] = run
			if s.claimOrderByRun == nil {
				s.claimOrderByRun = make(map[string]time.Time)
			}
			s.claimOrderByRun[runID] = approvedAt
			if audit.ID != "" {
				audit.TargetID = runID
				audit.Metadata = auditMetadata(audit.Metadata, map[string]any{"approval_id": approval.ID})
				s.auditEvents = append(s.auditEvents, audit)
			}
			return approval, nil
		}
	}
	return domain.Approval{}, ErrNotFound
}

func (s *MemoryStore) RejectRun(ctx context.Context, runID string, actorID string, rejectedAt time.Time) (domain.Approval, error) {
	return s.RejectRunWithAudit(ctx, runID, actorID, rejectedAt, domain.AuditEvent{})
}

func (s *MemoryStore) RejectRunWithAudit(_ context.Context, runID string, actorID string, rejectedAt time.Time, audit domain.AuditEvent) (domain.Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.auditIDAvailableLocked(audit.ID) {
		return domain.Approval{}, ErrConflict
	}
	if s.deploymentBackedRunLocked(runID) {
		return domain.Approval{}, ErrConflict
	}
	for i, approval := range s.approvals {
		if approval.RunID == runID && approval.Status == domain.ApprovalPending {
			approval.Status = domain.ApprovalRejected
			approval.ApprovedBy = &actorID
			approval.ApprovedAt = &rejectedAt
			s.approvals[i] = approval
			for j, run := range s.runs {
				if run.ID == runID {
					run.Status = domain.RunCanceled
					run.FinishedAt = &rejectedAt
					s.runs[j] = run
					break
				}
			}
			if audit.ID != "" {
				audit.TargetID = runID
				audit.Metadata = auditMetadata(audit.Metadata, map[string]any{"approval_id": approval.ID})
				s.auditEvents = append(s.auditEvents, audit)
			}
			return approval, nil
		}
	}
	return domain.Approval{}, ErrNotFound
}

func (s *MemoryStore) ListAuditEvents(context.Context) ([]domain.AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.AuditEvent, len(s.auditEvents))
	copy(out, s.auditEvents)
	return out, nil
}

func (s *MemoryStore) ListAuditEventsPage(ctx context.Context, page Page) (PageResult[domain.AuditEvent], error) {
	events, err := s.ListAuditEvents(ctx)
	if err != nil {
		return PageResult[domain.AuditEvent]{}, err
	}
	return paginateSlice(events, page), nil
}

func (s *MemoryStore) CreateAuditEvent(_ context.Context, event domain.AuditEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.auditEvents = append([]domain.AuditEvent{event}, s.auditEvents...)
	return nil
}

func paginateSlice[T any](items []T, page Page) PageResult[T] {
	total := len(items)
	limit := page.Limit
	offset := page.Offset
	if !page.Enabled {
		limit = total
		offset = 0
	}
	if page.Enabled && limit == 0 {
		limit = total
	}
	if limit > MaxPageLimit {
		limit = MaxPageLimit
	}
	if limit < 0 {
		limit = 0
	}
	if offset < 0 {
		offset = 0
	}
	if offset > MaxPageOffset {
		offset = MaxPageOffset
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return PageResult[T]{Items: items[offset:end], Limit: limit, Offset: offset, Total: total}
}

func (s *MemoryStore) ListServices(_ context.Context, projectID string) ([]domain.Service, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Service
	for _, v := range s.services {
		if projectID == "" || v.ProjectID == projectID {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *MemoryStore) CreateService(_ context.Context, v domain.Service) (domain.Service, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.services {
		if x.ID == v.ID || (x.ProjectID == v.ProjectID && x.Name == v.Name) {
			return domain.Service{}, ErrConflict
		}
	}
	s.services = append(s.services, v)
	if s.serviceByID == nil {
		s.serviceByID = map[string]int{}
	}
	s.serviceByID[v.ID] = len(s.services) - 1
	return v, nil
}
func (s *MemoryStore) CreateServiceWithAudit(_ context.Context, v domain.Service, audit domain.AuditEvent) (domain.Service, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.services {
		if x.ID == v.ID || (x.ProjectID == v.ProjectID && x.Name == v.Name) {
			return domain.Service{}, ErrConflict
		}
	}
	s.services = append(s.services, v)
	if s.serviceByID == nil {
		s.serviceByID = map[string]int{}
	}
	s.serviceByID[v.ID] = len(s.services) - 1
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return v, nil
}
func (s *MemoryStore) GetService(_ context.Context, id string) (domain.Service, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	index, ok := s.serviceByID[id]
	if !ok || index < 0 || index >= len(s.services) || s.services[index].ID != id {
		return domain.Service{}, ErrNotFound
	}
	return s.services[index], nil
}
func (s *MemoryStore) ListEnvironments(_ context.Context, serviceID string) ([]domain.Environment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Environment
	for _, v := range s.environments {
		if serviceID == "" || v.ServiceID == serviceID {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *MemoryStore) CreateEnvironment(_ context.Context, v domain.Environment) (domain.Environment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.environments {
		if x.ID == v.ID || (x.ServiceID == v.ServiceID && x.Name == v.Name) {
			return domain.Environment{}, ErrConflict
		}
	}
	s.environments = append(s.environments, v)
	if s.environmentByID == nil {
		s.environmentByID = map[string]int{}
	}
	s.environmentByID[v.ID] = len(s.environments) - 1
	return v, nil
}
func (s *MemoryStore) CreateEnvironmentWithAudit(_ context.Context, v domain.Environment, audit domain.AuditEvent) (domain.Environment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.environments {
		if x.ID == v.ID || (x.ServiceID == v.ServiceID && x.Name == v.Name) {
			return domain.Environment{}, ErrConflict
		}
	}
	s.environments = append(s.environments, v)
	if s.environmentByID == nil {
		s.environmentByID = map[string]int{}
	}
	s.environmentByID[v.ID] = len(s.environments) - 1
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return v, nil
}
func (s *MemoryStore) GetEnvironment(_ context.Context, id string) (domain.Environment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	index, ok := s.environmentByID[id]
	if !ok || index < 0 || index >= len(s.environments) || s.environments[index].ID != id {
		return domain.Environment{}, ErrNotFound
	}
	return s.environments[index], nil
}
func (s *MemoryStore) ListRevisions(_ context.Context, serviceID string) ([]domain.Revision, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Revision
	for _, v := range s.revisions {
		if serviceID == "" || v.ServiceID == serviceID {
			out = append(out, v)
		}
	}
	return out, nil
}
func (s *MemoryStore) CreateRevision(_ context.Context, v domain.Revision) (domain.Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.revisions {
		if x.ID == v.ID || (v.ContentIdentity != "" && x.ServiceID == v.ServiceID && x.ContentIdentity == v.ContentIdentity) {
			if v.ContentIdentity != "" && x.ServiceID == v.ServiceID && x.ContentIdentity == v.ContentIdentity {
				return x, nil
			}
			return domain.Revision{}, ErrConflict
		}
	}
	// Direct repository callers (fixtures/imports) may supply complete evidence;
	// application creates deliberately leave it pending for the runner.
	if v.GitCommit != "" && v.ComposeHash != "" {
		v.ProvenanceResolved = true
		v.ProvenanceState = "legacy_unverified"
		now := time.Now().UTC()
		v.ResolvedAt = &now
	}
	if v.ProvenanceState == "" {
		v.ProvenanceState = "pending"
	}
	s.revisions = append(s.revisions, v)
	return v, nil
}
func (s *MemoryStore) CreateRevisionWithAudit(_ context.Context, v domain.Revision, audit domain.AuditEvent) (domain.Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.revisions {
		if x.ID == v.ID || (v.ContentIdentity != "" && x.ServiceID == v.ServiceID && x.ContentIdentity == v.ContentIdentity) {
			if v.ContentIdentity != "" && x.ServiceID == v.ServiceID && x.ContentIdentity == v.ContentIdentity {
				return x, nil
			}
			return domain.Revision{}, ErrConflict
		}
	}
	s.revisions = append(s.revisions, v)
	s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
	return v, nil
}

func (s *MemoryStore) DeploymentPlan(_ context.Context, deploymentID, runID, leaseID, runnerID string, attempt int, fence string) (domain.DeploymentPlan, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := time.Now().UTC()
	var deployment domain.Deployment
	for _, d := range s.deployments {
		if d.ID == deploymentID && d.TaskRunID != nil && *d.TaskRunID == runID {
			deployment = d
			break
		}
	}
	if deployment.ID == "" {
		return domain.DeploymentPlan{}, ErrNotFound
	}
	valid := false
	for _, l := range s.leases {
		if l.ID == leaseID && l.RunID == runID && l.RunnerID == runnerID && l.Attempt == attempt && l.Fence == fence && l.Status == domain.LeaseActive && l.ExpiresAt.After(now) {
			valid = true
		}
	}
	if !valid {
		return domain.DeploymentPlan{}, ErrNotFound
	}
	var env domain.Environment
	for _, x := range s.environments {
		if x.ID == deployment.EnvironmentID {
			env = x
			break
		}
	}
	var service domain.Service
	for _, x := range s.services {
		if x.ID == env.ServiceID {
			service = x
			break
		}
	}
	var revision domain.Revision
	for _, x := range s.revisions {
		if x.ID == deployment.DesiredRevisionID {
			revision = x
			break
		}
	}
	var repository domain.Repository
	for _, x := range s.repositories {
		if x.ID == service.RepositoryID {
			repository = x
			break
		}
	}
	if env.ID == "" || service.ID == "" || revision.ID == "" || repository.ID == "" {
		return domain.DeploymentPlan{}, ErrNotFound
	}
	requestedRef := revision.RequestedRef
	// A resolved revision is durable provenance. Replays and rollback children
	// must use that commit even if the original requested branch has advanced.
	if immutableGitCommit(revision.GitCommit) {
		requestedRef = revision.GitCommit
	}
	var cancellationRequestID *string
	for key, receipt := range s.deploymentCancels {
		if strings.HasPrefix(key, deploymentID+"\x00") {
			value := receipt.RequestID
			cancellationRequestID = &value
			break
		}
	}
	return domain.DeploymentPlan{DeploymentID: deployment.ID, Status: deployment.Status, RunID: runID, LeaseID: leaseID, Attempt: attempt, Fence: fence, ProjectID: service.ProjectID, ServiceID: service.ID, EnvironmentID: env.ID, RepositoryID: repository.ID, RepositoryURL: repository.URL, RepositoryPolicy: repository.Policy, RequestedRef: requestedRef, ComposePath: service.ComposePath, Profiles: append([]string(nil), service.Profiles...), ComposeProject: env.ComposeProject, TimeoutSeconds: env.TimeoutSeconds, HealthPolicy: env.HealthPolicy, SecretBindings: append([]domain.SecretBinding(nil), env.SecretBindings...), RollbackSafe: env.RollbackSafe, PreviousHealthyRevisionID: deployment.PreviousHealthyRevisionID, RollbackOfID: deployment.RollbackOfID, CancellationRequestID: cancellationRequestID}, nil
}

func (s *MemoryStore) ResolveRevisionProvenance(_ context.Context, deploymentID, runID, leaseID, runnerID string, attempt int, fence, resolutionID, commit, hash string, digests []string, audit domain.AuditEvent) (domain.Revision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := deploymentID + "\x00" + resolutionID
	if replay, ok := s.provenanceReplays[key]; ok {
		if replay.runID == runID && replay.leaseID == leaseID && replay.runnerID == runnerID && replay.attempt == attempt && replay.fence == fence && replay.commit == commit && replay.hash == hash && reflect.DeepEqual(replay.digests, digests) {
			return replay.revision, nil
		}
		return domain.Revision{}, ErrConflict
	}
	now := time.Now().UTC()
	valid := false
	for _, da := range s.deploymentAttempts {
		if da.DeploymentID == deploymentID && da.RunID == runID && da.LeaseID == leaseID && da.RunnerID == runnerID && da.Attempt == attempt && da.Fence == fence {
			valid = true
			break
		}
	}
	if !valid {
		return domain.Revision{}, ErrNotFound
	}
	valid = false
	for _, l := range s.leases {
		if l.ID == leaseID && l.RunID == runID && l.RunnerID == runnerID && l.Attempt == attempt && l.Fence == fence && l.Status == domain.LeaseActive && l.ExpiresAt.After(now) {
			valid = true
		}
	}
	if !valid {
		return domain.Revision{}, ErrNotFound
	}
	for _, d := range s.deployments {
		if d.ID == deploymentID && d.TaskRunID != nil && *d.TaskRunID == runID {
			if d.Status != domain.DeploymentAssigned && d.Status != domain.DeploymentPreparing && d.Status != domain.DeploymentApplying && d.Status != domain.DeploymentVerifying {
				return domain.Revision{}, ErrNotFound
			}
			for i := range s.revisions {
				r := &s.revisions[i]
				if r.ID != d.DesiredRevisionID {
					continue
				}
				if r.ProvenanceResolved {
					// Rollback children reuse an already attested revision but still
					// need a per-attempt provenance receipt under their new fence.
					if r.GitCommit != commit || r.ComposeHash != hash || !reflect.DeepEqual(r.ImageDigests, digests) {
						return domain.Revision{}, ErrConflict
					}
				} else {
					if r.ProvenanceState != "pending" {
						return domain.Revision{}, ErrConflict
					}
					r.GitCommit, r.ComposeHash, r.ImageDigests = commit, hash, append([]string(nil), digests...)
					r.ContentIdentity = commit + ":" + hash
					r.ProvenanceResolved, r.ProvenanceState = true, "resolved"
					r.ResolvedAt = &now
				}
				if audit.ID != "" {
					audit.CreatedAt = now
					s.auditEvents = append(s.auditEvents, audit)
				}
				s.provenanceReplays[key] = memoryProvenanceReplay{deploymentID: deploymentID, resolutionID: resolutionID, runID: runID, leaseID: leaseID, runnerID: runnerID, attempt: attempt, fence: fence, commit: commit, hash: hash, digests: append([]string(nil), digests...), revision: *r}
				return *r, nil
			}
		}
	}
	return domain.Revision{}, ErrNotFound
}
func (s *MemoryStore) ListDeployments(_ context.Context, environmentID string) ([]domain.Deployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []domain.Deployment
	for _, v := range s.deployments {
		if environmentID == "" || v.EnvironmentID == environmentID {
			out = append(out, v)
		}
	}
	return out, nil
}

func (s *MemoryStore) deploymentBackedRunLocked(runID string) bool {
	for _, deployment := range s.deployments {
		if deployment.TaskRunID != nil && *deployment.TaskRunID == runID {
			return true
		}
	}
	return false
}

func (s *MemoryStore) CreateDeploymentRequest(_ context.Context, v domain.Deployment, run domain.TaskRun, a domain.AuditEvent) (domain.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v.RollbackOfID != nil {
		return domain.Deployment{}, ErrConflict
	}
	var environment domain.Environment
	for _, candidate := range s.environments {
		if candidate.ID == v.EnvironmentID {
			environment = candidate
			break
		}
	}
	if environment.ID == "" {
		return domain.Deployment{}, ErrNotFound
	}
	validRevision := false
	for _, revision := range s.revisions {
		if revision.ID == v.DesiredRevisionID && revision.ServiceID == environment.ServiceID {
			validRevision = true
			break
		}
	}
	if !validRevision {
		return domain.Deployment{}, ErrNotFound
	}
	for _, existing := range s.deployments {
		if existing.EnvironmentID == v.EnvironmentID && existing.IdempotencyKey == v.IdempotencyKey {
			if existing.DesiredRevisionID == v.DesiredRevisionID {
				return existing, nil
			}
			return domain.Deployment{}, ErrConflict
		}
		if existing.EnvironmentID == v.EnvironmentID && !domain.IsTerminalDeploymentStatus(existing.Status) {
			return domain.Deployment{}, ErrConflict
		}
	}
	for _, existing := range s.runs {
		if existing.ID == run.ID {
			return domain.Deployment{}, ErrConflict
		}
	}
	v.TaskRunID = stringPointer(run.ID)
	v.PreviousHealthyRevisionID = environment.CurrentHealthyRevisionID
	s.runs = append([]domain.TaskRun{run}, s.runs...)
	if s.claimOrderByRun == nil {
		s.claimOrderByRun = map[string]time.Time{}
	}
	s.claimOrderByRun[run.ID] = run.StartedAt
	s.deployments = append(s.deployments, v)
	if s.deploymentByID == nil {
		s.deploymentByID = map[string]int{}
	}
	s.deploymentByID[v.ID] = len(s.deployments) - 1
	s.auditEvents = append([]domain.AuditEvent{a}, s.auditEvents...)
	return v, nil
}
func deploymentTransitionAllowed(from, to domain.DeploymentStatus) bool {
	return domain.DeploymentTransitionAllowed(from, to)
}

func (s *MemoryStore) GetDeployment(_ context.Context, id string) (domain.Deployment, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	index, ok := s.deploymentByID[id]
	if !ok || index < 0 || index >= len(s.deployments) || s.deployments[index].ID != id {
		return domain.Deployment{}, ErrNotFound
	}
	return s.deployments[index], nil
}

func (s *MemoryStore) ConfirmDeployment(_ context.Context, id, confirmedBy string, audit domain.AuditEvent) (domain.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, deployment := range s.deployments {
		if deployment.ID != id {
			continue
		}
		if deployment.Status != domain.DeploymentWaitingConfirmation || deployment.TaskRunID == nil {
			return domain.Deployment{}, ErrConflict
		}
		now := time.Now().UTC()
		deployment.Status = domain.DeploymentAssigned
		deployment.ConfirmedBy = stringPointer(confirmedBy)
		deployment.UpdatedAt = now
		s.deployments[i] = deployment
		for j, run := range s.runs {
			if run.ID == *deployment.TaskRunID && run.Status == domain.RunWaitingApproval {
				run.Status = domain.RunQueued
				s.runs[j] = run
				s.claimOrderByRun[run.ID] = now
			}
		}
		s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
		return deployment, nil
	}
	return domain.Deployment{}, ErrNotFound
}

// FailPreAssignmentDeployment records a maintainer-side validation failure
// before any runner owns a deployment. Assigned work must use the fenced
// runner transition protocol instead.
func (s *MemoryStore) FailPreAssignmentDeployment(_ context.Context, id, failureCode string, audit domain.AuditEvent) (domain.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, deployment := range s.deployments {
		if deployment.ID != id {
			continue
		}
		if deployment.Status == domain.DeploymentFailed {
			if deployment.FailureCode == failureCode {
				return deployment, nil
			}
			return domain.Deployment{}, ErrConflict
		}
		if (deployment.Status != domain.DeploymentQueued && deployment.Status != domain.DeploymentWaitingConfirmation) || deployment.TaskRunID == nil {
			return domain.Deployment{}, ErrConflict
		}
		now := time.Now().UTC()
		runIndex := -1
		for j, run := range s.runs {
			if run.ID == *deployment.TaskRunID && (run.Status == domain.RunQueued || run.Status == domain.RunWaitingApproval) {
				runIndex = j
				break
			}
		}
		if runIndex == -1 {
			return domain.Deployment{}, ErrConflict
		}
		deployment.Status = domain.DeploymentFailed
		deployment.FailureCode = failureCode
		deployment.UpdatedAt = now
		deployment.FinishedAt = &now
		run := s.runs[runIndex]
		run.Status = domain.RunFailed
		run.RunnerID = nil
		run.FinishedAt = &now
		s.runs[runIndex] = run
		s.deployments[i] = deployment
		audit.CreatedAt = now
		s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
		return deployment, nil
	}
	return domain.Deployment{}, ErrNotFound
}

func sameDeploymentTransition(left, right domain.DeploymentTransitionRequest) bool {
	return left.DeploymentID == right.DeploymentID && left.RunID == right.RunID && left.LeaseID == right.LeaseID && left.RunnerID == right.RunnerID && left.Attempt == right.Attempt && left.Fence == right.Fence && left.ExpectedStatus == right.ExpectedStatus && left.TargetStatus == right.TargetStatus && left.FailureCode == right.FailureCode && ((left.HealthPassed == nil && right.HealthPassed == nil) || (left.HealthPassed != nil && right.HealthPassed != nil && *left.HealthPassed == *right.HealthPassed)) && reflect.DeepEqual(left.Metadata, right.Metadata)
}

func (s *MemoryStore) TransitionDeploymentAttempt(_ context.Context, request domain.DeploymentTransitionRequest, audit domain.AuditEvent) (domain.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deploymentTransitions == nil {
		s.deploymentTransitions = map[string]domain.DeploymentTransitionRequest{}
	}
	var lease domain.RunLease
	leaseIndex := -1
	foundLease := false
	for i, candidate := range s.leases {
		if candidate.ID == request.LeaseID && candidate.RunID == request.RunID && candidate.RunnerID == request.RunnerID && candidate.Attempt == request.Attempt && candidate.Fence == request.Fence {
			lease = candidate
			foundLease = true
			leaseIndex = i
			break
		}
	}
	if !foundLease {
		return domain.Deployment{}, ErrNotFound
	}
	attemptIndex := -1
	for i, candidate := range s.deploymentAttempts {
		if candidate.DeploymentID == request.DeploymentID && candidate.RunID == request.RunID && candidate.LeaseID == request.LeaseID && candidate.RunnerID == request.RunnerID && candidate.Attempt == request.Attempt && candidate.Fence == request.Fence {
			attemptIndex = i
			break
		}
	}
	if attemptIndex == -1 {
		return domain.Deployment{}, ErrNotFound
	}
	key := request.DeploymentID + "\x00" + request.TransitionKey
	if replay, ok := s.deploymentTransitions[key]; ok {
		if !sameDeploymentTransition(replay, request) {
			return domain.Deployment{}, ErrConflict
		}
		for _, deployment := range s.deployments {
			if deployment.ID == request.DeploymentID {
				return deployment, nil
			}
		}
		return domain.Deployment{}, ErrNotFound
	}
	now := time.Now().UTC()
	if lease.Status != domain.LeaseActive || !lease.ExpiresAt.After(now) {
		return domain.Deployment{}, ErrNotFound
	}
	for i, deployment := range s.deployments {
		if deployment.ID != request.DeploymentID {
			continue
		}
		resolved := false
		for _, revision := range s.revisions {
			if revision.ID == deployment.DesiredRevisionID {
				resolved = revision.ProvenanceResolved
				break
			}
		}
		if deployment.TaskRunID == nil || *deployment.TaskRunID != request.RunID || deployment.Status != request.ExpectedStatus || !domain.DeploymentRoleTransitionAllowed(deployment.RollbackOfID != nil, deployment.Status, request.TargetStatus) || (request.TargetStatus == domain.DeploymentApplying && !resolved) || ((request.TargetStatus == domain.DeploymentSucceeded || (request.TargetStatus == domain.DeploymentRolledBack && deployment.RollbackOfID != nil)) && (request.HealthPassed == nil || !*request.HealthPassed)) {
			return domain.Deployment{}, ErrConflict
		}
		deployment.Status = request.TargetStatus
		deployment.HealthPassed = request.HealthPassed
		deployment.FailureCode = request.FailureCode
		deployment.UpdatedAt = now
		attemptStatus, leaseStatus, runStatus, terminal := deploymentTerminalOutcome(request.TargetStatus)
		if terminal {
			if s.deploymentAttempts[attemptIndex].Status != "active" {
				return domain.Deployment{}, ErrConflict
			}
			runIndex := -1
			for j, run := range s.runs {
				if run.ID == request.RunID && run.RunnerID != nil && *run.RunnerID == request.RunnerID && run.Status == domain.RunRunning {
					runIndex = j
					break
				}
			}
			if runIndex == -1 {
				return domain.Deployment{}, ErrConflict
			}
			lease.Status = leaseStatus
			lease.CompletedAt = &now
			s.leases[leaseIndex] = lease
			run := s.runs[runIndex]
			run.Status = runStatus
			run.RunnerID = nil
			run.FinishedAt = &now
			s.runs[runIndex] = run
			deployment.FinishedAt = &now
			s.deploymentAttempts[attemptIndex].Status = attemptStatus
			s.deploymentAttempts[attemptIndex].FinishedAt = &now
		}
		if request.TargetStatus == domain.DeploymentSucceeded || (request.TargetStatus == domain.DeploymentRolledBack && deployment.RollbackOfID != nil) {
			for j := range s.environments {
				if s.environments[j].ID == deployment.EnvironmentID {
					s.environments[j].CurrentHealthyRevisionID = stringPointer(deployment.DesiredRevisionID)
				}
			}
		}
		if deployment.RollbackOfID != nil && (request.TargetStatus == domain.DeploymentRolledBack || request.TargetStatus == domain.DeploymentRollbackFailed) {
			for j := range s.deployments {
				if s.deployments[j].ID == *deployment.RollbackOfID && s.deployments[j].Status == domain.DeploymentRollingBack {
					s.deployments[j].Status = request.TargetStatus
					s.deployments[j].UpdatedAt, s.deployments[j].FinishedAt = now, &now
					break
				}
			}
		}
		s.deployments[i] = deployment
		s.deploymentTransitions[key] = request
		s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
		return deployment, nil
	}
	return domain.Deployment{}, ErrNotFound
}

// CancelDeploymentRequest is deliberately not implemented in terms of the
// runner transition API: pre-apply cancellation has no runner fence yet, and
// post-apply cancellation must retain that fence until the runner reconciles
// the target into rollback or manual intervention.
func (s *MemoryStore) CancelDeploymentRequest(_ context.Context, req domain.DeploymentCancelRequest, audit domain.AuditEvent) (domain.Deployment, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deploymentCancels == nil {
		s.deploymentCancels = map[string]domain.DeploymentCancelRequest{}
	}
	key := req.DeploymentID + "\x00" + req.RequestID
	if prior, ok := s.deploymentCancels[key]; ok {
		if prior.ActorID != req.ActorID {
			return domain.Deployment{}, ErrConflict
		}
		for _, d := range s.deployments {
			if d.ID == req.DeploymentID {
				return d, nil
			}
		}
		return domain.Deployment{}, ErrNotFound
	}
	for priorKey := range s.deploymentCancels {
		if strings.HasPrefix(priorKey, req.DeploymentID+"\x00") {
			return domain.Deployment{}, ErrConflict
		}
	}
	now := time.Now().UTC()
	for i := range s.deployments {
		d := s.deployments[i]
		if d.ID != req.DeploymentID {
			continue
		}
		if d.RollbackOfID != nil || domain.IsTerminalDeploymentStatus(d.Status) {
			return domain.Deployment{}, ErrConflict
		}
		switch d.Status {
		case domain.DeploymentQueued, domain.DeploymentWaitingConfirmation, domain.DeploymentAssigned, domain.DeploymentPreparing:
			d.Status, d.UpdatedAt, d.FinishedAt = domain.DeploymentCanceled, now, &now
			if d.TaskRunID != nil {
				for j := range s.runs {
					if s.runs[j].ID == *d.TaskRunID {
						s.runs[j].Status = domain.RunCanceled
						s.runs[j].RunnerID = nil
						s.runs[j].FinishedAt = &now
					}
				}
				for j := range s.leases {
					if s.leases[j].RunID == *d.TaskRunID && s.leases[j].Status == domain.LeaseActive {
						s.leases[j].Status = domain.RunCanceled
						s.leases[j].CompletedAt = &now
					}
				}
				for j := range s.deploymentAttempts {
					if s.deploymentAttempts[j].DeploymentID == d.ID && s.deploymentAttempts[j].Status == "active" {
						s.deploymentAttempts[j].Status = "canceled"
						s.deploymentAttempts[j].FinishedAt = &now
					}
				}
			}
		case domain.DeploymentApplying, domain.DeploymentVerifying:
			d.Status, d.UpdatedAt = domain.DeploymentCancelRequested, now
		default:
			return domain.Deployment{}, ErrConflict
		}
		s.deployments[i] = d
		s.deploymentCancels[key] = req
		audit.CreatedAt = now
		s.auditEvents = append([]domain.AuditEvent{audit}, s.auditEvents...)
		return d, nil
	}
	return domain.Deployment{}, ErrNotFound
}

// FailDeploymentAndCreateRollback is intentionally not expressed as two
// calls to TransitionDeploymentAttempt/CreateDeploymentRequest.  Between
// those calls another deployment could acquire the environment.  Holding the
// store lock makes the source terminalization, lease settlement and rollback
// queue insertion one all-or-nothing fenced mutation.
func (s *MemoryStore) FailDeploymentAndCreateRollback(_ context.Context, req domain.DeploymentFailureRollbackRequest, failedAudit, rollbackAudit domain.AuditEvent) (domain.DeploymentFailureRollbackResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.ExpectedStatus == domain.DeploymentCancelRequested {
		receipt, ok := s.deploymentCancels[req.DeploymentID+"\x00"+req.CancellationRequestID]
		if !ok || req.CancellationRequestID == "" || receipt.RequestID != req.CancellationRequestID || req.RequestID != req.CancellationRequestID {
			return domain.DeploymentFailureRollbackResult{}, ErrConflict
		}
	}
	rollbackDeploymentID, rollbackRunID := domain.RollbackObjectIDs(req.DeploymentID, req.RequestID)
	now := time.Now().UTC()
	key := "rollback:" + req.DeploymentID + ":" + req.RequestID
	for _, existing := range s.deployments {
		if existing.IdempotencyKey == key {
			if existing.ID != rollbackDeploymentID || existing.RollbackOfID == nil || *existing.RollbackOfID != req.DeploymentID || existing.TaskRunID == nil || *existing.TaskRunID != rollbackRunID {
				return domain.DeploymentFailureRollbackResult{}, ErrConflict
			}
			for _, source := range s.deployments {
				if source.ID == req.DeploymentID {
					if source.TaskRunID == nil || *source.TaskRunID != req.RunID {
						return domain.DeploymentFailureRollbackResult{}, ErrConflict
					}
					for _, attempt := range s.deploymentAttempts {
						if attempt.DeploymentID == req.DeploymentID && attempt.RunID == req.RunID && attempt.LeaseID == req.LeaseID && attempt.RunnerID == req.RunnerID && attempt.Attempt == req.Attempt && attempt.Fence == req.Fence {
							stored, ok := s.deploymentTransitions[source.ID+"\x00failure:"+req.RequestID]
							want := domain.DeploymentTransitionRequest{DeploymentID: req.DeploymentID, RunID: req.RunID, LeaseID: req.LeaseID, RunnerID: req.RunnerID, Attempt: req.Attempt, Fence: req.Fence, TransitionKey: "failure:" + req.RequestID, ExpectedStatus: req.ExpectedStatus, TargetStatus: domain.DeploymentRollingBack, FailureCode: req.FailureCode, Metadata: req.Metadata}
							if attempt.Status == "failed" && ok && sameDeploymentTransition(stored, want) {
								return domain.DeploymentFailureRollbackResult{Failed: source, Rollback: existing}, nil
							}
							return domain.DeploymentFailureRollbackResult{}, ErrConflict
						}
					}
					return domain.DeploymentFailureRollbackResult{}, ErrNotFound
				}
			}
			return domain.DeploymentFailureRollbackResult{}, ErrNotFound
		}
	}
	leaseIndex, attemptIndex, depIndex, runIndex := -1, -1, -1, -1
	for i, l := range s.leases {
		if l.ID == req.LeaseID && l.RunID == req.RunID && l.RunnerID == req.RunnerID && l.Attempt == req.Attempt && l.Fence == req.Fence {
			leaseIndex = i
			break
		}
	}
	if leaseIndex < 0 || s.leases[leaseIndex].Status != domain.LeaseActive || !s.leases[leaseIndex].ExpiresAt.After(now) {
		return domain.DeploymentFailureRollbackResult{}, ErrNotFound
	}
	for i, a := range s.deploymentAttempts {
		if a.DeploymentID == req.DeploymentID && a.RunID == req.RunID && a.LeaseID == req.LeaseID && a.RunnerID == req.RunnerID && a.Attempt == req.Attempt && a.Fence == req.Fence && a.Status == "active" {
			attemptIndex = i
			break
		}
	}
	for i, d := range s.deployments {
		if d.ID == req.DeploymentID && d.TaskRunID != nil && *d.TaskRunID == req.RunID {
			depIndex = i
			break
		}
	}
	for i, r := range s.runs {
		if r.ID == req.RunID && r.Status == domain.RunRunning && r.RunnerID != nil && *r.RunnerID == req.RunnerID {
			runIndex = i
			break
		}
	}
	if attemptIndex < 0 || depIndex < 0 || runIndex < 0 {
		return domain.DeploymentFailureRollbackResult{}, ErrNotFound
	}
	source := s.deployments[depIndex]
	if source.Status != req.ExpectedStatus || (source.Status != domain.DeploymentApplying && source.Status != domain.DeploymentVerifying && source.Status != domain.DeploymentCancelRequested) || source.PreviousHealthyRevisionID == nil {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	if req.ExpectedStatus == domain.DeploymentCancelRequested {
		if req.CancellationRequestID == "" || req.RequestID != req.CancellationRequestID {
			return domain.DeploymentFailureRollbackResult{}, ErrConflict
		}
		if receipt, ok := s.deploymentCancels[req.DeploymentID+"\x00"+req.CancellationRequestID]; !ok || receipt.RequestID != req.CancellationRequestID {
			return domain.DeploymentFailureRollbackResult{}, ErrConflict
		}
	}
	var env domain.Environment
	for _, e := range s.environments {
		if e.ID == source.EnvironmentID {
			env = e
			break
		}
	}
	if env.ID == "" || !env.RollbackSafe {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	// Ensure the recorded previous revision remains the last verified target.
	if env.CurrentHealthyRevisionID == nil || *env.CurrentHealthyRevisionID != *source.PreviousHealthyRevisionID {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	for _, d := range s.deployments {
		if d.EnvironmentID == source.EnvironmentID && !domain.IsTerminalDeploymentStatus(d.Status) && d.ID != source.ID {
			return domain.DeploymentFailureRollbackResult{}, ErrConflict
		}
	}
	// The root remains the active environment owner until its linked child
	// proves rollback health or fails loudly; only its execution attempt ends.
	source.Status, source.FailureCode, source.UpdatedAt, source.FinishedAt = domain.DeploymentRollingBack, req.FailureCode, now, nil
	s.deployments[depIndex] = source
	lease := s.leases[leaseIndex]
	lease.Status, lease.CompletedAt = domain.RunFailed, &now
	s.leases[leaseIndex] = lease
	run := s.runs[runIndex]
	run.Status, run.RunnerID, run.FinishedAt = domain.RunFailed, nil, &now
	s.runs[runIndex] = run
	s.deploymentAttempts[attemptIndex].Status, s.deploymentAttempts[attemptIndex].FinishedAt = "failed", &now
	if s.deploymentTransitions == nil {
		s.deploymentTransitions = map[string]domain.DeploymentTransitionRequest{}
	}
	s.deploymentTransitions[source.ID+"\x00failure:"+req.RequestID] = domain.DeploymentTransitionRequest{DeploymentID: source.ID, RunID: req.RunID, LeaseID: req.LeaseID, RunnerID: req.RunnerID, Attempt: req.Attempt, Fence: req.Fence, TransitionKey: "failure:" + req.RequestID, ExpectedStatus: req.ExpectedStatus, TargetStatus: domain.DeploymentRollingBack, FailureCode: req.FailureCode, Metadata: req.Metadata}
	rollback := domain.Deployment{ID: rollbackDeploymentID, EnvironmentID: source.EnvironmentID, DesiredRevisionID: *source.PreviousHealthyRevisionID, PreviousHealthyRevisionID: source.PreviousHealthyRevisionID, IdempotencyKey: "rollback:" + source.ID + ":" + req.RequestID, Status: domain.DeploymentQueued, RequestedBy: req.RunnerID, CreatedAt: now, UpdatedAt: now, RollbackOfID: &source.ID, FenceRequired: true, TaskRunID: stringPointer(rollbackRunID)}
	rollbackRun := domain.TaskRun{ID: rollbackRunID, ProjectID: "", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy, Inputs: map[string]any{"deployment_id": rollbackDeploymentID, "rollback_of_id": source.ID, "desired_revision_id": *source.PreviousHealthyRevisionID}, Secrets: append([]domain.SecretBinding(nil), run.RunSpec.Secrets...)}, Workflow: domain.Workflow{}, WorkflowState: domain.WorkflowState{}, Status: domain.RunQueued, RequestedBy: req.RunnerID, StartedAt: now}
	for _, service := range s.services {
		if service.ID == env.ServiceID {
			rollbackRun.ProjectID = service.ProjectID
			break
		}
	}
	if rollback.ID == "" || rollbackRun.ID == "" || rollbackRun.ProjectID == "" {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	s.runs = append([]domain.TaskRun{rollbackRun}, s.runs...)
	s.claimOrderByRun[rollbackRun.ID] = now
	s.deployments = append(s.deployments, rollback)
	if s.deploymentByID == nil {
		s.deploymentByID = map[string]int{}
	}
	s.deploymentByID[rollback.ID] = len(s.deployments) - 1
	failedAudit.CreatedAt, rollbackAudit.CreatedAt = now, now.Add(time.Microsecond)
	s.auditEvents = append([]domain.AuditEvent{rollbackAudit, failedAudit}, s.auditEvents...)
	return domain.DeploymentFailureRollbackResult{Failed: source, Rollback: rollback}, nil
}

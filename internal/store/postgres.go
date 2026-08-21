package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"nerocd/internal/domain"
	"nerocd/internal/observability"
	"nerocd/internal/store/sqlcgen"
)

type PostgresStore struct {
	pool    *pgxpool.Pool
	queries *sqlcgen.Queries
}

var (
	_ ProjectRepository             = (*PostgresStore)(nil)
	_ ProjectMemberRepository       = (*PostgresStore)(nil)
	_ TemplateRepository            = (*PostgresStore)(nil)
	_ SourceRepository              = (*PostgresStore)(nil)
	_ RunRepository                 = (*PostgresStore)(nil)
	_ RunnerRepository              = (*PostgresStore)(nil)
	_ UserRepository                = (*PostgresStore)(nil)
	_ SessionRepository             = (*PostgresStore)(nil)
	_ APITokenRepository            = (*PostgresStore)(nil)
	_ ApprovalRepository            = (*PostgresStore)(nil)
	_ AuditRepository               = (*PostgresStore)(nil)
	_ DeploymentRepository          = (*PostgresStore)(nil)
	_ OperationalSnapshotRepository = (*PostgresStore)(nil)
	_ OperationalObservationWriter  = (*PostgresStore)(nil)
	_ RunLogRetentionRepository     = (*PostgresStore)(nil)
)

// A claim or periodic reaper transaction may lock at most this many expired
// task runs. Keeping maintenance bounded prevents a large expired fleet from
// turning one runner claim into a global lock/materialization operation.
const leaseExpiryBatchSize = 64

func OpenPostgres(ctx context.Context, databaseURL string) (*PostgresStore, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, err
	}
	config.MaxConns = 20
	config.MinConns = 0
	config.MaxConnLifetime = 30 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	queries := sqlcgen.New(pool)
	compatible, err := queries.SchedulerSchemaCompatible(ctx)
	if err != nil {
		pool.Close()
		return nil, err
	}
	if compatible == nil || !*compatible {
		pool.Close()
		return nil, fmt.Errorf("database schema is incompatible: apply fenced lease, claim cursor, runner replay, and runner enrollment migrations before starting NeroCD")
	}
	return &PostgresStore{pool: pool, queries: queries}, nil
}

// SchemaCompatible is intentionally called for every readiness probe so an
// old or partially-applied schema can never remain ready after startup.
func (s *PostgresStore) SchemaCompatible(ctx context.Context) (bool, error) {
	compatible, err := s.queries.SchedulerSchemaCompatible(ctx)
	if err != nil {
		return false, err
	}
	return compatible != nil && *compatible, nil
}

// OperationalSnapshot reads authoritative aggregate state in one database
// statement. DB clock supplies all ages so an application host clock cannot
// make stale queue/lease signals go backwards.
func (s *PostgresStore) OperationalSnapshot(ctx context.Context) (observability.Snapshot, error) {
	base, err := s.queries.OperationalSnapshotBase(ctx)
	if err != nil {
		return observability.Snapshot{}, err
	}
	snapshot := observability.Snapshot{CollectedAt: base.CollectedAt, QueueDepth: base.Depth, QueueOldestAgeSeconds: maxSnapshotAge(base.QueueOldestAge), ActiveLeases: base.ActiveLeases, ExpiredLeases: base.ExpiredLeases, OldestRunnerHeartbeatSecond: maxSnapshotAge(base.RunnerOldestAge), RunnerJournalDepth: base.JournalDepth, RunnerRetryCount: base.RetryCount, RunnerRenewFailures: base.RenewFailures, BackupScheduleStatus: base.BackupScheduleStatus, BackupScheduleNextSeconds: maxSnapshotAge(base.BackupScheduleNextSeconds), BackupScheduleFailures: int64(base.BackupScheduleFailures)}
	snapshot.TerminalRuns = map[string]observability.DurationAggregate{
		"succeeded": {Count: base.SucceededCount, SumSeconds: maxSnapshotAge(base.SucceededDuration)},
		"failed":    {Count: base.FailedCount, SumSeconds: maxSnapshotAge(base.FailedDuration)},
		"canceled":  {Count: base.CanceledCount, SumSeconds: maxSnapshotAge(base.CanceledDuration)},
	}
	snapshot.Deployments = map[string]int64{}
	counts, err := s.queries.OperationalDeploymentCounts(ctx)
	if err != nil {
		return observability.Snapshot{}, err
	}
	for _, row := range counts {
		snapshot.Deployments[row.Status] = row.Count
	}
	health, err := s.queries.OperationalDeploymentHealth(ctx)
	if err != nil {
		return observability.Snapshot{}, err
	}
	snapshot.DeploymentHealthPassed, snapshot.DeploymentHealthFailed, snapshot.RollbackSucceeded, snapshot.RollbackFailed = health.Count, health.Count_2, health.Count_3, health.Count_4
	snapshot.BackupOutcome, snapshot.BackupReason = observability.BackupNone, "none"
	if base.Outcome != nil {
		snapshot.BackupOutcome = *base.Outcome
	}
	if base.Reason != nil {
		snapshot.BackupReason = *base.Reason
	}
	if base.BackupAge != nil {
		snapshot.BackupAgeSeconds = maxSnapshotAge(*base.BackupAge)
	}
	stat := s.pool.Stat()
	snapshot.Pool = observability.PoolState{Total: int64(stat.TotalConns()), Idle: int64(stat.IdleConns()), Acquired: int64(stat.AcquiredConns())}
	if err := snapshot.Validate(); err != nil {
		return observability.Snapshot{}, err
	}
	return snapshot, nil
}

func maxSnapshotAge(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}

func (s *PostgresStore) RecordRunnerOperationalObservation(ctx context.Context, runnerID string, journalDepth, retryCount, renewFailures int) error {
	if journalDepth < 0 || journalDepth > 8192 || retryCount < 0 || retryCount > 100000 || renewFailures < 0 || renewFailures > 100000 {
		return ErrConflict
	}
	return s.queries.UpsertRunnerOperationalObservation(ctx, sqlcgen.UpsertRunnerOperationalObservationParams{RunnerID: runnerID, JournalDepth: int32(journalDepth), RetryCount: int32(retryCount), RenewFailures: int32(renewFailures)})
}

func (s *PostgresStore) RunnerOperationalObservation(ctx context.Context, runnerID string) (RunnerOperationalObservation, error) {
	value, err := s.queries.GetRunnerOperationalObservation(ctx, runnerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunnerOperationalObservation{}, ErrNotFound
	}
	if err != nil {
		return RunnerOperationalObservation{}, err
	}
	return RunnerOperationalObservation{ObservedAt: value.ObservedAt, JournalDepth: int(value.JournalDepth), RetryCount: int(value.RetryCount), RenewFailures: int(value.RenewFailures)}, nil
}

func serviceFromSQLC(v sqlcgen.Service) domain.Service {
	return domain.Service{ID: v.ID, ProjectID: v.ProjectID, Name: v.Name, RepositoryID: v.RepositoryID, ComposePath: v.ComposePath, Profiles: v.Profiles, OwnerID: v.OwnerID, CreatedAt: v.CreatedAt}
}
func environmentFromSQLC(v sqlcgen.Environment) domain.Environment {
	var x domain.Environment
	_ = json.Unmarshal(v.HealthPolicy, &x.HealthPolicy)
	_ = json.Unmarshal(v.SecretBindings, &x.SecretBindings)
	x.ID = v.ID
	x.ServiceID = v.ServiceID
	x.Name = v.Name
	x.RunnerSelector = v.RunnerSelector
	x.ComposeProject = v.ComposeProject
	x.ConfirmationRequired = v.ConfirmationRequired
	x.TimeoutSeconds = int(v.TimeoutSeconds)
	x.RollbackSafe = v.RollbackSafe
	x.CurrentHealthyRevisionID = v.CurrentHealthyRevisionID
	x.CreatedAt = v.CreatedAt
	return x
}
func revisionFromSQLC(v sqlcgen.Revision) domain.Revision {
	return domain.Revision{ID: v.ID, ServiceID: v.ServiceID, RequestedRef: v.RequestedRef, GitCommit: v.GitCommit, ComposeHash: v.ComposeHash, ImageDigests: v.ImageDigests, ContentIdentity: v.ContentIdentity, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt, ProvenanceResolved: v.ProvenanceResolved, ProvenanceState: v.ProvenanceState, ResolvedAt: v.ResolvedAt}
}
func deploymentFromSQLC(v sqlcgen.Deployment) domain.Deployment {
	return domain.Deployment{ID: v.ID, EnvironmentID: v.EnvironmentID, DesiredRevisionID: v.DesiredRevisionID, PreviousHealthyRevisionID: v.PreviousHealthyRevisionID, TaskRunID: &v.TaskRunID, IdempotencyKey: v.IdempotencyKey, Status: v.Status, RequestedBy: v.RequestedBy, ConfirmedBy: v.ConfirmedBy, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, FinishedAt: v.FinishedAt, HealthPassed: v.HealthPassed, RollbackOfID: v.RollbackOfID, FailureCode: v.FailureCode, FenceRequired: v.FenceRequired}
}

func mustAuditJSON(metadata map[string]any) json.RawMessage {
	encoded, _ := json.Marshal(metadata)
	return encoded
}
func (s *PostgresStore) ListServices(ctx context.Context, p string) ([]domain.Service, error) {
	rows, e := s.queries.ListServices(ctx, p)
	if e != nil {
		return nil, e
	}
	out := make([]domain.Service, 0, len(rows))
	for _, v := range rows {
		out = append(out, serviceFromSQLC(v))
	}
	return out, nil
}
func (s *PostgresStore) CreateService(ctx context.Context, v domain.Service) (domain.Service, error) {
	x, e := s.queries.CreateService(ctx, sqlcgen.CreateServiceParams{ID: v.ID, ProjectID: v.ProjectID, Name: v.Name, RepositoryID: v.RepositoryID, ComposePath: v.ComposePath, Profiles: v.Profiles, OwnerID: v.OwnerID, CreatedAt: v.CreatedAt})
	if isUniqueViolation(e) {
		return domain.Service{}, ErrConflict
	}
	return serviceFromSQLC(x), e
}
func (s *PostgresStore) CreateServiceWithAudit(ctx context.Context, v domain.Service, audit domain.AuditEvent) (domain.Service, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return domain.Service{}, e
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	x, e := q.CreateService(ctx, sqlcgen.CreateServiceParams{ID: v.ID, ProjectID: v.ProjectID, Name: v.Name, RepositoryID: v.RepositoryID, ComposePath: v.ComposePath, Profiles: v.Profiles, OwnerID: v.OwnerID, CreatedAt: v.CreatedAt})
	if isUniqueViolation(e) {
		return domain.Service{}, ErrConflict
	}
	if e != nil {
		return domain.Service{}, e
	}
	if e = createAuditWithQueries(ctx, q, audit); e != nil {
		return domain.Service{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return domain.Service{}, e
	}
	return serviceFromSQLC(x), nil
}
func (s *PostgresStore) ListEnvironments(ctx context.Context, id string) ([]domain.Environment, error) {
	rows, e := s.queries.ListEnvironments(ctx, id)
	if e != nil {
		return nil, e
	}
	out := make([]domain.Environment, 0, len(rows))
	for _, v := range rows {
		out = append(out, environmentFromSQLC(v))
	}
	return out, nil
}
func (s *PostgresStore) CreateEnvironment(ctx context.Context, v domain.Environment) (domain.Environment, error) {
	hp, _ := json.Marshal(v.HealthPolicy)
	sb, _ := json.Marshal(v.SecretBindings)
	x, e := s.queries.CreateEnvironment(ctx, sqlcgen.CreateEnvironmentParams{ID: v.ID, ServiceID: v.ServiceID, Name: v.Name, RunnerSelector: v.RunnerSelector, ComposeProject: v.ComposeProject, HealthPolicy: hp, ConfirmationRequired: v.ConfirmationRequired, TimeoutSeconds: int32(v.TimeoutSeconds), SecretBindings: sb, RollbackSafe: v.RollbackSafe, CurrentHealthyRevisionID: v.CurrentHealthyRevisionID, CreatedAt: v.CreatedAt})
	if isUniqueViolation(e) {
		return domain.Environment{}, ErrConflict
	}
	return environmentFromSQLC(x), e
}
func (s *PostgresStore) CreateEnvironmentWithAudit(ctx context.Context, v domain.Environment, audit domain.AuditEvent) (domain.Environment, error) {
	hp, _ := json.Marshal(v.HealthPolicy)
	sb, _ := json.Marshal(v.SecretBindings)
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return domain.Environment{}, e
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	x, e := q.CreateEnvironment(ctx, sqlcgen.CreateEnvironmentParams{ID: v.ID, ServiceID: v.ServiceID, Name: v.Name, RunnerSelector: v.RunnerSelector, ComposeProject: v.ComposeProject, HealthPolicy: hp, ConfirmationRequired: v.ConfirmationRequired, TimeoutSeconds: int32(v.TimeoutSeconds), SecretBindings: sb, RollbackSafe: v.RollbackSafe, CurrentHealthyRevisionID: v.CurrentHealthyRevisionID, CreatedAt: v.CreatedAt})
	if isUniqueViolation(e) {
		return domain.Environment{}, ErrConflict
	}
	if e != nil {
		return domain.Environment{}, e
	}
	if e = createAuditWithQueries(ctx, q, audit); e != nil {
		return domain.Environment{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return domain.Environment{}, e
	}
	return environmentFromSQLC(x), nil
}
func (s *PostgresStore) ListRevisions(ctx context.Context, id string) ([]domain.Revision, error) {
	rows, e := s.queries.ListRevisions(ctx, id)
	if e != nil {
		return nil, e
	}
	out := make([]domain.Revision, 0, len(rows))
	for _, v := range rows {
		out = append(out, revisionFromSQLC(v))
	}
	return out, nil
}
func (s *PostgresStore) CreateRevision(ctx context.Context, v domain.Revision) (domain.Revision, error) {
	x, e := s.queries.CreateRevision(ctx, sqlcgen.CreateRevisionParams{ID: v.ID, ServiceID: v.ServiceID, RequestedRef: v.RequestedRef, GitCommit: v.GitCommit, ComposeHash: v.ComposeHash, ImageDigests: v.ImageDigests, ContentIdentity: v.ContentIdentity, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt})
	if isUniqueViolation(e) {
		if v.ContentIdentity == "" {
			return domain.Revision{}, ErrConflict
		}
		x, e = s.queries.GetRevisionByIdentity(ctx, sqlcgen.GetRevisionByIdentityParams{ServiceID: v.ServiceID, ContentIdentity: v.ContentIdentity})
		if e == nil {
			return revisionFromSQLC(x), nil
		}
		return domain.Revision{}, ErrConflict
	}
	return revisionFromSQLC(x), e
}
func (s *PostgresStore) CreateRevisionWithAudit(ctx context.Context, v domain.Revision, audit domain.AuditEvent) (domain.Revision, error) {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return domain.Revision{}, e
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	x, e := q.CreateRevision(ctx, sqlcgen.CreateRevisionParams{ID: v.ID, ServiceID: v.ServiceID, RequestedRef: v.RequestedRef, GitCommit: v.GitCommit, ComposeHash: v.ComposeHash, ImageDigests: v.ImageDigests, ContentIdentity: v.ContentIdentity, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt})
	if isUniqueViolation(e) {
		return domain.Revision{}, ErrConflict
	}
	if e != nil {
		return domain.Revision{}, e
	}
	if e = createAuditWithQueries(ctx, q, audit); e != nil {
		return domain.Revision{}, e
	}
	if e = tx.Commit(ctx); e != nil {
		return domain.Revision{}, e
	}
	return revisionFromSQLC(x), nil
}

// DeploymentPlan verifies runner identity, fence and the database clock in the
// same read-only statement.  It deliberately joins only controlled deployment
// configuration and opaque secret binding references.
func (s *PostgresStore) DeploymentPlan(ctx context.Context, deploymentID, runID, leaseID, runnerID string, attempt int, fence string) (domain.DeploymentPlan, error) {
	row, err := s.queries.DeploymentPlan(ctx, sqlcgen.DeploymentPlanParams{ID: deploymentID, TaskRunID: runID, LeaseID: leaseID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence})
	if err == pgx.ErrNoRows {
		return domain.DeploymentPlan{}, ErrNotFound
	}
	if err != nil {
		return domain.DeploymentPlan{}, err
	}
	requestedRef := row.RequestedRef
	// Once provenance is resolved, subsequent attempts—especially a rollback
	// child—must fetch the immutable observed commit, not a mutable branch name.
	if immutableGitCommit(row.GitCommit) {
		requestedRef = row.GitCommit
	}
	p := domain.DeploymentPlan{DeploymentID: row.DeploymentID, Status: row.DeploymentStatus, RunID: row.TaskRunID, LeaseID: row.LeaseID, Attempt: int(row.Attempt), Fence: row.Fence, ProjectID: row.ProjectID, ServiceID: row.ServiceID, EnvironmentID: row.EnvironmentID, RepositoryID: row.RepositoryID, RepositoryURL: row.Url, RequestedRef: requestedRef, ComposePath: row.ComposePath, Profiles: row.Profiles, ComposeProject: row.ComposeProject, TimeoutSeconds: int(row.TimeoutSeconds), RollbackSafe: row.RollbackSafe, PreviousHealthyRevisionID: row.PreviousHealthyRevisionID, RollbackOfID: row.RollbackOfID, CancellationRequestID: row.CancellationRequestID}
	if json.Unmarshal(row.HealthPolicy, &p.HealthPolicy) != nil || json.Unmarshal(row.SecretBindings, &p.SecretBindings) != nil || json.Unmarshal(row.RepositoryPolicy, &p.RepositoryPolicy) != nil {
		return domain.DeploymentPlan{}, ErrConflict
	}
	return p, nil
}

func (s *PostgresStore) ResolveRevisionProvenance(ctx context.Context, deploymentID, runID, leaseID, runnerID string, attempt int, fence, resolutionID, commit, hash string, digests []string, audit domain.AuditEvent) (domain.Revision, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Revision{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	// Replay lookup deliberately precedes lease-expiry validation.  An exact
	// committed acknowledgement is a receipt, not renewed runner authority.
	if v, found, e := provenanceReplay(ctx, q, deploymentID, resolutionID, runID, leaseID, runnerID, attempt, fence, commit, hash, digests); e != nil {
		return domain.Revision{}, e
	} else if found {
		if err = tx.Commit(ctx); err != nil {
			return domain.Revision{}, err
		}
		return v, nil
	}
	// Lock the deployment attempt first.  It serializes competing first
	// resolutions; after acquiring it, recheck the receipt before enforcing
	// the live lease and deployment state.
	row, err := q.LockProvenanceAttempt(ctx, sqlcgen.LockProvenanceAttemptParams{ID: deploymentID, RunID: runID, LeaseID: leaseID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence})
	if err == pgx.ErrNoRows {
		return domain.Revision{}, ErrNotFound
	}
	if err != nil {
		return domain.Revision{}, err
	}
	if v, found, e := provenanceReplay(ctx, q, deploymentID, resolutionID, runID, leaseID, runnerID, attempt, fence, commit, hash, digests); e != nil {
		return domain.Revision{}, e
	} else if found {
		if err = tx.Commit(ctx); err != nil {
			return domain.Revision{}, err
		}
		return v, nil
	}
	active, err := q.ProvenanceAttemptIsActive(ctx, sqlcgen.ProvenanceAttemptIsActiveParams{ID: deploymentID, ID_2: leaseID, RunID: runID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence})
	if err == pgx.ErrNoRows || active == nil || !*active {
		return domain.Revision{}, ErrNotFound
	}
	if err != nil {
		return domain.Revision{}, err
	}
	v := revisionFromSQLC(row)
	if v.ProvenanceResolved {
		// A rollback child deliberately reuses the last healthy revision. It
		// must attest to the same immutable provenance under its own fence,
		// rather than treating an already resolved revision as an error.
		if v.GitCommit != commit || v.ComposeHash != hash || !reflect.DeepEqual(v.ImageDigests, digests) {
			return domain.Revision{}, ErrConflict
		}
	} else {
		if v.ProvenanceState != "pending" {
			return domain.Revision{}, ErrConflict
		}
		updated, err := q.ResolveRevisionProvenance(ctx, sqlcgen.ResolveRevisionProvenanceParams{ID: v.ID, GitCommit: commit, ComposeHash: hash, ImageDigests: digests})
		if isUniqueViolation(err) {
			return domain.Revision{}, ErrConflict
		}
		if err != nil {
			return domain.Revision{}, err
		}
		v = revisionFromSQLC(updated)
	}
	if audit.ID != "" {
		audit.CreatedAt = time.Now().UTC()
		if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
			return domain.Revision{}, err
		}
	}
	err = q.CreateProvenanceResolutionReplay(ctx, sqlcgen.CreateProvenanceResolutionReplayParams{DeploymentID: deploymentID, Attempt: int32(attempt), ResolutionID: resolutionID, RunID: runID, LeaseID: leaseID, RunnerID: runnerID, Fence: fence, RevisionID: v.ID, GitCommit: commit, ComposeHash: hash, ImageDigests: digests, ContentIdentity: commit + ":" + hash, AuditID: audit.ID})
	if isUniqueViolation(err) {
		return domain.Revision{}, ErrConflict
	}
	if err != nil {
		return domain.Revision{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Revision{}, err
	}
	return v, nil
}

// provenanceReplay returns only a byte-for-byte authority/body match.  A
// known resolution key with any changed field is a conflict, never a hint.
func provenanceReplay(ctx context.Context, q *sqlcgen.Queries, deploymentID, resolutionID, runID, leaseID, runnerID string, attempt int, fence, commit, hash string, digests []string) (domain.Revision, bool, error) {
	row, err := q.GetProvenanceResolutionReplay(ctx, sqlcgen.GetProvenanceResolutionReplayParams{DeploymentID: deploymentID, ResolutionID: resolutionID})
	if err == pgx.ErrNoRows {
		return domain.Revision{}, false, nil
	}
	if err != nil {
		return domain.Revision{}, false, err
	}
	if row.RunID != runID || row.LeaseID != leaseID || row.RunnerID != runnerID || int(row.Attempt) != attempt || row.Fence != fence || row.ReplayGitCommit != commit || row.ReplayComposeHash != hash || !reflect.DeepEqual(row.ReplayImageDigests, digests) {
		return domain.Revision{}, false, ErrConflict
	}
	return revisionFromSQLC(sqlcgen.Revision{ID: row.ID, ServiceID: row.ServiceID, RequestedRef: row.RequestedRef, GitCommit: row.GitCommit, ComposeHash: row.ComposeHash, ImageDigests: row.ImageDigests, ContentIdentity: row.ContentIdentity, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, ProvenanceState: row.ProvenanceState, ProvenanceResolved: row.ProvenanceResolved, ResolvedAt: row.ResolvedAt}), true, nil
}
func (s *PostgresStore) ListDeployments(ctx context.Context, id string) ([]domain.Deployment, error) {
	rows, e := s.queries.ListDeployments(ctx, id)
	if e != nil {
		return nil, e
	}
	out := make([]domain.Deployment, 0, len(rows))
	for _, v := range rows {
		out = append(out, deploymentFromSQLC(v))
	}
	return out, nil
}

func (s *PostgresStore) GetDeployment(ctx context.Context, id string) (domain.Deployment, error) {
	row, err := s.queries.GetDeploymentByID(ctx, id)
	if err == pgx.ErrNoRows {
		return domain.Deployment{}, ErrNotFound
	}
	if err != nil {
		return domain.Deployment{}, err
	}
	return deploymentFromSQLC(row), nil
}

func (s *PostgresStore) GetEnvironment(ctx context.Context, id string) (domain.Environment, error) {
	row, err := s.queries.GetEnvironmentByID(ctx, id)
	if err == pgx.ErrNoRows {
		return domain.Environment{}, ErrNotFound
	}
	if err != nil {
		return domain.Environment{}, err
	}
	return environmentFromSQLC(row), nil
}

func (s *PostgresStore) GetService(ctx context.Context, id string) (domain.Service, error) {
	row, err := s.queries.GetServiceByID(ctx, id)
	if err == pgx.ErrNoRows {
		return domain.Service{}, ErrNotFound
	}
	if err != nil {
		return domain.Service{}, err
	}
	return serviceFromSQLC(row), nil
}

// CreateDeploymentRequest persists the typed, non-shell deployment plan and
// its first-class deployment record together. A client never supplies the run
// linkage, so it cannot attach an arbitrary generic execution to a deployment.
func (s *PostgresStore) CreateDeploymentRequest(ctx context.Context, v domain.Deployment, run domain.TaskRun, audit domain.AuditEvent) (domain.Deployment, error) {
	raw, err := json.Marshal(run.RunSpec)
	if err != nil {
		return domain.Deployment{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Deployment{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	if v.RollbackOfID != nil {
		return domain.Deployment{}, ErrConflict
	}
	if replay, lookupErr := q.GetDeploymentByEnvironmentKey(ctx, sqlcgen.GetDeploymentByEnvironmentKeyParams{EnvironmentID: v.EnvironmentID, IdempotencyKey: v.IdempotencyKey}); lookupErr == nil {
		if replay.DesiredRevisionID != v.DesiredRevisionID {
			return domain.Deployment{}, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.Deployment{}, err
		}
		return deploymentFromSQLC(replay), nil
	} else if lookupErr != pgx.ErrNoRows {
		return domain.Deployment{}, lookupErr
	}
	if _, err = q.CreateDeploymentRun(ctx, sqlcgen.CreateDeploymentRunParams{ID: run.ID, ProjectID: run.ProjectID, RunSpec: raw, RunnerTags: run.RunnerTags, Status: run.Status, RequestedBy: run.RequestedBy}); err != nil {
		return domain.Deployment{}, err
	}
	v.TaskRunID = &run.ID
	x, err := q.CreateDeployment(ctx, sqlcgen.CreateDeploymentParams{ID: v.ID, EnvironmentID: v.EnvironmentID, DesiredRevisionID: v.DesiredRevisionID, PreviousHealthyRevisionID: v.PreviousHealthyRevisionID, TaskRunID: run.ID, IdempotencyKey: v.IdempotencyKey, Status: v.Status, RequestedBy: v.RequestedBy, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, FenceRequired: true, RollbackOfID: v.RollbackOfID})
	if isUniqueViolation(err) {
		return domain.Deployment{}, ErrConflict
	}
	if err != nil {
		return domain.Deployment{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Deployment{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Deployment{}, err
	}
	return deploymentFromSQLC(x), nil
}

func (s *PostgresStore) ConfirmDeployment(ctx context.Context, id, confirmedBy string, audit domain.AuditEvent) (domain.Deployment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Deployment{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	confirmed, err := q.ConfirmDeployment(ctx, sqlcgen.ConfirmDeploymentParams{ID: id, ConfirmedBy: &confirmedBy})
	if err == pgx.ErrNoRows {
		return domain.Deployment{}, ErrConflict
	}
	if err != nil {
		return domain.Deployment{}, err
	}
	if err = q.QueueConfirmedDeploymentRun(ctx, confirmed.TaskRunID); err != nil {
		return domain.Deployment{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Deployment{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Deployment{}, err
	}
	return deploymentFromSQLC(confirmed), nil
}

// FailPreAssignmentDeployment is deliberately separate from the runner
// protocol: it can only record validation failures before a run is assigned.
// Once assigned, an exact lease/fence deployment transition is the only
// execution authority.
func (s *PostgresStore) FailPreAssignmentDeployment(ctx context.Context, id, failureCode string, audit domain.AuditEvent) (domain.Deployment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Deployment{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	current, err := q.LockDeploymentByID(ctx, id)
	if err == pgx.ErrNoRows {
		return domain.Deployment{}, ErrNotFound
	}
	if err != nil {
		return domain.Deployment{}, err
	}
	if current.Status == domain.DeploymentFailed {
		if current.FailureCode != failureCode {
			return domain.Deployment{}, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.Deployment{}, err
		}
		return deploymentFromSQLC(current), nil
	}
	if current.Status != domain.DeploymentQueued && current.Status != domain.DeploymentWaitingConfirmation {
		return domain.Deployment{}, ErrConflict
	}
	now, err := q.DatabaseClock(ctx)
	if err != nil {
		return domain.Deployment{}, err
	}
	failed, err := q.FailPreAssignmentDeployment(ctx, sqlcgen.FailPreAssignmentDeploymentParams{ID: id, FailureCode: failureCode})
	if err == pgx.ErrNoRows {
		return domain.Deployment{}, ErrConflict
	}
	if err != nil {
		return domain.Deployment{}, err
	}
	if changed, changeErr := q.FailPreAssignmentDeploymentRun(ctx, failed.TaskRunID); changeErr != nil {
		return domain.Deployment{}, changeErr
	} else if changed != 1 {
		return domain.Deployment{}, ErrConflict
	}
	audit.CreatedAt = now
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Deployment{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Deployment{}, err
	}
	return deploymentFromSQLC(failed), nil
}

func deploymentTransitionReplayMatches(row sqlcgen.DeploymentTransition, request domain.DeploymentTransitionRequest, metadata json.RawMessage) bool {
	var persisted, requested any
	if json.Unmarshal(row.Metadata, &persisted) != nil || json.Unmarshal(metadata, &requested) != nil || !reflect.DeepEqual(persisted, requested) {
		return false
	}
	return int(row.Attempt) == request.Attempt && row.ExpectedStatus == request.ExpectedStatus && row.TargetStatus == request.TargetStatus &&
		((row.HealthPassed == nil && request.HealthPassed == nil) || (row.HealthPassed != nil && request.HealthPassed != nil && *row.HealthPassed == *request.HealthPassed)) &&
		row.FailureCode == request.FailureCode
}

func deploymentTerminalOutcome(status domain.DeploymentStatus) (attemptStatus, leaseStatus, runStatus string, terminal bool) {
	switch status {
	case domain.DeploymentSucceeded, domain.DeploymentRolledBack:
		return "succeeded", domain.RunSucceeded, domain.RunSucceeded, true
	case domain.DeploymentFailed, domain.DeploymentRollbackFailed, domain.DeploymentManualIntervention:
		return "failed", domain.RunFailed, domain.RunFailed, true
	case domain.DeploymentCanceled:
		return "canceled", domain.RunCanceled, domain.RunCanceled, true
	default:
		return "", "", "", false
	}
}

// Generic task-run mutations must never alter the run that a deployment owns.
// Deployment transitions are the sole authority for that lifecycle; callers
// that need to act on it must supply the exact runner/lease/attempt/fence.
func rejectGenericDeploymentRun(ctx context.Context, queries *sqlcgen.Queries, runID string) error {
	isDeployment, err := queries.IsDeploymentRun(ctx, runID)
	if err != nil {
		return err
	}
	if isDeployment {
		return ErrConflict
	}
	return nil
}

// TransitionDeploymentAttempt follows the completion transaction's lock order:
// task run, per-run advisory log lock, deployment linkage/attempt, exact lease. The
// replay record is checked before expiry only when its complete fenced body
// matches; it cannot grant new mutation authority after reassignment.
func (s *PostgresStore) TransitionDeploymentAttempt(ctx context.Context, request domain.DeploymentTransitionRequest, audit domain.AuditEvent) (domain.Deployment, error) {
	metadata, err := json.Marshal(request.Metadata)
	if err != nil {
		return domain.Deployment{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Deployment{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	runID, err := q.GetLeaseRunID(ctx, request.LeaseID)
	if err == pgx.ErrNoRows {
		return domain.Deployment{}, ErrNotFound
	}
	if err != nil {
		return domain.Deployment{}, err
	}
	if runID != request.RunID {
		return domain.Deployment{}, ErrNotFound
	}
	if _, err = q.LockRunID(ctx, runID); err != nil {
		return domain.Deployment{}, err
	}
	if err = q.AcquireRunLogLock(ctx, runID); err != nil {
		return domain.Deployment{}, err
	}
	deployment, err := q.LockDeploymentForRun(ctx, runID)
	if err == pgx.ErrNoRows {
		return domain.Deployment{}, ErrNotFound
	}
	if err != nil {
		return domain.Deployment{}, err
	}
	if deployment.ID != request.DeploymentID {
		return domain.Deployment{}, ErrNotFound
	}
	attemptRow, err := q.LockDeploymentAttempt(ctx, sqlcgen.LockDeploymentAttemptParams{DeploymentID: request.DeploymentID, RunID: request.RunID, LeaseID: request.LeaseID, RunnerID: request.RunnerID, Attempt: int32(request.Attempt), Fence: request.Fence})
	if err == pgx.ErrNoRows {
		return domain.Deployment{}, ErrNotFound
	} else if err != nil {
		return domain.Deployment{}, err
	}
	if replay, replayErr := q.GetDeploymentTransitionReplay(ctx, sqlcgen.GetDeploymentTransitionReplayParams{DeploymentID: request.DeploymentID, TransitionKey: request.TransitionKey}); replayErr == nil {
		if !deploymentTransitionReplayMatches(replay, request, metadata) {
			return domain.Deployment{}, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.Deployment{}, err
		}
		return deploymentFromSQLC(deployment), nil
	} else if replayErr != pgx.ErrNoRows {
		return domain.Deployment{}, replayErr
	}
	if _, err = q.LockAuthorizedLease(ctx, sqlcgen.LockAuthorizedLeaseParams{LeaseID: request.LeaseID, RunID: request.RunID, RunnerID: request.RunnerID, Attempt: int32(request.Attempt), Fence: request.Fence}); err == pgx.ErrNoRows {
		return domain.Deployment{}, ErrNotFound
	} else if err != nil {
		return domain.Deployment{}, err
	}
	if !domain.DeploymentRoleTransitionAllowed(deployment.RollbackOfID != nil, deployment.Status, request.TargetStatus) || deployment.Status != request.ExpectedStatus || ((request.TargetStatus == domain.DeploymentSucceeded || (request.TargetStatus == domain.DeploymentRolledBack && deployment.RollbackOfID != nil)) && (request.HealthPassed == nil || !*request.HealthPassed)) {
		return domain.Deployment{}, ErrConflict
	}
	if request.TargetStatus == domain.DeploymentApplying {
		var resolved bool
		if resolved, err = q.IsRevisionProvenanceResolved(ctx, deployment.DesiredRevisionID); err != nil {
			return domain.Deployment{}, err
		}
		if !resolved {
			return domain.Deployment{}, ErrConflict
		}
	}
	attemptStatus, leaseStatus, runStatus, terminal := deploymentTerminalOutcome(request.TargetStatus)
	updated, err := q.FencedTransitionDeployment(ctx, sqlcgen.FencedTransitionDeploymentParams{ID: request.DeploymentID, Status: request.ExpectedStatus, Status_2: request.TargetStatus, HealthPassed: request.HealthPassed, FailureCode: request.FailureCode, Terminal: terminal})
	if err == pgx.ErrNoRows {
		return domain.Deployment{}, ErrConflict
	}
	if err != nil {
		return domain.Deployment{}, err
	}
	if request.TargetStatus == domain.DeploymentSucceeded || (request.TargetStatus == domain.DeploymentRolledBack && updated.RollbackOfID != nil) {
		desired := updated.DesiredRevisionID
		if err = q.CommitDeploymentHealthyRevision(ctx, sqlcgen.CommitDeploymentHealthyRevisionParams{ID: updated.EnvironmentID, CurrentHealthyRevisionID: &desired}); err != nil {
			return domain.Deployment{}, err
		}
	}
	// A linked rollback is the only child permitted while its root holds the
	// environment lock. Its terminal state atomically releases that root.
	if updated.RollbackOfID != nil && (request.TargetStatus == domain.DeploymentRolledBack || request.TargetStatus == domain.DeploymentRollbackFailed) {
		sourceStatus := domain.DeploymentRolledBack
		if request.TargetStatus == domain.DeploymentRollbackFailed {
			sourceStatus = domain.DeploymentRollbackFailed
		}
		if _, err = q.FinishRollbackSource(ctx, sqlcgen.FinishRollbackSourceParams{ID: *updated.RollbackOfID, Status: sourceStatus}); err == pgx.ErrNoRows {
			return domain.Deployment{}, ErrConflict
		} else if err != nil {
			return domain.Deployment{}, err
		}
	}
	if terminal {
		if attemptRow.Status != "active" {
			return domain.Deployment{}, ErrConflict
		}
		if _, err = q.CompleteDeploymentLease(ctx, sqlcgen.CompleteDeploymentLeaseParams{ID: request.LeaseID, RunID: request.RunID, RunnerID: request.RunnerID, Attempt: int32(request.Attempt), Fence: request.Fence, Status: leaseStatus}); err == pgx.ErrNoRows {
			return domain.Deployment{}, ErrConflict
		} else if err != nil {
			return domain.Deployment{}, err
		}
		runnerID := request.RunnerID
		updatedRows, updateErr := q.CompleteDeploymentRun(ctx, sqlcgen.CompleteDeploymentRunParams{ID: request.RunID, RunnerID: &runnerID, Status: runStatus})
		if updateErr != nil {
			return domain.Deployment{}, updateErr
		}
		if updatedRows != 1 {
			return domain.Deployment{}, ErrConflict
		}
		if err = q.FinishDeploymentAttempt(ctx, sqlcgen.FinishDeploymentAttemptParams{DeploymentID: request.DeploymentID, Attempt: int32(request.Attempt), Status: attemptStatus}); err != nil {
			return domain.Deployment{}, err
		}
	}
	if err = q.CreateDeploymentTransitionReplay(ctx, sqlcgen.CreateDeploymentTransitionReplayParams{DeploymentID: request.DeploymentID, Attempt: int32(request.Attempt), TransitionKey: request.TransitionKey, ExpectedStatus: request.ExpectedStatus, TargetStatus: request.TargetStatus, HealthPassed: request.HealthPassed, FailureCode: request.FailureCode, Metadata: metadata}); err != nil {
		return domain.Deployment{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Deployment{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Deployment{}, err
	}
	return deploymentFromSQLC(updated), nil
}

// CancelDeploymentRequest serializes a maintainer receipt with the deployment
// and its run. Pre-apply cancellation settles every owned record in the same
// transaction; post-apply cancellation intentionally preserves the active
// lease so the fenced runner can inspect the target and enter rollback.
func (s *PostgresStore) CancelDeploymentRequest(ctx context.Context, req domain.DeploymentCancelRequest, audit domain.AuditEvent) (domain.Deployment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Deployment{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	d, err := q.LockDeploymentByID(ctx, req.DeploymentID)
	if err == pgx.ErrNoRows {
		return domain.Deployment{}, ErrNotFound
	}
	if err != nil {
		return domain.Deployment{}, err
	}
	if d.RollbackOfID != nil {
		return domain.Deployment{}, ErrConflict
	}
	if receipt, receiptErr := q.GetDeploymentCancellation(ctx, req.DeploymentID); receiptErr == nil {
		if receipt.RequestID != req.RequestID || receipt.ActorID != req.ActorID {
			return domain.Deployment{}, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.Deployment{}, err
		}
		return deploymentFromSQLC(d), nil
	} else if receiptErr != pgx.ErrNoRows {
		return domain.Deployment{}, receiptErr
	}
	if domain.IsTerminalDeploymentStatus(d.Status) {
		return domain.Deployment{}, ErrConflict
	}
	if err = q.CreateDeploymentCancellation(ctx, sqlcgen.CreateDeploymentCancellationParams{DeploymentID: req.DeploymentID, RequestID: req.RequestID, ActorID: req.ActorID}); err != nil {
		return domain.Deployment{}, err
	}
	audit.CreatedAt, err = q.DatabaseClock(ctx)
	if err != nil {
		return domain.Deployment{}, err
	}
	var updated sqlcgen.Deployment
	switch d.Status {
	case domain.DeploymentQueued, domain.DeploymentWaitingConfirmation, domain.DeploymentAssigned, domain.DeploymentPreparing:
		if _, err = q.LockRunID(ctx, d.TaskRunID); err != nil {
			return domain.Deployment{}, err
		}
		if err = q.AcquireRunLogLock(ctx, d.TaskRunID); err != nil {
			return domain.Deployment{}, err
		}
		updated, err = q.CancelDeploymentBeforeApply(ctx, d.ID)
		if err != nil {
			return domain.Deployment{}, err
		}
		if err = q.CancelDeploymentActiveLeases(ctx, d.TaskRunID); err != nil {
			return domain.Deployment{}, err
		}
		if err = q.CancelDeploymentActiveAttempts(ctx, d.ID); err != nil {
			return domain.Deployment{}, err
		}
		if rows, runErr := q.CancelDeploymentRun(ctx, d.TaskRunID); runErr != nil || rows != 1 {
			if runErr != nil {
				return domain.Deployment{}, runErr
			}
			return domain.Deployment{}, ErrConflict
		}
	case domain.DeploymentApplying, domain.DeploymentVerifying:
		updated, err = q.RequestDeploymentCancellation(ctx, d.ID)
		if err != nil {
			return domain.Deployment{}, err
		}
	default:
		return domain.Deployment{}, ErrConflict
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Deployment{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Deployment{}, err
	}
	return deploymentFromSQLC(updated), nil
}

// FailDeploymentAndCreateRollback is the post-apply failure boundary.  It
// deliberately uses one transaction: a source deployment cannot be marked
// failed and release the environment before its rollback run exists.
func (s *PostgresStore) FailDeploymentAndCreateRollback(ctx context.Context, req domain.DeploymentFailureRollbackRequest, failedAudit, rollbackAudit domain.AuditEvent) (domain.DeploymentFailureRollbackResult, error) {
	if (req.ExpectedStatus != domain.DeploymentApplying && req.ExpectedStatus != domain.DeploymentVerifying && req.ExpectedStatus != domain.DeploymentCancelRequested) || req.RequestID == "" || (req.ExpectedStatus == domain.DeploymentCancelRequested && req.CancellationRequestID == "") {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	rollbackDeploymentID, rollbackRunID := domain.RollbackObjectIDs(req.DeploymentID, req.RequestID)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	if req.ExpectedStatus == domain.DeploymentCancelRequested {
		receipt, receiptErr := q.GetDeploymentCancellation(ctx, req.DeploymentID)
		if receiptErr != nil || req.CancellationRequestID == "" || receipt.RequestID != req.CancellationRequestID || req.RequestID != req.CancellationRequestID {
			return domain.DeploymentFailureRollbackResult{}, ErrConflict
		}
	}
	key := "rollback:" + req.DeploymentID + ":" + req.RequestID
	// A replay is accepted even when its old lease expired. The persisted
	// rollback record is the response receipt; no new authority is granted.
	environmentID, envErr := q.DeploymentEnvironmentID(ctx, req.DeploymentID)
	if envErr != nil && envErr != pgx.ErrNoRows {
		return domain.DeploymentFailureRollbackResult{}, envErr
	}
	if replay, e := q.GetDeploymentByEnvironmentKey(ctx, sqlcgen.GetDeploymentByEnvironmentKeyParams{EnvironmentID: environmentID, IdempotencyKey: key}); e == nil {
		return replayFailedDeploymentRollback(ctx, tx, q, req, replay, key)
	} else if e != pgx.ErrNoRows {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	// Lock in the same order as fenced transitions: run, deployment/attempt,
	// lease, then environment through the rollback relationship trigger.
	runID, e := q.GetLeaseRunID(ctx, req.LeaseID)
	if e == pgx.ErrNoRows || runID != req.RunID {
		return domain.DeploymentFailureRollbackResult{}, ErrNotFound
	}
	if e != nil {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	if _, e = q.LockRunID(ctx, req.RunID); e != nil {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	if e = q.AcquireRunLogLock(ctx, req.RunID); e != nil {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	source, e := q.LockDeploymentForRun(ctx, req.RunID)
	if e == pgx.ErrNoRows || source.ID != req.DeploymentID {
		return domain.DeploymentFailureRollbackResult{}, ErrNotFound
	}
	if e != nil {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	if req.ExpectedStatus == domain.DeploymentCancelRequested && source.Status == domain.DeploymentManualIntervention {
		transition, transitionErr := q.GetDeploymentTransitionReplay(ctx, sqlcgen.GetDeploymentTransitionReplayParams{DeploymentID: req.DeploymentID, TransitionKey: "failure:" + req.RequestID})
		if transitionErr != nil || transition.TargetStatus != domain.DeploymentManualIntervention || transition.ExpectedStatus != req.ExpectedStatus || transition.FailureCode != req.FailureCode {
			return domain.DeploymentFailureRollbackResult{}, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.DeploymentFailureRollbackResult{}, err
		}
		return domain.DeploymentFailureRollbackResult{Failed: deploymentFromSQLC(source)}, nil
	}
	if req.ExpectedStatus == domain.DeploymentCancelRequested {
		receipt, receiptErr := q.GetDeploymentCancellation(ctx, req.DeploymentID)
		if receiptErr == pgx.ErrNoRows || receiptErr != nil || receipt.RequestID != req.CancellationRequestID || req.RequestID != req.CancellationRequestID {
			return domain.DeploymentFailureRollbackResult{}, ErrConflict
		}
	}
	attempt, e := q.LockDeploymentAttempt(ctx, sqlcgen.LockDeploymentAttemptParams{DeploymentID: req.DeploymentID, RunID: req.RunID, LeaseID: req.LeaseID, RunnerID: req.RunnerID, Attempt: int32(req.Attempt), Fence: req.Fence})
	if e == pgx.ErrNoRows {
		return domain.DeploymentFailureRollbackResult{}, ErrNotFound
	}
	if e != nil {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	// A concurrent first report can have queried before the child existed and
	// then waited on this source lock. Recheck after the serialization point so
	// it observes the committed receipt instead of treating the now-terminal
	// source attempt as a fresh mutation.
	if replay, replayErr := q.GetDeploymentByEnvironmentKey(ctx, sqlcgen.GetDeploymentByEnvironmentKeyParams{EnvironmentID: source.EnvironmentID, IdempotencyKey: key}); replayErr == nil {
		return replayFailedDeploymentRollback(ctx, tx, q, req, replay, key)
	} else if replayErr != pgx.ErrNoRows {
		return domain.DeploymentFailureRollbackResult{}, replayErr
	}
	if _, e = q.LockAuthorizedLease(ctx, sqlcgen.LockAuthorizedLeaseParams{LeaseID: req.LeaseID, RunID: req.RunID, RunnerID: req.RunnerID, Attempt: int32(req.Attempt), Fence: req.Fence}); e == pgx.ErrNoRows {
		return domain.DeploymentFailureRollbackResult{}, ErrNotFound
	}
	if e != nil {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	if source.Status != req.ExpectedStatus || attempt.Status != "active" {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	environment, e := q.LockRollbackEnvironment(ctx, source.EnvironmentID)
	if e != nil {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	if req.ExpectedStatus == domain.DeploymentCancelRequested && (source.PreviousHealthyRevisionID == nil || !environment.RollbackSafe || environment.CurrentHealthyRevisionID == nil || (source.PreviousHealthyRevisionID != nil && *environment.CurrentHealthyRevisionID != *source.PreviousHealthyRevisionID)) {
		return finishCancellationManualIntervention(ctx, tx, q, req, failedAudit)
	}
	if source.PreviousHealthyRevisionID == nil {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	if !environment.RollbackSafe || environment.CurrentHealthyRevisionID == nil || *environment.CurrentHealthyRevisionID != *source.PreviousHealthyRevisionID {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	metadata, e := json.Marshal(req.Metadata)
	if e != nil {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	updated, e := q.FencedTransitionDeployment(ctx, sqlcgen.FencedTransitionDeploymentParams{ID: req.DeploymentID, Status: req.ExpectedStatus, Status_2: domain.DeploymentRollingBack, FailureCode: req.FailureCode, Terminal: false})
	if e == pgx.ErrNoRows {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	if e != nil {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	if _, e = q.CompleteDeploymentLease(ctx, sqlcgen.CompleteDeploymentLeaseParams{ID: req.LeaseID, RunID: req.RunID, RunnerID: req.RunnerID, Attempt: int32(req.Attempt), Fence: req.Fence, Status: domain.RunFailed}); e != nil {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	runnerID := req.RunnerID
	if rows, e := q.CompleteDeploymentRun(ctx, sqlcgen.CompleteDeploymentRunParams{ID: req.RunID, RunnerID: &runnerID, Status: domain.RunFailed}); e != nil || rows != 1 {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	if e = q.FinishDeploymentAttempt(ctx, sqlcgen.FinishDeploymentAttemptParams{DeploymentID: req.DeploymentID, Attempt: int32(req.Attempt), Status: "failed"}); e != nil {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	if e = q.CreateDeploymentTransitionReplay(ctx, sqlcgen.CreateDeploymentTransitionReplayParams{DeploymentID: req.DeploymentID, Attempt: int32(req.Attempt), TransitionKey: "failure:" + req.RequestID, ExpectedStatus: req.ExpectedStatus, TargetStatus: domain.DeploymentRollingBack, FailureCode: req.FailureCode, Metadata: metadata}); e != nil {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	projectID, e := q.DeploymentProjectID(ctx, source.EnvironmentID)
	if e != nil {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	sourceRun, e := q.GetRun(ctx, req.RunID)
	if e != nil {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	var sourceSpec domain.RunSpec
	if json.Unmarshal(sourceRun.RunSpec, &sourceSpec) != nil {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	// A rollback is a fresh fenced run, but it needs the same opaque secret
	// descriptors as the failed Compose run to fetch its immutable source.
	raw, _ := json.Marshal(domain.RunSpec{Type: domain.RunTypeComposeDeploy, Inputs: map[string]any{"deployment_id": rollbackDeploymentID, "rollback_of_id": source.ID, "desired_revision_id": *source.PreviousHealthyRevisionID}, Secrets: append([]domain.SecretBinding(nil), sourceSpec.Secrets...)})
	if _, e = q.CreateDeploymentRun(ctx, sqlcgen.CreateDeploymentRunParams{ID: rollbackRunID, ProjectID: projectID, RunSpec: raw, RunnerTags: []string{}, Status: domain.RunQueued, RequestedBy: source.RequestedBy}); e != nil {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	rollback, e := q.CreateRollbackDeploymentIfAbsent(ctx, sqlcgen.CreateRollbackDeploymentIfAbsentParams{ID: rollbackDeploymentID, EnvironmentID: source.EnvironmentID, DesiredRevisionID: *source.PreviousHealthyRevisionID, PreviousHealthyRevisionID: source.PreviousHealthyRevisionID, TaskRunID: rollbackRunID, IdempotencyKey: key, Status: domain.DeploymentQueued, RequestedBy: source.RequestedBy, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), FenceRequired: true, RollbackOfID: &source.ID})
	if e == pgx.ErrNoRows {
		// Another exact request committed while this transaction was waiting on
		// the source lock. Its durable child is the receipt; validate every
		// fenced field before acknowledging it rather than surfacing a unique
		// violation or accidentally accepting a changed replay body.
		replay, lookupErr := q.GetDeploymentByEnvironmentKey(ctx, sqlcgen.GetDeploymentByEnvironmentKeyParams{EnvironmentID: source.EnvironmentID, IdempotencyKey: key})
		if lookupErr != nil {
			return domain.DeploymentFailureRollbackResult{}, lookupErr
		}
		return replayFailedDeploymentRollback(ctx, tx, q, req, replay, key)
	}
	if e != nil {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	if e = createAuditWithQueries(ctx, q, failedAudit); e != nil {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	if e = createAuditWithQueries(ctx, q, rollbackAudit); e != nil {
		return domain.DeploymentFailureRollbackResult{}, e
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	return domain.DeploymentFailureRollbackResult{Failed: deploymentFromSQLC(updated), Rollback: deploymentFromSQLC(rollback)}, nil
}

// finishCancellationManualIntervention is the deliberate loud fallback when a
// cancel receipt is real but there is no safe immutable rollback target. It
// uses the same fence and transaction as the normal handoff, but never creates
// a child that could overwrite an unknown target.
func finishCancellationManualIntervention(ctx context.Context, tx pgx.Tx, q *sqlcgen.Queries, req domain.DeploymentFailureRollbackRequest, audit domain.AuditEvent) (domain.DeploymentFailureRollbackResult, error) {
	metadata, err := json.Marshal(req.Metadata)
	if err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	updated, err := q.FencedTransitionDeployment(ctx, sqlcgen.FencedTransitionDeploymentParams{ID: req.DeploymentID, Status: domain.DeploymentCancelRequested, Status_2: domain.DeploymentManualIntervention, FailureCode: req.FailureCode, Terminal: true})
	if err != nil {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	if _, err = q.CompleteDeploymentLease(ctx, sqlcgen.CompleteDeploymentLeaseParams{ID: req.LeaseID, RunID: req.RunID, RunnerID: req.RunnerID, Attempt: int32(req.Attempt), Fence: req.Fence, Status: domain.RunFailed}); err != nil {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	runnerID := req.RunnerID
	if rows, err := q.CompleteDeploymentRun(ctx, sqlcgen.CompleteDeploymentRunParams{ID: req.RunID, RunnerID: &runnerID, Status: domain.RunFailed}); err != nil || rows != 1 {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	if err = q.FinishDeploymentAttempt(ctx, sqlcgen.FinishDeploymentAttemptParams{DeploymentID: req.DeploymentID, Attempt: int32(req.Attempt), Status: "failed"}); err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	if err = q.CreateDeploymentTransitionReplay(ctx, sqlcgen.CreateDeploymentTransitionReplayParams{DeploymentID: req.DeploymentID, Attempt: int32(req.Attempt), TransitionKey: "failure:" + req.RequestID, ExpectedStatus: req.ExpectedStatus, TargetStatus: domain.DeploymentManualIntervention, FailureCode: req.FailureCode, Metadata: metadata}); err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	return domain.DeploymentFailureRollbackResult{Failed: deploymentFromSQLC(updated)}, nil
}

// replayFailedDeploymentRollback accepts a response-loss retry only if it is
// the same fenced operation.  A durable child alone is not authority to
// acknowledge a changed request body after the source lease has expired.
func replayFailedDeploymentRollback(ctx context.Context, tx pgx.Tx, q *sqlcgen.Queries, req domain.DeploymentFailureRollbackRequest, replay sqlcgen.Deployment, key string) (domain.DeploymentFailureRollbackResult, error) {
	rollbackDeploymentID, rollbackRunID := domain.RollbackObjectIDs(req.DeploymentID, req.RequestID)
	if replay.ID != rollbackDeploymentID || replay.RollbackOfID == nil || *replay.RollbackOfID != req.DeploymentID || replay.TaskRunID != rollbackRunID || replay.IdempotencyKey != key {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	source, err := q.LockDeploymentByID(ctx, req.DeploymentID)
	if err == pgx.ErrNoRows {
		return domain.DeploymentFailureRollbackResult{}, ErrNotFound
	}
	if err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	if source.TaskRunID != req.RunID {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	attempt, err := q.LockDeploymentAttempt(ctx, sqlcgen.LockDeploymentAttemptParams{DeploymentID: req.DeploymentID, RunID: req.RunID, LeaseID: req.LeaseID, RunnerID: req.RunnerID, Attempt: int32(req.Attempt), Fence: req.Fence})
	if err == pgx.ErrNoRows {
		return domain.DeploymentFailureRollbackResult{}, ErrNotFound
	}
	if err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	metadata, err := json.Marshal(req.Metadata)
	if err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	transition, err := q.GetDeploymentTransitionReplay(ctx, sqlcgen.GetDeploymentTransitionReplayParams{DeploymentID: req.DeploymentID, TransitionKey: "failure:" + req.RequestID})
	if err == pgx.ErrNoRows {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	if err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	if attempt.Status != "failed" || !deploymentTransitionReplayMatches(transition, domain.DeploymentTransitionRequest{Attempt: req.Attempt, ExpectedStatus: req.ExpectedStatus, TargetStatus: domain.DeploymentRollingBack, FailureCode: req.FailureCode, Metadata: req.Metadata}, metadata) {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	return domain.DeploymentFailureRollbackResult{Failed: deploymentFromSQLC(source), Rollback: deploymentFromSQLC(replay)}, nil
}

func (s *PostgresStore) Close() error {
	s.pool.Close()
	return nil
}

func (s *PostgresStore) GetUserByEmail(ctx context.Context, email string) (domain.User, error) {
	user, err := s.queries.GetUserByEmail(ctx, email)
	if err == pgx.ErrNoRows {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	return userFromSQLC(user), nil
}

func (s *PostgresStore) UpdatePasswordHash(ctx context.Context, userID, previousHash, passwordHash string) error {
	updated, err := s.queries.UpdatePasswordHash(ctx, sqlcgen.UpdatePasswordHashParams{ID: userID, PasswordHash: passwordHash, PasswordHash_2: previousHash})
	if err != nil {
		return err
	}
	if updated != 1 {
		return ErrConflict
	}
	return nil
}

func (s *PostgresStore) BootstrapAdmin(ctx context.Context, user domain.User, audit domain.AuditEvent) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	completedBy := user.ID
	completedAt := user.CreatedAt
	claimed, err := q.ClaimBootstrapAdmin(ctx, sqlcgen.ClaimBootstrapAdminParams{CompletedBy: &completedBy, CompletedAt: &completedAt})
	if err != nil {
		return err
	}
	if claimed != 1 {
		denied := domain.AuditEvent{ID: audit.ID + "-denied", ActorID: "system", Action: "identity.bootstrap_admin.denied", TargetID: "bootstrap-admin", Metadata: map[string]any{"reason": "already_completed"}, CreatedAt: user.CreatedAt}
		if err = createAuditWithQueries(ctx, q, denied); err != nil {
			return err
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		return ErrConflict
	}
	if err = q.CreateBootstrapUser(ctx, sqlcgen.CreateBootstrapUserParams{ID: user.ID, Email: user.Email, Name: user.Name, Status: user.Status, GlobalRole: user.GlobalRole, PasswordHash: user.PasswordHash, CreatedAt: user.CreatedAt}); err != nil {
		return err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) BootstrapComplete(ctx context.Context) (bool, error) {
	completed, err := s.queries.BootstrapComplete(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	return completed, err
}

func (s *PostgresStore) CreateSession(ctx context.Context, session domain.Session, tokenHash string) error {
	return s.queries.CreateSession(ctx, sqlcgen.CreateSessionParams{ID: session.ID, UserID: session.UserID, TokenHash: tokenHash, ExpiresAt: session.ExpiresAt, CreatedAt: session.CreatedAt, SourceIp: session.SourceIP, UserAgent: session.UserAgent, LastSeenAt: session.LastSeenAt})
}

func (s *PostgresStore) CreateSessionWithAudit(ctx context.Context, session domain.Session, tokenHash string, audit domain.AuditEvent) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	if err = q.CreateSession(ctx, sqlcgen.CreateSessionParams{ID: session.ID, UserID: session.UserID, TokenHash: tokenHash, ExpiresAt: session.ExpiresAt, CreatedAt: session.CreatedAt, SourceIp: session.SourceIP, UserAgent: session.UserAgent, LastSeenAt: session.LastSeenAt}); err != nil {
		return err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func sessionFromFields(id, userID string, expiresAt, createdAt time.Time, sourceIP, userAgent string, lastSeenAt, revokedAt *time.Time) domain.Session {
	return domain.Session{ID: id, UserID: userID, ExpiresAt: expiresAt, CreatedAt: createdAt, SourceIP: sourceIP, UserAgent: userAgent, LastSeenAt: lastSeenAt, RevokedAt: revokedAt}
}

func (s *PostgresStore) ListSessions(ctx context.Context) ([]domain.Session, error) {
	rows, err := s.queries.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]domain.Session, 0, len(rows))
	for _, row := range rows {
		result = append(result, sessionFromFields(row.ID, row.UserID, row.ExpiresAt, row.CreatedAt, row.SourceIp, row.UserAgent, row.LastSeenAt, row.RevokedAt))
	}
	return result, nil
}

func (s *PostgresStore) RevokeSessionByID(ctx context.Context, id string, revokedAt time.Time) (domain.Session, error) {
	row, err := s.queries.RevokeSessionByID(ctx, sqlcgen.RevokeSessionByIDParams{ID: id, RevokedAt: &revokedAt})
	if err == pgx.ErrNoRows {
		return domain.Session{}, ErrNotFound
	}
	if err != nil {
		return domain.Session{}, err
	}
	return sessionFromFields(row.ID, row.UserID, row.ExpiresAt, row.CreatedAt, row.SourceIp, row.UserAgent, row.LastSeenAt, row.RevokedAt), nil
}

func (s *PostgresStore) RevokeSessionByIDWithAudit(ctx context.Context, id string, revokedAt time.Time, audit domain.AuditEvent) (domain.Session, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Session{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.RevokeSessionByID(ctx, sqlcgen.RevokeSessionByIDParams{ID: id, RevokedAt: &revokedAt})
	if err == pgx.ErrNoRows {
		return domain.Session{}, ErrNotFound
	}
	if err != nil {
		return domain.Session{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Session{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Session{}, err
	}
	return sessionFromFields(row.ID, row.UserID, row.ExpiresAt, row.CreatedAt, row.SourceIp, row.UserAgent, row.LastSeenAt, row.RevokedAt), nil
}

func (s *PostgresStore) GetPrincipalBySessionTokenHash(ctx context.Context, tokenHash string, now time.Time) (domain.User, error) {
	threshold := now.Add(-SessionLastSeenUpdateInterval)
	user, err := s.queries.GetPrincipalBySessionTokenHash(ctx, sqlcgen.GetPrincipalBySessionTokenHashParams{TokenHash: tokenHash, ExpiresAt: now, LastSeenAt: &threshold})
	if err == pgx.ErrNoRows {
		return domain.User{}, ErrNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	return userFromSQLC(user), nil
}

func (s *PostgresStore) RevokeSessionByTokenHash(ctx context.Context, tokenHash string, revokedAt time.Time) error {
	count, err := s.queries.RevokeSessionByTokenHash(ctx, sqlcgen.RevokeSessionByTokenHashParams{TokenHash: tokenHash, RevokedAt: &revokedAt})
	if err != nil {
		return err
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) RevokeSessionByTokenHashWithAudit(ctx context.Context, tokenHash string, revokedAt time.Time, audit domain.AuditEvent) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	rows, err := q.RevokeSessionByTokenHash(ctx, sqlcgen.RevokeSessionByTokenHashParams{TokenHash: tokenHash, RevokedAt: &revokedAt})
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrNotFound
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) CreateAPIToken(ctx context.Context, token domain.APIToken) (domain.APIToken, error) {
	inserted, err := s.queries.CreateAPIToken(ctx, sqlcgen.CreateAPITokenParams{ID: token.ID, Name: token.Name, Kind: token.Kind, TokenHash: token.TokenHash, Roles: token.Roles, Status: token.Status, CreatedBy: token.CreatedBy, CreatedAt: token.CreatedAt, ExpiresAt: token.ExpiresAt})
	if err != nil {
		return domain.APIToken{}, err
	}
	return apiTokenFromSQLC(inserted), nil
}

func (s *PostgresStore) CreateAPITokenWithAudit(ctx context.Context, token domain.APIToken, audit domain.AuditEvent) (domain.APIToken, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.APIToken{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.CreateAPIToken(ctx, sqlcgen.CreateAPITokenParams{ID: token.ID, Name: token.Name, Kind: token.Kind, TokenHash: token.TokenHash, Roles: token.Roles, Status: token.Status, CreatedBy: token.CreatedBy, CreatedAt: token.CreatedAt, ExpiresAt: token.ExpiresAt})
	if err != nil {
		return domain.APIToken{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.APIToken{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.APIToken{}, err
	}
	return apiTokenFromSQLC(row), nil
}

func (s *PostgresStore) GetAPITokenByHash(ctx context.Context, tokenHash string, now time.Time) (domain.APIToken, error) {
	token, err := s.queries.GetAPITokenByHash(ctx, sqlcgen.GetAPITokenByHashParams{TokenHash: tokenHash, LastUsedAt: &now})
	if err == pgx.ErrNoRows {
		return domain.APIToken{}, ErrNotFound
	}
	if err != nil {
		return domain.APIToken{}, err
	}
	return apiTokenFromSQLC(token), nil
}

func (s *PostgresStore) RevokeAPIToken(ctx context.Context, tokenID string, revokedAt time.Time) (domain.APIToken, error) {
	token, err := s.queries.RevokeAPIToken(ctx, sqlcgen.RevokeAPITokenParams{ID: tokenID, RevokedAt: &revokedAt})
	if err == pgx.ErrNoRows {
		return domain.APIToken{}, ErrNotFound
	}
	if err != nil {
		return domain.APIToken{}, err
	}
	return apiTokenFromSQLC(token), nil
}

func (s *PostgresStore) RevokeAPITokenWithAudit(ctx context.Context, tokenID string, revokedAt time.Time, audit domain.AuditEvent) (domain.APIToken, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.APIToken{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.RevokeAPIToken(ctx, sqlcgen.RevokeAPITokenParams{ID: tokenID, RevokedAt: &revokedAt})
	if err == pgx.ErrNoRows {
		return domain.APIToken{}, ErrNotFound
	}
	if err != nil {
		return domain.APIToken{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.APIToken{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.APIToken{}, err
	}
	return apiTokenFromSQLC(row), nil
}

func (s *PostgresStore) ListProjects(ctx context.Context) ([]domain.Project, error) {
	rows, err := s.queries.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	projects := make([]domain.Project, 0, len(rows))
	for _, row := range rows {
		projects = append(projects, projectFromSQLC(row))
	}
	return projects, nil
}

func (s *PostgresStore) CreateProject(ctx context.Context, project domain.Project) (domain.Project, error) {
	inserted, err := s.queries.CreateProject(ctx, sqlcgen.CreateProjectParams{ID: project.ID, Name: project.Name, Description: project.Description, CreatedAt: project.CreatedAt})
	if err != nil {
		return domain.Project{}, err
	}
	return projectFromSQLC(inserted), nil
}

func (s *PostgresStore) CreateProjectWithOwner(ctx context.Context, project domain.Project, owner domain.ProjectMember, audit domain.AuditEvent) (domain.Project, error) {
	if owner.ProjectID != project.ID || owner.UserID == "" || owner.Role != domain.RoleOwner {
		return domain.Project{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	inserted, err := q.CreateProject(ctx, sqlcgen.CreateProjectParams{ID: project.ID, Name: project.Name, Description: project.Description, CreatedAt: project.CreatedAt})
	if err != nil {
		return domain.Project{}, err
	}
	if _, err = q.UpsertProjectMember(ctx, sqlcgen.UpsertProjectMemberParams{ID: owner.ID, ProjectID: owner.ProjectID, UserID: owner.UserID, Role: owner.Role, CreatedAt: owner.CreatedAt, UpdatedAt: owner.UpdatedAt}); err != nil {
		return domain.Project{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Project{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Project{}, err
	}
	return projectFromSQLC(inserted), nil
}

func (s *PostgresStore) UpdateProject(ctx context.Context, project domain.Project) (domain.Project, error) {
	updated, err := s.queries.UpdateProject(ctx, sqlcgen.UpdateProjectParams{ID: project.ID, Name: project.Name, Description: project.Description})
	if err == pgx.ErrNoRows {
		return domain.Project{}, ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	return projectFromSQLC(updated), nil
}

func (s *PostgresStore) UpdateProjectWithAudit(ctx context.Context, project domain.Project, audit domain.AuditEvent) (domain.Project, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	updated, err := q.UpdateProject(ctx, sqlcgen.UpdateProjectParams{ID: project.ID, Name: project.Name, Description: project.Description})
	if err == pgx.ErrNoRows {
		return domain.Project{}, ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Project{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Project{}, err
	}
	return projectFromSQLC(updated), nil
}

func (s *PostgresStore) ArchiveProject(ctx context.Context, id string, archivedAt time.Time) (domain.Project, error) {
	archived, err := s.queries.ArchiveProject(ctx, sqlcgen.ArchiveProjectParams{ID: id, ArchivedAt: &archivedAt})
	if err == pgx.ErrNoRows {
		return domain.Project{}, ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	return projectFromSQLC(archived), nil
}

func (s *PostgresStore) ArchiveProjectWithAudit(ctx context.Context, id string, archivedAt time.Time, audit domain.AuditEvent) (domain.Project, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Project{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	archived, err := q.ArchiveProject(ctx, sqlcgen.ArchiveProjectParams{ID: id, ArchivedAt: &archivedAt})
	if err == pgx.ErrNoRows {
		return domain.Project{}, ErrNotFound
	}
	if err != nil {
		return domain.Project{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Project{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Project{}, err
	}
	return projectFromSQLC(archived), nil
}

func (s *PostgresStore) ListProjectMembers(ctx context.Context, projectID string) ([]domain.ProjectMember, error) {
	rows, err := s.queries.ListProjectMembers(ctx, projectID)
	if err != nil {
		return nil, err
	}
	members := make([]domain.ProjectMember, 0, len(rows))
	for _, row := range rows {
		members = append(members, projectMemberListRowFromSQLC(row))
	}
	return members, nil
}

func (s *PostgresStore) UpsertProjectMember(ctx context.Context, member domain.ProjectMember) (domain.ProjectMember, error) {
	upserted, err := s.queries.UpsertProjectMember(ctx, sqlcgen.UpsertProjectMemberParams{ID: member.ID, ProjectID: member.ProjectID, UserID: member.UserID, Role: member.Role, CreatedAt: member.CreatedAt, UpdatedAt: member.UpdatedAt})
	if err != nil {
		return domain.ProjectMember{}, err
	}
	user, err := s.queries.GetUserByID(ctx, upserted.UserID)
	if err == pgx.ErrNoRows {
		return domain.ProjectMember{}, ErrNotFound
	}
	if err != nil {
		return domain.ProjectMember{}, err
	}
	return projectMemberFromSQLC(upserted, user), nil
}

func (s *PostgresStore) UpsertProjectMemberWithAudit(ctx context.Context, member domain.ProjectMember, audit domain.AuditEvent) (domain.ProjectMember, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.ProjectMember{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	upserted, err := q.UpsertProjectMember(ctx, sqlcgen.UpsertProjectMemberParams{ID: member.ID, ProjectID: member.ProjectID, UserID: member.UserID, Role: member.Role, CreatedAt: member.CreatedAt, UpdatedAt: member.UpdatedAt})
	if err != nil {
		return domain.ProjectMember{}, err
	}
	user, err := q.GetUserByID(ctx, upserted.UserID)
	if err == pgx.ErrNoRows {
		return domain.ProjectMember{}, ErrNotFound
	}
	if err != nil {
		return domain.ProjectMember{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.ProjectMember{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.ProjectMember{}, err
	}
	return projectMemberFromSQLC(upserted, user), nil
}

func (s *PostgresStore) ListRepositories(ctx context.Context, projectID string) ([]domain.Repository, error) {
	rows, err := s.queries.ListRepositories(ctx, projectID)
	if err != nil {
		return nil, err
	}
	repositories := make([]domain.Repository, 0, len(rows))
	for _, row := range rows {
		repository, err := repositoryFromSQLC(row)
		if err != nil {
			return nil, err
		}
		repositories = append(repositories, repository)
	}
	return repositories, nil
}

func (s *PostgresStore) CreateRepository(ctx context.Context, repository domain.Repository) (domain.Repository, error) {
	policy, err := json.Marshal(repository.Policy)
	if err != nil {
		return domain.Repository{}, err
	}
	inserted, err := s.queries.CreateRepository(ctx, sqlcgen.CreateRepositoryParams{ID: repository.ID, ProjectID: repository.ProjectID, Name: repository.Name, Url: repository.URL, Provider: repository.Provider, DefaultRef: repository.DefaultRef, RepositoryPolicy: policy, CreatedAt: repository.CreatedAt})
	if err != nil {
		return domain.Repository{}, err
	}
	return repositoryFromSQLC(inserted)
}
func (s *PostgresStore) CreateRepositoryWithAudit(ctx context.Context, repository domain.Repository, audit domain.AuditEvent) (domain.Repository, error) {
	policy, err := json.Marshal(repository.Policy)
	if err != nil {
		return domain.Repository{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Repository{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.CreateRepository(ctx, sqlcgen.CreateRepositoryParams{ID: repository.ID, ProjectID: repository.ProjectID, Name: repository.Name, Url: repository.URL, Provider: repository.Provider, DefaultRef: repository.DefaultRef, RepositoryPolicy: policy, CreatedAt: repository.CreatedAt})
	if err != nil {
		return domain.Repository{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Repository{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Repository{}, err
	}
	return repositoryFromSQLC(row)
}
func (s *PostgresStore) ConfigureRepositoryPolicy(ctx context.Context, request RepositoryPolicyConfiguration) (domain.Repository, error) {
	raw, err := json.Marshal(request.Policy)
	if err != nil {
		return domain.Repository{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Repository{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)
	row, err := q.LockRepositoryForPolicyConfiguration(ctx, sqlcgen.LockRepositoryForPolicyConfigurationParams{ID: request.RepositoryID, ProjectID: request.ProjectID})
	if err == pgx.ErrNoRows {
		return domain.Repository{}, ErrNotFound
	}
	if err != nil {
		return domain.Repository{}, err
	}
	repository, err := repositoryFromSQLC(row)
	if err != nil {
		return domain.Repository{}, err
	}
	authorized, err := q.RepositoryPolicyActorAuthorized(ctx, sqlcgen.RepositoryPolicyActorAuthorizedParams{ProjectID: request.ProjectID, ID: request.ActorID})
	if err != nil {
		return domain.Repository{}, err
	}
	if !authorized {
		return domain.Repository{}, ErrNotFound
	}
	receipt, err := q.GetRepositoryPolicyConfigurationReceipt(ctx, sqlcgen.GetRepositoryPolicyConfigurationReceiptParams{RepositoryID: request.RepositoryID, ConfigurationID: request.ConfigurationID})
	if err == nil {
		if receipt.ActorID != request.ActorID || receipt.PolicySha256 != request.PolicyHash {
			return domain.Repository{}, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.Repository{}, err
		}
		return repository, nil
	}
	if err != pgx.ErrNoRows {
		return domain.Repository{}, err
	}
	if repository.Policy.State != "legacy_unverified" {
		return domain.Repository{}, ErrConflict
	}
	if err = q.SetRepositoryPolicyConfiguration(ctx, sqlcgen.SetRepositoryPolicyConfigurationParams{ID: request.RepositoryID, RepositoryPolicy: raw}); err != nil {
		return domain.Repository{}, err
	}
	metadata, err := json.Marshal(request.Audit.Metadata)
	if err != nil {
		return domain.Repository{}, err
	}
	if err = q.CreateAuditEventAtDatabaseTime(ctx, sqlcgen.CreateAuditEventAtDatabaseTimeParams{ID: request.Audit.ID, ActorID: request.Audit.ActorID, Action: request.Audit.Action, TargetID: request.Audit.TargetID, Metadata: metadata}); err != nil {
		return domain.Repository{}, err
	}
	if err = q.CreateRepositoryPolicyConfigurationReceipt(ctx, sqlcgen.CreateRepositoryPolicyConfigurationReceiptParams{RepositoryID: request.RepositoryID, ConfigurationID: request.ConfigurationID, ActorID: request.ActorID, PolicySha256: request.PolicyHash, AuditID: request.Audit.ID}); err != nil {
		return domain.Repository{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Repository{}, err
	}
	repository.Policy = request.Policy
	return repository, nil
}

func (s *PostgresStore) ListAccessKeys(ctx context.Context, projectID string) ([]domain.AccessKey, error) {
	rows, err := s.queries.ListAccessKeys(ctx, projectID)
	if err != nil {
		return nil, err
	}
	keys := make([]domain.AccessKey, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, accessKeyFromSQLC(row))
	}
	return keys, nil
}

func (s *PostgresStore) CreateAccessKey(ctx context.Context, key domain.AccessKey) (domain.AccessKey, error) {
	inserted, err := s.queries.CreateAccessKey(ctx, sqlcgen.CreateAccessKeyParams{ID: key.ID, ProjectID: key.ProjectID, Name: key.Name, Kind: key.Kind, Fingerprint: key.Fingerprint, CreatedAt: key.CreatedAt})
	if err != nil {
		return domain.AccessKey{}, err
	}
	return accessKeyFromSQLC(inserted), nil
}
func (s *PostgresStore) CreateAccessKeyWithAudit(ctx context.Context, key domain.AccessKey, audit domain.AuditEvent) (domain.AccessKey, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.AccessKey{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.CreateAccessKey(ctx, sqlcgen.CreateAccessKeyParams{ID: key.ID, ProjectID: key.ProjectID, Name: key.Name, Kind: key.Kind, Fingerprint: key.Fingerprint, CreatedAt: key.CreatedAt})
	if err != nil {
		return domain.AccessKey{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.AccessKey{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.AccessKey{}, err
	}
	return accessKeyFromSQLC(row), nil
}

func (s *PostgresStore) ListInventories(ctx context.Context, projectID string) ([]domain.Inventory, error) {
	rows, err := s.queries.ListInventories(ctx, projectID)
	if err != nil {
		return nil, err
	}
	inventories := make([]domain.Inventory, 0, len(rows))
	for _, row := range rows {
		inventories = append(inventories, inventoryFromSQLC(row))
	}
	return inventories, nil
}

func (s *PostgresStore) CreateInventory(ctx context.Context, inventory domain.Inventory) (domain.Inventory, error) {
	inserted, err := s.queries.CreateInventory(ctx, sqlcgen.CreateInventoryParams{ID: inventory.ID, ProjectID: inventory.ProjectID, Name: inventory.Name, Kind: inventory.Kind, Source: inventory.Source, CreatedAt: inventory.CreatedAt})
	if err != nil {
		return domain.Inventory{}, err
	}
	return inventoryFromSQLC(inserted), nil
}
func (s *PostgresStore) CreateInventoryWithAudit(ctx context.Context, inventory domain.Inventory, audit domain.AuditEvent) (domain.Inventory, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Inventory{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.CreateInventory(ctx, sqlcgen.CreateInventoryParams{ID: inventory.ID, ProjectID: inventory.ProjectID, Name: inventory.Name, Kind: inventory.Kind, Source: inventory.Source, CreatedAt: inventory.CreatedAt})
	if err != nil {
		return domain.Inventory{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Inventory{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Inventory{}, err
	}
	return inventoryFromSQLC(row), nil
}

func (s *PostgresStore) ListTemplates(ctx context.Context, projectID string) ([]domain.TaskTemplate, error) {
	rows, err := s.queries.ListTemplates(ctx, projectID)
	if err != nil {
		return nil, err
	}
	templates := make([]domain.TaskTemplate, 0, len(rows))
	for _, row := range rows {
		template, err := taskTemplateFromSQLC(row)
		if err != nil {
			return nil, err
		}
		templates = append(templates, template)
	}
	return templates, nil
}

func (s *PostgresStore) GetTemplate(ctx context.Context, id string) (domain.TaskTemplate, error) {
	template, err := s.queries.GetTemplate(ctx, id)
	if err == pgx.ErrNoRows {
		return domain.TaskTemplate{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	return taskTemplateFromSQLC(template)
}

func (s *PostgresStore) CreateTemplate(ctx context.Context, template domain.TaskTemplate) (domain.TaskTemplate, error) {
	runSpec, workflow, err := templateJSON(template)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	inserted, err := s.queries.CreateTemplate(ctx, sqlcgen.CreateTemplateParams{ID: template.ID, ProjectID: template.ProjectID, Name: template.Name, Kind: template.Kind, RunSpec: runSpec, Workflow: workflow, RunnerTags: template.RunnerTags, RequiresAck: template.RequiresAck})
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	return taskTemplateFromSQLC(inserted)
}
func (s *PostgresStore) CreateTemplateWithAudit(ctx context.Context, template domain.TaskTemplate, audit domain.AuditEvent) (domain.TaskTemplate, error) {
	runSpec, workflow, err := templateJSON(template)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.CreateTemplate(ctx, sqlcgen.CreateTemplateParams{ID: template.ID, ProjectID: template.ProjectID, Name: template.Name, Kind: template.Kind, RunSpec: runSpec, Workflow: workflow, RunnerTags: template.RunnerTags, RequiresAck: template.RequiresAck})
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.TaskTemplate{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.TaskTemplate{}, err
	}
	return taskTemplateFromSQLC(row)
}

func (s *PostgresStore) UpdateTemplate(ctx context.Context, template domain.TaskTemplate) (domain.TaskTemplate, error) {
	runSpec, workflow, err := templateJSON(template)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	updated, err := s.queries.UpdateTemplate(ctx, sqlcgen.UpdateTemplateParams{ID: template.ID, Name: template.Name, Kind: template.Kind, RunSpec: runSpec, Workflow: workflow, RunnerTags: template.RunnerTags, RequiresAck: template.RequiresAck})
	if err == pgx.ErrNoRows {
		return domain.TaskTemplate{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	return taskTemplateFromSQLC(updated)
}
func (s *PostgresStore) UpdateTemplateWithAudit(ctx context.Context, template domain.TaskTemplate, audit domain.AuditEvent) (domain.TaskTemplate, error) {
	runSpec, workflow, err := templateJSON(template)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.UpdateTemplate(ctx, sqlcgen.UpdateTemplateParams{ID: template.ID, Name: template.Name, Kind: template.Kind, RunSpec: runSpec, Workflow: workflow, RunnerTags: template.RunnerTags, RequiresAck: template.RequiresAck})
	if err == pgx.ErrNoRows {
		return domain.TaskTemplate{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskTemplate{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.TaskTemplate{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.TaskTemplate{}, err
	}
	return taskTemplateFromSQLC(row)
}

func (s *PostgresStore) ListRuns(ctx context.Context, projectID string) ([]domain.TaskRun, error) {
	result, err := s.ListRunsPage(ctx, projectID, Page{})
	return result.Items, err
}

func (s *PostgresStore) ListRunsPage(ctx context.Context, projectID string, page Page) (PageResult[domain.TaskRun], error) {
	total64, err := s.queries.CountRuns(ctx, projectID)
	if err != nil {
		return PageResult[domain.TaskRun]{}, err
	}
	total := int(total64)
	limit, offset := resolvePage(page, total)
	rows, err := s.queries.ListRunsPage(ctx, sqlcgen.ListRunsPageParams{ProjectID: projectID, PageLimit: int32(limit), PageOffset: int32(offset)})
	if err != nil {
		return PageResult[domain.TaskRun]{}, err
	}
	runs := make([]domain.TaskRun, 0, len(rows))
	for _, row := range rows {
		run, err := taskRunFromSQLC(row)
		if err != nil {
			return PageResult[domain.TaskRun]{}, err
		}
		runs = append(runs, run)
	}
	return PageResult[domain.TaskRun]{Items: runs, Limit: limit, Offset: offset, Total: total}, nil
}

func (s *PostgresStore) CreateRun(ctx context.Context, run domain.TaskRun) (domain.TaskRun, error) {
	runSpec, workflow, state, err := runJSON(run)
	if err != nil {
		return domain.TaskRun{}, err
	}
	inserted, err := s.queries.CreateRun(ctx, sqlcgen.CreateRunParams{ID: run.ID, ProjectID: run.ProjectID, TemplateID: run.TemplateID, RunSpec: runSpec, Workflow: workflow, WorkflowState: state, RunnerTags: run.RunnerTags, Status: run.Status, RequestedBy: run.RequestedBy, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt})
	if err != nil {
		return domain.TaskRun{}, err
	}
	return taskRunFromSQLC(inserted)
}

func (s *PostgresStore) CreateRunRequest(ctx context.Context, run domain.TaskRun, log domain.RunLog, approval *domain.Approval, audit domain.AuditEvent) (domain.TaskRun, error) {
	runSpec, workflow, state, err := runJSON(run)
	if err != nil {
		return domain.TaskRun{}, err
	}
	metadata, err := json.Marshal(audit.Metadata)
	if err != nil {
		return domain.TaskRun{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.TaskRun{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	inserted, err := queries.CreateRun(ctx, sqlcgen.CreateRunParams{ID: run.ID, ProjectID: run.ProjectID, TemplateID: run.TemplateID, RunSpec: runSpec, Workflow: workflow, WorkflowState: state, RunnerTags: run.RunnerTags, Status: run.Status, RequestedBy: run.RequestedBy, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt})
	if err != nil {
		return domain.TaskRun{}, err
	}
	run, err = taskRunFromSQLC(inserted)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if _, err := insertRunLogWithSequence(ctx, tx, log); err != nil {
		return domain.TaskRun{}, err
	}
	if approval != nil {
		if _, err := queries.CreateApproval(ctx, sqlcgen.CreateApprovalParams{ID: approval.ID, RunID: approval.RunID, Status: approval.Status, RequestedBy: approval.RequestedBy, ApprovedBy: approval.ApprovedBy, CreatedAt: approval.CreatedAt, ApprovedAt: approval.ApprovedAt}); err != nil {
			return domain.TaskRun{}, err
		}
	}
	if err := queries.CreateAuditEvent(ctx, sqlcgen.CreateAuditEventParams{ID: audit.ID, ActorID: audit.ActorID, Action: audit.Action, TargetID: audit.TargetID, Metadata: metadata, CreatedAt: audit.CreatedAt}); err != nil {
		return domain.TaskRun{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TaskRun{}, err
	}
	return run, nil
}

func (s *PostgresStore) UpdateRunStatus(ctx context.Context, id string, status string, finishedAt *time.Time) (domain.TaskRun, error) {
	if err := rejectGenericDeploymentRun(ctx, s.queries, id); err != nil {
		return domain.TaskRun{}, err
	}
	updated, err := s.queries.UpdateRunStatus(ctx, sqlcgen.UpdateRunStatusParams{ID: id, Status: status, FinishedAt: finishedAt})
	if err == pgx.ErrNoRows {
		return domain.TaskRun{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskRun{}, err
	}
	return taskRunFromSQLC(updated)
}

func (s *PostgresStore) UpdateRunWorkflowState(ctx context.Context, id string, workflowState domain.WorkflowState) (domain.TaskRun, error) {
	if err := rejectGenericDeploymentRun(ctx, s.queries, id); err != nil {
		return domain.TaskRun{}, err
	}
	raw, err := json.Marshal(workflowState)
	if err != nil {
		return domain.TaskRun{}, err
	}
	updated, err := s.queries.UpdateRunWorkflowState(ctx, sqlcgen.UpdateRunWorkflowStateParams{ID: id, WorkflowState: raw})
	if err == pgx.ErrNoRows {
		return domain.TaskRun{}, ErrNotFound
	}
	if err != nil {
		return domain.TaskRun{}, err
	}
	return taskRunFromSQLC(updated)
}

func (s *PostgresStore) CreateRunLog(ctx context.Context, log domain.RunLog) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	if err := rejectGenericDeploymentRun(ctx, queries, log.RunID); err != nil {
		return err
	}
	if err := queries.AcquireRunLogLock(ctx, log.RunID); err != nil {
		return err
	}
	if _, err := insertRunLogWithSequence(ctx, tx, log); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) CreateRunLogForLease(ctx context.Context, log domain.RunLog, runnerID, leaseID string, attempt int, fence string, now time.Time) (domain.RunLog, error) {
	if log.EventKey != "" {
		logs, err := s.CreateRunLogsForLease(ctx, []domain.RunLog{log}, log.RunID, runnerID, leaseID, attempt, fence, now)
		if err != nil {
			return domain.RunLog{}, err
		}
		return logs[0], nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.RunLog{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	// Serialize all log allocation for a run, including controller and runner events.
	if err := queries.AcquireRunLogLock(ctx, log.RunID); err != nil {
		return domain.RunLog{}, err
	}
	if _, err := queries.LockAuthorizedLease(ctx, sqlcgen.LockAuthorizedLeaseParams{LeaseID: leaseID, RunID: log.RunID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence}); err == pgx.ErrNoRows {
		return domain.RunLog{}, ErrNotFound
	} else if err != nil {
		return domain.RunLog{}, err
	}
	log, err = insertRunLogWithSequence(ctx, tx, log)
	if err != nil {
		return domain.RunLog{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RunLog{}, err
	}
	return log, nil
}

func (s *PostgresStore) CreateRunLogsForLease(ctx context.Context, logs []domain.RunLog, runID, runnerID, leaseID string, attempt int, fence string, _ time.Time) ([]domain.RunLog, error) {
	if len(logs) == 0 {
		return []domain.RunLog{}, nil
	}
	for _, log := range logs {
		if log.RunID != runID || log.EventKey == "" || log.RequestedSequence <= 0 || log.LeaseID != leaseID || log.Attempt != attempt {
			return nil, ErrConflict
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	if err := queries.AcquireRunLogLock(ctx, runID); err != nil {
		return nil, err
	}
	// Exact replay is permitted after expiry, but only for the complete original
	// attempt identity. A persisted event key must not turn a partial or forged
	// capability into replay authority.
	if _, err := queries.GetLeaseReplayIdentity(ctx, sqlcgen.GetLeaseReplayIdentityParams{
		LeaseID: leaseID, RunID: runID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence,
	}); err == pgx.ErrNoRows {
		return nil, ErrNotFound
	} else if err != nil {
		return nil, err
	}
	results := make([]domain.RunLog, len(logs))
	newEvent := make([]bool, len(logs))
	hasNew := false
	for i, log := range logs {
		row, lookupErr := queries.GetRunnerEventByKey(ctx, sqlcgen.GetRunnerEventByKeyParams{RunID: runID, EventKey: &log.EventKey})
		if lookupErr == nil {
			existing := runLogFromSQLC(row)
			if existing.LeaseID != leaseID || existing.Attempt != attempt || existing.RequestedSequence != log.RequestedSequence || existing.Stream != log.Stream || existing.Message != log.Message {
				return nil, ErrConflict
			}
			results[i] = existing
			continue
		}
		if lookupErr != pgx.ErrNoRows {
			return nil, lookupErr
		}
		newEvent[i] = true
		hasNew = true
	}
	if hasNew {
		if _, err := queries.LockAuthorizedLease(ctx, sqlcgen.LockAuthorizedLeaseParams{LeaseID: leaseID, RunID: runID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence}); err == pgx.ErrNoRows {
			return nil, ErrNotFound
		} else if err != nil {
			return nil, err
		}
	}
	for i, log := range logs {
		if !newEvent[i] {
			continue
		}
		attempt32 := int32(attempt)
		requested := int32(log.RequestedSequence)
		row, err := queries.InsertRunnerEventWithSequence(ctx, sqlcgen.InsertRunnerEventWithSequenceParams{
			ID: log.ID, RunID: runID, Sequence: requested, Stream: log.Stream, Message: log.Message, CreatedAt: log.CreatedAt,
			EventKey: &log.EventKey, LeaseID: &leaseID, Attempt: &attempt32, RequestedSequence: &requested,
		})
		if err != nil {
			return nil, err
		}
		results[i] = runLogFromSQLC(row)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return results, nil
}

func (s *PostgresStore) ListRunners(ctx context.Context) ([]domain.Runner, error) {
	rows, err := s.queries.ListRunners(ctx)
	if err != nil {
		return nil, err
	}
	runners := make([]domain.Runner, 0, len(rows))
	for _, row := range rows {
		runners = append(runners, runnerFromSQLC(row))
	}
	return runners, nil
}

func (s *PostgresStore) GetRunnerByID(ctx context.Context, id string) (domain.Runner, error) {
	row, err := s.queries.GetRunnerByID(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, err
	}
	return runnerFromSQLC(row), nil
}

func (s *PostgresStore) RegisterRunner(ctx context.Context, runner domain.Runner) (domain.Runner, error) {
	upserted, err := s.queries.RegisterRunner(ctx, sqlcgen.RegisterRunnerParams{ID: runner.ID, Name: runner.Name, Tags: runner.Tags, Capabilities: runner.Capabilities, Status: runner.Status, RegisteredAt: runner.RegisteredAt, LastHeartbeatAt: runner.LastHeartbeatAt, TokenHash: runner.TokenHash})
	if isUniqueViolation(err) {
		return domain.Runner{}, ErrConflict
	}
	if err != nil {
		return domain.Runner{}, err
	}
	return runnerFromSQLC(upserted), nil
}
func (s *PostgresStore) RegisterRunnerWithAudit(ctx context.Context, runner domain.Runner, audit domain.AuditEvent) (domain.Runner, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Runner{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.RegisterRunner(ctx, sqlcgen.RegisterRunnerParams{ID: runner.ID, Name: runner.Name, Tags: runner.Tags, Capabilities: runner.Capabilities, Status: runner.Status, RegisteredAt: runner.RegisteredAt, LastHeartbeatAt: runner.LastHeartbeatAt, TokenHash: runner.TokenHash})
	if err != nil {
		return domain.Runner{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Runner{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Runner{}, err
	}
	return runnerFromSQLC(row), nil
}

func (s *PostgresStore) CreateRunnerEnrollment(ctx context.Context, enrollment domain.RunnerEnrollment, audit domain.AuditEvent) (domain.RunnerEnrollment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.RunnerEnrollment{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	ttl := enrollment.ExpiresAt.Sub(enrollment.CreatedAt)
	created, err := queries.CreateRunnerEnrollment(ctx, sqlcgen.CreateRunnerEnrollmentParams{ID: enrollment.ID, TokenHash: enrollment.TokenHash, RunnerID: enrollment.RunnerID, RunnerName: enrollment.RunnerName, Tags: enrollment.Tags, Capabilities: enrollment.Capabilities, CreatedBy: enrollment.CreatedBy, TtlMicroseconds: ttl.Microseconds()})
	if isUniqueViolation(err) {
		return domain.RunnerEnrollment{}, ErrConflict
	}
	if err != nil {
		return domain.RunnerEnrollment{}, err
	}
	if err := createAuditWithQueries(ctx, queries, audit); err != nil {
		return domain.RunnerEnrollment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RunnerEnrollment{}, err
	}
	return runnerEnrollmentFromSQLC(created), nil
}

func (s *PostgresStore) RevokeRunnerEnrollment(ctx context.Context, enrollmentID string, audit domain.AuditEvent) (domain.RunnerEnrollment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.RunnerEnrollment{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	revoked, err := queries.RevokeUnusedRunnerEnrollment(ctx, enrollmentID)
	if err == pgx.ErrNoRows {
		return domain.RunnerEnrollment{}, ErrNotFound
	}
	if err != nil {
		return domain.RunnerEnrollment{}, err
	}
	if err := createAuditWithQueries(ctx, queries, audit); err != nil {
		return domain.RunnerEnrollment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RunnerEnrollment{}, err
	}
	return runnerEnrollmentFromSQLC(revoked), nil
}

func (s *PostgresStore) ConsumeRunnerEnrollment(ctx context.Context, consume domain.RunnerEnrollmentConsume, audit domain.AuditEvent) (domain.Runner, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Runner{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	enrollment, err := queries.LockRunnerEnrollmentByTokenHash(ctx, consume.TokenHash)
	if err == pgx.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, err
	}
	if enrollment.UsedAt != nil {
		if enrollment.ConsumeRequestID == nil || enrollment.CredentialHash == nil || *enrollment.ConsumeRequestID != consume.RequestID || *enrollment.CredentialHash != consume.CredentialHash {
			return domain.Runner{}, ErrConflict
		}
		runnerRow, getErr := queries.GetRunnerByID(ctx, enrollment.RunnerID)
		if getErr != nil || runnerRow.TokenHash != consume.CredentialHash {
			return domain.Runner{}, ErrConflict
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.Runner{}, err
		}
		return runnerFromSQLC(runnerRow), nil
	}
	requestID, credentialHash := consume.RequestID, consume.CredentialHash
	if _, err := queries.MarkRunnerEnrollmentUsed(ctx, sqlcgen.MarkRunnerEnrollmentUsedParams{ID: enrollment.ID, ConsumeRequestID: &requestID, CredentialHash: &credentialHash}); err == pgx.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	} else if err != nil {
		return domain.Runner{}, err
	}
	runnerRow, err := queries.CreateRunnerForEnrollment(ctx, sqlcgen.CreateRunnerForEnrollmentParams{ID: enrollment.RunnerID, Name: enrollment.RunnerName, Tags: enrollment.Tags, Capabilities: enrollment.Capabilities, TokenHash: consume.CredentialHash})
	if isUniqueViolation(err) {
		return domain.Runner{}, ErrConflict
	}
	if err != nil {
		return domain.Runner{}, err
	}
	audit.ActorID = enrollment.RunnerID
	audit.TargetID = enrollment.RunnerID
	audit.Metadata = map[string]any{"enrollment_id": enrollment.ID, "runner_id": enrollment.RunnerID}
	if err := createAuditWithQueries(ctx, queries, audit); err != nil {
		return domain.Runner{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Runner{}, err
	}
	return runnerFromSQLC(runnerRow), nil
}

func createAuditWithQueries(ctx context.Context, queries *sqlcgen.Queries, audit domain.AuditEvent) error {
	metadata, err := json.Marshal(audit.Metadata)
	if err != nil {
		return err
	}
	return queries.CreateAuditEvent(ctx, sqlcgen.CreateAuditEventParams{ID: audit.ID, ActorID: audit.ActorID, Action: audit.Action, TargetID: audit.TargetID, Metadata: metadata, CreatedAt: audit.CreatedAt})
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *PostgresStore) UpdateRunnerToken(ctx context.Context, runnerID string, tokenHash string, status string, updatedAt time.Time) (domain.Runner, error) {
	updated, err := s.queries.UpdateRunnerToken(ctx, sqlcgen.UpdateRunnerTokenParams{ID: runnerID, TokenHash: tokenHash, Status: status, LastHeartbeatAt: updatedAt})
	if err == pgx.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, err
	}
	return runnerFromSQLC(updated), nil
}
func (s *PostgresStore) UpdateRunnerTokenWithAudit(ctx context.Context, runnerID string, tokenHash string, status string, updatedAt time.Time, audit domain.AuditEvent) (domain.Runner, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Runner{}, err
	}
	defer tx.Rollback(ctx)
	q := s.queries.WithTx(tx)
	row, err := q.UpdateRunnerToken(ctx, sqlcgen.UpdateRunnerTokenParams{ID: runnerID, TokenHash: tokenHash, Status: status, LastHeartbeatAt: updatedAt})
	if err == pgx.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, err
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Runner{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Runner{}, err
	}
	return runnerFromSQLC(row), nil
}

func (s *PostgresStore) GetRunnerByTokenHash(ctx context.Context, tokenHash string) (domain.Runner, error) {
	runner, err := s.queries.GetRunnerByTokenHash(ctx, tokenHash)
	if err == pgx.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, err
	}
	return runnerFromSQLC(runner), nil
}

func (s *PostgresStore) HeartbeatRunner(ctx context.Context, id string, heartbeatAt time.Time) (domain.Runner, error) {
	runner, err := s.queries.HeartbeatRunner(ctx, sqlcgen.HeartbeatRunnerParams{ID: id, LastHeartbeatAt: heartbeatAt})
	if err == pgx.ErrNoRows {
		return domain.Runner{}, ErrNotFound
	}
	if err != nil {
		return domain.Runner{}, err
	}
	return runnerFromSQLC(runner), nil
}

func (s *PostgresStore) ClaimRun(ctx context.Context, runnerID string, now time.Time, ttl time.Duration) (domain.ClaimedRun, error) {
	return s.ClaimRunWithAudit(ctx, runnerID, now, ttl, domain.AuditEvent{})
}

func (s *PostgresStore) ClaimRunWithAudit(ctx context.Context, runnerID string, now time.Time, ttl time.Duration, audit domain.AuditEvent) (domain.ClaimedRun, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	markedStale, err := queries.MarkStaleRunnerForClaim(ctx, sqlcgen.MarkStaleRunnerForClaimParams{RunnerID: runnerID, StaleBefore: now.Add(-2 * ttl)})
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	if markedStale > 0 {
		if err := tx.Commit(ctx); err != nil {
			return domain.ClaimedRun{}, err
		}
		return domain.ClaimedRun{}, ErrNotFound
	}
	runnerRow, err := queries.GetActiveRunnerForClaim(ctx, sqlcgen.GetActiveRunnerForClaimParams{ID: runnerID, LastHeartbeatAt: now.Add(-2 * ttl)})
	if err == pgx.ErrNoRows {
		return domain.ClaimedRun{}, ErrNotFound
	}
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	runner := runnerFromSQLC(runnerRow)
	// One durable cursor row both serializes claims by the same runner and keeps
	// bounded scans moving across persistently incompatible queued work.
	if err := queries.EnsureRunnerClaimCursor(ctx, runner.ID); err != nil {
		return domain.ClaimedRun{}, err
	}
	storedCursor, err := queries.LockRunnerClaimCursor(ctx, runner.ID)
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	// Claim locks runner cursor -> task run. Completion, periodic expiry, and
	// cancellation do not need a cursor and lock task run -> per-run log advisory
	// lock -> lease. Fenced log/artifact paths omit task run and begin at
	// advisory/lease. No path acquires a runner cursor after a task-run lock.
	if err := expireLeasesAtDBClock(ctx, queries); err != nil {
		return domain.ClaimedRun{}, err
	}
	var claimedRun *domain.TaskRun
	type claimCandidate struct {
		id           string
		claimOrderAt time.Time
	}
	var cursorOrder *time.Time
	cursorID := ""
	if storedCursor.ClaimOrderAt != nil && storedCursor.RunID != nil {
		cursorOrder = storedCursor.ClaimOrderAt
		cursorID = *storedCursor.RunID
	}
	reachedQueueEnd := false
	for batch := 0; batch < claimCandidateMaxBatches && claimedRun == nil; batch++ {
		candidates := make([]claimCandidate, 0, claimCandidateBatchSize)
		if cursorOrder == nil {
			rows, listErr := queries.ListQueuedClaimCandidatesFromHead(ctx, claimCandidateBatchSize)
			err = listErr
			for _, row := range rows {
				candidates = append(candidates, claimCandidate{id: row.ID, claimOrderAt: row.ClaimOrderAt})
			}
		} else {
			rows, listErr := queries.ListQueuedClaimCandidatesAfterCursor(ctx, sqlcgen.ListQueuedClaimCandidatesAfterCursorParams{ClaimOrderAt: *cursorOrder, RunID: cursorID, CandidateLimit: claimCandidateBatchSize})
			err = listErr
			for _, row := range rows {
				candidates = append(candidates, claimCandidate{id: row.ID, claimOrderAt: row.ClaimOrderAt})
			}
		}
		if err != nil {
			return domain.ClaimedRun{}, err
		}
		for _, candidate := range candidates {
			cursorOrder = &candidate.claimOrderAt
			cursorID = candidate.id
			row, err := queries.GetRun(ctx, candidate.id)
			if err != nil {
				return domain.ClaimedRun{}, err
			}
			run, err := taskRunFromSQLC(row)
			if err != nil {
				return domain.ClaimedRun{}, err
			}
			if covers(runner.Tags, run.RunnerTags) && contains(runner.Capabilities, claimRunType(run)) {
				claimedRun = &run
				break
			}
		}
		if len(candidates) < claimCandidateBatchSize {
			reachedQueueEnd = true
			break
		}
	}
	if claimedRun == nil {
		if reachedQueueEnd {
			cursorOrder = nil
			cursorID = ""
		}
		if err := queries.StoreRunnerClaimCursor(ctx, sqlcgen.StoreRunnerClaimCursorParams{ClaimOrderAt: cursorOrder, RunID: cursorID, RunnerID: runner.ID}); err != nil {
			return domain.ClaimedRun{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.ClaimedRun{}, err
		}
		return domain.ClaimedRun{}, ErrNotFound
	}
	if err := queries.StoreRunnerClaimCursor(ctx, sqlcgen.StoreRunnerClaimCursorParams{ClaimOrderAt: cursorOrder, RunID: cursorID, RunnerID: runner.ID}); err != nil {
		return domain.ClaimedRun{}, err
	}
	attempt, err := queries.ClaimQueuedRun(ctx, sqlcgen.ClaimQueuedRunParams{RunID: claimedRun.ID, RunnerID: &runner.ID})
	if err == pgx.ErrNoRows {
		return domain.ClaimedRun{}, ErrNotFound
	} else if err != nil {
		return domain.ClaimedRun{}, err
	}
	claimedRun.Status = domain.RunRunning
	claimedRun.RunnerID = &runner.ID
	leaseID, err := newLeaseToken("lease")
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	fence, err := newLeaseToken("fence")
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	leaseRow, err := queries.CreateActiveRunLease(ctx, sqlcgen.CreateActiveRunLeaseParams{ID: leaseID, RunID: claimedRun.ID, RunnerID: runner.ID, Status: domain.LeaseActive, TtlMicroseconds: ttl.Microseconds(), Attempt: attempt, Fence: fence})
	if err != nil {
		return domain.ClaimedRun{}, err
	}
	lease := runLeaseFromSQLC(leaseRow)
	// A typed deployment run gains its attempt only in the same transaction that
	// creates its lease. Generic runs intentionally have no deployment row.
	if deployment, lookupErr := queries.LockDeploymentForRun(ctx, claimedRun.ID); lookupErr == nil {
		if _, err = queries.CreateDeploymentAttemptForLease(ctx, sqlcgen.CreateDeploymentAttemptForLeaseParams{TaskRunID: claimedRun.ID, LeaseID: lease.ID, RunnerID: runner.ID, Attempt: int32(lease.Attempt), Fence: lease.Fence}); err != nil {
			return domain.ClaimedRun{}, err
		}
		if deployment.Status == domain.DeploymentQueued {
			if _, err = queries.AssignDeploymentForLease(ctx, claimedRun.ID); err != nil {
				return domain.ClaimedRun{}, err
			}
		}
	} else if lookupErr != pgx.ErrNoRows {
		return domain.ClaimedRun{}, lookupErr
	}
	if audit.ID != "" {
		audit.TargetID = claimedRun.ID
		audit.Metadata = auditMetadata(audit.Metadata, map[string]any{"runner_id": runner.ID, "lease_id": lease.ID, "attempt": lease.Attempt, "fence": lease.Fence})
		if err := createAuditWithQueries(ctx, queries, audit); err != nil {
			return domain.ClaimedRun{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ClaimedRun{}, err
	}
	return domain.ClaimedRun{Lease: lease, Run: *claimedRun, PrimitivePlan: primitivePlanForRun(*claimedRun)}, nil
}

func (s *PostgresStore) ExpireLeases(ctx context.Context, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := expireLeasesAtDBClock(ctx, s.queries.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func expireLeasesAtDBClock(ctx context.Context, queries *sqlcgen.Queries) error {
	runIDs, err := queries.ListExpiredRunningRunIDs(ctx, leaseExpiryBatchSize)
	if err != nil {
		return err
	}
	for _, runID := range runIDs {
		expired, err := queries.ExpireActiveLeasesForRun(ctx, runID)
		if err != nil {
			return err
		}
		if expired == 0 {
			continue
		}
		if err := queries.ExpireDeploymentAttemptsForRun(ctx, runID); err != nil {
			return err
		}
		if err := queries.RequeueExpiredRun(ctx, runID); err != nil {
			return err
		}
	}
	return nil
}

func (s *PostgresStore) RenewLease(ctx context.Context, runnerID, leaseID, fence string, attempt int, now time.Time, ttl time.Duration) (domain.RunLease, error) {
	// The WHERE clause is the fencing CAS: identity, generation, opaque fence and
	// unexpired active state must all still match at the instant of extension.
	lease, err := s.queries.RenewAuthorizedLease(ctx, sqlcgen.RenewAuthorizedLeaseParams{TtlMicroseconds: ttl.Microseconds(), LeaseID: leaseID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence})
	if err == pgx.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	} else if err != nil {
		return domain.RunLease{}, err
	}
	return runLeaseFromSQLC(lease), nil
}

func (s *PostgresStore) CompleteLeaseRequest(ctx context.Context, leaseID string, runnerID string, status string, attempt int, fence string, completionKey string, completedAt time.Time, runStatus string, finishedAt *time.Time, workflowState *domain.WorkflowState, logs []domain.RunLog, audit domain.AuditEvent) (domain.RunLease, error) {
	metadata, err := json.Marshal(audit.Metadata)
	if err != nil {
		return domain.RunLease{}, err
	}
	var workflowStateJSON json.RawMessage
	if workflowState != nil {
		workflowStateRaw, err := json.Marshal(workflowState)
		if err != nil {
			return domain.RunLease{}, err
		}
		workflowStateJSON = workflowStateRaw
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.RunLease{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	runID, err := queries.GetLeaseRunID(ctx, leaseID)
	if err == pgx.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	} else if err != nil {
		return domain.RunLease{}, err
	}
	if _, err := queries.LockRunID(ctx, runID); err != nil {
		return domain.RunLease{}, err
	}
	if err := queries.AcquireRunLogLock(ctx, runID); err != nil {
		return domain.RunLease{}, err
	}
	if deploymentRun, deploymentErr := queries.IsDeploymentRun(ctx, runID); deploymentErr != nil {
		return domain.RunLease{}, deploymentErr
	} else if deploymentRun {
		return domain.RunLease{}, ErrConflict
	}
	committed, err := queries.GetCommittedCompletion(ctx, sqlcgen.GetCommittedCompletionParams{LeaseID: leaseID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence, CompletionKey: &completionKey, Status: status})
	if err == nil {
		return runLeaseFromSQLC(committed), nil
	}
	if err != pgx.ErrNoRows {
		return domain.RunLease{}, err
	}
	leaseRow, err := queries.CompleteAuthorizedLease(ctx, sqlcgen.CompleteAuthorizedLeaseParams{Status: status, CompletionKey: &completionKey, LeaseID: leaseID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence})
	if err == pgx.ErrNoRows {
		observed, lookupErr := queries.GetLeaseForCompletion(ctx, sqlcgen.GetLeaseForCompletionParams{LeaseID: leaseID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence})
		if lookupErr == nil && observed.CompletionKey != nil {
			return domain.RunLease{}, ErrConflict
		}
		return domain.RunLease{}, ErrNotFound
	}
	if err != nil {
		return domain.RunLease{}, err
	}
	lease := runLeaseFromSQLC(leaseRow)
	if _, err := queries.UpdateRunStatus(ctx, sqlcgen.UpdateRunStatusParams{ID: lease.RunID, Status: runStatus, FinishedAt: finishedAt}); err != nil {
		return domain.RunLease{}, err
	}
	if workflowState != nil {
		if _, err := queries.UpdateRunWorkflowState(ctx, sqlcgen.UpdateRunWorkflowStateParams{ID: lease.RunID, WorkflowState: workflowStateJSON}); err != nil {
			return domain.RunLease{}, err
		}
	}
	for _, log := range logs {
		if _, err := insertRunLogWithSequence(ctx, tx, log); err != nil {
			return domain.RunLease{}, err
		}
	}
	if err := queries.CreateAuditEvent(ctx, sqlcgen.CreateAuditEventParams{ID: audit.ID, ActorID: audit.ActorID, Action: audit.Action, TargetID: audit.TargetID, Metadata: metadata, CreatedAt: audit.CreatedAt}); err != nil {
		return domain.RunLease{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.RunLease{}, err
	}
	return lease, nil
}

func (s *PostgresStore) GetLeaseForCompletion(ctx context.Context, leaseID, runnerID string, attempt int, fence string) (domain.RunLease, error) {
	lease, err := s.queries.GetLeaseForCompletion(ctx, sqlcgen.GetLeaseForCompletionParams{LeaseID: leaseID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence})
	if err == pgx.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	}
	if err != nil {
		return domain.RunLease{}, err
	}
	return runLeaseFromSQLC(lease), nil
}

func (s *PostgresStore) AuthorizeSecretAccess(ctx context.Context, request domain.SecretAccessRequest) (domain.SecretAccessGrant, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.SecretAccessGrant{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	// The exact lease row is locked before idempotency lookup. A prior audit can
	// therefore never authorize a read after DB-clock expiry or reassignment.
	if _, err := queries.LockAuthorizedLease(ctx, sqlcgen.LockAuthorizedLeaseParams{
		LeaseID: request.LeaseID, RunID: request.RunID, RunnerID: request.RunnerID,
		Attempt: int32(request.Attempt), Fence: request.Fence,
	}); err == pgx.ErrNoRows {
		return domain.SecretAccessGrant{}, ErrNotFound
	} else if err != nil {
		return domain.SecretAccessGrant{}, err
	}
	expected := secretAccessAudit(request, request.RequestedAt)
	var replay *domain.AuditEvent
	existingRow, err := queries.GetAuditEventByID(ctx, request.AccessID)
	if err == nil {
		existing, mapErr := auditEventFromSQLC(existingRow)
		if mapErr != nil {
			return domain.SecretAccessGrant{}, mapErr
		}
		if !secretAccessAuditsEqual(existing, expected) {
			return domain.SecretAccessGrant{}, ErrConflict
		}
		replay = &existing
	}
	if err != nil && err != pgx.ErrNoRows {
		return domain.SecretAccessGrant{}, err
	}
	runContext, err := queries.GetRunContextForSecretAccess(ctx, request.RunID)
	if err == pgx.ErrNoRows {
		return domain.SecretAccessGrant{}, ErrNotFound
	} else if err != nil {
		return domain.SecretAccessGrant{}, err
	}
	run := domain.TaskRun{ID: request.RunID}
	if err := json.Unmarshal(runContext.RunSpec, &run.RunSpec); err != nil {
		return domain.SecretAccessGrant{}, err
	}
	if err := json.Unmarshal(runContext.Workflow, &run.Workflow); err != nil {
		return domain.SecretAccessGrant{}, err
	}
	if err := json.Unmarshal(runContext.WorkflowState, &run.WorkflowState); err != nil {
		return domain.SecretAccessGrant{}, err
	}
	if !runAuthorizesSecretAccess(run, request) {
		return domain.SecretAccessGrant{}, ErrNotFound
	}
	if replay != nil {
		if err := tx.Commit(ctx); err != nil {
			return domain.SecretAccessGrant{}, err
		}
		return secretAccessGrant(request, replay.CreatedAt), nil
	}
	metadata, err := json.Marshal(expected.Metadata)
	if err != nil {
		return domain.SecretAccessGrant{}, err
	}
	createdRow, err := queries.CreateSecretAccessAudit(ctx, sqlcgen.CreateSecretAccessAuditParams{
		ID: request.AccessID, ActorID: request.RunnerID, TargetID: request.RunID, Metadata: metadata,
	})
	if err != nil {
		return domain.SecretAccessGrant{}, err
	}
	created, err := auditEventFromSQLC(createdRow)
	if err != nil {
		return domain.SecretAccessGrant{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.SecretAccessGrant{}, err
	}
	return secretAccessGrant(request, created.CreatedAt), nil
}

func (s *PostgresStore) CancelRunRequest(ctx context.Context, runID string, canceledAt time.Time, log domain.RunLog, audit domain.AuditEvent) (domain.TaskRun, error) {
	metadata, err := json.Marshal(audit.Metadata)
	if err != nil {
		return domain.TaskRun{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.TaskRun{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	status, err := queries.LockCancellableRunStatus(ctx, runID)
	if err == pgx.ErrNoRows {
		return domain.TaskRun{}, ErrNotFound
	} else if err != nil {
		return domain.TaskRun{}, err
	}
	if err := rejectGenericDeploymentRun(ctx, queries, runID); err != nil {
		return domain.TaskRun{}, err
	}
	if domain.IsTerminalRunStatus(status) {
		return domain.TaskRun{}, ErrNotFound
	}
	canceledAt, err = queries.DatabaseClock(ctx)
	if err != nil {
		return domain.TaskRun{}, err
	}
	log.CreatedAt = canceledAt
	audit.CreatedAt = canceledAt
	if err := queries.AcquireRunLogLock(ctx, runID); err != nil {
		return domain.TaskRun{}, err
	}
	if err := queries.CancelActiveLeasesForRun(ctx, runID); err != nil {
		return domain.TaskRun{}, err
	}
	if err := queries.CancelRun(ctx, sqlcgen.CancelRunParams{RunID: runID, FinishedAt: &canceledAt}); err != nil {
		return domain.TaskRun{}, err
	}
	updated, err := queries.GetRun(ctx, runID)
	if err != nil {
		return domain.TaskRun{}, err
	}
	run, err := taskRunFromSQLC(updated)
	if err != nil {
		return domain.TaskRun{}, err
	}
	if _, err := insertRunLogWithSequence(ctx, tx, log); err != nil {
		return domain.TaskRun{}, err
	}
	if err := queries.CreateAuditEvent(ctx, sqlcgen.CreateAuditEventParams{ID: audit.ID, ActorID: audit.ActorID, Action: audit.Action, TargetID: audit.TargetID, Metadata: metadata, CreatedAt: audit.CreatedAt}); err != nil {
		return domain.TaskRun{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.TaskRun{}, err
	}
	return run, nil
}

func (s *PostgresStore) ActiveLeaseForRun(ctx context.Context, runID string) (domain.RunLease, error) {
	lease, err := s.queries.GetActiveLeaseForRun(ctx, runID)
	if err == pgx.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	}
	if err != nil {
		return domain.RunLease{}, err
	}
	return runLeaseFromSQLC(lease), nil
}

func (s *PostgresStore) GetLeaseForRunner(ctx context.Context, leaseID string, runnerID string) (domain.RunLease, error) {
	lease, err := s.queries.GetActiveLeaseForRunner(ctx, sqlcgen.GetActiveLeaseForRunnerParams{LeaseID: leaseID, RunnerID: runnerID})
	if err == pgx.ErrNoRows {
		return domain.RunLease{}, ErrNotFound
	}
	if err != nil {
		return domain.RunLease{}, err
	}
	return runLeaseFromSQLC(lease), nil
}

func (s *PostgresStore) ListRunLogs(ctx context.Context, runID string) ([]domain.RunLog, error) {
	result, err := s.ListRunLogsPage(ctx, runID, Page{})
	return result.Items, err
}

func (s *PostgresStore) ListRunLogsPage(ctx context.Context, runID string, page Page) (PageResult[domain.RunLog], error) {
	total64, err := s.queries.CountRunLogs(ctx, runID)
	if err != nil {
		return PageResult[domain.RunLog]{}, err
	}
	total := int(total64)
	limit, offset := resolvePage(page, total)
	rows, err := s.queries.ListRunLogsPage(ctx, sqlcgen.ListRunLogsPageParams{RunID: runID, PageLimit: int32(limit), PageOffset: int32(offset)})
	if err != nil {
		return PageResult[domain.RunLog]{}, err
	}
	logs := make([]domain.RunLog, 0, len(rows))
	for _, row := range rows {
		logs = append(logs, runLogFromSQLC(row))
	}
	return PageResult[domain.RunLog]{Items: logs, Limit: limit, Offset: offset, Total: total}, nil
}

func (s *PostgresStore) ListArtifacts(ctx context.Context, runID string) ([]domain.ArtifactRecord, error) {
	result, err := s.ListArtifactsPage(ctx, runID, Page{})
	return result.Items, err
}

func (s *PostgresStore) ListArtifactsPage(ctx context.Context, runID string, page Page) (PageResult[domain.ArtifactRecord], error) {
	total64, err := s.queries.CountArtifacts(ctx, strings.TrimSpace(runID))
	if err != nil {
		return PageResult[domain.ArtifactRecord]{}, err
	}
	total := int(total64)
	limit, offset := resolvePage(page, total)
	rows, err := s.queries.ListArtifactsPage(ctx, sqlcgen.ListArtifactsPageParams{RunID: strings.TrimSpace(runID), PageLimit: int32(limit), PageOffset: int32(offset)})
	if err != nil {
		return PageResult[domain.ArtifactRecord]{}, err
	}
	artifacts := make([]domain.ArtifactRecord, 0, len(rows))
	for _, row := range rows {
		artifacts = append(artifacts, artifactFromSQLC(row))
	}
	return PageResult[domain.ArtifactRecord]{Items: artifacts, Limit: limit, Offset: offset, Total: total}, nil
}

func (s *PostgresStore) CreateArtifact(ctx context.Context, artifact domain.ArtifactRecord) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	if err := rejectGenericDeploymentRun(ctx, queries, artifact.RunID); err != nil {
		return err
	}
	if _, err := queries.CreateArtifact(ctx, sqlcgen.CreateArtifactParams{ID: artifact.ID, RunID: artifact.RunID, LeaseID: artifact.LeaseID, Name: artifact.Name, Path: artifact.Path, Found: artifact.Found, Required: artifact.Required, Size: artifact.Size, Kind: artifact.Kind, CreatedAt: artifact.CreatedAt}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) CreateArtifactForLease(ctx context.Context, artifact domain.ArtifactRecord, runnerID string, attempt int, fence string, now time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	if _, err := queries.LockAuthorizedLease(ctx, sqlcgen.LockAuthorizedLeaseParams{LeaseID: artifact.LeaseID, RunID: artifact.RunID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence}); err == pgx.ErrNoRows {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if _, err := queries.CreateArtifact(ctx, sqlcgen.CreateArtifactParams{ID: artifact.ID, RunID: artifact.RunID, LeaseID: artifact.LeaseID, Name: artifact.Name, Path: artifact.Path, Found: artifact.Found, Required: artifact.Required, Size: artifact.Size, Kind: artifact.Kind, CreatedAt: artifact.CreatedAt}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *PostgresStore) ListApprovals(ctx context.Context, status string) ([]domain.Approval, error) {
	rows, err := s.queries.ListApprovals(ctx, status)
	if err != nil {
		return nil, err
	}
	approvals := make([]domain.Approval, 0, len(rows))
	for _, row := range rows {
		approvals = append(approvals, approvalFromSQLC(row))
	}
	return approvals, nil
}

func (s *PostgresStore) CreateApproval(ctx context.Context, approval domain.Approval) (domain.Approval, error) {
	if err := rejectGenericDeploymentRun(ctx, s.queries, approval.RunID); err != nil {
		return domain.Approval{}, err
	}
	inserted, err := s.queries.CreateApproval(ctx, sqlcgen.CreateApprovalParams{ID: approval.ID, RunID: approval.RunID, Status: approval.Status, RequestedBy: approval.RequestedBy, ApprovedBy: approval.ApprovedBy, CreatedAt: approval.CreatedAt, ApprovedAt: approval.ApprovedAt})
	if err != nil {
		return domain.Approval{}, err
	}
	return approvalFromSQLC(inserted), nil
}

func (s *PostgresStore) ApproveRun(ctx context.Context, runID string, actorID string, approvedAt time.Time) (domain.Approval, error) {
	return s.ApproveRunWithAudit(ctx, runID, actorID, approvedAt, domain.AuditEvent{})
}

func (s *PostgresStore) ApproveRunWithAudit(ctx context.Context, runID string, actorID string, approvedAt time.Time, audit domain.AuditEvent) (domain.Approval, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Approval{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	if err := rejectGenericDeploymentRun(ctx, queries, runID); err != nil {
		return domain.Approval{}, err
	}
	updated, err := queries.ResolveApproval(ctx, sqlcgen.ResolveApprovalParams{RunID: runID, Status: domain.ApprovalApproved, ApprovedBy: &actorID, ApprovedAt: &approvedAt})
	if err == pgx.ErrNoRows {
		return domain.Approval{}, ErrNotFound
	}
	if err != nil {
		return domain.Approval{}, err
	}
	if _, err := queries.QueueApprovedRun(ctx, runID); err == pgx.ErrNoRows {
		return domain.Approval{}, ErrNotFound
	} else if err != nil {
		return domain.Approval{}, err
	}
	if audit.ID != "" {
		audit.TargetID = runID
		audit.Metadata = auditMetadata(audit.Metadata, map[string]any{"approval_id": updated.ID})
		if err := createAuditWithQueries(ctx, queries, audit); err != nil {
			return domain.Approval{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Approval{}, err
	}
	return approvalFromSQLC(updated), nil
}

func (s *PostgresStore) RejectRun(ctx context.Context, runID string, actorID string, rejectedAt time.Time) (domain.Approval, error) {
	return s.RejectRunWithAudit(ctx, runID, actorID, rejectedAt, domain.AuditEvent{})
}

func (s *PostgresStore) RejectRunWithAudit(ctx context.Context, runID string, actorID string, rejectedAt time.Time, audit domain.AuditEvent) (domain.Approval, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Approval{}, err
	}
	defer tx.Rollback(ctx)
	queries := s.queries.WithTx(tx)
	if err := rejectGenericDeploymentRun(ctx, queries, runID); err != nil {
		return domain.Approval{}, err
	}
	updated, err := queries.ResolveApproval(ctx, sqlcgen.ResolveApprovalParams{RunID: runID, Status: domain.ApprovalRejected, ApprovedBy: &actorID, ApprovedAt: &rejectedAt})
	if err == pgx.ErrNoRows {
		return domain.Approval{}, ErrNotFound
	}
	if err != nil {
		return domain.Approval{}, err
	}
	if _, err := queries.UpdateRunStatus(ctx, sqlcgen.UpdateRunStatusParams{ID: runID, Status: domain.RunCanceled, FinishedAt: &rejectedAt}); err != nil {
		return domain.Approval{}, err
	}
	if audit.ID != "" {
		audit.TargetID = runID
		audit.Metadata = auditMetadata(audit.Metadata, map[string]any{"approval_id": updated.ID})
		if err := createAuditWithQueries(ctx, queries, audit); err != nil {
			return domain.Approval{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Approval{}, err
	}
	return approvalFromSQLC(updated), nil
}

func (s *PostgresStore) ListAuditEvents(ctx context.Context) ([]domain.AuditEvent, error) {
	result, err := s.ListAuditEventsPage(ctx, Page{})
	return result.Items, err
}

func (s *PostgresStore) ListAuditEventsPage(ctx context.Context, page Page) (PageResult[domain.AuditEvent], error) {
	total64, err := s.queries.CountAuditEvents(ctx)
	if err != nil {
		return PageResult[domain.AuditEvent]{}, err
	}
	total := int(total64)
	limit, offset := resolvePage(page, total)
	rows, err := s.queries.ListAuditEventsPage(ctx, sqlcgen.ListAuditEventsPageParams{Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return PageResult[domain.AuditEvent]{}, err
	}
	events := make([]domain.AuditEvent, 0, len(rows))
	for _, row := range rows {
		event, err := auditEventFromSQLC(row)
		if err != nil {
			return PageResult[domain.AuditEvent]{}, err
		}
		events = append(events, event)
	}
	return PageResult[domain.AuditEvent]{Items: events, Limit: limit, Offset: offset, Total: total}, nil
}

func (s *PostgresStore) CreateAuditEvent(ctx context.Context, event domain.AuditEvent) error {
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return err
	}
	return s.queries.CreateAuditEvent(ctx, sqlcgen.CreateAuditEventParams{ID: event.ID, ActorID: event.ActorID, Action: event.Action, TargetID: event.TargetID, Metadata: metadata, CreatedAt: event.CreatedAt})
}

func decodeWorkflowState(raw []byte, workflowState *domain.WorkflowState) error {
	if len(raw) == 0 {
		*workflowState = domain.WorkflowState{}
		return nil
	}
	return json.Unmarshal(raw, workflowState)
}

func decodeRunSpec(raw []byte, runSpec *domain.RunSpec) error {
	if len(raw) == 0 {
		*runSpec = domain.RunSpec{Inputs: map[string]any{}}
		return nil
	}
	if err := json.Unmarshal(raw, runSpec); err != nil {
		return err
	}
	if runSpec.Inputs == nil {
		runSpec.Inputs = map[string]any{}
	}
	return nil
}

func decodeWorkflow(raw []byte, workflow *domain.Workflow) error {
	if len(raw) == 0 {
		*workflow = domain.Workflow{}
		return nil
	}
	return json.Unmarshal(raw, workflow)
}

func resolvePage(page Page, total int) (int, int) {
	limit := total
	offset := 0
	if page.Enabled {
		limit = page.Limit
		offset = page.Offset
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
	return limit, offset
}

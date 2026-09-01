package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

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

// ListServices implements the corresponding repository operation.
func (s *PostgresStore) ListServices(ctx context.Context, p string) ([]domain.Service, error) {
	rows, e := s.queries.ListServices(ctx, p)
	if e != nil {
		return nil, fmt.Errorf("list services query: %w", e)
	}
	out := make([]domain.Service, 0, len(rows))
	for _, v := range rows {
		out = append(out, serviceFromSQLC(v))
	}
	return out, nil
}

// CreateService implements the corresponding repository operation.
func (s *PostgresStore) CreateService(ctx context.Context, v domain.Service, opts ...MutationOption) (domain.Service, error) {
	audit := resolveMutationOptions(opts)
	if audit == nil {
		x, e := s.queries.CreateService(ctx, sqlcgen.CreateServiceParams{ID: v.ID, ProjectID: v.ProjectID, Name: v.Name, RepositoryID: v.RepositoryID, ComposePath: v.ComposePath, Profiles: v.Profiles, OwnerID: v.OwnerID, CreatedAt: v.CreatedAt})
		if isUniqueViolation(e) {
			return domain.Service{}, ErrConflict
		}
		return serviceFromSQLC(x), e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return domain.Service{}, fmt.Errorf("begin transaction: %w", e)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	x, e := q.CreateService(ctx, sqlcgen.CreateServiceParams{ID: v.ID, ProjectID: v.ProjectID, Name: v.Name, RepositoryID: v.RepositoryID, ComposePath: v.ComposePath, Profiles: v.Profiles, OwnerID: v.OwnerID, CreatedAt: v.CreatedAt})
	if isUniqueViolation(e) {
		return domain.Service{}, ErrConflict
	}
	if e != nil {
		return domain.Service{}, fmt.Errorf("create service query: %w", e)
	}
	if e = createAuditWithQueries(ctx, q, *audit); e != nil {
		return domain.Service{}, fmt.Errorf("create audit event: %w", e)
	}
	if e = tx.Commit(ctx); e != nil {
		return domain.Service{}, fmt.Errorf("commit transaction: %w", e)
	}
	return serviceFromSQLC(x), nil
}

// ListEnvironments implements the corresponding repository operation.
func (s *PostgresStore) ListEnvironments(ctx context.Context, id string) ([]domain.Environment, error) {
	rows, e := s.queries.ListEnvironments(ctx, id)
	if e != nil {
		return nil, fmt.Errorf("list environments query: %w", e)
	}
	out := make([]domain.Environment, 0, len(rows))
	for _, v := range rows {
		out = append(out, environmentFromSQLC(v))
	}
	return out, nil
}

// CreateEnvironment implements the corresponding repository operation.
func (s *PostgresStore) CreateEnvironment(ctx context.Context, v domain.Environment, opts ...MutationOption) (domain.Environment, error) {
	audit := resolveMutationOptions(opts)
	hp, _ := json.Marshal(v.HealthPolicy)
	sb, _ := json.Marshal(v.SecretBindings)
	runnerSelector := v.RunnerSelector
	if runnerSelector == nil {
		runnerSelector = []string{}
	}
	if audit == nil {
		x, e := s.queries.CreateEnvironment(ctx, sqlcgen.CreateEnvironmentParams{ID: v.ID, ServiceID: v.ServiceID, Name: v.Name, RunnerSelector: runnerSelector, ComposeProject: v.ComposeProject, HealthPolicy: hp, ConfirmationRequired: v.ConfirmationRequired, TimeoutSeconds: int32(v.TimeoutSeconds), SecretBindings: sb, RollbackSafe: v.RollbackSafe, CurrentHealthyRevisionID: v.CurrentHealthyRevisionID, CreatedAt: v.CreatedAt})
		if isUniqueViolation(e) {
			return domain.Environment{}, ErrConflict
		}
		return environmentFromSQLC(x), e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return domain.Environment{}, fmt.Errorf("begin transaction: %w", e)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	x, e := q.CreateEnvironment(ctx, sqlcgen.CreateEnvironmentParams{ID: v.ID, ServiceID: v.ServiceID, Name: v.Name, RunnerSelector: runnerSelector, ComposeProject: v.ComposeProject, HealthPolicy: hp, ConfirmationRequired: v.ConfirmationRequired, TimeoutSeconds: int32(v.TimeoutSeconds), SecretBindings: sb, RollbackSafe: v.RollbackSafe, CurrentHealthyRevisionID: v.CurrentHealthyRevisionID, CreatedAt: v.CreatedAt})
	if isUniqueViolation(e) {
		return domain.Environment{}, ErrConflict
	}
	if e != nil {
		return domain.Environment{}, fmt.Errorf("create environment query: %w", e)
	}
	if e = createAuditWithQueries(ctx, q, *audit); e != nil {
		return domain.Environment{}, fmt.Errorf("create audit event: %w", e)
	}
	if e = tx.Commit(ctx); e != nil {
		return domain.Environment{}, fmt.Errorf("commit transaction: %w", e)
	}
	return environmentFromSQLC(x), nil
}

// ListRevisions implements the corresponding repository operation.
func (s *PostgresStore) ListRevisions(ctx context.Context, id string) ([]domain.Revision, error) {
	rows, e := s.queries.ListRevisions(ctx, id)
	if e != nil {
		return nil, fmt.Errorf("list revisions query: %w", e)
	}
	out := make([]domain.Revision, 0, len(rows))
	for _, v := range rows {
		out = append(out, revisionFromSQLC(v))
	}
	return out, nil
}

// CreateRevision implements the corresponding repository operation.
func (s *PostgresStore) CreateRevision(ctx context.Context, v domain.Revision, opts ...MutationOption) (domain.Revision, error) {
	audit := resolveMutationOptions(opts)
	if audit == nil {
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
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return domain.Revision{}, fmt.Errorf("begin transaction: %w", e)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	x, e := q.CreateRevision(ctx, sqlcgen.CreateRevisionParams{ID: v.ID, ServiceID: v.ServiceID, RequestedRef: v.RequestedRef, GitCommit: v.GitCommit, ComposeHash: v.ComposeHash, ImageDigests: v.ImageDigests, ContentIdentity: v.ContentIdentity, CreatedBy: v.CreatedBy, CreatedAt: v.CreatedAt})
	if isUniqueViolation(e) {
		return domain.Revision{}, ErrConflict
	}
	if e != nil {
		return domain.Revision{}, fmt.Errorf("create revision query: %w", e)
	}
	if e = createAuditWithQueries(ctx, q, *audit); e != nil {
		return domain.Revision{}, fmt.Errorf("create audit event: %w", e)
	}
	if e = tx.Commit(ctx); e != nil {
		return domain.Revision{}, fmt.Errorf("commit transaction: %w", e)
	}
	return revisionFromSQLC(x), nil
}

// DeploymentPlan verifies runner identity, fence and the database clock in the
// same read-only statement.  It deliberately joins only controlled deployment
// configuration and opaque secret binding references.
// DeploymentPlan implements the corresponding repository operation.
func (s *PostgresStore) DeploymentPlan(ctx context.Context, deploymentID, runID, leaseID, runnerID string, attempt int, fence string) (domain.DeploymentPlan, error) {
	row, err := s.queries.DeploymentPlan(ctx, sqlcgen.DeploymentPlanParams{ID: deploymentID, TaskRunID: runID, LeaseID: leaseID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence})
	if err == pgx.ErrNoRows {
		return domain.DeploymentPlan{}, ErrNotFound
	}
	if err != nil {
		return domain.DeploymentPlan{}, fmt.Errorf("deployment plan query: %w", err)
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

// ResolveRevisionProvenance implements the corresponding repository operation.
func (s *PostgresStore) ResolveRevisionProvenance(ctx context.Context, deploymentID, runID, leaseID, runnerID string, attempt int, fence, resolutionID, commit, hash string, digests []string, audit domain.AuditEvent) (domain.Revision, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Revision{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	// Replay lookup deliberately precedes lease-expiry validation.  An exact
	// committed acknowledgement is a receipt, not renewed runner authority.
	if v, found, e := provenanceReplay(ctx, q, deploymentID, resolutionID, runID, leaseID, runnerID, attempt, fence, commit, hash, digests); e != nil {
		return domain.Revision{}, fmt.Errorf("check provenance replay: %w", e)
	} else if found {
		if err = tx.Commit(ctx); err != nil {
			return domain.Revision{}, fmt.Errorf("commit transaction: %w", err)
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
		return domain.Revision{}, fmt.Errorf("lock provenance attempt query: %w", err)
	}
	if v, found, e := provenanceReplay(ctx, q, deploymentID, resolutionID, runID, leaseID, runnerID, attempt, fence, commit, hash, digests); e != nil {
		return domain.Revision{}, fmt.Errorf("check provenance replay: %w", e)
	} else if found {
		if err = tx.Commit(ctx); err != nil {
			return domain.Revision{}, fmt.Errorf("commit transaction: %w", err)
		}
		return v, nil
	}
	active, err := q.ProvenanceAttemptIsActive(ctx, sqlcgen.ProvenanceAttemptIsActiveParams{ID: deploymentID, ID_2: leaseID, RunID: runID, RunnerID: runnerID, Attempt: int32(attempt), Fence: fence})
	if err == pgx.ErrNoRows || active == nil || !*active {
		return domain.Revision{}, ErrNotFound
	}
	if err != nil {
		return domain.Revision{}, fmt.Errorf("provenance attempt is active query: %w", err)
	}
	v := revisionFromSQLC(row)
	if v.ProvenanceResolved {
		// A rollback child deliberately reuses the last healthy revision. It
		// must attest to the same immutable provenance under its own fence,
		// rather than treating an already resolved revision as an error.
		imagesEqual := existingProvenanceImagesEquivalent(v.ImageDigests, digests)
		if v.GitCommit != commit || v.ComposeHash != hash || !imagesEqual {
			return domain.Revision{}, provenanceContentConflict(v.GitCommit, v.ComposeHash, commit, hash, imagesEqual)
		}
	} else {
		if v.ProvenanceState != "pending" {
			return domain.Revision{}, ErrConflict
		}
		updated, err := q.ResolveRevisionProvenance(ctx, sqlcgen.ResolveRevisionProvenanceParams{ID: v.ID, GitCommit: commit, ComposeHash: hash, ImageDigests: digests})
		if isUniqueViolation(err) {
			return domain.Revision{}, provenanceConflict(provenanceConflictUnique)
		}
		if err != nil {
			return domain.Revision{}, fmt.Errorf("resolve revision provenance query: %w", err)
		}
		v = revisionFromSQLC(updated)
	}
	if audit.ID != "" {
		audit.CreatedAt = time.Now().UTC()
		if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
			return domain.Revision{}, fmt.Errorf("create audit event: %w", err)
		}
	}
	err = q.CreateProvenanceResolutionReplay(ctx, sqlcgen.CreateProvenanceResolutionReplayParams{DeploymentID: deploymentID, Attempt: int32(attempt), ResolutionID: resolutionID, RunID: runID, LeaseID: leaseID, RunnerID: runnerID, Fence: fence, RevisionID: v.ID, GitCommit: commit, ComposeHash: hash, ImageDigests: digests, ContentIdentity: commit + ":" + hash, AuditID: audit.ID})
	if isUniqueViolation(err) {
		return domain.Revision{}, provenanceConflict(provenanceConflictUnique)
	}
	if err != nil {
		return domain.Revision{}, fmt.Errorf("create provenance resolution replay query: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Revision{}, fmt.Errorf("commit transaction: %w", err)
	}
	return v, nil
}

// existingProvenanceImagesEquivalent permits only a narrow legacy upgrade
// bridge. Replays remain byte-for-byte strict. A legacy revision may match one
// current image only when its sole bare sha256 suffix exactly matches the sole
// validated full reference for that deployment service.
func existingProvenanceImagesEquivalent(existing, incoming []string) bool {
	if reflect.DeepEqual(existing, incoming) {
		for _, image := range incoming {
			if !validFullImageReference(image) {
				return false
			}
		}
		return true
	}
	if len(existing) != 1 || len(incoming) != 1 {
		return false
	}
	legacy := strings.TrimSpace(existing[0])
	full := strings.TrimSpace(incoming[0])
	if !validBareImageDigest(legacy) || !validFullImageReference(full) {
		return false
	}
	_, digest, found := strings.Cut(full, "@")
	return found && digest == legacy
}

func validBareImageDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if character < '0' || character > '9' && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validFullImageReference(value string) bool {
	repository, digest, found := strings.Cut(value, "@")
	if !found || repository == "" || strings.LastIndex(repository, ":") > strings.LastIndex(repository, "/") {
		return false
	}
	for _, character := range repository {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '.' && character != '_' && character != '/' && character != ':' && character != '-' {
			return false
		}
	}
	return validBareImageDigest(digest)
}

// provenanceReplay returns only a byte-for-byte authority/body match.  A
// known resolution key with any changed field is a conflict, never a hint.
func provenanceReplay(ctx context.Context, q *sqlcgen.Queries, deploymentID, resolutionID, runID, leaseID, runnerID string, attempt int, fence, commit, hash string, digests []string) (domain.Revision, bool, error) {
	row, err := q.GetProvenanceResolutionReplay(ctx, sqlcgen.GetProvenanceResolutionReplayParams{DeploymentID: deploymentID, ResolutionID: resolutionID})
	if err == pgx.ErrNoRows {
		return domain.Revision{}, false, nil
	}
	if err != nil {
		return domain.Revision{}, false, fmt.Errorf("get provenance resolution replay query: %w", err)
	}
	if row.RunID != runID || row.LeaseID != leaseID || row.RunnerID != runnerID || int(row.Attempt) != attempt || row.Fence != fence || row.ReplayGitCommit != commit || row.ReplayComposeHash != hash || !reflect.DeepEqual(row.ReplayImageDigests, digests) {
		return domain.Revision{}, false, provenanceConflict(provenanceConflictReplayKey)
	}
	return revisionFromSQLC(sqlcgen.Revision{ID: row.ID, ServiceID: row.ServiceID, RequestedRef: row.RequestedRef, GitCommit: row.GitCommit, ComposeHash: row.ComposeHash, ImageDigests: row.ImageDigests, ContentIdentity: row.ContentIdentity, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt, ProvenanceState: row.ProvenanceState, ProvenanceResolved: row.ProvenanceResolved, ResolvedAt: row.ResolvedAt}), true, nil
}

// ListDeployments implements the corresponding repository operation.
func (s *PostgresStore) ListDeployments(ctx context.Context, id string) ([]domain.Deployment, error) {
	rows, e := s.queries.ListDeployments(ctx, id)
	if e != nil {
		return nil, fmt.Errorf("list deployments query: %w", e)
	}
	out := make([]domain.Deployment, 0, len(rows))
	for _, v := range rows {
		out = append(out, deploymentFromSQLC(v))
	}
	return out, nil
}

// GetDeployment implements the corresponding repository operation.
func (s *PostgresStore) GetDeployment(ctx context.Context, id string) (domain.Deployment, error) {
	row, err := s.queries.GetDeploymentByID(ctx, id)
	if err == pgx.ErrNoRows {
		return domain.Deployment{}, ErrNotFound
	}
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("get deployment by id query: %w", err)
	}
	return deploymentFromSQLC(row), nil
}

// GetEnvironment implements the corresponding repository operation.
func (s *PostgresStore) GetEnvironment(ctx context.Context, id string) (domain.Environment, error) {
	row, err := s.queries.GetEnvironmentByID(ctx, id)
	if err == pgx.ErrNoRows {
		return domain.Environment{}, ErrNotFound
	}
	if err != nil {
		return domain.Environment{}, fmt.Errorf("get environment by id query: %w", err)
	}
	return environmentFromSQLC(row), nil
}

// GetService implements the corresponding repository operation.
func (s *PostgresStore) GetService(ctx context.Context, id string) (domain.Service, error) {
	row, err := s.queries.GetServiceByID(ctx, id)
	if err == pgx.ErrNoRows {
		return domain.Service{}, ErrNotFound
	}
	if err != nil {
		return domain.Service{}, fmt.Errorf("get service by id query: %w", err)
	}
	return serviceFromSQLC(row), nil
}

// CreateDeploymentRequest persists the typed, non-shell deployment plan and
// its first-class deployment record together. A client never supplies the run
// linkage, so it cannot attach an arbitrary generic execution to a deployment.
// CreateDeploymentRequest implements the corresponding repository operation.
func (s *PostgresStore) CreateDeploymentRequest(ctx context.Context, v domain.Deployment, run domain.TaskRun, audit domain.AuditEvent) (domain.Deployment, error) {
	raw, err := json.Marshal(run.RunSpec)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("encode run spec: %w", err)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	if v.RollbackOfID != nil {
		return domain.Deployment{}, ErrConflict
	}
	if replay, lookupErr := q.GetDeploymentByEnvironmentKey(ctx, sqlcgen.GetDeploymentByEnvironmentKeyParams{EnvironmentID: v.EnvironmentID, IdempotencyKey: v.IdempotencyKey}); lookupErr == nil {
		if replay.DesiredRevisionID != v.DesiredRevisionID {
			return domain.Deployment{}, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.Deployment{}, fmt.Errorf("commit transaction: %w", err)
		}
		return deploymentFromSQLC(replay), nil
	} else if lookupErr != pgx.ErrNoRows {
		return domain.Deployment{}, fmt.Errorf("get deployment by environment key query: %w", lookupErr)
	}
	if _, err = q.CreateDeploymentRun(ctx, sqlcgen.CreateDeploymentRunParams{ID: run.ID, ProjectID: run.ProjectID, RunSpec: raw, RunnerTags: run.RunnerTags, Status: run.Status, RequestedBy: run.RequestedBy}); err != nil {
		return domain.Deployment{}, fmt.Errorf("create deployment run query: %w", err)
	}
	v.TaskRunID = &run.ID
	x, err := q.CreateDeployment(ctx, sqlcgen.CreateDeploymentParams{ID: v.ID, EnvironmentID: v.EnvironmentID, DesiredRevisionID: v.DesiredRevisionID, PreviousHealthyRevisionID: v.PreviousHealthyRevisionID, TaskRunID: run.ID, IdempotencyKey: v.IdempotencyKey, Status: v.Status, RequestedBy: v.RequestedBy, CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt, FenceRequired: true, RollbackOfID: v.RollbackOfID})
	if isUniqueViolation(err) {
		return domain.Deployment{}, ErrConflict
	}
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("create deployment query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Deployment{}, fmt.Errorf("create audit event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Deployment{}, fmt.Errorf("commit transaction: %w", err)
	}
	return deploymentFromSQLC(x), nil
}

// ConfirmDeployment implements the corresponding repository operation.
func (s *PostgresStore) ConfirmDeployment(ctx context.Context, id, confirmedBy string, audit domain.AuditEvent) (domain.Deployment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	confirmed, err := q.ConfirmDeployment(ctx, sqlcgen.ConfirmDeploymentParams{ID: id, ConfirmedBy: &confirmedBy})
	if err == pgx.ErrNoRows {
		return domain.Deployment{}, ErrConflict
	}
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("confirm deployment query: %w", err)
	}
	if err = q.QueueConfirmedDeploymentRun(ctx, confirmed.TaskRunID); err != nil {
		return domain.Deployment{}, fmt.Errorf("queue confirmed deployment run query: %w", err)
	}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Deployment{}, fmt.Errorf("create audit event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Deployment{}, fmt.Errorf("commit transaction: %w", err)
	}
	return deploymentFromSQLC(confirmed), nil
}

// FailPreAssignmentDeployment is deliberately separate from the runner
// protocol: it can only record validation failures before a run is assigned.
// Once assigned, an exact lease/fence deployment transition is the only
// execution authority.
// FailPreAssignmentDeployment implements the corresponding repository operation.
func (s *PostgresStore) FailPreAssignmentDeployment(ctx context.Context, id, failureCode string, audit domain.AuditEvent) (domain.Deployment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer rollbackTransaction(ctx, tx)
	q := s.queries.WithTx(tx)
	current, err := q.LockDeploymentByID(ctx, id)
	if err == pgx.ErrNoRows {
		return domain.Deployment{}, ErrNotFound
	}
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("lock deployment by id query: %w", err)
	}
	if current.Status == domain.DeploymentFailed {
		if current.FailureCode != failureCode {
			return domain.Deployment{}, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return domain.Deployment{}, fmt.Errorf("commit transaction: %w", err)
		}
		return deploymentFromSQLC(current), nil
	}
	if current.Status != domain.DeploymentQueued && current.Status != domain.DeploymentWaitingConfirmation {
		return domain.Deployment{}, ErrConflict
	}
	now, err := q.DatabaseClock(ctx)
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("database clock query: %w", err)
	}
	failed, err := q.FailPreAssignmentDeployment(ctx, sqlcgen.FailPreAssignmentDeploymentParams{ID: id, FailureCode: failureCode})
	if err == pgx.ErrNoRows {
		return domain.Deployment{}, ErrConflict
	}
	if err != nil {
		return domain.Deployment{}, fmt.Errorf("fail pre-assignment deployment query: %w", err)
	}
	if changed, changeErr := q.FailPreAssignmentDeploymentRun(ctx, failed.TaskRunID); changeErr != nil {
		return domain.Deployment{}, fmt.Errorf("fail pre-assignment deployment run query: %w", changeErr)
	} else if changed != 1 {
		return domain.Deployment{}, ErrConflict
	}
	audit.CreatedAt = now
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.Deployment{}, fmt.Errorf("create audit event: %w", err)
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.Deployment{}, fmt.Errorf("commit transaction: %w", err)
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

// TransitionDeploymentAttempt follows the completion transaction's lock order:
// task run, per-run advisory log lock, deployment linkage/attempt, exact lease. The
// replay record is checked before expiry only when its complete fenced body
// matches; it cannot grant new mutation authority after reassignment.
// TransitionDeploymentAttempt implements the corresponding repository operation.
func (s *PostgresStore) TransitionDeploymentAttempt(ctx context.Context, request domain.DeploymentTransitionRequest, audit domain.AuditEvent) (domain.Deployment, error) {
	metadata, err := json.Marshal(request.Metadata)
	if err != nil {
		return domain.Deployment{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Deployment{}, err
	}
	defer rollbackTransaction(ctx, tx)
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
// CancelDeploymentRequest implements the corresponding repository operation.
func (s *PostgresStore) CancelDeploymentRequest(ctx context.Context, req domain.DeploymentCancelRequest, audit domain.AuditEvent) (domain.Deployment, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.Deployment{}, err
	}
	defer rollbackTransaction(ctx, tx)
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

// FailDeploymentAndCreateRollback is the post-apply failure boundary. It uses
// one transaction for source settlement and either rollback creation or a loud
// manual fallback when no safe immutable rollback target exists.
// FailDeploymentAndCreateRollback implements the corresponding repository operation.
func (s *PostgresStore) FailDeploymentAndCreateRollback(ctx context.Context, req domain.DeploymentFailureRollbackRequest, failedAudit, rollbackAudit domain.AuditEvent) (domain.DeploymentFailureRollbackResult, error) {
	if (req.ExpectedStatus != domain.DeploymentApplying && req.ExpectedStatus != domain.DeploymentVerifying && req.ExpectedStatus != domain.DeploymentCancelRequested) || req.RequestID == "" || (req.ExpectedStatus == domain.DeploymentCancelRequested && req.CancellationRequestID == "") {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	rollbackDeploymentID, rollbackRunID := domain.RollbackObjectIDs(req.DeploymentID, req.RequestID)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	defer rollbackTransaction(ctx, tx)
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
	if source.Status == domain.DeploymentManualIntervention {
		return replayFailedDeploymentManualIntervention(ctx, tx, q, req, source)
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
	rollbackUnsafe := source.PreviousHealthyRevisionID == nil || !environment.RollbackSafe || environment.CurrentHealthyRevisionID == nil || (source.PreviousHealthyRevisionID != nil && *environment.CurrentHealthyRevisionID != *source.PreviousHealthyRevisionID)
	if rollbackUnsafe {
		return finishFailureManualIntervention(ctx, tx, q, req, failedAudit)
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

// finishFailureManualIntervention is the deliberate loud fallback when a
// post-apply failure has no safe immutable rollback target. It
// uses the same fence and transaction as the normal handoff, but never creates
// a child that could overwrite an unknown target.
func finishFailureManualIntervention(ctx context.Context, tx pgx.Tx, q *sqlcgen.Queries, req domain.DeploymentFailureRollbackRequest, audit domain.AuditEvent) (domain.DeploymentFailureRollbackResult, error) {
	metadata, err := json.Marshal(req.Metadata)
	if err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	updated, err := q.FencedTransitionDeployment(ctx, sqlcgen.FencedTransitionDeploymentParams{ID: req.DeploymentID, Status: req.ExpectedStatus, Status_2: domain.DeploymentManualIntervention, FailureCode: req.FailureCode, Terminal: true})
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

func replayFailedDeploymentManualIntervention(ctx context.Context, tx pgx.Tx, q *sqlcgen.Queries, req domain.DeploymentFailureRollbackRequest, source sqlcgen.Deployment) (domain.DeploymentFailureRollbackResult, error) {
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
	if attempt.Status != "failed" || !deploymentTransitionReplayMatches(transition, domain.DeploymentTransitionRequest{Attempt: req.Attempt, ExpectedStatus: req.ExpectedStatus, TargetStatus: domain.DeploymentManualIntervention, FailureCode: req.FailureCode, Metadata: req.Metadata}, metadata) {
		return domain.DeploymentFailureRollbackResult{}, ErrConflict
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.DeploymentFailureRollbackResult{}, err
	}
	return domain.DeploymentFailureRollbackResult{Failed: deploymentFromSQLC(source)}, nil
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

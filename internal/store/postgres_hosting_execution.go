package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"strconv"
	"time"

	"github.com/distribution/reference"
	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

// StartHostingExecution installs and starts the real River consumer. The server
// must call its stop function before closing the store pool.
func (s *PostgresStore) StartHostingExecution(ctx context.Context) (func(context.Context) error, error) {
	if err := s.configureHostingPorts(); err != nil {
		return nil, err
	}
	var handler hostingjobs.Handler = deploymentOnlyHandler{s}
	if s.hostingBuildPolicy != nil {
		handler = s
	}
	client, err := hostingjobs.New(ctx, s.pool, handler, hostingjobs.Config{})
	if err != nil {
		return nil, err
	}
	if err = client.Start(ctx); err != nil {
		return nil, err
	}
	s.hostingJobs = client
	recoveryCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			recoveryAttempt, stop := context.WithTimeout(recoveryCtx, 3*time.Second)
			_ = s.recoverHostingDispatch(recoveryAttempt)
			stop()
			// Runtime plaintext retention progresses independently of dispatch
			// recovery errors, transactions and their timeout budget.
			purgeAttempt, stopPurge := context.WithTimeout(recoveryCtx, 3*time.Second)
			_, _ = s.PurgeRuntimeLogs(purgeAttempt)
			stopPurge()
			pushAttempt, stopPush := context.WithTimeout(recoveryCtx, 3*time.Second)
			_ = s.recoverGitHubPushDispatch(pushAttempt)
			stopPush()
			autoAttempt, stopAuto := context.WithTimeout(recoveryCtx, 3*time.Second)
			_ = s.recoverAutoDeployDispatch(autoAttempt)
			stopAuto()
			bootstrapAttempt, stopBootstrap := context.WithTimeout(recoveryCtx, 3*time.Second)
			_ = s.recoverServerBootstrapDispatch(bootstrapAttempt)
			stopBootstrap()
			select {
			case <-recoveryCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func(stopCtx context.Context) error {
		cancel()
		select {
		case <-done:
		case <-stopCtx.Done():
			return stopCtx.Err()
		}
		return client.Stop(stopCtx)
	}, nil
}

// Recovery discovers accepted operations independently from River job retention
// or finite retry exhaustion. Each bounded pass rotates the selected prefix.
func (s *PostgresStore) recoverHostingDispatch(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTransaction(ctx, tx)
	ids, err := sqlcgen.New(tx).RecoverQueuedHostingOperations(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, archive, e := archiveOperation(ctx, tx, id); e != nil {
			return e
		} else if archive {
			continue
		}
		if _, err = s.hostingJobs.EnqueueTx(ctx, tx, hostingjobs.PrepareDeploymentArgs{OperationID: id}); err != nil {
			return err
		}
		if _, err = sqlcgen.New(tx).TouchHostingOperation(ctx, id); err != nil {
			return err
		}
	}
	if err = recoverApplicationDatabaseRoutes(ctx, tx, s); err != nil {
		return err
	}
	if err = s.recoverProxyDispatch(ctx, tx); err != nil {
		return err
	}
	if err = s.recoverBuildDispatch(ctx, tx); err != nil {
		return err
	}
	if err = s.recoverArchiveDispatch(ctx, tx); err != nil {
		return err
	}
	if err = s.recoverDatabaseConnectionDispatch(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ErrHostingRestoreHold blocks restored targets until trusted local recovery proof exists.
var ErrHostingRestoreHold = fmt.Errorf("%w: restored hosting ownership requires trusted local journal and process recovery proof", ErrConflict)

func hostingOperation(ctx context.Context, q sqlcgen.DBTX, id string) (domain.HostingOperation, error) {
	if op, found, err := archiveOperation(ctx, q, id); err != nil || found {
		return op, err
	}
	var op domain.HostingOperation
	var snapshot, release, plan, prepared, complete, result, cleanup []byte
	err := func() error {
		row, queryErr := sqlcgen.New(q).GetHostingOperationSnapshot(ctx, id)
		if queryErr != nil {
			return queryErr
		}
		op.ID = row.ID
		op.ProjectID = row.ProjectID
		op.ResourceID = row.ResourceID
		op.BindingID = row.BindingID
		op.RunID = row.RunID
		op.ReconciliationRunID = row.ReconciliationRunID
		op.State = row.State
		op.ReconcileRequested = row.ReconcileRequested
		op.CreatedBy = row.CreatedBy
		op.CreatedAt = row.CreatedAt
		op.UpdatedAt = row.UpdatedAt
		snapshot = row.Snapshot
		release = row.Release
		plan = row.Plan
		cleanup = row.CleanupPlan
		prepared = row.PreparedReceipt
		complete = row.CompleteReceipt
		result = row.Result
		op.MutationHold = row.MutationHold
		if row.RestoreOwnershipHeld == nil {
			return ErrConflict
		}
		op.RestoreOwnershipHeld = *row.RestoreOwnershipHeld
		return nil
	}()
	if err != nil {
		return op, hostingError(err)
	}
	for _, doc := range []struct {
		raw    []byte
		target any
	}{{snapshot, &op.Snapshot}, {release, &op.Release}, {plan, &op.Plan}, {prepared, &op.PreparedReceipt}, {complete, &op.CompleteReceipt}, {result, &op.Result}, {cleanup, &op.CleanupPlan}} {
		if len(doc.raw) > 0 {
			if err = json.Unmarshal(doc.raw, doc.target); err != nil {
				return domain.HostingOperation{}, fmt.Errorf("decode hosting evidence: %w", err)
			}
		}
	}
	if err = loadDatabaseUpgrade(ctx, q, &op); err != nil {
		return domain.HostingOperation{}, err
	}
	return op, nil
}

// GetHostingOperation loads immutable evidence without exposing lease authority.
func (s *PostgresStore) GetHostingOperation(ctx context.Context, id string) (domain.HostingOperation, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingOperation, error) {
		op, err := hostingOperation(ctx, tx, id)
		if err != nil || op.DatabaseUpgrade == nil {
			return op, err
		}
		if err = databaseUpgradeHistoricalRead(ctx, tx, op); err != nil {
			return domain.HostingOperation{}, err
		}
		return op, nil
	})
}

// RequestHostingDeployment captures desired state and inserts the River job in
// one transaction. Reusing a key with changed input is a conflict.
func (s *PostgresStore) RequestHostingDeployment(ctx context.Context, id, resourceID, actorID string, in domain.HostingDeploymentInput, audit domain.AuditEvent) (domain.HostingOperation, error) {
	if s.hostingJobs == nil {
		return domain.HostingOperation{}, hostingjobs.ErrUnavailable
	}
	if in.StorageRouteContinuity != nil {
		canonical, err := domain.CanonicalHostingStorageRouteContinuityInput(*in.StorageRouteContinuity)
		if err != nil {
			return domain.HostingOperation{}, ErrConflict
		}
		in.StorageRouteContinuity = &canonical
	}
	if in.ApplicationDatabaseRouteContinuity != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	if err := domain.ValidateHostingDeployment(in); err != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if in.StorageRouteContinuity != nil {
		if _, _, err = storageContinuityPrelock(ctx, tx, resourceID); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	if err = lockHostingResourceDestination(ctx, tx, resourceID); err != nil {
		return domain.HostingOperation{}, err
	}
	var snap domain.HostingSnapshot
	var projectID string
	var source []byte
	err = func() error {
		row, queryErr := sqlcgen.New(tx).LockHostingResourceTarget(ctx, resourceID)
		if queryErr != nil {
			return queryErr
		}
		if row.Kind != "application" {
			return ErrConflict
		}
		projectID = row.ProjectID
		snap.ResourceGeneration = row.Generation
		source = row.Source
		snap.BindingID = row.BindingID
		snap.DestinationID = row.DestinationID
		snap.RuntimeNamespace = row.RuntimeNamespace
		snap.ServerID = row.ServerID
		snap.RunnerID = row.RunnerID
		return nil
	}()
	if err != nil {
		return domain.HostingOperation{}, hostingError(err)
	}
	if err = authorizeBuildActor(ctx, tx, actorID, projectID, "maintainer"); err != nil {
		return domain.HostingOperation{}, err
	}
	var existingID, requestHash string
	err = func() error {
		row, queryErr := sqlcgen.New(tx).GetHostingIdempotency(ctx, sqlcgen.GetHostingIdempotencyParams{ResourceID: resourceID, IdempotencyKey: in.IdempotencyKey})
		if queryErr != nil {
			return queryErr
		}
		existingID = row.ID
		requestHash = row.RequestHash
		return nil
	}()
	if err == nil {
		if requestHash != domain.HostingDocumentHash(in) {
			return domain.HostingOperation{}, ErrConflict
		}
		op, readErr := hostingOperation(ctx, tx, existingID)
		if readErr != nil {
			return op, readErr
		}
		if err = authorizeBuildActor(ctx, tx, actorID, projectID, "maintainer"); err != nil {
			return domain.HostingOperation{}, err
		}
		return op, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.HostingOperation{}, err
	}
	if err = databaseBindingPendingGate(ctx, tx, resourceID); err != nil {
		return domain.HostingOperation{}, err
	}
	if snap.ResourceGeneration != in.ExpectedGeneration {
		return domain.HostingOperation{}, ErrConflict
	}
	var desired domain.ResourceSource
	if err = json.Unmarshal(source, &desired); err != nil {
		return domain.HostingOperation{}, err
	}
	if desired.Kind != "image" {
		return domain.HostingOperation{}, ErrConflict
	}
	if _, err = domain.NormalizeHostingImage(desired.Image.Reference); err != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	snap.ResourceID = resourceID
	snap.ImageReference = desired.Image.Reference
	snap.ContainerPort = in.ContainerPort
	snap.HostPort = in.HostPort
	snap.Readiness = in.Readiness
	snap.RuntimeConfiguration, err = lockHostingRuntimeConfiguration(ctx, tx, projectID, resourceID, in.ExpectedRuntimeConfigurationGeneration)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	snap.Storage, err = hostingStorageSnapshot(ctx, tx, projectID, resourceID)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if snap.Storage != (domain.HostingStorageRef{}) && snap.RuntimeConfiguration != (domain.HostingRuntimeConfigurationRef{}) {
		return domain.HostingOperation{}, ErrConflict
	}
	snap.PolicyVersion = domain.HostingPolicyVersion
	if snap.RuntimeConfiguration.Generation > 0 {
		snap.PolicyVersion = domain.HostingRuntimePolicyVersion
	}
	if snap.Storage != (domain.HostingStorageRef{}) {
		snap.PolicyVersion = domain.HostingOwnedStoragePolicyVersion
	}
	if err = requireHostingStorageAgent(ctx, tx, snap); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = requireHostingRuntimeAgent(ctx, tx, snap); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingTargetGate(ctx, tx, projectID, snap, true); err != nil {
		return domain.HostingOperation{}, err
	}
	continuity, err := prepareStorageContinuity(ctx, tx, id, projectID, &snap, in)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if err = s.resolveHostingReleasePort(ctx, tx, &snap, in.HostPort); err != nil {
		return domain.HostingOperation{}, err
	}
	_, err = sqlcgen.New(tx).CreateHostingPortReservation(ctx, sqlcgen.CreateHostingPortReservationParams{BindingID: snap.BindingID, DestinationID: snap.DestinationID, HostPort: int32(snap.HostPort)})
	if err != nil {
		return domain.HostingOperation{}, hostingError(err)
	}
	var reserved int
	var admitted bool
	var hold *string
	if err = func() error {
		row, queryErr := sqlcgen.New(tx).LockHostingPortReservation(ctx, snap.BindingID)
		if queryErr != nil {
			return queryErr
		}
		reserved = int(row.HostPort)
		admitted = row.RestoreAdmitted
		hold = row.HoldOperationID
		return nil
	}(); err != nil {
		return domain.HostingOperation{}, err
	}
	if reserved < 1024 || !admitted || hold != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	var busy bool
	if err = func() error {
		row, queryErr := sqlcgen.New(tx).HostingBindingBusy(ctx, snap.BindingID)
		if queryErr != nil {
			return queryErr
		}
		busy = row
		return nil
	}(); err != nil {
		return domain.HostingOperation{}, err
	}
	if busy {
		return domain.HostingOperation{}, ErrConflict
	}
	_, err = sqlcgen.New(tx).CreateHostingOperation(ctx, sqlcgen.CreateHostingOperationParams{ID: id, ProjectID: projectID, ResourceID: resourceID, BindingID: snap.BindingID, IdempotencyKey: in.IdempotencyKey, RequestHash: domain.HostingDocumentHash(in), CreatedBy: actorID})
	if err != nil {
		return domain.HostingOperation{}, hostingError(err)
	}
	if _, err = sqlcgen.New(tx).CreateHostingReleaseClaim(ctx, sqlcgen.CreateHostingReleaseClaimParams{OperationID: id, DestinationID: snap.DestinationID, HostPort: int32(snap.HostPort), BindingID: snap.BindingID}); err != nil {
		return domain.HostingOperation{}, hostingError(err)
	}
	contents, err := json.Marshal(snap)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).CreateHostingSnapshot(ctx, sqlcgen.CreateHostingSnapshotParams{OperationID: id, Body: contents, BodyHash: domain.HostingDocumentHash(snap)}); err != nil {
		return domain.HostingOperation{}, err
	}
	if continuity != nil {
		if err = sqlcgen.New(tx).CreateStorageRouteContinuity(ctx, sqlcgen.CreateStorageRouteContinuityParams{OperationID: id, ProxyID: continuity.ProxyID, Descriptor: buildJSON(continuity), DescriptorHash: snap.StorageRouteContinuity.Hash, AcceptedRequest: buildJSON(in)}); err != nil {
			return domain.HostingOperation{}, hostingError(err)
		}
	}
	audit.ActorID, audit.Action, audit.TargetID = actorID, "hosting.deployment_requested", id
	audit.Metadata = map[string]any{"project_id": projectID, "resource_id": resourceID, "binding_id": snap.BindingID}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = s.hostingJobs.EnqueueTx(ctx, tx, hostingjobs.PrepareDeploymentArgs{OperationID: id}); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, id)
	if err != nil {
		return op, err
	}
	if err = authorizeBuildActor(ctx, tx, actorID, projectID, "maintainer"); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = requireHostingRuntimeAgent(ctx, tx, snap); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.HostingOperation{}, err
	}
	return op, nil
}

// hostingTargetGate locks grant -> binding state -> runner, always after the
// task/lease locks on attempt paths. Revocation serializes with these locks.
func hostingTargetGate(ctx context.Context, tx pgx.Tx, projectID string, snap domain.HostingSnapshot, allowMissingState bool) error {
	return hostingTargetGateMode(ctx, tx, projectID, snap, allowMissingState, false)
}
func hostingTargetGateMode(ctx context.Context, tx pgx.Tx, projectID string, snap domain.HostingSnapshot, allowMissingState, observe bool) error {
	if err := requireHostingStorageAgent(ctx, tx, snap); err != nil {
		return err
	}
	if err := sqlcgen.New(tx).LockHostingExecutionDestination(ctx, snap.DestinationID); err != nil {
		return err
	}
	if snap.PolicyVersion == domain.HostingReleasePolicyVersion {
		if err := requireHostingReleaseAgent(ctx, tx, snap.RunnerID); err != nil {
			return err
		}
	}
	var revoked *time.Time
	if err := func() error {
		row, queryErr := sqlcgen.New(tx).LockHostingProjectGrant(ctx, sqlcgen.LockHostingProjectGrantParams{DestinationID: snap.DestinationID, ProjectID: projectID})
		if queryErr != nil {
			return queryErr
		}
		revoked = row
		return nil
	}(); err != nil {
		return hostingError(err)
	}
	if revoked != nil {
		return ErrConflict
	}
	var restored bool
	if err := func() error {
		row, queryErr := sqlcgen.New(tx).HostingBindingRestoreHeld(ctx, snap.BindingID)
		if queryErr != nil {
			return queryErr
		}
		restored = row
		return nil
	}(); err != nil {
		return err
	}
	if restored && !observe {
		return ErrHostingRestoreHold
	}
	var admitted bool
	err := func() error {
		row, queryErr := sqlcgen.New(tx).LockHostingAdmissionState(ctx, snap.BindingID)
		if queryErr != nil {
			return queryErr
		}
		admitted = row.RestoreAdmitted
		return nil
	}()
	if err != nil && (!allowMissingState || !errors.Is(err, pgx.ErrNoRows)) {
		return hostingError(err)
	}
	if err == nil && !admitted && !observe {
		return ErrHostingRestoreHold
	}
	err = func() error {
		row, queryErr := sqlcgen.New(tx).LockHostingExactRunner(ctx, sqlcgen.LockHostingExactRunnerParams{ID: snap.BindingID, ResourceID: snap.ResourceID, ID_2: snap.DestinationID, ID_3: snap.ServerID, ID_4: snap.RunnerID, ProjectID: projectID, Capability: hostingSnapshotTaskType(snap)})
		if queryErr != nil {
			return queryErr
		}
		_ = row
		return nil
	}()
	if err != nil {
		return hostingError(err)
	}
	return nil
}

// PrepareDeployment is the real River handler: one transaction dispatches one
// typed task and records its immutable exact runner target and dispatch receipt.
func (s *PostgresStore) PrepareDeployment(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTransaction(ctx, tx)
	op, err := hostingOperation(ctx, tx, id)
	if err != nil {
		return err
	}
	if op.State != "queued" {
		return nil
	}
	if err = hostingFreshRuntimeGate(ctx, tx, op); err == nil {
		err = hostingOperationTargetGate(ctx, tx, op, false)
	}
	if err != nil {
		if errors.Is(err, ErrHostingRestoreHold) {
			return nil
		}
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			if _, updateErr := sqlcgen.New(tx).FailQueuedHostingOperation(ctx, id); updateErr != nil {
				return updateErr
			}
			if _, releaseErr := sqlcgen.New(tx).ReleaseHostingFailedClaim(ctx, id); releaseErr != nil {
				return releaseErr
			}
			if releaseErr := releaseUnmutatedStorageContinuity(ctx, tx, op); releaseErr != nil {
				return releaseErr
			}
			if auditErr := hostingPhaseAudit(ctx, tx, s.queries, op, op.CreatedBy, "preflight_failed"); auditErr != nil {
				return auditErr
			}
			return tx.Commit(ctx)
		}
		return err
	}
	var state string
	if err = func() error {
		row, queryErr := sqlcgen.New(tx).LockHostingOperationState(ctx, id)
		if queryErr != nil {
			return queryErr
		}
		state = row
		return nil
	}(); err != nil {
		return err
	}
	if state != "queued" {
		return nil
	}
	runID, mode := "run_"+id, "deploy"
	if op.CleanupPlan != nil {
		runID, mode = "cleanup_"+id, "cleanup"
	}
	spec := domain.RunSpec{Type: hostingSnapshotTaskType(op.Snapshot), Inputs: map[string]any{}, HostingOperationID: id, HostingMode: mode}
	body, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	_, err = sqlcgen.New(tx).CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: runID, ProjectID: op.ProjectID, RunSpec: body, RequestedBy: op.CreatedBy})
	if err != nil {
		return err
	}
	if mode == "cleanup" {
		_, err = sqlcgen.New(tx).CreateHostingCleanupTaskTarget(ctx, sqlcgen.CreateHostingCleanupTaskTargetParams{RunID: runID, OperationID: id, BindingID: op.BindingID, RunnerID: op.Snapshot.RunnerID})
	} else {
		_, err = sqlcgen.New(tx).CreateHostingDeploymentTarget(ctx, sqlcgen.CreateHostingDeploymentTargetParams{RunID: runID, OperationID: id, BindingID: op.BindingID, RunnerID: op.Snapshot.RunnerID})
	}
	if err != nil {
		return err
	}
	if _, err = sqlcgen.New(tx).CreateHostingDispatchReceipt(ctx, sqlcgen.CreateHostingDispatchReceiptParams{OperationID: id, RunID: runID}); err != nil {
		return err
	}
	if _, err = sqlcgen.New(tx).DispatchHostingOperation(ctx, sqlcgen.DispatchHostingOperationParams{ID: id, RunID: hostingStringPointer(runID)}); err != nil {
		return err
	}
	audit := domain.AuditEvent{ID: "aud_dispatch_" + id, ActorID: op.CreatedBy, Action: "hosting.dispatched", TargetID: id, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"project_id": op.ProjectID, "resource_id": op.ResourceID, "binding_id": op.BindingID, "run_id": runID, "runner_id": op.Snapshot.RunnerID}}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return err
	}
	if err = localHostingOperationAuthority(ctx, tx, op, false); err != nil {
		return err
	}
	if err = runtimeHostingActorAuthority(ctx, tx, op); err != nil {
		return err
	}
	if op.CleanupPlan == nil {
		if err = requireHostingRuntimeAgent(ctx, tx, op.Snapshot); err != nil {
			return err
		}
	}
	if op.DatabaseUpgrade != nil {
		if err = finalizeDatabaseUpgradeRequest(ctx, tx, op); err != nil {
			return err
		}
	}
	if connectionOperation(op) {
		if err = sqlcgen.New(tx).FlushDatabaseConnection(ctx); err != nil {
			return err
		}
		if err = databaseConnectionAuthority(ctx, tx, op, false, false); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func hostingAuthorityExists(ctx context.Context, tx pgx.Tx, runnerID string, a domain.HostingAuthority, live bool) error {
	// PostgreSQL stores attempts as int32. Reject values before narrowing so
	// another submitted integer cannot alias the persisted lease authority.
	if a.Attempt < 1 || a.Attempt > math.MaxInt32 {
		return ErrConflict
	}
	if _, err := sqlcgen.New(tx).LockHostingTaskForAuthority(ctx, a.RunID); err != nil {
		return err
	}
	if err := func() error {
		row, queryErr := sqlcgen.New(tx).CheckHostingAuthority(ctx, sqlcgen.CheckHostingAuthorityParams{ID: a.LeaseID, RunID: a.RunID, RunnerID: runnerID, Attempt: int32(a.Attempt), Fence: a.Fence, OperationID: a.OperationID, Column7: live})
		if queryErr != nil {
			return queryErr
		}
		_ = row
		return nil
	}(); err != nil {
		return hostingError(err)
	}
	return nil
}

// HostingRunnerPlan checks current exact-target lease authority before returning a plan.
func (s *PostgresStore) HostingRunnerPlan(ctx context.Context, runnerID string, a domain.HostingAuthority) (domain.HostingOperation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = hostingAuthorityExists(ctx, tx, runnerID, a, true); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, a.OperationID)
	if err != nil {
		return op, err
	}
	var mode string
	if err = func() error {
		row, queryErr := sqlcgen.New(tx).GetHostingTaskMode(ctx, a.RunID)
		if queryErr != nil {
			return queryErr
		}
		mode = row
		return nil
	}(); err != nil {
		return domain.HostingOperation{}, err
	}
	op.TaskMode = mode
	if mode == "observe" {
		if !op.ReconcileRequested || op.ReconciliationRunID != a.RunID {
			return domain.HostingOperation{}, ErrConflict
		}
		if err = hostingOperationTargetGate(ctx, tx, op, true); err != nil {
			return domain.HostingOperation{}, err
		}
		var raw []byte
		if err = func() error {
			row, queryErr := sqlcgen.New(tx).GetHostingHealthy(ctx, op.BindingID)
			if queryErr != nil {
				return queryErr
			}
			raw = row
			return nil
		}(); err != nil {
			return domain.HostingOperation{}, err
		}
		if len(raw) > 0 {
			if err = json.Unmarshal(raw, &op.ReconciliationHealthy); err != nil {
				return domain.HostingOperation{}, err
			}
		}
		if op.ReconciliationHealthy != nil && domain.ValidateHostingHealthyStorage(*op.ReconciliationHealthy) != nil {
			return domain.HostingOperation{}, ErrConflict
		}
		if err = finalLocalHostingAuthority(ctx, tx, runnerID, a, op, true); err != nil {
			return domain.HostingOperation{}, err
		}
		if op.Snapshot.Storage != (domain.HostingStorageRef{}) && !connectionOperation(op) {
			if err = finalHostingStorageAuthority(ctx, tx, runnerID, a, op); err != nil {
				return domain.HostingOperation{}, err
			}
		}
		if err = finalDatabaseConnectionAuthority(ctx, tx, runnerID, a, op, true, false); err != nil {
			return domain.HostingOperation{}, err
		}
		return op, nil
	}
	if mode == "storage_recover" {
		if !storageRecoveryRun(ctx, tx, op, a.RunID) || op.State != "failed" || !op.MutationHold {
			return domain.HostingOperation{}, ErrConflict
		}
		if err = finalDatabaseConnectionAuthority(ctx, tx, runnerID, a, op, false, false); err != nil {
			return domain.HostingOperation{}, err
		}
		return op, nil
	}
	if mode == "finalize" {
		if err = continuityFinalizeAuthority(ctx, tx, runnerID, a, op, false); err != nil {
			return domain.HostingOperation{}, err
		}
		return op, nil
	}
	if op.State != "dispatched" && op.State != "preparing" && op.State != "prepared" {
		return domain.HostingOperation{}, ErrConflict
	}
	if err = hostingFreshRuntimeGate(ctx, tx, op); err == nil {
		err = hostingOperationTargetGate(ctx, tx, op, false)
	}
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if op.MutationHold {
		return domain.HostingOperation{}, ErrConflict
	}
	if _, err = sqlcgen.New(tx).BeginHostingPreparation(ctx, op.ID); err != nil {
		return domain.HostingOperation{}, err
	}
	if mode == "cleanup" {
		if _, err = sqlcgen.New(tx).FinishHostingPreparation(ctx, op.ID); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	op, err = hostingOperation(ctx, tx, op.ID)
	if err != nil {
		return op, err
	}
	op.TaskMode = mode
	if err = finalLocalHostingAuthority(ctx, tx, runnerID, a, op, false); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = finalHostingRuntimeAuthority(ctx, tx, runnerID, a, op); err != nil {
		return domain.HostingOperation{}, err
	}
	return op, tx.Commit(ctx)
}

// replayHostingReceipt checks stable identity/body before live-lease admission.
func replayHostingReceipt(ctx context.Context, tx pgx.Tx, a domain.HostingAuthority, phase, id, hash string) (bool, error) {
	var storedID, storedHash string
	var authority []byte
	err := func() error {
		row, queryErr := sqlcgen.New(tx).GetHostingReceipt(ctx, sqlcgen.GetHostingReceiptParams{OperationID: a.OperationID, Phase: phase})
		if queryErr != nil {
			return queryErr
		}
		storedID = row.ReceiptID
		storedHash = row.BodyHash
		authority = row.Authority
		return nil
	}()
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var old domain.HostingAuthority
	if err = json.Unmarshal(authority, &old); err != nil {
		return false, err
	}
	if old != a || storedID != id || storedHash != hash {
		return false, ErrConflict
	}
	op, err := hostingOperation(ctx, tx, a.OperationID)
	if err != nil {
		return false, err
	}
	if op.DatabaseUpgrade != nil {
		if err = databaseUpgradeHistoricalRead(ctx, tx, op); err != nil {
			return false, err
		}
	}
	return true, nil
}

func insertHostingReceipt(ctx context.Context, tx pgx.Tx, a domain.HostingAuthority, phase, id string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	authority, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = sqlcgen.New(tx).CreateHostingReceipt(ctx, sqlcgen.CreateHostingReceiptParams{OperationID: a.OperationID, Phase: phase, ReceiptID: id, Authority: authority, Body: raw, BodyHash: domain.HostingDocumentHash(body)})
	return hostingError(err)
}

// HostingPrepared atomically commits immutable preparation evidence or replays its receipt.
func (s *PostgresStore) HostingPrepared(ctx context.Context, runnerID string, in domain.HostingPreparedRequest) (domain.HostingOperation, error) {
	if in.ReceiptID == "" || len(in.ReceiptID) > 128 || domain.ValidateHostingRelease(in.Release) != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = hostingRequireMode(ctx, tx, in.HostingAuthority, "deploy"); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingAuthorityExists(ctx, tx, runnerID, in.HostingAuthority, false); err != nil {
		return domain.HostingOperation{}, err
	}
	replay, err := replayHostingReceipt(ctx, tx, in.HostingAuthority, "prepared", in.ReceiptID, domain.HostingDocumentHash(in.Release))
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if replay {
		op, readErr := hostingOperation(ctx, tx, in.OperationID)
		if readErr != nil {
			return op, readErr
		}
		op.TaskMode = "deploy"
		if err = databaseConnectionHistoricalRunnerRead(ctx, tx, runnerID, op); err != nil {
			return domain.HostingOperation{}, err
		}
		return op, nil
	}
	if err = hostingAuthorityExists(ctx, tx, runnerID, in.HostingAuthority, true); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, in.OperationID)
	if err != nil {
		return op, err
	}
	if op.State != "preparing" && op.State != "prepared" {
		return domain.HostingOperation{}, ErrConflict
	}
	if err = hostingFreshRuntimeGate(ctx, tx, op); err == nil {
		err = hostingOperationTargetGate(ctx, tx, op, false)
	}
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if op.Release != nil {
		if *op.Release != in.Release {
			return domain.HostingOperation{}, ErrConflict
		}
		op.TaskMode = "deploy"
		return op, nil
	}
	if op.Snapshot.Database != (domain.HostingDatabaseExecutionRef{}) {
		if domain.ValidateHostingDatabaseRelease(op.Snapshot, in.Release) != nil {
			return domain.HostingOperation{}, ErrConflict
		}
	} else if op.Snapshot.LocalBuild != (domain.HostingLocalBuildSource{}) {
		if domain.ValidateHostingSnapshotRelease(op.Snapshot, in.Release) != nil {
			return domain.HostingOperation{}, ErrConflict
		}
	} else {
		// Supplied digest must match the repository and digest recorded by Docker.
		original, normalizeErr := domain.NormalizeHostingImage(op.Snapshot.ImageReference)
		if normalizeErr != nil || in.Release.LocalBuild != (domain.HostingBuildArtifact{}) || !hostingReleaseMatchesSource(original, in.Release.CanonicalImage) {
			return domain.HostingOperation{}, ErrConflict
		}
	}
	var daemon *string
	var previousRaw []byte
	var hold *string
	if err = func() error {
		row, queryErr := sqlcgen.New(tx).GetHostingPreparedBinding(ctx, op.BindingID)
		if queryErr != nil {
			return queryErr
		}
		daemon = row.DaemonID
		previousRaw = row.Healthy
		hold = row.HoldOperationID
		return nil
	}(); err != nil {
		return domain.HostingOperation{}, err
	}
	if hold != nil || (daemon != nil && *daemon != in.Release.DaemonID) {
		return domain.HostingOperation{}, ErrConflict
	}
	var previous *domain.HostingHealthy
	if len(previousRaw) > 0 {
		if err = json.Unmarshal(previousRaw, &previous); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	var plan domain.HostingApplyPlan
	if op.ApplicationDatabaseStorage != nil {
		plan, err = compileHostingStoragePlan(ctx, tx, op, in.Release, previous, nil)
	} else if op.ApplicationDatabase != nil {
		input, inputErr := domain.HostingApplicationDatabaseExecutionPlanInput(*op.ApplicationDatabase)
		if inputErr != nil {
			return domain.HostingOperation{}, ErrConflict
		}
		plan, err = domain.CompileHostingApplicationDatabasePlan(op.Snapshot, in.Release, previous, nil, input)
	} else if op.Snapshot.Database != (domain.HostingDatabaseExecutionRef{}) {
		plan, err = compileHostingStoragePlan(ctx, tx, op, in.Release, previous, nil)
	} else if op.Snapshot.PolicyVersion == domain.HostingOwnedStoragePolicyVersion || op.Snapshot.PolicyVersion == domain.HostingReleasePolicyVersion || op.Snapshot.PolicyVersion == domain.HostingLocalBuildPolicyVersion || op.Snapshot.PolicyVersion == domain.HostingRuntimePolicyVersion || op.Snapshot.PolicyVersion == domain.HostingLocalBuildRuntimePolicyVersion {
		var retained []domain.HostingHealthy
		rows, readErr := sqlcgen.New(tx).GetHostingRetainedReleases(ctx, op.BindingID)
		if readErr != nil {
			return domain.HostingOperation{}, readErr
		}
		for _, row := range rows {
			if previous != nil && row.OperationID == previous.OperationID {
				continue
			}
			var old domain.HostingHealthy
			if err = json.Unmarshal(row.Body, &old); err != nil {
				return domain.HostingOperation{}, err
			}
			retained = append(retained, old)
		}
		if op.Snapshot.Storage != (domain.HostingStorageRef{}) {
			plan, err = compileHostingStoragePlan(ctx, tx, op, in.Release, previous, retained)
		} else if op.Snapshot.LocalBuild != (domain.HostingLocalBuildSource{}) {
			plan, err = domain.CompileLocalBuildHostingPlan(op.Snapshot, in.Release, previous, retained)
		} else {
			plan, err = domain.CompileRetainedHostingPlan(op.Snapshot, in.Release, previous, retained)
		}
	} else {
		plan, err = domain.CompileHostingPlan(op.Snapshot, in.Release, previous)
	}
	if err != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	if connectionOperation(op) {
		candidate := op
		candidate.Plan, candidate.Release = &plan, &in.Release
		if err = connectionPublicationBound(candidate); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	releaseRaw, err := json.Marshal(in.Release)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	planRaw, err := json.Marshal(plan)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).ObserveHostingDaemon(ctx, sqlcgen.ObserveHostingDaemonParams{BindingID: op.BindingID, DaemonID: hostingStringPointer(in.Release.DaemonID)}); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).CreateHostingRelease(ctx, sqlcgen.CreateHostingReleaseParams{OperationID: op.ID, Body: releaseRaw, BodyHash: in.Release.Hash}); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).CreateHostingApplyPlan(ctx, sqlcgen.CreateHostingApplyPlanParams{OperationID: op.ID, Body: planRaw, BodyHash: plan.Hash}); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = insertHostingReceipt(ctx, tx, in.HostingAuthority, "prepared", in.ReceiptID, in.Release); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingPhaseAudit(ctx, tx, s.queries, op, runnerID, "prepared"); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).FinishHostingPreparation(ctx, op.ID); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err = hostingOperation(ctx, tx, op.ID)
	if err != nil {
		return op, err
	}
	if err = finalLocalHostingAuthority(ctx, tx, runnerID, in.HostingAuthority, op, false); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = finalHostingRuntimeAuthority(ctx, tx, runnerID, in.HostingAuthority, op); err != nil {
		return domain.HostingOperation{}, err
	}
	if op.Snapshot.Storage != (domain.HostingStorageRef{}) {
		if err = finalHostingStorageAuthority(ctx, tx, runnerID, in.HostingAuthority, op); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	op.TaskMode = "deploy"
	return op, tx.Commit(ctx)
}

func hostingReleaseMatchesSource(source, canonical string) bool {
	original, err := reference.ParseNormalizedNamed(source)
	if err != nil {
		return false
	}
	resolved, err := reference.ParseNormalizedNamed(canonical)
	if err != nil || reference.TrimNamed(original).Name() != reference.TrimNamed(resolved).Name() {
		return false
	}
	if digest, ok := original.(reference.Digested); ok {
		actual, ok := resolved.(reference.Digested)
		return ok && actual.Digest() == digest.Digest()
	}
	return true
}

// HostingApplyBegin atomically admits mutation after rechecking the target and live lease.
func (s *PostgresStore) HostingApplyBegin(ctx context.Context, runnerID string, a domain.HostingAuthority) (domain.HostingOperation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = hostingAuthorityExists(ctx, tx, runnerID, a, true); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingTaskOperation(ctx, tx, a)
	if err != nil {
		return op, err
	}
	storageHeld := op.Snapshot.Storage != (domain.HostingStorageRef{}) && op.MutationHold && op.State == "prepared"
	if op.Snapshot.Storage != (domain.HostingStorageRef{}) {
		if !op.MutationHold {
			return domain.HostingOperation{}, ErrConflict
		}
		if err = hostingFreshRuntimeGate(ctx, tx, op); err != nil {
			return domain.HostingOperation{}, err
		}
		if err = hostingOperationTargetGate(ctx, tx, op, false); err != nil {
			return domain.HostingOperation{}, err
		}
		if err = hostingStorageWriterReady(ctx, tx, a, op); err != nil {
			return domain.HostingOperation{}, err
		}
		if err = finalHostingStorageAuthority(ctx, tx, runnerID, a, op); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	if op.MutationHold && op.State == "applying" && op.CleanupPlan == nil {
		if !hostingOperationPlanMatches(op) {
			return domain.HostingOperation{}, ErrConflict
		}
		if err = replayHostingApplyAdmission(ctx, tx, a, op); err != nil {
			return domain.HostingOperation{}, err
		}
		if err = finalDatabaseConnectionAuthority(ctx, tx, runnerID, a, op, false, false); err != nil {
			return domain.HostingOperation{}, err
		}
		return op, nil
	}
	if err = autoFirstMutation(ctx, tx, op.ID, true); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingFreshRuntimeGate(ctx, tx, op); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingOperationTargetGate(ctx, tx, op, false); err != nil {
		return domain.HostingOperation{}, err
	}
	if op.CleanupPlan != nil {
		return s.admitHostingCleanup(ctx, tx, runnerID, a, op)
	}
	if err = hostingRequireMode(ctx, tx, a, "deploy"); err != nil {
		return domain.HostingOperation{}, err
	}
	if op.Release == nil || !hostingOperationPlanMatches(op) {
		return domain.HostingOperation{}, ErrConflict
	}
	if op.Snapshot.Database == (domain.HostingDatabaseExecutionRef{}) && !op.Plan.RetainPrevious {
		referenced, checkErr := sqlcgen.New(tx).HostingReleasePortReferenced(ctx, sqlcgen.HostingReleasePortReferencedParams{DestinationID: op.Snapshot.DestinationID, HostPort: int32(op.Snapshot.HostPort)})
		if checkErr != nil {
			return domain.HostingOperation{}, checkErr
		}
		if referenced && op.Snapshot.StorageRouteContinuity == (domain.HostingStorageRouteContinuityRef{}) {
			return domain.HostingOperation{}, ErrConflict
		}
	}
	if op.State != "prepared" || op.MutationHold && !storageHeld {
		return domain.HostingOperation{}, ErrConflict
	}
	if !storageHeld {
		tag, err := sqlcgen.New(tx).AdmitHostingMutation(ctx, sqlcgen.AdmitHostingMutationParams{BindingID: op.BindingID, HoldOperationID: hostingStringPointer(op.ID), DaemonID: hostingStringPointer(op.Release.DaemonID)})
		if err != nil {
			return domain.HostingOperation{}, err
		}
		if tag != 1 {
			return domain.HostingOperation{}, ErrConflict
		}
		if _, err = sqlcgen.New(tx).HoldHostingReleaseClaim(ctx, op.ID); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	authorityRaw, marshalErr := json.Marshal(a)
	if marshalErr != nil {
		return domain.HostingOperation{}, marshalErr
	}
	if _, err = sqlcgen.New(tx).CreateHostingApplyAdmission(ctx, sqlcgen.CreateHostingApplyAdmissionParams{OperationID: op.ID, RunID: a.RunID, LeaseID: a.LeaseID, Authority: authorityRaw, PlanHash: op.Plan.Hash}); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).BeginHostingApply(ctx, op.ID); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingPhaseAudit(ctx, tx, s.queries, op, runnerID, "apply_admitted"); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err = hostingTaskOperation(ctx, tx, a)
	if err != nil {
		return op, err
	}
	if err = finalLocalHostingAuthority(ctx, tx, runnerID, a, op, false); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = finalHostingRuntimeAuthority(ctx, tx, runnerID, a, op); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = autoFirstMutation(ctx, tx, op.ID, false); err != nil {
		return domain.HostingOperation{}, err
	}
	if op.Snapshot.Storage != (domain.HostingStorageRef{}) {
		if err = finalHostingStorageAuthority(ctx, tx, runnerID, a, op); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	return op, tx.Commit(ctx)
}

func hostingPhaseAudit(ctx context.Context, tx pgx.Tx, q *sqlcgen.Queries, op domain.HostingOperation, actor, phase string) error {
	return createAuditWithQueries(ctx, q.WithTx(tx), domain.AuditEvent{ID: "aud_hosting_" + phase + "_" + op.ID, ActorID: actor, Action: "hosting." + phase, TargetID: op.ID, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"project_id": op.ProjectID, "resource_id": op.ResourceID, "run_id": op.RunID, "binding_id": op.BindingID}})
}

// HostingComplete atomically records result evidence and settles only a safe owned hold.
func (s *PostgresStore) HostingComplete(ctx context.Context, runnerID string, in domain.HostingCompleteRequest) (domain.HostingOperation, error) {
	if in.ReceiptID == "" || len(in.ReceiptID) > 128 || (in.Result.Outcome != "succeeded" && in.Result.Outcome != "failed") {
		return domain.HostingOperation{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = hostingAuthorityExists(ctx, tx, runnerID, in.HostingAuthority, false); err != nil {
		return domain.HostingOperation{}, err
	}
	replay, err := replayHostingReceipt(ctx, tx, in.HostingAuthority, "complete", in.ReceiptID, domain.HostingDocumentHash(in.Result))
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if replay {
		op, e := hostingTaskOperation(ctx, tx, in.HostingAuthority)
		if e != nil {
			return op, e
		}
		return op, databaseConnectionHistoricalRunnerRead(ctx, tx, runnerID, op)
	}
	// Only a previously committed matching receipt survives loss of authority.
	// Every first result remains a fenced mutation, even when a hold exists.
	if err = hostingAuthorityExists(ctx, tx, runnerID, in.HostingAuthority, true); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, in.OperationID)
	if err != nil {
		return op, err
	}
	if connectionOperation(op) {
		return s.completeDatabaseConnection(ctx, tx, runnerID, in, op)
	}
	if op.Snapshot.LocalBuild != (domain.HostingLocalBuildSource{}) {
		if err = hostingOperationTargetGate(ctx, tx, op, false); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	if err = sqlcgen.New(tx).LockHostingExecutionDestination(ctx, op.Snapshot.DestinationID); err != nil {
		return domain.HostingOperation{}, err
	}
	if op.Snapshot.StorageRouteContinuity != (domain.HostingStorageRouteContinuityRef{}) {
		if _, _, err = storageContinuityPrelock(ctx, tx, op.ResourceID); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	if op.Snapshot.Storage != (domain.HostingStorageRef{}) {
		if err = hostingFreshStorageGate(ctx, tx, op); err != nil {
			return domain.HostingOperation{}, err
		}
		if err = hostingOperationTargetGate(ctx, tx, op, false); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	if op.Snapshot.PolicyVersion == domain.HostingReleasePolicyVersion {
		if err = requireHostingReleaseAgent(ctx, tx, runnerID); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	if op.State == "succeeded" || op.State == "failed" || op.State == "canceled" {
		return domain.HostingOperation{}, ErrConflict
	}
	if op.CleanupPlan != nil {
		return s.completeHostingCleanup(ctx, tx, runnerID, in, op)
	}
	if err = hostingRequireMode(ctx, tx, in.HostingAuthority, "deploy"); err != nil {
		return domain.HostingOperation{}, err
	}
	if op.Snapshot.Storage != (domain.HostingStorageRef{}) && op.MutationHold {
		if in.Result.Outcome == "succeeded" || in.Result.Restored {
			if op.State != "applying" && op.State != "verifying" {
				return domain.HostingOperation{}, ErrConflict
			}
			if err = replayHostingApplyAdmission(ctx, tx, in.HostingAuthority, op); err != nil {
				return domain.HostingOperation{}, err
			}
		}
		if err = hostingStorageWriterReady(ctx, tx, in.HostingAuthority, op); err != nil {
			if in.Result.Outcome != "failed" || !in.Result.Uncertain || in.Result.Restored {
				return domain.HostingOperation{}, ErrConflict
			}
		}
	}
	// Pre-apply failure cannot assert external success. A post-admission
	// uncertain result retains the independent hold under live authority.
	if !op.MutationHold {
		if in.Result.Outcome != "failed" || in.Result.Uncertain || op.State == "cancel_requested" {
			return domain.HostingOperation{}, ErrConflict
		}
	} else {
		if op.Plan == nil || op.Release == nil || in.Result.PlanHash != op.Plan.Hash || in.Result.DaemonID != op.Release.DaemonID {
			return domain.HostingOperation{}, ErrConflict
		}
		if in.Result.Outcome == "succeeded" && (!in.Result.Ready || in.Result.ImageID != op.Release.ImageID || in.Result.ContainerID == "") {
			return domain.HostingOperation{}, ErrConflict
		}
	}
	if err = insertHostingReceipt(ctx, tx, in.HostingAuthority, "complete", in.ReceiptID, in.Result); err != nil {
		return domain.HostingOperation{}, err
	}
	uncertain := op.MutationHold && (in.Result.Uncertain || (in.Result.Outcome == "failed" && !in.Result.Restored))
	state := in.Result.Outcome
	if uncertain {
		state = "reconciliation_required"
	}
	if _, err = sqlcgen.New(tx).SetHostingOperationState(ctx, sqlcgen.SetHostingOperationStateParams{ID: op.ID, State: state}); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).CompleteHostingLease(ctx, sqlcgen.CompleteHostingLeaseParams{ID: in.LeaseID, Status: in.Result.Outcome, CompletionKey: hostingStringPointer(in.ReceiptID)}); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).CompleteHostingTask(ctx, sqlcgen.CompleteHostingTaskParams{ID: op.RunID, Status: in.Result.Outcome}); err != nil {
		return domain.HostingOperation{}, err
	}
	if op.MutationHold && !uncertain {
		if in.Result.Outcome == "succeeded" {
			healthy := domain.HostingHealthy{Snapshot: op.Snapshot, OperationID: op.ID, ContainerID: in.Result.ContainerID, Release: *op.Release, PlanHash: op.Plan.Hash}
			healthy.Storage, err = hostingStorageHealthyEvidence(ctx, tx, op)
			if err != nil {
				return domain.HostingOperation{}, err
			}
			if op.Snapshot.Database == (domain.HostingDatabaseExecutionRef{}) && op.Plan.PreviousHealthy != nil {
				if _, err = sqlcgen.New(tx).RetainPreviousHostingRelease(ctx, sqlcgen.RetainPreviousHostingReleaseParams{OperationID: op.Plan.PreviousHealthy.OperationID, RetainedRunning: op.Plan.RetainPrevious}); err != nil {
					return domain.HostingOperation{}, err
				}
			}
			healthyRaw, marshalErr := json.Marshal(healthy)
			if marshalErr != nil {
				return domain.HostingOperation{}, marshalErr
			}
			if op.Snapshot.Database == (domain.HostingDatabaseExecutionRef{}) {
				if _, err = sqlcgen.New(tx).RecordHostingHealthyRelease(ctx, sqlcgen.RecordHostingHealthyReleaseParams{OperationID: op.ID, BindingID: op.BindingID, DestinationID: op.Snapshot.DestinationID, HostPort: int32(op.Snapshot.HostPort), Body: healthyRaw}); err != nil {
					return domain.HostingOperation{}, fmt.Errorf("record healthy public release: %w", err)
				}
				if _, err = sqlcgen.New(tx).RetainHostingReleaseClaim(ctx, op.ID); err != nil {
					return domain.HostingOperation{}, fmt.Errorf("retain healthy public release claim: %w", err)
				}
			}

			raw, err := json.Marshal(healthy)
			if err != nil {
				return domain.HostingOperation{}, err
			}
			if _, err = sqlcgen.New(tx).SetHostingHealthy(ctx, sqlcgen.SetHostingHealthyParams{BindingID: op.BindingID, Healthy: raw, HoldOperationID: hostingStringPointer(op.ID)}); err != nil {
				return domain.HostingOperation{}, err
			}
		}
		if _, err = sqlcgen.New(tx).ClearHostingMutationHold(ctx, sqlcgen.ClearHostingMutationHoldParams{BindingID: op.BindingID, HoldOperationID: hostingStringPointer(op.ID)}); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	if in.Result.Outcome == "failed" && !uncertain {
		if _, err = sqlcgen.New(tx).ReleaseHostingFailedClaim(ctx, op.ID); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	if err = s.settleStorageContinuity(ctx, tx, op, in.Result); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = s.autoWakeup(ctx, tx, op.ResourceID); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingPhaseAudit(ctx, tx, s.queries, op, runnerID, "completed"); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err = hostingTaskOperation(ctx, tx, in.HostingAuthority)
	if err != nil {
		return op, err
	}
	if op.Snapshot.LocalBuild != (domain.HostingLocalBuildSource{}) {
		if err = hostingOperationTargetGate(ctx, tx, op, false); err != nil {
			return domain.HostingOperation{}, err
		}
		live, checkErr := sqlcgen.New(tx).HostingLeaseUnexpired(ctx, in.LeaseID)
		if checkErr != nil {
			return domain.HostingOperation{}, checkErr
		}
		if !live {
			return domain.HostingOperation{}, ErrConflict
		}
	}
	if err = finalHostingStorageSettlementAuthority(ctx, tx, runnerID, in.HostingAuthority, op, false); err != nil {
		return domain.HostingOperation{}, err
	}
	return op, tx.Commit(ctx)
}

// CancelHostingOperation cancels execution while preserving admitted uncertainty.
func (s *PostgresStore) CancelHostingOperation(ctx context.Context, id string, audit domain.AuditEvent) (domain.HostingOperation, error) {
	return s.hostingControlMutation(ctx, id, audit, false)
}

// RequestHostingReconciliation deduplicates owner-requested exact-target observation work.
func (s *PostgresStore) RequestHostingReconciliation(ctx context.Context, id string, audit domain.AuditEvent) (domain.HostingOperation, error) {
	return s.hostingControlMutation(ctx, id, audit, true)
}
func (s *PostgresStore) hostingControlMutation(ctx context.Context, id string, audit domain.AuditEvent, reconcile bool) (domain.HostingOperation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	before, err := hostingOperation(ctx, tx, id)
	if err != nil {
		return before, err
	}
	if connectionOperation(before) {
		if reconcile {
			// Specialized recovery requires exact retained stage/candidate CAS.
			return domain.HostingOperation{}, ErrConflict
		}
		if err = authorizeBuildActor(ctx, tx, audit.ActorID, before.ProjectID, domain.RoleOwner); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	if before.StorageArchive != nil {
		if reconcile {
			return domain.HostingOperation{}, ErrConflict
		}
		op, e := archiveCancel(ctx, tx, before, audit.ActorID)
		if e != nil {
			return domain.HostingOperation{}, e
		}
		return op, tx.Commit(ctx)
	}
	if err = hostingLockOperationRuns(ctx, tx, before); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = sqlcgen.New(tx).LockHostingExecutionDestination(ctx, before.Snapshot.DestinationID); err != nil {
		return domain.HostingOperation{}, err
	}
	local := before.Snapshot.LocalBuild != (domain.HostingLocalBuildSource{}) || before.Snapshot.Database != (domain.HostingDatabaseExecutionRef{})
	role := "maintainer"
	if reconcile {
		role = "owner"
	}
	if local {
		if _, err = lockBuildResourceDestination(ctx, tx, before.ResourceID); err != nil {
			return domain.HostingOperation{}, err
		}
		if err = authorizeBuildActor(ctx, tx, audit.ActorID, before.ProjectID, role); err != nil {
			return domain.HostingOperation{}, err
		}
		if reconcile && before.ReconciliationRunID != "" {
			original, readErr := sqlcgen.New(tx).HostingTaskActor(ctx, before.ReconciliationRunID)
			if readErr != nil {
				return domain.HostingOperation{}, readErr
			}
			if original != audit.ActorID {
				return domain.HostingOperation{}, fmt.Errorf("existing observation belongs to another requester; attribution recovery is required: %w", ErrConflict)
			}
		}
	}
	var hold *string
	if err = func() error {
		row, queryErr := sqlcgen.New(tx).LockHostingMutationHold(ctx, before.BindingID)
		if queryErr != nil {
			return queryErr
		}
		hold = row
		return nil
	}(); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = func() error {
		row, queryErr := sqlcgen.New(tx).LockHostingOperation(ctx, id)
		if queryErr != nil {
			return queryErr
		}
		_ = row
		return nil
	}(); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, id)
	if err != nil {
		return op, err
	}
	if op.RunID != before.RunID || op.ReconciliationRunID != before.ReconciliationRunID {
		return domain.HostingOperation{}, ErrConflict
	}
	if reconcile {
		if op.ReconcileRequested && op.ReconciliationRunID != "" {
			if local {
				if err = authorizeBuildActor(ctx, tx, audit.ActorID, op.ProjectID, role); err != nil {
					return domain.HostingOperation{}, err
				}
			}
			return op, nil
		}
		if err = hostingTargetGateMode(ctx, tx, op.ProjectID, op.Snapshot, false, true); err != nil {
			return domain.HostingOperation{}, err
		}
		observationRun := op.ReconciliationRunID
		if observationRun == "" {
			observationRun = "observe_" + id
			spec := domain.RunSpec{Type: hostingSnapshotTaskType(op.Snapshot), Inputs: map[string]any{}, HostingOperationID: id, HostingMode: "observe"}
			raw, marshalErr := json.Marshal(spec)
			if marshalErr != nil {
				return domain.HostingOperation{}, marshalErr
			}
			if _, err = sqlcgen.New(tx).CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: observationRun, ProjectID: op.ProjectID, RunSpec: raw, RequestedBy: audit.ActorID}); err != nil {
				return domain.HostingOperation{}, err
			}
			if _, err = sqlcgen.New(tx).CreateHostingObservationTarget(ctx, sqlcgen.CreateHostingObservationTargetParams{RunID: observationRun, OperationID: id, BindingID: op.BindingID, RunnerID: op.Snapshot.RunnerID}); err != nil {
				return domain.HostingOperation{}, err
			}
		} else {
			if _, err = sqlcgen.New(tx).RequeueHostingObservation(ctx, observationRun); err != nil {
				return domain.HostingOperation{}, err
			}
		}
		if _, err = sqlcgen.New(tx).RequestHostingObservation(ctx, sqlcgen.RequestHostingObservationParams{ID: id, ReconciliationRunID: hostingStringPointer(observationRun)}); err != nil {
			return domain.HostingOperation{}, err
		}
	} else {
		if op.State == "succeeded" || op.State == "failed" || op.State == "canceled" {
			return domain.HostingOperation{}, ErrConflict
		}
		state := "canceled"
		if hold != nil && *hold == id {
			state = "cancel_requested"
		}
		if _, err = sqlcgen.New(tx).CancelHostingOperationState(ctx, sqlcgen.CancelHostingOperationStateParams{ID: id, State: state}); err != nil {
			return domain.HostingOperation{}, err
		}
		if state == "canceled" {
			if _, err = sqlcgen.New(tx).ReleaseHostingFailedClaim(ctx, id); err != nil {
				return domain.HostingOperation{}, err
			}
		}
		for _, runID := range []string{op.RunID, op.ReconciliationRunID} {
			if runID != "" {
				if _, err = sqlcgen.New(tx).CancelHostingLeases(ctx, runID); err != nil {
					return domain.HostingOperation{}, err
				}
				if _, err = sqlcgen.New(tx).CancelHostingTask(ctx, runID); err != nil {
					return domain.HostingOperation{}, err
				}
			}
		}
	}
	audit.TargetID = op.ID
	audit.Metadata = map[string]any{"project_id": op.ProjectID, "resource_id": op.ResourceID, "binding_id": op.BindingID, "runner_id": op.Snapshot.RunnerID}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err = hostingOperation(ctx, tx, id)
	if err != nil {
		return op, err
	}
	if local {
		if err = authorizeBuildActor(ctx, tx, audit.ActorID, op.ProjectID, role); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	if !reconcile {
		if err = releaseUnmutatedStorageContinuity(ctx, tx, op); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	return op, tx.Commit(ctx)
}
func hostingLockOperationRuns(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	for _, id := range []string{op.RunID, op.ReconciliationRunID} {
		if id != "" {
			if err := func() error {
				row, queryErr := sqlcgen.New(tx).LockHostingTask(ctx, id)
				if queryErr != nil {
					return queryErr
				}
				_ = row
				return nil
			}(); err != nil {
				return hostingError(err)
			}
		}
	}
	return nil
}

// HostingReconciliation settles only evidence-backed uncertainty under observation authority.
func (s *PostgresStore) HostingReconciliation(ctx context.Context, runnerID string, in domain.HostingObservation) (domain.HostingOperation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	before, err := hostingOperation(ctx, tx, in.OperationID)
	if err != nil {
		return before, err
	}
	if err = hostingLockOperationRuns(ctx, tx, before); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingRequireMode(ctx, tx, in.HostingAuthority, "observe"); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingAuthorityExists(ctx, tx, runnerID, in.HostingAuthority, false); err != nil {
		return domain.HostingOperation{}, err
	}
	var committedHash string
	err = func() error {
		row, queryErr := sqlcgen.New(tx).GetHostingReconciliationReceipt(ctx, sqlcgen.GetHostingReconciliationReceiptParams{LeaseID: in.LeaseID, OperationID: in.OperationID})
		if queryErr != nil {
			return queryErr
		}
		committedHash = row
		return nil
	}()
	if err == nil {
		if committedHash != domain.HostingDocumentHash(in) {
			return domain.HostingOperation{}, ErrConflict
		}
		if before.DatabaseUpgrade != nil {
			if err = databaseUpgradeHistoricalRead(ctx, tx, before); err != nil {
				return domain.HostingOperation{}, err
			}
		}
		if err = databaseConnectionRunnerRead(ctx, tx, runnerID, before, true); err != nil {
			return domain.HostingOperation{}, err
		}
		return hostingTaskOperation(ctx, tx, in.HostingAuthority)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.HostingOperation{}, err
	}
	if err = hostingAuthorityExists(ctx, tx, runnerID, in.HostingAuthority, true); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingOperationTargetGate(ctx, tx, before, true); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, in.OperationID)
	if err != nil {
		return op, err
	}
	if op.RunID != before.RunID || op.ReconciliationRunID != before.ReconciliationRunID || !op.ReconcileRequested || op.ReconciliationRunID != in.RunID || op.BindingID != in.BindingID {
		return domain.HostingOperation{}, ErrConflict
	}
	if connectionOperation(op) {
		return s.recordDatabaseConnectionObservation(ctx, tx, runnerID, in, op)
	}
	var daemon *string
	var healthyRaw []byte
	var restoreAdmitted bool
	if err = func() error {
		row, queryErr := sqlcgen.New(tx).GetHostingObservationBinding(ctx, op.BindingID)
		if queryErr != nil {
			return queryErr
		}
		daemon = row.DaemonID
		healthyRaw = row.Healthy
		restoreAdmitted = row.RestoreAdmitted
		return nil
	}(); err != nil {
		return domain.HostingOperation{}, err
	}
	// A restored database may predate an admission and a still-running external
	// command. Neither an absent row nor an empty point-in-time observation can
	// establish that all pre-restore processes have stopped. Until trusted local
	// recovery evidence is supported, observation cannot lift this hold.
	if !restoreAdmitted || op.RestoreOwnershipHeld {
		return domain.HostingOperation{}, ErrConflict
	}
	if in.DaemonID == "" || len(in.DaemonID) > 128 || (daemon != nil && *daemon != in.DaemonID) {
		return domain.HostingOperation{}, ErrConflict
	}
	var admission bool
	if err = func() error {
		row, queryErr := sqlcgen.New(tx).HostingAdmissionExists(ctx, op.ID)
		if queryErr != nil {
			return queryErr
		}
		admission = row
		return nil
	}(); err != nil {
		return domain.HostingOperation{}, err
	}
	var expected *domain.HostingHealthy
	if op.MutationHold {
		if op.Result == nil || op.Result.Uncertain || op.Plan == nil || op.Release == nil {
			return domain.HostingOperation{}, ErrConflict
		}
		var liveDeployment bool
		if err = func() error {
			row, queryErr := sqlcgen.New(tx).HostingDeploymentLeaseLive(ctx, op.RunID)
			if queryErr != nil {
				return queryErr
			}
			liveDeployment = row
			return nil
		}(); err != nil {
			return domain.HostingOperation{}, err
		}
		if liveDeployment {
			return domain.HostingOperation{}, ErrConflict
		}
		expected = &domain.HostingHealthy{Snapshot: op.Snapshot, OperationID: op.ID, ContainerID: op.Result.ContainerID, Release: *op.Release, PlanHash: op.Plan.Hash}
		expected.Storage, err = hostingStorageHealthyEvidence(ctx, tx, op)
		if err != nil {
			return domain.HostingOperation{}, err
		}
		if op.Result.Outcome != "succeeded" {
			if !op.Result.Restored || op.Plan.PreviousHealthy == nil {
				return domain.HostingOperation{}, ErrConflict
			}
			expected = op.Plan.PreviousHealthy
		}
	} else {
		// On a database that has not been restored, absence of admission plus
		// absence of a hold proves this operation never acquired permission.
		if admission && op.CompleteReceipt == nil {
			return domain.HostingOperation{}, ErrConflict
		}
		if len(healthyRaw) > 0 {
			if err = json.Unmarshal(healthyRaw, &expected); err != nil {
				return domain.HostingOperation{}, err
			}
		}
	}
	if expected != nil && domain.ValidateHostingHealthyStorage(*expected) != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	if expected == nil {
		if op.MutationHold || !in.NoOwnedContainers || in.ContainerID != "" || in.ImageID != "" {
			return domain.HostingOperation{}, ErrConflict
		}
	} else {
		if in.NoOwnedContainers || !in.Ready || in.ContainerID != expected.ContainerID || in.ImageID != expected.Release.ImageID || in.PlanHash != expected.PlanHash || in.ReleaseHash != expected.Release.Hash {
			return domain.HostingOperation{}, ErrConflict
		}
	}
	if op.MutationHold {
		raw, marshalErr := json.Marshal(expected)
		if marshalErr != nil {
			return domain.HostingOperation{}, marshalErr
		}
		if _, err = sqlcgen.New(tx).SettleHostingObservedHealthy(ctx, sqlcgen.SettleHostingObservedHealthyParams{BindingID: op.BindingID, Healthy: raw, HoldOperationID: hostingStringPointer(op.ID)}); err != nil {
			return domain.HostingOperation{}, err
		}
		if _, err = sqlcgen.New(tx).SetHostingOperationState(ctx, sqlcgen.SetHostingOperationStateParams{ID: op.ID, State: op.Result.Outcome}); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	if _, err = sqlcgen.New(tx).ObserveHostingDaemon(ctx, sqlcgen.ObserveHostingDaemonParams{BindingID: op.BindingID, DaemonID: hostingStringPointer(in.DaemonID)}); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).ClearHostingObservationRequest(ctx, op.ID); err != nil {
		return domain.HostingOperation{}, err
	}
	raw, marshalErr := json.Marshal(in)
	if marshalErr != nil {
		return domain.HostingOperation{}, marshalErr
	}
	if _, err = sqlcgen.New(tx).CreateHostingReconciliationReceipt(ctx, sqlcgen.CreateHostingReconciliationReceiptParams{LeaseID: in.LeaseID, OperationID: op.ID, Body: raw, BodyHash: domain.HostingDocumentHash(in)}); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).CompleteHostingObservationLease(ctx, sqlcgen.CompleteHostingObservationLeaseParams{ID: in.LeaseID, CompletionKey: hostingStringPointer("observed_" + in.LeaseID)}); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).CompleteHostingObservationTask(ctx, in.RunID); err != nil {
		return domain.HostingOperation{}, err
	}
	audit := domain.AuditEvent{ID: "aud_observed_" + in.LeaseID, ActorID: runnerID, Action: "hosting.target_observed", TargetID: op.ID, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"project_id": op.ProjectID, "resource_id": op.ResourceID, "runner_id": runnerID, "destination_id": op.Snapshot.DestinationID, "binding_id": op.BindingID, "task_mode": "observe"}}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err = hostingTaskOperation(ctx, tx, in.HostingAuthority)
	if err != nil {
		return op, err
	}
	if err = finalLocalHostingObservationReportAuthority(ctx, tx, runnerID, in.HostingAuthority, op); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = finalHostingStorageSettlementAuthority(ctx, tx, runnerID, in.HostingAuthority, op, true); err != nil {
		return domain.HostingOperation{}, err
	}
	return op, tx.Commit(ctx)
}

func (s *PostgresStore) hostingClaimAllowed(ctx context.Context, tx pgx.Tx, run domain.TaskRun, runnerID string) (bool, error) {
	if run.RunSpec.Type == domain.RunTypeStorageArchive {
		return s.archiveClaimAllowed(ctx, tx, run, runnerID)
	}
	if run.RunSpec.Type == domain.HostingBuildTaskType {
		return s.buildClaimAllowed(ctx, tx, run, runnerID)
	}
	if run.RunSpec.Type == domain.RunTypeProxyApply {
		if domain.ValidateProxyApplyTask(run) != nil {
			return false, nil
		}
		proxyOp, e := proxyOperation(ctx, tx, run.RunSpec.ProxyOperationID)
		if e != nil {
			return false, e
		}
		if proxyOp.Snapshot.StorageSwitch != nil {
			if e = applicationDatabaseStorageAgent(ctx, tx, runnerID); e != nil {
				return false, nil
			}
			fresh, e := sqlcgen.New(tx).ApplicationDatabaseFinalizeClaimFresh(ctx, run.ID)
			if e != nil {
				return false, e
			}
			if !fresh {
				return false, nil
			}
			original, e := hostingOperation(ctx, tx, proxyOp.Snapshot.StorageSwitch.OperationID)
			if e != nil {
				return false, e
			}
			if e = checkDatabaseConnectionRunnerContext(ctx, tx, runnerID); e != nil {
				return false, nil
			}
			if e = databaseConnectionAuthority(ctx, tx, original, false, false); e != nil {
				return false, nil
			}
		}
		return sqlcgen.New(tx).ProxyClaimEligible(ctx, sqlcgen.ProxyClaimEligibleParams{RunID: run.ID, RunnerID: runnerID})
	}
	if run.RunSpec.Type != domain.RunTypeImageDeploy && run.RunSpec.Type != domain.RunTypeLocalBuildDeploy && run.RunSpec.Type != domain.RunTypeDatabaseDeploy {
		return !domain.ContainsHostingExecution(run.RunSpec, run.Workflow), nil
	}
	if validateHostingDeploymentTask(run) != nil {
		return false, nil
	}
	var operationID, mode string
	err := func() error {
		row, queryErr := sqlcgen.New(tx).GetHostingTargetMode(ctx, sqlcgen.GetHostingTargetModeParams{RunID: run.ID, RunnerID: runnerID})
		if queryErr != nil {
			return queryErr
		}
		operationID = row.OperationID
		mode = row.Mode
		return nil
	}()
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	op, err := hostingOperation(ctx, tx, operationID)
	if err != nil {
		return false, err
	}
	if run.RunSpec.Type != hostingSnapshotTaskType(op.Snapshot) || op.Snapshot.RunnerID != runnerID || run.RunSpec.HostingOperationID != op.ID || run.RunSpec.HostingMode != mode {
		return false, nil
	}
	if connectionOperation(op) {
		if _, err = databaseConnectionContextCredential(ctx, runnerID); err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
				return false, nil
			}
			return false, err
		}
	}
	if op.ApplicationDatabaseStorage != nil {
		if e := applicationDatabaseStorageAgent(ctx, tx, runnerID); e != nil {
			return false, nil
		}
	}
	if mode == "observe" {
		if !op.ReconcileRequested || op.ReconciliationRunID != run.ID {
			return false, nil
		}
	} else if mode == "storage_recover" {
		if !storageRecoveryRun(ctx, tx, op, run.ID) || op.State != "failed" || !op.MutationHold {
			return false, nil
		}
		fresh, e := sqlcgen.New(tx).ApplicationDatabaseFinalizeClaimFresh(ctx, run.ID)
		if e != nil {
			return false, e
		}
		if !fresh {
			return false, nil
		}
	} else if mode == "finalize" {
		if !continuityFinalizeRun(op, run.ID) || op.ApplicationDatabaseYield == nil || !op.MutationHold || op.State != "applying" && op.State != "verifying" {
			return false, nil
		}
		fresh, claimErr := sqlcgen.New(tx).ApplicationDatabaseFinalizeClaimFresh(ctx, run.ID)
		if claimErr != nil {
			return false, claimErr
		}
		if !fresh {
			return false, nil
		}
		if err = continuityProxyApplied(ctx, tx, op); err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
				return false, nil
			}
			return false, err
		}
	} else {
		if op.RunID != run.ID || op.MutationHold || (op.State != "dispatched" && op.State != "prepared" && op.State != "preparing") {
			return false, nil
		}
	}
	if mode == "deploy" {
		err = requireHostingRuntimeAgent(ctx, tx, op.Snapshot)
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	err = hostingOperationTargetGate(ctx, tx, op, mode == "observe")
	if err == nil && connectionOperation(op) {
		err = checkDatabaseConnectionRunnerContext(ctx, tx, runnerID)
		if err == nil && mode == "observe" {
			err = databaseConnectionObservationHold(ctx, tx, op)
		}
	}
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrHostingRestoreHold) {
		return false, nil
	}
	return err == nil, err
}

func rejectGenericHostingRun(ctx context.Context, q *sqlcgen.Queries, runID string) error {
	ownedBuild, err := q.IsHostingBuildTask(ctx, runID)
	if err != nil {
		return err
	}
	if ownedBuild {
		return ErrConflict
	}
	row, err := q.GetRun(ctx, runID)
	if err != nil {
		return hostingError(err)
	}
	run, err := taskRunFromSQLC(row)
	if err != nil {
		return err
	}
	if domain.ContainsHostingExecution(run.RunSpec, run.Workflow) {
		return ErrConflict
	}

	proxy, err := q.ProxyIsTask(ctx, runID)
	if err != nil {
		return err
	}
	if proxy {
		return ErrConflict
	}
	owned, err := q.IsHostingRun(ctx, runID)
	if err != nil {
		return err
	}
	if owned != nil && *owned {
		return ErrConflict
	}
	return nil
}

// hostingTaskOperation projects the exact durable task relationship onto a
// runner response. Task mode is not part of stored operation or receipt bodies;
// historical receipt reads retain their existing authority and expiry rules.
func hostingTaskOperation(ctx context.Context, tx pgx.Tx, a domain.HostingAuthority) (domain.HostingOperation, error) {
	op, err := hostingOperation(ctx, tx, a.OperationID)
	if err != nil {
		return op, err
	}
	mode, err := sqlcgen.New(tx).CheckHostingTaskMode(ctx, sqlcgen.CheckHostingTaskModeParams{RunID: a.RunID, OperationID: a.OperationID})
	if err != nil {
		return domain.HostingOperation{}, hostingError(err)
	}
	op.TaskMode = mode
	return op, nil
}

func hostingRequireMode(ctx context.Context, tx pgx.Tx, a domain.HostingAuthority, expected string) error {
	var mode string
	if err := func() error {
		row, queryErr := sqlcgen.New(tx).CheckHostingTaskMode(ctx, sqlcgen.CheckHostingTaskModeParams{RunID: a.RunID, OperationID: a.OperationID})
		if queryErr != nil {
			return queryErr
		}
		mode = row
		return nil
	}(); err != nil {
		return hostingError(err)
	}
	if mode != expected {
		return ErrConflict
	}
	return nil
}

// HostingStatus returns status for an exact persisted attempt.
func (s *PostgresStore) HostingStatus(ctx context.Context, runnerID string, in domain.HostingStatusRequest) (domain.HostingOperation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = hostingAuthorityExists(ctx, tx, runnerID, in.HostingAuthority, in.Phase != ""); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, in.OperationID)
	if err != nil {
		return op, err
	}
	var mode string
	if err = func() error {
		row, queryErr := sqlcgen.New(tx).GetHostingTaskMode(ctx, in.RunID)
		if queryErr != nil {
			return queryErr
		}
		mode = row
		return nil
	}(); err != nil {
		return domain.HostingOperation{}, err
	}
	op.TaskMode = mode
	if in.Phase == "" {
		observe := mode == "observe"
		borrowObservation := connectionOperation(op) && mode == "deploy" && op.ReconcileRequested
		if borrowObservation {
			if !op.MutationHold || in.RunID != op.RunID {
				return domain.HostingOperation{}, ErrConflict
			}
			if err = databaseConnectionObservationHold(ctx, tx, op); err != nil {
				return domain.HostingOperation{}, err
			}
			observe = true
		}
		if err = databaseConnectionRunnerRead(ctx, tx, runnerID, op, observe); err != nil {
			return domain.HostingOperation{}, err
		}
		if borrowObservation {
			if err = databaseConnectionObservationHold(ctx, tx, op); err != nil {
				return domain.HostingOperation{}, err
			}
		}
		return op, nil
	}
	if in.Phase != "verifying" || mode != "deploy" || !op.MutationHold || (op.State != "applying" && op.State != "verifying") {
		return domain.HostingOperation{}, ErrConflict
	}
	if _, err = sqlcgen.New(tx).BeginHostingVerification(ctx, op.ID); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = finalLocalHostingAuthority(ctx, tx, runnerID, in.HostingAuthority, op, false); err != nil {
		return domain.HostingOperation{}, err
	}
	op.State = "verifying"
	if err = finalDatabaseConnectionAuthority(ctx, tx, runnerID, in.HostingAuthority, op, false, false); err != nil {
		return domain.HostingOperation{}, err
	}
	return op, tx.Commit(ctx)
}

// ClaimHostingObservation lets startup reconciliation obtain only its owner's
// exact read-only task before normal heartbeat/claim traffic. The old full
// fenced tuple authenticates the retained intent; it never regains apply rights.
func (s *PostgresStore) ClaimHostingObservation(ctx context.Context, runnerID string, a domain.HostingAuthority, ttl time.Duration) (domain.HostingOperation, error) {
	if ttl <= 0 || ttl > 10*time.Minute {
		return domain.HostingOperation{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	before, err := hostingOperation(ctx, tx, a.OperationID)
	if err != nil {
		return before, err
	}
	if err = hostingLockOperationRuns(ctx, tx, before); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingRequireMode(ctx, tx, a, "deploy"); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingAuthorityExists(ctx, tx, runnerID, a, false); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingOperationTargetGate(ctx, tx, before, true); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, a.OperationID)
	if err != nil {
		return op, err
	}
	if !op.ReconcileRequested || op.ReconciliationRunID == "" || op.RunID != before.RunID || op.ReconciliationRunID != before.ReconciliationRunID {
		return domain.HostingOperation{}, ErrConflict
	}
	if connectionOperation(op) {
		if err = databaseConnectionObservationHold(ctx, tx, op); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	q := s.queries.WithTx(tx)
	existing, err := q.GetActiveLeaseForRun(ctx, op.ReconciliationRunID)
	if err == nil {
		lease := runLeaseFromSQLC(existing)
		if lease.RunnerID != runnerID {
			return domain.HostingOperation{}, ErrConflict
		}
		op.TaskMode = "observe"
		op.ObservationLease = &lease
		current := domain.HostingAuthority{OperationID: op.ID, RunID: lease.RunID, LeaseID: lease.ID, Attempt: lease.Attempt, Fence: lease.Fence}
		if err = finalLocalHostingAuthority(ctx, tx, runnerID, current, op, true); err != nil {
			return domain.HostingOperation{}, err
		}
		if err = finalDatabaseConnectionAuthority(ctx, tx, runnerID, current, op, true, false); err != nil {
			return domain.HostingOperation{}, err
		}
		return op, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.HostingOperation{}, err
	}
	if connectionOperation(op) {
		queued, readErr := q.GetRun(ctx, op.ReconciliationRunID)
		if readErr != nil || queued.Status != domain.RunQueued || op.ReconciliationRunID != "observe_"+op.ID {
			return domain.HostingOperation{}, ErrConflict
		}
	} else {
		if _, err = sqlcgen.New(tx).ExpireHostingObservationLease(ctx, op.ReconciliationRunID); err != nil {
			return domain.HostingOperation{}, err
		}
		if _, err = sqlcgen.New(tx).ResetHostingObservation(ctx, op.ReconciliationRunID); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	attempt, err := q.ClaimQueuedRun(ctx, sqlcgen.ClaimQueuedRunParams{RunID: op.ReconciliationRunID, RunnerID: &runnerID})
	if err != nil {
		return domain.HostingOperation{}, err
	}
	leaseID, err := newLeaseToken("lease")
	if err != nil {
		return domain.HostingOperation{}, err
	}
	fence, err := newLeaseToken("fence")
	if err != nil {
		return domain.HostingOperation{}, err
	}
	row, err := q.CreateActiveRunLease(ctx, sqlcgen.CreateActiveRunLeaseParams{ID: leaseID, RunID: op.ReconciliationRunID, RunnerID: runnerID, Status: domain.LeaseActive, TtlMicroseconds: ttl.Microseconds(), Attempt: attempt, Fence: fence})
	if err != nil {
		return domain.HostingOperation{}, err
	}
	lease := runLeaseFromSQLC(row)
	op.TaskMode = "observe"
	op.ObservationLease = &lease
	audit := domain.AuditEvent{ID: "aud_observe_claim_" + lease.ID, ActorID: runnerID, Action: "hosting.observation_claimed", TargetID: op.ID, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"project_id": op.ProjectID, "resource_id": op.ResourceID, "binding_id": op.BindingID, "runner_id": runnerID, "destination_id": op.Snapshot.DestinationID, "task_mode": "observe"}}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.HostingOperation{}, err
	}
	current := domain.HostingAuthority{OperationID: op.ID, RunID: lease.RunID, LeaseID: lease.ID, Attempt: lease.Attempt, Fence: lease.Fence}
	if err = finalLocalHostingAuthority(ctx, tx, runnerID, current, op, true); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = finalDatabaseConnectionAuthority(ctx, tx, runnerID, current, op, true, false); err != nil {
		return domain.HostingOperation{}, err
	}
	return op, tx.Commit(ctx)
}

// ListHostingOperations returns bounded resource history in stable newest-first order.
func (s *PostgresStore) ListHostingOperations(ctx context.Context, resourceID string, page Page) (PageResult[domain.HostingOperation], error) {
	var total int
	if err := func() error {
		row, queryErr := sqlcgen.New(s.pool).CountHostingOperations(ctx, resourceID)
		if queryErr != nil {
			return queryErr
		}
		total = int(row)
		return nil
	}(); err != nil {
		return PageResult[domain.HostingOperation]{}, err
	}
	page = hostingPage(page, total)
	ids, err := sqlcgen.New(s.pool).ListHostingOperationIDs(ctx, sqlcgen.ListHostingOperationIDsParams{ResourceID: resourceID, Limit: int32(page.Limit), Offset: int32(page.Offset)})
	if err != nil {
		return PageResult[domain.HostingOperation]{}, err
	}
	result := PageResult[domain.HostingOperation]{Items: make([]domain.HostingOperation, 0, len(ids)), Limit: page.Limit, Offset: page.Offset, Total: total}
	for _, id := range ids {
		op, err := s.GetHostingOperation(ctx, id)
		if err != nil {
			return PageResult[domain.HostingOperation]{}, err
		}
		result.Items = append(result.Items, op)
	}
	return result, nil
}

func hostingStringPointer(value string) *string { return &value }

func (s *PostgresStore) configureHostingPorts() error {
	s.hostingPortMin, s.hostingPortMax = 20000, 60999
	for _, option := range []struct {
		name   string
		target *int
	}{{"NEROCD_HOSTING_AUTO_PORT_MIN", &s.hostingPortMin}, {"NEROCD_HOSTING_AUTO_PORT_MAX", &s.hostingPortMax}} {
		if value := os.Getenv(option.name); value != "" {
			parsed, err := strconv.Atoi(value)
			if err != nil {
				return errors.New("invalid operator hosting port range")
			}
			*option.target = parsed
		}
	}
	if s.hostingPortMin < 1024 || s.hostingPortMax > 65535 || s.hostingPortMin > s.hostingPortMax {
		return errors.New("invalid operator hosting port range")
	}
	return nil
}
func lockHostingResourceDestination(ctx context.Context, tx pgx.Tx, resourceID string) error {
	destination, err := sqlcgen.New(tx).HostingResourceDestination(ctx, resourceID)
	if err != nil {
		return hostingError(err)
	}
	return sqlcgen.New(tx).LockHostingExecutionDestination(ctx, destination)
}
func requireHostingReleaseAgent(ctx context.Context, tx pgx.Tx, runnerID string) error {
	capable, err := sqlcgen.New(tx).HostingRunnerHasReleasePorts(ctx, sqlcgen.HostingRunnerHasReleasePortsParams{RunnerID: runnerID, Capability: domain.HostingReleasePortsCapability})
	if err != nil {
		return hostingError(err)
	}
	if !capable {
		return ErrConflict
	}
	return nil
}
func (s *PostgresStore) resolveHostingReleasePort(ctx context.Context, tx pgx.Tx, snap *domain.HostingSnapshot, requested int) error {
	q := sqlcgen.New(tx)
	if requested == 0 {
		// Destination admission serializes reclamation with requests, external
		// admission, settlement and route references. Evidence rows remain intact.
		if _, err := q.ReclaimUnusedHostingReleaseSlots(ctx, snap.DestinationID); err != nil {
			return err
		}
		port, err := q.PickHostingReleasePort(ctx, sqlcgen.PickHostingReleasePortParams{MinPort: int32(s.hostingPortMin), MaxPort: int32(s.hostingPortMax), DestinationID: snap.DestinationID})
		if err != nil {
			return hostingError(err)
		}
		snap.HostPort = int(port)
		snap.PolicyVersion = hostingRetainedPolicy(*snap)
	}
	previousID := ""
	previousRaw, err := q.GetHostingHealthy(ctx, snap.BindingID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if len(previousRaw) > 0 {
		var old domain.HostingHealthy
		if err = json.Unmarshal(previousRaw, &old); err != nil {
			return err
		}
		previousID = old.OperationID
		if old.Snapshot.HostPort != snap.HostPort {
			snap.PolicyVersion = hostingRetainedPolicy(*snap)
		}
	}
	referenced, err := q.HostingReleasePortReferenced(ctx, sqlcgen.HostingReleasePortReferencedParams{DestinationID: snap.DestinationID, HostPort: int32(snap.HostPort)})
	if err != nil {
		return err
	}
	if referenced && snap.StorageRouteContinuity == (domain.HostingStorageRouteContinuityRef{}) {
		return ErrConflict
	}
	reusable, err := q.HostingReleaseSlotReusable(ctx, sqlcgen.HostingReleaseSlotReusableParams{DestinationID: snap.DestinationID, HostPort: int32(snap.HostPort), PreviousOperationID: previousID, BindingID: snap.BindingID})
	if err != nil {
		return err
	}
	if reusable == nil || !*reusable {
		return ErrConflict
	}
	rows, err := q.GetHostingRetainedReleases(ctx, snap.BindingID)
	if err != nil {
		return err
	}
	if len(rows) > 32 {
		return ErrConflict
	}
	for _, row := range rows {
		if row.OperationID != previousID && row.RetainedRunning {
			snap.PolicyVersion = hostingRetainedPolicy(*snap)
		}
	}
	if snap.PolicyVersion == domain.HostingReleasePolicyVersion {
		if err = requireHostingReleaseAgent(ctx, tx, snap.RunnerID); err != nil {
			return err
		}
	}
	if _, err = q.ReserveHostingReleaseSlot(ctx, sqlcgen.ReserveHostingReleaseSlotParams{DestinationID: snap.DestinationID, HostPort: int32(snap.HostPort), BindingID: snap.BindingID}); err != nil {
		return hostingError(err)
	}
	owner, err := q.LockHostingReleaseSlot(ctx, sqlcgen.LockHostingReleaseSlotParams{DestinationID: snap.DestinationID, HostPort: int32(snap.HostPort)})
	if err != nil {
		return err
	}
	if owner != snap.BindingID {
		return ErrConflict
	}
	return nil
}

// RequestHostingReleaseCleanup selects an exact retained target under current
// project, destination, release-port, and route-reference transaction authority.
func (s *PostgresStore) RequestHostingReleaseCleanup(ctx context.Context, id, resourceID, targetID, actor string, in domain.HostingCleanupInput, audit domain.AuditEvent) (domain.HostingOperation, error) {
	if s.hostingJobs == nil {
		return domain.HostingOperation{}, hostingjobs.ErrUnavailable
	}
	if err := domain.ValidateHostingDeployment(domain.HostingDeploymentInput{ExpectedGeneration: in.ExpectedGeneration, IdempotencyKey: in.IdempotencyKey, ContainerPort: 80, HostPort: 1024, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 1}}); err != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	requestHash := domain.HostingDocumentHash(struct {
		Kind, Target string
		Input        domain.HostingCleanupInput
	}{"cleanup", targetID, in})
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = lockHostingResourceDestination(ctx, tx, resourceID); err != nil {
		return domain.HostingOperation{}, err
	}
	target, err := sqlcgen.New(tx).LockHostingResourceTarget(ctx, resourceID)
	if err != nil {
		return domain.HostingOperation{}, hostingError(err)
	}
	if target.Kind == "database" {
		return domain.HostingOperation{}, ErrConflict
	}
	var resourceSource domain.ResourceSource
	if err = json.Unmarshal(target.Source, &resourceSource); err != nil {
		return domain.HostingOperation{}, err
	}
	if resourceSource.Kind == "git" {
		if err = authorizeBuildActor(ctx, tx, actor, target.ProjectID, "maintainer"); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	if row, lookupErr := sqlcgen.New(tx).GetHostingIdempotency(ctx, sqlcgen.GetHostingIdempotencyParams{ResourceID: resourceID, IdempotencyKey: in.IdempotencyKey}); lookupErr == nil {
		if row.RequestHash != requestHash {
			return domain.HostingOperation{}, ErrConflict
		}
		existing, readErr := hostingOperation(ctx, tx, row.ID)
		if readErr != nil {
			return existing, readErr
		}
		if resourceSource.Kind == "git" {
			if err = hostingOperationTargetGate(ctx, tx, existing, false); err != nil {
				return domain.HostingOperation{}, err
			}
			if err = authorizeBuildActor(ctx, tx, actor, target.ProjectID, "maintainer"); err != nil {
				return domain.HostingOperation{}, err
			}
		}
		return existing, nil
	} else if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return domain.HostingOperation{}, lookupErr
	}
	if target.Generation != in.ExpectedGeneration {
		return domain.HostingOperation{}, ErrConflict
	}
	row, err := sqlcgen.New(tx).GetHostingRetainedRelease(ctx, targetID)
	if err != nil {
		return domain.HostingOperation{}, hostingError(err)
	}
	var retained domain.HostingHealthy
	if err = json.Unmarshal(row.Body, &retained); err != nil {
		return domain.HostingOperation{}, err
	}
	if row.State != "retained" || retained.Snapshot.ResourceID != resourceID || retained.Snapshot.BindingID != target.BindingID || retained.Snapshot.DestinationID != target.DestinationID {
		return domain.HostingOperation{}, ErrConflict
	}
	raw, err := sqlcgen.New(tx).GetHostingHealthy(ctx, target.BindingID)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	var current domain.HostingHealthy
	if err = json.Unmarshal(raw, &current); err != nil {
		return domain.HostingOperation{}, err
	}
	plan, err := domain.CompileHostingCleanupPlan(retained, current.OperationID)
	if err != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	snapshot := retained.Snapshot
	snapshot.PolicyVersion = hostingRetainedPolicy(snapshot)
	if err = hostingTargetGate(ctx, tx, target.ProjectID, snapshot, false); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingCleanupUnreferenced(ctx, tx, plan); err != nil {
		return domain.HostingOperation{}, err
	}
	state, err := sqlcgen.New(tx).LockHostingPortReservation(ctx, target.BindingID)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if state.HoldOperationID != nil || !state.RestoreAdmitted {
		return domain.HostingOperation{}, ErrConflict
	}
	busy, err := sqlcgen.New(tx).HostingBindingBusy(ctx, target.BindingID)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if busy {
		return domain.HostingOperation{}, ErrConflict
	}
	if _, err = sqlcgen.New(tx).CreateHostingOperation(ctx, sqlcgen.CreateHostingOperationParams{ID: id, ProjectID: target.ProjectID, ResourceID: resourceID, BindingID: target.BindingID, IdempotencyKey: in.IdempotencyKey, RequestHash: requestHash, CreatedBy: actor}); err != nil {
		return domain.HostingOperation{}, hostingError(err)
	}
	snapshotRaw, err := json.Marshal(snapshot)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).CreateHostingSnapshot(ctx, sqlcgen.CreateHostingSnapshotParams{OperationID: id, Body: snapshotRaw, BodyHash: domain.HostingDocumentHash(snapshot)}); err != nil {
		return domain.HostingOperation{}, err
	}
	if snapshot.LocalBuild != (domain.HostingLocalBuildSource{}) {
		if err = authorizeBuildActor(ctx, tx, actor, target.ProjectID, "maintainer"); err != nil {
			return domain.HostingOperation{}, err
		}
		if _, err = sqlcgen.New(tx).CreateHostingOperationBuildArtifact(ctx, sqlcgen.CreateHostingOperationBuildArtifactParams{OperationID: id, BuildID: snapshot.LocalBuild.Artifact.BuildID, ArtifactHash: snapshot.LocalBuild.ArtifactHash}); err != nil {
			return domain.HostingOperation{}, hostingError(err)
		}
	}
	planRaw, err := json.Marshal(plan)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).CreateHostingCleanupTarget(ctx, sqlcgen.CreateHostingCleanupTargetParams{OperationID: id, TargetOperationID: targetID, Plan: planRaw, PlanHash: plan.Hash}); err != nil {
		return domain.HostingOperation{}, err
	}
	audit.ActorID, audit.Action, audit.TargetID = actor, "hosting.release_cleanup_requested", id
	audit.Metadata = map[string]any{"resource_id": resourceID, "release_operation_id": targetID, "destination_id": target.DestinationID}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = s.hostingJobs.EnqueueTx(ctx, tx, hostingjobs.PrepareDeploymentArgs{OperationID: id}); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, id)
	if err != nil {
		return op, err
	}
	if snapshot.LocalBuild != (domain.HostingLocalBuildSource{}) {
		if err = authorizeBuildActor(ctx, tx, actor, target.ProjectID, "maintainer"); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	return op, tx.Commit(ctx)
}

// ListHostingRetainedReleases returns only the selected resource's release ledger.
func (s *PostgresStore) ListHostingRetainedReleases(ctx context.Context, resourceID string) ([]domain.HostingRetainedRelease, error) {
	binding, err := s.GetResourceBinding(ctx, resourceID)
	if err != nil {
		return nil, err
	}
	rows, err := sqlcgen.New(s.pool).GetHostingRetainedReleases(ctx, binding.ID)
	if err != nil {
		return nil, err
	}
	result := make([]domain.HostingRetainedRelease, 0, len(rows))
	for _, row := range rows {
		if row.State == "healthy" {
			continue
		}
		var target domain.HostingHealthy
		if err = json.Unmarshal(row.Body, &target); err != nil {
			return nil, err
		}
		detail, err := sqlcgen.New(s.pool).GetHostingRetainedRelease(ctx, row.OperationID)
		if err != nil {
			return nil, err
		}
		result = append(result, domain.HostingRetainedRelease{Healthy: target, State: row.State, RetainedRunning: row.RetainedRunning, CleanupOperationID: detail.CleanupOperationID})
	}
	return result, nil
}
func hostingCleanupUnreferenced(ctx context.Context, tx pgx.Tx, plan domain.HostingCleanupPlan) error {
	row, err := sqlcgen.New(tx).GetHostingRetainedRelease(ctx, plan.Target.OperationID)
	if err != nil {
		return hostingError(err)
	}
	if row.State != "retained" && row.State != "cleanup_pending" {
		return ErrConflict
	}
	var target domain.HostingHealthy
	if err = json.Unmarshal(row.Body, &target); err != nil {
		return err
	}
	if !reflect.DeepEqual(target, plan.Target) {
		return ErrConflict
	}
	if err = archiveImageReferenced(ctx, tx, target); err != nil {
		return err
	}
	referenced, err := sqlcgen.New(tx).HostingReleaseReferenced(ctx, target.OperationID)
	if err != nil {
		return err
	}
	if referenced {
		return ErrConflict
	}
	raw, err := sqlcgen.New(tx).GetHostingHealthy(ctx, target.Snapshot.BindingID)
	if err != nil {
		return err
	}
	var healthy domain.HostingHealthy
	if err = json.Unmarshal(raw, &healthy); err != nil {
		return err
	}
	if healthy.OperationID == target.OperationID || healthy.OperationID != plan.CurrentHealthyOperationID {
		return ErrConflict
	}
	return nil
}
func (s *PostgresStore) admitHostingCleanup(ctx context.Context, tx pgx.Tx, runnerID string, a domain.HostingAuthority, op domain.HostingOperation) (domain.HostingOperation, error) {
	if err := hostingRequireMode(ctx, tx, a, "cleanup"); err != nil {
		return domain.HostingOperation{}, err
	}
	if op.MutationHold && op.State == "applying" {
		return op, nil
	}
	if op.State != "prepared" || op.MutationHold {
		return domain.HostingOperation{}, ErrConflict
	}
	if err := hostingCleanupUnreferenced(ctx, tx, *op.CleanupPlan); err != nil {
		return domain.HostingOperation{}, err
	}
	rows, err := sqlcgen.New(tx).AdmitHostingMutation(ctx, sqlcgen.AdmitHostingMutationParams{BindingID: op.BindingID, HoldOperationID: hostingStringPointer(op.ID), DaemonID: hostingStringPointer(op.CleanupPlan.Target.Release.DaemonID)})
	if err != nil || rows != 1 {
		return domain.HostingOperation{}, ErrConflict
	}
	authority, err := json.Marshal(a)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).CreateHostingApplyAdmission(ctx, sqlcgen.CreateHostingApplyAdmissionParams{OperationID: op.ID, RunID: a.RunID, LeaseID: a.LeaseID, Authority: authority, PlanHash: op.CleanupPlan.Hash}); err != nil {
		return domain.HostingOperation{}, err
	}
	if changed, err := sqlcgen.New(tx).MarkHostingCleanupPending(ctx, sqlcgen.MarkHostingCleanupPendingParams{OperationID: op.CleanupPlan.Target.OperationID, CleanupOperationID: hostingStringPointer(op.ID)}); err != nil || changed != 1 {
		return domain.HostingOperation{}, ErrConflict
	}
	if _, err = sqlcgen.New(tx).BeginHostingApply(ctx, op.ID); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingPhaseAudit(ctx, tx, s.queries, op, runnerID, "cleanup_admitted"); err != nil {
		return domain.HostingOperation{}, err
	}
	updated, err := hostingTaskOperation(ctx, tx, a)
	if err != nil {
		return updated, err
	}
	if err = finalLocalHostingAuthority(ctx, tx, runnerID, a, op, false); err != nil {
		return domain.HostingOperation{}, err
	}
	return updated, tx.Commit(ctx)
}
func (s *PostgresStore) completeHostingCleanup(ctx context.Context, tx pgx.Tx, runnerID string, in domain.HostingCompleteRequest, op domain.HostingOperation) (domain.HostingOperation, error) {
	if err := hostingRequireMode(ctx, tx, in.HostingAuthority, "cleanup"); err != nil {
		return domain.HostingOperation{}, err
	}
	if in.Result.PlanHash != op.CleanupPlan.Hash || in.Result.DaemonID != op.CleanupPlan.Target.Release.DaemonID || in.Result.ImageID != op.CleanupPlan.Target.Release.ImageID || in.Result.ContainerID != op.CleanupPlan.Target.ContainerID {
		return domain.HostingOperation{}, ErrConflict
	}
	if !op.MutationHold || op.State != "applying" {
		return domain.HostingOperation{}, ErrConflict
	}
	if err := insertHostingReceipt(ctx, tx, in.HostingAuthority, "complete", in.ReceiptID, in.Result); err != nil {
		return domain.HostingOperation{}, err
	}
	uncertain := in.Result.Uncertain || in.Result.Outcome != "succeeded"
	state := in.Result.Outcome
	if uncertain {
		state = "reconciliation_required"
	}
	q := sqlcgen.New(tx)
	if _, err := q.SetHostingOperationState(ctx, sqlcgen.SetHostingOperationStateParams{ID: op.ID, State: state}); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err := q.CompleteHostingLease(ctx, sqlcgen.CompleteHostingLeaseParams{ID: in.LeaseID, Status: in.Result.Outcome, CompletionKey: hostingStringPointer(in.ReceiptID)}); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err := q.CompleteHostingTask(ctx, sqlcgen.CompleteHostingTaskParams{ID: op.RunID, Status: in.Result.Outcome}); err != nil {
		return domain.HostingOperation{}, err
	}
	if !uncertain {
		if err := archiveImageReferenced(ctx, tx, op.CleanupPlan.Target); err != nil {
			return domain.HostingOperation{}, err
		}
		if rows, err := q.MarkHostingCleanupRemoved(ctx, sqlcgen.MarkHostingCleanupRemovedParams{OperationID: op.CleanupPlan.Target.OperationID, CleanupOperationID: hostingStringPointer(op.ID)}); err != nil || rows != 1 {
			return domain.HostingOperation{}, ErrConflict
		}
		if _, err := q.ReleaseHostingCleanupClaim(ctx, op.CleanupPlan.Target.OperationID); err != nil {
			return domain.HostingOperation{}, err
		}
		if _, err := q.ReleaseUnusedHostingSlot(ctx, sqlcgen.ReleaseUnusedHostingSlotParams{DestinationID: op.Snapshot.DestinationID, HostPort: int32(op.Snapshot.HostPort)}); err != nil {
			return domain.HostingOperation{}, err
		}
		if _, err := q.ClearHostingMutationHold(ctx, sqlcgen.ClearHostingMutationHoldParams{BindingID: op.BindingID, HoldOperationID: hostingStringPointer(op.ID)}); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	if err := hostingPhaseAudit(ctx, tx, s.queries, op, runnerID, "cleanup_completed"); err != nil {
		return domain.HostingOperation{}, err
	}
	result, err := hostingTaskOperation(ctx, tx, in.HostingAuthority)
	if err != nil {
		return result, err
	}
	if op.Snapshot.LocalBuild != (domain.HostingLocalBuildSource{}) {
		if err = hostingOperationTargetGate(ctx, tx, op, false); err != nil {
			return domain.HostingOperation{}, err
		}
		live, checkErr := sqlcgen.New(tx).HostingLeaseUnexpired(ctx, in.LeaseID)
		if checkErr != nil {
			return domain.HostingOperation{}, checkErr
		}
		if !live {
			return domain.HostingOperation{}, ErrConflict
		}
	}
	return result, tx.Commit(ctx)
}

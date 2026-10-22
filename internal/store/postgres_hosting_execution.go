package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	client, err := hostingjobs.New(ctx, s.pool, s, hostingjobs.Config{})
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
		if _, err = s.hostingJobs.EnqueueTx(ctx, tx, hostingjobs.PrepareDeploymentArgs{OperationID: id}); err != nil {
			return err
		}
		if _, err = sqlcgen.New(tx).TouchHostingOperation(ctx, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ErrHostingRestoreHold blocks restored targets until trusted local recovery proof exists.
var ErrHostingRestoreHold = fmt.Errorf("%w: restored hosting ownership requires trusted local journal and process recovery proof", ErrConflict)

func hostingOperation(ctx context.Context, q sqlcgen.DBTX, id string) (domain.HostingOperation, error) {
	var op domain.HostingOperation
	var snapshot, release, plan, prepared, complete, result []byte
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
	}{{snapshot, &op.Snapshot}, {release, &op.Release}, {plan, &op.Plan}, {prepared, &op.PreparedReceipt}, {complete, &op.CompleteReceipt}, {result, &op.Result}} {
		if len(doc.raw) > 0 {
			if err = json.Unmarshal(doc.raw, doc.target); err != nil {
				return domain.HostingOperation{}, fmt.Errorf("decode hosting evidence: %w", err)
			}
		}
	}
	return op, nil
}

// GetHostingOperation loads immutable evidence without exposing lease authority.
func (s *PostgresStore) GetHostingOperation(ctx context.Context, id string) (domain.HostingOperation, error) {
	return hostingOperation(ctx, s.pool, id)
}

// RequestHostingDeployment captures desired state and inserts the River job in
// one transaction. Reusing a key with changed input is a conflict.
func (s *PostgresStore) RequestHostingDeployment(ctx context.Context, id, resourceID, actorID string, in domain.HostingDeploymentInput, audit domain.AuditEvent) (domain.HostingOperation, error) {
	if s.hostingJobs == nil {
		return domain.HostingOperation{}, hostingjobs.ErrUnavailable
	}
	if err := domain.ValidateHostingDeployment(in); err != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	var snap domain.HostingSnapshot
	var projectID string
	var source []byte
	err = func() error {
		row, queryErr := sqlcgen.New(tx).LockHostingResourceTarget(ctx, resourceID)
		if queryErr != nil {
			return queryErr
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
		return hostingOperation(ctx, tx, existingID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.HostingOperation{}, err
	}
	if snap.ResourceGeneration != in.ExpectedGeneration {
		return domain.HostingOperation{}, ErrConflict
	}
	var desired domain.ResourceSource
	if err = json.Unmarshal(source, &desired); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = domain.NormalizeHostingImage(desired.Image.Reference); err != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	snap.ResourceID = resourceID
	snap.ImageReference = desired.Image.Reference
	snap.ContainerPort = in.ContainerPort
	snap.HostPort = in.HostPort
	snap.Readiness = in.Readiness
	snap.PolicyVersion = domain.HostingPolicyVersion
	if err = hostingTargetGate(ctx, tx, projectID, snap, true); err != nil {
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
	if reserved != snap.HostPort || !admitted || hold != nil {
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
	contents, err := json.Marshal(snap)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).CreateHostingSnapshot(ctx, sqlcgen.CreateHostingSnapshotParams{OperationID: id, Body: contents, BodyHash: domain.HostingDocumentHash(snap)}); err != nil {
		return domain.HostingOperation{}, err
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
		row, queryErr := sqlcgen.New(tx).LockHostingExactRunner(ctx, sqlcgen.LockHostingExactRunnerParams{ID: snap.BindingID, ResourceID: snap.ResourceID, ID_2: snap.DestinationID, ID_3: snap.ServerID, ID_4: snap.RunnerID, ProjectID: projectID})
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
	if err = hostingTargetGate(ctx, tx, op.ProjectID, op.Snapshot, false); err != nil {
		if errors.Is(err, ErrHostingRestoreHold) {
			return nil
		}
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			if _, updateErr := sqlcgen.New(tx).FailQueuedHostingOperation(ctx, id); updateErr != nil {
				return updateErr
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
	runID := "run_" + id
	spec := domain.RunSpec{Type: domain.RunTypeImageDeploy, Inputs: map[string]any{}, HostingOperationID: id, HostingMode: "deploy"}
	body, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	_, err = sqlcgen.New(tx).CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: runID, ProjectID: op.ProjectID, RunSpec: body, RequestedBy: op.CreatedBy})
	if err != nil {
		return err
	}
	if _, err = sqlcgen.New(tx).CreateHostingDeploymentTarget(ctx, sqlcgen.CreateHostingDeploymentTargetParams{RunID: runID, OperationID: id, BindingID: op.BindingID, RunnerID: op.Snapshot.RunnerID}); err != nil {
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
	return tx.Commit(ctx)
}

func hostingAuthorityExists(ctx context.Context, tx pgx.Tx, runnerID string, a domain.HostingAuthority, live bool) error {
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
		if err = hostingTargetGateMode(ctx, tx, op.ProjectID, op.Snapshot, false, true); err != nil {
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
		return op, nil
	}
	if op.State != "dispatched" && op.State != "preparing" && op.State != "prepared" {
		return domain.HostingOperation{}, ErrConflict
	}
	if err = hostingTargetGate(ctx, tx, op.ProjectID, op.Snapshot, false); err != nil {
		return domain.HostingOperation{}, err
	}
	if op.MutationHold {
		return domain.HostingOperation{}, ErrConflict
	}
	if _, err = sqlcgen.New(tx).BeginHostingPreparation(ctx, op.ID); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err = hostingOperation(ctx, tx, op.ID)
	if err != nil {
		return op, err
	}
	op.TaskMode = mode
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
		return hostingOperation(ctx, tx, in.OperationID)
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
	if err = hostingTargetGate(ctx, tx, op.ProjectID, op.Snapshot, false); err != nil {
		return domain.HostingOperation{}, err
	}
	if op.Release != nil {
		if *op.Release != in.Release {
			return domain.HostingOperation{}, ErrConflict
		}
		return op, nil
	}
	// Supplied digest must match the repository and digest recorded by Docker.
	original, err := domain.NormalizeHostingImage(op.Snapshot.ImageReference)
	if err != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	if !hostingReleaseMatchesSource(original, in.Release.CanonicalImage) {
		return domain.HostingOperation{}, ErrConflict
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
	plan, err := domain.CompileHostingPlan(op.Snapshot, in.Release, previous)
	if err != nil {
		return domain.HostingOperation{}, ErrConflict
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
	if err = hostingRequireMode(ctx, tx, a, "deploy"); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingAuthorityExists(ctx, tx, runnerID, a, true); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, a.OperationID)
	if err != nil {
		return op, err
	}
	if err = hostingTargetGate(ctx, tx, op.ProjectID, op.Snapshot, false); err != nil {
		return domain.HostingOperation{}, err
	}
	if op.Plan == nil || op.Release == nil || op.Plan.CompilerVersion != domain.HostingCompilerVersion || op.Snapshot.PolicyVersion != domain.HostingPolicyVersion {
		return domain.HostingOperation{}, ErrConflict
	}
	// Repeated admission under the same live tuple is safe; runtime never repeats
	// side effects merely because this acknowledgement was lost.
	if op.MutationHold && op.State == "applying" {
		return op, nil
	}
	if op.State != "prepared" || op.MutationHold {
		return domain.HostingOperation{}, ErrConflict
	}
	tag, err := sqlcgen.New(tx).AdmitHostingMutation(ctx, sqlcgen.AdmitHostingMutationParams{BindingID: op.BindingID, HoldOperationID: hostingStringPointer(op.ID), DaemonID: hostingStringPointer(op.Release.DaemonID)})
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if tag != 1 {
		return domain.HostingOperation{}, ErrConflict
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
	op, err = hostingOperation(ctx, tx, op.ID)
	if err != nil {
		return op, err
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
	if err = hostingRequireMode(ctx, tx, in.HostingAuthority, "deploy"); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingAuthorityExists(ctx, tx, runnerID, in.HostingAuthority, false); err != nil {
		return domain.HostingOperation{}, err
	}
	replay, err := replayHostingReceipt(ctx, tx, in.HostingAuthority, "complete", in.ReceiptID, domain.HostingDocumentHash(in.Result))
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if replay {
		return hostingOperation(ctx, tx, in.OperationID)
	}
	op, err := hostingOperation(ctx, tx, in.OperationID)
	if err != nil {
		return op, err
	}
	if op.State == "succeeded" || op.State == "failed" || op.State == "canceled" {
		return domain.HostingOperation{}, ErrConflict
	}
	var live bool
	if err = func() error {
		row, queryErr := sqlcgen.New(tx).HostingLeaseLive(ctx, in.LeaseID)
		if queryErr != nil {
			return queryErr
		}
		if row == nil {
			return ErrConflict
		}
		live = *row
		return nil
	}(); err != nil {
		return domain.HostingOperation{}, err
	}
	// Pre-apply failure may finish only under live authority and cannot assert
	// external success. Expired post-admission evidence is retained for owner
	// reconciliation, never treated as renewed permission to mutate a workload.
	if !op.MutationHold {
		if !live || in.Result.Outcome != "failed" || in.Result.Uncertain || op.State == "cancel_requested" {
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
	uncertain := op.MutationHold && (!live || in.Result.Uncertain || (in.Result.Outcome == "failed" && !in.Result.Restored))
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
	if err = hostingPhaseAudit(ctx, tx, s.queries, op, runnerID, "completed"); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err = hostingOperation(ctx, tx, op.ID)
	if err != nil {
		return op, err
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
	if err = hostingLockOperationRuns(ctx, tx, before); err != nil {
		return domain.HostingOperation{}, err
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
			return op, nil
		}
		if err = hostingTargetGateMode(ctx, tx, op.ProjectID, op.Snapshot, false, true); err != nil {
			return domain.HostingOperation{}, err
		}
		observationRun := op.ReconciliationRunID
		if observationRun == "" {
			observationRun = "observe_" + id
			spec := domain.RunSpec{Type: domain.RunTypeImageDeploy, Inputs: map[string]any{}, HostingOperationID: id, HostingMode: "observe"}
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
		return hostingOperation(ctx, tx, in.OperationID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.HostingOperation{}, err
	}
	if err = hostingAuthorityExists(ctx, tx, runnerID, in.HostingAuthority, true); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingTargetGateMode(ctx, tx, before.ProjectID, before.Snapshot, false, true); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, in.OperationID)
	if err != nil {
		return op, err
	}
	if op.RunID != before.RunID || op.ReconciliationRunID != before.ReconciliationRunID || !op.ReconcileRequested || op.ReconciliationRunID != in.RunID || op.BindingID != in.BindingID {
		return domain.HostingOperation{}, ErrConflict
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
	op, err = hostingOperation(ctx, tx, op.ID)
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

func (s *PostgresStore) hostingClaimAllowed(ctx context.Context, tx pgx.Tx, run domain.TaskRun, runnerID string) (bool, error) {
	if run.RunSpec.Type != domain.RunTypeImageDeploy {
		return true, nil
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
	if op.Snapshot.RunnerID != runnerID || run.RunSpec.HostingOperationID != op.ID || run.RunSpec.HostingMode != mode {
		return false, nil
	}
	if mode == "observe" {
		if !op.ReconcileRequested || op.ReconciliationRunID != run.ID {
			return false, nil
		}
	} else {
		if op.RunID != run.ID || op.MutationHold || (op.State != "dispatched" && op.State != "prepared" && op.State != "preparing") {
			return false, nil
		}
	}
	err = hostingTargetGateMode(ctx, tx, op.ProjectID, op.Snapshot, false, mode == "observe")
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrHostingRestoreHold) {
		return false, nil
	}
	return err == nil, err
}

func rejectGenericHostingRun(ctx context.Context, q *sqlcgen.Queries, runID string) error {
	owned, err := q.IsHostingRun(ctx, runID)
	if err != nil {
		return err
	}
	if owned != nil && *owned {
		return ErrConflict
	}
	return nil
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
		return op, nil
	}
	if in.Phase != "verifying" || mode != "deploy" || !op.MutationHold || (op.State != "applying" && op.State != "verifying") {
		return domain.HostingOperation{}, ErrConflict
	}
	if _, err = sqlcgen.New(tx).BeginHostingVerification(ctx, op.ID); err != nil {
		return domain.HostingOperation{}, err
	}
	op.State = "verifying"
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
	if err = hostingTargetGateMode(ctx, tx, before.ProjectID, before.Snapshot, false, true); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, a.OperationID)
	if err != nil {
		return op, err
	}
	if !op.ReconcileRequested || op.ReconciliationRunID == "" || op.RunID != before.RunID || op.ReconciliationRunID != before.ReconciliationRunID {
		return domain.HostingOperation{}, ErrConflict
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
		return op, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).ExpireHostingObservationLease(ctx, op.ReconciliationRunID); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).ResetHostingObservation(ctx, op.ReconciliationRunID); err != nil {
		return domain.HostingOperation{}, err
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
		op, err := hostingOperation(ctx, s.pool, id)
		if err != nil {
			return PageResult[domain.HostingOperation]{}, err
		}
		result.Items = append(result.Items, op)
	}
	return result, nil
}

func hostingStringPointer(value string) *string { return &value }

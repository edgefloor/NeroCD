package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

// PrepareStorageArchive is the real DB-only River dispatcher, including when
// build execution is disabled. It never performs Docker or artifact I/O.
func (s *PostgresStore) PrepareStorageArchive(ctx context.Context, id string) error {
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		op, found, err := archiveOperation(ctx, tx, id)
		if err != nil {
			return false, err
		}
		if !found {
			return false, ErrNotFound
		}
		if op.State != "queued" {
			return true, nil
		}
		if err = s.archiveGate(ctx, tx, op); err != nil {
			return false, err
		}
		q := sqlcgen.New(tx)
		state, err := q.LockHostingOperationState(ctx, id)
		if err != nil {
			return false, err
		}
		if state != "queued" {
			return true, nil
		}
		prefix := map[string]string{"backup": "backup_", "restore": "restore_", "recover": "archive_recover_"}[op.StorageArchive.Mode]
		run := domain.TaskRun{ID: prefix + id, ProjectID: op.ProjectID, RunSpec: domain.RunSpec{Type: domain.RunTypeStorageArchive, Inputs: map[string]any{}, HostingOperationID: id, HostingMode: op.StorageArchive.Mode}}
		if domain.ValidateStorageArchiveTask(run) != nil {
			return false, ErrConflict
		}
		raw, err := json.Marshal(run.RunSpec)
		if err != nil {
			return false, err
		}
		if _, err = q.CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: run.ID, ProjectID: op.ProjectID, RunSpec: raw, RequestedBy: op.CreatedBy}); err != nil {
			return false, err
		}
		if err = q.CreateStorageArchiveTaskTarget(ctx, sqlcgen.CreateStorageArchiveTaskTargetParams{RunID: run.ID, OperationID: id, RunnerID: op.StorageArchive.Plan.RunnerID, Mode: op.StorageArchive.Mode}); err != nil {
			return false, err
		}
		if _, err = q.DispatchHostingOperation(ctx, sqlcgen.DispatchHostingOperationParams{ID: id, RunID: &run.ID}); err != nil {
			return false, err
		}
		if err = archiveAudit(ctx, tx, op.CreatedBy, "dispatched", op); err != nil {
			return false, err
		}
		return true, nil
	})
	return err
}

func (s *PostgresStore) archiveClaimAllowed(ctx context.Context, tx pgx.Tx, run domain.TaskRun, runner string) (bool, error) {
	if domain.ValidateStorageArchiveTask(run) != nil {
		return false, nil
	}
	q := sqlcgen.New(tx)
	target, err := q.ReadStorageArchiveTaskTarget(ctx, run.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if target.RunnerID != runner || target.OperationID != run.RunSpec.HostingOperationID || target.Mode != run.RunSpec.HostingMode {
		return false, nil
	}
	// Keep the existing cursor -> task -> destination/resource -> lease order.
	if _, err = q.LockHostingTaskForAuthority(ctx, run.ID); err != nil {
		return false, err
	}
	current, err := q.GetRun(ctx, run.ID)
	if err != nil {
		return false, err
	}
	if current.Status != domain.RunQueued {
		return false, nil
	}
	op, found, err := archiveOperation(ctx, tx, target.OperationID)
	if err != nil {
		return false, err
	}
	if !found || op.RunID != run.ID || op.State != "dispatched" && op.State != "prepared" && op.State != "preparing" {
		return false, nil
	}
	err = s.archiveGate(ctx, tx, op)
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrStorageArchiveUnavailable) {
		return false, nil
	}
	return err == nil, err
}

func (s *PostgresStore) recoverArchiveDispatch(ctx context.Context, tx pgx.Tx) error {
	if s.storageArchiveConfiguration == nil {
		return nil
	}
	q := sqlcgen.New(tx)
	if err := storageArchiveSchema(ctx, q); err != nil {
		return err
	}
	ids, err := q.RecoverQueuedStorageArchives(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = s.hostingJobs.EnqueueStorageArchiveTx(ctx, tx, hostingjobs.PrepareStorageArchiveArgs{OperationID: id}); err != nil {
			return err
		}
		if _, err = q.TouchHostingOperation(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// RequestStorageArchiveRecovery grants fresh verification/resumption authority,
// never replay of helper creation, extraction, publication or source deletion.
func (s *PostgresStore) RequestStorageArchiveRecovery(ctx context.Context, id, originalID, actor string, in domain.HostingStorageArchiveRecoveryInput, event domain.AuditEvent) (domain.HostingOperation, error) {
	if in.Validate() != nil {
		return domain.HostingOperation{}, domain.ErrInvalidStorageArchive
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingOperation, error) {
		var zero domain.HostingOperation
		q := sqlcgen.New(tx)
		original, found, err := archiveOperation(ctx, tx, originalID)
		if err != nil {
			return zero, err
		}
		if !found || original.StorageArchive.Mode == "recover" {
			return zero, ErrConflict
		}
		// Read the hold owner before taking task locks. Recheck under the
		// destination/resource/binding locks before granting any authority.
		var heldID string
		if err = tx.QueryRow(ctx, `SELECT coalesce(hold_operation_id,'') FROM hosting_binding_state WHERE binding_id=$1`, original.BindingID).Scan(&heldID); err != nil {
			return zero, err
		}
		held := original
		if heldID != "" && heldID != originalID {
			held, found, err = archiveOperation(ctx, tx, heldID)
			if err != nil {
				return zero, err
			}
			if !found || held.StorageArchive.Mode != "recover" || held.StorageArchive.Plan.RecoveryOfOperationID != originalID || held.StorageArchive.Plan.RecoveryEvidence.OriginalPlanHash != original.StorageArchive.Plan.Hash || held.BindingID != original.BindingID {
				return zero, ErrConflict
			}
		}
		runs := []string{original.RunID, held.RunID}
		slices.Sort(runs)
		for _, run := range slices.Compact(runs) {
			if run != "" {
				if _, err = q.LockHostingTask(ctx, run); err != nil {
					return zero, err
				}
			}
		}
		p := original.StorageArchive.Plan
		resources := []string{p.SourceStorage.ResourceID}
		if p.Target != nil {
			resources = append(resources, p.Target.Resource.ID)
		}
		if err = archivePrelock(ctx, tx, p.DestinationID, resources...); err != nil {
			return zero, err
		}
		if _, _, err = hostingStoragePlacement(ctx, tx, actor, p.SourceStorage.ResourceID, domain.RoleOwner); err != nil {
			return zero, err
		}
		if p.Target != nil {
			if _, _, err = hostingStoragePlacement(ctx, tx, actor, p.Target.Resource.ID, domain.RoleOwner); err != nil {
				return zero, err
			}
		}
		if err = s.archiveConfigured(ctx, q); err != nil {
			return zero, err
		}
		hash := domain.HostingDocumentHash(struct {
			Actor, Original string
			Input           domain.HostingStorageArchiveRecoveryInput
		}{actor, originalID, in})
		prior, err := q.ReadStorageArchiveRecoveryRequest(ctx, sqlcgen.ReadStorageArchiveRecoveryRequestParams{OriginalOperationID: originalID, RequestID: in.RequestID})
		if err == nil {
			if prior.CreatedBy != actor || prior.RequestHash != hash {
				return zero, ErrConflict
			}
			op, _, e := archiveOperation(ctx, tx, prior.OperationID)
			return op, e
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if p.Hash != in.ExpectedPlanHash || original.State == "succeeded" {
			return zero, ErrConflict
		}
		reservation, err := q.LockHostingPortReservation(ctx, original.BindingID)
		if err != nil {
			return zero, err
		}
		if heldID == "" || reservation.HoldOperationID == nil || *reservation.HoldOperationID != heldID {
			return zero, ErrConflict
		}
		// Task locks precede placement locks. Reload progress only after both
		// are held so a just-committed resume proof cannot be lost.
		original, _, err = archiveOperation(ctx, tx, originalID)
		if err != nil {
			return zero, err
		}
		held, _, err = archiveOperation(ctx, tx, heldID)
		if err != nil {
			return zero, err
		}
		if held.RunID != "" {
			live, e := q.HostingDeploymentLeaseLive(ctx, held.RunID)
			if e != nil {
				return zero, e
			}
			if live {
				return zero, ErrConflict
			}
		}
		evidence := archiveRemainingRecoveryEvidence(original, held)
		if evidence == nil {
			return zero, ErrConflict
		}
		p.OperationID = id
		p.Mode = "recover"
		p.RecoveryOfOperationID = originalID
		p.RecoveryEvidence = evidence
		p.Hash = ""
		p.Hash = domain.HostingDocumentHash(p)
		if err = archiveInsertPlan(ctx, tx, p, actor, "storage_archive_recover_"+in.RequestID, hash); err != nil {
			return zero, err
		}
		if err = q.InsertStorageArchiveRecovery(ctx, sqlcgen.InsertStorageArchiveRecoveryParams{OperationID: id, OriginalOperationID: originalID, RequestID: in.RequestID, RequestHash: hash, CreatedBy: actor}); err != nil {
			return zero, err
		}
		updated, err := tx.Exec(ctx, `UPDATE hosting_binding_state SET hold_operation_id=$2 WHERE binding_id=$1 AND hold_operation_id=$3`, original.BindingID, id, heldID)
		if err != nil {
			return zero, err
		}
		if updated.RowsAffected() != 1 {
			return zero, ErrConflict
		}
		if held.RunID != "" {
			if _, err = q.CancelHostingLeases(ctx, held.RunID); err != nil {
				return zero, err
			}
			if _, err = q.CancelHostingTask(ctx, held.RunID); err != nil {
				return zero, err
			}
		}
		if _, err = q.SetHostingOperationState(ctx, sqlcgen.SetHostingOperationStateParams{ID: heldID, State: "reconciliation_required"}); err != nil {
			return zero, err
		}
		op, _, err := archiveOperation(ctx, tx, id)
		if err != nil {
			return zero, err
		}
		if err = s.archiveGate(ctx, tx, op); err != nil {
			return zero, err
		}
		if err = archiveAudit(ctx, tx, actor, "recovery_requested", op); err != nil {
			return zero, err
		}
		if _, err = s.hostingJobs.EnqueueStorageArchiveTx(ctx, tx, hostingjobs.PrepareStorageArchiveArgs{OperationID: id}); err != nil {
			return zero, err
		}
		_ = event
		return op, nil
	})
}

// The held recovery already contains the remaining set from every earlier
// recovery. Never rebuild that set from the original stop intent alone.
func archiveRemainingRecoveryEvidence(original, held domain.HostingOperation) *domain.HostingStorageArchiveRecoveryEvidence {
	if held.ID != original.ID {
		prior := held.StorageArchive.Plan.RecoveryEvidence
		if prior == nil || prior.OriginalPlanHash != original.StorageArchive.Plan.Hash {
			return nil
		}
		evidence := *prior
		evidence.Writers = slices.Clone(prior.Writers)
		evidence.ResumeRequiredContainerIDs = slices.Clone(prior.ResumeRequiredContainerIDs)
		if resumed := archiveProgressByPhase(held, "resumed"); resumed != nil {
			evidence.ResumeRequiredContainerIDs = slices.DeleteFunc(evidence.ResumeRequiredContainerIDs, func(id string) bool {
				return slices.ContainsFunc(resumed.Writers, func(w domain.HostingStorageArchiveWriterState) bool { return w.ContainerID == id && w.Running })
			})
		}
		return &evidence
	}
	evidence := &domain.HostingStorageArchiveRecoveryEvidence{OriginalPlanHash: original.StorageArchive.Plan.Hash, Writers: []domain.HostingStorageArchiveWriterState{}, ResumeRequiredContainerIDs: []string{}}
	if snapshot := archiveProgressByPhase(original, "writer_snapshot"); snapshot != nil {
		evidence.SnapshotReceiptID = snapshot.Receipt.ID
		evidence.Writers = slices.Clone(snapshot.Writers)
	}
	if archiveProgressByPhase(original, "stop_intent") != nil && archiveProgressByPhase(original, "resumed") == nil {
		if len(evidence.Writers) == 0 {
			return nil
		}
		for _, w := range evidence.Writers {
			if w.Running {
				evidence.ResumeRequiredContainerIDs = append(evidence.ResumeRequiredContainerIDs, w.ContainerID)
			}
		}
	}
	return evidence
}

func archiveCancel(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, actor string) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	p := op.StorageArchive.Plan
	q := sqlcgen.New(tx)
	if err := hostingLockOperationRuns(ctx, tx, op); err != nil {
		return zero, err
	}
	resources := []string{p.SourceStorage.ResourceID}
	if p.Target != nil {
		resources = append(resources, p.Target.Resource.ID)
	}
	if err := archivePrelock(ctx, tx, p.DestinationID, resources...); err != nil {
		return zero, err
	}
	for _, id := range resources {
		if _, _, err := hostingStoragePlacement(ctx, tx, actor, id, domain.RoleOwner); err != nil {
			return zero, err
		}
	}
	if _, err := q.LockHostingPortReservation(ctx, op.BindingID); err != nil {
		return zero, err
	}
	if _, err := q.LockHostingOperation(ctx, op.ID); err != nil {
		return zero, err
	}
	current, _, err := archiveOperation(ctx, tx, op.ID)
	if err != nil {
		return zero, err
	}
	if current.State == "succeeded" || current.State == "failed" || current.State == "canceled" || current.State == "cancel_requested" {
		return current, nil
	}
	state := "cancel_requested"
	if p.Target == nil && archiveProgressByPhase(current, "stop_intent") == nil && p.Mode != "recover" {
		state = "canceled"
		if _, err = q.ClearHostingMutationHold(ctx, sqlcgen.ClearHostingMutationHoldParams{BindingID: op.BindingID, HoldOperationID: &op.ID}); err != nil {
			return zero, err
		}
	}
	if op.RunID != "" {
		if _, err = q.CancelHostingLeases(ctx, op.RunID); err != nil {
			return zero, err
		}
		if _, err = q.CancelHostingTask(ctx, op.RunID); err != nil {
			return zero, err
		}
	}
	if _, err = q.SetHostingOperationState(ctx, sqlcgen.SetHostingOperationStateParams{ID: op.ID, State: state}); err != nil {
		return zero, err
	}
	if err = archiveAudit(ctx, tx, actor, "canceled", op); err != nil {
		return zero, err
	}
	result, _, err := archiveOperation(ctx, tx, op.ID)
	return result, err
}

var _ hostingjobs.StorageArchiveHandler = (*PostgresStore)(nil)
var _ HostingStorageArchiveRepository = (*PostgresStore)(nil)

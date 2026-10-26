package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

type storageRetentionRow struct {
	plan domain.HostingStorageBackupRetentionPlan
	view domain.HostingStorageBackupRetentionExecution
	run  *string
}

func storageRetentionLoad(ctx context.Context, tx pgx.Tx, id string, lock bool) (storageRetentionRow, error) {
	var r storageRetentionRow
	var plan, body []byte
	var hash string
	var e error
	if lock {
		v, err := sqlcgen.New(tx).StorageRetentionLockExecution(ctx, id)
		plan, body, hash, r.run, e = v.Plan, v.Body, v.PlanHash, v.RunID, err
	} else {
		v, err := sqlcgen.New(tx).StorageRetentionReadExecution(ctx, id)
		plan, body, hash, r.run, e = v.Plan, v.Body, v.PlanHash, v.RunID, err
	}
	if e != nil {
		return r, hostingError(e)
	}
	if json.Unmarshal(plan, &r.plan) != nil || json.Unmarshal(body, &r.view) != nil || r.plan.Validate() != nil || r.plan.Hash != hash || r.plan.Operation.ID != id || r.view.ID != id {
		return r, ErrConflict
	}
	initial := r.plan.Operation
	if r.view.Version != initial.Version || r.view.ProjectID != initial.ProjectID || r.view.ResourceID != initial.ResourceID || r.view.PreviewID != initial.PreviewID || r.view.PolicyRevision != initial.PolicyRevision || r.view.PlanHash != initial.PlanHash || !r.view.CreatedAt.Equal(initial.CreatedAt) {
		return r, ErrConflict
	}
	validated := r.plan
	validated.Progress = r.view.Items
	if validated.Progress == nil || validated.Validate() != nil {
		return r, ErrConflict
	}
	return r, nil
}
func storageRetentionAuthority(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, live bool) (storageRetentionRow, error) {
	if _, e := sqlcgen.New(tx).LockRunID(ctx, a.RunID); e != nil {
		return storageRetentionRow{}, hostingError(e)
	}
	var id string
	id, e := sqlcgen.New(tx).StorageRetentionLockAuthority(ctx, sqlcgen.StorageRetentionLockAuthorityParams{ID: a.OperationID, RunID: optionalStorageRestore(a.RunID), RunnerID: runner, ID_2: a.LeaseID, Attempt: int32(a.Attempt), Fence: a.Fence, Column7: live})
	if e != nil {
		return storageRetentionRow{}, hostingError(e)
	}
	return storageRetentionLoad(ctx, tx, id, true)
}
func (s *PostgresStore) PrepareStorageBackupRetention(ctx context.Context, id string) error {
	_, e := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		if e := storageRetentionBoundary(ctx, tx, id); e != nil {
			return false, e
		}
		r, e := storageRetentionLoad(ctx, tx, id, true)
		if e != nil {
			return false, e
		}
		if r.view.State != "queued" {
			return true, nil
		}
		if e = storageRetentionCurrent(ctx, tx, r.plan); e != nil {
			return false, e
		}
		run := domain.TaskRun{ID: "storage_retention_" + id, ProjectID: r.view.ProjectID, RunSpec: domain.RunSpec{Type: domain.RunTypeStorageBackupRetention, HostingOperationID: id, HostingMode: "retention", Inputs: map[string]any{}}}
		if domain.ValidateStorageBackupRetentionTask(run) != nil {
			return false, ErrConflict
		}
		if _, e = sqlcgen.New(tx).CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: run.ID, ProjectID: run.ProjectID, RunSpec: buildJSON(run.RunSpec), RequestedBy: r.plan.ActorID}); e != nil {
			return false, e
		}
		r.view.State = "dispatched"
		r.view.UpdatedAt = time.Now().UTC()
		e = sqlcgen.New(tx).StorageRetentionDispatch(ctx, sqlcgen.StorageRetentionDispatchParams{ID: id, RunID: optionalStorageRestore(run.ID), Body: buildJSON(r.view)})
		return e == nil, e
	})
	return e
}
func (h deploymentOnlyHandler) PrepareStorageBackupRetention(ctx context.Context, id string) error {
	return h.store.PrepareStorageBackupRetention(ctx, id)
}
func (s *PostgresStore) StorageBackupRetentionPlan(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingStorageBackupRetentionPlan, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingStorageBackupRetentionPlan, error) {
		if e := storageRetentionBoundary(ctx, tx, a.OperationID); e != nil {
			return domain.HostingStorageBackupRetentionPlan{}, e
		}
		r, e := storageRetentionAuthority(ctx, tx, runner, a, true)
		if e != nil {
			return r.plan, e
		}
		if r.view.State != "dispatched" && r.view.State != "applying" {
			return domain.HostingStorageBackupRetentionPlan{}, ErrConflict
		}
		if e = storageRetentionCurrent(ctx, tx, r.plan); e != nil {
			return r.plan, e
		}
		r.plan.Progress = r.view.Items
		return r.plan, nil
	})
}
func (s *PostgresStore) storageBackupRetentionClaimAllowed(ctx context.Context, tx pgx.Tx, run domain.TaskRun, runner string) (bool, error) {
	if domain.ValidateStorageBackupRetentionTask(run) != nil {
		return false, nil
	}
	r, e := storageRetentionLoad(ctx, tx, run.RunSpec.HostingOperationID, false)
	if e != nil {
		return false, e
	}
	if r.run == nil || *r.run != run.ID || r.plan.RunnerID != runner || r.view.ProjectID != run.ProjectID || r.view.State != "dispatched" {
		return false, nil
	}
	e = storageRetentionCurrent(ctx, tx, r.plan)
	if errors.Is(e, ErrConflict) || errors.Is(e, ErrNotFound) || errors.Is(e, auth.ErrForbidden) || errors.Is(e, auth.ErrUnauthenticated) {
		return false, nil
	}
	return e == nil, e
}
func (s *PostgresStore) StorageBackupRetentionMutation(ctx context.Context, runner string, in domain.HostingStorageBackupRetentionMutation) (domain.HostingStorageBackupRetentionExecution, error) {
	var zero domain.HostingStorageBackupRetentionExecution
	if in.Validate() != nil {
		return zero, domain.ErrInvalidStorageBackupRetention
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingStorageBackupRetentionExecution, error) {
		if e := storageRetentionBoundary(ctx, tx, in.OperationID); e != nil {
			return zero, e
		}
		r, e := storageRetentionAuthority(ctx, tx, runner, in.HostingAuthority, false)
		if e != nil {
			return zero, e
		}
		phaseKey := in.Phase
		if in.ProbeOnly {
			phaseKey = "probe_" + phaseKey
		}
		var hash string
		var raw []byte
		savedReceipt, e := sqlcgen.New(tx).StorageRetentionReadReceipt(ctx, sqlcgen.StorageRetentionReadReceiptParams{ExecutionID: in.OperationID, CopyID: in.CopyID, Phase: phaseKey, Attempt: int32(in.Attempt)})
		hash, raw = savedReceipt.BodyHash, savedReceipt.Response
		if e == nil {
			if hash != domain.HostingDocumentHash(in) || json.Unmarshal(raw, &zero) != nil {
				return zero, ErrConflict
			}
			return zero, nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return zero, e
		}
		if _, e = storageRetentionAuthority(ctx, tx, runner, in.HostingAuthority, true); e != nil {
			// The exact historical tuple resolved above. Only its live authority
			// is now absent; unknown or foreign tuples never reach this branch.
			if errors.Is(e, ErrNotFound) {
				return zero, ErrConflict
			}
			return zero, e
		}
		if e = storageRetentionCurrent(ctx, tx, r.plan); e != nil {
			return zero, e
		}
		if in.PlanHash != r.plan.Hash {
			return zero, ErrConflict
		}
		if e = storageRetentionLock(ctx, tx); e != nil {
			return zero, e
		}
		index := -1
		for i, v := range r.view.Items {
			if v.CopyID == in.CopyID {
				index = i
			}
		}
		if index < 0 {
			return zero, ErrConflict
		}
		item := &r.view.Items[index]
		if in.ProbeOnly {
			if r.plan.Copies[index].Target == nil || !item.ReservationHeld {
				return zero, ErrConflict
			}
			switch in.Phase {
			case "intent", "deleted", "absent":
			case "uncertain", "failed":
				r.view.State = "uncertain"
				item.FailureCode = in.FailureCode
			default:
				return zero, ErrConflict
			}
		} else if in.Phase == "intent" {
			if item.State != "pending" && item.State != "intent" && item.State != "uncertain" {
				return zero, ErrConflict
			}
			item.FailureCode = ""
			item.State = "intent"
			r.view.State = "applying"
		} else {
			if item.State != "intent" && item.State != "uncertain" && !(in.Phase == "failed" && item.State == "pending") {
				return zero, ErrConflict
			}
			item.State = in.Phase
			item.FailureCode = in.FailureCode
			item.ReservationHeld = in.Phase == "uncertain"
			availability := "available"
			if in.Phase == "deleted" || in.Phase == "absent" {
				availability = "deleted"
			} else if in.Phase == "uncertain" {
				availability = "uncertain"
			}
			if e = sqlcgen.New(tx).StorageRetentionSetCopyAvailability(ctx, sqlcgen.StorageRetentionSetCopyAvailabilityParams{ID: in.CopyID, Availability: availability}); e != nil {
				return zero, e
			}
		}
		if e = sqlcgen.New(tx).StorageRetentionSetItem(ctx, sqlcgen.StorageRetentionSetItemParams{ExecutionID: in.OperationID, CopyID: in.CopyID, State: item.State, ReservationHeld: item.ReservationHeld}); e != nil {
			return zero, e
		}
		done := true
		probeFailed := in.ProbeOnly && (in.Phase == "uncertain" || in.Phase == "failed")
		held := false
		failed := false
		uncertain := false
		r.view.LocalState = "not_selected"
		r.view.RemoteState = "not_selected"
		for _, location := range []string{"local", "remote"} {
			loc := "not_selected"
			for _, v := range r.view.Items {
				if v.Location != location {
					continue
				}
				if loc == "not_selected" {
					loc = "succeeded"
				}
				switch v.State {
				case "pending", "intent":
					if loc != "uncertain" {
						loc = "pending"
					}
					done = false
				case "failed":
					if loc != "pending" && loc != "uncertain" {
						loc = "failed"
					}
					failed = true
				case "uncertain":
					loc = "uncertain"
					uncertain = true
				}
				held = held || v.ReservationHeld
			}
			if location == "local" {
				r.view.LocalState = loc
			} else {
				r.view.RemoteState = loc
			}
		}
		r.view.ReservationHeld = held
		r.view.UpdatedAt = time.Now().UTC()
		if probeFailed {
			done = true
			uncertain = true
		}
		if done {
			r.view.State = "succeeded"
			if failed {
				r.view.State = "failed"
			}
			if uncertain {
				r.view.State = "uncertain"
			}
			status := "failed"
			if r.view.State == "succeeded" {
				status = "succeeded"
			}
			if e = sqlcgen.New(tx).StorageRetentionCompleteTask(ctx, sqlcgen.StorageRetentionCompleteTaskParams{ID: in.RunID, Status: status}); e != nil {
				return zero, e
			}
			if e = sqlcgen.New(tx).StorageRetentionCompleteLease(ctx, sqlcgen.StorageRetentionCompleteLeaseParams{ID: in.LeaseID, Status: status, CompletionKey: optionalStorageRestore(in.ReceiptID)}); e != nil {
				return zero, e
			}
		}
		if e = sqlcgen.New(tx).StorageRetentionSetExecution(ctx, sqlcgen.StorageRetentionSetExecutionParams{ID: in.OperationID, Body: buildJSON(r.view), ReservationHeld: held}); e != nil {
			return zero, e
		}
		if e = storageRetentionAudit(ctx, tx, r.plan.ActorID, in.OperationID, "retention_"+in.ReceiptID, r.view.ProjectID); e != nil {
			return zero, e
		}
		if e = sqlcgen.New(tx).StorageRetentionInsertReceipt(ctx, sqlcgen.StorageRetentionInsertReceiptParams{ExecutionID: in.OperationID, CopyID: in.CopyID, Phase: phaseKey, Attempt: int32(in.Attempt), ReceiptID: in.ReceiptID, Authority: buildJSON(in.HostingAuthority), Body: buildJSON(in), BodyHash: domain.HostingDocumentHash(in), Response: buildJSON(r.view)}); e != nil {
			return zero, e
		}
		if e = storageRetentionCurrent(ctx, tx, r.plan); e != nil {
			return zero, e
		}
		clock, e := sqlcgen.New(tx).CheckStorageArchiveAuthorityClock(ctx, sqlcgen.CheckStorageArchiveAuthorityClockParams{ActorID: r.plan.ActorID, LeaseID: in.LeaseID, RequireActive: !done})
		if e != nil {
			return zero, e
		}
		if !clock.ActorValid || !clock.LeaseValid {
			return zero, ErrConflict
		}
		return r.view, nil
	})
}
func (s *PostgresStore) recoverStorageBackupRetentions(ctx context.Context) error {
	if !s.hostingJobs.StorageBackupRetentionEnabled() {
		return nil
	}
	_, e := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		rows, e := sqlcgen.New(tx).StorageRetentionRecoverQueued(ctx)
		if e != nil {
			return false, e
		}
		ids := rows
		for _, id := range ids {
			if _, e = s.hostingJobs.EnqueueStorageBackupRetentionTx(ctx, tx, hostingjobs.StorageBackupRetentionArgs{OperationID: id}); e != nil {
				return false, e
			}
		}
		return true, nil
	})
	return e
}

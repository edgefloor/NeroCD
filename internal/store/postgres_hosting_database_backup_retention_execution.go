package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

type backupRetentionRow struct {
	plan domain.HostingDatabaseBackupRetentionPlan
	view domain.HostingDatabaseBackupRetentionExecution
	run  *string
}

func backupRetentionLoad(ctx context.Context, tx pgx.Tx, id string, lock bool) (backupRetentionRow, error) {
	var r backupRetentionRow
	var plan, body []byte
	var hash string
	q := sqlcgen.New(tx)
	var e error
	if lock {
		row, err := q.LockDatabaseBackupRetentionExecution(ctx, id)
		plan, body, hash, r.run, e = row.Plan, row.Body, row.PlanHash, row.RunID, err
	} else {
		row, err := q.ReadDatabaseBackupRetentionExecution(ctx, id)
		plan, body, hash, r.run, e = row.Plan, row.Body, row.PlanHash, row.RunID, err
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
func backupRetentionAuthority(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, live bool) (backupRetentionRow, error) {
	if _, e := sqlcgen.New(tx).LockRunID(ctx, a.RunID); e != nil {
		return backupRetentionRow{}, hostingError(e)
	}
	var id string
	attempt, e := databaseBackupAuthorityAttempt(a.Attempt)
	if e != nil {
		return backupRetentionRow{}, e
	}
	id, e = sqlcgen.New(tx).LockDatabaseBackupRetentionAuthority(ctx, sqlcgen.LockDatabaseBackupRetentionAuthorityParams{ID: a.OperationID, RunID: &a.RunID, RunnerID: runner, ID_2: a.LeaseID, Attempt: attempt, Fence: a.Fence, Column7: live})
	if e != nil {
		return backupRetentionRow{}, hostingError(e)
	}
	return backupRetentionLoad(ctx, tx, id, true)
}

// PrepareDatabaseBackupRetention prepares an admitted database retention batch for dispatch.
func (s *PostgresStore) PrepareDatabaseBackupRetention(ctx context.Context, id string) error {
	_, e := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		if e := backupRetentionBoundary(ctx, tx, id); e != nil {
			return false, e
		}
		r, e := backupRetentionLoad(ctx, tx, id, true)
		if e != nil {
			return false, e
		}
		if r.view.State != "queued" {
			return true, nil
		}
		if e = backupRetentionCurrent(ctx, tx, r.plan); e != nil {
			return false, e
		}
		run := domain.TaskRun{ID: "db_retention_" + id, ProjectID: r.view.ProjectID, RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseBackupRetention, HostingOperationID: id, HostingMode: "retention", Inputs: map[string]any{}}}
		if domain.ValidateDatabaseBackupRetentionTask(run) != nil {
			return false, ErrConflict
		}
		if _, e = sqlcgen.New(tx).CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: run.ID, ProjectID: run.ProjectID, RunSpec: buildJSON(run.RunSpec), RequestedBy: r.plan.ActorID}); e != nil {
			return false, e
		}
		r.view.State = "dispatched"
		r.view.UpdatedAt = time.Now().UTC()
		e = sqlcgen.New(tx).DispatchDatabaseBackupRetention(ctx, sqlcgen.DispatchDatabaseBackupRetentionParams{ID: id, RunID: &run.ID, Body: buildJSON(r.view)})
		return e == nil, e
	})
	return e
}
func (h deploymentOnlyHandler) PrepareDatabaseBackupRetention(ctx context.Context, id string) error {
	return h.store.PrepareDatabaseBackupRetention(ctx, id)
}

// DatabaseBackupRetentionPlan loads the database retention plan under current runner lease
// authority.
func (s *PostgresStore) DatabaseBackupRetentionPlan(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingDatabaseBackupRetentionPlan, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupRetentionPlan, error) {
		if e := backupRetentionBoundary(ctx, tx, a.OperationID); e != nil {
			return domain.HostingDatabaseBackupRetentionPlan{}, e
		}
		r, e := backupRetentionAuthority(ctx, tx, runner, a, true)
		if e != nil {
			return r.plan, e
		}
		if r.view.State != "dispatched" && r.view.State != "applying" {
			return domain.HostingDatabaseBackupRetentionPlan{}, ErrConflict
		}
		if e = backupRetentionCurrent(ctx, tx, r.plan); e != nil {
			return r.plan, e
		}
		r.plan.Progress = r.view.Items
		return r.plan, nil
	})
}
func (s *PostgresStore) databaseBackupRetentionClaimAllowed(ctx context.Context, tx pgx.Tx, run domain.TaskRun, runner string) (bool, error) {
	if domain.ValidateDatabaseBackupRetentionTask(run) != nil {
		return false, nil
	}
	r, e := backupRetentionLoad(ctx, tx, run.RunSpec.HostingOperationID, false)
	if e != nil {
		return false, e
	}
	if r.run == nil || *r.run != run.ID || r.plan.RunnerID != runner || r.view.ProjectID != run.ProjectID || r.view.State != "dispatched" {
		return false, nil
	}
	e = backupRetentionCurrent(ctx, tx, r.plan)
	if errors.Is(e, ErrConflict) || errors.Is(e, ErrNotFound) || errors.Is(e, auth.ErrForbidden) || errors.Is(e, auth.ErrUnauthenticated) {
		return false, nil
	}
	return e == nil, e
}

// DatabaseBackupRetentionMutation records a fenced database copy deletion outcome and receipt.
func (s *PostgresStore) DatabaseBackupRetentionMutation(ctx context.Context, runner string, in domain.HostingDatabaseBackupRetentionMutation) (domain.HostingDatabaseBackupRetentionExecution, error) {
	var zero domain.HostingDatabaseBackupRetentionExecution
	if in.Validate() != nil {
		return zero, domain.ErrInvalidDatabaseBackupRetention
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupRetentionExecution, error) {
		if e := backupRetentionBoundary(ctx, tx, in.OperationID); e != nil {
			return zero, e
		}
		r, e := backupRetentionAuthority(ctx, tx, runner, in.HostingAuthority, false)
		if e != nil {
			return zero, e
		}
		if in.ProbeOnly && !strings.HasPrefix(in.ProbeKey, domain.HostingDatabaseBackupRetentionProbeNamespace(r.plan.Operation.Version)+in.OperationID+"/") {
			return zero, ErrConflict
		}
		phaseKey := in.Phase
		if in.ProbeOnly {
			phaseKey = "probe_" + phaseKey
		}
		var hash string
		var raw []byte
		readDatabaseBackupRetentionReceiptRow, e := sqlcgen.New(tx).ReadDatabaseBackupRetentionReceipt(ctx, sqlcgen.ReadDatabaseBackupRetentionReceiptParams{ExecutionID: in.OperationID, CopyID: in.CopyID, Phase: phaseKey, Attempt: int32(in.Attempt)})
		hash = readDatabaseBackupRetentionReceiptRow.BodyHash
		raw = readDatabaseBackupRetentionReceiptRow.Response
		if e == nil {
			if hash != domain.HostingDocumentHash(in) || json.Unmarshal(raw, &zero) != nil {
				return zero, ErrConflict
			}
			return zero, nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return zero, e
		}
		if _, e = backupRetentionAuthority(ctx, tx, runner, in.HostingAuthority, true); e != nil {
			// The exact historical tuple resolved above. Only its live authority
			// is now absent; unknown or foreign tuples never reach this branch.
			if errors.Is(e, ErrNotFound) {
				return zero, ErrConflict
			}
			return zero, e
		}
		if e = backupRetentionCurrent(ctx, tx, r.plan); e != nil {
			return zero, e
		}
		if in.PlanHash != r.plan.Hash {
			return zero, ErrConflict
		}
		if e = backupRetentionLock(ctx, tx); e != nil {
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
			if item.State != "intent" && item.State != "uncertain" && (in.Phase != "failed" || item.State != "pending") {
				return zero, ErrConflict
			}
			item.State = in.Phase
			item.FailureCode = in.FailureCode
			item.ReservationHeld = in.Phase == "uncertain"
			availability := "available"
			switch in.Phase {
			case "deleted", "absent":
				availability = "deleted"
			case "uncertain":
				availability = "uncertain"
			}
			if e = sqlcgen.New(tx).SetDatabaseBackupCopyAvailability(ctx, sqlcgen.SetDatabaseBackupCopyAvailabilityParams{ID: in.CopyID, Availability: availability}); e != nil {
				return zero, e
			}
		}
		if e = sqlcgen.New(tx).SetDatabaseBackupRetentionItem(ctx, sqlcgen.SetDatabaseBackupRetentionItemParams{ExecutionID: in.OperationID, CopyID: in.CopyID, State: item.State, ReservationHeld: item.ReservationHeld}); e != nil {
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
			if e = sqlcgen.New(tx).CompleteDatabaseBackupRetentionTask(ctx, sqlcgen.CompleteDatabaseBackupRetentionTaskParams{ID: in.RunID, Status: status}); e != nil {
				return zero, e
			}
			if e = sqlcgen.New(tx).CompleteDatabaseBackupRetentionLease(ctx, sqlcgen.CompleteDatabaseBackupRetentionLeaseParams{ID: in.LeaseID, Status: status, CompletionKey: &in.ReceiptID}); e != nil {
				return zero, e
			}
		}
		if e = sqlcgen.New(tx).SetDatabaseBackupRetentionExecutionState(ctx, sqlcgen.SetDatabaseBackupRetentionExecutionStateParams{ID: in.OperationID, Body: buildJSON(r.view), ReservationHeld: held}); e != nil {
			return zero, e
		}
		if e = databaseBackupAudit(ctx, tx, r.plan.ActorID, in.OperationID, "retention_"+in.ReceiptID, r.view.ProjectID); e != nil {
			return zero, e
		}
		if e = sqlcgen.New(tx).CreateDatabaseBackupRetentionReceipt(ctx, sqlcgen.CreateDatabaseBackupRetentionReceiptParams{ExecutionID: in.OperationID, CopyID: in.CopyID, Phase: phaseKey, Attempt: int32(in.Attempt), ReceiptID: in.ReceiptID, Authority: buildJSON(in.HostingAuthority), Body: buildJSON(in), BodyHash: domain.HostingDocumentHash(in), Response: buildJSON(r.view)}); e != nil {
			return zero, e
		}
		if e = backupRetentionCurrent(ctx, tx, r.plan); e != nil {
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
func (s *PostgresStore) recoverDatabaseBackupRetentions(ctx context.Context) error {
	if !s.hostingJobs.DatabaseBackupRetentionEnabled() {
		return nil
	}
	_, e := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		ids, e := sqlcgen.New(tx).ListQueuedDatabaseBackupRetentionIDs(ctx)
		if e != nil {
			return false, e
		}
		for _, id := range ids {
			if _, e = s.hostingJobs.EnqueueDatabaseBackupRetentionTx(ctx, tx, hostingjobs.DatabaseBackupRetentionArgs{OperationID: id}); e != nil {
				return false, e
			}
		}
		return true, nil
	})
	return e
}

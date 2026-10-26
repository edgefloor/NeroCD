package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// The order matches archive producer admission: placement/resource -> pool ->
// task -> lease -> execution. Historical receipt replay does not reuse cached
// owner authorization, and this lock helper grants no mutation authority.
func backupRetentionBoundary(ctx context.Context, tx pgx.Tx, id string) error {
	var resource string
	resource, e := sqlcgen.New(tx).ReadDatabaseBackupRetentionResource(ctx, id)
	if e != nil {
		return hostingError(e)
	}
	if e := lockHostingResourceDestination(ctx, tx, resource); e != nil {
		return e
	}
	if _, e := sqlcgen.New(tx).LockHostingResourceTarget(ctx, resource); e != nil {
		return hostingError(e)
	}
	return backupRetentionLock(ctx, tx)
}
func (s *PostgresStore) backupRetentionResumeTx(ctx context.Context, tx pgx.Tx, id, actor, resource string, in domain.HostingDatabaseBackupRetentionExecutionInput, requestHash string) (domain.HostingDatabaseBackupRetentionExecution, error) {
	var zero domain.HostingDatabaseBackupRetentionExecution
	// Caller already holds placement and pool locks. Lock the task before leases.
	prior, e := backupRetentionLoad(ctx, tx, id, false)
	if e != nil {
		return zero, e
	}
	if prior.plan.ActorID != actor || prior.view.PolicyRevision != in.ExpectedPolicyRevision || prior.view.PlanHash != in.ExpectedPlanHash || prior.view.PreviewID != in.PreviewID {
		return zero, ErrConflict
	}
	if e = backupRetentionCurrent(ctx, tx, prior.plan); e != nil {
		return zero, e
	}
	if prior.run != nil {
		if _, e = sqlcgen.New(tx).LockRunID(ctx, *prior.run); e != nil {
			return zero, hostingError(e)
		}
	}
	r, e := backupRetentionLoad(ctx, tx, id, true)
	if e != nil {
		return zero, e
	}
	if r.view.State == "succeeded" || r.view.State == "failed" && !r.view.ReservationHeld || r.run == nil {
		if e = backupRetentionSaveRequest(ctx, tx, resource, actor, "execution", in.RequestID, requestHash, r.view); e != nil {
			return zero, e
		}
		return r.view, nil
	}
	var live bool
	live, e = sqlcgen.New(tx).DatabaseBackupRetentionLeaseLive(ctx, *r.run)
	if e != nil {
		return zero, e
	}
	if live {
		if e = backupRetentionSaveRequest(ctx, tx, resource, actor, "execution", in.RequestID, requestHash, r.view); e != nil {
			return zero, e
		}
		return r.view, nil
	}
	nextAttemptRow, e := sqlcgen.New(tx).ReadDatabaseBackupRetentionNextAttempt(ctx, *r.run)
	if e != nil {
		return zero, e
	}
	nextAttempt := int(nextAttemptRow)
	// A dispatched task without a first lease is already claimable; do not create
	// a duplicate attempt or another reservation while the runner is starting.
	if nextAttempt == 1 {
		if e = backupRetentionSaveRequest(ctx, tx, resource, actor, "execution", in.RequestID, requestHash, r.view); e != nil {
			return zero, e
		}
		return r.view, nil
	}
	retryable := false
	for i, item := range r.view.Items {
		if !item.ReservationHeld {
			continue
		}
		copy := r.plan.Copies[i]
		unknownNonversion := item.State == "uncertain" && copy.Remote != nil && (copy.Remote.VersionID == "" || copy.Remote.VersionID == "null")
		if !unknownNonversion {
			retryable = true
		}
	}
	if !retryable {
		return zero, ErrConflict
	}
	next := nextAttempt
	if e = sqlcgen.New(tx).ExpireDatabaseBackupRetentionLeases(ctx, *r.run); e != nil {
		return zero, e
	}
	if e = sqlcgen.New(tx).CreateDatabaseBackupRetentionResume(ctx, sqlcgen.CreateDatabaseBackupRetentionResumeParams{ExecutionID: id, NextAttempt: int32(next), RequestID: in.RequestID, ActorID: actor, PlanHash: r.plan.Hash}); e != nil {
		return zero, hostingError(e)
	}
	r.view.State = "dispatched"
	r.view.UpdatedAt = time.Now().UTC()
	if e = sqlcgen.New(tx).QueueDatabaseBackupRetentionTask(ctx, *r.run); e != nil {
		return zero, e
	}
	if e = sqlcgen.New(tx).UpdateDatabaseBackupRetentionExecutionBody(ctx, sqlcgen.UpdateDatabaseBackupRetentionExecutionBodyParams{ID: id, Body: buildJSON(r.view)}); e != nil {
		return zero, e
	}
	if e = databaseBackupAudit(ctx, tx, actor, id, "retention_resume_"+in.RequestID, r.view.ProjectID); e != nil {
		return zero, e
	}
	if e = backupRetentionSaveRequest(ctx, tx, resource, actor, "execution", in.RequestID, requestHash, r.view); e != nil {
		return zero, e
	}
	return r.view, databaseBackupFinalizeActor(ctx, tx, actor)
}

// DatabaseBackupRetentionRetirement proves that a newer audited database retention attempt
// superseded a lease.
func (s *PostgresStore) DatabaseBackupRetentionRetirement(ctx context.Context, runner string, in domain.HostingDatabaseBackupRetentionRetirementInput) (domain.HostingDatabaseBackupRetentionRetirement, error) {
	var zero domain.HostingDatabaseBackupRetentionRetirement
	if in.Validate() != nil {
		return zero, domain.ErrInvalidDatabaseBackupRetention
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupRetentionRetirement, error) {
		if e := backupRetentionBoundary(ctx, tx, in.OperationID); e != nil {
			return zero, e
		}
		r, e := backupRetentionAuthority(ctx, tx, runner, in.HostingAuthority, false)
		if e != nil {
			return zero, e
		}
		if in.PlanHash != r.plan.Hash {
			return zero, ErrConflict
		}
		nextRow, e := sqlcgen.New(tx).ReadDatabaseBackupRetentionRetirement(ctx, sqlcgen.ReadDatabaseBackupRetentionRetirementParams{ExecutionID: in.OperationID, RunID: in.RunID, ID: in.LeaseID, NextAttempt: int32(in.Attempt), PlanHash: in.PlanHash, Fence: in.Fence})
		if e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return zero, ErrConflict
			}
			return zero, hostingError(e)
		}
		return domain.HostingDatabaseBackupRetentionRetirement{HostingAuthority: in.HostingAuthority, PlanHash: in.PlanHash, NextAttempt: int(nextRow), ReplayOnly: true}, nil
	})
}

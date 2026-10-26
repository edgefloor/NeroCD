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
func storageRetentionBoundary(ctx context.Context, tx pgx.Tx, id string) error {
	resource, e := sqlcgen.New(tx).StorageRetentionReadResource(ctx, id)
	if e != nil {
		return hostingError(e)
	}
	if e := lockHostingResourceDestination(ctx, tx, resource); e != nil {
		return e
	}
	if _, e := sqlcgen.New(tx).LockHostingResourceTarget(ctx, resource); e != nil {
		return hostingError(e)
	}
	return storageRetentionLock(ctx, tx)
}
func (s *PostgresStore) storageRetentionResumeTx(ctx context.Context, tx pgx.Tx, id, actor, resource string, in domain.HostingStorageBackupRetentionExecutionInput, requestHash string) (domain.HostingStorageBackupRetentionExecution, error) {
	var zero domain.HostingStorageBackupRetentionExecution
	// Caller already holds placement and pool locks. Lock the task before leases.
	prior, e := storageRetentionLoad(ctx, tx, id, false)
	if e != nil {
		return zero, e
	}
	if prior.plan.ActorID != actor || prior.view.PolicyRevision != in.ExpectedPolicyRevision || prior.view.PlanHash != in.ExpectedPlanHash || prior.view.PreviewID != in.PreviewID {
		return zero, ErrConflict
	}
	if e = storageRetentionCurrent(ctx, tx, prior.plan); e != nil {
		return zero, e
	}
	if prior.run != nil {
		if _, e = sqlcgen.New(tx).LockRunID(ctx, *prior.run); e != nil {
			return zero, hostingError(e)
		}
	}
	r, e := storageRetentionLoad(ctx, tx, id, true)
	if e != nil {
		return zero, e
	}
	if r.view.State == "succeeded" || r.view.State == "failed" && !r.view.ReservationHeld || r.run == nil {
		if e = storageRetentionSaveRequest(ctx, tx, resource, actor, "execution", in.RequestID, requestHash, r.view); e != nil {
			return zero, e
		}
		return r.view, nil
	}
	var live bool
	if live, e = sqlcgen.New(tx).StorageRetentionLeaseLive(ctx, *r.run); e != nil {
		return zero, e
	}
	if live {
		if e = storageRetentionSaveRequest(ctx, tx, resource, actor, "execution", in.RequestID, requestHash, r.view); e != nil {
			return zero, e
		}
		return r.view, nil
	}
	nextAttempt, e := sqlcgen.New(tx).StorageRetentionNextAttempt(ctx, *r.run)
	if e != nil {
		return zero, e
	}
	// A dispatched task without a first lease is already claimable; do not create
	// a duplicate attempt or another reservation while the runner is starting.
	if nextAttempt == 1 {
		if e = storageRetentionSaveRequest(ctx, tx, resource, actor, "execution", in.RequestID, requestHash, r.view); e != nil {
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
	if e = sqlcgen.New(tx).StorageRetentionExpireLeases(ctx, *r.run); e != nil {
		return zero, e
	}
	if e = sqlcgen.New(tx).StorageRetentionInsertResume(ctx, sqlcgen.StorageRetentionInsertResumeParams{ExecutionID: id, NextAttempt: int32(next), RequestID: in.RequestID, ActorID: actor, PlanHash: r.plan.Hash}); e != nil {
		return zero, hostingError(e)
	}
	r.view.State = "dispatched"
	r.view.UpdatedAt = time.Now().UTC()
	if e = sqlcgen.New(tx).StorageRetentionRequeueTask(ctx, *r.run); e != nil {
		return zero, e
	}
	if e = sqlcgen.New(tx).StorageRetentionSetExecutionBody(ctx, sqlcgen.StorageRetentionSetExecutionBodyParams{ID: id, Body: buildJSON(r.view)}); e != nil {
		return zero, e
	}
	if e = storageRetentionAudit(ctx, tx, actor, id, "retention_resume_"+in.RequestID, r.view.ProjectID); e != nil {
		return zero, e
	}
	if e = storageRetentionSaveRequest(ctx, tx, resource, actor, "execution", in.RequestID, requestHash, r.view); e != nil {
		return zero, e
	}
	return r.view, archiveFinalizeActor(ctx, tx, actor)
}
func (s *PostgresStore) StorageBackupRetentionRetirement(ctx context.Context, runner string, in domain.HostingStorageBackupRetentionRetirementInput) (domain.HostingStorageBackupRetentionRetirement, error) {
	var zero domain.HostingStorageBackupRetentionRetirement
	if in.Validate() != nil {
		return zero, domain.ErrInvalidStorageBackupRetention
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingStorageBackupRetentionRetirement, error) {
		if e := storageRetentionBoundary(ctx, tx, in.OperationID); e != nil {
			return zero, e
		}
		r, e := storageRetentionAuthority(ctx, tx, runner, in.HostingAuthority, false)
		if e != nil {
			return zero, e
		}
		if in.PlanHash != r.plan.Hash {
			return zero, ErrConflict
		}
		next, e := sqlcgen.New(tx).StorageRetentionReadRetirement(ctx, sqlcgen.StorageRetentionReadRetirementParams{ExecutionID: in.OperationID, RunID: in.RunID, ID: in.LeaseID, NextAttempt: int32(in.Attempt), PlanHash: in.PlanHash, Fence: in.Fence})
		if e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return zero, ErrConflict
			}
			return zero, hostingError(e)
		}
		return domain.HostingStorageBackupRetentionRetirement{HostingAuthority: in.HostingAuthority, PlanHash: in.PlanHash, NextAttempt: int(next), ReplayOnly: true}, nil
	})
}

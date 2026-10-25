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
	if e := tx.QueryRow(ctx, `SELECT resource_id FROM hosting_database_backup_retention_executions WHERE id=$1`, id).Scan(&resource); e != nil {
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
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM run_leases WHERE run_id=$1 AND status='active' AND expires_at>clock_timestamp())`, *r.run).Scan(&live); e != nil {
		return zero, e
	}
	if live {
		if e = backupRetentionSaveRequest(ctx, tx, resource, actor, "execution", in.RequestID, requestHash, r.view); e != nil {
			return zero, e
		}
		return r.view, nil
	}
	var nextAttempt int
	if e = tx.QueryRow(ctx, `SELECT next_attempt FROM task_runs WHERE id=$1`, *r.run).Scan(&nextAttempt); e != nil {
		return zero, e
	}
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
	if _, e = tx.Exec(ctx, `UPDATE run_leases SET status='expired',completed_at=clock_timestamp() WHERE run_id=$1 AND status='active' AND expires_at<=clock_timestamp()`, *r.run); e != nil {
		return zero, e
	}
	if _, e = tx.Exec(ctx, `INSERT INTO hosting_database_backup_retention_resumes(execution_id,next_attempt,request_id,actor_id,plan_hash) VALUES($1,$2,$3,$4,$5)`, id, next, in.RequestID, actor, r.plan.Hash); e != nil {
		return zero, hostingError(e)
	}
	r.view.State = "dispatched"
	r.view.UpdatedAt = time.Now().UTC()
	if _, e = tx.Exec(ctx, `UPDATE task_runs SET status='queued',runner_id=NULL,finished_at=NULL,claim_order_at=clock_timestamp() WHERE id=$1`, *r.run); e != nil {
		return zero, e
	}
	if _, e = tx.Exec(ctx, `UPDATE hosting_database_backup_retention_executions SET body=$2 WHERE id=$1`, id, buildJSON(r.view)); e != nil {
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
		var next int
		e = tx.QueryRow(ctx, `SELECT a.next_attempt FROM hosting_database_backup_retention_resumes a JOIN run_leases old ON old.run_id=$2 AND old.id=$3 WHERE a.execution_id=$1 AND a.next_attempt>$4 AND a.plan_hash=$5 AND old.attempt=$4 AND old.fence=$6 AND (old.status<>'active' OR old.expires_at<=clock_timestamp()) ORDER BY a.next_attempt LIMIT 1`, in.OperationID, in.RunID, in.LeaseID, in.Attempt, in.PlanHash, in.Fence).Scan(&next)
		if e != nil {
			if errors.Is(e, pgx.ErrNoRows) {
				return zero, ErrConflict
			}
			return zero, hostingError(e)
		}
		return domain.HostingDatabaseBackupRetentionRetirement{HostingAuthority: in.HostingAuthority, PlanHash: in.PlanHash, NextAttempt: next, ReplayOnly: true}, nil
	})
}

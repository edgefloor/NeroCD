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

type backupRetentionRow struct {
	plan domain.HostingDatabaseBackupRetentionPlan
	view domain.HostingDatabaseBackupRetentionExecution
	run  *string
}

func backupRetentionLoad(ctx context.Context, tx pgx.Tx, id string, lock bool) (backupRetentionRow, error) {
	var r backupRetentionRow
	var plan, body []byte
	var hash string
	query := `SELECT plan,body,plan_hash,run_id FROM hosting_database_backup_retention_executions WHERE id=$1`
	if lock {
		query += " FOR UPDATE"
	}
	e := tx.QueryRow(ctx, query, id).Scan(&plan, &body, &hash, &r.run)
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
	e := tx.QueryRow(ctx, `SELECT e.id FROM hosting_database_backup_retention_executions e JOIN task_runs t ON t.id=e.run_id JOIN run_leases l ON l.run_id=t.id WHERE e.id=$1 AND e.run_id=$2 AND e.runner_id=$3 AND l.id=$4 AND l.runner_id=$3 AND l.attempt=$5 AND l.fence=$6 AND (NOT $7 OR (l.status='active' AND l.expires_at>clock_timestamp())) AND t.run_spec=jsonb_build_object('type','database_backup_retention','hosting_operation_id',e.id,'hosting_mode','retention','inputs','{}'::jsonb) FOR UPDATE OF l`, a.OperationID, a.RunID, runner, a.LeaseID, a.Attempt, a.Fence, live).Scan(&id)
	if e != nil {
		return backupRetentionRow{}, hostingError(e)
	}
	return backupRetentionLoad(ctx, tx, id, true)
}
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
		_, e = tx.Exec(ctx, `UPDATE hosting_database_backup_retention_executions SET run_id=$2,body=$3 WHERE id=$1`, id, run.ID, buildJSON(r.view))
		return e == nil, e
	})
	return e
}
func (h deploymentOnlyHandler) PrepareDatabaseBackupRetention(ctx context.Context, id string) error {
	return h.store.PrepareDatabaseBackupRetention(ctx, id)
}
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
		phaseKey := in.Phase
		if in.ProbeOnly {
			phaseKey = "probe_" + phaseKey
		}
		var hash string
		var raw []byte
		e = tx.QueryRow(ctx, `SELECT body_hash,response FROM hosting_database_backup_retention_receipts WHERE execution_id=$1 AND copy_id=$2 AND phase=$3 AND attempt=$4`, in.OperationID, in.CopyID, phaseKey, in.Attempt).Scan(&hash, &raw)
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
			if _, e = tx.Exec(ctx, `UPDATE hosting_database_backup_copies SET availability=$2 WHERE id=$1`, in.CopyID, availability); e != nil {
				return zero, e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE hosting_database_backup_retention_items SET state=$3,reservation_held=$4 WHERE execution_id=$1 AND copy_id=$2`, in.OperationID, in.CopyID, item.State, item.ReservationHeld); e != nil {
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
			if _, e = tx.Exec(ctx, `UPDATE task_runs SET status=$2,finished_at=clock_timestamp() WHERE id=$1`, in.RunID, status); e != nil {
				return zero, e
			}
			if _, e = tx.Exec(ctx, `UPDATE run_leases SET status=$2,completed_at=clock_timestamp(),completion_key=$3 WHERE id=$1 AND status='active'`, in.LeaseID, status, in.ReceiptID); e != nil {
				return zero, e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE hosting_database_backup_retention_executions SET body=$2,reservation_held=$3 WHERE id=$1`, in.OperationID, buildJSON(r.view), held); e != nil {
			return zero, e
		}
		if e = databaseBackupAudit(ctx, tx, r.plan.ActorID, in.OperationID, "retention_"+in.ReceiptID, r.view.ProjectID); e != nil {
			return zero, e
		}
		if _, e = tx.Exec(ctx, `INSERT INTO hosting_database_backup_retention_receipts(execution_id,copy_id,phase,attempt,receipt_id,authority,body,body_hash,response) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, in.OperationID, in.CopyID, phaseKey, in.Attempt, in.ReceiptID, buildJSON(in.HostingAuthority), buildJSON(in), domain.HostingDocumentHash(in), buildJSON(r.view)); e != nil {
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
		rows, e := tx.Query(ctx, `SELECT id FROM hosting_database_backup_retention_executions WHERE body->>'state'='queued' ORDER BY created_at,id LIMIT 100`)
		if e != nil {
			return false, e
		}
		var ids []string
		for rows.Next() {
			var id string
			if e = rows.Scan(&id); e != nil {
				rows.Close()
				return false, e
			}
			ids = append(ids, id)
		}
		rows.Close()
		if rows.Err() != nil {
			return false, rows.Err()
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

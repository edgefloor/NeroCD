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

type backupRemoteRow struct {
	plan   domain.HostingDatabaseBackupTransferPlan
	view   domain.HostingDatabaseBackupTransfer
	run    *string
	daemon string
}

func backupRemoteLoad(ctx context.Context, tx pgx.Tx, id string, lock bool) (backupRemoteRow, error) {
	var r backupRemoteRow
	var row sqlcgen.HostingDatabaseBackupTransfer
	var err error
	if lock {
		row, err = sqlcgen.New(tx).LockDatabaseBackupTransfer(ctx, id)
	} else {
		row, err = sqlcgen.New(tx).ReadDatabaseBackupTransfer(ctx, id)
	}
	if err != nil {
		return r, hostingError(err)
	}
	if json.Unmarshal(row.Plan, &r.plan) != nil || r.plan.Hash != row.PlanHash || r.plan.Operation.ID != id || r.plan.Operation.ProjectID != row.ProjectID || r.plan.ActorID != row.ActorID || r.plan.RunnerID != row.RunnerID || r.plan.Operation.DestinationID != row.DestinationID || r.plan.Operation.TargetID != row.TargetID || r.plan.Operation.TargetRevision != row.TargetRevision || r.plan.Operation.Mode != row.Mode || r.plan.Operation.BackupID != proxyString(row.BackupID) || r.plan.Operation.RestoreOperationID != proxyString(row.RestoreOperationID) || r.plan.Validate() != nil {
		return r, ErrConflict
	}
	r.run = row.RunID
	r.daemon = row.DaemonID
	r.view = r.plan.Operation
	r.view.State = row.State
	r.view.FailureCode = row.FailureCode
	r.view.CreatedAt = row.CreatedAt
	r.view.UpdatedAt = row.UpdatedAt

	if r.view.Validate() != nil {
		return r, ErrConflict
	}
	return r, nil
}

// Restore uses historical immutable configuration. Current enablement and exact
// old key references are checked independently, never redirected to the latest.
func backupRemoteCurrent(ctx context.Context, tx pgx.Tx, p domain.HostingDatabaseBackupTransferPlan) error {
	if p.Validate() != nil {
		return ErrConflict
	}
	if err := proxyAuthorize(ctx, tx, p.ActorID, p.Operation.ProjectID, domain.RoleOwner, false); err != nil {
		return err
	}
	q := sqlcgen.New(tx)
	if err := databaseBindingGrant(ctx, q, p.Operation.ProjectID, p.Operation.DestinationID); err != nil {
		return err
	}
	var enabled bool
	var project string
	if err := tx.QueryRow(ctx, `SELECT enabled,project_id FROM hosting_database_backup_targets WHERE id=$1 FOR SHARE`, p.Operation.TargetID).Scan(&enabled, &project); err != nil {
		return hostingError(err)
	}
	if !enabled || project != p.Operation.ProjectID {
		return ErrConflict
	}
	snapshot, err := backupRemoteTargetSnapshot(ctx, tx, p.Operation.TargetID, p.Operation.TargetRevision)
	if err != nil {
		return err
	}
	if domain.HostingDocumentHash(snapshot) != domain.HostingDocumentHash(p.Target) {
		return ErrConflict
	}
	for _, ref := range []domain.BackupRemoteCredentialRef{snapshot.Access, snapshot.Secret} {
		if _, err = backupRemoteCredentialRecord(ctx, tx, project, ref, true); err != nil {
			return err
		}
	}
	var server, runner string
	if err = tx.QueryRow(ctx, `SELECT d.server_id,s.runner_id FROM destinations d JOIN servers s ON s.id=d.server_id WHERE d.id=$1 FOR SHARE OF d,s`, p.Operation.DestinationID).Scan(&server, &runner); err != nil {
		return hostingError(err)
	}
	if server != p.ServerID || runner != p.RunnerID {
		return ErrConflict
	}
	var eligible string
	err = tx.QueryRow(ctx, `SELECT r.id FROM runners r JOIN runner_enrollments e ON e.runner_id=r.id WHERE r.id=$1 AND r.status='active' AND r.token_hash<>'' AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash AND $2=ANY(r.capabilities) AND $3=ANY(r.capabilities) AND NOT('shell'=ANY(r.capabilities)) FOR SHARE OF r,e`, runner, domain.HostingDatabaseBackupRemoteCapability, domain.RunTypeDatabaseBackupTransfer).Scan(&eligible)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	return databaseBackupFinalizeActor(ctx, tx, p.ActorID)
}

func (s *PostgresStore) backupRemoteInsert(ctx context.Context, tx pgx.Tx, p domain.HostingDatabaseBackupTransferPlan, request, hash string, predecessor *string) error {
	if p.Validate() != nil || !s.hostingJobs.DatabaseBackupTransferEnabled() {
		return ErrConflict
	}
	var backup, restore *string
	if p.Operation.BackupID != "" {
		backup = &p.Operation.BackupID
	}
	if p.Operation.RestoreOperationID != "" {
		restore = &p.Operation.RestoreOperationID
	}
	var blob any
	if p.Operation.Blob != nil {
		blob = buildJSON(p.Operation.Blob)
	}
	_, err := tx.Exec(ctx, `INSERT INTO hosting_database_backup_transfers(id,project_id,target_id,target_revision,destination_id,actor_id,request_id,request_hash,mode,backup_id,restore_operation_id,predecessor_id,state,runner_id,daemon_id,blob,plan,plan_hash,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'queued',$13,$14,$15,$16,$17,$18,$18)`, p.Operation.ID, p.Operation.ProjectID, p.Operation.TargetID, p.Operation.TargetRevision, p.Operation.DestinationID, p.ActorID, request, hash, p.Operation.Mode, backup, restore, predecessor, p.RunnerID, p.DaemonID, blob, buildJSON(p), p.Hash, p.Operation.CreatedAt)
	if err != nil {
		return hostingError(err)
	}
	if _, err = s.hostingJobs.EnqueueDatabaseBackupTransferTx(ctx, tx, hostingjobs.DatabaseBackupTransferArgs{OperationID: p.Operation.ID}); err != nil {
		return err
	}
	return databaseBackupAudit(ctx, tx, p.ActorID, p.Operation.ID, "remote_requested", p.Operation.ProjectID)
}

func (s *PostgresStore) ValidateDatabaseBackupTarget(ctx context.Context, id, project, actor, target string, in domain.HostingDatabaseBackupTargetValidationInput) (domain.HostingDatabaseBackupTransfer, error) {
	if in.Validate() != nil {
		return domain.HostingDatabaseBackupTransfer{}, domain.ErrInvalidDatabaseBackupRemote
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupTransfer, error) {
		var zero domain.HostingDatabaseBackupTransfer
		if err := proxyAuthorize(ctx, tx, actor, project, domain.RoleOwner, false); err != nil {
			return zero, err
		}
		var locked string
		if err := tx.QueryRow(ctx, `SELECT id FROM projects WHERE id=$1 FOR UPDATE`, project).Scan(&locked); err != nil {
			return zero, hostingError(err)
		}
		hash := domain.HostingDocumentHash(struct {
			Actor, Target string
			Input         domain.HostingDatabaseBackupTargetValidationInput
		}{actor, target, in})
		var prior, priorHash string
		err := tx.QueryRow(ctx, `SELECT id,request_hash FROM hosting_database_backup_transfers WHERE project_id=$1 AND request_id=$2`, project, in.RequestID).Scan(&prior, &priorHash)
		if err == nil {
			if priorHash != hash {
				return zero, ErrConflict
			}
			r, e := backupRemoteLoad(ctx, tx, prior, false)
			return r.view, e
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		var revision int64
		if err = tx.QueryRow(ctx, `SELECT revision FROM hosting_database_backup_targets WHERE id=$1 AND project_id=$2 FOR SHARE`, target, project).Scan(&revision); err != nil {
			return zero, hostingError(err)
		}
		if revision != in.ExpectedRevision {
			return zero, ErrConflict
		}
		snapshot, err := backupRemoteTargetSnapshot(ctx, tx, target, revision)
		if err != nil {
			return zero, err
		}
		var server, runner string
		if err = tx.QueryRow(ctx, `SELECT d.server_id,s.runner_id FROM destinations d JOIN servers s ON s.id=d.server_id WHERE d.id=$1`, in.DestinationID).Scan(&server, &runner); err != nil {
			return zero, hostingError(err)
		}
		now := time.Now().UTC()
		limits := s.databaseBackupPolicy()
		view := domain.HostingDatabaseBackupTransfer{ID: id, Version: domain.HostingDatabaseBackupTransferVersion, ProjectID: project, TargetID: target, TargetRevision: revision, DestinationID: in.DestinationID, Mode: "validate", State: "queued", CreatedAt: now, UpdatedAt: now}
		p := domain.HostingDatabaseBackupTransferPlan{Operation: view, Target: snapshot, ActorID: actor, RunnerID: runner, ServerID: server, MaxBytes: limits.MaxBytes, TimeoutSeconds: limits.TimeoutSeconds}
		p.Hash = domain.HostingDocumentHash(p)
		if err = backupRemoteCurrent(ctx, tx, p); err != nil {
			return zero, err
		}
		if err = s.backupRemoteInsert(ctx, tx, p, in.RequestID, hash, nil); err != nil {
			return zero, err
		}
		return view, nil
	})
}

func (s *PostgresStore) GetDatabaseBackupTransfer(ctx context.Context, actor, id string) (domain.HostingDatabaseBackupTransfer, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupTransfer, error) {
		r, err := backupRemoteLoad(ctx, tx, id, false)
		if err != nil {
			return r.view, err
		}
		if err = proxyAuthorize(ctx, tx, actor, r.view.ProjectID, domain.RoleViewer, false); err != nil {
			return domain.HostingDatabaseBackupTransfer{}, err
		}
		if err = databaseBindingGrant(ctx, sqlcgen.New(tx), r.view.ProjectID, r.view.DestinationID); err != nil {
			return domain.HostingDatabaseBackupTransfer{}, err
		}
		return r.view, proxyCheckLegacyToken(ctx, tx, actor)
	})
}
func (s *PostgresStore) ListDatabaseBackupTransfers(ctx context.Context, actor, id string, validation bool) ([]domain.HostingDatabaseBackupTransfer, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) ([]domain.HostingDatabaseBackupTransfer, error) {
		var project string
		query := `SELECT project_id FROM hosting_database_backups WHERE id=$1`
		if validation {
			query = `SELECT project_id FROM hosting_database_backup_targets WHERE id=$1`
		}
		if err := tx.QueryRow(ctx, query, id).Scan(&project); err != nil {
			return nil, hostingError(err)
		}
		if err := proxyAuthorize(ctx, tx, actor, project, domain.RoleViewer, false); err != nil {
			return nil, err
		}
		query = `SELECT id FROM hosting_database_backup_transfers WHERE backup_id=$1 ORDER BY created_at DESC,id DESC LIMIT 100`
		if validation {
			query = `SELECT id FROM hosting_database_backup_transfers WHERE target_id=$1 AND mode='validate' ORDER BY created_at DESC,id DESC LIMIT 100`
		}
		rows, err := tx.Query(ctx, query, id)
		if err != nil {
			return nil, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if rows.Err() != nil {
			return nil, rows.Err()
		}
		out := make([]domain.HostingDatabaseBackupTransfer, 0, len(ids))
		for _, id := range ids {
			r, e := backupRemoteLoad(ctx, tx, id, false)
			if e != nil {
				return nil, e
			}
			if e = databaseBindingGrant(ctx, sqlcgen.New(tx), project, r.view.DestinationID); e != nil {
				return nil, e
			}
			out = append(out, r.view)
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}

func (s *PostgresStore) PrepareDatabaseBackupTransfer(ctx context.Context, id string) error {
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		r, err := backupRemoteLoad(ctx, tx, id, true)
		if err != nil {
			return false, err
		}
		if r.view.State != "queued" {
			return true, nil
		}
		if err = backupRemoteCurrent(ctx, tx, r.plan); err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, auth.ErrForbidden) || errors.Is(err, auth.ErrUnauthenticated) {
				_, e := tx.Exec(ctx, `UPDATE hosting_database_backup_transfers SET state='failed',failure_code='authority_denied',updated_at=clock_timestamp() WHERE id=$1`, id)
				return e == nil, e
			}
			return false, err
		}
		run := domain.TaskRun{ID: "db_transfer_" + id, ProjectID: r.view.ProjectID, RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseBackupTransfer, HostingOperationID: id, HostingMode: r.view.Mode, Inputs: map[string]any{}}}
		if domain.ValidateDatabaseBackupTransferTask(run) != nil {
			return false, ErrConflict
		}
		if _, err = sqlcgen.New(tx).CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: run.ID, ProjectID: run.ProjectID, RunSpec: buildJSON(run.RunSpec), RequestedBy: r.plan.ActorID}); err != nil {
			return false, err
		}
		_, err = tx.Exec(ctx, `UPDATE hosting_database_backup_transfers SET state='dispatched',run_id=$2,updated_at=clock_timestamp() WHERE id=$1`, id, run.ID)
		return err == nil, err
	})
	return err
}

func backupRemoteAuthority(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, live bool) (backupRemoteRow, error) {
	var id string
	err := tx.QueryRow(ctx, `SELECT b.id FROM hosting_database_backup_transfers b JOIN task_runs t ON t.id=b.run_id JOIN run_leases l ON l.run_id=t.id WHERE b.id=$1 AND b.run_id=$2 AND b.runner_id=$3 AND l.id=$4 AND l.runner_id=$3 AND l.attempt=$5 AND l.fence=$6 AND (NOT $7 OR (l.status='active' AND l.expires_at>clock_timestamp())) AND t.run_spec=jsonb_build_object('type','database_backup_transfer','hosting_operation_id',b.id,'hosting_mode',b.mode,'inputs','{}'::jsonb) FOR UPDATE OF l`, a.OperationID, a.RunID, runner, a.LeaseID, a.Attempt, a.Fence, live).Scan(&id)
	if err != nil {
		return backupRemoteRow{}, hostingError(err)
	}
	return backupRemoteLoad(ctx, tx, id, true)
}
func (s *PostgresStore) DatabaseBackupTransferPlan(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingDatabaseBackupTransferPlan, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupTransferPlan, error) {
		r, err := backupRemoteAuthority(ctx, tx, runner, a, true)
		if err != nil {
			return domain.HostingDatabaseBackupTransferPlan{}, err
		}
		if r.view.State != "dispatched" && r.view.State != "applying" {
			return domain.HostingDatabaseBackupTransferPlan{}, ErrConflict
		}
		return r.plan, backupRemoteCurrent(ctx, tx, r.plan)
	})
}

func (s *PostgresStore) DatabaseBackupTransferMutation(ctx context.Context, runner string, in domain.HostingDatabaseBackupTransferMutation) (domain.HostingDatabaseBackupTransfer, error) {
	if in.Validate() != nil {
		return domain.HostingDatabaseBackupTransfer{}, domain.ErrInvalidDatabaseBackupRemote
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupTransfer, error) {
		var zero domain.HostingDatabaseBackupTransfer
		r, err := backupRemoteAuthority(ctx, tx, runner, in.HostingAuthority, false)
		if err != nil {
			return zero, err
		}
		var hash string
		var response []byte
		err = tx.QueryRow(ctx, `SELECT body_hash,response FROM hosting_database_backup_transfer_receipts WHERE operation_id=$1 AND phase=$2`, r.view.ID, in.Phase).Scan(&hash, &response)
		if err == nil {
			if hash != domain.HostingDocumentHash(in) || json.Unmarshal(response, &zero) != nil {
				return zero, ErrConflict
			}
			return zero, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		expired := false
		if _, err = backupRemoteAuthority(ctx, tx, runner, in.HostingAuthority, true); err != nil {
			if !backupRemoteExpiredUploadClassification(ctx, tx, runner, in, r) {
				return zero, err
			}
			expired = true
		}
		if !expired {
			if err = backupRemoteCurrent(ctx, tx, r.plan); err != nil {
				return zero, err
			}
		}
		if in.PlanHash != r.plan.Hash || r.daemon != "" && r.daemon != in.DaemonID {
			return zero, ErrConflict
		}
		state := r.view.State
		switch in.Phase {
		case "intent":
			if state != "dispatched" || in.FailureCode != "" {
				return zero, ErrConflict
			}
			state = "applying"
		case "complete":
			if state != "applying" || in.FailureCode != "" {
				return zero, ErrConflict
			}
			state = "succeeded"
		case "failed":
			if state != "dispatched" && state != "applying" || in.FailureCode == "" {
				return zero, ErrConflict
			}
			state = "failed"
		case "uncertain":
			if state != "applying" || in.FailureCode == "" {
				return zero, ErrConflict
			}
			state = "uncertain"
		}
		if _, err = tx.Exec(ctx, `UPDATE hosting_database_backup_transfers SET state=$2,failure_code=$3,daemon_id=$4,updated_at=clock_timestamp() WHERE id=$1`, r.view.ID, state, in.FailureCode, in.DaemonID); err != nil {
			return zero, err
		}
		if in.Phase != "intent" && !expired {
			status := "failed"
			if state == "succeeded" {
				status = "succeeded"
			}
			if _, err = tx.Exec(ctx, `UPDATE task_runs SET status=$2,finished_at=clock_timestamp() WHERE id=$1`, in.RunID, status); err != nil {
				return zero, err
			}
			if _, err = tx.Exec(ctx, `UPDATE run_leases SET status=$2,completed_at=clock_timestamp(),completion_key=$3 WHERE id=$1 AND status='active'`, in.LeaseID, status, in.ReceiptID); err != nil {
				return zero, err
			}
		}
		updated, err := backupRemoteLoad(ctx, tx, r.view.ID, false)
		if err != nil {
			return zero, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO hosting_database_backup_transfer_receipts(operation_id,phase,receipt_id,authority,body,body_hash,response,expired_classification) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, r.view.ID, in.Phase, in.ReceiptID, buildJSON(in.HostingAuthority), buildJSON(in), domain.HostingDocumentHash(in), buildJSON(updated.view), expired)
		if err != nil {
			return zero, err
		}
		if expired {
			return updated.view, nil
		}
		if err = backupRemoteCurrent(ctx, tx, r.plan); err != nil {
			return zero, err
		}
		clock, err := sqlcgen.New(tx).CheckStorageArchiveAuthorityClock(ctx, sqlcgen.CheckStorageArchiveAuthorityClockParams{ActorID: r.plan.ActorID, LeaseID: in.LeaseID, RequireActive: in.Phase == "intent"})
		if err != nil {
			return zero, err
		}
		if !clock.ActorValid || !clock.LeaseValid {
			return zero, ErrConflict
		}
		return updated.view, nil
	})
}

// This is an acknowledgment of uncertainty, never expired execution authority.
// The lease and transfer rows are already locked by backupRemoteAuthority.
func backupRemoteExpiredUploadClassification(ctx context.Context, tx pgx.Tx, runner string, in domain.HostingDatabaseBackupTransferMutation, r backupRemoteRow) bool {
	if in.Phase != "uncertain" || in.FailureCode != "interrupted" || r.view.Mode != "upload" || r.view.State != "applying" || in.PlanHash != r.plan.Hash || r.daemon != in.DaemonID {
		return false
	}
	var current string
	if tx.QueryRow(ctx, `SELECT r.id FROM runners r JOIN runner_enrollments e ON e.runner_id=r.id WHERE r.id=$1 AND r.status='active' AND r.token_hash<>'' AND e.credential_hash=r.token_hash AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND 'hosting_postgresql_remote_backup_v1'=ANY(r.capabilities) FOR SHARE OF r,e`, runner).Scan(&current) != nil {
		return false
	}
	var allowed bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM run_leases l JOIN runners r ON r.id=l.runner_id JOIN runner_enrollments e ON e.runner_id=r.id JOIN hosting_database_backup_transfer_receipts i ON i.operation_id=$1 AND i.phase='intent' WHERE l.id=$2 AND l.run_id=$3 AND l.runner_id=$4 AND l.attempt=$5 AND l.fence=$6 AND l.expires_at<=clock_timestamp() AND l.status IN ('active','expired') AND r.status='active' AND r.token_hash<>'' AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND 'hosting_postgresql_remote_backup_v1'=ANY(r.capabilities) AND i.authority=$7 AND i.body->>'plan_hash'=$8 AND i.body->>'daemon_id'=$9 AND NOT EXISTS(SELECT 1 FROM run_leases newer WHERE newer.run_id=l.run_id AND newer.attempt>l.attempt))`, in.OperationID, in.LeaseID, in.RunID, runner, in.Attempt, in.Fence, buildJSON(in.HostingAuthority), in.PlanHash, in.DaemonID).Scan(&allowed)
	return err == nil && allowed
}

func (h deploymentOnlyHandler) PrepareDatabaseBackupTransfer(ctx context.Context, id string) error {
	return h.store.PrepareDatabaseBackupTransfer(ctx, id)
}

func (s *PostgresStore) recoverDatabaseBackupTransfers(ctx context.Context) error {
	if !s.hostingJobs.DatabaseBackupTransferEnabled() {
		return nil
	}
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		rows, err := tx.Query(ctx, `SELECT id FROM hosting_database_backup_transfers WHERE state='queued' ORDER BY created_at,id LIMIT 100`)
		if err != nil {
			return false, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return false, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if rows.Err() != nil {
			return false, rows.Err()
		}
		for _, id := range ids {
			if _, err = s.hostingJobs.EnqueueDatabaseBackupTransferTx(ctx, tx, hostingjobs.DatabaseBackupTransferArgs{OperationID: id}); err != nil {
				return false, err
			}
		}
		return true, nil
	})
	return err
}

func (s *PostgresStore) databaseBackupTransferClaimAllowed(ctx context.Context, tx pgx.Tx, run domain.TaskRun, runner string) (bool, error) {
	if domain.ValidateDatabaseBackupTransferTask(run) != nil {
		return false, nil
	}
	r, err := backupRemoteLoad(ctx, tx, run.RunSpec.HostingOperationID, false)
	if err != nil {
		return false, err
	}
	if r.run == nil || *r.run != run.ID || r.plan.Operation.ProjectID != run.ProjectID || r.plan.RunnerID != runner || r.view.Mode != run.RunSpec.HostingMode || r.view.State != "dispatched" {
		return false, nil
	}
	err = backupRemoteCurrent(ctx, tx, r.plan)
	return err == nil, err
}

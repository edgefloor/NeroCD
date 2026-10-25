package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

// ConfigureDatabaseBackupLimits freezes protected policy before queue startup.
func (s *PostgresStore) ConfigureDatabaseBackupLimits(l domain.HostingDatabaseBackupLimits) error {
	if s.hostingJobs != nil {
		return ErrConflict
	}
	if l.Validate() != nil {
		return domain.ErrInvalidDatabaseBackup
	}
	s.databaseBackupLimits = &l
	return nil
}
func (s *PostgresStore) databaseBackupPolicy() domain.HostingDatabaseBackupLimits {
	if s.databaseBackupLimits != nil {
		return *s.databaseBackupLimits
	}
	return domain.DefaultHostingDatabaseBackupLimits()
}
func databaseBackupView(r sqlcgen.HostingDatabaseBackup) (domain.HostingDatabaseBackupOperation, error) {
	p, err := databaseBackupPlan(r)
	if err != nil {
		return domain.HostingDatabaseBackupOperation{}, err
	}
	v := domain.HostingDatabaseBackupOperation{MaxBytes: p.MaxBytes, TimeoutSeconds: p.TimeoutSeconds, ID: r.ID, Version: domain.HostingDatabaseBackupVersion, ProjectID: r.ProjectID, SourceResourceID: r.SourceResourceID, SourceGeneration: r.SourceGeneration, BackupID: r.BackupID, Mode: r.Mode, State: r.State, TargetResourceID: proxyString(r.TargetResourceID), Consistency: "logical_snapshot_source_writable", FailureCode: r.FailureCode, OwnershipHeld: r.OwnershipHeld, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
	if r.Blob != nil {
		var b domain.HostingDatabaseBackupBlob
		if json.Unmarshal(r.Blob, &b) != nil || b.Validate() != nil {
			return v, ErrConflict
		}
		v.Blob = &b
	}
	return v, nil
}
func databaseBackupPlan(r sqlcgen.HostingDatabaseBackup) (domain.HostingDatabaseBackupPlan, error) {
	var p domain.HostingDatabaseBackupPlan
	if json.Unmarshal(r.Plan, &p) != nil || p.Hash != r.PlanHash || p.Operation.ID != r.ID || p.Operation.ProjectID != r.ProjectID || p.ActorID != r.CreatedBy || p.RunnerID != r.RunnerID || p.Operation.SourceResourceID != r.SourceResourceID || p.Operation.BackupID != r.BackupID || p.Operation.TargetResourceID != proxyString(r.TargetResourceID) {
		return p, ErrConflict
	}
	return p, nil
}
func databaseBackupSchema(ctx context.Context, q *sqlcgen.Queries) error {
	ok, err := q.DatabaseBackupSchemaPresent(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return ErrHostingDatabaseUnavailable
	}
	return nil
}
func databaseBackupAgent(ctx context.Context, tx pgx.Tx, p domain.HostingDatabaseBackupPlan) error {
	for _, capability := range []string{domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability} {
		ok, err := sqlcgen.New(tx).HostingRunnerHasReleasePorts(ctx, sqlcgen.HostingRunnerHasReleasePortsParams{RunnerID: p.RunnerID, Capability: capability})
		if err != nil {
			return err
		}
		if !ok {
			return ErrConflict
		}
	}
	return nil
}

// Locks placement, current owner/grant, exact healthy source and credential refs.
// This does not stop client writes and never extends historical lease authority.
func databaseBackupSource(ctx context.Context, tx pgx.Tx, actor, resource string) (domain.HostingDatabase, domain.HostingHealthy, domain.HostingOperation, error) {
	var v domain.HostingDatabase
	var h domain.HostingHealthy
	var op domain.HostingOperation
	if err := lockHostingResourceDestination(ctx, tx, resource); err != nil {
		return v, h, op, err
	}
	q := sqlcgen.New(tx)
	target, err := q.LockHostingResourceTarget(ctx, resource)
	if err != nil {
		return v, h, op, hostingError(err)
	}
	if err = proxyAuthorize(ctx, tx, actor, target.ProjectID, domain.RoleOwner, false); err != nil {
		return v, h, op, err
	}
	if err = databaseBindingGrant(ctx, q, target.ProjectID, target.DestinationID); err != nil {
		return v, h, op, err
	}
	v, err = hostingDatabaseLoad(ctx, q, resource)
	if err != nil {
		return v, h, op, err
	}
	if v.Engine != "postgresql" || v.RecipeVersion != domain.HostingDatabasePostgresRecipe {
		return v, h, op, ErrConflict
	}
	op, h, err = databaseUpgradeHealthy(ctx, tx, resource, target.BindingID)
	if err != nil {
		return v, h, op, err
	}
	if target.Generation != h.Snapshot.ResourceGeneration {
		return v, h, op, ErrConflict
	}
	if err = databaseOperationAuthorityMode(ctx, tx, op, false, true); err != nil {
		return v, h, op, err
	}
	return v, h, op, nil
}

// Restore authorizes the artifact's immutable provenance and current placement,
// not the health or credential versions of the original source container.
func databaseBackupRestoreAuthority(ctx context.Context, tx pgx.Tx, actor string, p domain.HostingDatabaseBackupPlan) error {
	if err := lockHostingResourceDestination(ctx, tx, p.Operation.SourceResourceID); err != nil {
		return err
	}
	q := sqlcgen.New(tx)
	target, err := q.LockHostingResourceTarget(ctx, p.Operation.SourceResourceID)
	if err != nil {
		return hostingError(err)
	}
	snap := p.Source.Snapshot
	if target.ProjectID != p.Operation.ProjectID || target.BindingID != snap.BindingID || target.DestinationID != snap.DestinationID || target.ServerID != snap.ServerID || target.RunnerID != p.RunnerID {
		return ErrConflict
	}
	if err = proxyAuthorize(ctx, tx, actor, target.ProjectID, domain.RoleOwner, false); err != nil {
		return err
	}
	if err = databaseBindingGrant(ctx, q, target.ProjectID, target.DestinationID); err != nil {
		return err
	}
	_, err = q.LockHostingExactRunner(ctx, sqlcgen.LockHostingExactRunnerParams{ID: target.BindingID, ResourceID: p.Operation.SourceResourceID, ID_2: target.DestinationID, ID_3: target.ServerID, ID_4: target.RunnerID, ProjectID: target.ProjectID, Capability: domain.HostingDatabaseBackupCapability})
	if err != nil {
		return hostingError(err)
	}
	return proxyCheckLegacyToken(ctx, tx, actor)
}
func databaseBackupCurrent(ctx context.Context, tx pgx.Tx, r sqlcgen.HostingDatabaseBackup) (domain.HostingDatabaseBackupPlan, error) {
	p, err := databaseBackupPlan(r)
	if err != nil {
		return p, err
	}
	if r.Mode == "restore" {
		if err = databaseBackupRestoreAuthority(ctx, tx, r.CreatedBy, p); err != nil {
			return p, err
		}
	} else {
		v, h, op, err := databaseBackupSource(ctx, tx, r.CreatedBy, r.SourceResourceID)
		if err != nil {
			return p, err
		}
		if err = databaseBackupSourceRole(ctx, tx, h.Snapshot.BindingID); err != nil {
			return p, err
		}
		if domain.HostingDocumentHash(h) != domain.HostingDocumentHash(p.Source) || op.Plan == nil || op.Plan.Hash != p.SourcePlan.Hash || domain.HostingDocumentHash(v.Credentials) != domain.HostingDocumentHash(p.Credentials) {
			return p, ErrConflict
		}
	}
	if err = databaseBackupAgent(ctx, tx, p); err != nil {
		return p, err
	}
	if r.Mode == "restore" && p.Target != nil {
		_, th, to, err := databaseBackupSource(ctx, tx, r.CreatedBy, *r.TargetResourceID)
		if err != nil {
			return p, err
		}
		if domain.HostingDocumentHash(th) != domain.HostingDocumentHash(p.Target) || to.ID != proxyString(r.DeploymentOperationID) || p.TargetPlan == nil || to.Plan.Hash != p.TargetPlan.Hash {
			return p, ErrConflict
		}
	}
	return p, proxyCheckLegacyToken(ctx, tx, r.CreatedBy)
}

// The caller already owns the destination admission lock. Restore insertion
// takes that same lock at the database boundary, so this read cannot race a
// competing target ownership admission. The exact restore may inspect its own
// target; a separate live backup may not use it until import settles.
func databaseBackupSourceRole(ctx context.Context, tx pgx.Tx, binding string) error {
	var held bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hosting_database_backups WHERE target_binding_id=$1 AND ownership_held)`, binding).Scan(&held); err != nil {
		return err
	}
	if held {
		return ErrConflict
	}
	return nil
}
func databaseBackupFinalizeActor(ctx context.Context, tx pgx.Tx, actor string) error {
	if _, err := tx.Exec(ctx, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
		return err
	}
	return proxyCheckLegacyToken(ctx, tx, actor)
}
func databaseBackupAudit(ctx context.Context, tx pgx.Tx, actor, id, phase, project string) error {
	return createAuditWithQueries(ctx, sqlcgen.New(tx), domain.AuditEvent{ID: "aud_dbbackup_" + phase + "_" + id, ActorID: actor, Action: "hosting.database_backup." + phase, TargetID: id, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"project_id": project}})
}
func (s *PostgresStore) RequestDatabaseBackup(ctx context.Context, id, resource, actor string, in domain.HostingDatabaseBackupInput, _ domain.AuditEvent) (domain.HostingDatabaseBackupOperation, error) {
	if in.Validate() != nil {
		return domain.HostingDatabaseBackupOperation{}, domain.ErrInvalidDatabaseBackup
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupOperation, error) {
		return s.requestDatabaseBackupTx(ctx, tx, id, resource, actor, in)
	})
}

// requestDatabaseBackupTx shares the exact immediate admission and River enqueue
// with schedule occurrences; its caller owns the surrounding transaction.
func (s *PostgresStore) requestDatabaseBackupTx(ctx context.Context, tx pgx.Tx, id, resource, actor string, in domain.HostingDatabaseBackupInput) (domain.HostingDatabaseBackupOperation, error) {
	var zero domain.HostingDatabaseBackupOperation
	q := sqlcgen.New(tx)
	if s.hostingJobs == nil || !s.hostingJobs.DatabaseBackupEnabled() {
		return zero, hostingjobs.ErrUnavailable
	}
	if err := databaseBackupSchema(ctx, q); err != nil {
		return zero, err
	}
	v, h, op, err := databaseBackupSource(ctx, tx, actor, resource)
	if err != nil {
		return zero, err
	}
	hash := domain.HostingDocumentHash(struct {
		Actor string
		Input domain.HostingDatabaseBackupInput
	}{actor, in})
	var existing string
	err = tx.QueryRow(ctx, `SELECT id FROM hosting_database_backups WHERE source_resource_id=$1 AND mode='backup' AND request_id=$2`, resource, in.RequestID).Scan(&existing)
	if err == nil {
		r, e := q.ReadDatabaseBackup(ctx, existing)
		if e != nil {
			return zero, e
		}
		if r.RequestHash != hash || r.CreatedBy != actor {
			return zero, ErrConflict
		}
		return databaseBackupView(r)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	if in.ExpectedResourceGeneration != v.Resource.Generation {
		return zero, ErrConflict
	}
	if err = databaseBackupSourceRole(ctx, tx, h.Snapshot.BindingID); err != nil {
		return zero, err
	}
	state, e := q.LockHostingAdmissionState(ctx, h.Snapshot.BindingID)
	if e != nil {
		return zero, e
	}
	if state.HoldOperationID != nil || !state.RestoreAdmitted {
		return zero, ErrConflict
	}
	limits := s.databaseBackupPolicy()
	now := time.Now().UTC()
	view := domain.HostingDatabaseBackupOperation{MaxBytes: limits.MaxBytes, TimeoutSeconds: limits.TimeoutSeconds, ID: id, Version: domain.HostingDatabaseBackupVersion, ProjectID: v.Resource.ProjectID, SourceResourceID: resource, SourceGeneration: in.ExpectedResourceGeneration, BackupID: id, Mode: "backup", State: "queued", Consistency: "logical_snapshot_source_writable", OwnershipHeld: true, CreatedAt: now, UpdatedAt: now}
	p := domain.HostingDatabaseBackupPlan{Operation: view, ActorID: actor, RunnerID: h.Snapshot.RunnerID, Source: h, SourcePlan: *op.Plan, Credentials: v.Credentials, LocationID: id, MaxBytes: limits.MaxBytes, TimeoutSeconds: limits.TimeoutSeconds}
	storageRow, e := q.ReadHostingStorage(ctx, resource)
	if e != nil {
		return zero, e
	}
	p.SourceStorage, e = hostingStorageFromSQL(storageRow)
	if e != nil {
		return zero, e
	}
	p.Hash = domain.HostingDocumentHash(p)
	if p.Validate() != nil {
		return zero, ErrConflict
	}
	if err = databaseBackupAgent(ctx, tx, p); err != nil {
		return zero, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO hosting_database_backups(id,project_id,source_resource_id,source_binding_id,source_generation,backup_id,mode,request_id,request_hash,created_by,state,runner_id,plan,plan_hash) VALUES($1,$2,$3,$4,$5,$1,'backup',$6,$7,$8,'queued',$9,$10,$11)`, id, view.ProjectID, resource, h.Snapshot.BindingID, view.SourceGeneration, in.RequestID, hash, actor, p.RunnerID, buildJSON(p), p.Hash)
	if err != nil {
		return zero, hostingError(err)
	}
	if err = databaseBackupAudit(ctx, tx, actor, id, "requested", view.ProjectID); err != nil {
		return zero, err
	}
	if _, err = s.hostingJobs.EnqueueDatabaseBackupTx(ctx, tx, hostingjobs.DatabaseBackupArgs{OperationID: id}); err != nil {
		return zero, err
	}
	saved, err := q.ReadDatabaseBackup(ctx, id)
	if err != nil {
		return zero, err
	}
	if _, err = databaseBackupCurrent(ctx, tx, saved); err != nil {
		return zero, err
	}
	return view, databaseBackupFinalizeActor(ctx, tx, actor)
}

func (s *PostgresStore) GetDatabaseBackup(ctx context.Context, actor, id string) (domain.HostingDatabaseBackupOperation, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupOperation, error) {
		q := sqlcgen.New(tx)
		r, err := q.ReadDatabaseBackup(ctx, id)
		if err != nil {
			return domain.HostingDatabaseBackupOperation{}, hostingError(err)
		}
		if err = proxyAuthorize(ctx, tx, actor, r.ProjectID, domain.RoleViewer, false); err != nil {
			return domain.HostingDatabaseBackupOperation{}, err
		}
		if err = databaseBindingGrant(ctx, q, r.ProjectID, databaseBackupDestination(r)); err != nil {
			return domain.HostingDatabaseBackupOperation{}, err
		}
		v, err := databaseBackupView(r)
		if err != nil {
			return v, err
		}
		return v, proxyCheckLegacyToken(ctx, tx, actor)
	})
}
func databaseBackupDestination(r sqlcgen.HostingDatabaseBackup) string {
	p, _ := databaseBackupPlan(r)
	return p.Source.Snapshot.DestinationID
}
func (s *PostgresStore) ListDatabaseBackups(ctx context.Context, actor, resource string) ([]domain.HostingDatabaseBackupOperation, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) ([]domain.HostingDatabaseBackupOperation, error) {
		q := sqlcgen.New(tx)
		r, err := q.GetResource(ctx, resource)
		if err != nil {
			return nil, hostingError(err)
		}
		if err = proxyAuthorize(ctx, tx, actor, r.ProjectID, domain.RoleViewer, false); err != nil {
			return nil, err
		}
		v, err := hostingDatabaseLoad(ctx, q, resource)
		if err != nil {
			return nil, err
		}
		if err = databaseBindingGrant(ctx, q, r.ProjectID, v.Binding.DestinationID); err != nil {
			return nil, err
		}
		rows, err := q.ListDatabaseBackups(ctx, resource)
		if err != nil {
			return nil, err
		}
		out := make([]domain.HostingDatabaseBackupOperation, 0, len(rows))
		for _, row := range rows {
			v, e := databaseBackupView(row)
			if e != nil {
				return nil, e
			}
			out = append(out, v)
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}
func (s *PostgresStore) RequestDatabaseBackupRestore(ctx context.Context, id, backup, actor string, in domain.HostingDatabaseBackupRestoreInput, create domain.HostingDatabaseCreateInput, records []HostingDatabaseCredentialRecord, event domain.AuditEvent) (domain.HostingDatabaseBackupOperation, error) {
	if in.Validate() != nil || create.Validate() != nil {
		return domain.HostingDatabaseBackupOperation{}, domain.ErrInvalidDatabaseBackup
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupOperation, error) {
		var zero domain.HostingDatabaseBackupOperation
		q := sqlcgen.New(tx)
		original, err := q.ReadDatabaseBackup(ctx, backup)
		if err != nil {
			return zero, hostingError(err)
		}
		p, err := databaseBackupPlan(original)
		if err != nil {
			return zero, err
		}
		if err = proxyAuthorize(ctx, tx, actor, original.ProjectID, domain.RoleOwner, false); err != nil {
			return zero, err
		}
		if p.Validate() != nil {
			return zero, ErrConflict
		}
		if err = databaseBackupRestoreAuthority(ctx, tx, actor, p); err != nil {
			return zero, err
		}
		if original.Mode != "backup" || original.State != "succeeded" || original.Blob == nil {
			return zero, ErrConflict
		}
		hash := domain.HostingDocumentHash(struct {
			Actor, Backup string
			Input         domain.HostingDatabaseBackupRestoreInput
		}{actor, backup, in})
		prior, err := q.FindDatabaseBackupRequest(ctx, sqlcgen.FindDatabaseBackupRequestParams{SourceResourceID: original.SourceResourceID, Mode: "restore", BackupID: backup, RequestID: in.RequestID})
		if err == nil {
			if prior.CreatedBy != actor || prior.RequestHash != hash {
				return zero, ErrConflict
			}
			return databaseBackupView(prior)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		source, err := hostingDatabaseLoad(ctx, q, original.SourceResourceID)
		if err != nil {
			return zero, err
		}
		if create.ProjectID != source.Resource.ProjectID || create.ProjectEnvironmentID != source.Resource.ProjectEnvironmentID || create.DestinationID != source.Binding.DestinationID || create.Name != in.Name || create.Engine != "postgresql" || create.RecipeVersion != domain.HostingDatabasePostgresRecipe {
			return zero, ErrConflict
		}
		target, err := s.createHostingDatabaseTx(ctx, tx, actor, create, records, event)
		if err != nil {
			return zero, err
		}
		child := "restore_deploy_" + id
		_, err = s.requestHostingDatabaseDeploymentTx(ctx, tx, child, target.Resource.ID, actor, domain.HostingDatabaseDeploymentInput{IdempotencyKey: "restore_" + id, ExpectedGeneration: target.Resource.Generation}, domain.AuditEvent{ID: "aud_restore_deploy_" + id, CreatedAt: time.Now().UTC()})
		if err != nil {
			return zero, err
		}
		limits := s.databaseBackupPolicy()
		now := time.Now().UTC()
		view := domain.HostingDatabaseBackupOperation{MaxBytes: limits.MaxBytes, TimeoutSeconds: limits.TimeoutSeconds, ID: id, Version: domain.HostingDatabaseBackupVersion, ProjectID: original.ProjectID, SourceResourceID: original.SourceResourceID, SourceGeneration: original.SourceGeneration, BackupID: backup, Mode: "restore", State: "waiting_target", TargetResourceID: target.Resource.ID, Consistency: "logical_snapshot_source_writable", OwnershipHeld: true, CreatedAt: now, UpdatedAt: now}
		var blob domain.HostingDatabaseBackupBlob
		if json.Unmarshal(original.Blob, &blob) != nil || blob.Validate() != nil {
			return zero, ErrConflict
		}
		view.Blob = &blob
		p.Operation = view
		p.MaxBytes = limits.MaxBytes
		p.TimeoutSeconds = limits.TimeoutSeconds
		if blob.Bytes > p.MaxBytes {
			return zero, ErrConflict
		}
		p.ActorID = actor
		p.Hash = ""
		p.Hash = domain.HostingDocumentHash(p)
		_, err = tx.Exec(ctx, `INSERT INTO hosting_database_backups(id,project_id,source_resource_id,source_binding_id,source_generation,backup_id,mode,request_id,request_hash,created_by,state,target_resource_id,target_binding_id,deployment_operation_id,runner_id,plan,plan_hash,blob) VALUES($1,$2,$3,$4,$5,$6,'restore',$7,$8,$9,'waiting_target',$10,$11,$12,$13,$14,$15,$16)`, id, view.ProjectID, view.SourceResourceID, original.SourceBindingID, view.SourceGeneration, backup, in.RequestID, hash, actor, target.Resource.ID, target.Binding.ID, child, p.RunnerID, buildJSON(p), p.Hash, original.Blob)
		if err != nil {
			return zero, hostingError(err)
		}
		if err = databaseBackupAudit(ctx, tx, actor, id, "requested", view.ProjectID); err != nil {
			return zero, err
		}
		if _, err = s.hostingJobs.EnqueueDatabaseBackupTx(ctx, tx, hostingjobs.DatabaseBackupArgs{OperationID: id}); err != nil {
			return zero, err
		}
		return view, databaseBackupFinalizeActor(ctx, tx, actor)
	})
}

func (s *PostgresStore) PrepareDatabaseBackup(ctx context.Context, id string) error {
	waiting := false
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		q := sqlcgen.New(tx)
		r, err := q.LockDatabaseBackup(ctx, id)
		if err != nil {
			return false, hostingError(err)
		}
		if r.State != "queued" && r.State != "waiting_target" {
			return true, nil
		}
		p, err := databaseBackupCurrent(ctx, tx, r)
		if err != nil {
			return false, err
		}
		if r.Mode == "restore" {
			child, err := hostingOperation(ctx, tx, *r.DeploymentOperationID)
			if err != nil {
				return false, err
			}
			if child.State != "succeeded" {
				if child.State == "failed" || child.State == "canceled" {
					_, err = tx.Exec(ctx, `UPDATE hosting_database_backups SET state='uncertain',failure_code='execution_failed',updated_at=clock_timestamp() WHERE id=$1`, id)
					return err == nil, err
				}
				waiting = true
				return true, nil
			}
			_, h, op, err := databaseBackupSource(ctx, tx, r.CreatedBy, *r.TargetResourceID)
			if err != nil {
				return false, err
			}
			storageRow, e := q.ReadHostingStorage(ctx, *r.TargetResourceID)
			if e != nil {
				return false, e
			}
			storage, e := hostingStorageFromSQL(storageRow)
			if e != nil {
				return false, e
			}
			p.TargetStorage = &storage
			p.Target = &h
			p.TargetPlan = op.Plan
			p.Hash = ""
			p.Hash = domain.HostingDocumentHash(p)
		}
		if p.Validate() != nil {
			return false, ErrConflict
		}
		run := domain.TaskRun{ID: "db_backup_" + id, ProjectID: r.ProjectID, RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseBackup, Inputs: map[string]any{}, HostingOperationID: id, HostingMode: r.Mode}}
		if domain.ValidateDatabaseBackupTask(run) != nil {
			return false, ErrConflict
		}
		if _, err = q.CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: run.ID, ProjectID: r.ProjectID, RunSpec: buildJSON(run.RunSpec), RequestedBy: r.CreatedBy}); err != nil {
			return false, err
		}
		_, err = tx.Exec(ctx, `UPDATE hosting_database_backups SET state='dispatched',run_id=$2,plan=$3,plan_hash=$4,updated_at=clock_timestamp() WHERE id=$1`, id, run.ID, buildJSON(p), p.Hash)
		if err != nil {
			return false, err
		}
		if err = databaseBackupAudit(ctx, tx, r.CreatedBy, id, "dispatched", r.ProjectID); err != nil {
			return false, err
		}
		return true, databaseBackupFinalizeActor(ctx, tx, r.CreatedBy)
	})
	if err == nil && waiting {
		return hostingjobs.ErrDatabaseBackupWaiting
	}
	return err
}
func (s *PostgresStore) databaseBackupClaimAllowed(ctx context.Context, tx pgx.Tx, run domain.TaskRun, runner string) (bool, error) {
	if domain.ValidateDatabaseBackupTask(run) != nil {
		return false, nil
	}
	r, err := sqlcgen.New(tx).ReadDatabaseBackupByRun(ctx, &run.ID)
	if err != nil {
		return false, err
	}
	if r.RunnerID != runner || r.ID != run.RunSpec.HostingOperationID || r.Mode != run.RunSpec.HostingMode || r.State != "dispatched" {
		return false, nil
	}
	p, err := databaseBackupCurrent(ctx, tx, r)
	return err == nil && p.Validate() == nil, err
}
func databaseBackupAuthority(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, live bool) (sqlcgen.HostingDatabaseBackup, error) {
	var r sqlcgen.HostingDatabaseBackup
	err := tx.QueryRow(ctx, `SELECT b.id FROM hosting_database_backups b JOIN task_runs t ON t.id=b.run_id JOIN run_leases l ON l.run_id=t.id WHERE b.id=$1 AND b.run_id=$2 AND b.runner_id=$3 AND l.id=$4 AND l.runner_id=$3 AND l.attempt=$5 AND l.fence=$6 AND (NOT $7 OR (l.status='active' AND l.expires_at>clock_timestamp())) AND t.run_spec=jsonb_build_object('type','database_backup','hosting_operation_id',b.id,'hosting_mode',b.mode,'inputs','{}'::jsonb) FOR UPDATE OF l`, a.OperationID, a.RunID, runner, a.LeaseID, a.Attempt, a.Fence, live).Scan(&r.ID)
	if err != nil {
		return r, hostingError(err)
	}
	return sqlcgen.New(tx).LockDatabaseBackup(ctx, r.ID)
}
func (s *PostgresStore) DatabaseBackupPlan(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingDatabaseBackupPlan, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupPlan, error) {
		r, err := databaseBackupAuthority(ctx, tx, runner, a, true)
		if err != nil {
			return domain.HostingDatabaseBackupPlan{}, err
		}
		p, err := databaseBackupCurrent(ctx, tx, r)
		if err != nil {
			return p, err
		}
		if !r.OwnershipHeld || p.Validate() != nil {
			return p, ErrConflict
		}
		return p, nil
	})
}
func (s *PostgresStore) DatabaseBackupMutation(ctx context.Context, runner string, in domain.HostingDatabaseBackupMutation) (domain.HostingDatabaseBackupOperation, error) {
	if in.Validate() != nil {
		return domain.HostingDatabaseBackupOperation{}, domain.ErrInvalidDatabaseBackup
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupOperation, error) {
		var zero domain.HostingDatabaseBackupOperation
		q := sqlcgen.New(tx)
		r, err := databaseBackupAuthority(ctx, tx, runner, in.HostingAuthority, false)
		if err != nil {
			return zero, err
		}
		old, err := q.ReadDatabaseBackupReceipt(ctx, sqlcgen.ReadDatabaseBackupReceiptParams{OperationID: r.ID, Phase: in.Phase})
		if err == nil {
			if old.BodyHash != domain.HostingDocumentHash(in) || json.Unmarshal(old.Response, &zero) != nil {
				return zero, ErrConflict
			}
			return zero, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if _, err = databaseBackupAuthority(ctx, tx, runner, in.HostingAuthority, true); err != nil {
			return zero, err
		}
		p, err := databaseBackupCurrent(ctx, tx, r)
		if err != nil {
			return zero, err
		}
		if in.PlanHash != p.Hash || !r.OwnershipHeld || in.Blob != nil && in.Blob.Bytes > p.MaxBytes {
			return zero, ErrConflict
		}
		state := r.State
		hold := true
		switch in.Phase {
		case "intent":
			if state != "dispatched" {
				return zero, ErrConflict
			}
			state = "applying"
		case "complete":
			if state != "applying" {
				return zero, ErrConflict
			}
			if r.Mode == "restore" && (r.Blob == nil || domain.HostingDocumentHash(in.Blob) != domain.HostingDocumentHash(p.Operation.Blob)) {
				return zero, ErrConflict
			}
			state = "succeeded"
			hold = false
		case "failed":
			if state != "dispatched" {
				return zero, ErrConflict
			}
			state = "failed"
			hold = r.Mode == "restore"
		case "uncertain":
			if state != "applying" {
				return zero, ErrConflict
			}
			state = "uncertain"
		}
		blob := r.Blob
		if in.Blob != nil {
			blob = buildJSON(in.Blob)
		}
		_, err = tx.Exec(ctx, `UPDATE hosting_database_backups SET state=$2,ownership_held=$3,blob=$4,failure_code=$5,updated_at=clock_timestamp() WHERE id=$1`, r.ID, state, hold, blob, in.FailureCode)
		if err != nil {
			return zero, err
		}
		if in.Phase != "intent" {
			status := "failed"
			if state == "succeeded" {
				status = "succeeded"
			}
			_, err = tx.Exec(ctx, `UPDATE task_runs SET status=$2,finished_at=clock_timestamp() WHERE id=$1`, in.RunID, status)
			if err != nil {
				return zero, err
			}
			_, err = tx.Exec(ctx, `UPDATE run_leases SET status=$3,completed_at=clock_timestamp(),completion_key=$2 WHERE id=$1 AND status='active'`, in.LeaseID, in.ReceiptID, status)
			if err != nil {
				return zero, err
			}
		}
		if err = databaseBackupAudit(ctx, tx, runner, r.ID, in.Phase, r.ProjectID); err != nil {
			return zero, err
		}
		updated, err := q.ReadDatabaseBackup(ctx, r.ID)
		if err != nil {
			return zero, err
		}
		view, err := databaseBackupView(updated)
		if err != nil {
			return zero, err
		}
		_, err = tx.Exec(ctx, `INSERT INTO hosting_database_backup_receipts(operation_id,phase,receipt_id,authority,body,body_hash,response) VALUES($1,$2,$3,$4,$5,$6,$7)`, r.ID, in.Phase, in.ReceiptID, buildJSON(in.HostingAuthority), buildJSON(in), domain.HostingDocumentHash(in), buildJSON(view))
		if err != nil {
			return zero, err
		}
		if _, err = databaseBackupCurrent(ctx, tx, updated); err != nil {
			return zero, err
		}
		if err = databaseBackupFinalizeActor(ctx, tx, r.CreatedBy); err != nil {
			return zero, err
		}
		clock, err := q.CheckStorageArchiveAuthorityClock(ctx, sqlcgen.CheckStorageArchiveAuthorityClockParams{ActorID: r.CreatedBy, LeaseID: in.LeaseID, RequireActive: in.Phase == "intent"})
		if err != nil {
			return zero, err
		}
		if !clock.ActorValid || !clock.LeaseValid {
			return zero, ErrConflict
		}
		return view, nil
	})
}

var _ HostingDatabaseBackupRepository = (*PostgresStore)(nil)

package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func externalImportResourceAuthority(ctx context.Context, tx pgx.Tx, actor, resource, role string) (sqlcgen.LockHostingResourceTargetRow, error) {
	var zero sqlcgen.LockHostingResourceTargetRow
	if err := lockHostingResourceDestination(ctx, tx, resource); err != nil {
		return zero, err
	}
	r, err := sqlcgen.New(tx).LockHostingResourceTarget(ctx, resource)
	if err != nil {
		return zero, hostingError(err)
	}
	if err = proxyAuthorize(ctx, tx, actor, r.ProjectID, role, false); err != nil {
		return zero, err
	}
	if err = databaseBindingGrant(ctx, sqlcgen.New(tx), r.ProjectID, r.DestinationID); err != nil {
		return zero, err
	}
	return r, proxyCheckLegacyToken(ctx, tx, actor)
}

func externalImportSourceAuthority(ctx context.Context, tx pgx.Tx, p domain.HostingDatabaseImportStagePlan) error {
	if p.Stage.Version == domain.HostingDatabaseImportUploadVersion {
		return p.Validate()
	}
	q := sqlcgen.New(tx)
	current, err := q.LockDatabaseBackupTargetAuthority(ctx, p.S3Target.Target.ID)
	if err != nil || !current.Enabled || current.ProjectID != p.ProjectID {
		return ErrConflict
	}
	revision, err := q.LockDatabaseBackupTargetRevision(ctx, sqlcgen.LockDatabaseBackupTargetRevisionParams{ID: p.S3Target.Target.ID, ProjectID: p.ProjectID})
	if err != nil || revision != p.S3Target.Target.Revision {
		return ErrConflict
	}
	snapshot, err := backupRemoteTargetSnapshot(ctx, tx, p.Stage.S3Source.TargetID, p.Stage.S3Source.Revision)
	if err != nil || domain.HostingDocumentHash(snapshot) != domain.HostingDocumentHash(p.S3Target) {
		return ErrConflict
	}
	for _, ref := range []domain.BackupRemoteCredentialRef{snapshot.Access, snapshot.Secret} {
		if _, err = backupRemoteCredentialRecord(ctx, tx, p.ProjectID, ref, true); err != nil {
			return err
		}
	}
	validated, err := q.DatabaseBackupRemoteTargetValidated(ctx, sqlcgen.DatabaseBackupRemoteTargetValidatedParams{TargetID: snapshot.Target.ID, TargetRevision: snapshot.Target.Revision, DestinationID: p.Target.Snapshot.DestinationID, RunnerID: p.RunnerID, DaemonID: p.Target.Release.DaemonID})
	if err != nil || !validated {
		return ErrConflict
	}
	return nil
}

func externalImportRunner(ctx context.Context, tx pgx.Tx, runner, mode string) error {
	capability, kind := domain.HostingDatabaseImportStageCapability, domain.RunTypeDatabaseImportStage
	if mode == "upload" {
		capability = domain.HostingDatabaseImportUploadCapability
	}
	if mode == "import" {
		capability, kind = domain.HostingDatabaseImportCapability, domain.RunTypeDatabaseImport
	}
	q := sqlcgen.New(tx)
	for _, cap := range []string{capability, domain.HostingStackCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingOwnedStorageCapability, domain.HostingDatabaseMySQLCapability} {
		if _, err := q.LockDatabaseBackupRemoteRunner(ctx, sqlcgen.LockDatabaseBackupRemoteRunnerParams{ID: runner, Column2: cap, Column3: kind}); err != nil {
			return hostingError(err)
		}
	}
	return nil
}

func externalImportStageView(raw []byte) (domain.HostingDatabaseImportStage, error) {
	var view domain.HostingDatabaseImportStage
	if json.Unmarshal(raw, &view) != nil || view.Validate() != nil {
		return view, ErrConflict
	}
	return view, nil
}

func externalImportView(raw []byte) (domain.HostingDatabaseImportOperation, error) {
	var view domain.HostingDatabaseImportOperation
	if json.Unmarshal(raw, &view) != nil || view.Validate() != nil {
		return view, ErrConflict
	}
	return view, nil
}

func externalImportAudit(ctx context.Context, tx pgx.Tx, actor, project, id, phase string) error {
	return createAuditWithQueries(ctx, sqlcgen.New(tx), domain.AuditEvent{ID: "aud_dbimport_" + phase + "_" + id, ActorID: actor, Action: "hosting.database_import." + phase, TargetID: id, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"project_id": project}})
}

// RequestDatabaseImportStage admits an inspection with no database mutation ownership.
func (s *PostgresStore) RequestDatabaseImportStage(ctx context.Context, id, resource, actor string, in domain.HostingDatabaseImportStageInput) (domain.HostingDatabaseImportStage, error) {
	if in.Validate() != nil {
		return domain.HostingDatabaseImportStage{}, domain.ErrInvalidDatabaseImport
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseImportStage, error) {
		var zero domain.HostingDatabaseImportStage
		r, err := externalImportResourceAuthority(ctx, tx, actor, resource, domain.RoleOwner)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		hash := domain.HostingDocumentHash(struct {
			Actor, Resource string
			Input           domain.HostingDatabaseImportStageInput
		}{actor, resource, in})
		prior, err := q.FindDatabaseImportStageRequest(ctx, sqlcgen.FindDatabaseImportStageRequestParams{TargetResourceID: resource, RequestID: in.RequestID})
		if err == nil {
			if prior.ActorID != actor || prior.RequestHash != hash {
				return zero, ErrConflict
			}
			return externalImportStageView(prior.Body)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		h, operation, generation, err := databaseBackupComponentSource(ctx, tx, actor, resource, in.Component, domain.RoleOwner)
		if err != nil || operation.Plan == nil {
			return zero, ErrConflict
		}
		if generation != in.ExpectedResourceGeneration || h.OperationID != in.ExpectedDeployedOperationID {
			return zero, ErrConflict
		}
		if _, err = domain.HostingMySQLComponent(h, *operation.Plan, in.Component); err != nil {
			return zero, ErrConflict
		}
		snapshot, err := backupRemoteTargetSnapshot(ctx, tx, in.S3Source.TargetID, in.S3Source.Revision)
		if err != nil {
			return zero, err
		}
		now := time.Now().UTC()
		view := domain.HostingDatabaseImportStage{ID: id, Version: domain.HostingDatabaseImportStageVersion, TargetResourceID: resource, Component: in.Component, ExpectedDeployedOperationID: h.OperationID, ExpectedResourceGeneration: generation, S3Source: in.S3Source, State: "queued", CreatedAt: now, UpdatedAt: now}
		p := domain.HostingDatabaseImportStagePlan{Stage: view, ActorID: actor, ProjectID: r.ProjectID, RunnerID: r.RunnerID, Target: h, TargetPlan: *operation.Plan, S3Target: snapshot, MaxBytes: domain.HostingDatabaseImportMaxBytes, TimeoutSeconds: 3600}
		p.Hash = domain.HostingDocumentHash(p)
		if p.Validate() != nil {
			return zero, ErrConflict
		}
		if err = externalImportSourceAuthority(ctx, tx, p); err != nil {
			return zero, err
		}
		if err = externalImportRunner(ctx, tx, r.RunnerID, "stage"); err != nil {
			return zero, err
		}
		if err = q.CreateDatabaseImportStage(ctx, sqlcgen.CreateDatabaseImportStageParams{ID: id, ProjectID: r.ProjectID, TargetResourceID: resource, TargetBindingID: r.BindingID, ActorID: actor, RequestID: in.RequestID, RequestHash: hash, RunnerID: r.RunnerID, DaemonID: h.Release.DaemonID, Plan: buildJSON(p), PlanHash: p.Hash, Body: buildJSON(view)}); err != nil {
			return zero, hostingError(err)
		}
		if err = dispatchExternalImport(ctx, tx, id, r.ProjectID, actor, "stage"); err != nil {
			return zero, err
		}
		return view, externalImportAudit(ctx, tx, actor, r.ProjectID, id, "stage_requested")
	})
}

// GetDatabaseImportStage reads saved facts after current owner/binding authorization.
func (s *PostgresStore) GetDatabaseImportStage(ctx context.Context, actor, id string) (domain.HostingDatabaseImportStage, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseImportStage, error) {
		r, err := sqlcgen.New(tx).ReadDatabaseImportStage(ctx, id)
		if err != nil {
			return domain.HostingDatabaseImportStage{}, hostingError(err)
		}
		if _, err = externalImportResourceAuthority(ctx, tx, actor, r.TargetResourceID, domain.RoleViewer); err != nil {
			return domain.HostingDatabaseImportStage{}, err
		}
		r, err = sqlcgen.New(tx).LockDatabaseImportStage(ctx, id)
		if err != nil {
			return domain.HostingDatabaseImportStage{}, hostingError(err)
		}
		view, err := externalImportStageView(r.Body)
		if err != nil {
			return view, err
		}
		if view.Version == domain.HostingDatabaseImportUploadVersion && view.State == "queued" && r.RunID == nil && !time.Now().Before(*view.UploadDeadline) {
			view.State = "failed"
			view.FailureCode = "stage_expired"
			view.UpdatedAt = time.Now().UTC()
			err = sqlcgen.New(tx).SetDatabaseImportStage(ctx, sqlcgen.SetDatabaseImportStageParams{ID: id, Body: buildJSON(view), ReadyReceipt: r.ReadyReceipt})
		}
		return view, err
	})
}

// RequestDatabaseImport consumes one exact ready receipt; replay precedes expiry/file checks.
func (s *PostgresStore) RequestDatabaseImport(ctx context.Context, id, resource, actor string, in domain.HostingDatabaseImportInput) (domain.HostingDatabaseImportOperation, error) {
	if in.Validate() != nil || in.ComponentTarget.ResourceID != resource {
		return domain.HostingDatabaseImportOperation{}, domain.ErrInvalidDatabaseImport
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseImportOperation, error) {
		var zero domain.HostingDatabaseImportOperation
		r, err := externalImportResourceAuthority(ctx, tx, actor, resource, domain.RoleOwner)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		hash := domain.HostingDocumentHash(struct {
			Actor, Resource string
			Input           domain.HostingDatabaseImportInput
		}{actor, resource, in})
		prior, err := q.FindDatabaseImportRequest(ctx, sqlcgen.FindDatabaseImportRequestParams{TargetResourceID: resource, RequestID: in.RequestID})
		if err == nil {
			if prior.ActorID != actor || prior.RequestHash != hash {
				return zero, ErrConflict
			}
			return externalImportView(prior.Body)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		stage, err := q.LockDatabaseImportStage(ctx, in.StageID)
		if err != nil || stage.ActorID != actor || stage.ProjectID != r.ProjectID || stage.TargetResourceID != resource || stage.TargetBindingID != r.BindingID || stage.RunnerID != r.RunnerID {
			return zero, ErrConflict
		}
		view, err := externalImportStageView(stage.Body)
		if err != nil || view.State != "ready" || view.StageHash != in.ExpectedStageHash || view.ExpiresAt == nil || !time.Now().Before(*view.ExpiresAt) {
			return zero, ErrConflict
		}
		var stagePlan domain.HostingDatabaseImportStagePlan
		var receipt domain.HostingDatabaseImportReadyReceipt
		if json.Unmarshal(stage.Plan, &stagePlan) != nil || json.Unmarshal(stage.ReadyReceipt, &receipt) != nil || receipt.ValidateFor(stagePlan) != nil || receipt.Hash != view.StageHash {
			return zero, ErrConflict
		}
		h, operation, generation, err := databaseBackupComponentSource(ctx, tx, actor, resource, in.ComponentTarget.Component, domain.RoleOwner)
		if err != nil || operation.Plan == nil || generation != in.ComponentTarget.ExpectedResourceGeneration || h.OperationID != in.ComponentTarget.ExpectedDeployedOperationID || domain.HostingDocumentHash(h) != domain.HostingDocumentHash(stagePlan.Target) || operation.Plan.Hash != stagePlan.TargetPlan.Hash {
			return zero, ErrConflict
		}
		if err = externalImportSourceAuthority(ctx, tx, stagePlan); err != nil {
			return zero, err
		}
		if err = externalImportRunner(ctx, tx, r.RunnerID, "import"); err != nil {
			return zero, err
		}
		now := time.Now().UTC()
		out := domain.HostingDatabaseImportOperation{ID: id, Version: domain.HostingDatabaseImportVersion, StageID: stage.ID, TargetResourceID: resource, TargetComponent: "mysql", TargetDeployedOperationID: h.OperationID, State: "queued", OwnershipHeld: true, CreatedAt: now, UpdatedAt: now}
		p := domain.HostingDatabaseImportPlan{Operation: out, StagePlan: stagePlan, Receipt: receipt}
		p.Hash = domain.HostingDocumentHash(p)
		if p.Validate() != nil {
			return zero, ErrConflict
		}
		if err = q.CreateDatabaseImport(ctx, sqlcgen.CreateDatabaseImportParams{ID: id, StageID: stage.ID, ProjectID: r.ProjectID, TargetResourceID: resource, TargetBindingID: r.BindingID, ActorID: actor, RequestID: in.RequestID, RequestHash: hash, RunnerID: r.RunnerID, DaemonID: h.Release.DaemonID, Plan: buildJSON(p), PlanHash: p.Hash, Body: buildJSON(out)}); err != nil {
			return zero, hostingError(err)
		}
		view.State, view.UpdatedAt = "consumed", now
		if err = q.SetDatabaseImportStage(ctx, sqlcgen.SetDatabaseImportStageParams{ID: stage.ID, Body: buildJSON(view), ReadyReceipt: stage.ReadyReceipt}); err != nil {
			return zero, err
		}
		if err = dispatchExternalImport(ctx, tx, id, r.ProjectID, actor, "import"); err != nil {
			return zero, err
		}
		return out, externalImportAudit(ctx, tx, actor, r.ProjectID, id, "requested")
	})
}

// GetDatabaseImport returns the original confirmed operation without replaying SQL.
func (s *PostgresStore) GetDatabaseImport(ctx context.Context, actor, id string) (domain.HostingDatabaseImportOperation, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseImportOperation, error) {
		r, err := sqlcgen.New(tx).ReadDatabaseImport(ctx, id)
		if err != nil {
			return domain.HostingDatabaseImportOperation{}, hostingError(err)
		}
		if _, err = externalImportResourceAuthority(ctx, tx, actor, r.TargetResourceID, domain.RoleViewer); err != nil {
			return domain.HostingDatabaseImportOperation{}, err
		}
		return externalImportView(r.Body)
	})
}

func dispatchExternalImport(ctx context.Context, tx pgx.Tx, id, project, actor, mode string) error {
	kind, prefix := domain.RunTypeDatabaseImportStage, "db_import_stage_"
	if mode == "import" {
		kind, prefix = domain.RunTypeDatabaseImport, "db_import_"
	}
	run := domain.TaskRun{ID: prefix + id, ProjectID: project, RunSpec: domain.RunSpec{Type: kind, HostingOperationID: id, HostingMode: mode, Inputs: map[string]any{}}}
	if domain.ValidateDatabaseImportTask(run) != nil {
		return ErrConflict
	}
	q := sqlcgen.New(tx)
	if _, err := q.CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: run.ID, ProjectID: project, RunSpec: buildJSON(run.RunSpec), RequestedBy: actor}); err != nil {
		return err
	}
	if mode == "stage" {
		return q.DispatchDatabaseImportStage(ctx, sqlcgen.DispatchDatabaseImportStageParams{ID: id, RunID: &run.ID})
	}
	return q.DispatchDatabaseImport(ctx, sqlcgen.DispatchDatabaseImportParams{ID: id, RunID: &run.ID})
}

func externalImportCurrent(ctx context.Context, tx pgx.Tx, p domain.HostingDatabaseImportStagePlan, mode string) error {
	if p.Validate() != nil {
		return ErrConflict
	}
	r, err := externalImportResourceAuthority(ctx, tx, p.ActorID, p.Stage.TargetResourceID, domain.RoleOwner)
	if err != nil {
		return err
	}
	if r.Generation != p.Stage.ExpectedResourceGeneration || r.BindingID != p.Target.Snapshot.BindingID || r.RunnerID != p.RunnerID || r.ServerID != p.Target.Snapshot.ServerID || r.DestinationID != p.Target.Snapshot.DestinationID {
		return ErrConflict
	}
	state, err := sqlcgen.New(tx).LockObservationSettledState(ctx, r.BindingID)
	var healthy domain.HostingHealthy
	if err != nil || state.HoldOperationID != nil || !state.RestoreAdmitted || json.Unmarshal(state.Healthy, &healthy) != nil || domain.HostingDocumentHash(healthy) != domain.HostingDocumentHash(p.Target) {
		return ErrConflict
	}
	if mode == "stage" {
		held, e := sqlcgen.New(tx).ExternalImportTargetOwnershipHeld(ctx, r.BindingID)
		if e != nil {
			return e
		}
		if held {
			return ErrConflict
		}
	}
	busy, e := sqlcgen.New(tx).HostingBindingBusy(ctx, r.BindingID)
	if e != nil {
		return e
	}
	if busy {
		return ErrConflict
	}
	stopped, e := readStackStopped(ctx, tx, r.BindingID)
	if e != nil {
		return e
	}
	if stopped != nil {
		return ErrConflict
	}
	operation, err := hostingOperation(ctx, tx, p.Target.OperationID)
	if err != nil || operation.Plan == nil || operation.State != "succeeded" || operation.MutationHold || operation.Plan.Hash != p.TargetPlan.Hash {
		return ErrConflict
	}
	if err = hostingOperationTargetGate(ctx, tx, operation, false); err != nil {
		return err
	}
	if err = externalImportSourceAuthority(ctx, tx, p); err != nil {
		return err
	}
	if mode == "import" && p.Stage.Version == domain.HostingDatabaseImportUploadVersion {
		if err = externalImportRunner(ctx, tx, p.RunnerID, "upload"); err != nil {
			return err
		}
	}
	if mode == "stage" && p.Stage.Version == domain.HostingDatabaseImportUploadVersion {
		mode = "upload"
	}
	return externalImportRunner(ctx, tx, p.RunnerID, mode)
}

func (s *PostgresStore) databaseImportClaimAllowed(ctx context.Context, tx pgx.Tx, run domain.TaskRun, runner string) (bool, error) {
	if domain.ValidateDatabaseImportTask(run) != nil {
		return false, nil
	}
	q := sqlcgen.New(tx)
	if run.RunSpec.Type == domain.RunTypeDatabaseImportStage {
		row, err := q.ReadDatabaseImportStage(ctx, run.RunSpec.HostingOperationID)
		if err != nil {
			return false, hostingError(err)
		}
		var p domain.HostingDatabaseImportStagePlan
		if row.RunID == nil || *row.RunID != run.ID || row.RunnerID != runner || row.ProjectID != run.ProjectID || json.Unmarshal(row.Plan, &p) != nil || p.Validate() != nil {
			return false, nil
		}
		view, err := externalImportStageView(row.Body)
		if err != nil || view.State != "queued" {
			return false, nil
		}
		return true, externalImportCurrent(ctx, tx, p, "stage")
	}
	row, err := q.ReadDatabaseImport(ctx, run.RunSpec.HostingOperationID)
	if err != nil {
		return false, hostingError(err)
	}
	var p domain.HostingDatabaseImportPlan
	if row.RunID == nil || *row.RunID != run.ID || row.RunnerID != runner || row.ProjectID != run.ProjectID || !row.OwnershipHeld || json.Unmarshal(row.Plan, &p) != nil || p.Validate() != nil {
		return false, nil
	}
	view, err := externalImportView(row.Body)
	if err != nil || view.State != "queued" {
		return false, nil
	}
	return true, externalImportCurrent(ctx, tx, p.StagePlan, "import")
}

func externalImportAuthority(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, live bool) error {
	if a.Attempt < 1 || a.Attempt > 2147483647 {
		return ErrConflict
	}
	q := sqlcgen.New(tx)
	if _, err := q.LockRunID(ctx, a.RunID); err != nil {
		return hostingError(err)
	}
	var err error
	switch a.RunID {
	case "db_import_stage_" + a.OperationID:
		_, err = q.LockDatabaseImportStageAuthority(ctx, sqlcgen.LockDatabaseImportStageAuthorityParams{ID: a.OperationID, RunID: &a.RunID, RunnerID: runner, ID_2: a.LeaseID, Attempt: int32(a.Attempt), Fence: a.Fence, Column7: live})
	case "db_import_" + a.OperationID:
		_, err = q.LockDatabaseImportAuthority(ctx, sqlcgen.LockDatabaseImportAuthorityParams{ID: a.OperationID, RunID: &a.RunID, RunnerID: runner, ID_2: a.LeaseID, Attempt: int32(a.Attempt), Fence: a.Fence, Column7: live})
	default:
		return ErrConflict
	}
	return hostingError(err)
}

// DatabaseImportStagePlan rechecks the full fenced authority and current source/target grant.
func (s *PostgresStore) DatabaseImportStagePlan(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingDatabaseImportStagePlan, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseImportStagePlan, error) {
		var p domain.HostingDatabaseImportStagePlan
		if a.RunID != "db_import_stage_"+a.OperationID {
			return p, ErrConflict
		}
		if e := externalImportOperationResource(ctx, tx, runner, a); e != nil {
			return p, e
		}
		row, e := sqlcgen.New(tx).LockDatabaseImportStage(ctx, a.OperationID)
		if e != nil {
			return p, hostingError(e)
		}
		if e = externalImportAuthority(ctx, tx, runner, a, true); e != nil {
			return p, e
		}
		view, e := externalImportStageView(row.Body)
		if e != nil || (view.State != "queued" && view.State != "running") || json.Unmarshal(row.Plan, &p) != nil || p.Validate() != nil || p.Hash != row.PlanHash {
			return p, ErrConflict
		}
		return p, externalImportCurrent(ctx, tx, p, "stage")
	})
}

// DatabaseImportPlan authorizes only the admitted local stage, never another GET.
func (s *PostgresStore) DatabaseImportPlan(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingDatabaseImportPlan, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseImportPlan, error) {
		var p domain.HostingDatabaseImportPlan
		if a.RunID != "db_import_"+a.OperationID {
			return p, ErrConflict
		}
		if e := externalImportOperationResource(ctx, tx, runner, a); e != nil {
			return p, e
		}
		row, e := sqlcgen.New(tx).LockDatabaseImport(ctx, a.OperationID)
		if e != nil {
			return p, hostingError(e)
		}
		if e = externalImportAuthority(ctx, tx, runner, a, true); e != nil {
			return p, e
		}
		view, e := externalImportView(row.Body)
		if e != nil || !row.OwnershipHeld || (view.State != "queued" && view.State != "running") || json.Unmarshal(row.Plan, &p) != nil || p.Validate() != nil || p.Hash != row.PlanHash {
			return p, ErrConflict
		}
		return p, externalImportCurrent(ctx, tx, p.StagePlan, "import")
	})
}

// DatabaseImportMutation saves exact replay evidence and terminal task/lease state atomically.
func (s *PostgresStore) DatabaseImportMutation(ctx context.Context, runner string, in domain.HostingDatabaseImportMutation) (domain.HostingDatabaseImportReceiptResult, error) {
	if in.Validate() != nil {
		return domain.HostingDatabaseImportReceiptResult{}, domain.ErrInvalidDatabaseImport
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseImportReceiptResult, error) {
		var out domain.HostingDatabaseImportReceiptResult
		q := sqlcgen.New(tx)
		if e := externalImportOperationResource(ctx, tx, runner, in.HostingAuthority); e != nil {
			return out, e
		}
		// Lock the operation before its lease, in the same order as plan reads.
		stage := in.RunID == "db_import_stage_"+in.OperationID
		var sp domain.HostingDatabaseImportStagePlan
		var ip domain.HostingDatabaseImportPlan
		var sv domain.HostingDatabaseImportStage
		var iv domain.HostingDatabaseImportOperation
		var ready []byte
		if stage {
			row, e := q.LockDatabaseImportStage(ctx, in.OperationID)
			if e != nil {
				return out, hostingError(e)
			}
			if json.Unmarshal(row.Plan, &sp) != nil || sp.Validate() != nil || sp.Hash != row.PlanHash {
				return out, ErrConflict
			}
			v, e := externalImportStageView(row.Body)
			if e != nil {
				return out, e
			}
			sv = v
			ready = row.ReadyReceipt
		} else {
			row, e := q.LockDatabaseImport(ctx, in.OperationID)
			if e != nil {
				return out, hostingError(e)
			}
			if json.Unmarshal(row.Plan, &ip) != nil || ip.Validate() != nil || ip.Hash != row.PlanHash {
				return out, ErrConflict
			}
			sp = ip.StagePlan
			v, e := externalImportView(row.Body)
			if e != nil {
				return out, e
			}
			iv = v
		}
		if e := externalImportAuthority(ctx, tx, runner, in.HostingAuthority, false); e != nil {
			return out, e
		}
		old, e := q.ReadDatabaseImportReceipt(ctx, in.ReceiptID)
		if e == nil {
			if old.OperationID != in.OperationID || old.Phase != in.Phase || old.BodyHash != domain.HostingDocumentHash(in) || json.Unmarshal(old.Response, &out) != nil {
				return out, ErrConflict
			}
			return out, nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return out, e
		}
		if e = externalImportAuthority(ctx, tx, runner, in.HostingAuthority, true); e != nil {
			return out, e
		}
		if e = externalImportCurrent(ctx, tx, sp, map[bool]string{true: "stage", false: "import"}[stage]); e != nil {
			return out, e
		}
		expectedHash := sp.Hash
		if !stage {
			expectedHash = ip.Hash
		}
		if in.PlanHash != expectedHash || in.DaemonID != sp.Target.Release.DaemonID {
			return out, ErrConflict
		}
		now := time.Now().UTC()
		terminal := false
		success := false
		if stage {
			switch in.Phase {
			case "stage_intent":
				if sv.State != "queued" {
					return out, ErrConflict
				}
				sv.State = "running"
			case "ready":
				if sv.State != "running" || in.Ready.ValidateFor(sp) != nil || in.Ready.ReadyAt.After(now.Add(time.Minute)) || !now.Before(in.Ready.ExpiresAt) {
					return out, ErrConflict
				}
				sv, e = in.Ready.StageView(sp)
				if e != nil {
					return out, e
				}
				ready = buildJSON(in.Ready)
				terminal = true
				success = true
			case "stage_failed":
				if sv.State != "queued" && sv.State != "running" {
					return out, ErrConflict
				}
				sv.State = "failed"
				sv.FailureCode = in.FailureCode
				terminal = true
			default:
				return out, ErrConflict
			}
			sv.UpdatedAt = now
			if sv.Validate() != nil {
				return out, ErrConflict
			}
			if e = q.SetDatabaseImportStage(ctx, sqlcgen.SetDatabaseImportStageParams{ID: sv.ID, Body: buildJSON(sv), ReadyReceipt: ready}); e != nil {
				return out, e
			}
			out.Stage = &sv
		} else {
			// Writer identities are frozen by the real healthy receipt, never arbitrary containers.
			if e = validateExternalImportWriters(ip, in); e != nil {
				return out, e
			}
			switch in.Phase {
			case "import_intent":
				if iv.State != "queued" {
					return out, ErrConflict
				}
				iv.State = "running"
			case "failed":
				if iv.State != "queued" || !in.WritersResumed {
					return out, ErrConflict
				}
				iv.State = "failed"
				iv.OwnershipHeld = false
				terminal = true
			case "complete", "uncertain":
				if iv.State != "running" {
					return out, ErrConflict
				}
				var original domain.HostingDatabaseImportMutation
				if ready, e = q.ReadDatabaseImportIntentBody(ctx, iv.ID); e != nil || json.Unmarshal(ready, &original) != nil || domain.HostingDocumentHash(original.Writers) != domain.HostingDocumentHash(in.Writers) {
					return out, ErrConflict
				}
				iv.State = "uncertain"
				terminal = true
				if in.Phase == "complete" {
					iv.State = "succeeded"
					iv.OwnershipHeld = false
					success = true
				}
			default:
				return out, ErrConflict
			}
			iv.FailureCode = in.FailureCode
			iv.WritersResumed = in.WritersResumed
			iv.UpdatedAt = now
			if iv.Validate() != nil {
				return out, ErrConflict
			}
			if e = q.SetDatabaseImport(ctx, sqlcgen.SetDatabaseImportParams{ID: iv.ID, Body: buildJSON(iv), OwnershipHeld: iv.OwnershipHeld}); e != nil {
				return out, e
			}
			out.Import = &iv
		}
		if terminal {
			status := "failed"
			if success {
				status = "succeeded"
			}
			if e = q.CompleteDatabaseBackupTask(ctx, sqlcgen.CompleteDatabaseBackupTaskParams{ID: in.RunID, Status: status}); e != nil {
				return out, e
			}
			if e = q.CompleteDatabaseBackupLease(ctx, sqlcgen.CompleteDatabaseBackupLeaseParams{ID: in.LeaseID, CompletionKey: &in.ReceiptID, Status: status}); e != nil {
				return out, e
			}
		}
		if e = q.CreateDatabaseImportReceipt(ctx, sqlcgen.CreateDatabaseImportReceiptParams{OperationID: in.OperationID, Phase: in.Phase, ReceiptID: in.ReceiptID, Authority: buildJSON(in.HostingAuthority), Body: buildJSON(in), BodyHash: domain.HostingDocumentHash(in), Response: buildJSON(out)}); e != nil {
			return out, hostingError(e)
		}
		return out, externalImportAudit(ctx, tx, runner, sp.ProjectID, in.OperationID, in.Phase)
	})
}

func validateExternalImportWriters(p domain.HostingDatabaseImportPlan, m domain.HostingDatabaseImportMutation) error {
	if m.Phase == "failed" {
		if len(m.Writers) != 0 {
			return ErrConflict
		}
		return nil
	}
	if len(m.Writers) != len(p.StagePlan.Target.Stack.Components)-1 || len(m.Writers) > 16 {
		return ErrConflict
	}
	seen := map[string]bool{}
	for _, w := range m.Writers {
		found := false
		for _, c := range p.StagePlan.Target.Stack.Components {
			if c.Component != "mysql" && c.ContainerID == w.ContainerID && c.OperationID == w.OperationID && c.ImageID == w.ImageID && w.PlanHash == p.StagePlan.Target.PlanHash && w.ReleaseHash == p.StagePlan.Target.Release.Hash && !seen[c.ContainerID] {
				found = true
				seen[c.ContainerID] = true
			}
		}
		if !found {
			return ErrConflict
		}
	}
	return nil
}

func externalImportCredentialAuthority(ctx context.Context, tx pgx.Tx, runner, tokenHash string, in domain.BackupRemoteCredentialRequest) (domain.HostingDatabaseImportStagePlan, time.Time, error) {
	var p domain.HostingDatabaseImportStagePlan
	var expiry time.Time
	if in.Validate() != nil || in.RetentionCopyID != "" || in.Authority.RunID != "db_import_stage_"+in.Authority.OperationID {
		return p, expiry, ErrConflict
	}
	if e := externalImportOperationResource(ctx, tx, runner, in.Authority); e != nil {
		return p, expiry, e
	}
	row, e := sqlcgen.New(tx).LockDatabaseImportStage(ctx, in.Authority.OperationID)
	if e != nil {
		return p, expiry, hostingError(e)
	}
	if e = externalImportAuthority(ctx, tx, runner, in.Authority, true); e != nil {
		return p, expiry, e
	}
	v, e := externalImportStageView(row.Body)
	if e != nil || v.State != "queued" && v.State != "running" || json.Unmarshal(row.Plan, &p) != nil || p.Validate() != nil || p.Hash != in.PlanHash || p.Stage.S3Source.TargetID != in.TargetID || p.Stage.S3Source.Revision != in.TargetRevision || p.Target.Release.DaemonID != in.DaemonID {
		return p, expiry, ErrConflict
	}
	if e = externalImportCurrent(ctx, tx, p, "stage"); e != nil {
		return p, expiry, e
	}
	expiry, e = sqlcgen.New(tx).ReadDatabaseBackupRemoteCredentialExpiry(ctx, sqlcgen.ReadDatabaseBackupRemoteCredentialExpiryParams{ID: in.Authority.LeaseID, ID_2: runner, TokenHash: tokenHash})
	return p, expiry, hostingError(e)
}

// AuthorizeDatabaseImportCredentials grants only a single stage's protected GET.
func (s *PostgresStore) AuthorizeDatabaseImportCredentials(ctx context.Context, runner, tokenHash string, in domain.BackupRemoteCredentialRequest) (BackupRemoteCredentialAuthorization, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (BackupRemoteCredentialAuthorization, error) {
		var out BackupRemoteCredentialAuthorization
		p, expiry, e := externalImportCredentialAuthority(ctx, tx, runner, tokenHash, in)
		if e != nil {
			return out, e
		}
		hash := domain.HostingDocumentHash(struct {
			Runner  string
			Request domain.BackupRemoteCredentialRequest
		}{runner, in})
		var raw []byte
		raw, e = sqlcgen.New(tx).ReadDatabaseImportDelivery(ctx, in.DeliveryID)
		if errors.Is(e, pgx.ErrNoRows) {
			out.Metadata = domain.BackupRemoteCredentialMetadata{Purpose: domain.HostingDatabaseImportCredentialPurpose, Request: in, RunnerID: runner, RequestHash: hash, UseUntil: expiry}
			e = sqlcgen.New(tx).CreateDatabaseImportDelivery(ctx, sqlcgen.CreateDatabaseImportDeliveryParams{DeliveryID: in.DeliveryID, StageID: p.Stage.ID, Metadata: buildJSON(out.Metadata)})
			if e != nil {
				return out, hostingError(e)
			}
		} else if e != nil {
			return out, e
		} else if json.Unmarshal(raw, &out.Metadata) != nil {
			return out, ErrConflict
		}
		m := out.Metadata
		if m.Validate() != nil || m.Purpose != domain.HostingDatabaseImportCredentialPurpose || m.Request != in || m.RequestHash != hash || m.RunnerID != runner || !time.Now().Before(m.UseUntil) || m.UseUntil.After(expiry) {
			return out, ErrConflict
		}
		out.ProjectID = p.ProjectID
		out.Records.Access, e = backupRemoteCredentialRecord(ctx, tx, p.ProjectID, p.S3Target.Access, true)
		if e != nil {
			return out, e
		}
		out.Records.Secret, e = backupRemoteCredentialRecord(ctx, tx, p.ProjectID, p.S3Target.Secret, true)
		return out, e
	})
}

// RevalidateDatabaseImportCredentials repeats current authority immediately before delivery.
func (s *PostgresStore) RevalidateDatabaseImportCredentials(ctx context.Context, runner, tokenHash string, in domain.BackupRemoteCredentialRequest, m domain.BackupRemoteCredentialMetadata) error {
	_, e := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		_, expiry, e := externalImportCredentialAuthority(ctx, tx, runner, tokenHash, in)
		if e != nil {
			return false, e
		}
		var saved domain.BackupRemoteCredentialMetadata
		var raw []byte
		if raw, e = sqlcgen.New(tx).ReadDatabaseImportStageDelivery(ctx, sqlcgen.ReadDatabaseImportStageDeliveryParams{DeliveryID: in.DeliveryID, StageID: in.Authority.OperationID}); e != nil {
			return false, hostingError(e)
		}
		if json.Unmarshal(raw, &saved) != nil || saved.Validate() != nil || m.Validate() != nil || m.Purpose != domain.HostingDatabaseImportCredentialPurpose || m.Request != in || m.RunnerID != runner || !time.Now().Before(m.UseUntil) || m.UseUntil.After(expiry) {
			return false, ErrConflict
		}
		comparable := saved
		comparable.UseUntil = m.UseUntil
		if comparable != m || !saved.UseUntil.Equal(m.UseUntil) {
			return false, ErrConflict
		}
		return true, nil
	})
	return e
}

// All new family entry points acquire resource/destination before operation and
// lease rows, matching public confirmation/replay and expiry cleanup.
func externalImportOperationResource(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority) error {
	q := sqlcgen.New(tx)
	var actor, resource, binding, project, expectedRunner string
	switch a.RunID {
	case "db_import_stage_" + a.OperationID:
		row, e := q.ReadDatabaseImportStage(ctx, a.OperationID)
		if e != nil {
			return hostingError(e)
		}
		actor, resource, binding, project, expectedRunner = row.ActorID, row.TargetResourceID, row.TargetBindingID, row.ProjectID, row.RunnerID
	case "db_import_" + a.OperationID:
		row, e := q.ReadDatabaseImport(ctx, a.OperationID)
		if e != nil {
			return hostingError(e)
		}
		actor, resource, binding, project, expectedRunner = row.ActorID, row.TargetResourceID, row.TargetBindingID, row.ProjectID, row.RunnerID
	default:
		return ErrConflict
	}
	if expectedRunner != runner {
		return ErrConflict
	}
	r, e := externalImportResourceAuthority(ctx, tx, actor, resource, domain.RoleOwner)
	if e != nil {
		return e
	}
	if r.BindingID != binding || r.ProjectID != project || r.RunnerID != runner {
		return ErrConflict
	}
	return nil
}

// AuthorizeDatabaseImportStageCleanup atomically excludes new confirmation before
// permitting exact local deletion. A consumed reservation never expires here.
func (s *PostgresStore) AuthorizeDatabaseImportStageCleanup(ctx context.Context, runner string, in domain.HostingDatabaseImportStageCleanup) (domain.HostingDatabaseImportStageCleanupResult, error) {
	if in.Validate() != nil {
		return domain.HostingDatabaseImportStageCleanupResult{}, domain.ErrInvalidDatabaseImport
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseImportStageCleanupResult, error) {
		out := domain.HostingDatabaseImportStageCleanupResult{HostingDatabaseImportStageCleanup: in}
		a := domain.HostingAuthority{OperationID: in.StageID, RunID: "db_import_stage_" + in.StageID}
		if e := externalImportOperationResource(ctx, tx, runner, a); e != nil {
			return out, e
		}
		q := sqlcgen.New(tx)
		row, e := q.LockDatabaseImportStage(ctx, in.StageID)
		if e != nil {
			return out, hostingError(e)
		}
		var p domain.HostingDatabaseImportStagePlan
		if json.Unmarshal(row.Plan, &p) != nil || p.Validate() != nil || p.Hash != in.PlanHash || row.PlanHash != in.PlanHash || p.RunnerID != runner {
			return out, ErrConflict
		}
		mode := "stage"
		if p.Stage.Version == domain.HostingDatabaseImportUploadVersion {
			mode = "upload"
		}
		if e = externalImportRunner(ctx, tx, runner, mode); e != nil {
			return out, e
		}
		v, e := externalImportStageView(row.Body)
		if e != nil {
			return out, e
		}
		if v.State == "consumed" {
			return out, nil
		}
		if v.State == "failed" && in.ReadyHash == "" {
			out.Delete = true
			return out, nil
		}
		if v.State != "ready" && v.State != "expired" {
			return out, nil
		}
		var receipt domain.HostingDatabaseImportReadyReceipt
		if json.Unmarshal(row.ReadyReceipt, &receipt) != nil || receipt.ValidateFor(p) != nil || in.ReadyHash != receipt.Hash || v.StageHash != receipt.Hash {
			return out, ErrConflict
		}
		var expired bool
		if expired, e = q.DatabaseImportStageDeadlinePassed(ctx, receipt.ExpiresAt); e != nil {
			return out, e
		}
		if !expired {
			return out, nil
		}
		v.State = "expired"
		v.UpdatedAt = time.Now().UTC()
		if e = q.SetDatabaseImportStage(ctx, sqlcgen.SetDatabaseImportStageParams{ID: v.ID, Body: buildJSON(v), ReadyReceipt: row.ReadyReceipt}); e != nil {
			return out, e
		}
		out.Delete = true
		return out, nil
	})
}

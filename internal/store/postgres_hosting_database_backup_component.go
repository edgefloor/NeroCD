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

// DatabaseBackupComponents returns authorized settled identities, not a runtime health claim.
func (s *PostgresStore) DatabaseBackupComponents(ctx context.Context, actor, resource string) (domain.HostingDatabaseBackupComponents, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupComponents, error) {
		out := domain.HostingDatabaseBackupComponents{ResourceID: resource, Reason: "unavailable", Components: []domain.HostingDatabaseBackupComponent{}}
		r, err := sqlcgen.New(tx).GetResource(ctx, resource)
		if err != nil {
			return out, hostingError(err)
		}
		if err = proxyAuthorize(ctx, tx, actor, r.ProjectID, domain.RoleViewer, false); err != nil {
			return out, err
		}
		out.ResourceGeneration = r.Generation
		h, op, _, err := databaseBackupComponentSource(ctx, tx, actor, resource, "", domain.RoleViewer)
		if errors.Is(err, ErrConflict) || errors.Is(err, pgx.ErrNoRows) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out.OperationID, out.Available, out.Reason = h.OperationID, true, "available"
		for _, c := range h.Stack.Components {
			version := domain.HostingDatabaseBackupComponentVersionFor(h, *op.Plan, c.Component)
			if version == "" {
				continue
			}
			engine, major := "postgresql", 16
			if version == domain.HostingDatabaseMySQLBackupComponentVersion {
				engine, major = "mysql", 8
			}
			out.Components = append(out.Components, domain.HostingDatabaseBackupComponent{Component: c.Component, Engine: engine, MajorVersion: major, Running: c.Running, Eligible: true, Reason: "requires_runtime_profile_verification"})
		}
		if len(out.Components) == 0 {
			out.Available, out.Reason = false, "unsupported_profile"
		}
		return out, nil
	})
}

// Component authority is separate from the single-container database recipe.
// The destination lock serializes backup/import against stack replacement.
func databaseBackupComponentSource(ctx context.Context, tx pgx.Tx, actor, resource, component string, role string) (domain.HostingHealthy, domain.HostingOperation, int64, error) {
	var h domain.HostingHealthy
	var op domain.HostingOperation
	if err := lockHostingResourceDestination(ctx, tx, resource); err != nil {
		return h, op, 0, err
	}
	q := sqlcgen.New(tx)
	r, err := q.LockHostingResourceTarget(ctx, resource)
	if err != nil {
		return h, op, 0, hostingError(err)
	}
	if err = proxyAuthorize(ctx, tx, actor, r.ProjectID, role, false); err != nil {
		return h, op, 0, err
	}
	if err = databaseBindingGrant(ctx, q, r.ProjectID, r.DestinationID); err != nil {
		return h, op, 0, err
	}
	state, err := q.LockObservationSettledState(ctx, r.BindingID)
	if err != nil {
		return h, op, 0, err
	}
	if state.HoldOperationID != nil || !state.RestoreAdmitted {
		return h, op, 0, ErrConflict
	}
	busy, err := q.HostingBindingBusy(ctx, r.BindingID)
	if err != nil {
		return h, op, 0, err
	}
	if busy {
		return h, op, 0, ErrConflict
	}
	stopped, err := readStackStopped(ctx, tx, r.BindingID)
	if err != nil {
		return h, op, 0, err
	}
	if stopped != nil || json.Unmarshal(state.Healthy, &h) != nil || domain.ValidateHostingStackHealthy(h) != nil || h.Snapshot.BindingID != r.BindingID || h.Snapshot.ResourceID != resource || h.Snapshot.DestinationID != r.DestinationID || h.Snapshot.RunnerID != r.RunnerID || h.Snapshot.ServerID != r.ServerID {
		return h, op, 0, ErrConflict
	}
	op, err = hostingOperation(ctx, tx, h.OperationID)
	if err != nil {
		return h, op, 0, err
	}
	if op.Plan == nil || op.State != "succeeded" || op.MutationHold || op.ProjectID != r.ProjectID || op.ResourceID != resource {
		return h, op, 0, ErrConflict
	}
	if _, err = domain.CompileHostingStackStopPlan(h, *op.Plan); err != nil {
		return h, op, 0, ErrConflict
	}
	if err = hostingOperationTargetGate(ctx, tx, op, false); err != nil {
		return h, op, 0, err
	}
	if component != "" {
		if domain.HostingDatabaseBackupComponentVersionFor(h, *op.Plan, component) == "" {
			return h, op, 0, ErrConflict
		}
	}
	return h, op, r.Generation, proxyCheckLegacyToken(ctx, tx, actor)
}

func databaseBackupComponentCurrent(ctx context.Context, tx pgx.Tx, r sqlcgen.HostingDatabaseBackup, p domain.HostingDatabaseBackupPlan) (domain.HostingDatabaseBackupPlan, error) {
	if p.Validate() != nil {
		return p, ErrConflict
	}
	if r.Mode == "restore" {
		if err := databaseBackupRestoreAuthority(ctx, tx, r.CreatedBy, p); err != nil {
			return p, err
		}
		h, op, _, err := databaseBackupComponentSource(ctx, tx, r.CreatedBy, p.Operation.TargetResourceID, p.Component.Target, domain.RoleOwner)
		if err != nil {
			return p, err
		}
		if domain.HostingDocumentHash(h) != domain.HostingDocumentHash(p.Target) || op.ID != proxyString(r.DeploymentOperationID) || op.Plan.Hash != p.TargetPlan.Hash {
			return p, ErrConflict
		}
	} else {
		h, op, _, err := databaseBackupComponentSource(ctx, tx, r.CreatedBy, r.SourceResourceID, p.Component.Source, domain.RoleOwner)
		if err != nil {
			return p, err
		}
		if err = databaseBackupSourceRole(ctx, tx, h.Snapshot.BindingID); err != nil {
			return p, err
		}
		if domain.HostingDocumentHash(h) != domain.HostingDocumentHash(p.Source) || op.Plan.Hash != p.SourcePlan.Hash {
			return p, ErrConflict
		}
	}
	return p, databaseBackupAgent(ctx, tx, p)
}

func (s *PostgresStore) requestDatabaseBackupComponent(ctx context.Context, tx pgx.Tx, id, resource, actor string, in domain.HostingDatabaseBackupInput) (domain.HostingDatabaseBackupOperation, error) {
	var zero domain.HostingDatabaseBackupOperation
	q := sqlcgen.New(tx)
	if s.hostingJobs == nil || !s.hostingJobs.DatabaseBackupEnabled() {
		return zero, hostingjobs.ErrUnavailable
	}
	if err := databaseBackupSchema(ctx, q); err != nil {
		return zero, err
	}
	h, op, generation, err := databaseBackupComponentSource(ctx, tx, actor, resource, in.Component, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	hash := domain.HostingDocumentHash(struct {
		Actor string
		Input domain.HostingDatabaseBackupInput
	}{actor, in})
	prior, err := q.FindDatabaseBackupSourceRequest(ctx, sqlcgen.FindDatabaseBackupSourceRequestParams{SourceResourceID: resource, RequestID: in.RequestID})
	if err == nil {
		r, e := q.ReadDatabaseBackup(ctx, prior)
		if e != nil {
			return zero, e
		}
		if r.CreatedBy != actor || r.RequestHash != hash {
			return zero, ErrConflict
		}
		return databaseBackupView(r)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	if generation != in.ExpectedResourceGeneration || h.OperationID != in.ExpectedDeployedOperationID {
		return zero, ErrConflict
	}
	if err = databaseBackupSourceRole(ctx, tx, h.Snapshot.BindingID); err != nil {
		return zero, err
	}
	limits := s.databaseBackupPolicy()
	now := time.Now().UTC()
	view := domain.HostingDatabaseBackupOperation{ID: id, Version: domain.HostingDatabaseBackupComponentVersionFor(h, *op.Plan, in.Component), ProjectID: op.ProjectID, SourceResourceID: resource, SourceGeneration: h.Snapshot.ResourceGeneration, SourceComponent: in.Component, SourceDeployedOperationID: h.OperationID, BackupID: id, Mode: "backup", State: "queued", Consistency: "logical_snapshot_source_writable", OwnershipHeld: true, MaxBytes: limits.MaxBytes, TimeoutSeconds: limits.TimeoutSeconds, RemoteTarget: in.RemoteTarget, CreatedAt: now, UpdatedAt: now}
	p := domain.HostingDatabaseBackupPlan{Operation: view, Component: &domain.HostingDatabaseBackupComponentPlan{Source: in.Component}, ActorID: actor, RunnerID: h.Snapshot.RunnerID, Source: h, SourcePlan: *op.Plan, LocationID: id, MaxBytes: limits.MaxBytes, TimeoutSeconds: limits.TimeoutSeconds}
	p.Hash = domain.HostingDocumentHash(p)
	if p.Validate() != nil {
		return zero, ErrConflict
	}
	if err = databaseBackupAgent(ctx, tx, p); err != nil {
		return zero, err
	}
	if in.RemoteTarget != nil {
		if _, err = backupRemoteSelect(ctx, tx, p, *in.RemoteTarget); err != nil {
			return zero, err
		}
	}
	if err = q.CreateDatabaseBackup(ctx, sqlcgen.CreateDatabaseBackupParams{ID: id, ProjectID: view.ProjectID, SourceResourceID: resource, SourceBindingID: h.Snapshot.BindingID, SourceGeneration: view.SourceGeneration, RequestID: in.RequestID, RequestHash: hash, CreatedBy: actor, RunnerID: p.RunnerID, Plan: buildJSON(p), PlanHash: p.Hash}); err != nil {
		return zero, hostingError(err)
	}
	if in.RemoteTarget != nil {
		if err = q.CreateDatabaseBackupRemoteSelection(ctx, sqlcgen.CreateDatabaseBackupRemoteSelectionParams{OperationID: id, TargetID: in.RemoteTarget.TargetID, TargetRevision: in.RemoteTarget.Revision}); err != nil {
			return zero, err
		}
	}
	if err = databaseBackupAudit(ctx, tx, actor, id, "requested", view.ProjectID); err != nil {
		return zero, err
	}
	if _, err = s.hostingJobs.EnqueueDatabaseBackupTx(ctx, tx, hostingjobs.DatabaseBackupArgs{OperationID: id}); err != nil {
		return zero, err
	}
	return view, proxyCheckLegacyToken(ctx, tx, actor)
}

func (s *PostgresStore) requestDatabaseBackupComponentRestore(ctx context.Context, tx pgx.Tx, id, backup, actor string, in domain.HostingDatabaseBackupRestoreInput) (domain.HostingDatabaseBackupOperation, error) {
	var zero domain.HostingDatabaseBackupOperation
	q := sqlcgen.New(tx)
	if s.hostingJobs == nil || !s.hostingJobs.DatabaseBackupEnabled() {
		return zero, hostingjobs.ErrUnavailable
	}
	original, err := q.ReadDatabaseBackup(ctx, backup)
	if err != nil {
		return zero, hostingError(err)
	}
	p, err := databaseBackupPlan(original)
	if err != nil {
		return zero, err
	}
	if p.Operation.Version == domain.HostingDatabaseMySQLBackupComponentVersion && in.RemoteSource != nil {
		return zero, ErrConflict
	}
	if p.Component == nil || p.Validate() != nil || original.Mode != "backup" || original.State != "succeeded" || original.Blob == nil || in.ComponentTarget.ResourceID == original.SourceResourceID {
		return zero, ErrConflict
	}
	if err = databaseBackupRestoreAuthority(ctx, tx, actor, p); err != nil {
		return zero, err
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
	target := *in.ComponentTarget
	h, op, generation, err := databaseBackupComponentSource(ctx, tx, actor, target.ResourceID, target.Component, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	if op.ProjectID != original.ProjectID || generation != target.ExpectedResourceGeneration || h.OperationID != target.ExpectedDeployedOperationID || h.Snapshot.DestinationID != p.Source.Snapshot.DestinationID || h.Snapshot.RunnerID != p.RunnerID {
		return zero, ErrConflict
	}
	if err = backupRetentionArchiveAdmission(ctx, tx, backup, in.RemoteSource); err != nil {
		return zero, err
	}
	var blob domain.HostingDatabaseBackupBlob
	if json.Unmarshal(original.Blob, &blob) != nil || blob.Validate() != nil {
		return zero, ErrConflict
	}
	limits := s.databaseBackupPolicy()
	if blob.Bytes > limits.MaxBytes {
		return zero, ErrConflict
	}
	now := time.Now().UTC()
	view := domain.HostingDatabaseBackupOperation{ID: id, Version: p.Operation.Version, ProjectID: original.ProjectID, SourceResourceID: original.SourceResourceID, SourceGeneration: original.SourceGeneration, SourceComponent: p.Component.Source, SourceDeployedOperationID: p.Source.OperationID, BackupID: backup, Mode: "restore", State: "waiting_target", TargetResourceID: target.ResourceID, TargetComponent: target.Component, TargetDeployedOperationID: h.OperationID, Blob: &blob, Consistency: "logical_snapshot_source_writable", OwnershipHeld: true, MaxBytes: limits.MaxBytes, TimeoutSeconds: limits.TimeoutSeconds, CreatedAt: now, UpdatedAt: now}
	p.Operation, p.ActorID, p.Target, p.TargetPlan = view, actor, &h, op.Plan
	p.Component = &domain.HostingDatabaseBackupComponentPlan{Source: p.Component.Source, Target: target.Component}
	p.MaxBytes, p.TimeoutSeconds = limits.MaxBytes, limits.TimeoutSeconds
	p.Hash = ""
	p.Hash = domain.HostingDocumentHash(p)
	if p.Validate() != nil {
		return zero, ErrConflict
	}
	if err = databaseBackupAgent(ctx, tx, p); err != nil {
		return zero, err
	}
	if err = q.CreateDatabaseBackupRestore(ctx, sqlcgen.CreateDatabaseBackupRestoreParams{ID: id, ProjectID: view.ProjectID, SourceResourceID: view.SourceResourceID, SourceBindingID: original.SourceBindingID, SourceGeneration: view.SourceGeneration, BackupID: backup, RequestID: in.RequestID, RequestHash: hash, CreatedBy: actor, TargetResourceID: &target.ResourceID, TargetBindingID: &h.Snapshot.BindingID, DeploymentOperationID: &h.OperationID, RunnerID: p.RunnerID, Plan: buildJSON(p), PlanHash: p.Hash, Blob: original.Blob}); err != nil {
		return zero, hostingError(err)
	}
	if in.RemoteSource != nil {
		if err = s.backupRemoteRestoreAdmit(ctx, tx, p, *in.RemoteSource); err != nil {
			return zero, err
		}
	}
	if err = databaseBackupAudit(ctx, tx, actor, id, "requested", view.ProjectID); err != nil {
		return zero, err
	}
	if _, err = s.hostingJobs.EnqueueDatabaseBackupTx(ctx, tx, hostingjobs.DatabaseBackupArgs{OperationID: id}); err != nil {
		return zero, err
	}
	return view, proxyCheckLegacyToken(ctx, tx, actor)
}

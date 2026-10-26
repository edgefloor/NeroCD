package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

func storageRetentionLock(ctx context.Context, tx pgx.Tx) error {
	e := sqlcgen.New(tx).StorageRetentionLockPool(ctx)
	return e
}
func storageRetentionPolicy(ctx context.Context, tx pgx.Tx, resource string) (*domain.HostingStorageBackupRetentionPolicy, error) {
	var raw []byte
	raw, err := sqlcgen.New(tx).StorageRetentionReadPolicy(ctx, resource)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p domain.HostingStorageBackupRetentionPolicy
	if json.Unmarshal(raw, &p) != nil || p.Version != domain.HostingStorageBackupRetentionVersion || p.ResourceID != resource {
		return nil, ErrConflict
	}
	return &p, nil
}
func storageRetentionReplay(ctx context.Context, tx pgx.Tx, resource, actor, kind, request, hash string, out any) (bool, error) {
	var got, savedActor string
	var raw []byte
	savedRequest, e := sqlcgen.New(tx).StorageRetentionReadRequest(ctx, sqlcgen.StorageRetentionReadRequestParams{ResourceID: resource, Kind: kind, RequestID: request})
	savedActor, got, raw = savedRequest.ActorID, savedRequest.RequestHash, savedRequest.Response
	if errors.Is(e, pgx.ErrNoRows) {
		return false, nil
	}
	if e != nil {
		return false, e
	}
	if got != hash || savedActor != actor || json.Unmarshal(raw, out) != nil {
		return true, ErrConflict
	}
	return true, nil
}
func storageRetentionSaveRequest(ctx context.Context, tx pgx.Tx, resource, actor, kind, request, hash string, out any) error {
	e := sqlcgen.New(tx).StorageRetentionInsertRequest(ctx, sqlcgen.StorageRetentionInsertRequestParams{ResourceID: resource, Kind: kind, RequestID: request, ActorID: actor, RequestHash: hash, Response: buildJSON(out)})
	return e
}

// GetStorageBackupRetention returns the authorized storage backup retention policy.
func (s *PostgresStore) GetStorageBackupRetention(ctx context.Context, actor, resource string) (*domain.HostingStorageBackupRetentionPolicy, error) {
	return archiveActorTransaction(ctx, s, actor, func(tx pgx.Tx) (*domain.HostingStorageBackupRetentionPolicy, error) {
		if _, e := storageRetentionScopeAuthority(ctx, tx, actor, resource, domain.RoleViewer); e != nil {
			return nil, e
		}
		return storageRetentionPolicy(ctx, tx, resource)
	})
}

// SaveStorageBackupRetention updates the authorized storage backup retention policy.
func (s *PostgresStore) SaveStorageBackupRetention(ctx context.Context, actor, resource string, in domain.HostingStorageBackupRetentionInput) (domain.HostingStorageBackupRetentionPolicy, error) {
	var zero domain.HostingStorageBackupRetentionPolicy
	if in.Validate() != nil {
		return zero, domain.ErrInvalidStorageBackupRetention
	}
	return archiveActorTransaction(ctx, s, actor, func(tx pgx.Tx) (domain.HostingStorageBackupRetentionPolicy, error) {
		value, e := storageRetentionScopeAuthority(ctx, tx, actor, resource, domain.RoleOwner)
		if e != nil {
			return zero, e
		}
		if e = storageRetentionLock(ctx, tx); e != nil {
			return zero, e
		}
		hash := domain.HostingDocumentHash(in)
		if replay, e := storageRetentionReplay(ctx, tx, resource, actor, "policy", in.RequestID, hash, &zero); replay || e != nil {
			return zero, e
		}
		old, e := storageRetentionPolicy(ctx, tx, resource)
		if e != nil {
			return zero, e
		}
		revision := int64(0)
		now := time.Now().UTC()
		created := now
		if old != nil {
			revision = old.Revision
			created = old.CreatedAt
		}
		if revision != in.ExpectedRevision || value.Resource.Generation != in.ExpectedResourceGeneration {
			return zero, ErrConflict
		}
		out := domain.HostingStorageBackupRetentionPolicy{Version: domain.HostingStorageBackupRetentionVersion, ProjectID: value.Resource.ProjectID, ResourceID: resource, Revision: revision + 1, ResourceGeneration: value.Resource.Generation, Storage: value.Storage.Reference(), BindingID: value.Binding.ID, DestinationID: value.Binding.DestinationID, Enabled: in.Enabled, Automatic: in.Automatic, Local: in.Local, Remote: in.Remote, CreatedAt: created, UpdatedAt: now}
		e = sqlcgen.New(tx).StorageRetentionSavePolicy(ctx, sqlcgen.StorageRetentionSavePolicyParams{ResourceID: resource, ProjectID: out.ProjectID, ActorID: actor, Revision: out.Revision, Body: buildJSON(out)})
		if e != nil {
			return zero, e
		}
		if e = storageRetentionSaveRequest(ctx, tx, resource, actor, "policy", in.RequestID, hash, out); e != nil {
			return zero, e
		}
		if e = storageRetentionAudit(ctx, tx, actor, resource, fmt.Sprintf("retention_policy_%d", out.Revision), out.ProjectID); e != nil {
			return zero, e
		}
		return out, archiveFinalizeActor(ctx, tx, actor)
	})
}

func storageRetentionPreviewTx(ctx context.Context, tx pgx.Tx, id, resource, actor string, p domain.HostingStorageBackupRetentionPolicy) (domain.HostingStorageBackupRetentionPreview, []domain.StorageBackupRetentionCopy, error) {
	var zero domain.HostingStorageBackupRetentionPreview
	inventory, e := storageRetentionInventory(ctx, tx, resource)
	if e != nil {
		return zero, nil, e
	}
	visible := make([]domain.BackupRetentionItem, len(inventory))
	byID := map[string]domain.StorageBackupRetentionCopy{}
	for i, c := range inventory {
		visible[i] = c.Item
		byID[c.Item.CopyID] = c
	}
	var at time.Time
	if at, e = sqlcgen.New(tx).StorageRetentionPreviewTime(ctx); e != nil {
		return zero, nil, e
	}
	items, local, remote, truncated := domain.SelectStorageBackupRetention(p, visible, at)
	out := domain.HostingStorageBackupRetentionPreview{ID: id, Version: domain.HostingStorageBackupRetentionVersion, ProjectID: p.ProjectID, ResourceID: resource, PolicyRevision: p.Revision, EvaluatedAt: at, LocalReferenceAt: local, RemoteReferenceAt: remote, SelectionTruncated: truncated, Items: items, CreatedAt: time.Now().UTC()}
	chosen := []domain.StorageBackupRetentionCopy{}
	for _, item := range items {
		if item.Selected {
			c := byID[item.CopyID]
			c.Item = item
			chosen = append(chosen, c)
		}
	}
	out.PlanHash = domain.HostingDocumentHash(struct {
		Preview domain.HostingStorageBackupRetentionPreview
		Copies  []domain.StorageBackupRetentionCopy
	}{out, chosen})
	e = sqlcgen.New(tx).StorageRetentionInsertPreview(ctx, sqlcgen.StorageRetentionInsertPreviewParams{ID: id, ResourceID: resource, ActorID: actor, PolicyRevision: p.Revision, Body: buildJSON(out), Copies: buildJSON(chosen), PlanHash: out.PlanHash})
	return out, chosen, e
}

// PreviewStorageBackupRetention records a retention preview for storage backup copies.
func (s *PostgresStore) PreviewStorageBackupRetention(ctx context.Context, id, actor, resource string, in domain.HostingStorageBackupRetentionPreviewInput) (domain.HostingStorageBackupRetentionPreview, error) {
	var zero domain.HostingStorageBackupRetentionPreview
	if in.Validate() != nil {
		return zero, domain.ErrInvalidStorageBackupRetention
	}
	return archiveActorTransaction(ctx, s, actor, func(tx pgx.Tx) (domain.HostingStorageBackupRetentionPreview, error) {
		if _, e := storageRetentionScopeAuthority(ctx, tx, actor, resource, domain.RoleOwner); e != nil {
			return zero, e
		}
		if e := storageRetentionLock(ctx, tx); e != nil {
			return zero, e
		}
		hash := domain.HostingDocumentHash(in)
		if replay, e := storageRetentionReplay(ctx, tx, resource, actor, "preview", in.RequestID, hash, &zero); replay || e != nil {
			return zero, e
		}
		p, e := storageRetentionPolicy(ctx, tx, resource)
		if e != nil {
			return zero, e
		}
		scope, e := storageRetentionScopeAuthority(ctx, tx, actor, resource, domain.RoleOwner)
		if e != nil {
			return zero, e
		}
		if p == nil || p.Storage != scope.Storage.Reference() || p.BindingID != scope.Binding.ID || p.DestinationID != scope.Binding.DestinationID || p.Revision != in.ExpectedPolicyRevision {
			return zero, ErrConflict
		}
		out, _, e := storageRetentionPreviewTx(ctx, tx, id, resource, actor, *p)
		if e != nil {
			return zero, e
		}
		if e = storageRetentionSaveRequest(ctx, tx, resource, actor, "preview", in.RequestID, hash, out); e != nil {
			return zero, e
		}
		return out, archiveFinalizeActor(ctx, tx, actor)
	})
}

// ListStorageBackupRetentionExecutions lists authorized storage backup retention executions.
func (s *PostgresStore) ListStorageBackupRetentionExecutions(ctx context.Context, actor, resource string) ([]domain.HostingStorageBackupRetentionExecution, error) {
	return archiveActorTransaction(ctx, s, actor, func(tx pgx.Tx) ([]domain.HostingStorageBackupRetentionExecution, error) {
		if _, e := storageRetentionScopeAuthority(ctx, tx, actor, resource, domain.RoleViewer); e != nil {
			return nil, e
		}
		rows, e := sqlcgen.New(tx).StorageRetentionListExecutions(ctx, resource)
		if e != nil {
			return nil, e
		}
		out := []domain.HostingStorageBackupRetentionExecution{}
		for _, raw := range rows {
			var v domain.HostingStorageBackupRetentionExecution
			if json.Unmarshal(raw, &v) != nil {
				return nil, ErrConflict
			}
			out = append(out, v)
		}
		return out, nil
	})
}

// GetStorageBackupRetentionExecution returns an authorized storage backup retention execution.
func (s *PostgresStore) GetStorageBackupRetentionExecution(ctx context.Context, actor, resource, id string) (domain.HostingStorageBackupRetentionExecution, error) {
	return archiveActorTransaction(ctx, s, actor, func(tx pgx.Tx) (domain.HostingStorageBackupRetentionExecution, error) {
		var v domain.HostingStorageBackupRetentionExecution
		if _, e := storageRetentionScopeAuthority(ctx, tx, actor, resource, domain.RoleViewer); e != nil {
			return v, e
		}
		var raw []byte
		raw, e := sqlcgen.New(tx).StorageRetentionReadExecutionBody(ctx, sqlcgen.StorageRetentionReadExecutionBodyParams{ID: id, ResourceID: resource})
		if e != nil {
			return v, hostingError(e)
		}
		if json.Unmarshal(raw, &v) != nil || v.Version != domain.HostingStorageBackupRetentionVersion {
			return v, ErrConflict
		}
		return v, nil
	})
}

// ExecuteStorageBackupRetention admits deletion of the storage copies selected by a saved
// preview.
func (s *PostgresStore) ExecuteStorageBackupRetention(ctx context.Context, id, actor, resource string, in domain.HostingStorageBackupRetentionExecutionInput) (domain.HostingStorageBackupRetentionExecution, error) {
	var zero domain.HostingStorageBackupRetentionExecution
	if in.Validate() != nil {
		return zero, domain.ErrInvalidStorageBackupRetention
	}
	return archiveActorTransaction(ctx, s, actor, func(tx pgx.Tx) (domain.HostingStorageBackupRetentionExecution, error) {
		if _, e := storageRetentionScopeAuthority(ctx, tx, actor, resource, domain.RoleOwner); e != nil {
			return zero, e
		}
		if e := storageRetentionLock(ctx, tx); e != nil {
			return zero, e
		}
		hash := domain.HostingDocumentHash(in)
		if replay, e := storageRetentionReplay(ctx, tx, resource, actor, "execution", in.RequestID, hash, &zero); replay || e != nil {
			return zero, e
		}
		var existing string
		existing, lookup := sqlcgen.New(tx).StorageRetentionFindExecution(ctx, sqlcgen.StorageRetentionFindExecutionParams{PreviewID: in.PreviewID, ResourceID: resource})
		if lookup == nil {
			return s.storageRetentionResumeTx(ctx, tx, existing, actor, resource, in, hash, "execution")
		}
		if !errors.Is(lookup, pgx.ErrNoRows) {
			return zero, lookup
		}
		p, e := storageRetentionPolicy(ctx, tx, resource)
		if e != nil {
			return zero, e
		}
		scope, e := storageRetentionScopeAuthority(ctx, tx, actor, resource, domain.RoleOwner)
		if e != nil {
			return zero, e
		}
		if p == nil || p.Storage != scope.Storage.Reference() || p.BindingID != scope.Binding.ID || p.DestinationID != scope.Binding.DestinationID || p.Revision != in.ExpectedPolicyRevision || !p.Enabled {
			return zero, ErrConflict
		}
		var raw, copyRaw []byte
		var preview domain.HostingStorageBackupRetentionPreview
		var chosen []domain.StorageBackupRetentionCopy
		savedPreview, e := sqlcgen.New(tx).StorageRetentionReadPreview(ctx, sqlcgen.StorageRetentionReadPreviewParams{ID: in.PreviewID, ResourceID: resource, PolicyRevision: p.Revision})
		raw, copyRaw = savedPreview.Body, savedPreview.Copies
		if e != nil {
			return zero, hostingError(e)
		}
		if json.Unmarshal(raw, &preview) != nil || json.Unmarshal(copyRaw, &chosen) != nil || preview.PlanHash != in.ExpectedPlanHash || len(chosen) == 0 || len(chosen) > 100 {
			return zero, ErrConflict
		}
		// Recompute against the complete current inventory, never trust UI selection.
		current, e := storageRetentionInventory(ctx, tx, resource)
		if e != nil {
			return zero, e
		}
		var all []domain.BackupRetentionItem
		byID := map[string]domain.StorageBackupRetentionCopy{}
		for _, c := range current {
			all = append(all, c.Item)
			byID[c.Item.CopyID] = c
		}
		selected, _, _, _ := domain.SelectStorageBackupRetention(*p, all, preview.EvaluatedAt)
		eligible := map[string]bool{}
		for _, c := range selected {
			if c.Selected {
				eligible[c.CopyID] = true
			}
		}
		if len(eligible) != len(chosen) {
			return zero, ErrConflict
		}
		now := time.Now().UTC()
		out := domain.HostingStorageBackupRetentionExecution{ID: id, Version: domain.HostingStorageBackupRetentionVersion, ProjectID: p.ProjectID, ResourceID: resource, PreviewID: preview.ID, PolicyRevision: p.Revision, PlanHash: preview.PlanHash, State: "queued", ReservationHeld: true, LocalState: "not_selected", RemoteState: "not_selected", Items: []domain.BackupRetentionExecutionItem{}, CreatedAt: now, UpdatedAt: now}
		return s.storageRetentionAdmitTx(ctx, tx, actor, resource, in.RequestID, hash, "execution", "retention_requested", out, chosen, byID, eligible)
	})
}
func storageRetentionCurrent(ctx context.Context, tx pgx.Tx, p domain.HostingStorageBackupRetentionPlan) error {
	if p.Validate() != nil {
		return ErrConflict
	}
	value, e := storageRetentionScopeAuthority(ctx, tx, p.ActorID, p.Operation.ResourceID, domain.RoleOwner)
	if e != nil {
		return e
	}
	_, e = sqlcgen.New(tx).StorageRetentionLockRunner(ctx, sqlcgen.StorageRetentionLockRunnerParams{ID: p.RunnerID, Column2: domain.HostingStorageBackupRetentionCapability, Column3: domain.RunTypeStorageBackupRetention})
	if e != nil {
		return hostingError(e)
	}
	if p.Operation.Origin == domain.StorageBackupManualSelection {
		if _, e = sqlcgen.New(tx).StorageRetentionLockRunner(ctx, sqlcgen.StorageRetentionLockRunnerParams{ID: p.RunnerID, Column2: domain.HostingStorageBackupDeletionCapability, Column3: domain.RunTypeStorageBackupRetention}); e != nil {
			return hostingError(e)
		}
	}
	if p.Operation.Origin == domain.StorageBackupManualSelection && value.Resource.Generation != p.Operation.ResourceGeneration {
		return ErrConflict
	}
	for _, c := range p.Copies {
		if c.DestinationID != value.Binding.DestinationID || c.Storage != value.Storage.Reference() {
			return ErrConflict
		}
		var server, runner string
		placement, e := sqlcgen.New(tx).StorageRetentionLockPlacement(ctx, c.DestinationID)
		server, runner = placement.ServerID, placement.RunnerID
		if e != nil {
			return e
		}
		if runner != p.RunnerID || server != c.ServerID {
			return ErrConflict
		}
		if c.Target != nil {
			upload, e := storageRemoteLoad(ctx, tx, c.Item.TransferID, false)
			if e != nil {
				return e
			}
			check := upload.plan
			check.ActorID = p.ActorID
			check.Hash = ""
			check.Hash = domain.HostingDocumentHash(check)
			if domain.HostingDocumentHash(check.Target) != domain.HostingDocumentHash(c.Target) || check.ObjectKey != c.ObjectKey {
				return ErrConflict
			}
			if e = storageRemoteCurrent(ctx, tx, check); e != nil {
				return e
			}
		}
	}
	return archiveFinalizeActor(ctx, tx, p.ActorID)
}

func storageRetentionAudit(ctx context.Context, tx pgx.Tx, actor, id, phase, project string) error {
	return createAuditWithQueries(ctx, sqlcgen.New(tx), domain.AuditEvent{ID: "aud_storage_retention_" + phase + "_" + id, ActorID: actor, Action: "hosting.storage_backup_retention." + phase, TargetID: id, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"project_id": project}})
}

func (s *PostgresStore) storageRetentionAdmitTx(ctx context.Context, tx pgx.Tx, actor, resource, requestID, hash, kind, phase string, out domain.HostingStorageBackupRetentionExecution, chosen []domain.StorageBackupRetentionCopy, byID map[string]domain.StorageBackupRetentionCopy, eligible map[string]bool) (domain.HostingStorageBackupRetentionExecution, error) {
	var zero domain.HostingStorageBackupRetentionExecution
	var e error
	id := out.ID
	runnerID := chosen[0].RunnerID
	for _, c := range chosen {
		fresh, ok := byID[c.Item.CopyID]
		if !ok || !eligible[c.Item.CopyID] || c.RunnerID != runnerID || fresh.IdentityHash != c.IdentityHash || fresh.Item.Availability != "available" {
			return zero, ErrConflict
		}
		out.Items = append(out.Items, domain.BackupRetentionExecutionItem{CopyID: c.Item.CopyID, BackupID: c.Item.BackupID, Location: c.Item.Location, State: "pending", ReservationHeld: true})
		if c.Item.Location == "local" {
			out.LocalState = "pending"
		} else {
			out.RemoteState = "pending"
		}
	}
	plan := domain.HostingStorageBackupRetentionPlan{Operation: out, ActorID: actor, RunnerID: runnerID, Copies: chosen, TimeoutSeconds: 900}
	plan.Hash = domain.HostingDocumentHash(plan)
	if plan.Validate() != nil {
		return zero, ErrConflict
	}
	if e = storageRetentionCurrent(ctx, tx, plan); e != nil {
		return zero, e
	}
	for _, copy := range chosen {
		active, e := storageRetentionActive(ctx, tx, runnerID, copy.Item.BackupID)
		if e != nil {
			return zero, e
		}
		if active {
			return zero, ErrBackupRetentionBusy
		}
	}
	if !s.hostingJobs.StorageBackupRetentionEnabled() {
		return zero, ErrConflict
	}
	e = sqlcgen.New(tx).StorageRetentionInsertExecution(ctx, sqlcgen.StorageRetentionInsertExecutionParams{ID: id, ResourceID: resource, ActorID: actor, RunnerID: runnerID, PreviewID: out.PreviewID, Plan: buildJSON(plan), PlanHash: plan.Hash, Body: buildJSON(out)})
	if e != nil {
		return zero, hostingError(e)
	}
	for _, c := range chosen {
		var location string
		if location, e = sqlcgen.New(tx).StorageRetentionReadCopyLocation(ctx, c.Item.CopyID); e != nil {
			return zero, e
		}
		if e = sqlcgen.New(tx).StorageRetentionInsertItem(ctx, sqlcgen.StorageRetentionInsertItemParams{ExecutionID: id, CopyID: c.Item.CopyID, IdentityHash: c.IdentityHash, LocationHash: location}); e != nil {
			return zero, hostingError(e)
		}
		if e = sqlcgen.New(tx).StorageRetentionReserveCopy(ctx, c.Item.CopyID); e != nil {
			return zero, e
		}
	}
	if _, e = s.hostingJobs.EnqueueStorageBackupRetentionTx(ctx, tx, hostingjobs.StorageBackupRetentionArgs{OperationID: id}); e != nil {
		return zero, e
	}
	if e = storageRetentionSaveRequest(ctx, tx, resource, actor, kind, requestID, hash, out); e != nil {
		return zero, e
	}
	if e = storageRetentionAudit(ctx, tx, actor, id, phase, out.ProjectID); e != nil {
		return zero, e
	}
	return out, archiveFinalizeActor(ctx, tx, actor)
}

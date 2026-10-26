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

func storageDeletionScope(ctx context.Context, tx pgx.Tx, actor, resource, backup, role string) (storageRetentionScope, error) {
	scope, e := storageRetentionScopeAuthority(ctx, tx, actor, resource, role)
	if e != nil {
		return scope, e
	}
	owner, e := sqlcgen.New(tx).StorageRetentionBackupResource(ctx, backup)
	if e != nil {
		return scope, hostingError(e)
	}
	if owner != resource {
		return scope, ErrNotFound
	}
	return scope, nil
}

// PreviewStorageBackupDeletion previews deletion of the selected backup copy.
func (s *PostgresStore) PreviewStorageBackupDeletion(ctx context.Context, id, actor, resource, backup string, in domain.HostingStorageBackupDeletionPreviewInput) (domain.HostingStorageBackupRetentionPreview, error) {
	var zero domain.HostingStorageBackupRetentionPreview
	if in.Validate() != nil {
		return zero, domain.ErrInvalidStorageBackupRetention
	}
	return archiveActorTransaction(ctx, s, actor, func(tx pgx.Tx) (domain.HostingStorageBackupRetentionPreview, error) {
		scope, e := storageDeletionScope(ctx, tx, actor, resource, backup, domain.RoleOwner)
		if e != nil {
			return zero, e
		}
		if e = storageRetentionLock(ctx, tx); e != nil {
			return zero, e
		}
		hash := domain.HostingDocumentHash(struct {
			Backup string
			Input  domain.HostingStorageBackupDeletionPreviewInput
		}{backup, in})
		if replay, e := storageRetentionReplay(ctx, tx, resource, actor, "deletion_preview", in.RequestID, hash, &zero); replay || e != nil {
			return zero, e
		}
		if scope.Resource.Generation != in.ExpectedResourceGeneration {
			return zero, ErrConflict
		}
		inventory, e := storageRetentionInventory(ctx, tx, resource)
		if e != nil {
			return zero, e
		}
		chosen, final, e := domain.SelectStorageBackupDeletion(backup, in.CopyIDs, inventory)
		if e != nil {
			return zero, ErrConflict
		}
		now := time.Now().UTC()
		out := domain.HostingStorageBackupRetentionPreview{ID: id, Origin: domain.StorageBackupManualSelection, Version: domain.HostingStorageBackupDeletionVersion, ProjectID: scope.Resource.ProjectID, ResourceID: resource, BackupID: backup, ResourceGeneration: scope.Resource.Generation, FinalCopy: final, EvaluatedAt: now, CreatedAt: now, Items: []domain.BackupRetentionItem{}}
		for _, c := range chosen {
			out.Items = append(out.Items, c.Item)
		}
		out.PlanHash = domain.HostingDocumentHash(struct {
			Preview domain.HostingStorageBackupRetentionPreview
			Copies  []domain.StorageBackupRetentionCopy
		}{out, chosen})
		if e = sqlcgen.New(tx).StorageRetentionInsertPreview(ctx, sqlcgen.StorageRetentionInsertPreviewParams{ID: id, ResourceID: resource, ActorID: actor, PolicyRevision: 0, Body: buildJSON(out), Copies: buildJSON(chosen), PlanHash: out.PlanHash}); e != nil {
			return zero, e
		}
		if e = storageRetentionSaveRequest(ctx, tx, resource, actor, "deletion_preview", in.RequestID, hash, out); e != nil {
			return zero, e
		}
		return out, archiveFinalizeActor(ctx, tx, actor)
	})
}

// ExecuteStorageBackupDeletion admits manual deletion from a saved backup preview.
func (s *PostgresStore) ExecuteStorageBackupDeletion(ctx context.Context, id, actor, resource, backup string, in domain.HostingStorageBackupDeletionInput) (domain.HostingStorageBackupRetentionExecution, error) {
	var zero domain.HostingStorageBackupRetentionExecution
	if in.Validate() != nil {
		return zero, domain.ErrInvalidStorageBackupRetention
	}
	return archiveActorTransaction(ctx, s, actor, func(tx pgx.Tx) (domain.HostingStorageBackupRetentionExecution, error) {
		scope, e := storageDeletionScope(ctx, tx, actor, resource, backup, domain.RoleOwner)
		if e != nil {
			return zero, e
		}
		if e = storageRetentionLock(ctx, tx); e != nil {
			return zero, e
		}
		hash := domain.HostingDocumentHash(struct {
			Backup string
			Input  domain.HostingStorageBackupDeletionInput
		}{backup, in})
		if replay, e := storageRetentionReplay(ctx, tx, resource, actor, "deletion_execution", in.RequestID, hash, &zero); replay || e != nil {
			return zero, e
		}
		saved, e := sqlcgen.New(tx).StorageRetentionReadPreview(ctx, sqlcgen.StorageRetentionReadPreviewParams{ID: in.PreviewID, ResourceID: resource, PolicyRevision: 0})
		if e != nil {
			return zero, hostingError(e)
		}
		var preview domain.HostingStorageBackupRetentionPreview
		var chosen []domain.StorageBackupRetentionCopy
		if json.Unmarshal(saved.Body, &preview) != nil || json.Unmarshal(saved.Copies, &chosen) != nil || preview.Version != domain.HostingStorageBackupDeletionVersion || preview.Origin != domain.StorageBackupManualSelection || preview.BackupID != backup || preview.PlanHash != in.ExpectedPlanHash || len(chosen) == 0 || len(chosen) > domain.BackupRetentionBatchLimit || preview.ResourceGeneration != scope.Resource.Generation || preview.FinalCopy && !in.ConfirmFinalCopy {
			return zero, ErrConflict
		}
		// Existing execution resumes the same immutable copies, including held intents.
		existing, e := sqlcgen.New(tx).StorageRetentionFindExecution(ctx, sqlcgen.StorageRetentionFindExecutionParams{PreviewID: in.PreviewID, ResourceID: resource})
		if e == nil {
			return s.storageRetentionResumeTx(ctx, tx, existing, actor, resource, domain.HostingStorageBackupRetentionExecutionInput{RequestID: in.RequestID, PreviewID: in.PreviewID, ExpectedPlanHash: in.ExpectedPlanHash}, hash, "deletion_execution")
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return zero, e
		}
		inventory, e := storageRetentionInventory(ctx, tx, resource)
		if e != nil {
			return zero, e
		}
		ids := []string{}
		byID := map[string]domain.StorageBackupRetentionCopy{}
		eligible := map[string]bool{}
		for _, c := range chosen {
			ids = append(ids, c.Item.CopyID)
			eligible[c.Item.CopyID] = true
		}
		for _, c := range inventory {
			byID[c.Item.CopyID] = c
		}
		_, final, e := domain.SelectStorageBackupDeletion(backup, ids, inventory)
		if e != nil || final != preview.FinalCopy || final && !in.ConfirmFinalCopy {
			return zero, ErrConflict
		}
		now := time.Now().UTC()
		out := domain.HostingStorageBackupRetentionExecution{ID: id, Origin: domain.StorageBackupManualSelection, Version: domain.HostingStorageBackupDeletionVersion, ProjectID: scope.Resource.ProjectID, ResourceID: resource, BackupID: backup, ResourceGeneration: preview.ResourceGeneration, FinalCopy: preview.FinalCopy, PreviewID: preview.ID, PlanHash: preview.PlanHash, State: "queued", ReservationHeld: true, LocalState: "not_selected", RemoteState: "not_selected", Items: []domain.BackupRetentionExecutionItem{}, CreatedAt: now, UpdatedAt: now}
		return s.storageRetentionAdmitTx(ctx, tx, actor, resource, in.RequestID, hash, "deletion_execution", "manual_deletion_requested", out, chosen, byID, eligible)
	})
}

// ListStorageBackupDeletions lists manual deletion executions for the selected backup.
func (s *PostgresStore) ListStorageBackupDeletions(ctx context.Context, actor, resource, backup string) ([]domain.HostingStorageBackupRetentionExecution, error) {
	return archiveActorTransaction(ctx, s, actor, func(tx pgx.Tx) ([]domain.HostingStorageBackupRetentionExecution, error) {
		if _, e := storageDeletionScope(ctx, tx, actor, resource, backup, domain.RoleViewer); e != nil {
			return nil, e
		}
		rows, e := sqlcgen.New(tx).StorageDeletionListExecutions(ctx, sqlcgen.StorageDeletionListExecutionsParams{ResourceID: resource, BackupID: backup})
		if e != nil {
			return nil, e
		}
		out := []domain.HostingStorageBackupRetentionExecution{}
		for _, raw := range rows {
			var v domain.HostingStorageBackupRetentionExecution
			if json.Unmarshal(raw, &v) != nil || !domain.ValidStorageBackupDeletionOperation(v) {
				return nil, ErrConflict
			}
			out = append(out, v)
		}
		return out, nil
	})
}

// GetStorageBackupDeletion returns an authorized manual backup deletion execution.
func (s *PostgresStore) GetStorageBackupDeletion(ctx context.Context, actor, resource, backup, id string) (domain.HostingStorageBackupRetentionExecution, error) {
	return archiveActorTransaction(ctx, s, actor, func(tx pgx.Tx) (domain.HostingStorageBackupRetentionExecution, error) {
		var out domain.HostingStorageBackupRetentionExecution
		if _, e := storageDeletionScope(ctx, tx, actor, resource, backup, domain.RoleViewer); e != nil {
			return out, e
		}
		raw, e := sqlcgen.New(tx).StorageRetentionReadExecutionBody(ctx, sqlcgen.StorageRetentionReadExecutionBodyParams{ID: id, ResourceID: resource})
		if e != nil {
			return out, hostingError(e)
		}
		if json.Unmarshal(raw, &out) != nil || out.BackupID != backup || out.Origin != domain.StorageBackupManualSelection || !domain.ValidStorageBackupDeletionOperation(out) {
			return domain.HostingStorageBackupRetentionExecution{}, ErrNotFound
		}
		return out, nil
	})
}

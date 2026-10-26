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

func archiveLoadRestore(ctx context.Context, db sqlcgen.DBTX, id string) (domain.HostingStorageRestore, error) {
	var result domain.HostingStorageRestore
	op, found, err := archiveOperation(ctx, db, id)
	if err != nil {
		return result, err
	}
	if !found || op.StorageArchive.Plan.Target == nil || op.StorageArchive.Mode != "restore" {
		return result, ErrNotFound
	}
	return domain.HostingStorageRestore{Resource: op.StorageArchive.Plan.Target.Resource, Storage: op.StorageArchive.Plan.Target.Storage, Operation: op}, nil
}

// RequestStorageRestore clones only pinned image and mount metadata into a new
// held resource on the same project/destination. It grants no source stop rights.
func (s *PostgresStore) RequestStorageRestore(ctx context.Context, id, backupID, actor, resourceID, bindingID string, in domain.HostingStorageRestoreInput, event domain.AuditEvent) (domain.HostingStorageRestore, error) {
	if in.Validate() != nil || resourceID == "" || bindingID == "" {
		return domain.HostingStorageRestore{}, domain.ErrInvalidStorageArchive
	}
	return archiveActorTransaction(ctx, s, actor, func(tx pgx.Tx) (domain.HostingStorageRestore, error) {
		var zero domain.HostingStorageRestore
		q := sqlcgen.New(tx)
		if err := storageArchiveSchema(ctx, q); err != nil {
			return zero, err
		}
		backup, err := q.ReadStorageBackup(ctx, backupID)
		if err != nil {
			return zero, hostingError(err)
		}
		if err = lockHostingResourceDestination(ctx, tx, backup.ResourceID); err != nil {
			return zero, err
		}
		source, binding, err := hostingStoragePlacement(ctx, tx, actor, backup.ResourceID, domain.RoleOwner)
		if err != nil {
			return zero, err
		}
		if err = s.archiveConfigured(ctx, q); err != nil {
			return zero, err
		}
		hash := domain.HostingDocumentHash(struct {
			Actor, Backup string
			Input         domain.HostingStorageRestoreInput
		}{actor, backupID, in})
		prior, err := q.ReadStorageRestoreRequest(ctx, sqlcgen.ReadStorageRestoreRequestParams{BackupID: backupID, RequestID: in.RequestID})
		if err == nil {
			if prior.CreatedBy != actor || prior.RequestHash != hash {
				return zero, ErrConflict
			}
			if _, _, e := hostingStoragePlacement(ctx, tx, actor, prior.ResourceID, domain.RoleOwner); e != nil {
				return zero, e
			}
			return archiveLoadRestore(ctx, tx, prior.OperationID)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if err := storageRetentionArchiveAdmission(ctx, tx, backupID, in.RemoteSource); err != nil {
			return zero, err
		}
		original, found, err := archiveOperation(ctx, tx, backup.OperationID)
		if err != nil {
			return zero, err
		}
		if !found || original.State != "succeeded" || original.StorageArchive.Mode != "backup" {
			return zero, ErrConflict
		}
		raw, err := q.ReadStorageArchiveBlob(ctx, backupID)
		if err != nil {
			return zero, hostingError(err)
		}
		var blob domain.HostingStorageArchiveBlob
		if json.Unmarshal(raw, &blob) != nil || blob.Validate() != nil {
			return zero, ErrConflict
		}
		p := original.StorageArchive.Plan
		if p.SourceStorage.ResourceID != source.ID || p.ProjectID != source.ProjectID || p.DestinationID != binding.DestinationID || resourceID == source.ID || bindingID == binding.ID {
			return zero, ErrConflict
		}
		now := time.Now().UTC()
		savedSource, err := q.GetResource(ctx, source.ID)
		if err != nil {
			return zero, err
		}
		imageSource := domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: p.SourceHealthy.Release.CanonicalImage}}
		imageRaw, err := json.Marshal(imageSource)
		if err != nil {
			return zero, err
		}
		created, err := q.CreateResource(ctx, sqlcgen.CreateResourceParams{ID: resourceID, ProjectID: source.ProjectID, ProjectEnvironmentID: savedSource.ProjectEnvironmentID, Name: in.Name, Kind: "application", Source: imageRaw, CreatedBy: actor, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			return zero, err
		}
		resource, err := resourceFromHostingSQL(created)
		if err != nil {
			return zero, err
		}
		namespace, err := newLeaseToken("ncd")
		if err != nil {
			return zero, err
		}
		if _, err = q.CreateResourceBinding(ctx, sqlcgen.CreateResourceBindingParams{ID: bindingID, ResourceID: resourceID, DestinationID: binding.DestinationID, RuntimeNamespace: namespace, CreatedBy: actor, CreatedAt: now}); err != nil {
			return zero, err
		}
		st := p.SourceStorage
		st.ID, st.VolumeName = domain.HostingStorageIdentity(bindingID)
		st.ResourceID = resourceID
		st.BindingID = bindingID
		st.CreatedBy = actor
		st.CreatedAt = now
		st.Hash = st.DocumentHash()
		stored, err := q.InsertHostingStorage(ctx, sqlcgen.InsertHostingStorageParams{ID: st.ID, ProjectID: st.ProjectID, ResourceID: st.ResourceID, BindingID: st.BindingID, DestinationID: st.DestinationID, Kind: st.Kind, Driver: st.Driver, VolumeName: st.VolumeName, MountPath: st.MountPath, AccessMode: st.AccessMode, Retention: st.Retention, BodyHash: st.Hash, CreatedBy: actor})
		if err != nil {
			return zero, err
		}
		st, err = hostingStorageFromSQL(stored)
		if err != nil {
			return zero, err
		}
		storageAudit := domain.AuditEvent{ID: domain.HostingStorageAuditID(resourceID, in.RequestID), ActorID: actor, Action: "hosting.storage.created", TargetID: st.ID, CreatedAt: now, Metadata: map[string]any{"project_id": st.ProjectID, "resource_id": st.ResourceID, "binding_id": st.BindingID, "destination_id": st.DestinationID, "storage_generation": st.Generation, "storage_hash": st.Hash}}
		if err = createAuditWithQueries(ctx, q, storageAudit); err != nil {
			return zero, err
		}
		if err = q.InsertHostingStorageReceipt(ctx, sqlcgen.InsertHostingStorageReceiptParams{ResourceID: resourceID, RequestID: in.RequestID, StorageID: st.ID, ActorID: actor, RequestHash: hash, ExpectedResourceGeneration: 1, AuditID: storageAudit.ID}); err != nil {
			return zero, err
		}
		d := domain.HostingStoragePlan{Reference: st.Reference(), VolumeName: st.VolumeName, MountPath: st.MountPath, CreationOperationID: id, DaemonID: p.Helper.DaemonID}
		d.CreationNonce = domain.HostingStorageCreationNonce(d.Reference, id, d.DaemonID)
		p.OperationID = id
		p.Mode = "restore"
		p.RemoteTarget = nil
		p.RemoteSource = in.RemoteSource
		p.Target = &domain.HostingStorageArchiveTarget{Resource: resource, Storage: st, Plan: d}
		p.Writers = nil
		p.Blob = &blob
		p.Helper = s.storageArchiveConfiguration.helper
		p.Limits = s.storageArchiveConfiguration.limits
		p.Hash = ""
		p.Hash = domain.HostingDocumentHash(p)
		if err = archiveRunner(ctx, tx, p); err != nil {
			return zero, err
		}
		if err = archiveInsertPlan(ctx, tx, p, actor, "storage_restore_"+in.RequestID, hash); err != nil {
			return zero, err
		}
		if err = q.InsertStorageRestore(ctx, sqlcgen.InsertStorageRestoreParams{OperationID: id, BackupID: backupID, ResourceID: resourceID, RequestID: in.RequestID, RequestHash: hash, CreatedBy: actor}); err != nil {
			return zero, err
		}
		// Reserve ordinary future image capacity through the existing port guard.
		// No container, route, readiness claim or application release is created.
		snap := p.SourceHealthy.Snapshot
		snap.ResourceID = resourceID
		snap.BindingID = bindingID
		snap.RuntimeNamespace = namespace
		snap.Storage = st.Reference()
		snap.StorageRouteContinuity = domain.HostingStorageRouteContinuityRef{}
		if err = s.resolveHostingReleasePort(ctx, tx, &snap, 0); err != nil {
			return zero, err
		}
		if err = q.ReserveStorageArchiveRestoreTarget(ctx, sqlcgen.ReserveStorageArchiveRestoreTargetParams{BindingID: bindingID, DestinationID: p.DestinationID, HostPort: int32(snap.HostPort), DaemonID: &p.Helper.DaemonID, HoldOperationID: &id}); err != nil {
			return zero, err
		}
		op, _, err := archiveOperation(ctx, tx, id)
		if err != nil {
			return zero, err
		}
		if err = archiveAudit(ctx, tx, actor, "requested", op); err != nil {
			return zero, err
		}
		if in.RemoteSource != nil {
			if err = s.storageRemoteRestoreAdmit(ctx, tx, p, actor, *in.RemoteSource); err != nil {
				return zero, err
			}
		} else if _, err = s.hostingJobs.EnqueueStorageArchiveTx(ctx, tx, hostingjobs.PrepareStorageArchiveArgs{OperationID: id}); err != nil {
			return zero, err
		}
		_ = event
		return archiveLoadRestore(ctx, tx, id)
	})
}

// GetStorageRestore requires current viewer membership and both destination grants.
func (s *PostgresStore) GetStorageRestore(ctx context.Context, actor, id string) (domain.HostingStorageRestore, error) {
	return archiveActorTransaction(ctx, s, actor, func(tx pgx.Tx) (domain.HostingStorageRestore, error) {
		var zero domain.HostingStorageRestore
		q := sqlcgen.New(tx)
		if err := storageArchiveSchema(ctx, q); err != nil {
			return zero, err
		}
		row, err := q.ReadStorageRestore(ctx, id)
		if err != nil {
			return zero, hostingError(err)
		}
		if _, _, err = hostingStoragePlacement(ctx, tx, actor, row.ResourceID, domain.RoleViewer); err != nil {
			return zero, err
		}
		result, err := archiveLoadRestore(ctx, tx, id)
		if err != nil {
			return zero, err
		}
		if _, _, err = hostingStoragePlacement(ctx, tx, actor, result.Operation.StorageArchive.Plan.SourceStorage.ResourceID, domain.RoleViewer); err != nil {
			return zero, err
		}
		return result, nil
	})
}

func readArchiveRestoreMaterialization(ctx context.Context, tx pgx.Tx, st domain.HostingStorage) (domain.HostingStorageMaterialization, error) {
	var m domain.HostingStorageMaterialization
	q := sqlcgen.New(tx)
	row, err := q.ReadStorageRestoreOrigin(ctx, st.ID)
	if err != nil {
		return m, err
	}
	if json.Unmarshal(row.Origin, &m) != nil || m.Validate(st) != nil || domain.HostingDocumentHash(m) != row.OriginHash {
		return m, ErrConflict
	}
	r, err := q.ReadStorageRestoreMaterialized(ctx, st.ID)
	if err == nil {
		m.State = "materialized"
		m.MaterializedReceipt = &domain.HostingReceipt{ID: r.ReceiptID, BodyHash: r.BodyHash, CreatedAt: r.CreatedAt}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return m, err
	}
	if m.Validate(st) != nil {
		return m, ErrConflict
	}
	return m, nil
}

package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

// The project target registry is shared configuration only. Storage uses its own
// validation, capability, immutable transfer plans, receipts and blob format.
func storageRemoteAudit(ctx context.Context, tx pgx.Tx, actor, id, phase, project string) error {
	return createAuditWithQueries(ctx, sqlcgen.New(tx), domain.AuditEvent{ID: "aud_storage_remote_" + phase + "_" + id, ActorID: actor, Action: "hosting.storage_backup_remote." + phase, TargetID: id, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"project_id": project}})
}
func (s *PostgresStore) storageRemoteSelect(ctx context.Context, tx pgx.Tx, p domain.HostingStorageArchivePlan, actor string, selection domain.BackupTargetSelection) error {
	var revision int64
	var err error
	revision, err = sqlcgen.New(tx).LockStorageRemoteRevision(ctx, sqlcgen.LockStorageRemoteRevisionParams{ID: selection.TargetID, ProjectID: p.ProjectID})
	if err != nil {
		return hostingError(err)
	}
	if revision != selection.Revision {
		return ErrConflict
	}
	snapshot, err := backupRemoteTargetSnapshot(ctx, tx, selection.TargetID, selection.Revision)
	if err != nil {
		return err
	}
	var validated bool
	validated, err = sqlcgen.New(tx).StorageRemoteTargetValidated(ctx, sqlcgen.StorageRemoteTargetValidatedParams{TargetID: selection.TargetID, TargetRevision: selection.Revision, DestinationID: p.DestinationID, RunnerID: p.RunnerID, DaemonID: p.Helper.DaemonID})
	if err != nil {
		return err
	}
	if !validated {
		return ErrConflict
	}
	now := time.Now().UTC()
	v := domain.HostingStorageBackupTransfer{ID: "selection", Version: domain.HostingStorageBackupTransferVersion, ProjectID: p.ProjectID, TargetID: selection.TargetID, TargetRevision: selection.Revision, DestinationID: p.DestinationID, Mode: "validate", State: "queued", CreatedAt: now, UpdatedAt: now}
	check := domain.HostingStorageBackupTransferPlan{Operation: v, Target: snapshot, ActorID: actor, RunnerID: p.RunnerID, ServerID: p.ServerID, MaxBytes: p.Limits.ArchiveBytes, TimeoutSeconds: 3600}
	check.Hash = domain.HostingDocumentHash(check)
	return storageRemoteCurrent(ctx, tx, check)
}
func (s *PostgresStore) storageRemoteLocalComplete(ctx context.Context, tx pgx.Tx, p domain.HostingStorageArchivePlan, actor string, blob domain.HostingStorageArchiveBlob) error {
	if p.RemoteTarget == nil {
		return nil
	}
	selection := p.RemoteTarget
	snapshot, err := backupRemoteTargetSnapshot(ctx, tx, selection.TargetID, selection.Revision)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	v := domain.HostingStorageBackupTransfer{ID: "storage_upload_" + p.BackupID, Version: domain.HostingStorageBackupTransferVersion, ProjectID: p.ProjectID, TargetID: selection.TargetID, TargetRevision: selection.Revision, DestinationID: p.DestinationID, Mode: "upload", BackupID: p.BackupID, State: "queued", Blob: &blob, CreatedAt: now, UpdatedAt: now}
	plan := domain.HostingStorageBackupTransferPlan{Operation: v, Target: snapshot, ActorID: actor, RunnerID: p.RunnerID, ServerID: p.ServerID, DaemonID: p.Helper.DaemonID, LocationID: p.BackupID, ObjectKey: domain.StorageRemoteObjectKey(p.BackupID, blob), MaxBytes: p.Limits.ArchiveBytes, TimeoutSeconds: 3600, Limits: p.Limits}
	plan.Hash = domain.HostingDocumentHash(plan)
	return s.storageRemoteInsert(ctx, tx, plan, "upload_"+p.BackupID, domain.HostingDocumentHash(struct{ Backup, Plan string }{p.BackupID, p.Hash}), nil)
}

// RetryStorageBackupTransfer admits a retry of the selected failed storage backup upload.
func (s *PostgresStore) RetryStorageBackupTransfer(ctx context.Context, id, actor, backup string, in domain.HostingStorageBackupTransferRetryInput) (domain.HostingStorageBackupTransfer, error) {
	return archiveActorTransaction(ctx, s, actor, func(tx pgx.Tx) (domain.HostingStorageBackupTransfer, error) {
		var zero domain.HostingStorageBackupTransfer
		if in.Validate() != nil {
			return zero, domain.ErrInvalidStorageBackupRemote
		}
		var resource string
		var err error
		resource, err = sqlcgen.New(tx).LockStorageRemoteBackupResource(ctx, backup)
		if err != nil {
			return zero, hostingError(err)
		}
		if _, _, err := hostingStoragePlacement(ctx, tx, actor, resource, domain.RoleOwner); err != nil {
			return zero, err
		}
		hash := domain.HostingDocumentHash(struct {
			Actor, Backup string
			Input         domain.HostingStorageBackupTransferRetryInput
		}{actor, backup, in})
		var prior, priorHash string
		readStorageRemoteBackupRequestRow, err := sqlcgen.New(tx).ReadStorageRemoteBackupRequest(ctx, sqlcgen.ReadStorageRemoteBackupRequestParams{ID: backup, RequestID: in.RequestID})
		prior, priorHash = readStorageRemoteBackupRequestRow.ID, readStorageRemoteBackupRequestRow.RequestHash
		if err == nil {
			if hash != priorHash {
				return zero, ErrConflict
			}
			r, e := storageRemoteLoad(ctx, tx, prior, false)
			return r.view, e
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		old, err := storageRemoteLoad(ctx, tx, in.ExpectedTransferID, true)
		if err != nil {
			return zero, err
		}
		if old.view.BackupID != backup || old.view.Mode == "validate" || old.view.State != "failed" && old.view.State != "uncertain" {
			return zero, ErrConflict
		}
		var latest string
		latest, err = sqlcgen.New(tx).LatestStorageRemoteTransfer(ctx, sqlcgen.LatestStorageRemoteTransferParams{BackupID: &backup, Mode: old.view.Mode, RestoreOperationID: optionalStorageRestore(old.view.RestoreOperationID)})
		if err != nil {
			return zero, hostingError(err)
		}
		if latest != in.ExpectedTransferID {
			return zero, ErrConflict
		}
		if old.view.Mode == "download" {
			_, err = sqlcgen.New(tx).RetryStorageRemoteRestore(ctx, old.view.RestoreOperationID)
			if err != nil {
				return zero, hostingError(err)
			}
		}
		p := old.plan
		p.ActorID = actor
		p.Operation.ID = id
		p.Operation.State = "queued"
		p.Operation.FailureCode = ""
		p.Operation.CreatedAt = time.Now().UTC()
		p.Operation.UpdatedAt = p.Operation.CreatedAt
		p.Hash = ""
		p.Hash = domain.HostingDocumentHash(p)
		if err = storageRemoteCurrent(ctx, tx, p); err != nil {
			return zero, err
		}
		if err = s.storageRemoteInsert(ctx, tx, p, in.RequestID, hash, &latest); err != nil {
			return zero, err
		}
		return p.Operation, nil
	})
}
func (s *PostgresStore) storageRemoteRestorePlan(ctx context.Context, tx pgx.Tx, p domain.HostingStorageArchivePlan, actor string, source domain.BackupRemoteSource) (domain.HostingStorageBackupTransferPlan, error) {
	upload, err := storageRemoteLoad(ctx, tx, source.TransferID, false)
	if err != nil {
		return domain.HostingStorageBackupTransferPlan{}, err
	}
	if upload.view.Mode != "upload" || upload.view.State != "succeeded" || upload.view.BackupID != p.BackupID || upload.view.TargetRevision != source.ExpectedTargetRevision || upload.view.ProjectID != p.ProjectID || upload.view.DestinationID != p.DestinationID || upload.plan.RunnerID != p.RunnerID || upload.plan.DaemonID != p.Helper.DaemonID || domain.HostingDocumentHash(upload.view.Blob) != domain.HostingDocumentHash(p.Blob) {
		return upload.plan, ErrConflict
	}
	check := upload.plan
	check.ActorID = actor
	check.Hash = ""
	check.Hash = domain.HostingDocumentHash(check)
	return check, storageRemoteCurrent(ctx, tx, check)
}
func (s *PostgresStore) storageRemoteRestoreCurrent(ctx context.Context, tx pgx.Tx, p domain.HostingStorageArchivePlan, actor string) error {
	_, err := s.storageRemoteRestorePlan(ctx, tx, p, actor, *p.RemoteSource)
	return err
}
func (s *PostgresStore) storageRemoteRestoreAdmit(ctx context.Context, tx pgx.Tx, p domain.HostingStorageArchivePlan, actor string, source domain.BackupRemoteSource) error {
	check, err := s.storageRemoteRestorePlan(ctx, tx, p, actor, source)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	check.Operation.ID = "storage_download_" + p.OperationID
	check.Operation.Mode = "download"
	check.Operation.RestoreOperationID = p.OperationID
	check.Operation.State = "queued"
	check.Operation.CreatedAt = now
	check.Operation.UpdatedAt = now
	check.Limits = p.Limits
	check.MaxBytes = p.Limits.ArchiveBytes
	check.Hash = ""
	check.Hash = domain.HostingDocumentHash(check)
	return s.storageRemoteInsert(ctx, tx, check, "download_"+p.OperationID, domain.HostingDocumentHash(source), &source.TransferID)
}
func (s *PostgresStore) storageRemoteDownloadComplete(ctx context.Context, tx pgx.Tx, p domain.HostingStorageBackupTransferPlan) error {
	if p.Operation.Mode != "download" {
		return nil
	}
	_, err := s.hostingJobs.EnqueueStorageArchiveTx(ctx, tx, hostingjobs.PrepareStorageArchiveArgs{OperationID: p.Operation.RestoreOperationID})
	return err
}

func optionalStorageRestore(id string) *string {
	if id == "" {
		return nil
	}
	return &id
}

func storageRemoteDownloadFailed(ctx context.Context, tx pgx.Tx, p domain.HostingStorageBackupTransferPlan) error {
	if p.Operation.Mode != "download" {
		return nil
	}
	// No extraction task was admitted. Keep new-volume ownership held and expose a
	// terminal dependency failure. A separately authorized download retry may queue it.
	err := sqlcgen.New(tx).FailStorageRemoteRestore(ctx, p.Operation.RestoreOperationID)
	return err
}

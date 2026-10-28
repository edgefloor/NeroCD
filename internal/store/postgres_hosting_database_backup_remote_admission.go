package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func backupRemoteSelect(ctx context.Context, tx pgx.Tx, p domain.HostingDatabaseBackupPlan, selection domain.BackupTargetSelection) (domain.HostingDatabaseBackupTargetSnapshot, error) {
	var zero domain.HostingDatabaseBackupTargetSnapshot
	if selection.Validate() != nil || domain.HostingDatabaseBackupTransferVersionFor(p.Operation.Version) == "" {
		return zero, ErrConflict
	}
	var current int64
	current, err := sqlcgen.New(tx).LockDatabaseBackupTargetRevision(ctx, sqlcgen.LockDatabaseBackupTargetRevisionParams{ID: selection.TargetID, ProjectID: p.Operation.ProjectID})
	if err != nil {
		return zero, hostingError(err)
	}
	if current != selection.Revision {
		return zero, ErrConflict
	}
	snapshot, err := backupRemoteTargetSnapshot(ctx, tx, selection.TargetID, selection.Revision)
	if err != nil {
		return zero, err
	}
	var validated bool
	validated, err = sqlcgen.New(tx).DatabaseBackupRemoteTargetValidated(ctx, sqlcgen.DatabaseBackupRemoteTargetValidatedParams{TargetID: selection.TargetID, TargetRevision: int64(selection.Revision), DestinationID: p.Source.Snapshot.DestinationID, RunnerID: p.RunnerID, DaemonID: p.Source.Release.DaemonID})
	if err != nil {
		return zero, err
	}
	if !validated {
		return zero, ErrConflict
	}
	if p.Operation.Version == domain.HostingDatabaseMySQLBackupVersion {
		present, schemaErr := sqlcgen.New(tx).MySQLRemoteBackupSchemaPresent(ctx)
		if schemaErr != nil {
			return zero, schemaErr
		}
		if !present {
			return zero, ErrConflict
		}
		if _, err = sqlcgen.New(tx).LockDatabaseBackupRemoteRunner(ctx, sqlcgen.LockDatabaseBackupRemoteRunnerParams{ID: p.RunnerID, Column2: domain.HostingDatabaseMySQLBackupRemoteCapability, Column3: domain.RunTypeDatabaseBackupTransfer}); err != nil {
			return zero, hostingError(err)
		}
	}
	// Use a closed validation-shaped plan for the common current authority guard.
	now := time.Now().UTC()
	view := domain.HostingDatabaseBackupTransfer{ID: "selection", Version: domain.HostingDatabaseBackupTransferVersion, ProjectID: p.Operation.ProjectID, TargetID: selection.TargetID, TargetRevision: selection.Revision, DestinationID: p.Source.Snapshot.DestinationID, Mode: "validate", State: "queued", CreatedAt: now, UpdatedAt: now}
	check := domain.HostingDatabaseBackupTransferPlan{Operation: view, Target: snapshot, ActorID: p.ActorID, RunnerID: p.RunnerID, ServerID: p.Source.Snapshot.ServerID, MaxBytes: p.MaxBytes, TimeoutSeconds: p.TimeoutSeconds}
	check.Hash = domain.HostingDocumentHash(check)
	return snapshot, backupRemoteCurrent(ctx, tx, check)
}

func (s *PostgresStore) backupRemoteLocalComplete(ctx context.Context, tx pgx.Tx, r sqlcgen.HostingDatabaseBackup) error {
	p, err := databaseBackupPlan(r)
	if err != nil {
		return err
	}
	if r.Mode != "backup" || p.Operation.RemoteTarget == nil {
		return nil
	}
	selection := p.Operation.RemoteTarget
	snapshot, err := backupRemoteTargetSnapshot(ctx, tx, selection.TargetID, selection.Revision)
	if err != nil {
		return err
	}
	view, err := databaseBackupView(r)
	if err != nil {
		return err
	}
	if view.State != "succeeded" || view.Blob == nil {
		return ErrConflict
	}
	now := time.Now().UTC()
	id := "db_upload_" + r.ID
	out := domain.HostingDatabaseBackupTransfer{ID: id, Version: domain.HostingDatabaseBackupTransferVersionFor(p.Operation.Version), ProjectID: r.ProjectID, TargetID: selection.TargetID, TargetRevision: selection.Revision, DestinationID: p.Source.Snapshot.DestinationID, Mode: "upload", BackupID: r.ID, State: "queued", Blob: view.Blob, CreatedAt: now, UpdatedAt: now}
	plan := domain.HostingDatabaseBackupTransferPlan{Operation: out, Target: snapshot, ActorID: p.ActorID, RunnerID: p.RunnerID, ServerID: p.Source.Snapshot.ServerID, DaemonID: p.Source.Release.DaemonID, LocationID: r.ID, ObjectKey: domain.BackupRemoteObjectKey(r.ID, *view.Blob), MaxBytes: p.MaxBytes, TimeoutSeconds: p.TimeoutSeconds}
	plan.Hash = domain.HostingDocumentHash(plan)
	return s.backupRemoteInsert(ctx, tx, plan, "upload_"+r.ID, domain.HostingDocumentHash(struct{ Backup, Plan string }{r.ID, p.Hash}), nil)
}

// RetryDatabaseBackupTransfer admits a retry of the selected failed database backup upload.
func (s *PostgresStore) RetryDatabaseBackupTransfer(ctx context.Context, id, actor, backup string, in domain.HostingDatabaseBackupTransferRetryInput) (domain.HostingDatabaseBackupTransfer, error) {
	if in.Validate() != nil {
		return domain.HostingDatabaseBackupTransfer{}, domain.ErrInvalidDatabaseBackupRemote
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupTransfer, error) {
		var zero domain.HostingDatabaseBackupTransfer
		b, err := sqlcgen.New(tx).LockDatabaseBackup(ctx, backup)
		if err != nil {
			return zero, hostingError(err)
		}
		if err = proxyAuthorize(ctx, tx, actor, b.ProjectID, domain.RoleOwner, false); err != nil {
			return zero, err
		}
		hash := domain.HostingDocumentHash(struct {
			Actor, Backup string
			Input         domain.HostingDatabaseBackupTransferRetryInput
		}{actor, backup, in})
		var prior, priorHash string
		findDatabaseBackupTransferRequestRow, err := sqlcgen.New(tx).FindDatabaseBackupTransferRequest(ctx, sqlcgen.FindDatabaseBackupTransferRequestParams{ProjectID: b.ProjectID, RequestID: in.RequestID})
		prior = findDatabaseBackupTransferRequestRow.ID
		priorHash = findDatabaseBackupTransferRequestRow.RequestHash
		if err == nil {
			if priorHash != hash {
				return zero, ErrConflict
			}
			saved, e := backupRemoteLoad(ctx, tx, prior, false)
			return saved.view, e
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		var latest string
		latest, err = sqlcgen.New(tx).ReadLatestDatabaseBackupUpload(ctx, &backup)
		if err != nil {
			return zero, hostingError(err)
		}
		if latest != in.ExpectedTransferID {
			return zero, ErrConflict
		}
		old, err := backupRemoteLoad(ctx, tx, latest, true)
		if err != nil {
			return zero, err
		}
		if old.view.Mode != "upload" || old.view.BackupID != backup || old.view.State != "failed" && old.view.State != "uncertain" || b.State != "succeeded" {
			return zero, ErrConflict
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
		if err = backupRemoteCurrent(ctx, tx, p); err != nil {
			return zero, err
		}
		if err = s.backupRemoteInsert(ctx, tx, p, in.RequestID, hash, &latest); err != nil {
			return zero, err
		}
		return p.Operation, nil
	})
}

func (s *PostgresStore) backupRemoteRestoreAdmit(ctx context.Context, tx pgx.Tx, p domain.HostingDatabaseBackupPlan, source domain.BackupRemoteSource) error {
	if p.Operation.Version != domain.HostingDatabaseBackupVersion {
		return ErrConflict
	}
	upload, err := backupRemoteLoad(ctx, tx, source.TransferID, false)
	if err != nil {
		return err
	}
	if upload.view.Mode != "upload" || upload.view.State != "succeeded" || upload.view.BackupID != p.Operation.BackupID || upload.view.TargetRevision != source.ExpectedTargetRevision || upload.view.ProjectID != p.Operation.ProjectID || upload.view.DestinationID != p.Source.Snapshot.DestinationID || upload.plan.RunnerID != p.RunnerID || upload.plan.DaemonID != p.Source.Release.DaemonID || domain.HostingDocumentHash(upload.view.Blob) != domain.HostingDocumentHash(p.Operation.Blob) {
		return ErrConflict
	}
	check := upload.plan
	check.ActorID = p.ActorID
	check.Hash = ""
	check.Hash = domain.HostingDocumentHash(check)
	if err = backupRemoteCurrent(ctx, tx, check); err != nil {
		return err
	}
	now := time.Now().UTC()
	check.Operation.ID = "db_download_" + p.Operation.ID
	check.Operation.Mode = "download"
	check.MaxBytes = p.MaxBytes
	check.TimeoutSeconds = p.TimeoutSeconds
	check.Operation.RestoreOperationID = p.Operation.ID
	check.Operation.State = "queued"
	check.Operation.CreatedAt = now
	check.Operation.UpdatedAt = now
	check.Hash = ""
	check.Hash = domain.HostingDocumentHash(check)
	return s.backupRemoteInsert(ctx, tx, check, "download_"+p.Operation.ID, domain.HostingDocumentHash(source), &source.TransferID)
}

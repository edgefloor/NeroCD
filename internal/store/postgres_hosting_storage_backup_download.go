package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func storageDownloadSource(ctx context.Context, tx pgx.Tx, actor, backup string) (domain.HostingStorageBackupDownloadPlan, error) {
	var p domain.HostingStorageBackupDownloadPlan
	q := sqlcgen.New(tx)
	row, err := q.ReadStorageBackup(ctx, backup)
	if err != nil {
		return p, hostingError(err)
	}
	if err = lockHostingResourceDestination(ctx, tx, row.ResourceID); err != nil {
		return p, err
	}
	r, b, err := archivePlacement(ctx, tx, actor, row.ResourceID, domain.RoleOwner)
	if errors.Is(err, ErrConflict) {
		binding, e := q.GetResourceBinding(ctx, row.ResourceID)
		if e == nil {
			if e = databaseBindingGrant(ctx, q, row.ProjectID, binding.DestinationID); e != nil {
				return p, auth.ErrForbidden
			}
		}
	}
	if err != nil {
		return p, err
	}
	if err = storageRetentionLock(ctx, tx); err != nil {
		return p, err
	}
	op, found, err := archiveOperation(ctx, tx, row.OperationID)
	if err != nil {
		return p, err
	}
	if !found || op.State != "succeeded" || op.StorageArchive == nil {
		return p, ErrConflict
	}
	original := op.StorageArchive.Plan
	if b.ID != original.SourceBindingID() || b.DestinationID != original.DestinationID || r.ProjectID != original.ProjectID {
		return p, ErrConflict
	}
	raw, err := q.ReadStorageArchiveBlob(ctx, backup)
	if err != nil {
		return p, hostingError(err)
	}
	if json.Unmarshal(raw, &p.Blob) != nil || p.Blob.Validate() != nil || !archiveBlobMatchesPlan(&p.Blob, original) {
		return p, ErrConflict
	}
	p.ActorID, p.ProjectID, p.ResourceID, p.BindingID = actor, r.ProjectID, r.ID, b.ID
	p.BackupID, p.RunnerID, p.DestinationID, p.DaemonID = backup, original.RunnerID, original.DestinationID, original.Helper.DaemonID
	if original.ComposeSource != nil {
		receipt, e := q.ReadStorageArchiveReceipt(ctx, sqlcgen.ReadStorageArchiveReceiptParams{OperationID: op.ID, Phase: "archive_published"})
		if e != nil {
			return p, hostingError(e)
		}
		var proof domain.HostingStorageArchiveProgress
		if json.Unmarshal(receipt.Body, &proof) != nil || proof.Validate() != nil || receipt.BodyHash != domain.HostingDocumentHash(proof) || receipt.PlanHash != original.Hash || proof.PlanHash != original.Hash || proof.LocalCopy == nil || proof.Blob == nil || *proof.Blob != p.Blob {
			return p, ErrNotFound
		}
		p.LocalCopy = *proof.LocalCopy
		p.CopyID = "sbc_local_" + backup
	} else {
		copies, e := q.ReadStorageDownloadCopies(ctx, backup)
		if e != nil {
			return p, e
		}
		for _, row := range copies {
			if row.Availability == "reserved" && !row.Superseded {
				return p, ErrConflict
			}
			if row.Availability != "available" || row.Superseded {
				continue
			}
			var c domain.StorageBackupRetentionCopy
			if json.Unmarshal(row.Body, &c) != nil || c.Local == nil || c.Local.Validate() != nil {
				return p, ErrConflict
			}
			if c.Item.Digest != p.Blob.SHA256 || c.Item.Bytes != p.Blob.SizeBytes || c.RunnerID != p.RunnerID || c.DestinationID != p.DestinationID || c.DaemonID != p.DaemonID {
				return p, ErrConflict
			}
			reserved, e := q.CheckStorageDownloadReservation(ctx, storageRetentionLocation(c))
			if e != nil {
				return p, e
			}
			if reserved {
				return p, ErrConflict
			}
			p.LocalCopy = *c.Local
			p.CopyID = c.Item.CopyID
			break
		}
		if p.CopyID == "" {
			return p, ErrNotFound
		}
	}
	if p.LocalCopy.Validate() != nil || p.LocalCopy.Bytes != p.Blob.SizeBytes {
		return p, ErrNotFound
	}
	placement, err := q.StorageRemotePlacement(ctx, p.DestinationID)
	if err != nil {
		return p, hostingError(err)
	}
	if placement.RunnerID != p.RunnerID || placement.ServerID != original.ServerID {
		return p, ErrConflict
	}
	_, err = q.LockStorageRemoteRunner(ctx, sqlcgen.LockStorageRemoteRunnerParams{ID: p.RunnerID, Column2: domain.HostingStorageBackupDownloadCapability, Column3: domain.RunTypeStorageBackupTransfer})
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrStorageDownloadUnavailable
	}
	if err != nil {
		return p, err
	}
	return p, archiveFinalizeActor(ctx, tx, actor)
}

// StorageBackupDownloadOptions reports current owner access and local-copy eligibility.
func (s *PostgresStore) StorageBackupDownloadOptions(ctx context.Context, actor, backup string) (domain.HostingStorageBackupDownloadOptions, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingStorageBackupDownloadOptions, error) {
		out := domain.HostingStorageBackupDownloadOptions{BackupID: backup}
		p, err := storageDownloadSource(ctx, tx, actor, backup)
		switch {
		case err == nil:
			out.Available = true
			out.Blob = &p.Blob
		case errors.Is(err, ErrStorageDownloadUnavailable):
			out.ReasonCode = "runner_unavailable"
		case errors.Is(err, ErrNotFound):
			out.ReasonCode = "local_copy_unavailable"
		case errors.Is(err, ErrConflict):
			out.ReasonCode = "backup_not_succeeded"
		default:
			return out, err
		}
		return out, nil
	})
}

// CreateStorageBackupDownload atomically admits one expiring read and its ordinary task.
func (s *PostgresStore) CreateStorageBackupDownload(ctx context.Context, id, actor, backup string, expires time.Time) (domain.HostingStorageBackupDownloadPlan, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingStorageBackupDownloadPlan, error) {
		p, err := storageDownloadSource(ctx, tx, actor, backup)
		if err != nil {
			return p, err
		}
		if !expires.After(time.Now()) || expires.After(time.Now().Add(301*time.Second)) {
			return p, ErrConflict
		}
		p.SessionID = id
		p.ExpiresAt = expires.UTC().Truncate(time.Microsecond)
		p.Hash = domain.HostingDocumentHash(p)
		if p.Validate() != nil {
			return p, ErrConflict
		}
		run := domain.TaskRun{ID: "storage_transfer_" + id, ProjectID: p.ProjectID, RunSpec: domain.RunSpec{Type: domain.RunTypeStorageBackupTransfer, HostingOperationID: id, HostingMode: domain.HostingStorageBackupDownloadMode, Inputs: map[string]any{}}}
		if domain.ValidateStorageBackupTransferTask(run) != nil {
			return p, ErrConflict
		}
		q := sqlcgen.New(tx)
		if _, err = q.CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: run.ID, ProjectID: run.ProjectID, RunSpec: buildJSON(run.RunSpec), RequestedBy: actor}); err != nil {
			return p, err
		}
		err = q.InsertStorageDownload(ctx, sqlcgen.InsertStorageDownloadParams{ID: id, BackupID: backup, ActorID: actor, RunnerID: p.RunnerID, RunID: run.ID, Plan: buildJSON(p), PlanHash: p.Hash, ExpiresAt: p.ExpiresAt})
		if err != nil {
			return p, err
		}
		err = createAuditWithQueries(ctx, q, domain.AuditEvent{ID: "aud_download_requested_" + id, ActorID: actor, Action: "hosting.storage_backup_download.requested", TargetID: id, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"project_id": p.ProjectID, "backup_id": p.BackupID, "copy_id": p.CopyID, "plan_hash": p.Hash}})
		return p, err
	})
}

func storageDownloadCurrent(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority) (domain.HostingStorageBackupDownloadPlan, error) {
	var p domain.HostingStorageBackupDownloadPlan
	q := sqlcgen.New(tx)
	if err := checkDatabaseConnectionRunnerContext(ctx, tx, runner); err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			return p, auth.ErrForbidden
		}
		return p, err
	}
	// Match ordinary transfer lock ordering: task, session, then lease.
	if _, err := q.LockHostingTaskForAuthority(ctx, a.RunID); err != nil {
		return p, hostingError(err)
	}
	row, err := q.LockStorageDownload(ctx, a.OperationID)
	if err != nil {
		return p, hostingError(err)
	}
	if json.Unmarshal(row.Plan, &p) != nil || p.Validate() != nil || p.SessionID != row.ID || p.SessionID != a.OperationID || p.BackupID != row.BackupID || p.ActorID != row.ActorID || p.RunnerID != row.RunnerID || !p.ExpiresAt.Equal(row.ExpiresAt) || row.PlanHash != p.Hash || row.RunnerID != runner || row.RunID != a.RunID {
		return p, ErrConflict
	}
	if _, err = q.CheckStorageDownloadAuthority(ctx, sqlcgen.CheckStorageDownloadAuthorityParams{ID: a.OperationID, RunID: a.RunID, RunnerID: runner, ID_2: a.LeaseID, Attempt: int32(a.Attempt), Fence: a.Fence}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return p, auth.ErrForbidden
		}
		return p, err
	}
	fresh, err := storageDownloadSource(ctx, tx, p.ActorID, p.BackupID)
	if err != nil {
		return p, err
	}
	fresh.SessionID = p.SessionID
	fresh.ExpiresAt = p.ExpiresAt
	fresh.Hash = domain.HostingDocumentHash(fresh)
	if fresh != p {
		return p, ErrConflict
	}
	if row.Authority != nil {
		var prior domain.HostingAuthority
		if json.Unmarshal(row.Authority, &prior) != nil || prior != a {
			return p, ErrConflict
		}
	}
	return p, nil
}

// StorageBackupDownloadPlan resolves a read under the full current runner lease.
func (s *PostgresStore) StorageBackupDownloadPlan(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingStorageBackupDownloadPlan, error) {
	return s.CheckStorageBackupDownload(ctx, runner, a)
}

// CheckStorageBackupDownload rechecks actor, copy, credential and lease authority.
func (s *PostgresStore) CheckStorageBackupDownload(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingStorageBackupDownloadPlan, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingStorageBackupDownloadPlan, error) {
		return storageDownloadCurrent(ctx, tx, runner, a)
	})
}

// BeginStorageBackupDownload consumes a valid stream attempt exactly once.
func (s *PostgresStore) BeginStorageBackupDownload(ctx context.Context, runner string, in domain.HostingStorageBackupDownloadStream) (domain.HostingStorageBackupDownloadPlan, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingStorageBackupDownloadPlan, error) {
		p, err := storageDownloadCurrent(ctx, tx, runner, in.HostingAuthority)
		if err != nil {
			return p, err
		}
		if in.PlanHash != p.Hash {
			return p, ErrConflict
		}
		n, err := sqlcgen.New(tx).BeginStorageDownload(ctx, sqlcgen.BeginStorageDownloadParams{ID: p.SessionID, Authority: buildJSON(in.HostingAuthority)})
		if err != nil {
			return p, err
		}
		if n != 1 {
			return p, ErrConflict
		}
		return p, nil
	})
}

// CloseStorageBackupDownload closes controller-owned read metadata without replaying bytes.
func (s *PostgresStore) CloseStorageBackupDownload(ctx context.Context, id string, success bool) error {
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		q := sqlcgen.New(tx)
		row, err := q.LockStorageDownload(ctx, id)
		if err != nil {
			return false, hostingError(err)
		}
		state := "failed"
		if success {
			state = "succeeded"
		}
		if row.State == "succeeded" || row.State == "failed" {
			return true, nil
		}
		if err = q.FinishStorageDownload(ctx, sqlcgen.FinishStorageDownloadParams{ID: id, State: state}); err != nil {
			return false, err
		}
		err = createAuditWithQueries(ctx, q, domain.AuditEvent{ID: "aud_download_closed_" + id, ActorID: row.ActorID, Action: "hosting.storage_backup_download." + state, TargetID: id, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"backup_id": row.BackupID}})
		return err == nil, err
	})
	return err
}

func (s *PostgresStore) storageDownloadClaimAllowed(ctx context.Context, tx pgx.Tx, run domain.TaskRun, runner string) (bool, error) {
	if domain.ValidateStorageBackupTransferTask(run) != nil {
		return false, nil
	}
	row, err := sqlcgen.New(tx).ReadStorageDownload(ctx, run.RunSpec.HostingOperationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if row.RunnerID != runner || row.RunID != run.ID {
		return false, nil
	}
	if row.State != "waiting" || !row.ExpiresAt.After(time.Now()) {
		if err := sqlcgen.New(tx).CancelStorageDownloadTask(ctx, run.ID); err != nil {
			return false, err
		}
		return false, nil
	}
	var p domain.HostingStorageBackupDownloadPlan
	if json.Unmarshal(row.Plan, &p) != nil || p.Validate() != nil {
		return false, ErrConflict
	}
	_, err = storageDownloadSource(ctx, tx, p.ActorID, p.BackupID)
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrStorageDownloadUnavailable) {
		return false, nil
	}
	return err == nil, err
}

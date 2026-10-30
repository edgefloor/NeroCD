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

var errDatabaseDownloadBusy = errors.New("database backup copy busy")
var errDatabaseDownloadPlacement = errors.New("database backup placement changed")

func databaseDownloadSource(ctx context.Context, tx pgx.Tx, actor, backup string) (domain.HostingDatabaseBackupDownloadPlan, error) {
	var p domain.HostingDatabaseBackupDownloadPlan
	q := sqlcgen.New(tx)
	row, err := q.ReadDatabaseBackup(ctx, backup)
	if err != nil {
		return p, hostingError(err)
	}
	if err = lockHostingResourceDestination(ctx, tx, row.SourceResourceID); err != nil {
		return p, err
	}
	target, err := q.LockHostingResourceTarget(ctx, row.SourceResourceID)
	if err != nil {
		return p, hostingError(err)
	}
	if err = proxyAuthorize(ctx, tx, actor, target.ProjectID, domain.RoleOwner, false); err != nil {
		return p, err
	}
	if err = databaseBindingGrant(ctx, q, target.ProjectID, target.DestinationID); err != nil {
		return p, auth.ErrForbidden
	}
	original, err := databaseBackupPlan(row)
	if err != nil || original.Validate() != nil {
		return p, ErrConflict
	}
	if target.ProjectID != row.ProjectID || target.BindingID != original.Source.Snapshot.BindingID || target.DestinationID != original.Source.Snapshot.DestinationID {
		return p, errDatabaseDownloadPlacement
	}
	if row.Mode != "backup" || row.State != "succeeded" || row.OwnershipHeld {
		return p, ErrConflict
	}
	if json.Unmarshal(row.Blob, &p.Blob) != nil || p.Blob.Validate() != nil || p.Blob.Format != domain.HostingDatabaseBackupFormatFor(original.Operation.Version) {
		return p, ErrConflict
	}
	if err = backupRetentionLock(ctx, tx); err != nil {
		return p, err
	}
	p.Version, p.BackupVersion = domain.HostingDatabaseBackupDownloadVersion, original.Operation.Version
	p.ActorID, p.ProjectID, p.ResourceID, p.BindingID = actor, target.ProjectID, row.SourceResourceID, target.BindingID
	p.BackupID, p.RunnerID, p.DestinationID, p.ServerID, p.DaemonID = row.ID, row.RunnerID, original.Source.Snapshot.DestinationID, original.Source.Snapshot.ServerID, original.Source.Release.DaemonID
	copies, err := q.ReadDatabaseDownloadCopies(ctx, backup)
	if err != nil {
		return p, err
	}
	for _, row := range copies {
		if row.Superseded {
			continue
		}
		if row.Availability == "reserved" {
			return p, errDatabaseDownloadBusy
		}
		if row.Availability != "available" {
			continue
		}
		var c domain.BackupRetentionCopy
		if json.Unmarshal(row.Body, &c) != nil || c.Local == nil || c.Local.Validate() != nil {
			return p, ErrConflict
		}
		if c.Item.BackupID != backup || c.Item.Location != "local" || c.Item.Digest != p.Blob.Digest || c.Item.Bytes != p.Blob.Bytes || c.RunnerID != p.RunnerID || c.DestinationID != p.DestinationID || c.ServerID != p.ServerID || c.DaemonID != p.DaemonID || c.Local.Bytes != p.Blob.Bytes || row.LocationHash != backupRetentionLocalLocation(c.ServerID, *c.Local, c.Item.Digest) {
			return p, ErrConflict
		}
		reserved, e := q.CheckDatabaseDownloadReservation(ctx, row.LocationHash)
		if e != nil {
			return p, e
		}
		if reserved {
			return p, errDatabaseDownloadBusy
		}
		p.LocalCopy, p.CopyID = *c.Local, c.Item.CopyID
		break
	}
	if p.CopyID == "" {
		return p, ErrNotFound
	}
	placement, err := q.StorageRemotePlacement(ctx, p.DestinationID)
	if err != nil {
		return p, hostingError(err)
	}
	if placement.RunnerID != p.RunnerID || placement.ServerID != p.ServerID {
		return p, errDatabaseDownloadPlacement
	}
	_, err = q.LockStorageRemoteRunner(ctx, sqlcgen.LockStorageRemoteRunnerParams{ID: p.RunnerID, Column2: domain.HostingDatabaseBackupDownloadCapability, Column3: domain.RunTypeDatabaseBackupTransfer})
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrDatabaseDownloadUnavailable
	}
	if err != nil {
		return p, err
	}
	return p, databaseBackupFinalizeActor(ctx, tx, actor)
}

// DatabaseBackupDownloadOptions reports current owner access and local-copy eligibility.
func (s *PostgresStore) DatabaseBackupDownloadOptions(ctx context.Context, actor, backup string) (domain.HostingDatabaseBackupDownloadOptions, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupDownloadOptions, error) {
		out := domain.HostingDatabaseBackupDownloadOptions{BackupID: backup}
		p, err := databaseDownloadSource(ctx, tx, actor, backup)
		switch {
		case err == nil:
			out.Available = true
			out.Blob = &p.Blob
		case errors.Is(err, errDatabaseDownloadBusy):
			out.ReasonCode = "copy_busy"
		case errors.Is(err, errDatabaseDownloadPlacement):
			out.ReasonCode = "placement_changed"
		case errors.Is(err, ErrDatabaseDownloadUnavailable):
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

// CreateDatabaseBackupDownload atomically admits one expiring read and its ordinary task.
func (s *PostgresStore) CreateDatabaseBackupDownload(ctx context.Context, id, actor, backup string, expires time.Time) (domain.HostingDatabaseBackupDownloadPlan, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupDownloadPlan, error) {
		p, err := databaseDownloadSource(ctx, tx, actor, backup)
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
		run := domain.TaskRun{ID: "db_transfer_" + id, ProjectID: p.ProjectID, RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseBackupTransfer, HostingOperationID: id, HostingMode: domain.HostingDatabaseBackupDownloadMode, Inputs: map[string]any{}}}
		if domain.ValidateDatabaseBackupTransferTask(run) != nil {
			return p, ErrConflict
		}
		q := sqlcgen.New(tx)
		if _, err = q.CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: run.ID, ProjectID: run.ProjectID, RunSpec: buildJSON(run.RunSpec), RequestedBy: actor}); err != nil {
			return p, err
		}
		err = q.InsertDatabaseDownload(ctx, sqlcgen.InsertDatabaseDownloadParams{ID: id, BackupID: backup, ActorID: actor, RunnerID: p.RunnerID, RunID: run.ID, Plan: buildJSON(p), PlanHash: p.Hash, ExpiresAt: p.ExpiresAt})
		if err != nil {
			return p, err
		}
		err = createAuditWithQueries(ctx, q, domain.AuditEvent{ID: "aud_download_requested_" + id, ActorID: actor, Action: "hosting.database_backup_download.requested", TargetID: id, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"project_id": p.ProjectID, "backup_id": p.BackupID, "copy_id": p.CopyID, "plan_hash": p.Hash}})
		return p, err
	})
}

func databaseDownloadCurrent(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority) (domain.HostingDatabaseBackupDownloadPlan, error) {
	var p domain.HostingDatabaseBackupDownloadPlan
	q := sqlcgen.New(tx)
	if err := checkDatabaseConnectionRunnerContext(ctx, tx, runner); err != nil {
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			return p, auth.ErrForbidden
		}
		return p, err
	}
	preliminary, err := q.ReadDatabaseDownload(ctx, a.OperationID)
	if err != nil {
		return p, hostingError(err)
	}
	var initial domain.HostingDatabaseBackupDownloadPlan
	if json.Unmarshal(preliminary.Plan, &initial) != nil || initial.Validate() != nil {
		return p, ErrConflict
	}
	fresh, err := databaseDownloadSource(ctx, tx, initial.ActorID, initial.BackupID)
	if err != nil {
		return p, err
	}
	// Resource and pool are locked before task, session, and lease.
	if _, err := q.LockHostingTaskForAuthority(ctx, a.RunID); err != nil {
		return p, hostingError(err)
	}
	row, err := q.LockDatabaseDownload(ctx, a.OperationID)
	if err != nil {
		return p, hostingError(err)
	}
	if json.Unmarshal(row.Plan, &p) != nil || p.Validate() != nil || p.SessionID != row.ID || p.SessionID != a.OperationID || p.BackupID != row.BackupID || p.ActorID != row.ActorID || p.RunnerID != row.RunnerID || !p.ExpiresAt.Equal(row.ExpiresAt) || row.PlanHash != p.Hash || row.RunnerID != runner || row.RunID != a.RunID {
		return p, ErrConflict
	}
	if _, err = q.CheckDatabaseDownloadAuthority(ctx, sqlcgen.CheckDatabaseDownloadAuthorityParams{ID: a.OperationID, RunID: a.RunID, RunnerID: runner, ID_2: a.LeaseID, Attempt: int32(a.Attempt), Fence: a.Fence}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return p, auth.ErrForbidden
		}
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

// DatabaseBackupDownloadPlan resolves a read under the full current runner lease.
func (s *PostgresStore) DatabaseBackupDownloadPlan(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingDatabaseBackupDownloadPlan, error) {
	return s.CheckDatabaseBackupDownload(ctx, runner, a)
}

// CheckDatabaseBackupDownload rechecks actor, copy, credential and lease authority.
func (s *PostgresStore) CheckDatabaseBackupDownload(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingDatabaseBackupDownloadPlan, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupDownloadPlan, error) {
		return databaseDownloadCurrent(ctx, tx, runner, a)
	})
}

// BeginDatabaseBackupDownload consumes a valid stream attempt exactly once.
func (s *PostgresStore) BeginDatabaseBackupDownload(ctx context.Context, runner string, in domain.HostingDatabaseBackupDownloadStream) (domain.HostingDatabaseBackupDownloadPlan, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupDownloadPlan, error) {
		p, err := databaseDownloadCurrent(ctx, tx, runner, in.HostingAuthority)
		if err != nil {
			return p, err
		}
		if in.PlanHash != p.Hash {
			return p, ErrConflict
		}
		n, err := sqlcgen.New(tx).BeginDatabaseDownload(ctx, sqlcgen.BeginDatabaseDownloadParams{ID: p.SessionID, Authority: buildJSON(in.HostingAuthority)})
		if err != nil {
			return p, err
		}
		if n != 1 {
			return p, ErrConflict
		}
		return p, nil
	})
}

// CloseDatabaseBackupDownload closes controller-owned read metadata without replaying bytes.
func (s *PostgresStore) CloseDatabaseBackupDownload(ctx context.Context, id string, success bool) error {
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		q := sqlcgen.New(tx)
		row, err := q.LockDatabaseDownload(ctx, id)
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
		if err = q.FinishDatabaseDownload(ctx, sqlcgen.FinishDatabaseDownloadParams{ID: id, State: state}); err != nil {
			return false, err
		}
		err = createAuditWithQueries(ctx, q, domain.AuditEvent{ID: "aud_download_closed_" + id, ActorID: row.ActorID, Action: "hosting.database_backup_download." + state, TargetID: id, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"backup_id": row.BackupID}})
		return err == nil, err
	})
	return err
}

func (s *PostgresStore) databaseDownloadClaimAllowed(ctx context.Context, tx pgx.Tx, run domain.TaskRun, runner string) (bool, error) {
	if domain.ValidateDatabaseBackupTransferTask(run) != nil {
		return false, nil
	}
	row, err := sqlcgen.New(tx).ReadDatabaseDownload(ctx, run.RunSpec.HostingOperationID)
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
		if err := sqlcgen.New(tx).CancelDatabaseDownloadTask(ctx, run.ID); err != nil {
			return false, err
		}
		return false, nil
	}
	var p domain.HostingDatabaseBackupDownloadPlan
	if json.Unmarshal(row.Plan, &p) != nil || p.Validate() != nil {
		return false, ErrConflict
	}
	_, err = databaseDownloadSource(ctx, tx, p.ActorID, p.BackupID)
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrDatabaseDownloadUnavailable) {
		return false, nil
	}
	return err == nil, err
}

// Complete only terminal metadata for a validated local-download task. This
// exception is private to CompleteLeaseRequest; every other generic mutation
// continues rejecting all hosting execution. The caller already locked the task
// and commits this metadata together with the ordinary fenced lease CAS/audit.
func completeDatabaseDownloadMetadata(ctx context.Context, tx pgx.Tx, runID, runner string, a domain.HostingAuthority, status string) (bool, error) {
	q := sqlcgen.New(tx)
	task, err := q.GetRun(ctx, runID)
	if err != nil {
		return false, hostingError(err)
	}
	run, err := taskRunFromSQLC(task)
	if err != nil {
		return false, err
	}
	if run.RunSpec.Type != domain.RunTypeDatabaseBackupTransfer || run.RunSpec.HostingMode != domain.HostingDatabaseBackupDownloadMode {
		return false, nil
	}
	if domain.ValidateDatabaseBackupTransferTask(run) != nil {
		return false, ErrConflict
	}
	a.OperationID = run.RunSpec.HostingOperationID
	row, err := q.LockDatabaseDownload(ctx, a.OperationID)
	if err != nil {
		return false, hostingError(err)
	}
	var p domain.HostingDatabaseBackupDownloadPlan
	if json.Unmarshal(row.Plan, &p) != nil || p.Validate() != nil || p.SessionID != row.ID || p.SessionID != a.OperationID || row.RunID != runID || row.RunnerID != runner || p.RunnerID != runner || p.ProjectID != run.ProjectID || p.ActorID != run.RequestedBy || p.BackupID != row.BackupID || p.ActorID != row.ActorID || p.Hash != row.PlanHash || !p.ExpiresAt.Equal(row.ExpiresAt) {
		return false, ErrConflict
	}
	if row.Authority != nil {
		var original domain.HostingAuthority
		if json.Unmarshal(row.Authority, &original) != nil || original != a {
			return false, ErrConflict
		}
	}
	if row.State == "succeeded" && row.Authority == nil {
		return false, ErrConflict
	}
	switch status {
	case domain.RunSucceeded:
		// A runner cannot manufacture successful browser delivery, including before
		// Begin. Replay is metadata only and keeps the exact recorded stream tuple.
		if row.State != "succeeded" || row.Authority == nil {
			return false, ErrConflict
		}
	case domain.RunFailed, domain.RunCanceled:
		// A delivered browser archive can coexist with a failed runner outcome
		// when the stream acknowledgment was lost. Preserve delivery evidence;
		// retiring this lease does not authorize another stream.
		if row.State == "succeeded" {
			return true, nil
		}
		if row.State != "failed" {
			if err = q.FinishDatabaseDownload(ctx, sqlcgen.FinishDatabaseDownloadParams{ID: row.ID, State: "failed"}); err != nil {
				return false, err
			}
			if err = createAuditWithQueries(ctx, q, domain.AuditEvent{ID: "aud_download_closed_" + row.ID, ActorID: row.ActorID, Action: "hosting.database_backup_download.failed", TargetID: row.ID, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"backup_id": row.BackupID}}); err != nil {
				return false, err
			}
		}
	default:
		return false, ErrConflict
	}
	return true, nil
}

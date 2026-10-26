package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

type storageRemoteRow struct {
	plan   domain.HostingStorageBackupTransferPlan
	view   domain.HostingStorageBackupTransfer
	run    *string
	daemon string
}

func storageRemoteLoad(ctx context.Context, tx pgx.Tx, id string, lock bool) (storageRemoteRow, error) {
	var r storageRemoteRow
	var row sqlcgen.HostingStorageBackupTransfer
	var err error
	if lock {
		row, err = sqlcgen.New(tx).LockStorageRemoteTransfer(ctx, id)
	} else {
		row, err = sqlcgen.New(tx).ReadStorageRemoteTransfer(ctx, id)
	}
	if err != nil {
		return r, hostingError(err)
	}
	if json.Unmarshal(row.Plan, &r.plan) != nil || r.plan.Hash != row.PlanHash || r.plan.Operation.ID != id || r.plan.Operation.ProjectID != row.ProjectID || r.plan.ActorID != row.ActorID || r.plan.RunnerID != row.RunnerID || r.plan.Operation.DestinationID != row.DestinationID || r.plan.Operation.TargetID != row.TargetID || r.plan.Operation.TargetRevision != row.TargetRevision || r.plan.Operation.Mode != row.Mode || r.plan.Operation.BackupID != proxyString(row.BackupID) || r.plan.Operation.RestoreOperationID != proxyString(row.RestoreOperationID) || r.plan.Validate() != nil {
		return r, ErrConflict
	}
	r.run = row.RunID
	r.daemon = row.DaemonID
	r.view = r.plan.Operation
	r.view.State = row.State
	r.view.FailureCode = row.FailureCode
	r.view.CreatedAt = row.CreatedAt
	r.view.UpdatedAt = row.UpdatedAt

	if r.view.Validate() != nil {
		return r, ErrConflict
	}
	return r, nil
}

// Restore uses historical immutable configuration. Current enablement and exact
// old key references are checked independently, never redirected to the latest.
func storageRemoteCurrent(ctx context.Context, tx pgx.Tx, p domain.HostingStorageBackupTransferPlan) error {
	if p.Validate() != nil {
		return ErrConflict
	}
	if err := proxyAuthorize(ctx, tx, p.ActorID, p.Operation.ProjectID, domain.RoleOwner, false); err != nil {
		return err
	}
	q := sqlcgen.New(tx)
	if err := databaseBindingGrant(ctx, q, p.Operation.ProjectID, p.Operation.DestinationID); err != nil {
		return err
	}
	var enabled bool
	var project string
	lockStorageRemoteTargetRow, err := sqlcgen.New(tx).LockStorageRemoteTarget(ctx, p.Operation.TargetID)
	enabled, project = lockStorageRemoteTargetRow.Enabled, lockStorageRemoteTargetRow.ProjectID
	if err != nil {
		return hostingError(err)
	}
	if !enabled || project != p.Operation.ProjectID {
		return ErrConflict
	}
	snapshot, err := backupRemoteTargetSnapshot(ctx, tx, p.Operation.TargetID, p.Operation.TargetRevision)
	if err != nil {
		return err
	}
	if domain.HostingDocumentHash(snapshot) != domain.HostingDocumentHash(p.Target) {
		return ErrConflict
	}
	for _, ref := range []domain.BackupRemoteCredentialRef{snapshot.Access, snapshot.Secret} {
		if _, err = backupRemoteCredentialRecord(ctx, tx, project, ref, true); err != nil {
			return err
		}
	}
	var server, runner string
	storageRemotePlacementRow, err := sqlcgen.New(tx).StorageRemotePlacement(ctx, p.Operation.DestinationID)
	server, runner = storageRemotePlacementRow.ServerID, storageRemotePlacementRow.RunnerID
	if err != nil {
		return hostingError(err)
	}
	if server != p.ServerID || runner != p.RunnerID {
		return ErrConflict
	}
	_, err = sqlcgen.New(tx).LockStorageRemoteRunner(ctx, sqlcgen.LockStorageRemoteRunnerParams{ID: runner, Column2: domain.HostingStorageBackupRemoteCapability, Column3: domain.RunTypeStorageBackupTransfer})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	return databaseBackupFinalizeActor(ctx, tx, p.ActorID)
}

func (s *PostgresStore) storageRemoteInsert(ctx context.Context, tx pgx.Tx, p domain.HostingStorageBackupTransferPlan, request, hash string, predecessor *string) error {
	if p.Validate() != nil || !s.hostingJobs.StorageBackupTransferEnabled() {
		return ErrConflict
	}
	var backup, restore *string
	if p.Operation.BackupID != "" {
		backup = &p.Operation.BackupID
	}
	if p.Operation.RestoreOperationID != "" {
		restore = &p.Operation.RestoreOperationID
	}
	var blob []byte
	if p.Operation.Blob != nil {
		blob = buildJSON(p.Operation.Blob)
	}
	err := sqlcgen.New(tx).InsertStorageRemoteTransfer(ctx, sqlcgen.InsertStorageRemoteTransferParams{ID: p.Operation.ID, ProjectID: p.Operation.ProjectID, TargetID: p.Operation.TargetID, TargetRevision: p.Operation.TargetRevision, DestinationID: p.Operation.DestinationID, ActorID: p.ActorID, RequestID: request, RequestHash: hash, Mode: p.Operation.Mode, BackupID: backup, RestoreOperationID: restore, PredecessorID: predecessor, RunnerID: p.RunnerID, DaemonID: p.DaemonID, Blob: blob, Plan: buildJSON(p), PlanHash: p.Hash, CreatedAt: p.Operation.CreatedAt})
	if err != nil {
		return hostingError(err)
	}
	if _, err = s.hostingJobs.EnqueueStorageBackupTransferTx(ctx, tx, hostingjobs.StorageBackupTransferArgs{OperationID: p.Operation.ID}); err != nil {
		return err
	}
	return storageRemoteAudit(ctx, tx, p.ActorID, p.Operation.ID, "requested", p.Operation.ProjectID)
}

// ValidateStorageBackupTarget queues remote target validation for the selected destination.
func (s *PostgresStore) ValidateStorageBackupTarget(ctx context.Context, id, project, actor, target string, in domain.HostingDatabaseBackupTargetValidationInput) (domain.HostingStorageBackupTransfer, error) {
	if in.Validate() != nil {
		return domain.HostingStorageBackupTransfer{}, domain.ErrInvalidStorageBackupRemote
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingStorageBackupTransfer, error) {
		var zero domain.HostingStorageBackupTransfer
		if err := proxyAuthorize(ctx, tx, actor, project, domain.RoleOwner, false); err != nil {
			return zero, err
		}
		var err error
		_, err = sqlcgen.New(tx).LockStorageRemoteProject(ctx, project)
		if err != nil {
			return zero, hostingError(err)
		}
		hash := domain.HostingDocumentHash(struct {
			Actor, Target string
			Input         domain.HostingDatabaseBackupTargetValidationInput
		}{actor, target, in})
		var prior, priorHash string
		readStorageRemoteRequestRow, err := sqlcgen.New(tx).ReadStorageRemoteRequest(ctx, sqlcgen.ReadStorageRemoteRequestParams{ProjectID: project, RequestID: in.RequestID})
		prior, priorHash = readStorageRemoteRequestRow.ID, readStorageRemoteRequestRow.RequestHash
		if err == nil {
			if priorHash != hash {
				return zero, ErrConflict
			}
			r, e := storageRemoteLoad(ctx, tx, prior, false)
			return r.view, e
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		var revision int64
		revision, err = sqlcgen.New(tx).LockStorageRemoteRevision(ctx, sqlcgen.LockStorageRemoteRevisionParams{ID: target, ProjectID: project})
		if err != nil {
			return zero, hostingError(err)
		}
		if revision != in.ExpectedRevision {
			return zero, ErrConflict
		}
		snapshot, err := backupRemoteTargetSnapshot(ctx, tx, target, revision)
		if err != nil {
			return zero, err
		}
		var server, runner string
		storageRemotePlacementUnlockedRow, err := sqlcgen.New(tx).StorageRemotePlacementUnlocked(ctx, in.DestinationID)
		server, runner = storageRemotePlacementUnlockedRow.ServerID, storageRemotePlacementUnlockedRow.RunnerID
		if err != nil {
			return zero, hostingError(err)
		}
		now := time.Now().UTC()
		if s.storageArchiveConfiguration == nil {
			return zero, ErrStorageArchiveUnavailable
		}
		limits := s.storageArchiveConfiguration.limits
		view := domain.HostingStorageBackupTransfer{ID: id, Version: domain.HostingStorageBackupTransferVersion, ProjectID: project, TargetID: target, TargetRevision: revision, DestinationID: in.DestinationID, Mode: "validate", State: "queued", CreatedAt: now, UpdatedAt: now}
		p := domain.HostingStorageBackupTransferPlan{Operation: view, Target: snapshot, ActorID: actor, RunnerID: runner, ServerID: server, Limits: limits, MaxBytes: limits.ArchiveBytes, TimeoutSeconds: 3600}
		p.Hash = domain.HostingDocumentHash(p)
		if err = storageRemoteCurrent(ctx, tx, p); err != nil {
			return zero, err
		}
		if err = s.storageRemoteInsert(ctx, tx, p, in.RequestID, hash, nil); err != nil {
			return zero, err
		}
		return view, databaseBackupFinalizeActor(ctx, tx, actor)
	})
}

// GetStorageBackupTransfer returns an authorized storage backup transfer view.
func (s *PostgresStore) GetStorageBackupTransfer(ctx context.Context, actor, id string) (domain.HostingStorageBackupTransfer, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingStorageBackupTransfer, error) {
		r, err := storageRemoteLoad(ctx, tx, id, false)
		if err != nil {
			return r.view, err
		}
		if err = proxyAuthorize(ctx, tx, actor, r.view.ProjectID, domain.RoleViewer, false); err != nil {
			return domain.HostingStorageBackupTransfer{}, err
		}
		if err = databaseBindingGrant(ctx, sqlcgen.New(tx), r.view.ProjectID, r.view.DestinationID); err != nil {
			return domain.HostingStorageBackupTransfer{}, err
		}
		return r.view, proxyCheckLegacyToken(ctx, tx, actor)
	})
}

// ListStorageBackupTransfers lists authorized storage backup transfers or target validations.
func (s *PostgresStore) ListStorageBackupTransfers(ctx context.Context, actor, id string, validation bool) ([]domain.HostingStorageBackupTransfer, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) ([]domain.HostingStorageBackupTransfer, error) {
		var project string
		var err error
		if validation {
			project, err = sqlcgen.New(tx).ReadStorageRemoteTargetProject(ctx, id)
		} else {
			project, err = sqlcgen.New(tx).ReadStorageRemoteBackupProject(ctx, id)
		}
		if err != nil {
			return nil, hostingError(err)
		}
		if err := proxyAuthorize(ctx, tx, actor, project, domain.RoleViewer, false); err != nil {
			return nil, err
		}
		var ids []string
		if validation {
			ids, err = sqlcgen.New(tx).ListStorageRemoteValidations(ctx, id)
		} else {
			ids, err = sqlcgen.New(tx).ListStorageRemoteBackupTransfers(ctx, &id)
		}
		if err != nil {
			return nil, err
		}
		out := make([]domain.HostingStorageBackupTransfer, 0, len(ids))
		for _, id := range ids {
			r, e := storageRemoteLoad(ctx, tx, id, false)
			if e != nil {
				return nil, e
			}
			if e = databaseBindingGrant(ctx, sqlcgen.New(tx), project, r.view.DestinationID); e != nil {
				return nil, e
			}
			out = append(out, r.view)
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}

// PrepareStorageBackupTransfer prepares a queued storage backup transfer for dispatch.
func (s *PostgresStore) PrepareStorageBackupTransfer(ctx context.Context, id string) error {
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		r, err := storageRemoteLoad(ctx, tx, id, true)
		if err != nil {
			return false, err
		}
		if r.view.State != "queued" {
			return true, nil
		}
		if err = storageRemoteCurrent(ctx, tx, r.plan); err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, auth.ErrForbidden) || errors.Is(err, auth.ErrUnauthenticated) {
				e := sqlcgen.New(tx).DenyStorageRemoteTransfer(ctx, id)
				if e == nil {
					e = storageRemoteDownloadFailed(ctx, tx, r.plan)
				}
				if e == nil && r.view.Mode == "upload" {
					var receipt string
					receipt, e = notificationStorageDirectReceipt(ctx, tx, id, "authority_denied")
					if e == nil {
						e = notificationStorageUploadFailure(ctx, tx, id, receipt)
					}
				}
				return e == nil, e
			}
			return false, err
		}
		run := domain.TaskRun{ID: "storage_transfer_" + id, ProjectID: r.view.ProjectID, RunSpec: domain.RunSpec{Type: domain.RunTypeStorageBackupTransfer, HostingOperationID: id, HostingMode: r.view.Mode, Inputs: map[string]any{}}}
		if domain.ValidateStorageBackupTransferTask(run) != nil {
			return false, ErrConflict
		}
		_, err = sqlcgen.New(tx).CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: run.ID, ProjectID: run.ProjectID, RunSpec: buildJSON(run.RunSpec), RequestedBy: r.plan.ActorID})
		if err != nil {
			return false, err
		}
		err = sqlcgen.New(tx).DispatchStorageRemoteTransfer(ctx, sqlcgen.DispatchStorageRemoteTransferParams{ID: id, RunID: optionalStorageRestore(run.ID)})
		return err == nil, err
	})
	return err
}

func storageRemoteAuthority(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, live bool) (storageRemoteRow, error) {
	var id string
	id, err := sqlcgen.New(tx).LockStorageRemoteAuthority(ctx, sqlcgen.LockStorageRemoteAuthorityParams{ID: a.OperationID, RunID: optionalStorageRestore(a.RunID), RunnerID: runner, ID_2: a.LeaseID, Attempt: int32(a.Attempt), Fence: a.Fence, Column7: live})
	if err != nil {
		return storageRemoteRow{}, hostingError(err)
	}
	return storageRemoteLoad(ctx, tx, id, true)
}

// StorageBackupTransferPlan loads the storage transfer plan under current runner lease authority.
func (s *PostgresStore) StorageBackupTransferPlan(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingStorageBackupTransferPlan, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingStorageBackupTransferPlan, error) {
		r, err := storageRemoteAuthority(ctx, tx, runner, a, true)
		if err != nil {
			return domain.HostingStorageBackupTransferPlan{}, err
		}
		if r.view.State != "dispatched" && r.view.State != "applying" {
			return domain.HostingStorageBackupTransferPlan{}, ErrConflict
		}
		return r.plan, storageRemoteCurrent(ctx, tx, r.plan)
	})
}

// StorageBackupTransferMutation records a fenced storage backup transfer mutation and receipt.
func (s *PostgresStore) StorageBackupTransferMutation(ctx context.Context, runner string, in domain.HostingStorageBackupTransferMutation) (domain.HostingStorageBackupTransfer, error) {
	if in.Validate() != nil {
		return domain.HostingStorageBackupTransfer{}, domain.ErrInvalidStorageBackupRemote
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingStorageBackupTransfer, error) {
		var zero domain.HostingStorageBackupTransfer
		r, err := storageRemoteAuthority(ctx, tx, runner, in.HostingAuthority, false)
		if err != nil {
			return zero, err
		}
		// Exact committed receipts acknowledge history under authenticated fenced
		// authority. Revoked execution inputs cannot prevent retirement of that
		// history, and a receipt never authorizes another effect.
		var hash string
		var response []byte
		readStorageRemoteReceiptRow, err := sqlcgen.New(tx).ReadStorageRemoteReceipt(ctx, sqlcgen.ReadStorageRemoteReceiptParams{OperationID: r.view.ID, Phase: in.Phase})
		hash, response = readStorageRemoteReceiptRow.BodyHash, readStorageRemoteReceiptRow.Response
		if err == nil {
			if hash != domain.HostingDocumentHash(in) || json.Unmarshal(response, &zero) != nil {
				return zero, ErrConflict
			}
			return zero, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		expired := false
		if _, err = storageRemoteAuthority(ctx, tx, runner, in.HostingAuthority, true); err != nil {
			if !storageRemoteExpiredTransferClassification(ctx, tx, runner, in, r) {
				return zero, err
			}
			expired = true
		}
		if !expired {
			if err = storageRemoteCurrent(ctx, tx, r.plan); err != nil {
				return zero, err
			}
		}
		if r.plan.DaemonID != "" && in.DaemonID != r.plan.DaemonID {
			return zero, ErrConflict
		}
		if in.PlanHash != r.plan.Hash || r.daemon != "" && r.daemon != in.DaemonID {
			return zero, ErrConflict
		}
		if domain.ValidateStorageTransferCopyProof(r.plan, in) != nil {
			return zero, ErrConflict
		}
		state := r.view.State
		switch in.Phase {
		case "intent":
			if state != "dispatched" || in.FailureCode != "" {
				return zero, ErrConflict
			}
			state = "applying"
		case "complete":
			if state != "applying" || in.FailureCode != "" {
				return zero, ErrConflict
			}
			state = "succeeded"
		case "failed":
			if state != "dispatched" && state != "applying" || in.FailureCode == "" {
				return zero, ErrConflict
			}
			state = "failed"
		case "uncertain":
			if state != "applying" || in.FailureCode == "" {
				return zero, ErrConflict
			}
			state = "uncertain"
		}
		if err = sqlcgen.New(tx).SetStorageRemoteTransferState(ctx, sqlcgen.SetStorageRemoteTransferStateParams{ID: r.view.ID, State: state, FailureCode: in.FailureCode, DaemonID: in.DaemonID}); err != nil {
			return zero, err
		}
		if in.Phase != "intent" && !expired {
			status := "failed"
			if state == "succeeded" {
				status = "succeeded"
			}
			if err = sqlcgen.New(tx).CompleteStorageRemoteTask(ctx, sqlcgen.CompleteStorageRemoteTaskParams{ID: in.RunID, Status: status}); err != nil {
				return zero, err
			}
			if err = sqlcgen.New(tx).CompleteStorageRemoteLease(ctx, sqlcgen.CompleteStorageRemoteLeaseParams{ID: in.LeaseID, Status: status, CompletionKey: optionalStorageRestore(in.ReceiptID)}); err != nil {
				return zero, err
			}
		}
		if in.Phase == "failed" || in.Phase == "uncertain" {
			if err = storageRemoteDownloadFailed(ctx, tx, r.plan); err != nil {
				return zero, err
			}
		}
		if err = storageRemoteAudit(ctx, tx, runner, r.view.ID, in.Phase, r.view.ProjectID); err != nil {
			return zero, err
		}
		if in.Phase == "complete" {
			if r.view.Mode == "upload" {
				resource, err := sqlcgen.New(tx).StorageRetentionBackupResource(ctx, r.view.BackupID)
				if err != nil {
					return zero, err
				}
				if err = storageRetentionPublication(ctx, tx, resource, "upload_"+r.view.ID); err != nil {
					return zero, err
				}
			}
			if err = storageRetentionRegisterTransfer(ctx, tx, r, in); err != nil {
				return zero, err
			}
			if err = s.storageRemoteDownloadComplete(ctx, tx, r.plan); err != nil {
				return zero, err
			}
		}
		updated, err := storageRemoteLoad(ctx, tx, r.view.ID, false)
		if err != nil {
			return zero, err
		}
		err = sqlcgen.New(tx).InsertStorageRemoteReceipt(ctx, sqlcgen.InsertStorageRemoteReceiptParams{OperationID: r.view.ID, Phase: in.Phase, ReceiptID: in.ReceiptID, Authority: buildJSON(in.HostingAuthority), Body: buildJSON(in), BodyHash: domain.HostingDocumentHash(in), Response: buildJSON(updated.view), ExpiredClassification: expired})
		if err != nil {
			return zero, err
		}
		if in.Phase == "failed" || in.Phase == "uncertain" {
			if err = notificationStorageUploadFailure(ctx, tx, r.view.ID, in.ReceiptID); err != nil {
				return zero, err
			}
		}
		if expired {
			return updated.view, nil
		}
		if err = storageRemoteCurrent(ctx, tx, r.plan); err != nil {
			return zero, err
		}
		clock, err := sqlcgen.New(tx).CheckStorageArchiveAuthorityClock(ctx, sqlcgen.CheckStorageArchiveAuthorityClockParams{ActorID: r.plan.ActorID, LeaseID: in.LeaseID, RequireActive: in.Phase == "intent"})
		if err != nil {
			return zero, err
		}
		if !clock.ActorValid || !clock.LeaseValid {
			return zero, ErrConflict
		}
		return updated.view, nil
	})
}

// This is an acknowledgment of uncertainty, never expired execution authority.
// The lease and transfer rows are already locked by storageRemoteAuthority.
func storageRemoteExpiredTransferClassification(ctx context.Context, tx pgx.Tx, runner string, in domain.HostingStorageBackupTransferMutation, r storageRemoteRow) bool {
	if in.Phase != "uncertain" || in.FailureCode != "interrupted" || (r.view.Mode != "upload" && r.view.Mode != "download") || r.view.State != "applying" || in.PlanHash != r.plan.Hash || r.daemon != in.DaemonID {
		return false
	}
	if _, err := sqlcgen.New(tx).LockStorageRemoteExpiredRunner(ctx, runner); err != nil {
		return false
	}
	var allowed bool
	allowed, err := sqlcgen.New(tx).StorageRemoteExpiredClassification(ctx, sqlcgen.StorageRemoteExpiredClassificationParams{OperationID: in.OperationID, ID: in.LeaseID, RunID: in.RunID, RunnerID: runner, Attempt: int32(in.Attempt), Fence: in.Fence, Authority: buildJSON(in.HostingAuthority), Column8: in.PlanHash, Column9: in.DaemonID})
	return err == nil && allowed
}

func (h deploymentOnlyHandler) PrepareStorageBackupTransfer(ctx context.Context, id string) error {
	return h.store.PrepareStorageBackupTransfer(ctx, id)
}

func (s *PostgresStore) recoverStorageBackupTransfers(ctx context.Context) error {
	if !s.hostingJobs.StorageBackupTransferEnabled() {
		return nil
	}
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		ids, err := sqlcgen.New(tx).QueuedStorageRemoteTransfers(ctx)
		if err != nil {
			return false, err
		}
		for _, id := range ids {
			if _, err = s.hostingJobs.EnqueueStorageBackupTransferTx(ctx, tx, hostingjobs.StorageBackupTransferArgs{OperationID: id}); err != nil {
				return false, err
			}
		}
		return true, nil
	})
	return err
}

func (s *PostgresStore) storageBackupTransferClaimAllowed(ctx context.Context, tx pgx.Tx, run domain.TaskRun, runner string) (bool, error) {
	if domain.ValidateStorageBackupTransferTask(run) != nil {
		return false, nil
	}
	r, err := storageRemoteLoad(ctx, tx, run.RunSpec.HostingOperationID, false)
	if err != nil {
		return false, err
	}
	if r.run == nil || *r.run != run.ID || r.plan.Operation.ProjectID != run.ProjectID || r.plan.RunnerID != runner || r.view.Mode != run.RunSpec.HostingMode || r.view.State != "dispatched" {
		return false, nil
	}
	err = storageRemoteCurrent(ctx, tx, r.plan)
	// Expected current-authority denial makes this item ineligible. Let the
	// claim cursor advance without granting a lease or changing its history.
	// Database and cancellation errors still abort the transaction.
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, auth.ErrForbidden) {
		return false, nil
	}
	return err == nil, err
}

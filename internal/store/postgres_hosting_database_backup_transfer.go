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

type backupRemoteRow struct {
	plan   domain.HostingDatabaseBackupTransferPlan
	view   domain.HostingDatabaseBackupTransfer
	run    *string
	daemon string
}

func backupRemoteLoad(ctx context.Context, tx pgx.Tx, id string, lock bool) (backupRemoteRow, error) {
	var r backupRemoteRow
	var row sqlcgen.HostingDatabaseBackupTransfer
	var err error
	if lock {
		row, err = sqlcgen.New(tx).LockDatabaseBackupTransfer(ctx, id)
	} else {
		row, err = sqlcgen.New(tx).ReadDatabaseBackupTransfer(ctx, id)
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
func backupRemoteCurrent(ctx context.Context, tx pgx.Tx, p domain.HostingDatabaseBackupTransferPlan) error {
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
	lockDatabaseBackupTargetAuthorityRow, err := sqlcgen.New(tx).LockDatabaseBackupTargetAuthority(ctx, p.Operation.TargetID)
	enabled = lockDatabaseBackupTargetAuthorityRow.Enabled
	project = lockDatabaseBackupTargetAuthorityRow.ProjectID
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
	lockDatabaseBackupRemoteDestinationRow, err := sqlcgen.New(tx).LockDatabaseBackupRemoteDestination(ctx, p.Operation.DestinationID)
	server = lockDatabaseBackupRemoteDestinationRow.ServerID
	runner = lockDatabaseBackupRemoteDestinationRow.RunnerID
	if err != nil {
		return hostingError(err)
	}
	if server != p.ServerID || runner != p.RunnerID {
		return ErrConflict
	}
	_, err = sqlcgen.New(tx).LockDatabaseBackupRemoteRunner(ctx, sqlcgen.LockDatabaseBackupRemoteRunnerParams{ID: runner, Column2: domain.HostingDatabaseBackupRemoteCapability, Column3: domain.RunTypeDatabaseBackupTransfer})
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	return databaseBackupFinalizeActor(ctx, tx, p.ActorID)
}

func (s *PostgresStore) backupRemoteInsert(ctx context.Context, tx pgx.Tx, p domain.HostingDatabaseBackupTransferPlan, request, hash string, predecessor *string) error {
	if p.Validate() != nil || !s.hostingJobs.DatabaseBackupTransferEnabled() {
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
	err := sqlcgen.New(tx).CreateDatabaseBackupTransfer(ctx, sqlcgen.CreateDatabaseBackupTransferParams{ID: p.Operation.ID, ProjectID: p.Operation.ProjectID, TargetID: p.Operation.TargetID, TargetRevision: int64(p.Operation.TargetRevision), DestinationID: p.Operation.DestinationID, ActorID: p.ActorID, RequestID: request, RequestHash: hash, Mode: p.Operation.Mode, BackupID: backup, RestoreOperationID: restore, PredecessorID: predecessor, RunnerID: p.RunnerID, DaemonID: p.DaemonID, Blob: blob, Plan: buildJSON(p), PlanHash: p.Hash, CreatedAt: p.Operation.CreatedAt})
	if err != nil {
		return hostingError(err)
	}
	if _, err = s.hostingJobs.EnqueueDatabaseBackupTransferTx(ctx, tx, hostingjobs.DatabaseBackupTransferArgs{OperationID: p.Operation.ID}); err != nil {
		return err
	}
	return databaseBackupAudit(ctx, tx, p.ActorID, p.Operation.ID, "remote_requested", p.Operation.ProjectID)
}

// ValidateDatabaseBackupTarget queues remote target validation for the selected destination.
func (s *PostgresStore) ValidateDatabaseBackupTarget(ctx context.Context, id, project, actor, target string, in domain.HostingDatabaseBackupTargetValidationInput) (domain.HostingDatabaseBackupTransfer, error) {
	if in.Validate() != nil {
		return domain.HostingDatabaseBackupTransfer{}, domain.ErrInvalidDatabaseBackupRemote
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupTransfer, error) {
		var zero domain.HostingDatabaseBackupTransfer
		if err := proxyAuthorize(ctx, tx, actor, project, domain.RoleOwner, false); err != nil {
			return zero, err
		}
		_, err := sqlcgen.New(tx).LockDatabaseBackupTargetProject(ctx, project)
		if err != nil {
			return zero, hostingError(err)
		}
		hash := domain.HostingDocumentHash(struct {
			Actor, Target string
			Input         domain.HostingDatabaseBackupTargetValidationInput
		}{actor, target, in})
		var prior, priorHash string
		findDatabaseBackupTransferRequestRow, err := sqlcgen.New(tx).FindDatabaseBackupTransferRequest(ctx, sqlcgen.FindDatabaseBackupTransferRequestParams{ProjectID: project, RequestID: in.RequestID})
		prior = findDatabaseBackupTransferRequestRow.ID
		priorHash = findDatabaseBackupTransferRequestRow.RequestHash
		if err == nil {
			if priorHash != hash {
				return zero, ErrConflict
			}
			r, e := backupRemoteLoad(ctx, tx, prior, false)
			return r.view, e
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		var revision int64
		revision, err = sqlcgen.New(tx).LockDatabaseBackupTargetRevision(ctx, sqlcgen.LockDatabaseBackupTargetRevisionParams{ID: target, ProjectID: project})
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
		readDatabaseBackupRemoteDestinationRow, err := sqlcgen.New(tx).ReadDatabaseBackupRemoteDestination(ctx, in.DestinationID)
		server = readDatabaseBackupRemoteDestinationRow.ServerID
		runner = readDatabaseBackupRemoteDestinationRow.RunnerID
		if err != nil {
			return zero, hostingError(err)
		}
		now := time.Now().UTC()
		limits := s.databaseBackupPolicy()
		view := domain.HostingDatabaseBackupTransfer{ID: id, Version: domain.HostingDatabaseBackupTransferVersion, ProjectID: project, TargetID: target, TargetRevision: revision, DestinationID: in.DestinationID, Mode: "validate", State: "queued", CreatedAt: now, UpdatedAt: now}
		p := domain.HostingDatabaseBackupTransferPlan{Operation: view, Target: snapshot, ActorID: actor, RunnerID: runner, ServerID: server, MaxBytes: limits.MaxBytes, TimeoutSeconds: limits.TimeoutSeconds}
		p.Hash = domain.HostingDocumentHash(p)
		if err = backupRemoteCurrent(ctx, tx, p); err != nil {
			return zero, err
		}
		if err = s.backupRemoteInsert(ctx, tx, p, in.RequestID, hash, nil); err != nil {
			return zero, err
		}
		return view, nil
	})
}

// GetDatabaseBackupTransfer returns an authorized database backup transfer view.
func (s *PostgresStore) GetDatabaseBackupTransfer(ctx context.Context, actor, id string) (domain.HostingDatabaseBackupTransfer, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupTransfer, error) {
		r, err := backupRemoteLoad(ctx, tx, id, false)
		if err != nil {
			return r.view, err
		}
		if err = proxyAuthorize(ctx, tx, actor, r.view.ProjectID, domain.RoleViewer, false); err != nil {
			return domain.HostingDatabaseBackupTransfer{}, err
		}
		if err = databaseBindingGrant(ctx, sqlcgen.New(tx), r.view.ProjectID, r.view.DestinationID); err != nil {
			return domain.HostingDatabaseBackupTransfer{}, err
		}
		return r.view, proxyCheckLegacyToken(ctx, tx, actor)
	})
}

// ListDatabaseBackupTransfers lists authorized database backup transfers or target validations.
func (s *PostgresStore) ListDatabaseBackupTransfers(ctx context.Context, actor, id string, validation bool) ([]domain.HostingDatabaseBackupTransfer, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) ([]domain.HostingDatabaseBackupTransfer, error) {
		q := sqlcgen.New(tx)
		var project string
		var err error
		if validation {
			project, err = q.ReadDatabaseBackupValidationProject(ctx, id)
		} else {
			project, err = q.ReadDatabaseBackupTransferProject(ctx, id)
		}
		if err != nil {
			return nil, hostingError(err)
		}
		if err = proxyAuthorize(ctx, tx, actor, project, domain.RoleViewer, false); err != nil {
			return nil, err
		}
		var ids []string
		if validation {
			ids, err = q.ListDatabaseBackupValidationIDs(ctx, id)
		} else {
			ids, err = q.ListDatabaseBackupTransferIDs(ctx, &id)
		}
		if err != nil {
			return nil, err
		}
		out := make([]domain.HostingDatabaseBackupTransfer, 0, len(ids))
		for _, id := range ids {
			r, e := backupRemoteLoad(ctx, tx, id, false)
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

// PrepareDatabaseBackupTransfer prepares a queued database backup transfer for dispatch.
func (s *PostgresStore) PrepareDatabaseBackupTransfer(ctx context.Context, id string) error {
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		r, err := backupRemoteLoad(ctx, tx, id, true)
		if err != nil {
			return false, err
		}
		if r.view.State != "queued" {
			return true, nil
		}
		if err = backupRemoteCurrent(ctx, tx, r.plan); err != nil {
			if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, auth.ErrForbidden) || errors.Is(err, auth.ErrUnauthenticated) {
				e := sqlcgen.New(tx).FailDatabaseBackupTransferAuthority(ctx, id)
				if e == nil && r.view.Mode == "upload" {
					receipt, receiptErr := notificationDirectReceipt(ctx, tx, "transfer", id, "authority_denied")
					e = receiptErr
					if e == nil {
						e = notificationUploadFailure(ctx, tx, id, receipt)
					}
				}
				return e == nil, e
			}
			return false, err
		}
		run := domain.TaskRun{ID: "db_transfer_" + id, ProjectID: r.view.ProjectID, RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseBackupTransfer, HostingOperationID: id, HostingMode: r.view.Mode, Inputs: map[string]any{}}}
		if domain.ValidateDatabaseBackupTransferTask(run) != nil {
			return false, ErrConflict
		}
		if _, err = sqlcgen.New(tx).CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: run.ID, ProjectID: run.ProjectID, RunSpec: buildJSON(run.RunSpec), RequestedBy: r.plan.ActorID}); err != nil {
			return false, err
		}
		err = sqlcgen.New(tx).DispatchDatabaseBackupTransfer(ctx, sqlcgen.DispatchDatabaseBackupTransferParams{ID: id, RunID: &run.ID})
		return err == nil, err
	})
	return err
}

func backupRemoteAuthority(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, live bool) (backupRemoteRow, error) {
	var id string
	attempt, err := databaseBackupAuthorityAttempt(a.Attempt)
	if err != nil {
		return backupRemoteRow{}, err
	}
	id, err = sqlcgen.New(tx).LockDatabaseBackupTransferAuthority(ctx, sqlcgen.LockDatabaseBackupTransferAuthorityParams{ID: a.OperationID, RunID: &a.RunID, RunnerID: runner, ID_2: a.LeaseID, Attempt: attempt, Fence: a.Fence, Column7: live})
	if err != nil {
		return backupRemoteRow{}, hostingError(err)
	}
	return backupRemoteLoad(ctx, tx, id, true)
}

// DatabaseBackupTransferPlan loads the database transfer plan under current runner lease
// authority.
func (s *PostgresStore) DatabaseBackupTransferPlan(ctx context.Context, runner string, a domain.HostingAuthority) (domain.HostingDatabaseBackupTransferPlan, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupTransferPlan, error) {
		r, err := backupRemoteAuthority(ctx, tx, runner, a, true)
		if err != nil {
			return domain.HostingDatabaseBackupTransferPlan{}, err
		}
		if r.view.State != "dispatched" && r.view.State != "applying" {
			return domain.HostingDatabaseBackupTransferPlan{}, ErrConflict
		}
		return r.plan, backupRemoteCurrent(ctx, tx, r.plan)
	})
}

// DatabaseBackupTransferMutation records a fenced database backup transfer mutation and receipt.
func (s *PostgresStore) DatabaseBackupTransferMutation(ctx context.Context, runner string, in domain.HostingDatabaseBackupTransferMutation) (domain.HostingDatabaseBackupTransfer, error) {
	if in.Validate() != nil {
		return domain.HostingDatabaseBackupTransfer{}, domain.ErrInvalidDatabaseBackupRemote
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBackupTransfer, error) {
		var zero domain.HostingDatabaseBackupTransfer
		r, err := backupRemoteAuthority(ctx, tx, runner, in.HostingAuthority, false)
		if err != nil {
			return zero, err
		}
		var hash string
		var response []byte
		readDatabaseBackupTransferReceiptRow, err := sqlcgen.New(tx).ReadDatabaseBackupTransferReceipt(ctx, sqlcgen.ReadDatabaseBackupTransferReceiptParams{OperationID: r.view.ID, Phase: in.Phase})
		hash = readDatabaseBackupTransferReceiptRow.BodyHash
		response = readDatabaseBackupTransferReceiptRow.Response
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
		if _, err = backupRemoteAuthority(ctx, tx, runner, in.HostingAuthority, true); err != nil {
			if !backupRemoteExpiredUploadClassification(ctx, tx, runner, in, r) {
				return zero, err
			}
			expired = true
		}
		if !expired {
			if err = backupRemoteCurrent(ctx, tx, r.plan); err != nil {
				return zero, err
			}
		}
		if in.PlanHash != r.plan.Hash || r.daemon != "" && r.daemon != in.DaemonID {
			return zero, ErrConflict
		}
		if in.LocalCopy != nil && r.view.Mode != "download" || in.RemoteCopy != nil && r.view.Mode != "upload" {
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
		if err = sqlcgen.New(tx).SetDatabaseBackupTransferMutationState(ctx, sqlcgen.SetDatabaseBackupTransferMutationStateParams{ID: r.view.ID, State: state, FailureCode: in.FailureCode, DaemonID: in.DaemonID}); err != nil {
			return zero, err
		}
		if in.Phase != "intent" && !expired {
			status := "failed"
			if state == "succeeded" {
				status = "succeeded"
			}
			if err = sqlcgen.New(tx).CompleteDatabaseBackupTransferTask(ctx, sqlcgen.CompleteDatabaseBackupTransferTaskParams{ID: in.RunID, Status: status}); err != nil {
				return zero, err
			}
			if err = sqlcgen.New(tx).CompleteDatabaseBackupTransferLease(ctx, sqlcgen.CompleteDatabaseBackupTransferLeaseParams{ID: in.LeaseID, Status: status, CompletionKey: &in.ReceiptID}); err != nil {
				return zero, err
			}
		}
		if err = backupRetentionRegisterRemote(ctx, tx, r, in); err != nil {
			return zero, err
		}
		if r.view.Mode == "download" && in.Phase == "complete" && in.LocalCopy != nil {
			backup, err := sqlcgen.New(tx).ReadDatabaseBackup(ctx, r.view.BackupID)
			if err != nil || backup.RunnerID != r.plan.RunnerID {
				return zero, ErrConflict
			}
			if err = backupRetentionRegisterLocalCopy(ctx, tx, backup, domain.HostingDatabaseBackupMutation{Phase: "complete", Blob: r.view.Blob, LocalCopy: in.LocalCopy}, "dbc_download_"+r.view.ID); err != nil {
				return zero, err
			}
			if err = backupRetentionPublication(ctx, tx, backup.SourceResourceID, "download_local_"+r.view.ID); err != nil {
				return zero, err
			}
		}
		if r.view.Mode == "upload" && in.Phase == "complete" {
			var resource string
			resource, err = sqlcgen.New(tx).ReadDatabaseBackupSourceResource(ctx, r.view.BackupID)
			if err != nil {
				return zero, err
			}
			if err = backupRetentionPublication(ctx, tx, resource, "remote_"+r.view.ID); err != nil {
				return zero, err
			}
		}
		updated, err := backupRemoteLoad(ctx, tx, r.view.ID, false)
		if err != nil {
			return zero, err
		}
		err = sqlcgen.New(tx).CreateDatabaseBackupTransferReceipt(ctx, sqlcgen.CreateDatabaseBackupTransferReceiptParams{OperationID: r.view.ID, Phase: in.Phase, ReceiptID: in.ReceiptID, Authority: buildJSON(in.HostingAuthority), Body: buildJSON(in), BodyHash: domain.HostingDocumentHash(in), Response: buildJSON(updated.view), ExpiredClassification: expired})
		if err != nil {
			return zero, err
		}
		if r.view.Mode == "upload" && (in.Phase == "failed" || in.Phase == "uncertain") {
			if err = notificationUploadFailure(ctx, tx, r.view.ID, in.ReceiptID); err != nil {
				return zero, err
			}
		}
		if expired {
			return updated.view, nil
		}
		if err = backupRemoteCurrent(ctx, tx, r.plan); err != nil {
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
// The lease and transfer rows are already locked by backupRemoteAuthority.
func backupRemoteExpiredUploadClassification(ctx context.Context, tx pgx.Tx, runner string, in domain.HostingDatabaseBackupTransferMutation, r backupRemoteRow) bool {
	if in.Phase != "uncertain" || in.FailureCode != "interrupted" || r.view.Mode != "upload" || r.view.State != "applying" || in.PlanHash != r.plan.Hash || r.daemon != in.DaemonID {
		return false
	}
	_, err := sqlcgen.New(tx).LockExpiredDatabaseBackupUploadRunner(ctx, runner)
	if err != nil {
		return false
	}
	var allowed bool
	allowed, err = sqlcgen.New(tx).ExpiredDatabaseBackupUploadAllowed(ctx, sqlcgen.ExpiredDatabaseBackupUploadAllowedParams{OperationID: in.OperationID, ID: in.LeaseID, RunID: in.RunID, RunnerID: runner, Attempt: int32(in.Attempt), Fence: in.Fence, Authority: buildJSON(in.HostingAuthority), Column8: in.PlanHash, Column9: in.DaemonID})
	return err == nil && allowed
}

func (h deploymentOnlyHandler) PrepareDatabaseBackupTransfer(ctx context.Context, id string) error {
	return h.store.PrepareDatabaseBackupTransfer(ctx, id)
}

func (s *PostgresStore) recoverDatabaseBackupTransfers(ctx context.Context) error {
	if !s.hostingJobs.DatabaseBackupTransferEnabled() {
		return nil
	}
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		ids, err := sqlcgen.New(tx).ListQueuedDatabaseBackupTransferIDs(ctx)
		if err != nil {
			return false, err
		}
		for _, id := range ids {
			if _, err = s.hostingJobs.EnqueueDatabaseBackupTransferTx(ctx, tx, hostingjobs.DatabaseBackupTransferArgs{OperationID: id}); err != nil {
				return false, err
			}
		}
		return true, nil
	})
	return err
}

func (s *PostgresStore) databaseBackupTransferClaimAllowed(ctx context.Context, tx pgx.Tx, run domain.TaskRun, runner string) (bool, error) {
	if domain.ValidateDatabaseBackupTransferTask(run) != nil {
		return false, nil
	}
	r, err := backupRemoteLoad(ctx, tx, run.RunSpec.HostingOperationID, false)
	if err != nil {
		return false, err
	}
	if r.run == nil || *r.run != run.ID || r.plan.Operation.ProjectID != run.ProjectID || r.plan.RunnerID != runner || r.view.Mode != run.RunSpec.HostingMode || r.view.State != "dispatched" {
		return false, nil
	}
	err = backupRemoteCurrent(ctx, tx, r.plan)
	// Expected current-authority denial makes this item ineligible. Let the
	// claim cursor advance without granting a lease or changing its history.
	// Database and cancellation errors still abort the transaction.
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, auth.ErrForbidden) {
		return false, nil
	}
	return err == nil, err
}

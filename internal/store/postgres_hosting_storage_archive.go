package store

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

type storageArchiveConfiguration struct {
	helper domain.HostingStorageArchiveHelper
	limits domain.HostingStorageArchiveLimits
}

// ConfigureStorageArchive installs trusted operator configuration before queue
// startup. Nil disables new admission. No image tag or placeholder is inferred.
func (s *PostgresStore) ConfigureStorageArchive(helper *domain.HostingStorageArchiveHelper, limits domain.HostingStorageArchiveLimits) error {
	if s.hostingJobs != nil {
		return ErrConflict
	}
	if helper == nil {
		s.storageArchiveConfiguration = nil
		return nil
	}
	if helper.Validate() != nil || limits.Validate() != nil {
		return domain.ErrInvalidStorageArchive
	}
	s.storageArchiveConfiguration = &storageArchiveConfiguration{helper: *helper, limits: limits}
	return nil
}

// Existing row locks preserve roles, grants and revocation through commit.
// Legacy token expiry still advances while those locks or later work wait.
func archiveActorTransaction[T any](ctx context.Context, s *PostgresStore, actor string, f func(pgx.Tx) (T, error)) (T, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (T, error) {
		var zero T
		value, err := f(tx)
		if err != nil {
			return zero, err
		}
		if err = archiveFinalizeActor(ctx, tx, actor); err != nil {
			return zero, err
		}
		return value, nil
	})
}

func archiveFinalizeActor(ctx context.Context, tx pgx.Tx, actor string) error {
	if err := sqlcgen.New(tx).FlushStorageArchiveConstraints(ctx); err != nil {
		return err
	}
	return proxyCheckLegacyToken(ctx, tx, actor)
}

func storageArchiveSchema(ctx context.Context, q *sqlcgen.Queries) error {
	exists, err := q.StorageArchiveSchemaPresent(ctx)
	if err != nil {
		return err
	}
	if !exists {
		return ErrStorageArchiveUnavailable
	}
	ok, err := q.StorageArchiveSchemaCompatible(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return ErrStorageArchiveUnavailable
	}
	return nil
}

// archiveOperation is also used by the public generic operation reader. Old
// schemas remain readable; the new family is never projected as an image plan.
func archiveOperation(ctx context.Context, db sqlcgen.DBTX, id string) (domain.HostingOperation, bool, error) {
	var zero domain.HostingOperation
	q := sqlcgen.New(db)
	exists, err := q.StorageArchiveSchemaPresent(ctx)
	if err != nil || !exists {
		return zero, false, err
	}
	row, err := q.ReadStorageArchivePlan(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, false, nil
	}
	if err != nil {
		return zero, true, err
	}
	var plan domain.HostingStorageArchivePlan
	if json.Unmarshal(row.Body, &plan) != nil || plan.Validate() != nil || plan.Hash != row.BodyHash {
		return zero, true, ErrConflict
	}
	o, err := q.ReadStorageArchiveOperation(ctx, id)
	if err != nil {
		return zero, true, err
	}
	op := domain.HostingOperation{ID: o.ID, ProjectID: o.ProjectID, ResourceID: o.ResourceID, BindingID: o.BindingID, RunID: proxyString(o.RunID), State: o.State, CreatedBy: o.CreatedBy, CreatedAt: o.CreatedAt, UpdatedAt: o.UpdatedAt, TaskMode: row.Mode, StorageArchive: &domain.HostingStorageArchive{Mode: row.Mode, BackupID: row.BackupID, RecoveryOfOperationID: proxyString(row.RecoveryOfOperationID), Plan: plan, Progress: []domain.HostingStorageArchiveProgressEvidence{}}}
	if plan.Target != nil {
		op.StorageArchive.RestoreID = plan.Target.Plan.CreationOperationID
	}
	receipts, err := q.ReadStorageArchiveReceipts(ctx, id)
	if err != nil {
		return zero, true, err
	}
	for _, r := range receipts {
		switch r.Phase {
		case "prepared":
			op.StorageArchive.PreparedReceipt = &domain.HostingReceipt{ID: r.ReceiptID, BodyHash: r.BodyHash, CreatedAt: r.CreatedAt}
		case "apply_begin":
		case "complete":
			var in domain.HostingStorageArchiveCompleteRequest
			if json.Unmarshal(r.Body, &in) != nil || in.Validate() != nil {
				return zero, true, ErrConflict
			}
			op.StorageArchive.Result = &in.Result
			op.CompleteReceipt = &domain.HostingReceipt{ID: r.ReceiptID, BodyHash: r.BodyHash, CreatedAt: r.CreatedAt}
		default:
			var in domain.HostingStorageArchiveProgress
			if json.Unmarshal(r.Body, &in) != nil || in.Validate() != nil {
				return zero, true, ErrConflict
			}
			op.StorageArchive.Progress = append(op.StorageArchive.Progress, domain.HostingStorageArchiveProgressEvidence{Receipt: domain.HostingReceipt{ID: r.ReceiptID, BodyHash: r.BodyHash, CreatedAt: r.CreatedAt}, PlanHash: in.PlanHash, Phase: in.Phase, Writers: in.Writers, HelperContainerID: in.HelperContainerID, Blob: in.Blob, RestoreVerified: in.RestoreVerified})
		}
	}
	holds, err := q.ReadStorageArchiveHolds(ctx, sqlcgen.ReadStorageArchiveHoldsParams{OperationID: id, BindingID: op.BindingID})
	if err != nil {
		return zero, true, err
	}
	op.MutationHold, op.RestoreOwnershipHeld = holds.MutationHold, holds.RestoreOwnershipHeld
	return op, true, nil
}

func (s *PostgresStore) archiveConfigured(ctx context.Context, q *sqlcgen.Queries) error {
	if s.storageArchiveConfiguration == nil || s.hostingJobs == nil {
		return ErrStorageArchiveUnavailable
	}
	return storageArchiveSchema(ctx, q)
}

func archiveAudit(ctx context.Context, tx pgx.Tx, actor, phase string, op domain.HostingOperation) error {
	return createAuditWithQueries(ctx, sqlcgen.New(tx), domain.AuditEvent{ID: "aud_archive_" + phase + "_" + op.ID, ActorID: actor, Action: "hosting.storage_archive." + phase, TargetID: op.ID, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"project_id": op.ProjectID, "resource_id": op.ResourceID, "plan_hash": op.StorageArchive.Plan.Hash}})
}

func archivePrelock(ctx context.Context, tx pgx.Tx, destination string, resources ...string) error {
	q := sqlcgen.New(tx)
	if err := q.LockHostingExecutionDestination(ctx, destination); err != nil {
		return err
	}
	ids := slices.Clone(resources)
	slices.Sort(ids)
	for _, id := range slices.Compact(ids) {
		if _, err := q.LockResourceConfiguration(ctx, id); err != nil {
			return hostingError(err)
		}
	}
	return nil
}

func archiveRunner(ctx context.Context, tx pgx.Tx, p domain.HostingStorageArchivePlan) error {
	_, err := sqlcgen.New(tx).LockStorageArchiveRunner(ctx, sqlcgen.LockStorageArchiveRunnerParams{RunnerID: p.RunnerID, ServerID: p.ServerID, DestinationID: p.DestinationID, ProjectID: p.ProjectID})
	if err != nil {
		return hostingError(err)
	}
	// Combined writers require the same explicit qualification as ordinary
	// deployment. Archive execution itself still delivers no runtime secrets.
	for _, h := range append([]domain.HostingHealthy{p.SourceHealthy}, p.Writers...) {
		if h.Snapshot.PolicyVersion == domain.HostingOwnedStorageRuntimePolicyVersion {
			if err := requireHostingStorageAgent(ctx, tx, h.Snapshot); err != nil {
				return err
			}
			if err := requireHostingRuntimeAgent(ctx, tx, h.Snapshot); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *PostgresStore) archiveGate(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	if op.StorageArchive == nil || op.StorageArchive.Plan.Validate() != nil {
		return ErrConflict
	}
	p := op.StorageArchive.Plan
	if p.RemoteSource != nil {
		if err := s.storageRemoteRestoreCurrent(ctx, tx, p, op.CreatedBy); err != nil {
			return err
		}
	}
	resources := []string{p.SourceStorage.ResourceID}
	if p.Target != nil {
		resources = append(resources, p.Target.Resource.ID)
	}
	if err := archivePrelock(ctx, tx, p.DestinationID, resources...); err != nil {
		return err
	}
	r, b, err := hostingStoragePlacement(ctx, tx, op.CreatedBy, p.SourceStorage.ResourceID, domain.RoleOwner)
	if err != nil {
		return err
	}
	if r.ProjectID != p.ProjectID || b.ID != p.SourceStorage.BindingID || b.DestinationID != p.DestinationID {
		return ErrConflict
	}
	current, err := currentHostingStorage(ctx, tx, p.ProjectID, p.SourceStorage.ResourceID)
	if err != nil {
		return err
	}
	if current.Reference() != p.SourceStorage.Reference() {
		return ErrConflict
	}
	if s.storageArchiveConfiguration == nil || s.storageArchiveConfiguration.helper != p.Helper || s.storageArchiveConfiguration.limits != p.Limits {
		return ErrStorageArchiveUnavailable
	}
	if p.Target != nil {
		target, placement, e := hostingStoragePlacement(ctx, tx, op.CreatedBy, p.Target.Resource.ID, domain.RoleOwner)
		if e != nil {
			return e
		}
		if target.ProjectID != p.ProjectID || placement.ID != p.Target.Storage.BindingID || placement.DestinationID != p.DestinationID {
			return ErrConflict
		}
		st, e := currentHostingStorage(ctx, tx, p.ProjectID, p.Target.Resource.ID)
		if e != nil {
			return e
		}
		if st.Reference() != p.Target.Storage.Reference() {
			return ErrConflict
		}
	}
	if _, err = sqlcgen.New(tx).LockHostingPortReservation(ctx, op.BindingID); err != nil {
		return err
	}
	if _, err = sqlcgen.New(tx).LockHostingOperation(ctx, op.ID); err != nil {
		return err
	}
	if err = archiveRunner(ctx, tx, p); err != nil {
		return err
	}
	return proxyCheckLegacyToken(ctx, tx, op.CreatedBy)
}

func archiveInsertPlan(ctx context.Context, tx pgx.Tx, p domain.HostingStorageArchivePlan, actor, key, requestHash string) error {
	if p.Validate() != nil {
		return ErrConflict
	}
	resource, binding := p.SourceStorage.ResourceID, p.SourceStorage.BindingID
	var targetResource, targetBinding, targetStorage, original *string
	if p.Target != nil {
		resource, binding = p.Target.Resource.ID, p.Target.Storage.BindingID
		targetResource = &p.Target.Resource.ID
		targetBinding = &p.Target.Storage.BindingID
		targetStorage = &p.Target.Storage.ID
	}
	if p.RecoveryOfOperationID != "" {
		original = &p.RecoveryOfOperationID
	}
	q := sqlcgen.New(tx)
	if _, err := q.CreateHostingOperation(ctx, sqlcgen.CreateHostingOperationParams{ID: p.OperationID, ProjectID: p.ProjectID, ResourceID: resource, BindingID: binding, IdempotencyKey: key, RequestHash: requestHash, CreatedBy: actor}); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return q.InsertStorageArchivePlan(ctx, sqlcgen.InsertStorageArchivePlanParams{OperationID: p.OperationID, Mode: p.Mode, BackupID: p.BackupID, SourceResourceID: p.SourceStorage.ResourceID, SourceBindingID: p.SourceStorage.BindingID, SourceStorageID: p.SourceStorage.ID, TargetResourceID: targetResource, TargetBindingID: targetBinding, TargetStorageID: targetStorage, RecoveryOfOperationID: original, Body: raw, BodyHash: p.Hash})
}

func archiveLoadBackup(ctx context.Context, db sqlcgen.DBTX, row sqlcgen.HostingStorageBackup) (domain.HostingStorageBackup, error) {
	op, found, err := archiveOperation(ctx, db, row.OperationID)
	if err != nil {
		return domain.HostingStorageBackup{}, err
	}
	if !found || op.StorageArchive == nil {
		return domain.HostingStorageBackup{}, ErrConflict
	}
	b := domain.HostingStorageBackup{ID: row.ID, ProjectID: row.ProjectID, ResourceID: row.ResourceID, OperationID: row.OperationID, State: op.State, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt}
	raw, err := sqlcgen.New(db).ReadStorageArchiveBlob(ctx, row.ID)
	if err == nil {
		var blob domain.HostingStorageArchiveBlob
		if json.Unmarshal(raw, &blob) != nil || blob.Validate() != nil {
			return b, ErrConflict
		}
		b.Blob = &blob
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return b, err
	}
	rows, e := sqlcgen.New(db).StorageRetentionBackupCopies(ctx, row.ID)
	if e != nil {
		return b, e
	}
	b.Copies = []domain.BackupRetentionItem{}
	for _, row := range rows {
		var copy domain.StorageBackupRetentionCopy
		if json.Unmarshal(row.Body, &copy) != nil {
			return b, ErrConflict
		}
		item := copy.Item
		item.Availability = row.Availability
		item.Selected = false
		item.ProtectedReasons = []string{}
		if item.Availability == "unproven" {
			item.ProtectedReasons = append(item.ProtectedReasons, "unproven_ownership")
		}
		if row.Superseded {
			item.ProtectedReasons = append(item.ProtectedReasons, "superseded")
		}
		b.Copies = append(b.Copies, item)
	}
	if b.Blob != nil && b.State == "succeeded" {
		if len(rows) == 0 {
			b.Copies = append(b.Copies, domain.BackupRetentionItem{CopyID: "sbc_local_" + b.ID, BackupID: b.ID, Location: "local", Digest: b.Blob.SHA256, Bytes: b.Blob.SizeBytes, CreatedAt: b.CreatedAt, Availability: "unproven", ProtectedReasons: []string{"unproven_ownership"}})
		}
		legacy, err := sqlcgen.New(db).StorageRetentionLegacyTransfersForBackup(ctx, &b.ID)
		if err != nil {
			return b, err
		}
		for _, id := range legacy {
			b.Copies = append(b.Copies, domain.BackupRetentionItem{CopyID: "sbc_remote_" + id, BackupID: b.ID, TransferID: id, Location: "remote", Digest: b.Blob.SHA256, Bytes: b.Blob.SizeBytes, CreatedAt: b.CreatedAt, Availability: "unproven", ProtectedReasons: []string{"unproven_ownership"}})
		}
	}
	return b, nil
}

// RequestStorageBackup admits current owner intent and its queue job atomically.
func (s *PostgresStore) RequestStorageBackup(ctx context.Context, id, resource, actor string, in domain.HostingStorageBackupInput, event domain.AuditEvent) (domain.HostingStorageBackup, error) {
	if in.Validate() != nil {
		return domain.HostingStorageBackup{}, domain.ErrInvalidStorageArchive
	}
	return archiveActorTransaction(ctx, s, actor, func(tx pgx.Tx) (domain.HostingStorageBackup, error) {
		return s.requestStorageBackupTx(ctx, tx, id, resource, actor, in)
	})
}

// requestStorageBackupTx permits schedule cursor, occurrence and ordinary archive
// admission to commit together without nesting a public transaction.
func (s *PostgresStore) requestStorageBackupTx(ctx context.Context, tx pgx.Tx, id, resource, actor string, in domain.HostingStorageBackupInput) (domain.HostingStorageBackup, error) {
	var zero domain.HostingStorageBackup
	q := sqlcgen.New(tx)
	if err := s.archiveConfigured(ctx, q); err != nil {
		return zero, err
	}
	if err := lockHostingResourceDestination(ctx, tx, resource); err != nil {
		return zero, err
	}
	r, b, err := hostingStoragePlacement(ctx, tx, actor, resource, domain.RoleOwner)
	if err != nil {
		return zero, err
	}
	hash := domain.HostingDocumentHash(struct {
		Actor, Resource string
		Input           domain.HostingStorageBackupInput
	}{actor, resource, in})
	prior, err := q.ReadStorageBackupRequest(ctx, sqlcgen.ReadStorageBackupRequestParams{ResourceID: resource, RequestID: in.RequestID})
	if err == nil {
		if prior.CreatedBy != actor || prior.RequestHash != hash {
			return zero, ErrConflict
		}
		return archiveLoadBackup(ctx, tx, prior)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, err
	}
	if r.Generation != in.ExpectedResourceGeneration {
		return zero, ErrConflict
	}
	state, err := q.LockHostingPortReservation(ctx, b.ID)
	if err != nil {
		return zero, hostingError(err)
	}
	if !state.RestoreAdmitted || state.HoldOperationID != nil {
		return zero, ErrConflict
	}
	busy, err := q.HostingBindingBusy(ctx, b.ID)
	if err != nil {
		return zero, err
	}
	if busy {
		return zero, ErrConflict
	}
	st, err := currentHostingStorage(ctx, tx, r.ProjectID, resource)
	if err != nil {
		return zero, err
	}
	m, err := readHostingStorageMaterialization(ctx, tx, st)
	if err != nil {
		return zero, err
	}
	raw, err := q.GetHostingHealthy(ctx, b.ID)
	if err != nil {
		return zero, err
	}
	var healthy domain.HostingHealthy
	if json.Unmarshal(raw, &healthy) != nil || healthy.Storage == nil {
		return zero, ErrConflict
	}
	if err = archiveImageCleanupPending(ctx, tx, healthy); err != nil {
		return zero, err
	}
	writers := []domain.HostingHealthy{healthy}
	retained, err := q.GetHostingRetainedReleases(ctx, b.ID)
	if err != nil {
		return zero, err
	}
	for _, row := range retained {
		var h domain.HostingHealthy
		if json.Unmarshal(row.Body, &h) != nil {
			return zero, ErrConflict
		}
		if h.ContainerID != healthy.ContainerID {
			if row.State == "cleanup_pending" {
				return zero, ErrConflict
			}
			writers = append(writers, h)
		}
	}
	slices.SortFunc(writers, func(a, b domain.HostingHealthy) int {
		if a.ContainerID < b.ContainerID {
			return -1
		}
		if a.ContainerID > b.ContainerID {
			return 1
		}
		return 0
	})
	backupID := "hsb_" + domain.HostingDocumentHash(struct{ Resource, Request string }{resource, in.RequestID})[7:39]
	p := domain.HostingStorageArchivePlan{OperationID: id, ProjectID: r.ProjectID, ServerID: healthy.Snapshot.ServerID, DestinationID: b.DestinationID, RunnerID: healthy.Snapshot.RunnerID, Mode: "backup", BackupID: backupID, SourceHealthy: healthy, SourceStorage: st, SourceMaterialization: m, Writers: writers, Helper: s.storageArchiveConfiguration.helper, Limits: s.storageArchiveConfiguration.limits, CompilerVersion: domain.HostingStorageArchiveVersion, PolicyVersion: domain.HostingStorageArchiveVersion}
	p.RemoteTarget = in.RemoteTarget
	if in.RemoteTarget != nil {
		if err = s.storageRemoteSelect(ctx, tx, p, actor, *in.RemoteTarget); err != nil {
			return zero, err
		}
	}
	p.Hash = domain.HostingDocumentHash(p)
	if err = archiveRunner(ctx, tx, p); err != nil {
		return zero, err
	}
	if err = archiveInsertPlan(ctx, tx, p, actor, "storage_backup_"+in.RequestID, hash); err != nil {
		return zero, err
	}
	if err = q.InsertStorageBackup(ctx, sqlcgen.InsertStorageBackupParams{ID: backupID, OperationID: id, ProjectID: r.ProjectID, ResourceID: resource, RequestID: in.RequestID, RequestHash: hash, CreatedBy: actor}); err != nil {
		return zero, err
	}
	if err = q.AdmitStorageArchiveHold(ctx, sqlcgen.AdmitStorageArchiveHoldParams{BindingID: b.ID, OperationID: id}); err != nil {
		return zero, err
	}
	op, _, err := archiveOperation(ctx, tx, id)
	if err != nil {
		return zero, err
	}
	if err = archiveAudit(ctx, tx, actor, "requested", op); err != nil {
		return zero, err
	}
	if _, err = s.hostingJobs.EnqueueStorageArchiveTx(ctx, tx, hostingjobs.PrepareStorageArchiveArgs{OperationID: id}); err != nil {
		return zero, err
	}
	row, err := q.ReadStorageBackup(ctx, backupID)
	if err != nil {
		return zero, err
	}
	return archiveLoadBackup(ctx, tx, row)
}

// GetStorageBackup rechecks current viewer membership and destination grant.
func (s *PostgresStore) GetStorageBackup(ctx context.Context, actor, id string) (domain.HostingStorageBackup, error) {
	return archiveActorTransaction(ctx, s, actor, func(tx pgx.Tx) (domain.HostingStorageBackup, error) {
		var zero domain.HostingStorageBackup
		q := sqlcgen.New(tx)
		if err := storageArchiveSchema(ctx, q); err != nil {
			return zero, err
		}
		row, err := q.ReadStorageBackup(ctx, id)
		if err != nil {
			return zero, hostingError(err)
		}
		if _, _, err = hostingStoragePlacement(ctx, tx, actor, row.ResourceID, domain.RoleViewer); err != nil {
			return zero, err
		}
		return archiveLoadBackup(ctx, tx, row)
	})
}

// ListStorageBackups pages only after current viewer and grant authorization.
func (s *PostgresStore) ListStorageBackups(ctx context.Context, actor, resource string, page Page) (PageResult[domain.HostingStorageBackup], error) {
	return archiveActorTransaction(ctx, s, actor, func(tx pgx.Tx) (PageResult[domain.HostingStorageBackup], error) {
		var result PageResult[domain.HostingStorageBackup]
		q := sqlcgen.New(tx)
		if err := storageArchiveSchema(ctx, q); err != nil {
			return result, err
		}
		if _, _, err := hostingStoragePlacement(ctx, tx, actor, resource, domain.RoleViewer); err != nil {
			return result, err
		}
		total, err := q.CountStorageBackups(ctx, resource)
		if err != nil {
			return result, err
		}
		result.Total = int(total)
		result.Limit, result.Offset = resolvePage(page, result.Total)
		result.Items = []domain.HostingStorageBackup{}
		rows, err := q.ListStorageBackups(ctx, sqlcgen.ListStorageBackupsParams{ResourceID: resource, Limit: int32(result.Limit), Offset: int32(result.Offset)})
		if err != nil {
			return result, err
		}
		for _, row := range rows {
			item, e := archiveLoadBackup(ctx, tx, row)
			if e != nil {
				return result, e
			}
			result.Items = append(result.Items, item)
		}
		return result, nil
	})
}

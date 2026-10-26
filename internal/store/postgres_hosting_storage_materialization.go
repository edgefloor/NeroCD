package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func hostingStorageMaterializationCompatible(ctx context.Context, q *sqlcgen.Queries) error {
	compatible, err := q.HostingStorageMaterializationSchemaCompatible(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || !compatible {
		return ErrHostingStorageUnavailable
	}
	return nil
}

func currentHostingStorage(ctx context.Context, tx pgx.Tx, project, resource string) (domain.HostingStorage, error) {
	row, err := sqlcgen.New(tx).ReadHostingStorage(ctx, resource)
	if err != nil {
		return domain.HostingStorage{}, err
	}
	item, err := hostingStorageFromSQL(row)
	if err != nil {
		return item, err
	}
	if item.ProjectID != project {
		return domain.HostingStorage{}, ErrConflict
	}
	return item, nil
}

// A resource row lock serializes admission against the first metadata save.
func hostingStorageSnapshot(ctx context.Context, tx pgx.Tx, project, resource string) (domain.HostingStorageRef, error) {
	item, err := currentHostingStorage(ctx, tx, project, resource)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.HostingStorageRef{}, nil
	}
	return item.Reference(), err
}

func requireHostingStorageAgent(ctx context.Context, tx pgx.Tx, snap domain.HostingSnapshot) error {
	if err := domain.ValidateHostingStorageSnapshot(snap); err != nil {
		return ErrConflict
	}
	if snap.Storage == (domain.HostingStorageRef{}) {
		return nil
	}
	if snap.Database != (domain.HostingDatabaseExecutionRef{}) {
		return requireHostingDatabaseAgent(ctx, tx, snap)
	}
	if err := requireHostingReleaseAgent(ctx, tx, snap.RunnerID); err != nil {
		return err
	}
	capabilities := []string{domain.HostingOwnedStorageCapability}
	if snap.PolicyVersion == domain.HostingOwnedStorageRuntimePolicyVersion {
		capabilities = append(capabilities, domain.HostingOwnedStorageRuntimeCapability)
	}
	for _, capability := range capabilities {
		capable, err := sqlcgen.New(tx).HostingRunnerHasReleasePorts(ctx, sqlcgen.HostingRunnerHasReleasePortsParams{RunnerID: snap.RunnerID, Capability: capability})
		if err != nil {
			return err
		}
		if !capable {
			return ErrConflict
		}
	}

	return nil
}

func hostingFreshStorageGate(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	if err := hostingStorageMaterializationCompatible(ctx, sqlcgen.New(tx)); err != nil {
		return err
	}
	ref, err := hostingStorageSnapshot(ctx, tx, op.ProjectID, op.ResourceID)
	if err != nil {
		return err
	}
	if ref != op.Snapshot.Storage {
		return ErrConflict
	}
	if ref == (domain.HostingStorageRef{}) {
		return nil
	}
	if err = authorizeBuildActor(ctx, tx, op.CreatedBy, op.ProjectID, "maintainer"); err != nil {
		return err
	}
	return requireHostingStorageAgent(ctx, tx, op.Snapshot)
}

func readHostingStorageMaterialization(ctx context.Context, tx pgx.Tx, storage domain.HostingStorage) (domain.HostingStorageMaterialization, error) {
	var m domain.HostingStorageMaterialization
	row, err := sqlcgen.New(tx).LockHostingStorageMaterialization(ctx, storage.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		present, e := sqlcgen.New(tx).StorageArchiveSchemaPresent(ctx)
		if e != nil {
			return m, e
		}
		if present {
			return readArchiveRestoreMaterialization(ctx, tx, storage)
		}
	}
	if err != nil {
		return m, err
	}
	if json.Unmarshal(row.Origin, &m) != nil || domain.HostingDocumentHash(m) != row.OriginHash || m.Validate(storage) != nil {
		return m, ErrConflict
	}
	if row.ReceiptID != nil {
		if row.BodyHash == nil || row.CreatedAt == nil {
			return m, ErrConflict
		}
		m.State = "materialized"
		m.MaterializedReceipt = &domain.HostingReceipt{ID: *row.ReceiptID, BodyHash: *row.BodyHash, CreatedAt: *row.CreatedAt}
	}
	if m.Validate(storage) != nil {
		return m, ErrConflict
	}
	return m, nil
}

// Called before hashing preparation. A historical origin is reused even after
// failed or cancelled workloads; a different daemon never relocates the data.
func compileHostingStoragePlan(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, release domain.HostingRelease, previous *domain.HostingHealthy, retained []domain.HostingHealthy) (domain.HostingApplyPlan, error) {
	storage, err := currentHostingStorage(ctx, tx, op.ProjectID, op.ResourceID)
	if err != nil {
		return domain.HostingApplyPlan{}, err
	}
	if storage.Reference() != op.Snapshot.Storage || storage.BindingID != op.BindingID || storage.DestinationID != op.Snapshot.DestinationID {
		return domain.HostingApplyPlan{}, ErrConflict
	}
	descriptor := domain.HostingStoragePlan{Reference: storage.Reference(), VolumeName: storage.VolumeName, MountPath: storage.MountPath, CreationOperationID: op.ID, DaemonID: release.DaemonID}
	descriptor.CreationNonce = domain.HostingStorageCreationNonce(descriptor.Reference, op.ID, release.DaemonID)
	origin, err := readHostingStorageMaterialization(ctx, tx, storage)
	if err == nil {
		if origin.DaemonID != release.DaemonID {
			return domain.HostingApplyPlan{}, ErrConflict
		}
		descriptor.CreationOperationID, descriptor.CreationNonce = origin.CreationOperationID, origin.CreationNonce
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.HostingApplyPlan{}, err
	}
	if op.ApplicationDatabaseStorage != nil {
		if op.ApplicationDatabaseStorage.Validate(op.Snapshot) != nil || op.ApplicationDatabase == nil {
			return domain.HostingApplyPlan{}, ErrConflict
		}
		input, e := domain.HostingApplicationDatabaseExecutionPlanInput(*op.ApplicationDatabase)
		if e != nil {
			return domain.HostingApplyPlan{}, ErrConflict
		}
		return domain.CompileHostingApplicationDatabaseStoragePlan(op.Snapshot, release, previous, retained, input, descriptor)
	}
	if op.Snapshot.Database != (domain.HostingDatabaseExecutionRef{}) {
		if len(retained) != 0 {
			return domain.HostingApplyPlan{}, ErrConflict
		}
		if op.DatabaseMaintenance != nil {
			proof := op.DatabaseMaintenance
			if proof.Validate() != nil || proof.PreviousPlan.Storage == nil || previous == nil || !networkCaptureDocumentEqual(*previous, proof.PreviousHealthy) || release != proof.PreviousPlan.Release || descriptor != *proof.PreviousPlan.Storage {
				return domain.HostingApplyPlan{}, ErrConflict
			}
			return domain.CompileHostingDatabaseMaintenancePlan(op.Snapshot, release, *proof)
		}
		if op.DatabaseUpgrade != nil {
			proof := op.DatabaseUpgrade
			if previous == nil || !networkCaptureDocumentEqual(*previous, proof.PreviousHealthy) || release != proof.PreviousPlan.Release || descriptor != *proof.PreviousPlan.Storage {
				return domain.HostingApplyPlan{}, ErrConflict
			}
			input, e := domain.HostingDatabaseUpgradePlanInput(*proof)
			if e != nil {
				return domain.HostingApplyPlan{}, ErrConflict
			}
			database := *proof.PreviousPlan.Database
			database.Network = nil
			return domain.CompileHostingDatabasePlanV2(op.Snapshot, release, *previous, proof.PreviousPlan, descriptor, database, input)
		}
		view, readErr := hostingDatabaseLoad(ctx, sqlcgen.New(tx), op.ResourceID)
		if readErr != nil {
			return domain.HostingApplyPlan{}, fmt.Errorf("read database credential references for plan: %w", readErr)
		}
		database := domain.HostingDatabasePlan{Reference: op.Snapshot.Database, NetworkName: domain.HostingDatabaseNetworkName(op.BindingID), Credentials: view.Credentials}
		return domain.CompileHostingDatabasePlan(op.Snapshot, release, previous, descriptor, database)
	}
	if op.Snapshot.PolicyVersion == domain.HostingOwnedStorageRuntimePolicyVersion {
		return domain.CompileHostingOwnedStorageRuntimePlan(op.Snapshot, release, previous, retained, descriptor)
	}
	return domain.CompileHostingOwnedStoragePlan(op.Snapshot, release, previous, retained, descriptor)
}

func storageAttemptOperation(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority) (domain.HostingOperation, domain.HostingStorage, error) {
	var zero domain.HostingStorage
	if err := hostingStorageMaterializationCompatible(ctx, sqlcgen.New(tx)); err != nil {
		return domain.HostingOperation{}, zero, err
	}
	if err := hostingAuthorityExists(ctx, tx, runner, a, true); err != nil {
		return domain.HostingOperation{}, zero, err
	}
	op, err := hostingOperation(ctx, tx, a.OperationID)
	if err != nil {
		return op, zero, err
	}
	if op.CleanupPlan != nil || op.Release == nil || op.Snapshot.Storage == (domain.HostingStorageRef{}) || !hostingOperationPlanMatches(op) {
		return op, zero, ErrConflict
	}
	if err = hostingRequireMode(ctx, tx, a, "deploy"); err != nil {
		return op, zero, err
	}
	if err = hostingFreshRuntimeGate(ctx, tx, op); err != nil {
		return op, zero, err
	}
	if err = hostingOperationTargetGate(ctx, tx, op, false); err != nil {
		return op, zero, err
	}
	storage, err := currentHostingStorage(ctx, tx, op.ProjectID, op.ResourceID)
	if err != nil {
		return op, zero, err
	}
	if storage.Reference() != op.Plan.Storage.Reference || storage.VolumeName != op.Plan.Storage.VolumeName || storage.MountPath != op.Plan.Storage.MountPath {
		return op, zero, ErrConflict
	}
	return op, storage, nil
}

func storageAdmissionMatches(row sqlcgen.HostingStorageOperationAdmission, a domain.HostingAuthority, planHash string, ref domain.HostingStorageRef) bool {
	var authority domain.HostingAuthority
	return json.Unmarshal(row.Authority, &authority) == nil && authority == a && row.PlanHash == planHash && row.StorageID == ref.StorageID
}

func finalHostingStorageAuthority(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, op domain.HostingOperation) error {
	if err := hostingFreshStorageGate(ctx, tx, op); err != nil {
		return err
	}
	if err := proxyCheckLegacyToken(ctx, tx, op.CreatedBy); err != nil {
		return err
	}
	if err := hostingAuthorityExists(ctx, tx, runner, a, true); err != nil {
		return err
	}
	if err := validateStorageContinuityFinal(ctx, tx, op); err != nil {
		return err
	}
	if op.ApplicationDatabaseStorage != nil {
		return finalDatabaseConnectionAuthority(ctx, tx, runner, a, op, false, false)
	}
	if op.DatabaseUpgrade != nil || op.DatabaseMaintenance != nil {
		return finalDatabaseAuthority(ctx, tx, runner, a, op, false, false)
	}
	return nil
}

// First settlement has tentatively completed its own lease. Check the locked
// historical tuple and database expiry without reviving it as active authority.
func finalHostingStorageSettlementAuthority(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, op domain.HostingOperation, observe bool) error {
	if op.Snapshot.Storage == (domain.HostingStorageRef{}) {
		return nil
	}
	if op.DatabaseUpgrade != nil {
		return finalDatabaseAuthority(ctx, tx, runner, a, op, observe, true)
	}
	if err := hostingFreshStorageGate(ctx, tx, op); err != nil {
		return err
	}
	if err := hostingOperationTargetGate(ctx, tx, op, observe); err != nil {
		return err
	}
	if err := proxyCheckLegacyToken(ctx, tx, op.CreatedBy); err != nil {
		return err
	}
	if err := hostingAuthorityExists(ctx, tx, runner, a, false); err != nil {
		return err
	}
	live, err := sqlcgen.New(tx).HostingLeaseUnexpired(ctx, a.LeaseID)
	if err != nil {
		return err
	}
	if !live {
		return ErrConflict
	}
	return nil
}

// HostingStorageCreateBegin commits the independent hold, irreversible origin,
// and exact attempt admission before returning a one-shot create permission.
func (s *PostgresStore) HostingStorageCreateBegin(ctx context.Context, runner string, in domain.HostingStorageCreateRequest) (domain.HostingStorageCreateAck, error) {
	var zero domain.HostingStorageCreateAck
	if in.Validate() != nil {
		return zero, ErrConflict
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingStorageCreateAck, error) {
		if _, found, e := archiveOperation(ctx, tx, in.OperationID); e != nil {
			return zero, e
		} else if found {
			return s.archiveStorageCreateBegin(ctx, tx, runner, in)
		}
		op, storage, err := storageAttemptOperation(ctx, tx, runner, in.HostingAuthority)
		if err != nil {
			return zero, err
		}
		if in.PlanHash != op.Plan.Hash || in.StorageRef != op.Snapshot.Storage || in.CreationNonce != op.Plan.Storage.CreationNonce {
			return zero, ErrConflict
		}
		q := sqlcgen.New(tx)
		prior, err := readStorageOperationAdmission(ctx, tx, op.ID)
		if err == nil {
			if !op.MutationHold || !storageAdmissionMatches(prior, in.HostingAuthority, in.PlanHash, in.StorageRef) {
				return zero, ErrConflict
			}
			origin, readErr := readHostingStorageMaterialization(ctx, tx, storage)
			if readErr != nil {
				return zero, readErr
			}
			return domain.HostingStorageCreateAck{Storage: storage, Materialization: origin}, finalHostingStorageAuthority(ctx, tx, runner, in.HostingAuthority, op)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if op.State != "prepared" || op.MutationHold {
			return zero, ErrConflict
		}
		if err = autoFirstMutation(ctx, tx, op.ID, true); err != nil {
			return zero, err
		}
		changed, err := q.AdmitHostingMutation(ctx, sqlcgen.AdmitHostingMutationParams{BindingID: op.BindingID, HoldOperationID: hostingStringPointer(op.ID), DaemonID: hostingStringPointer(op.Release.DaemonID)})
		if err != nil {
			return zero, err
		}
		if changed != 1 {
			return zero, ErrConflict
		}
		if _, err = q.HoldHostingReleaseClaim(ctx, op.ID); err != nil {
			return zero, err
		}
		origin, err := readHostingStorageMaterialization(ctx, tx, storage)
		fresh := errors.Is(err, pgx.ErrNoRows)
		if fresh {
			if op.Plan.Storage.CreationOperationID != op.ID {
				return zero, ErrConflict
			}
			origin = domain.HostingStorageMaterialization{StorageRef: storage.Reference(), State: "create_admitted", CreationOperationID: op.ID, CreationPlanHash: op.Plan.Hash, CreationNonce: op.Plan.Storage.CreationNonce, DaemonID: op.Release.DaemonID}
			origin.Labels = domain.ExpectedHostingStorageLabels(storage, origin)
			if origin.Validate(storage) != nil {
				return zero, ErrConflict
			}
			if err = q.InsertHostingStorageMaterialization(ctx, sqlcgen.InsertHostingStorageMaterializationParams{StorageID: storage.ID, OriginOperationID: op.ID, OriginPlanHash: op.Plan.Hash, Origin: buildJSON(origin), OriginHash: domain.HostingDocumentHash(origin)}); err != nil {
				return zero, err
			}
		} else if err != nil {
			return zero, err
		}
		if origin.CreationNonce != op.Plan.Storage.CreationNonce || origin.CreationOperationID != op.Plan.Storage.CreationOperationID || origin.DaemonID != op.Release.DaemonID {
			return zero, ErrConflict
		}
		if err = insertStorageOperationAdmission(ctx, tx, sqlcgen.InsertHostingStorageOperationAdmissionParams{OperationID: op.ID, StorageID: storage.ID, PlanHash: op.Plan.Hash, Authority: buildJSON(in.HostingAuthority)}); err != nil {
			return zero, err
		}
		if err = storageMaterializationAudit(ctx, tx, runner, "create_admitted", op, storage.ID, in.PlanHash); err != nil {
			return zero, err
		}
		if err = autoFirstMutation(ctx, tx, op.ID, false); err != nil {
			return zero, err
		}
		if err = finalHostingStorageAuthority(ctx, tx, runner, in.HostingAuthority, op); err != nil {
			return zero, err
		}
		return domain.HostingStorageCreateAck{Storage: storage, Materialization: origin, CreatePermitted: fresh}, nil
	})
}

func storageMaterializationAudit(ctx context.Context, tx pgx.Tx, runner, phase string, op domain.HostingOperation, storage, planHash string) error {
	return createAuditWithQueries(ctx, sqlcgen.New(tx), domain.AuditEvent{ID: "aud_storage_" + phase + "_" + op.ID, ActorID: runner, Action: "hosting.storage." + phase, TargetID: storage, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"project_id": op.ProjectID, "resource_id": op.ResourceID, "binding_id": op.BindingID, "operation_id": op.ID, "plan_hash": planHash}})
}

func storageReceipt(row sqlcgen.HostingStorageMaterializedReceipt) domain.HostingReceipt {
	return domain.HostingReceipt{ID: row.ReceiptID, BodyHash: row.BodyHash, CreatedAt: row.CreatedAt}
}

// HostingStorageMaterialized never inspects Docker itself. It commits an exact
// typed runner observation and ACK before ordinary application-writer admission.
func (s *PostgresStore) HostingStorageMaterialized(ctx context.Context, runner string, in domain.HostingStorageMaterializedRequest) (domain.HostingStorageMaterializedAck, error) {
	var zero domain.HostingStorageMaterializedAck
	if in.Validate() != nil {
		return zero, ErrConflict
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingStorageMaterializedAck, error) {
		if _, found, e := archiveOperation(ctx, tx, in.OperationID); e != nil {
			return zero, e
		} else if found {
			return s.archiveStorageMaterialized(ctx, tx, runner, in)
		}
		if err := hostingStorageMaterializationCompatible(ctx, sqlcgen.New(tx)); err != nil {
			return zero, err
		}
		if err := hostingAuthorityExists(ctx, tx, runner, in.HostingAuthority, false); err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		prior, err := readStorageMaterializedReceipt(ctx, tx, in.OperationID)
		if err == nil {
			var authority domain.HostingAuthority
			if json.Unmarshal(prior.Authority, &authority) != nil || authority != in.HostingAuthority || prior.ReceiptID != in.ReceiptID || prior.BodyHash != domain.HostingDocumentHash(in) {
				return zero, ErrConflict
			}
			op, readErr := hostingOperation(ctx, tx, in.OperationID)
			if readErr != nil {
				return zero, readErr
			}
			storage, readErr := currentHostingStorage(ctx, tx, op.ProjectID, op.ResourceID)
			if readErr != nil {
				return zero, readErr
			}
			origin, readErr := readHostingStorageMaterialization(ctx, tx, storage)
			if readErr != nil || in.Matches(storage, origin) != nil {
				return zero, ErrConflict
			}
			if op.DatabaseUpgrade != nil {
				if readErr = databaseUpgradeHistoricalRead(ctx, tx, op); readErr != nil {
					return zero, readErr
				}
			}
			if err = databaseConnectionHistoricalRunnerRead(ctx, tx, runner, op); err != nil {
				return zero, err
			}
			return domain.HostingStorageMaterializedAck{Materialization: origin, Receipt: storageReceipt(prior)}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		op, storage, err := storageAttemptOperation(ctx, tx, runner, in.HostingAuthority)
		if err != nil {
			return zero, err
		}
		if !op.MutationHold || op.State != "prepared" || in.PlanHash != op.Plan.Hash || in.StorageRef != op.Snapshot.Storage {
			return zero, ErrConflict
		}
		admission, err := readStorageOperationAdmission(ctx, tx, op.ID)
		if err != nil {
			return zero, err
		}
		if !storageAdmissionMatches(admission, in.HostingAuthority, in.PlanHash, in.StorageRef) {
			return zero, ErrConflict
		}
		origin, err := readHostingStorageMaterialization(ctx, tx, storage)
		if err != nil {
			return zero, err
		}
		if in.Matches(storage, origin) != nil {
			return zero, ErrConflict
		}
		if err = storageMaterializationAudit(ctx, tx, runner, "materialized", op, storage.ID, in.PlanHash); err != nil {
			return zero, err
		}
		row, err := insertStorageMaterializedReceipt(ctx, tx, sqlcgen.InsertHostingStorageMaterializedReceiptParams{OperationID: op.ID, StorageID: storage.ID, ReceiptID: in.ReceiptID, Authority: buildJSON(in.HostingAuthority), Body: buildJSON(in), BodyHash: domain.HostingDocumentHash(in), AuditID: "aud_storage_materialized_" + op.ID})
		if err != nil {
			return zero, err
		}
		if origin.State == "create_admitted" {
			changed, markErr := q.MarkHostingStorageMaterialized(ctx, sqlcgen.MarkHostingStorageMaterializedParams{StorageID: storage.ID, MaterializedOperationID: hostingStringPointer(op.ID), MaterializedReceiptID: hostingStringPointer(in.ReceiptID)})
			if markErr != nil {
				return zero, markErr
			}
			if changed != 1 {
				return zero, ErrConflict
			}
		}
		origin, err = readHostingStorageMaterialization(ctx, tx, storage)
		if err != nil {
			return zero, err
		}
		return domain.HostingStorageMaterializedAck{Materialization: origin, Receipt: storageReceipt(row)}, finalHostingStorageAuthority(ctx, tx, runner, in.HostingAuthority, op)
	})
}

// A historical ACK is evidence only. A writer needs its exact current tuple's
// admission and receipt as well as the caller's independent live-authority checks.
func hostingStorageWriterReady(ctx context.Context, tx pgx.Tx, a domain.HostingAuthority, op domain.HostingOperation) error {
	if op.Snapshot.Storage == (domain.HostingStorageRef{}) {
		return nil
	}
	if op.Plan == nil || op.Plan.Storage == nil {
		return ErrConflict
	}
	admission, err := readStorageOperationAdmission(ctx, tx, op.ID)
	if err != nil {
		return ErrConflict
	}
	if !storageAdmissionMatches(admission, a, op.Plan.Hash, op.Snapshot.Storage) {
		return ErrConflict
	}
	row, err := readStorageMaterializedReceipt(ctx, tx, op.ID)
	if err != nil {
		return ErrConflict
	}
	var body domain.HostingStorageMaterializedRequest
	if json.Unmarshal(row.Body, &body) != nil || body.HostingAuthority != a || body.PlanHash != op.Plan.Hash || body.StorageRef != op.Snapshot.Storage || row.BodyHash != domain.HostingDocumentHash(body) {
		return ErrConflict
	}
	_, err = hostingStorageHealthyEvidence(ctx, tx, op)
	return err
}

// Resolve historical evidence from committed controller records, never from the
// latest plan hash or an uncommitted runner observation.
func hostingStorageHealthyEvidence(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) (*domain.HostingHealthyStorage, error) {
	if op.Snapshot.Storage == (domain.HostingStorageRef{}) {
		return nil, nil
	}
	if op.Plan == nil || op.Plan.Storage == nil {
		return nil, ErrConflict
	}
	storage, err := currentHostingStorage(ctx, tx, op.ProjectID, op.ResourceID)
	if err != nil {
		return nil, err
	}
	origin, err := readHostingStorageMaterialization(ctx, tx, storage)
	if err != nil {
		return nil, err
	}
	row, err := readStorageMaterializedReceipt(ctx, tx, op.ID)
	if err != nil {
		return nil, ErrConflict
	}
	var body domain.HostingStorageMaterializedRequest
	if json.Unmarshal(row.Body, &body) != nil || body.OperationID != op.ID || body.PlanHash != op.Plan.Hash || row.BodyHash != domain.HostingDocumentHash(body) || origin.State != "materialized" || body.Matches(storage, origin) != nil || op.Plan.Storage.Reference != body.StorageRef || op.Plan.Storage.CreationOperationID != origin.CreationOperationID || op.Plan.Storage.CreationNonce != origin.CreationNonce || op.Plan.Storage.DaemonID != origin.DaemonID || op.Plan.Storage.MountPath != storage.MountPath {
		return nil, ErrConflict
	}
	return &domain.HostingHealthyStorage{Plan: *op.Plan.Storage, CreationPlanHash: origin.CreationPlanHash}, nil
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

var _ HostingDatabaseUpgradeRepository = (*PostgresStore)(nil)

func databaseUpgradeSchema(ctx context.Context, q *sqlcgen.Queries) error {
	present, err := q.DatabaseNetworkUpgradeSchemaPresent(ctx)
	if err != nil || !present {
		return ErrHostingDatabaseUnavailable
	}
	ok, err := q.DatabaseNetworkUpgradeSchema(ctx)
	if err != nil || !ok {
		return ErrHostingDatabaseUnavailable
	}
	return nil
}

// Old schemas keep their original operations. V2 always requires the sidecar.
func loadDatabaseUpgrade(ctx context.Context, db sqlcgen.DBTX, op *domain.HostingOperation) error {
	if err := loadDatabaseConnectionExecution(ctx, db, op); err != nil {
		return err
	}
	q := sqlcgen.New(db)
	present, err := q.DatabaseNetworkUpgradeSchemaPresent(ctx)
	if err != nil {
		return err
	}
	if present {
		row, e := q.ReadDatabaseNetworkUpgrade(ctx, op.ID)
		if e == nil {
			var proof domain.HostingDatabaseUpgradeProof
			if json.Unmarshal(row.Proof, &proof) != nil || proof.Hash != row.ProofHash || proof.CapturePlan.OperationID != row.CaptureID || proof.OriginReceipt.ID != row.OriginReceiptID || proof.PreviousHealthy.OperationID != row.PreviousOperationID {
				return ErrConflict
			}
			op.DatabaseUpgrade = &proof
		} else if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
	}
	if connectionOperation(*op) {
		if op.DatabaseUpgrade != nil {
			return ErrConflict
		}
		return nil
	}
	if domain.ValidateHostingDatabaseUpgradeOperation(*op) != nil {
		return ErrConflict
	}
	return nil
}

func hostingOperationPlanMatches(op domain.HostingOperation) bool {
	if op.Plan == nil {
		return false
	}
	if op.ApplicationDatabase != nil {
		return domain.ValidateHostingApplicationDatabaseOperation(op) == nil
	}
	if op.DatabaseMaintenance != nil {
		return domain.ValidateHostingDatabaseMaintenanceOperation(op) == nil
	}
	if op.DatabaseUpgrade != nil {
		return domain.ValidateHostingDatabaseUpgradeOperation(op) == nil
	}
	return domain.ValidateHostingDatabaseUpgradeOperation(op) == nil && domain.HostingPlanFamilyMatches(op.Snapshot, *op.Plan)
}

// Healthy selection for upgrades accepts only a complete, settled predecessor.
// The existing binding helper stays v1-only for new pending app bindings.
func databaseUpgradeHealthy(ctx context.Context, tx pgx.Tx, resource, binding string) (domain.HostingOperation, domain.HostingHealthy, error) {
	var h domain.HostingHealthy
	raw, err := sqlcgen.New(tx).GetHostingHealthy(ctx, binding)
	if err != nil || json.Unmarshal(raw, &h) != nil {
		return domain.HostingOperation{}, h, ErrConflict
	}
	op, err := hostingOperation(ctx, tx, h.OperationID)
	if err != nil {
		return op, h, err
	}
	if op.ResourceID != resource || op.BindingID != binding || op.State != "succeeded" || op.MutationHold || op.RestoreOwnershipHeld || op.ReconcileRequested || op.CompleteReceipt == nil || op.Result == nil || op.Release == nil || !op.Result.Ready || op.Result.Uncertain || op.Result.Restored || op.Result.Outcome != "succeeded" || op.Result.ContainerID != h.ContainerID || op.Result.ImageID != h.Release.ImageID || op.Result.DaemonID != h.Release.DaemonID || op.Result.PlanHash != h.PlanHash || !hostingOperationPlanMatches(op) || op.Plan.Hash != h.PlanHash || op.Snapshot != h.Snapshot || *op.Release != h.Release || domain.ValidateHostingHealthyStorage(h) != nil || !networkCaptureDocumentEqual(op.Plan.Storage, &h.Storage.Plan) {
		return domain.HostingOperation{}, h, ErrConflict
	}
	return op, h, nil
}

func requireDatabaseUpgradeAgent(ctx context.Context, tx pgx.Tx, snap domain.HostingSnapshot) error {
	if err := requireHostingDatabaseAgent(ctx, tx, snap); err != nil {
		return err
	}
	ok, err := sqlcgen.New(tx).HostingRunnerHasReleasePorts(ctx, sqlcgen.HostingRunnerHasReleasePortsParams{RunnerID: snap.RunnerID, Capability: domain.HostingDatabaseUpgradeCapability})
	if err != nil {
		return err
	}
	if !ok {
		return ErrConflict
	}
	return nil
}

// Check the durable capture association again at each authority boundary. A
// historical proof is never sufficient to authorize a new network effect.
func databaseUpgradeAuthority(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, observe bool) error {
	q := sqlcgen.New(tx)
	if connectionOperation(op) {
		return databaseConnectionAuthority(ctx, tx, op, observe, false)
	}
	if !observe {
		if err := databaseConnectionLegacyDatabaseGate(ctx, tx, op); err != nil {
			return err
		}
	}
	if op.DatabaseUpgrade == nil {
		// An ordinary v1 deployment must never replace a current v2 writer.
		raw, err := q.GetHostingHealthy(ctx, op.BindingID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if len(raw) != 0 {
			var h domain.HostingHealthy
			if json.Unmarshal(raw, &h) != nil {
				return ErrConflict
			}
			row, err := q.GetHostingOperationSnapshot(ctx, h.OperationID)
			if err != nil {
				return err
			}
			var p domain.HostingApplyPlan
			if json.Unmarshal(row.Plan, &p) != nil || p.CompilerVersion == domain.HostingDatabaseNetworkCompilerVersion || p.Database != nil && p.Database.Network != nil {
				return ErrConflict
			}
		}
		return nil
	}
	if err := databaseUpgradeSchema(ctx, q); err != nil {
		return err
	}
	if domain.ValidateHostingDatabaseUpgradeOperation(op) != nil {
		return ErrConflict
	}
	p := op.DatabaseUpgrade
	c, err := readNetworkCapture(ctx, q, p.CapturePlan.OperationID)
	if err != nil || c.Receipt == nil || !networkCapturePlanEqual(c.Plan, p.CapturePlan) || !networkCaptureDocumentEqual(*c.Receipt, p.OriginReceipt) {
		return ErrConflict
	}
	if err = networkCaptureHistorical(ctx, tx, c, op.CreatedBy); err != nil {
		return err
	}
	if err = authorizeBuildActor(ctx, tx, op.CreatedBy, op.ProjectID, domain.RoleOwner); err != nil {
		return err
	}
	if err = requireDatabaseUpgradeAgent(ctx, tx, op.Snapshot); err != nil {
		return err
	}
	if !observe {
		raw, e := q.GetHostingHealthy(ctx, op.BindingID)
		if e != nil {
			return e
		}
		var h domain.HostingHealthy
		if json.Unmarshal(raw, &h) != nil {
			return ErrConflict
		}
		if !networkCaptureDocumentEqual(h, p.PreviousHealthy) {
			// Settlement has tentatively recorded its own complete receipt and
			// healthy state. Accept only that exact result under the same locks.
			if op.State != "succeeded" || op.Result == nil || !op.Result.Ready || op.Result.Uncertain || op.Result.Restored || op.Plan == nil || op.Release == nil || h.OperationID != op.ID || h.PlanHash != op.Plan.Hash || h.ContainerID != op.Result.ContainerID || h.Snapshot != op.Snapshot || h.Release != *op.Release || domain.ValidateHostingHealthyStorage(h) != nil {
				return ErrConflict
			}
		}
	}
	return nil
}

func finalizeDatabaseUpgradeRequest(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	if err := sqlcgen.New(tx).FlushDatabaseNetworkUpgrade(ctx); err != nil {
		return err
	}
	if err := hostingTargetGate(ctx, tx, op.ProjectID, op.Snapshot, false); err != nil {
		return err
	}
	if err := databaseOperationAuthority(ctx, tx, op, false); err != nil {
		return err
	}
	return proxyCheckLegacyToken(ctx, tx, op.CreatedBy)
}

func databaseUpgradeHistoricalRead(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	if err := sqlcgen.New(tx).FlushDatabaseNetworkUpgrade(ctx); err != nil {
		return err
	}
	if err := hostingTargetGateMode(ctx, tx, op.ProjectID, op.Snapshot, false, true); err != nil {
		return err
	}
	if err := databaseOperationAuthorityMode(ctx, tx, op, false, true); err != nil {
		return err
	}
	return proxyCheckLegacyToken(ctx, tx, op.CreatedBy)
}

// RequestHostingDatabaseNetworkUpgrade atomically binds saved nonsecret proof,
// audit, operation, and River job. The caller selects identities only.
func (s *PostgresStore) RequestHostingDatabaseNetworkUpgrade(ctx context.Context, id, resource, actor string, in domain.HostingDatabaseUpgradeInput, audit domain.AuditEvent) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	if s.hostingJobs == nil {
		return zero, hostingjobs.ErrUnavailable
	}
	if in.Validate() != nil || domain.ValidateDatabaseDeploymentTask(domain.TaskRun{ID: "run_" + id, RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseDeploy, HostingOperationID: id, HostingMode: "deploy"}}) != nil {
		return zero, ErrConflict
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingOperation, error) {
		q := sqlcgen.New(tx)
		if err := databaseUpgradeSchema(ctx, q); err != nil {
			return zero, err
		}
		if err := lockHostingResourceDestination(ctx, tx, resource); err != nil {
			return zero, err
		}
		target, err := q.LockHostingResourceTarget(ctx, resource)
		if err != nil {
			return zero, hostingError(err)
		}
		if target.Kind != "database" {
			return zero, ErrConflict
		}
		if err = authorizeBuildActor(ctx, tx, actor, target.ProjectID, domain.RoleOwner); err != nil {
			return zero, err
		}
		if err = databaseBindingGrant(ctx, q, target.ProjectID, target.DestinationID); err != nil {
			return zero, err
		}
		requestHash := domain.HostingDocumentHash(struct {
			Kind, Actor string
			Input       domain.HostingDatabaseUpgradeInput
		}{domain.HostingDatabaseUpgradeVersion, actor, in})
		existing, err := q.GetHostingIdempotency(ctx, sqlcgen.GetHostingIdempotencyParams{ResourceID: resource, IdempotencyKey: in.IdempotencyKey})
		if err == nil {
			op, e := hostingOperation(ctx, tx, existing.ID)
			if e != nil {
				return zero, e
			}
			if existing.RequestHash != requestHash || op.CreatedBy != actor || op.DatabaseUpgrade == nil || op.DatabaseUpgrade.CapturePlan.OperationID != in.CaptureID || op.DatabaseUpgrade.OriginReceipt.OriginHash != in.ExpectedOriginHash {
				return zero, ErrConflict
			}
			// Replay retains the original proof after later legitimate upgrades.
			return op, databaseUpgradeHistoricalRead(ctx, tx, op)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if target.Generation != in.ExpectedGeneration {
			return zero, ErrConflict
		}
		if _, err = q.LockHostingAdmissionState(ctx, target.BindingID); err != nil {
			return zero, err
		}
		safe, err := q.HostingDatabaseBindingTargetSafe(ctx, target.BindingID)
		if err != nil || safe == nil || !*safe {
			return zero, ErrConflict
		}
		previous, healthy, err := databaseUpgradeHealthy(ctx, tx, resource, target.BindingID)
		if err != nil {
			return zero, err
		}
		capture, err := readNetworkCapture(ctx, q, in.CaptureID)
		if err != nil || capture.Receipt == nil || capture.Receipt.OriginHash != in.ExpectedOriginHash || capture.Plan.ResourceID != resource {
			return zero, ErrConflict
		}
		if err = networkCaptureHistorical(ctx, tx, capture, actor); err != nil {
			return zero, err
		}
		proof := domain.HostingDatabaseUpgradeProof{Version: domain.HostingDatabaseUpgradeVersion, CapturePlan: capture.Plan, OriginReceipt: *capture.Receipt, PreviousPlan: *previous.Plan, PreviousHealthy: healthy}
		proof.Hash = proof.DocumentHash()
		op := domain.HostingOperation{ID: id, ProjectID: target.ProjectID, ResourceID: resource, BindingID: target.BindingID, Snapshot: previous.Snapshot, CreatedBy: actor, DatabaseUpgrade: &proof}
		if domain.ValidateHostingDatabaseUpgradeOperation(op) != nil {
			return zero, ErrConflict
		}
		if err = hostingTargetGate(ctx, tx, op.ProjectID, op.Snapshot, false); err != nil {
			return zero, err
		}
		if err = requireDatabaseUpgradeAgent(ctx, tx, op.Snapshot); err != nil {
			return zero, err
		}
		if err = q.CreateHostingDatabaseDeployment(ctx, sqlcgen.CreateHostingDatabaseDeploymentParams{OperationID: id, ResourceID: resource, BindingID: op.BindingID, DestinationID: op.Snapshot.DestinationID, Reference: buildJSON(op.Snapshot.Database), Credentials: buildJSON(previous.Plan.Database.Credentials)}); err != nil {
			return zero, err
		}
		if _, err = q.CreateHostingOperation(ctx, sqlcgen.CreateHostingOperationParams{ID: id, ProjectID: op.ProjectID, ResourceID: resource, BindingID: op.BindingID, IdempotencyKey: in.IdempotencyKey, RequestHash: requestHash, CreatedBy: actor}); err != nil {
			return zero, err
		}
		if _, err = q.CreateHostingSnapshot(ctx, sqlcgen.CreateHostingSnapshotParams{OperationID: id, Body: buildJSON(op.Snapshot), BodyHash: domain.HostingDocumentHash(op.Snapshot)}); err != nil {
			return zero, err
		}
		audit.ActorID, audit.Action, audit.TargetID = actor, "hosting.database_network_upgrade.requested", id
		audit.Metadata = map[string]any{"project_id": op.ProjectID, "resource_id": resource, "capture_id": capture.ID, "proof_hash": proof.Hash}
		if err = createAuditWithQueries(ctx, q, audit); err != nil {
			return zero, err
		}
		if err = q.InsertDatabaseNetworkUpgrade(ctx, sqlcgen.InsertDatabaseNetworkUpgradeParams{OperationID: id, CaptureID: capture.ID, OriginReceiptID: capture.Receipt.ID, PreviousOperationID: previous.ID, Proof: buildJSON(proof), ProofHash: proof.Hash, AuditID: audit.ID}); err != nil {
			return zero, err
		}
		if _, err = s.hostingJobs.EnqueueTx(ctx, tx, hostingjobs.PrepareDeploymentArgs{OperationID: id}); err != nil {
			return zero, fmt.Errorf("enqueue database network upgrade: %w", err)
		}
		op, err = hostingOperation(ctx, tx, id)
		if err != nil {
			return zero, err
		}
		return op, finalizeDatabaseUpgradeRequest(ctx, tx, op)
	})
}

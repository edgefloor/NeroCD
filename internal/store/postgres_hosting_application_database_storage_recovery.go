package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func applicationStorageProxyID(ctx context.Context, q *sqlcgen.Queries, op domain.HostingOperation) (string, bool, error) {
	if op.ApplicationDatabaseRouteContinuity == nil {
		return "", false, ErrConflict
	}
	if op.ApplicationDatabaseStorage != nil {
		row, err := q.ReadApplicationDatabaseStorageRecovery(ctx, op.ID)
		if err == nil {
			if row.Action != "complete_replacement" || row.ProxyOperationID == nil {
				return "", false, ErrConflict
			}
			return *row.ProxyOperationID, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", false, err
		}
	}
	return op.ApplicationDatabaseRouteContinuity.ProxyOperationID, false, nil
}

// Eligibility is a read of durable facts. Every owner POST repeats these checks
// under the connection/task/destination locks and current grants.
func applicationStorageRecoveryAllowed(ctx context.Context, db sqlcgen.DBTX, op domain.HostingOperation, action string) error {
	q := sqlcgen.New(db)
	if op.ApplicationDatabaseStorage == nil || op.Plan == nil || !op.MutationHold || op.RestoreOwnershipHeld || op.Result != nil || op.ReconciliationRunID != "" {
		return ErrConflict
	}
	writer, err := applicationStorageWriter(ctx, q, op)
	if err != nil || writer == nil {
		return ErrConflict
	}
	ended, err := q.ApplicationDatabaseStorageOriginalEnded(ctx, op.RunID)
	if err != nil || ended == nil || !*ended {
		return ErrConflict
	}
	unused, err := q.ApplicationDatabaseStorageFinalizeUnused(ctx, op.ID)
	if err != nil || unused == nil || !*unused {
		return ErrConflict
	}
	stage, err := connectionStage(ctx, q, op)
	if err != nil || stage == nil {
		return ErrConflict
	}
	if action == "resume_predecessor" {
		if writer.Receipt.Phase != "replacement_failed" || op.State != "failed" || op.ApplicationDatabaseYield != nil || stage.Hash != writer.Receipt.StageHash || stage.Candidate.Hash != writer.Receipt.CandidateHash {
			return ErrConflict
		}
		return nil
	}
	if action != "complete_replacement" || writer.Receipt.Phase != "stopped" || op.State != "applying" && op.State != "verifying" || op.ApplicationDatabaseYield == nil || op.ApplicationDatabaseRouteContinuity == nil || stage.Phase != "ready" || stage.Hash != op.ApplicationDatabaseYield.StageHash || stage.Candidate.Hash != op.ApplicationDatabaseYield.CandidateHash {
		return ErrConflict
	}
	// Only a closed refusal with exact restored metadata is eligible. Unknown
	// loaded/file effects remain held, rather than being guessed from a timeout.
	raw, err := q.ProxyOperation(ctx, op.ApplicationDatabaseRouteContinuity.ProxyOperationID)
	if err != nil {
		return ErrConflict
	}

	if raw.State != "refused" || raw.MutationHold || raw.RestoreHeld || raw.MetadataConflict || len(raw.Result) == 0 || len(raw.Prepared) == 0 {
		return ErrConflict
	}
	p, err := q.ProxyGet(ctx, raw.ProxyID)
	if err != nil || !p.Enabled || p.RestoreHeld || p.HoldOperationID != nil || p.ActiveOperationID != nil || p.LoadedGeneration != op.ApplicationDatabaseRouteContinuity.Request.ExpectedLoadedGeneration {
		return ErrConflict
	}

	var snapshot domain.HostingProxySnapshot
	var result domain.HostingProxyResult
	var plan domain.HostingProxyPlan
	if json.Unmarshal(raw.Snapshot, &snapshot) != nil || json.Unmarshal(raw.Result, &result) != nil || json.Unmarshal(raw.Plan, &plan) != nil || domain.ValidateHostingProxyPlan(plan) != nil || snapshot.StorageSwitch == nil || snapshot.StorageSwitch.OperationID != op.ID || snapshot.StorageSwitch.WriterStateHash != writer.Receipt.Hash || result.Outcome != "refused" || result.PlanHash != plan.Hash || result.LoadedGeneration != p.LoadedGeneration || !connectionJSONMatches(p.Configuration, snapshot.Configuration) {
		return ErrConflict
	}
	c := op.ApplicationDatabaseRouteContinuity
	prior, err := q.ProxyOperation(ctx, c.Request.ExpectedLoadedProxyOperationID)
	if err != nil || prior.SnapshotHash != c.LoadedSnapshotHash {
		return ErrConflict
	}
	var priorSnapshot domain.HostingProxySnapshot
	if json.Unmarshal(prior.Snapshot, &priorSnapshot) != nil {
		return ErrConflict
	}
	routes, err := q.ProxyListRoutes(ctx, p.ID)
	if err != nil {
		return err
	}
	selected := map[string]domain.HostingRouteSwitchSelection{}
	for _, pick := range c.Request.Routes {
		selected[pick.RouteID] = pick
	}
	expected := map[string]domain.HostingProxyRouteTarget{}
	for _, route := range priorSnapshot.Routes {
		expected[route.RouteID] = route
	}
	count := 0
	for _, route := range routes {
		if !route.Enabled {
			continue
		}
		count++
		want, ok := expected[route.ID]
		if !ok || route.ProjectID != want.ProjectID || route.ResourceID != want.ResourceID || route.BindingID != want.BindingID || route.Hostname != want.Hostname || route.PathPrefix != want.PathPrefix || route.SelectedReleaseOperationID != want.ReleaseOperationID {
			return ErrConflict
		}
		if pick, own := selected[route.ID]; own {
			if route.Version != pick.ExpectedVersion+2 || route.SelectedReleaseOperationID != pick.ExpectedSelectedReleaseOperationID {
				return ErrConflict
			}
		}
	}
	if count != len(expected) {
		return ErrConflict
	}
	return nil
}
func (s *PostgresStore) admitApplicationStorageRecovery(ctx context.Context, tx pgx.Tx, id, actor string, in domain.HostingDatabaseConnectionRecoveryInput, event domain.AuditEvent, c sqlcgen.HostingDatabaseConnection, b domain.HostingDatabaseBinding, op domain.HostingOperation) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	q := sqlcgen.New(tx)
	if err := applicationStorageRecoveryAllowed(ctx, tx, op, in.Action); err != nil {
		return zero, err
	}
	if op.ApplicationDatabaseStorageState == nil || op.ApplicationDatabaseStorageState.StateHash != in.StorageStateHash || in.ApplicationDatabaseStorage == nil || in.ApplicationDatabaseStorage.ExpectedStorageGeneration != op.Snapshot.Storage.Generation || in.ApplicationDatabaseStorage.ExpectedStorageHash != op.Snapshot.Storage.Hash || in.ApplicationDatabaseStorage.Validate() != nil {
		return zero, ErrConflict
	}
	if _, err := q.ReadApplicationDatabaseStorageRecovery(ctx, op.ID); !errors.Is(err, pgx.ErrNoRows) {
		return zero, ErrConflict
	}
	if err := databaseConnectionAuthority(ctx, tx, op, false, false); err != nil {
		return zero, err
	}
	runID := "storage_recover_" + op.ID
	var proxyID *string
	if in.Action == "complete_replacement" {
		p := "appdb_storage_proxy_" + op.ID
		proxyID = &p
		runID = "proxy_" + p
	}
	plan := domain.HostingApplicationDatabaseStorageRecoveryPlan{Version: domain.HostingApplicationDatabaseStorageVersion, OperationID: op.ID, Action: in.Action, RunID: runID, StorageRef: op.Snapshot.Storage, StorageStateHash: in.StorageStateHash, RequestHash: domain.HostingDocumentHash(in)}
	plan.Hash = plan.DocumentHash()
	if plan.Validate() != nil {
		return zero, ErrConflict
	}
	if err := q.InsertApplicationDatabaseStorageRecovery(ctx, sqlcgen.InsertApplicationDatabaseStorageRecoveryParams{OperationID: op.ID, Action: in.Action, RunID: runID, ProxyOperationID: proxyID, ActorID: actor, Request: buildJSON(in), RequestHash: plan.RequestHash, Body: buildJSON(plan)}); err != nil {
		return zero, err
	}
	if in.Action == "resume_predecessor" {
		spec := domain.RunSpec{Type: domain.RunTypeImageDeploy, Inputs: map[string]any{}, HostingOperationID: op.ID, HostingMode: "storage_recover"}
		if _, err := q.CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: runID, ProjectID: op.ProjectID, RunSpec: buildJSON(spec), RequestedBy: actor}); err != nil {
			return zero, err
		}
		if err := q.CreateApplicationDatabaseStorageRecoveryTarget(ctx, sqlcgen.CreateApplicationDatabaseStorageRecoveryTargetParams{RunID: runID, OperationID: op.ID, BindingID: op.BindingID, RunnerID: op.Snapshot.RunnerID}); err != nil {
			return zero, err
		}
	} else if err := s.createApplicationDatabaseSwitch(ctx, tx, op); err != nil {
		return zero, err
	}
	event.ID, event.ActorID, event.Action, event.TargetID = "aud_connection_storage_recover_"+id, actor, "hosting.application_database.storage_recovery_requested", op.ID
	event.Metadata = map[string]any{"request_hash": plan.RequestHash, "action": in.Action, "connection_id": c.ID, "storage_state_hash": in.StorageStateHash, "stage_hash": in.StageHash, "candidate_hash": in.CandidateHash, "recovery_run_id": runID, "membership_revision": in.ExpectedMembershipRevision}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	if err := createAuditWithQueries(ctx, q, event); err != nil {
		return zero, err
	}

	out, err := hostingOperation(ctx, tx, op.ID)
	if err != nil {
		return zero, err
	}
	return out, finalizeConnectionRequest(ctx, tx, c, b, actor, domain.RoleOwner, false)
}
func storageSwitchSnapshot(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, prior domain.HostingProxyOperation, snapshot *domain.HostingProxySnapshot, recovery bool) error {
	if op.ApplicationDatabaseStorage == nil {
		return nil
	}
	if prior.Plan == nil || prior.Preparation == nil {
		return ErrConflict
	}
	q := sqlcgen.New(tx)
	writer, err := applicationStorageWriter(ctx, q, op)
	if err != nil || writer == nil || writer.Receipt.Phase != "stopped" {
		return ErrConflict
	}
	owned, err := currentHostingStorage(ctx, tx, op.ProjectID, op.ResourceID)
	if err != nil {
		return err
	}
	origin, err := readHostingStorageMaterialization(ctx, tx, owned)
	if err != nil || origin.State != "materialized" {
		return ErrConflict
	}
	proof := domain.HostingProxyStorageSwitch{Version: domain.HostingProxyStorageSwitchVersion, Action: "switch", OperationID: op.ID, ContinuationHash: op.ApplicationDatabaseRouteContinuity.Hash, StorageRef: op.Snapshot.Storage, StorageOriginHash: domain.HostingDocumentHash(origin), WriterStateHash: writer.Receipt.Hash, CandidateHash: op.ApplicationDatabaseYield.CandidateHash, RouteScopeHash: domain.HostingDocumentHash(proxySortedTargets(snapshot.Routes)), PriorGeneration: prior.Generation, PriorPreviousGeneration: prior.Plan.PreviousGeneration, PriorEngineHash: prior.Preparation.EngineHash, PriorRequiredRouteIDs: []string{}}
	if prior.Snapshot.VerifiedSwitch != nil {
		proof.PriorRequiredRouteIDs = prior.Snapshot.VerifiedSwitch.RouteIDs()
	}
	if recovery {
		failed, err := proxyOperation(ctx, tx, op.ApplicationDatabaseRouteContinuity.ProxyOperationID)
		if err != nil || failed.State != "refused" || failed.Preparation == nil || failed.Plan == nil || failed.Snapshot.StorageSwitch == nil {
			return ErrConflict
		}
		proof.Action = "complete_replacement"
		proof.FailedProxyOperationID = failed.ID
		proof.FailedGeneration = failed.Generation
		proof.FailedPlanHash = failed.Plan.Hash
		proof.FailedEngineHash = failed.Preparation.EngineHash
	}
	proof.Hash = proof.DocumentHash()
	snapshot.StorageSwitch = &proof
	return proof.Validate(*snapshot, prior.Generation)
}
func (s *PostgresStore) HostingApplicationDatabaseStorageProxyPlan(ctx context.Context, runner, fingerprint string, a domain.HostingAuthority) (domain.HostingApplicationDatabaseStorageProxyPlan, error) {
	var zero domain.HostingApplicationDatabaseStorageProxyPlan
	ctx, err := BindHostingDatabaseConnectionRunnerContext(ctx, runner, fingerprint)
	if err != nil {
		return zero, err
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingApplicationDatabaseStorageProxyPlan, error) {
		if err := proxyAuthority(ctx, tx, runner, a, true); err != nil {
			return zero, err
		}
		p, err := proxyOperation(ctx, tx, a.OperationID)
		if err != nil || p.Plan == nil || p.Snapshot.StorageSwitch == nil || p.RunID != a.RunID || !p.MutationHold || p.State != "applying" {
			return zero, ErrConflict
		}
		op, err := hostingOperation(ctx, tx, p.Snapshot.StorageSwitch.OperationID)
		if err != nil {
			return zero, err
		}
		effective, _, err := applicationStorageProxyID(ctx, sqlcgen.New(tx), op)
		if err != nil || effective != p.ID {
			return zero, ErrConflict
		}
		if err = databaseConnectionAuthority(ctx, tx, op, false, false); err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		stage, err := connectionStage(ctx, q, op)
		if err != nil || stage == nil || stage.Phase != "ready" || op.ApplicationDatabaseYield == nil || stage.Hash != op.ApplicationDatabaseYield.StageHash {
			return zero, ErrConflict
		}
		writer, err := applicationStorageWriter(ctx, q, op)
		if err != nil || writer == nil || writer.Receipt.Phase != "stopped" || writer.Receipt.Hash != p.Snapshot.StorageSwitch.WriterStateHash {
			return zero, ErrConflict
		}
		proxy, err := proxyLock(ctx, tx, p.ProxyID)
		if err != nil {
			return zero, err
		}
		rows, err := q.ProxyListRoutes(ctx, proxy.ID)
		if err != nil {
			return zero, err
		}
		scope, err := continuityScope(ctx, tx, proxy, op, rows, true)
		if err != nil || !reflect.DeepEqual(scope, proxySortedTargets(p.Snapshot.Routes)) || domain.HostingDocumentHash(scope) != p.Snapshot.StorageSwitch.RouteScopeHash {
			return zero, ErrConflict
		}
		owned, err := currentHostingStorage(ctx, tx, op.ProjectID, op.ResourceID)
		if err != nil {
			return zero, err
		}
		origin, err := readHostingStorageMaterialization(ctx, tx, owned)
		if err != nil || origin.State != "materialized" || domain.HostingDocumentHash(origin) != p.Snapshot.StorageSwitch.StorageOriginHash {
			return zero, ErrConflict
		}
		if err = checkDatabaseConnectionRunnerContext(ctx, tx, runner); err != nil {
			return zero, err
		}
		if err = proxyAuthority(ctx, tx, runner, a, true); err != nil {
			return zero, err
		}
		return domain.HostingApplicationDatabaseStorageProxyPlan{Operation: op, Stage: *stage, Storage: domain.HostingStorageCreateAck{Storage: owned, Materialization: origin, CreatePermitted: false}}, nil
	})
}

func applicationStorageProxyOrigin(ctx context.Context, tx pgx.Tx, proxy string) (string, error) {
	q := sqlcgen.New(tx)
	id, err := q.ApplicationDatabaseRouteOrigin(ctx, proxy)
	if !errors.Is(err, pgx.ErrNoRows) {
		return id, err
	}
	present, e := q.ApplicationDatabaseStoragePresent(ctx)
	if e != nil || !present {
		return "", err
	}
	return q.ApplicationDatabaseStorageProxyOrigin(ctx, &proxy)
}

var _ HostingApplicationDatabaseStorageRunnerRepository = (*PostgresStore)(nil)

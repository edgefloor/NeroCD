package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

func loadApplicationDatabaseContinuity(ctx context.Context, db sqlcgen.DBTX, op *domain.HostingOperation) error {
	present, err := sqlcgen.New(db).ApplicationDatabaseRouteSchemaPresent(ctx)
	if err != nil || !present {
		return err
	}
	raw, err := sqlcgen.New(db).ReadApplicationDatabaseRouteContinuation(ctx, op.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if json.Unmarshal(raw, &op.ApplicationDatabaseRouteContinuity) != nil || op.ApplicationDatabaseRouteContinuity.Validate(*op) != nil {
		return ErrConflict
	}
	raw, err = sqlcgen.New(db).ReadApplicationDatabaseYield(ctx, op.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var y domain.HostingApplicationDatabaseYieldRequest
	if json.Unmarshal(raw, &y) != nil || y.OperationID != op.ID || y.RunID != op.RunID || y.ContinuationHash != op.ApplicationDatabaseRouteContinuity.Hash || op.Plan == nil || y.PlanHash != op.Plan.Hash {
		return ErrConflict
	}
	receipt := y.Receipt()
	op.ApplicationDatabaseYield = &receipt
	return nil
}

func continuityAgent(ctx context.Context, tx pgx.Tx, runner string) error {
	eligible, err := sqlcgen.New(tx).ApplicationDatabaseRouteRunnerEligible(ctx, runner)
	if err != nil {
		return hostingError(err)
	}
	if !eligible {
		return ErrConflict
	}
	eligible, err = sqlcgen.New(tx).ProxyVerifiedSwitchRunnerEligible(ctx, runner)
	if err != nil {
		return err
	}
	if !eligible {
		return ErrConflict
	}
	return nil
}

func connectionDeploymentSupported(ctx context.Context, tx pgx.Tx, app, database string, continuity bool) (bool, error) {
	if !continuity {
		return sqlcgen.New(tx).ConnectionApplicationSupported(ctx, sqlcgen.ConnectionApplicationSupportedParams{ID: app, DatabaseResourceID: database})
	}
	return sqlcgen.New(tx).ConnectionRouteDeploymentSupported(ctx, sqlcgen.ConnectionRouteDeploymentSupportedParams{ApplicationResourceID: app, DatabaseResourceID: database})
}

func admitApplicationDatabaseContinuity(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, in domain.HostingApplicationDatabaseRouteContinuityInput, previous domain.HostingHealthy) (domain.HostingApplicationDatabaseRouteContinuity, error) {
	var zero domain.HostingApplicationDatabaseRouteContinuity
	q := sqlcgen.New(tx)
	compatible, err := q.ApplicationDatabaseRouteSchemaCompatible(ctx)
	if err != nil || !compatible {
		return zero, ErrHostingDatabaseConnectionUnavailable
	}
	if err := continuityAgent(ctx, tx, op.Snapshot.RunnerID); err != nil {
		return zero, err
	}
	if err := q.LockHostingExecutionDestination(ctx, op.Snapshot.DestinationID); err != nil {
		return zero, err
	}
	row, err := q.ProxyGetByServer(ctx, op.Snapshot.ServerID)
	if err != nil {
		return zero, hostingError(err)
	}
	p, err := proxyLock(ctx, tx, row.ID)
	if err != nil {
		return zero, err
	}
	if !p.Enabled || p.RestoreOwnershipHeld || p.ActiveOperationID != "" || p.HoldOperationID != "" || p.LoadedGeneration != in.ExpectedLoadedGeneration || p.DaemonID != previous.Release.DaemonID || p.DestinationID != op.Snapshot.DestinationID || p.RunnerID != op.Snapshot.RunnerID || int(p.Configuration.ManagementPort) == op.Snapshot.HostPort {
		return zero, ErrConflict
	}
	pending, err := q.ProxyPending(ctx, p.ID)
	if err != nil || pending {
		return zero, ErrConflict
	}
	prior, err := proxyLoadedPredecessor(ctx, tx, p)
	if err != nil {
		return zero, err
	}
	if prior.ID != in.ExpectedLoadedProxyOperationID {
		return zero, ErrConflict
	}
	rows, err := q.ProxyListRoutes(ctx, p.ID)
	if err != nil {
		return zero, err
	}
	current, err := proxyCurrentScope(ctx, tx, p, rows)
	if err != nil {
		return zero, err
	}
	if !reflect.DeepEqual(current, proxySortedTargets(prior.Snapshot.Routes)) {
		return zero, ErrConflict
	}
	picks := make(map[string]domain.HostingRouteSwitchSelection, len(in.Routes))
	for _, pick := range in.Routes {
		picks[pick.RouteID] = pick
	}
	count := 0
	for _, route := range rows {
		if !route.Enabled || route.ResourceID != op.ResourceID {
			continue
		}
		count++
		pick, ok := picks[route.ID]
		if !ok || route.ProjectID != op.ProjectID || route.BindingID != op.BindingID || route.Version != pick.ExpectedVersion || route.SelectedReleaseOperationID != pick.ExpectedSelectedReleaseOperationID || route.SelectedReleaseOperationID != previous.OperationID {
			return zero, ErrConflict
		}
	}
	if count != len(in.Routes) || count == 0 {
		return zero, ErrConflict
	}
	c := domain.HostingApplicationDatabaseRouteContinuity{Version: domain.HostingApplicationDatabaseRouteContinuityVersion, OperationID: op.ID, ProxyOperationID: "appdb_proxy_" + op.ID, FinalizeRunID: "finalize_" + op.ID, Request: in, ProxyID: p.ID, LoadedSnapshotHash: domain.HostingDocumentHash(prior.Snapshot), PreviousHealthy: previous}
	c.Hash = c.DocumentHash()
	return c, c.Validate(op)
}

func continuityFinalizeRun(op domain.HostingOperation, run string) bool {
	return op.ApplicationDatabaseRouteContinuity != nil && run == op.ApplicationDatabaseRouteContinuity.FinalizeRunID
}

// Exact applied proof is checked again before every finalization effect. A
// failed, rolled back, superseded, or metadata-conflicted switch grants nothing.
func continuityProxyApplied(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	c := op.ApplicationDatabaseRouteContinuity
	if c == nil || c.Validate(op) != nil || op.ApplicationDatabaseYield == nil || op.Plan == nil {
		return ErrConflict
	}
	// The continuation controller locks database targets before the proxy. Match
	// that order for claims and finalize requests, including duplicate wakeups.
	if err := databaseConnectionAuthority(ctx, tx, op, false, false); err != nil {
		return err
	}
	q := sqlcgen.New(tx)
	stage, err := connectionStage(ctx, q, op)
	if err != nil || stage == nil || stage.Phase != "ready" || stage.Hash != op.ApplicationDatabaseYield.StageHash || stage.Candidate.DocumentHash() != op.ApplicationDatabaseYield.CandidateHash {
		return ErrConflict
	}
	p, err := proxyLock(ctx, tx, c.ProxyID)
	if err != nil {
		return err
	}
	applied, err := proxyOperation(ctx, tx, c.ProxyOperationID)
	if err != nil {
		return err
	}
	if applied.State != "applied" || applied.Result == nil || applied.Result.Outcome != "applied" || applied.MetadataConflict || applied.MutationHold || applied.RestoreOwnershipHeld || applied.Plan == nil || applied.Snapshot.VerifiedSwitch == nil || applied.Snapshot.VerifiedSwitch.PriorAppliedProxyOperationID != c.Request.ExpectedLoadedProxyOperationID || applied.Snapshot.VerifiedSwitch.ExpectedLoadedGeneration != c.Request.ExpectedLoadedGeneration || applied.Plan.Hash != applied.Result.PlanHash || p.LoadedGeneration != applied.Snapshot.Generation || applied.Result.LoadedGeneration != p.LoadedGeneration || p.RestoreOwnershipHeld || p.HoldOperationID != "" || p.ActiveOperationID != "" {
		return ErrConflict
	}
	if domain.ValidateHostingVerifiedSwitchResult(*applied.Plan, *applied.Result, time.Time{}) != nil {
		return ErrConflict
	}
	prior, err := proxyOperation(ctx, tx, c.Request.ExpectedLoadedProxyOperationID)
	if err != nil || domain.HostingDocumentHash(prior.Snapshot) != c.LoadedSnapshotHash {
		return ErrConflict
	}
	rows, err := q.ProxyListRoutes(ctx, p.ID)
	if err != nil {
		return err
	}
	scope, err := continuityScope(ctx, tx, p, op, rows, true)
	if err != nil || !reflect.DeepEqual(scope, proxySortedTargets(applied.Snapshot.Routes)) {
		return ErrConflict
	}
	return proxyAuthorize(ctx, tx, op.CreatedBy, op.ProjectID, domain.RoleOwner, false)
}

func continuityFinalizeAuthority(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, op domain.HostingOperation, settled bool) error {
	if !continuityFinalizeRun(op, a.RunID) || op.ApplicationDatabaseYield == nil || !settled && (!op.MutationHold || op.State != "applying" && op.State != "verifying") {
		return ErrConflict
	}
	if err := hostingRequireMode(ctx, tx, a, "finalize"); err != nil {
		return err
	}
	if err := continuityProxyApplied(ctx, tx, op); err != nil {
		return err
	}
	return finalDatabaseConnectionAuthority(ctx, tx, runner, a, op, false, settled)
}

// HostingApplicationDatabaseYield persists append-before-send readiness evidence,
// ends the original task and lease, and wakes a separate durable controller job.
func (s *PostgresStore) HostingApplicationDatabaseYield(ctx context.Context, runner, fingerprint string, in domain.HostingApplicationDatabaseYieldRequest) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	if in.Validate() != nil {
		return zero, ErrConflict
	}
	ctx, err := BindHostingDatabaseConnectionRunnerContext(ctx, runner, fingerprint)
	if err != nil {
		return zero, err
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingOperation, error) {
		op, err := connectionRunnerOperation(ctx, tx, runner, in.HostingAuthority, false)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		if _, err = q.LockHostingOperationState(ctx, op.ID); err != nil {
			return zero, err
		}
		raw, err := q.ReadApplicationDatabaseYield(ctx, op.ID)
		if err == nil {
			if !connectionJSONMatches(raw, in) {
				return zero, ErrConflict
			}
			return op, databaseConnectionHistoricalRunnerRead(ctx, tx, runner, op)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		stage, err := connectionStage(ctx, q, op)
		if err != nil || stage == nil || in.ValidateStage(op, *stage) != nil || !op.MutationHold || op.State != "applying" && op.State != "verifying" {
			return zero, ErrConflict
		}
		if err = hostingRequireMode(ctx, tx, in.HostingAuthority, "deploy"); err != nil {
			return zero, err
		}
		if err = finalDatabaseConnectionAuthority(ctx, tx, runner, in.HostingAuthority, op, false, false); err != nil {
			return zero, err
		}
		if err = q.DeferApplicationDatabaseYield(ctx); err != nil {
			return zero, err
		}
		if err = q.InsertApplicationDatabaseYield(ctx, sqlcgen.InsertApplicationDatabaseYieldParams{OperationID: op.ID, ReceiptID: in.ReceiptID, Request: buildJSON(in), RequestHash: domain.HostingDocumentHash(in)}); err != nil {
			return zero, err
		}
		if _, err = s.hostingJobs.EnqueueApplicationDatabaseRouteTx(ctx, tx, hostingjobs.ApplicationDatabaseRouteArgs{OperationID: op.ID}); err != nil {
			return zero, err
		}
		if _, err = q.CompleteHostingLease(ctx, sqlcgen.CompleteHostingLeaseParams{ID: in.LeaseID, Status: "succeeded", CompletionKey: &in.ReceiptID}); err != nil {
			return zero, err
		}
		if _, err = q.CompleteHostingTask(ctx, sqlcgen.CompleteHostingTaskParams{ID: in.RunID, Status: "succeeded"}); err != nil {
			return zero, err
		}
		if err = finalDatabaseConnectionAuthority(ctx, tx, runner, in.HostingAuthority, op, false, true); err != nil {
			return zero, err
		}
		receipt := in.Receipt()
		op.ApplicationDatabaseYield = &receipt
		return op, nil
	})
}

func (h deploymentOnlyHandler) PrepareApplicationDatabaseRoute(ctx context.Context, id string) error {
	return h.store.PrepareApplicationDatabaseRoute(ctx, id)
}

// PrepareApplicationDatabaseRoute performs metadata transitions only. It never
// waits on runner work in a lease and never modifies the original operation RunID.
func (s *PostgresStore) PrepareApplicationDatabaseRoute(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	waiting := false
	_, err := buildTransaction(ctx, s, func(tx pgx.Tx) (bool, error) {
		op, err := hostingOperation(ctx, tx, id)
		if err != nil {
			return false, err
		}
		if _, err = sqlcgen.New(tx).LockHostingOperationState(ctx, id); err != nil {
			return false, err
		}
		op, err = hostingOperation(ctx, tx, id)
		if err != nil {
			return false, err
		}
		c := op.ApplicationDatabaseRouteContinuity
		if c == nil || op.ApplicationDatabaseYield == nil {
			return false, ErrConflict
		}
		if op.State == "succeeded" || op.State == "reconciliation_required" || op.State == "failed" || op.State == "canceled" {
			return true, nil
		}
		if err = databaseConnectionAuthority(ctx, tx, op, false, false); err != nil {
			return false, err
		}
		q := sqlcgen.New(tx)
		p, err := proxyOperation(ctx, tx, c.ProxyOperationID)
		if errors.Is(err, ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
			if err = s.createApplicationDatabaseSwitch(ctx, tx, op); err != nil {
				return false, err
			}
			waiting = true
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if p.State != "applied" {
			if p.State == "failed" || p.State == "reconciliation_required" || p.State == "superseded" || p.Result != nil {
				return true, nil
			}
			waiting = true
			return true, nil
		}
		if err = continuityProxyApplied(ctx, tx, op); err != nil {
			return false, err
		}
		exists, err := q.ApplicationDatabaseFinalizeTargetExists(ctx, c.FinalizeRunID)
		if err != nil {
			return false, err
		}
		if exists {
			return true, nil
		}
		spec := domain.RunSpec{Type: domain.RunTypeImageDeploy, HostingOperationID: op.ID, HostingMode: "finalize", Inputs: map[string]any{}}
		if _, err = q.CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: c.FinalizeRunID, ProjectID: op.ProjectID, RunSpec: buildJSON(spec), RequestedBy: op.CreatedBy}); err != nil {
			return false, err
		}
		if err = q.CreateApplicationDatabaseFinalizeTarget(ctx, sqlcgen.CreateApplicationDatabaseFinalizeTargetParams{RunID: c.FinalizeRunID, OperationID: op.ID, BindingID: op.BindingID, RunnerID: op.Snapshot.RunnerID}); err != nil {
			return false, err
		}
		return true, databaseConnectionAuthority(ctx, tx, op, false, false)
	})
	if err != nil {
		return err
	}
	if waiting {
		return hostingjobs.ErrDatabaseConnectionWaiting
	}
	return nil
}

// continuityScope admits exactly this ready held candidate. It does not relax
// ordinary busy binding admission and rechecks grants for every selected route.
func continuityScope(ctx context.Context, tx pgx.Tx, p domain.HostingProxy, op domain.HostingOperation, rows []sqlcgen.HostingProxyRoute, selected bool) ([]domain.HostingProxyRouteTarget, error) {
	c := op.ApplicationDatabaseRouteContinuity
	if c == nil || c.Validate(op) != nil || op.ApplicationDatabaseYield == nil || op.Plan == nil || !op.MutationHold || op.RestoreOwnershipHeld || op.State != "applying" && op.State != "verifying" || p.ID != c.ProxyID || p.RunnerID != op.Snapshot.RunnerID || p.DaemonID != op.Release.DaemonID {
		return nil, ErrConflict
	}
	if err := databaseConnectionAuthority(ctx, tx, op, false, false); err != nil {
		return nil, err
	}
	q := sqlcgen.New(tx)
	stage, err := connectionStage(ctx, q, op)
	if err != nil || stage == nil || stage.Phase != "ready" || stage.Hash != op.ApplicationDatabaseYield.StageHash || stage.Candidate.DocumentHash() != op.ApplicationDatabaseYield.CandidateHash {
		return nil, ErrConflict
	}
	state, err := q.LockHostingPortReservation(ctx, op.BindingID)
	if err != nil || !state.RestoreAdmitted || state.HoldOperationID == nil || *state.HoldOperationID != op.ID {
		return nil, ErrConflict
	}
	if err = proxyLockRouteResources(ctx, tx, rows); err != nil {
		return nil, err
	}
	picks := make(map[string]domain.HostingRouteSwitchSelection, len(c.Request.Routes))
	for _, pick := range c.Request.Routes {
		picks[pick.RouteID] = pick
	}
	count := 0
	out := make([]domain.HostingProxyRouteTarget, 0, len(rows))
	for _, row := range rows {
		if !row.Enabled {
			continue
		}
		if row.ResourceID != op.ResourceID {
			if err = proxyRouteBindingAvailable(ctx, tx, p, row); err != nil {
				return nil, err
			}
			target, e := proxyHealthyTarget(ctx, tx, p, row)
			if e != nil {
				return nil, e
			}
			out = append(out, target)
			continue
		}
		count++
		pick, ok := picks[row.ID]
		version, release := pick.ExpectedVersion, pick.ExpectedSelectedReleaseOperationID
		if selected {
			version++
			release = op.ID
		}
		if !ok || row.ProjectID != op.ProjectID || row.BindingID != op.BindingID || row.Version != version || row.SelectedReleaseOperationID != release {
			return nil, ErrConflict
		}
		if err = databaseBindingGrant(ctx, q, op.ProjectID, p.DestinationID); err != nil {
			return nil, err
		}
		grant, e := q.ProxyGetGrant(ctx, row.GrantID)
		if e != nil || grant.RevokedAt != nil || grant.ProxyID != p.ID || grant.ProjectID != op.ProjectID || !domain.HostingHostnameGranted(grant.Hostname, grant.Wildcard, row.Hostname) {
			return nil, ErrConflict
		}
		if selected {
			out = append(out, domain.HostingProxyRouteTarget{RouteID: row.ID, ProjectID: row.ProjectID, ResourceID: row.ResourceID, BindingID: row.BindingID, ReleaseOperationID: op.ID, Hostname: row.Hostname, PathPrefix: row.PathPrefix, HostPort: uint16(op.Snapshot.HostPort)})
		} else {
			target, e := proxyHealthyTarget(ctx, tx, p, row)
			if e != nil {
				return nil, e
			}
			out = append(out, target)
		}
	}
	if count != len(picks) {
		return nil, ErrConflict
	}
	return proxySortedTargets(out), nil
}

func continuityStrictScope(ctx context.Context, tx pgx.Tx, p domain.HostingProxy, proxy domain.HostingProxyOperation, rows []sqlcgen.HostingProxyRoute) ([]domain.HostingProxyRouteTarget, error) {
	present, err := sqlcgen.New(tx).ApplicationDatabaseRouteSchemaPresent(ctx)
	if err != nil {
		return nil, err
	}
	if !present {
		return proxyCurrentScope(ctx, tx, p, rows)
	}
	id, err := sqlcgen.New(tx).ApplicationDatabaseRouteOrigin(ctx, proxy.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return proxyCurrentScope(ctx, tx, p, rows)
	}
	if err != nil {
		return nil, err
	}
	op, err := hostingOperation(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	c := op.ApplicationDatabaseRouteContinuity
	if c == nil || proxy.Snapshot.VerifiedSwitch == nil || proxy.Snapshot.VerifiedSwitch.PriorAppliedProxyOperationID != c.Request.ExpectedLoadedProxyOperationID || proxy.Snapshot.VerifiedSwitch.ExpectedLoadedGeneration != c.Request.ExpectedLoadedGeneration {
		return nil, ErrConflict
	}
	prior, err := proxyOperation(ctx, tx, c.Request.ExpectedLoadedProxyOperationID)
	if err != nil || domain.HostingDocumentHash(prior.Snapshot) != c.LoadedSnapshotHash || !reflect.DeepEqual(proxySortedTargets(domain.HostingProxyPriorRoutes(proxy.Snapshot)), proxySortedTargets(prior.Snapshot.Routes)) {
		return nil, ErrConflict
	}
	return continuityScope(ctx, tx, p, op, rows, true)
}

func (s *PostgresStore) createApplicationDatabaseSwitch(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	c := op.ApplicationDatabaseRouteContinuity
	q := sqlcgen.New(tx)
	if c == nil || op.ApplicationDatabaseYield == nil {
		return ErrConflict
	}
	if err := q.LockHostingExecutionDestination(ctx, op.Snapshot.DestinationID); err != nil {
		return err
	}
	p, err := proxyLock(ctx, tx, c.ProxyID)
	if err != nil {
		return err
	}
	if !p.Enabled || p.ActiveOperationID != "" || p.HoldOperationID != "" || p.RestoreOwnershipHeld || p.LoadedGeneration != c.Request.ExpectedLoadedGeneration {
		return ErrConflict
	}
	pending, err := q.ProxyPending(ctx, p.ID)
	if err != nil || pending {
		return ErrConflict
	}
	prior, err := proxyLoadedPredecessor(ctx, tx, p)
	if err != nil || prior.ID != c.Request.ExpectedLoadedProxyOperationID || domain.HostingDocumentHash(prior.Snapshot) != c.LoadedSnapshotHash {
		return ErrConflict
	}
	rows, err := q.ProxyListRoutes(ctx, p.ID)
	if err != nil {
		return err
	}
	current, err := continuityScope(ctx, tx, p, op, rows, false)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, proxySortedTargets(prior.Snapshot.Routes)) {
		return ErrConflict
	}
	v := &domain.HostingVerifiedSwitch{Version: 1, ExpectedLoadedGeneration: p.LoadedGeneration, PriorAppliedProxyOperationID: prior.ID}
	for _, pick := range c.Request.Routes {
		found := false
		for n, target := range current {
			if target.RouteID != pick.RouteID {
				continue
			}
			found = true
			v.Routes = append(v.Routes, domain.HostingVerifiedSwitchRoute{RouteID: pick.RouteID, PriorVersion: pick.ExpectedVersion, PriorTarget: target})
			target.ReleaseOperationID = op.ID
			target.HostPort = uint16(op.Snapshot.HostPort)
			current[n] = target
		}
		if !found {
			return ErrConflict
		}
	}
	generation, err := q.ProxyNextGeneration(ctx, p.ID)
	if err != nil {
		return err
	}
	snapshot := domain.HostingProxySnapshot{ProxyID: p.ID, ServerID: p.ServerID, DestinationID: p.DestinationID, RunnerID: p.RunnerID, Generation: generation, Configuration: p.Configuration, Routes: current, VerifiedSwitch: v}
	plan, err := domain.CompileHostingProxyPlan(snapshot, p.LoadedGeneration, p.DaemonID)
	if err != nil {
		return ErrConflict
	}
	snapshot = plan.Snapshot
	for _, pick := range c.Request.Routes {
		changed, e := q.ProxySwitchRoute(ctx, sqlcgen.ProxySwitchRouteParams{ID: pick.RouteID, SelectedReleaseOperationID: op.ID, Version: pick.ExpectedVersion, SelectedReleaseOperationID_2: pick.ExpectedSelectedReleaseOperationID})
		if e != nil {
			return e
		}
		if changed != 1 {
			return ErrConflict
		}
	}
	_, err = s.proxyInsertSnapshot(ctx, tx, p, snapshot, c.ProxyOperationID, op.ProjectID, "", op.CreatedBy, "appdb:"+op.ID, domain.HostingDocumentHash(c), domain.AuditEvent{ID: "aud_appdb_proxy_" + op.ID, CreatedAt: time.Now().UTC()}, false)
	if err != nil {
		return err
	}
	for _, route := range v.Routes {
		if _, err = q.ProxyReferencePriorRelease(ctx, sqlcgen.ProxyReferencePriorReleaseParams{OperationID: c.ProxyOperationID, RouteID: route.RouteID, PriorProxyOperationID: prior.ID, PriorReleaseOperationID: route.PriorTarget.ReleaseOperationID, PriorVersion: route.PriorVersion}); err != nil {
			return err
		}
	}
	return databaseConnectionAuthority(ctx, tx, op, false, false)
}

func recoverApplicationDatabaseRoutes(ctx context.Context, tx pgx.Tx, s *PostgresStore) error {
	present, err := sqlcgen.New(tx).ApplicationDatabaseRouteSchemaPresent(ctx)
	if err != nil || !present {
		return err
	}
	ids, err := sqlcgen.New(tx).RecoverApplicationDatabaseRoutes(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = s.hostingJobs.EnqueueApplicationDatabaseRouteTx(ctx, tx, hostingjobs.ApplicationDatabaseRouteArgs{OperationID: id}); err != nil {
			return err
		}
	}
	return nil
}

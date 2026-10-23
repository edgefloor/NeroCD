package store

import (
	"context"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

func proxyLockRouteResources(ctx context.Context, tx pgx.Tx, rows []sqlcgen.HostingProxyRoute) error {
	resources := make(map[string]bool, len(rows))
	for _, r := range rows {
		resources[r.ResourceID] = true
	}
	ids := make([]string, 0, len(resources))
	for id := range resources {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, err := sqlcgen.New(tx).ProxyLockResource(ctx, id); err != nil {
			return hostingError(err)
		}
	}
	return nil
}

func proxyRouteBindingAvailable(ctx context.Context, tx pgx.Tx, p domain.HostingProxy, row sqlcgen.HostingProxyRoute) error {
	q := sqlcgen.New(tx)
	grant, err := q.GetDestinationProjectGrant(ctx, sqlcgen.GetDestinationProjectGrantParams{DestinationID: p.DestinationID, ProjectID: row.ProjectID})
	if err != nil {
		return hostingError(err)
	}
	if grant.RevokedAt != nil {
		return ErrConflict
	}
	busy, err := q.HostingBindingBusy(ctx, row.BindingID)
	if err != nil {
		return err
	}
	if busy {
		return ErrConflict
	}
	state, err := q.LockHostingPortReservation(ctx, row.BindingID)
	if err != nil {
		return hostingError(err)
	}
	if !state.RestoreAdmitted || state.HoldOperationID != nil {
		return ErrHostingRestoreHold
	}
	held, err := q.HostingBindingRestoreHeld(ctx, row.BindingID)
	if err != nil {
		return err
	}
	if held {
		return ErrHostingRestoreHold
	}
	return nil
}

func proxyCurrentScope(ctx context.Context, tx pgx.Tx, p domain.HostingProxy, rows []sqlcgen.HostingProxyRoute) ([]domain.HostingProxyRouteTarget, error) {
	if err := proxyLockRouteResources(ctx, tx, rows); err != nil {
		return nil, err
	}
	var routes []domain.HostingProxyRouteTarget
	for _, r := range rows {
		if !r.Enabled {
			continue
		}
		if err := proxyRouteBindingAvailable(ctx, tx, p, r); err != nil {
			return nil, err
		}
		target, err := proxyHealthyTarget(ctx, tx, p, r)
		if err != nil {
			return nil, err
		}
		routes = append(routes, target)
	}
	sort.Slice(routes, func(i, j int) bool { return routes[i].RouteID < routes[j].RouteID })
	return routes, nil
}

func proxyLoadedPredecessor(ctx context.Context, tx pgx.Tx, p domain.HostingProxy) (domain.HostingProxyOperation, error) {
	id, err := sqlcgen.New(tx).ProxyLoadedAppliedOperation(ctx, sqlcgen.ProxyLoadedAppliedOperationParams{ProxyID: p.ID, Generation: p.LoadedGeneration})
	if err != nil {
		return domain.HostingProxyOperation{}, hostingError(err)
	}
	op, err := proxyOperation(ctx, tx, id)
	if err != nil {
		return op, err
	}
	if op.Result == nil || op.Result.Outcome != "applied" || op.Result.LoadedGeneration != p.LoadedGeneration || op.MutationHold || op.RestoreOwnershipHeld {
		return op, ErrConflict
	}
	return op, nil
}

func proxySortedTargets(routes []domain.HostingProxyRouteTarget) []domain.HostingProxyRouteTarget {
	copy := append([]domain.HostingProxyRouteTarget(nil), routes...)
	sort.Slice(copy, func(i, j int) bool { return copy[i].RouteID < copy[j].RouteID })
	return copy
}

func proxyOrdinaryMutationAllowed(ctx context.Context, tx pgx.Tx, p domain.HostingProxy) error {
	ids, err := sqlcgen.New(tx).ProxyStrictPending(ctx, p.ID)
	if err != nil {
		return err
	}
	if len(ids) != 0 {
		return ErrConflict
	}
	return nil
}

// RequestHostingRouteSwitch freezes exact loaded routes and derived release ports.
func (s *PostgresStore) RequestHostingRouteSwitch(ctx context.Context, resource, operation, actor string, in domain.HostingRouteSwitchInput, audit domain.AuditEvent) (domain.HostingProxyOperation, error) {
	if s.hostingJobs == nil {
		return domain.HostingProxyOperation{}, hostingjobs.ErrUnavailable
	}
	in, err := domain.CanonicalHostingRouteSwitchInput(in)
	if err != nil {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	placement, err := s.queries.ProxyResourceTarget(ctx, resource)
	if err != nil {
		return domain.HostingProxyOperation{}, hostingError(err)
	}
	key, err := proxyRequestKey("switch:"+resource, in.IdempotencyKey)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	requestHash := domain.HostingDocumentHash(in)
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	q := sqlcgen.New(tx)
	if err = q.LockHostingExecutionDestination(ctx, placement.DestinationID); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	proxyRow, err := q.ProxyGetByServer(ctx, placement.ServerID)
	if err != nil {
		return domain.HostingProxyOperation{}, hostingError(err)
	}
	p, err := proxyLock(ctx, tx, proxyRow.ID)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	rows, err := q.ProxyListRoutes(ctx, p.ID)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if err = proxyLockRouteResources(ctx, tx, rows); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if err = proxyAuthorize(ctx, tx, actor, placement.ProjectID, domain.RoleMaintainer, false); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if replay, err := proxyReplay(ctx, tx, p.ID, key, requestHash); err != nil {
		return domain.HostingProxyOperation{}, err
	} else if replay != "" {
		op, err := proxyOperation(ctx, tx, replay)
		if err != nil {
			return op, err
		}
		return op, proxyCheckLegacyToken(ctx, tx, actor)
	}
	if !p.Enabled || p.RestoreOwnershipHeld || p.ActiveOperationID != "" || p.HoldOperationID != "" || p.LoadedGeneration != in.ExpectedLoadedGeneration || p.DaemonID == "" {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	pending, err := q.ProxyPending(ctx, p.ID)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	eligible, err := q.ProxyVerifiedSwitchRunnerEligible(ctx, p.RunnerID)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if pending || !eligible {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	prior, err := proxyLoadedPredecessor(ctx, tx, p)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	current, err := proxyCurrentScope(ctx, tx, p, rows)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if !reflect.DeepEqual(current, proxySortedTargets(prior.Snapshot.Routes)) {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	byID := make(map[string]sqlcgen.HostingProxyRoute, len(rows))
	for _, r := range rows {
		byID[r.ID] = r
	}
	selected := make(map[string]domain.HostingProxyRouteTarget, len(in.Routes))
	v := &domain.HostingVerifiedSwitch{Version: 1, ExpectedLoadedGeneration: p.LoadedGeneration, PriorAppliedProxyOperationID: prior.ID}
	for _, pick := range in.Routes {
		row, ok := byID[pick.RouteID]
		if !ok || !row.Enabled || row.ResourceID != resource || row.ProjectID != placement.ProjectID || row.BindingID != placement.BindingID || row.Version != pick.ExpectedVersion || row.SelectedReleaseOperationID != pick.ExpectedSelectedReleaseOperationID {
			return domain.HostingProxyOperation{}, ErrConflict
		}
		original, err := proxyHealthyTarget(ctx, tx, p, row)
		if err != nil {
			return domain.HostingProxyOperation{}, err
		}
		row.SelectedReleaseOperationID = in.TargetReleaseOperationID
		target, err := proxyHealthyTarget(ctx, tx, p, row)
		if err != nil {
			return domain.HostingProxyOperation{}, err
		}
		selected[row.ID] = target
		v.Routes = append(v.Routes, domain.HostingVerifiedSwitchRoute{RouteID: row.ID, PriorVersion: pick.ExpectedVersion, PriorTarget: original})
	}
	for n, r := range current {
		if target, ok := selected[r.RouteID]; ok {
			current[n] = target
		}
	}
	generation, err := q.ProxyNextGeneration(ctx, p.ID)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	snapshot := domain.HostingProxySnapshot{ProxyID: p.ID, ServerID: p.ServerID, DestinationID: p.DestinationID, RunnerID: p.RunnerID, Generation: generation, Configuration: p.Configuration, Routes: current, VerifiedSwitch: v}
	plan, err := domain.CompileHostingProxyPlan(snapshot, p.LoadedGeneration, p.DaemonID)
	if err != nil {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	snapshot = plan.Snapshot
	for _, pick := range in.Routes {
		changed, err := q.ProxySwitchRoute(ctx, sqlcgen.ProxySwitchRouteParams{ID: pick.RouteID, SelectedReleaseOperationID: in.TargetReleaseOperationID, Version: pick.ExpectedVersion, SelectedReleaseOperationID_2: pick.ExpectedSelectedReleaseOperationID})
		if err != nil {
			return domain.HostingProxyOperation{}, hostingError(err)
		}
		if changed != 1 {
			return domain.HostingProxyOperation{}, ErrConflict
		}
	}
	op, err := s.proxyInsertSnapshot(ctx, tx, p, snapshot, operation, placement.ProjectID, "", actor, key, requestHash, audit, false)
	if err != nil {
		return op, err
	}
	for _, r := range v.Routes {
		if _, err = q.ProxyReferencePriorRelease(ctx, sqlcgen.ProxyReferencePriorReleaseParams{OperationID: operation, RouteID: r.RouteID, PriorProxyOperationID: prior.ID, PriorReleaseOperationID: r.PriorTarget.ReleaseOperationID, PriorVersion: r.PriorVersion}); err != nil {
			return domain.HostingProxyOperation{}, err
		}
	}
	return op, proxyCommitMutation(ctx, tx, actor)
}

func (s *PostgresStore) proxyInsertSnapshot(ctx context.Context, tx pgx.Tx, p domain.HostingProxy, snapshot domain.HostingProxySnapshot, operation, project, route, actor, key, requestHash string, audit domain.AuditEvent, supersede bool) (domain.HostingProxyOperation, error) {
	q := sqlcgen.New(tx)
	if _, err := domain.CompileHostingProxyPlan(snapshot, p.LoadedGeneration, p.DaemonID); err != nil {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if len(raw) > 32<<10 {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	if supersede {
		if _, err = q.ProxySupersedeUnadmitted(ctx, p.ID); err != nil {
			return domain.HostingProxyOperation{}, err
		}
		if _, err = q.ProxyClearUnadmittedActive(ctx, p.ID); err != nil {
			return domain.HostingProxyOperation{}, err
		}
	}
	if _, err = q.ProxyCreateOperation(ctx, sqlcgen.ProxyCreateOperationParams{ID: operation, ProxyID: p.ID, ProjectID: proxyPointer(project), RouteID: proxyPointer(route), Generation: snapshot.Generation, IdempotencyKey: key, RequestHash: requestHash, Snapshot: raw, SnapshotHash: domain.HostingDocumentHash(snapshot), CreatedBy: actor}); err != nil {
		return domain.HostingProxyOperation{}, hostingError(err)
	}
	for _, r := range snapshot.Routes {
		if _, err = q.ProxyReferenceRelease(ctx, sqlcgen.ProxyReferenceReleaseParams{OperationID: operation, RouteID: r.RouteID, ReleaseOperationID: r.ReleaseOperationID}); err != nil {
			return domain.HostingProxyOperation{}, err
		}
	}
	if _, err = s.hostingJobs.EnqueueProxyTx(ctx, tx, hostingjobs.PrepareProxyArgs{OperationID: operation}); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	audit.ActorID = actor
	audit.TargetID = operation
	audit.Action = "hosting.proxy_generation_requested"
	audit.Metadata = map[string]any{"proxy_id": p.ID, "generation": snapshot.Generation, "route_id": route, "project_id": project}
	if snapshot.VerifiedSwitch != nil {
		audit.Metadata["verified_switch"] = true
	}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	return proxyOperation(ctx, tx, operation)
}

func proxyValidateStrictSnapshot(ctx context.Context, tx pgx.Tx, p domain.HostingProxy, op domain.HostingProxyOperation) error {
	v := op.Snapshot.VerifiedSwitch
	if v == nil {
		return nil
	}
	if !p.Enabled || p.LoadedGeneration != v.ExpectedLoadedGeneration || p.RestoreOwnershipHeld {
		return ErrConflict
	}
	prior, err := proxyLoadedPredecessor(ctx, tx, p)
	if err != nil {
		return err
	}
	if prior.ID != v.PriorAppliedProxyOperationID || !reflect.DeepEqual(proxySortedTargets(prior.Snapshot.Routes), proxySortedTargets(domain.HostingProxyPriorRoutes(op.Snapshot))) {
		return ErrConflict
	}
	q := sqlcgen.New(tx)
	rows, err := q.ProxyListRoutes(ctx, p.ID)
	if err != nil {
		return err
	}
	current, err := proxyCurrentScope(ctx, tx, p, rows)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(current, proxySortedTargets(op.Snapshot.Routes)) {
		return ErrConflict
	}
	eligible, err := q.ProxyVerifiedSwitchRunnerEligible(ctx, p.RunnerID)
	if err != nil {
		return err
	}
	if !eligible {
		return ErrConflict
	}
	refs, err := q.ProxyPriorReleaseReferences(ctx, op.ID)
	if err != nil {
		return err
	}
	if len(refs) != len(v.Routes) {
		return ErrConflict
	}
	for n, selected := range v.Routes {
		r := refs[n]
		if r.RouteID != selected.RouteID || r.PriorVersion != selected.PriorVersion || r.PriorProxyOperationID != prior.ID || r.PriorReleaseOperationID != selected.PriorTarget.ReleaseOperationID {
			return ErrConflict
		}
		found := false
		for _, row := range rows {
			if row.ID == selected.RouteID {
				found = row.Version == selected.PriorVersion+1
				break
			}
		}
		if !found {
			return ErrConflict
		}
	}
	return proxyAuthorize(ctx, tx, op.CreatedBy, v.Routes[0].PriorTarget.ProjectID, domain.RoleMaintainer, false)
}

// proxyRestoreSwitchMetadata changes only the exact desired selection we own.
// Conflicts retain a separate hold; durable engine receipts remain truthful.
func proxyRestoreSwitchMetadata(ctx context.Context, tx pgx.Tx, p domain.HostingProxy, op domain.HostingProxyOperation) (bool, error) {
	v := op.Snapshot.VerifiedSwitch
	if v == nil {
		return false, nil
	}
	q := sqlcgen.New(tx)
	rows, err := q.ProxyListRoutes(ctx, p.ID)
	if err != nil {
		return false, err
	}
	if err = proxyLockRouteResources(ctx, tx, rows); err != nil {
		return false, err
	}
	candidate := make(map[string]string, len(op.Snapshot.Routes))
	for _, r := range op.Snapshot.Routes {
		candidate[r.RouteID] = r.ReleaseOperationID
	}
	conflicts := make([]string, 0)
	for _, r := range v.Routes {
		changed, err := q.ProxyRestoreSwitchRoute(ctx, sqlcgen.ProxyRestoreSwitchRouteParams{ID: r.RouteID, SelectedReleaseOperationID: r.PriorTarget.ReleaseOperationID, Version: r.PriorVersion + 1, SelectedReleaseOperationID_2: candidate[r.RouteID]})
		if err != nil {
			return false, err
		}
		if changed != 1 {
			conflicts = append(conflicts, r.RouteID)
		}
	}
	if len(conflicts) == 0 {
		return false, nil
	}
	if p.ActiveOperationID == "" {
		if _, err = q.ProxySetActive(ctx, sqlcgen.ProxySetActiveParams{ID: p.ID, ActiveOperationID: proxyPointer(op.ID)}); err != nil {
			return false, err
		}
	}
	if p.HoldOperationID == "" {
		changed, err := q.ProxyHold(ctx, sqlcgen.ProxyHoldParams{ID: p.ID, HoldOperationID: proxyPointer(op.ID)})
		if err != nil {
			return false, err
		}
		if changed != 1 {
			return false, ErrConflict
		}
	}
	if _, err = q.ProxySetState(ctx, sqlcgen.ProxySetStateParams{ID: op.ID, State: "uncertain"}); err != nil {
		return false, err
	}
	audit := domain.AuditEvent{ID: "aud_switch_metadata_conflict_" + op.ID, ActorID: op.CreatedBy, Action: "hosting.route_switch_metadata_conflict", TargetID: op.ID, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"proxy_id": p.ID, "route_ids": conflicts, "recovery": "unsupported_requires_reviewed_reconciliation"}}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return false, err
	}
	return true, nil
}

func proxyValidateOperationSnapshot(ctx context.Context, tx pgx.Tx, p domain.HostingProxy, op domain.HostingProxyOperation) error {
	if op.Snapshot.VerifiedSwitch != nil {
		return proxyValidateStrictSnapshot(ctx, tx, p, op)
	}
	return proxyValidateSnapshot(ctx, tx, p, op.Snapshot)
}

// proxyPrepareRevocation restores unadmitted desired state before withdrawing it.
// Admitted or conflicting metadata remains fenced independently of grant revocation.
func proxyPrepareRevocation(ctx context.Context, tx pgx.Tx, p domain.HostingProxy) (domain.HostingProxy, *domain.HostingProxyOperation, error) {
	q := sqlcgen.New(tx)
	ids, err := q.ProxyStrictPending(ctx, p.ID)
	if err != nil {
		return p, nil, err
	}
	var held *domain.HostingProxyOperation
	for _, id := range ids {
		op, err := proxyOperation(ctx, tx, id)
		if err != nil {
			return p, nil, err
		}
		if op.MutationHold {
			held = &op
			continue
		}
		conflict, err := proxyRestoreSwitchMetadata(ctx, tx, p, op)
		if err != nil {
			return p, nil, err
		}
		if conflict {
			held = &op
			continue
		}
		if _, err = q.ProxySetState(ctx, sqlcgen.ProxySetStateParams{ID: id, State: "superseded"}); err != nil {
			return p, nil, err
		}
		if _, err = q.ProxySettleUnadmitted(ctx, sqlcgen.ProxySettleUnadmittedParams{ID: p.ID, ActiveOperationID: proxyPointer(id)}); err != nil {
			return p, nil, err
		}
	}
	p, err = proxyLoad(ctx, tx, p.ID)
	return p, held, err
}

func (s *PostgresStore) proxyQueueRevocation(ctx context.Context, tx pgx.Tx, p domain.HostingProxy, held *domain.HostingProxyOperation, operation, actor, key, requestHash string, audit domain.AuditEvent) (domain.HostingProxyOperation, error) {
	if held == nil {
		return s.queueProxyGeneration(ctx, tx, p, operation, p.ControlProjectID, "", actor, key, requestHash, audit)
	}
	prior, err := proxyLoadedPredecessor(ctx, tx, p)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if held.Snapshot.VerifiedSwitch == nil || held.Snapshot.VerifiedSwitch.PriorAppliedProxyOperationID != prior.ID {
		return domain.HostingProxyOperation{}, ErrConflict
	}
	q := sqlcgen.New(tx)
	rows, err := q.ProxyListRoutes(ctx, p.ID)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	if err = proxyLockRouteResources(ctx, tx, rows); err != nil {
		return domain.HostingProxyOperation{}, err
	}
	allowed := make(map[string]bool, len(rows))
	for _, r := range rows {
		if !p.Enabled || !r.Enabled {
			continue
		}
		grant, err := q.ProxyGetGrant(ctx, r.GrantID)
		if err != nil {
			return domain.HostingProxyOperation{}, err
		}
		destination, err := q.GetDestinationProjectGrant(ctx, sqlcgen.GetDestinationProjectGrantParams{DestinationID: p.DestinationID, ProjectID: r.ProjectID})
		if err != nil {
			return domain.HostingProxyOperation{}, err
		}
		allowed[r.ID] = grant.RevokedAt == nil && destination.RevokedAt == nil && grant.ProxyID == p.ID && grant.ProjectID == r.ProjectID && domain.HostingHostnameGranted(grant.Hostname, grant.Wildcard, r.Hostname)
	}
	routes := make([]domain.HostingProxyRouteTarget, 0, len(prior.Snapshot.Routes))
	for _, r := range prior.Snapshot.Routes {
		if allowed[r.RouteID] {
			routes = append(routes, r)
		}
	}
	generation, err := q.ProxyNextGeneration(ctx, p.ID)
	if err != nil {
		return domain.HostingProxyOperation{}, err
	}
	snapshot := domain.HostingProxySnapshot{ProxyID: p.ID, ServerID: p.ServerID, DestinationID: p.DestinationID, RunnerID: p.RunnerID, Generation: generation, Configuration: p.Configuration, Routes: routes}
	return s.proxyInsertSnapshot(ctx, tx, p, snapshot, operation, p.ControlProjectID, "", actor, key, requestHash, audit, true)
}

func (s *PostgresStore) proxyFinishDeferredWithdrawal(ctx context.Context, tx pgx.Tx, p domain.HostingProxy, settled domain.HostingProxyOperation) error {
	q := sqlcgen.New(tx)
	ids, err := q.ProxyDeferredWithdrawals(ctx, sqlcgen.ProxyDeferredWithdrawalsParams{ProxyID: p.ID, Generation: settled.Generation})
	if err != nil || len(ids) == 0 {
		return err
	}
	p, err = proxyLoad(ctx, tx, p.ID)
	if err != nil {
		return err
	}
	digest := strings.TrimPrefix(domain.HostingDocumentHash(settled.ID), "sha256:")
	id := "pop_withdraw_" + digest[:24]
	audit := domain.AuditEvent{ID: "aud_withdraw_" + digest[:24], ActorID: settled.CreatedBy, CreatedAt: time.Now().UTC()}
	_, err = s.queueProxyGeneration(ctx, tx, p, id, p.ControlProjectID, "", settled.CreatedBy, "deferred:"+digest, domain.HostingDocumentHash(ids), audit)
	return err
}

var _ HostingRouteSwitchRepository = (*PostgresStore)(nil)

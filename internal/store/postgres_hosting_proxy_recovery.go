package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

type proxyRecoveryState struct {
	p      domain.HostingProxy
	op     domain.HostingProxyOperation
	review domain.HostingProxyRecoveryReview
}

// proxyRecoveryScope takes attempt locks before destination/proxy/resource locks.
// It never interprets an expired lease or a missing result as completed effects.
func proxyRecoveryScope(ctx context.Context, tx pgx.Tx, operation, actor string) (proxyRecoveryState, error) {
	var state proxyRecoveryState
	op, err := proxyOperation(ctx, tx, operation)
	if err != nil {
		return state, err
	}
	if op.RunID == "" {
		return state, ErrConflict
	}
	q := sqlcgen.New(tx)
	if _, err = q.LockRunID(ctx, op.RunID); err != nil {
		return state, hostingError(err)
	}
	authorityRaw, err := q.ProxyRecoveryCompleteAuthority(ctx, operation)
	if err != nil {
		return state, hostingError(err)
	}
	var authority domain.HostingAuthority
	if err = json.Unmarshal(authorityRaw, &authority); err != nil {
		return state, err
	}
	lease, err := q.ProxyRecoveryLockLease(ctx, sqlcgen.ProxyRecoveryLockLeaseParams{ID: authority.LeaseID, RunID: op.RunID})
	if err != nil {
		return state, hostingError(err)
	}
	if err = q.LockHostingExecutionDestination(ctx, op.Snapshot.DestinationID); err != nil {
		return state, err
	}
	p, err := proxyLock(ctx, tx, op.ProxyID)
	if err != nil {
		return state, err
	}
	op, err = proxyOperation(ctx, tx, operation)
	if err != nil {
		return state, err
	}
	rows, err := q.ProxyListRoutes(ctx, p.ID)
	if err != nil {
		return state, err
	}
	if err = proxyLockRouteResources(ctx, tx, rows); err != nil {
		return state, err
	}
	if err = proxyAuthorize(ctx, tx, actor, p.ControlProjectID, domain.RoleOwner, true); err != nil {
		return state, err
	}
	state.p = p
	state.op = op
	if op.Snapshot.ProxyID != p.ID || op.Snapshot.ServerID != p.ServerID || op.Snapshot.DestinationID != p.DestinationID || op.Snapshot.RunnerID != p.RunnerID || !reflect.DeepEqual(op.Snapshot.Configuration, p.Configuration) {
		return state, ErrConflict
	}
	v := op.Snapshot.VerifiedSwitch
	if op.State != "uncertain" || !op.MetadataConflict || !op.MutationHold || op.RestoreOwnershipHeld || p.RestoreOwnershipHeld || p.ActiveOperationID != op.ID || p.HoldOperationID != op.ID || v == nil || v.Version != 1 || op.Plan == nil || op.CompleteReceipt == nil || op.Result == nil || len(v.Routes) < 1 || len(v.Routes) > domain.HostingProxyMaxRoutes || len(rows) > domain.HostingProxyMaxRoutes {
		return state, ErrConflict
	}
	if op.Result.Outcome != "rolled_back" && op.Result.Outcome != "refused" {
		return state, ErrConflict
	}
	if authority.OperationID != op.ID || authority.RunID != op.RunID || authority.Attempt != int(lease.Attempt) || authority.Fence != lease.Fence || lease.RunnerID != p.RunnerID || lease.Status != "failed" || lease.CompletedAt == nil || proxyString(lease.CompletionKey) != op.CompleteReceipt.ID {
		return state, ErrConflict
	}
	proof, err := q.ProxyRecoveryTerminalTask(ctx, sqlcgen.ProxyRecoveryTerminalTaskParams{RunID: op.RunID, NextAttempt: int32(authority.Attempt + 1), RunnerID: p.RunnerID, OperationID: op.ID, ProxyID: p.ID, ServerID: p.ServerID, DestinationID: p.DestinationID})
	if err != nil {
		return state, err
	}
	if !proof {
		return state, ErrConflict
	}
	admission, err := q.ProxyRecoveryAdmission(ctx, op.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return state, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if op.Result.Outcome != "refused" {
			return state, ErrConflict
		}
	} else {
		var admittedAuthority domain.HostingAuthority
		if json.Unmarshal(admission.Authority, &admittedAuthority) != nil || admittedAuthority != authority || admission.RunID != authority.RunID || admission.LeaseID != authority.LeaseID || admission.PlanHash != op.Result.EngineHash {
			return state, ErrConflict
		}
	}
	if domain.ValidateHostingProxyPlan(*op.Plan) != nil || !reflect.DeepEqual(op.Plan.Snapshot, op.Snapshot) || op.Plan.PreviousGeneration != p.LoadedGeneration || domain.HostingDocumentHash(op.Result) != op.CompleteReceipt.BodyHash || op.Result.PlanHash != op.Plan.Hash || op.Result.DaemonID != p.DaemonID || op.Plan.DaemonID != p.DaemonID || p.LoadedGeneration != v.ExpectedLoadedGeneration {
		return state, ErrConflict
	}
	preparedHash := ""
	if op.Preparation == nil || op.PreparedReceipt == nil {
		if op.Result.Outcome != "refused" || len(admission.Authority) != 0 {
			return state, ErrConflict
		}
	} else {
		if op.Preparation.PlanHash != op.Plan.Hash || domain.HostingDocumentHash(op.Preparation) != op.PreparedReceipt.BodyHash || op.Result.EngineHash != op.Preparation.EngineHash || op.Preparation.StaticHash != p.StaticHash || op.Preparation.DaemonID != p.DaemonID {
			return state, ErrConflict
		}
		preparedHash = op.PreparedReceipt.BodyHash
	}
	prior, err := proxyLoadedPredecessor(ctx, tx, p)
	if err != nil {
		return state, err
	}
	if prior.ID != v.PriorAppliedProxyOperationID || prior.Preparation == nil || prior.Preparation.StaticHash != p.StaticHash || prior.Result.DaemonID != p.DaemonID || !reflect.DeepEqual(prior.Snapshot.Configuration, p.Configuration) || !reflect.DeepEqual(proxySortedTargets(prior.Snapshot.Routes), proxySortedTargets(domain.HostingProxyPriorRoutes(op.Snapshot))) {
		return state, ErrConflict
	}
	refs, err := q.ProxyPriorReleaseReferences(ctx, op.ID)
	if err != nil {
		return state, err
	}
	if len(refs) != len(v.Routes) {
		return state, ErrConflict
	}
	for i, r := range refs {
		s := v.Routes[i]
		if r.RouteID != s.RouteID || r.PriorVersion != s.PriorVersion || r.PriorProxyOperationID != prior.ID || r.PriorReleaseOperationID != s.PriorTarget.ReleaseOperationID {
			return state, ErrConflict
		}
	}
	// Bind all row and delegation facts, including disabled/unselected routes.
	grantIDs := make(map[string]bool)
	projects := make(map[string]bool)
	for _, row := range rows {
		grantIDs[row.GrantID] = true
		projects[row.ProjectID] = true
	}
	var grantFacts []json.RawMessage
	allowed := make(map[string]bool)
	ids := make([]string, 0, len(grantIDs))
	for id := range grantIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fact, err := q.ProxyRecoveryLockHostnameFact(ctx, id)
		if err != nil {
			return state, hostingError(err)
		}
		grantFacts = append(grantFacts, fact)
	}
	ids = ids[:0]
	for id := range projects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fact, err := q.ProxyRecoveryLockDestinationFact(ctx, sqlcgen.ProxyRecoveryLockDestinationFactParams{DestinationID: p.DestinationID, ProjectID: id})
		if err != nil {
			return state, hostingError(err)
		}
		grantFacts = append(grantFacts, fact)
	}
	for _, row := range rows {
		g, e := q.ProxyGetGrant(ctx, row.GrantID)
		if e != nil {
			return state, e
		}
		d, e := q.GetDestinationProjectGrant(ctx, sqlcgen.GetDestinationProjectGrantParams{DestinationID: p.DestinationID, ProjectID: row.ProjectID})
		if e != nil {
			return state, e
		}
		allowed[row.ID] = p.Enabled && row.Enabled && g.RevokedAt == nil && d.RevokedAt == nil && g.ProxyID == p.ID && g.ProjectID == row.ProjectID && domain.HostingHostnameGranted(g.Hostname, g.Wildcard, row.Hostname)
	}
	deferred, err := q.ProxyRecoveryDeferredFacts(ctx, sqlcgen.ProxyRecoveryDeferredFactsParams{ProxyID: p.ID, ID: op.ID})
	if err != nil {
		return state, err
	}
	competing, err := q.ProxyRecoveryCompeting(ctx, sqlcgen.ProxyRecoveryCompetingParams{ProxyID: p.ID, ID: op.ID})
	if err != nil {
		return state, err
	}
	if competing {
		return state, ErrConflict
	}
	review := domain.HostingProxyRecoveryReview{Version: 1, OperationID: op.ID, ProxyID: p.ID, ActorID: actor, LoadedGeneration: p.LoadedGeneration, DesiredGeneration: p.DesiredGeneration, PriorOperationID: prior.ID, PriorSnapshotHash: domain.HostingDocumentHash(prior.Snapshot), CompleteReceiptID: op.CompleteReceipt.ID, CompleteReceiptHash: op.CompleteReceipt.BodyHash, EngineOutcome: op.Result.Outcome, Routes: make([]domain.HostingProxyRecoveryRoute, 0, len(v.Routes)), ConvergenceRoutes: make([]domain.HostingProxyRouteTarget, 0)}
	for _, selected := range v.Routes {
		found := false
		for _, row := range rows {
			if row.ID != selected.RouteID {
				continue
			}
			found = true
			priorTarget := selected.PriorTarget
			if row.ResourceID != priorTarget.ResourceID || row.ProjectID != priorTarget.ProjectID || row.BindingID != priorTarget.BindingID || row.Hostname != priorTarget.Hostname || row.PathPrefix != priorTarget.PathPrefix {
				return state, ErrConflict
			}
			review.Routes = append(review.Routes, domain.HostingProxyRecoveryRoute{RouteID: row.ID, Version: row.Version, SelectedReleaseOperationID: row.SelectedReleaseOperationID, Enabled: row.Enabled, Authorized: allowed[row.ID], PriorTarget: priorTarget})
		}
		if !found {
			return state, ErrConflict
		}
	}
	sort.Slice(review.Routes, func(i, j int) bool { return review.Routes[i].RouteID < review.Routes[j].RouteID })
	for _, r := range proxySortedTargets(prior.Snapshot.Routes) {
		if allowed[r.RouteID] {
			review.ConvergenceRoutes = append(review.ConvergenceRoutes, r)
		}
	}
	review.FactsHash = domain.HostingDocumentHash(struct {
		Proxy                                 domain.HostingProxy
		Rows                                  []sqlcgen.HostingProxyRoute
		Grants                                []json.RawMessage
		Deferred                              json.RawMessage
		AuthorityHash, PlanHash, PreparedHash string
	}{p, rows, grantFacts, deferred, domain.HostingDocumentHash(authority), op.Plan.Hash, preparedHash})
	state.review = review
	return state, nil
}

func proxyRecoveryResolution(ctx context.Context, tx pgx.Tx, operation string) (domain.HostingProxyRecoveryResolution, error) {
	var result domain.HostingProxyRecoveryResolution
	row, err := sqlcgen.New(tx).ProxyRecoveryResolution(ctx, operation)
	if err == nil {
		err = json.Unmarshal(row.Body, &result)
	}
	return result, err
}

// proxyRecoveryRead authorizes receipt replay without acquiring mutation authority.
func proxyRecoveryRead(ctx context.Context, tx pgx.Tx, operation, actor string) error {
	op, err := proxyOperation(ctx, tx, operation)
	if err != nil {
		return err
	}
	p, err := proxyLoad(ctx, tx, op.ProxyID)
	if err != nil {
		return err
	}
	return proxyAuthorize(ctx, tx, actor, p.ControlProjectID, domain.RoleOwner, true)
}

// HostingProxyRecovery reads completed-conflict eligibility or its immutable resolution.
func (s *PostgresStore) HostingProxyRecovery(ctx context.Context, operation, actor string) (domain.HostingProxyRecoveryStatus, error) {
	status := domain.HostingProxyRecoveryStatus{OperationID: operation, Reason: "unsupported_or_unfinished"}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return status, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = proxyRecoveryRead(ctx, tx, operation, actor); err != nil {
		return status, err
	}
	resolution, err := proxyRecoveryResolution(ctx, tx, operation)
	if err == nil {
		status.Reason = "resolved"
		status.Resolution = &resolution
		return status, proxyCheckLegacyToken(ctx, tx, actor)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return status, err
	}
	// Release read locks before entering the prescribed mutation lock order.
	if err = tx.Rollback(ctx); err != nil {
		return status, err
	}
	tx, err = s.pool.Begin(ctx)
	if err != nil {
		return status, err
	}
	defer rollbackTransaction(ctx, tx)
	state, err := proxyRecoveryScope(ctx, tx, operation, actor)
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		if err = proxyRecoveryRead(ctx, tx, operation, actor); err != nil {
			return status, err
		}
		return status, proxyCheckLegacyToken(ctx, tx, actor)
	}
	if err != nil {
		return status, err
	}
	status.Eligible = true
	status.Reason = "completed_metadata_conflict"
	status.Review = &state.review
	return status, proxyCheckLegacyToken(ctx, tx, actor)
}

// PlanHostingProxyRecovery records one bounded review under current proxy authority.
func (s *PostgresStore) PlanHostingProxyRecovery(ctx context.Context, operation, planID, actor string, in domain.HostingProxyRecoveryInput, audit domain.AuditEvent) (domain.HostingProxyRecoveryPlan, error) {
	var plan domain.HostingProxyRecoveryPlan
	if err := domain.ValidateHostingProxyRecoveryInput(in); err != nil {
		return plan, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return plan, err
	}
	defer rollbackTransaction(ctx, tx)
	// Exact replay is read-authorized and retains the original expiry.
	if err = proxyRecoveryRead(ctx, tx, operation, actor); err != nil {
		return plan, err
	}
	stored, err := sqlcgen.New(tx).ProxyRecoveryPlanByKey(ctx, sqlcgen.ProxyRecoveryPlanByKeyParams{OperationID: operation, RequestKey: in.IdempotencyKey})
	if err == nil {
		if stored.ActorID != actor || stored.RequestHash != domain.HostingDocumentHash(in) {
			return plan, ErrConflict
		}
		if err = json.Unmarshal(stored.Body, &plan); err != nil {
			return plan, err
		}
		return plan, proxyCheckLegacyToken(ctx, tx, actor)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return plan, err
	}
	if err = tx.Rollback(ctx); err != nil {
		return plan, err
	}
	tx, err = s.pool.Begin(ctx)
	if err != nil {
		return plan, err
	}
	defer rollbackTransaction(ctx, tx)
	state, err := proxyRecoveryScope(ctx, tx, operation, actor)
	if err != nil {
		return plan, err
	}
	if state.review.LoadedGeneration != in.ExpectedLoadedGeneration || state.review.DesiredGeneration != in.ExpectedDesiredGeneration || state.review.CompleteReceiptID != in.ExpectedCompleteReceiptID || state.review.CompleteReceiptHash != in.ExpectedCompleteReceiptHash {
		return plan, ErrConflict
	}
	// The proxy lock serializes competing keys across all actors.
	pending, err := sqlcgen.New(tx).ProxyRecoveryPlanPending(ctx, operation)
	if err != nil {
		return plan, err
	}
	if pending {
		return plan, ErrConflict
	}
	now, err := sqlcgen.New(tx).DatabaseClock(ctx)
	if err != nil {
		return plan, err
	}
	plan = domain.HostingProxyRecoveryPlan{ID: planID, Review: state.review, CreatedAt: now, ExpiresAt: now.Add(domain.HostingProxyRecoveryPlanLifetime)}
	plan.PlanHash, err = domain.HostingProxyRecoveryPlanHash(plan)
	if err != nil {
		return plan, ErrConflict
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		return plan, err
	}
	err = sqlcgen.New(tx).ProxyRecoveryCreatePlan(ctx, sqlcgen.ProxyRecoveryCreatePlanParams{ID: plan.ID, OperationID: operation, ActorID: actor, RequestKey: in.IdempotencyKey, RequestHash: domain.HostingDocumentHash(in), Body: raw, BodyHash: plan.PlanHash, CreatedAt: now, ExpiresAt: plan.ExpiresAt})
	if err != nil {
		return plan, hostingError(err)
	}
	audit.TargetID = operation
	audit.ActorID = actor
	audit.Action = "hosting.proxy_recovery_planned"
	audit.Metadata = map[string]any{"plan_id": plan.ID, "plan_hash": plan.PlanHash}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return plan, err
	}
	if err = proxyRecoveryFinal(ctx, tx, actor, plan.ExpiresAt); err != nil {
		return plan, err
	}
	return plan, tx.Commit(ctx)
}

func proxyRecoveryFinal(ctx context.Context, tx pgx.Tx, actor string, expiry time.Time) error {
	if err := proxyCheckLegacyToken(ctx, tx, actor); err != nil {
		return err
	}
	now, err := sqlcgen.New(tx).DatabaseClock(ctx)
	if err != nil {
		return err
	}
	if !now.Before(expiry) {
		return ErrConflict
	}
	return nil
}

// ApplyHostingProxyRecovery atomically repairs reviewed metadata and queues convergence.
func (s *PostgresStore) ApplyHostingProxyRecovery(ctx context.Context, operation, planID, resolutionID, actor string, in domain.HostingProxyRecoveryApplyInput, audit domain.AuditEvent) (domain.HostingProxyRecoveryResolution, error) {
	var resolution domain.HostingProxyRecoveryResolution
	if err := domain.ValidateHostingProxyRecoveryApplyInput(in); err != nil {
		return resolution, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return resolution, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = proxyRecoveryRead(ctx, tx, operation, actor); err != nil {
		return resolution, err
	}
	stored, err := sqlcgen.New(tx).ProxyRecoveryResolution(ctx, operation)
	if err == nil {
		if stored.ActorID != actor || stored.RequestKey != in.IdempotencyKey || stored.RequestHash != domain.HostingDocumentHash(in) {
			return resolution, ErrConflict
		}
		if err = json.Unmarshal(stored.Body, &resolution); err != nil {
			return resolution, err
		}
		if resolution.PlanID != planID {
			return resolution, ErrConflict
		}
		return resolution, proxyCheckLegacyToken(ctx, tx, actor)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return resolution, err
	}
	if s.hostingJobs == nil {
		return resolution, hostingjobs.ErrUnavailable
	}
	if err = tx.Rollback(ctx); err != nil {
		return resolution, err
	}
	tx, err = s.pool.Begin(ctx)
	if err != nil {
		return resolution, err
	}
	defer rollbackTransaction(ctx, tx)
	state, err := proxyRecoveryScope(ctx, tx, operation, actor)
	if err != nil {
		return resolution, err
	}
	raw, err := sqlcgen.New(tx).ProxyRecoveryPlanForApply(ctx, sqlcgen.ProxyRecoveryPlanForApplyParams{ID: planID, OperationID: operation, ActorID: actor})
	if err != nil {
		return resolution, hostingError(err)
	}
	var plan domain.HostingProxyRecoveryPlan
	if err = json.Unmarshal(raw, &plan); err != nil {
		return resolution, err
	}
	hash, err := domain.HostingProxyRecoveryPlanHash(plan)
	if err != nil || hash != plan.PlanHash || hash != in.PlanHash || !reflect.DeepEqual(plan.Review, state.review) {
		return resolution, ErrConflict
	}
	if err = proxyRecoveryFinal(ctx, tx, actor, plan.ExpiresAt); err != nil {
		return resolution, err
	}
	for _, r := range plan.Review.Routes {
		if r.SelectedReleaseOperationID == r.PriorTarget.ReleaseOperationID {
			continue
		}
		tag, e := sqlcgen.New(tx).ProxyRecoveryRestoreRoute(ctx, sqlcgen.ProxyRecoveryRestoreRouteParams{ID: r.RouteID, SelectedReleaseOperationID: r.PriorTarget.ReleaseOperationID, Version: r.Version, SelectedReleaseOperationID_2: r.SelectedReleaseOperationID})
		if e != nil {
			return resolution, e
		}
		if tag != 1 {
			return resolution, ErrConflict
		}
	}
	q := sqlcgen.New(tx)
	if _, err = q.ProxySetState(ctx, sqlcgen.ProxySetStateParams{ID: operation, State: state.review.EngineOutcome}); err != nil {
		return resolution, err
	}
	count, err := q.ProxySettle(ctx, sqlcgen.ProxySettleParams{ID: state.p.ID, ActiveOperationID: proxyPointer(operation), LoadedGeneration: state.p.LoadedGeneration})
	if err != nil {
		return resolution, err
	}
	if count != 1 {
		return resolution, ErrConflict
	}
	// Reuse withdrawal's exact loaded predecessor, never desired B metadata.
	p, err := proxyLoad(ctx, tx, state.p.ID)
	if err != nil {
		return resolution, err
	}
	convergenceID := "pop_recover_" + resolutionID
	queueAudit := audit
	queueAudit.ID = "aud_queue_" + resolutionID
	queued, err := s.proxyQueueRevocation(ctx, tx, p, &state.op, convergenceID, actor, "recovery:"+plan.ID, plan.PlanHash, queueAudit)
	if err != nil {
		return resolution, err
	}
	if !reflect.DeepEqual(proxySortedTargets(queued.Snapshot.Routes), proxySortedTargets(plan.Review.ConvergenceRoutes)) {
		return resolution, ErrConflict
	}
	now, err := q.DatabaseClock(ctx)
	if err != nil {
		return resolution, err
	}
	resolution = domain.HostingProxyRecoveryResolution{ID: resolutionID, OperationID: operation, PlanID: plan.ID, PlanHash: plan.PlanHash, ActorID: actor, EngineOutcome: state.review.EngineOutcome, CompleteReceiptID: state.review.CompleteReceiptID, CompleteReceiptHash: state.review.CompleteReceiptHash, ConvergenceOperationID: queued.ID, CreatedAt: now}
	for _, r := range plan.Review.Routes {
		afterVersion := r.Version
		if r.SelectedReleaseOperationID != r.PriorTarget.ReleaseOperationID {
			afterVersion++
		}
		resolution.Routes = append(resolution.Routes, domain.HostingProxyRecoveryChange{RouteID: r.RouteID, BeforeVersion: r.Version, AfterVersion: afterVersion, BeforeReleaseOperationID: r.SelectedReleaseOperationID, AfterReleaseOperationID: r.PriorTarget.ReleaseOperationID, Enabled: r.Enabled})
	}
	raw, err = json.Marshal(resolution)
	if err != nil {
		return resolution, err
	}
	err = q.ProxyRecoveryCreateResolution(ctx, sqlcgen.ProxyRecoveryCreateResolutionParams{ID: resolutionID, OperationID: operation, PlanID: plan.ID, ActorID: actor, PlanHash: plan.PlanHash, RequestKey: in.IdempotencyKey, RequestHash: domain.HostingDocumentHash(in), CompleteReceiptID: resolution.CompleteReceiptID, CompleteReceiptHash: resolution.CompleteReceiptHash, ConvergenceOperationID: queued.ID, Body: raw, BodyHash: domain.HostingDocumentHash(resolution), CreatedAt: now})
	if err != nil {
		return resolution, hostingError(err)
	}
	audit.TargetID = operation
	audit.ActorID = actor
	audit.Action = "hosting.proxy_metadata_recovered"
	audit.Metadata = map[string]any{"resolution_id": resolutionID, "plan_id": plan.ID, "plan_hash": plan.PlanHash, "convergence_operation_id": queued.ID}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return resolution, err
	}
	if err = proxyRecoveryFinal(ctx, tx, actor, plan.ExpiresAt); err != nil {
		return resolution, err
	}
	return resolution, tx.Commit(ctx)
}

var _ HostingProxyRecoveryRepository = (*PostgresStore)(nil)

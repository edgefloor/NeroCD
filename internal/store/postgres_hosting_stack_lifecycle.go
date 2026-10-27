package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

func readStackStopped(ctx context.Context, db sqlcgen.DBTX, binding string) (*domain.HostingStackStopped, error) {
	raw, err := sqlcgen.New(db).ReadHostingStackStopped(ctx, binding)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (len(raw) == 0 || string(raw) == "null") {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var stopped domain.HostingStackStopped
	if json.Unmarshal(raw, &stopped) != nil || stopped.Validate() != nil {
		return nil, ErrConflict
	}
	return &stopped, nil
}
func loadStackStopPlan(ctx context.Context, db sqlcgen.DBTX, op *domain.HostingOperation) error {
	raw, err := sqlcgen.New(db).ReadHostingStackStopPlan(ctx, op.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var plan domain.HostingStackStopPlan
	if json.Unmarshal(raw, &plan) != nil || plan.Validate() != nil || plan.Target.Snapshot != op.Snapshot || plan.Target.Snapshot.BindingID != op.BindingID {
		return ErrConflict
	}
	op.StackStopPlan = &plan
	return nil
}
func requireStackLifecycleAgent(ctx context.Context, tx pgx.Tx, runner string) error {
	ok, err := sqlcgen.New(tx).HostingRunnerHasReleasePorts(ctx, sqlcgen.HostingRunnerHasReleasePortsParams{RunnerID: runner, Capability: domain.HostingStackLifecycleCapability})
	if err != nil {
		return err
	}
	if !ok {
		return ErrConflict
	}
	return nil
}
func stackStopAuthority(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, allowStopped bool) error {
	if op.StackStopPlan == nil || op.StackStopPlan.Validate() != nil {
		return ErrConflict
	}
	if err := hostingOperationTargetGate(ctx, tx, op, false); err != nil {
		return err
	}
	if err := authorizeBuildActor(ctx, tx, op.CreatedBy, op.ProjectID, "maintainer"); err != nil {
		return err
	}
	if err := requireStackLifecycleAgent(ctx, tx, op.Snapshot.RunnerID); err != nil {
		return err
	}
	state, err := sqlcgen.New(tx).LockObservationSettledState(ctx, op.BindingID)
	if err != nil {
		return err
	}
	if !state.RestoreAdmitted || state.HoldOperationID != nil && *state.HoldOperationID != op.ID || !stackStopHealthyMatches(state.Healthy, op.StackStopPlan.Target) {
		return ErrConflict
	}
	stopped, err := readStackStopped(ctx, tx, op.BindingID)
	if err != nil {
		return err
	}
	if stopped != nil && (!allowStopped || stopped.StopOperationID != op.ID) {
		return ErrConflict
	}
	return nil
}

// HostingStackLifecycle exposes settled lifecycle evidence independently of desired options.
func (s *PostgresStore) HostingStackLifecycle(ctx context.Context, actor, resource string) (domain.HostingStackLifecycle, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingStackLifecycle, error) {
		out := domain.HostingStackLifecycle{ResourceID: resource, State: "unavailable"}
		q := sqlcgen.New(tx)
		r, err := q.ReadHostingRuntimeResource(ctx, resource)
		if err != nil {
			return out, err
		}
		out.ResourceGeneration = r.Generation
		if err = authorizeBuildActor(ctx, tx, actor, r.ProjectID, "viewer"); err != nil {
			return out, err
		}
		var source domain.ResourceSource
		if json.Unmarshal(r.Source, &source) != nil || source.Kind != "compose" {
			return out, nil
		}
		binding, err := q.RuntimeLogCurrentBinding(ctx, resource)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		state, err := q.LockObservationSettledState(ctx, binding.ID)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		if state.HoldOperationID != nil || !state.RestoreAdmitted {
			out.State = "held"
			return out, nil
		}
		stopped, err := readStackStopped(ctx, tx, binding.ID)
		if err != nil {
			return out, err
		}
		if stopped != nil {
			out.State = "stopped"
			out.StopOperationID = stopped.StopOperationID
			out.DeployedOperationID = stopped.Target.OperationID
			return out, nil
		}
		var healthy domain.HostingHealthy
		if len(state.Healthy) > 0 && json.Unmarshal(state.Healthy, &healthy) == nil && domain.ValidateHostingStackHealthy(healthy) == nil {
			out.State = "running"
			out.DeployedOperationID = healthy.OperationID
		}
		return out, nil
	})
}

// RequestHostingStackStop freezes the deployed receipt, never client Docker selectors.
func (s *PostgresStore) RequestHostingStackStop(ctx context.Context, id, resource, actor string, in domain.HostingStackStopInput, audit domain.AuditEvent) (domain.HostingOperation, error) {
	if in.Validate() != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	if s.hostingJobs == nil {
		return domain.HostingOperation{}, hostingjobs.ErrUnavailable
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingOperation, error) {
		var zero domain.HostingOperation
		q := sqlcgen.New(tx)
		if err := lockHostingResourceDestination(ctx, tx, resource); err != nil {
			return zero, err
		}
		r, err := q.LockHostingResourceTarget(ctx, resource)
		if err != nil {
			return zero, err
		}
		if err = authorizeBuildActor(ctx, tx, actor, r.ProjectID, "maintainer"); err != nil {
			return zero, err
		}
		old, err := q.GetHostingIdempotency(ctx, sqlcgen.GetHostingIdempotencyParams{ResourceID: resource, IdempotencyKey: in.IdempotencyKey})
		if err == nil {
			if old.RequestHash != domain.HostingDocumentHash(in) {
				return zero, ErrConflict
			}
			return hostingOperation(ctx, tx, old.ID)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if r.Generation != in.ExpectedGeneration {
			return zero, ErrConflict
		}
		var source domain.ResourceSource
		if json.Unmarshal(r.Source, &source) != nil || source.Kind != "compose" {
			return zero, ErrConflict
		}
		state, err := q.LockObservationSettledState(ctx, r.BindingID)
		if err != nil {
			return zero, err
		}
		if state.HoldOperationID != nil || !state.RestoreAdmitted {
			return zero, ErrConflict
		}
		busy, err := q.HostingBindingBusy(ctx, r.BindingID)
		if err != nil {
			return zero, err
		}
		if busy {
			return zero, ErrConflict
		}
		stopped, err := readStackStopped(ctx, tx, r.BindingID)
		if err != nil {
			return zero, err
		}
		if stopped != nil {
			return zero, ErrConflict
		}
		var target domain.HostingHealthy
		if json.Unmarshal(state.Healthy, &target) != nil || target.OperationID != in.ExpectedDeployedOperationID || domain.ValidateHostingStackHealthy(target) != nil {
			return zero, ErrConflict
		}
		deployed, err := hostingOperation(ctx, tx, target.OperationID)
		if err != nil {
			return zero, err
		}
		if deployed.Plan == nil || deployed.State != "succeeded" || deployed.MutationHold || deployed.ResourceID != resource {
			return zero, ErrConflict
		}
		plan, err := domain.CompileHostingStackStopPlan(target, *deployed.Plan)
		if err != nil {
			return zero, ErrConflict
		}
		probe := domain.HostingOperation{ID: id, ProjectID: r.ProjectID, ResourceID: resource, BindingID: r.BindingID, Snapshot: target.Snapshot, CreatedBy: actor, StackStopPlan: &plan}
		if err = stackStopAuthority(ctx, tx, probe, false); err != nil {
			return zero, err
		}
		if _, err = q.CreateHostingOperation(ctx, sqlcgen.CreateHostingOperationParams{ID: id, ProjectID: r.ProjectID, ResourceID: resource, BindingID: r.BindingID, IdempotencyKey: in.IdempotencyKey, RequestHash: domain.HostingDocumentHash(in), CreatedBy: actor}); err != nil {
			return zero, hostingError(err)
		}
		if _, err = q.CreateHostingSnapshot(ctx, sqlcgen.CreateHostingSnapshotParams{OperationID: id, Body: buildJSON(target.Snapshot), BodyHash: domain.HostingDocumentHash(target.Snapshot)}); err != nil {
			return zero, err
		}
		if err = q.CreateHostingStackStopPlan(ctx, sqlcgen.CreateHostingStackStopPlanParams{OperationID: id, TargetOperationID: target.OperationID, Plan: buildJSON(plan), PlanHash: plan.Hash}); err != nil {
			return zero, err
		}
		audit.ActorID = actor
		audit.Action = "hosting.stack_stop_requested"
		audit.TargetID = id
		audit.Metadata = map[string]any{"resource_id": resource, "deployed_operation_id": target.OperationID}
		if err = createAuditWithQueries(ctx, q, audit); err != nil {
			return zero, err
		}
		if _, err = s.hostingJobs.EnqueueTx(ctx, tx, hostingjobs.PrepareDeploymentArgs{OperationID: id}); err != nil {
			return zero, err
		}
		return hostingOperation(ctx, tx, id)
	})
}

func (s *PostgresStore) admitStackStop(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, op domain.HostingOperation) (domain.HostingOperation, error) {
	if err := hostingRequireMode(ctx, tx, a, domain.HostingStackStopMode); err != nil {
		return op, err
	}
	if err := stackStopAuthority(ctx, tx, op, false); err != nil {
		return op, err
	}
	q := sqlcgen.New(tx)
	if op.MutationHold && op.State == "applying" {
		valid, err := q.HostingApplyAdmissionMatches(ctx, sqlcgen.HostingApplyAdmissionMatchesParams{OperationID: op.ID, RunID: a.RunID, LeaseID: a.LeaseID, Authority: buildJSON(a), PlanHash: op.StackStopPlan.Hash})
		if err != nil {
			return op, err
		}
		if !valid {
			return op, ErrConflict
		}
		return op, nil
	}
	if op.State != "prepared" || op.MutationHold {
		return op, ErrConflict
	}
	n, err := q.AdmitHostingMutation(ctx, sqlcgen.AdmitHostingMutationParams{BindingID: op.BindingID, HoldOperationID: &op.ID, DaemonID: &op.StackStopPlan.Target.Release.DaemonID})
	if err != nil {
		return op, err
	}
	if n != 1 {
		return op, ErrConflict
	}
	if _, err = q.CreateHostingApplyAdmission(ctx, sqlcgen.CreateHostingApplyAdmissionParams{OperationID: op.ID, RunID: a.RunID, LeaseID: a.LeaseID, Authority: buildJSON(a), PlanHash: op.StackStopPlan.Hash}); err != nil {
		return op, err
	}
	if _, err = q.BeginHostingApply(ctx, op.ID); err != nil {
		return op, err
	}
	if err = hostingPhaseAudit(ctx, tx, q, op, runner, "stack_stop_admitted"); err != nil {
		return op, err
	}
	if err = q.EndHostingStackLogDemand(ctx, op.ResourceID); err != nil {
		return op, err
	}
	if err = hostingAuthorityExists(ctx, tx, runner, a, true); err != nil {
		return op, err
	}
	out, err := hostingTaskOperation(ctx, tx, a)
	if err != nil {
		return op, err
	}
	return out, tx.Commit(ctx)
}

func (s *PostgresStore) completeStackStop(ctx context.Context, tx pgx.Tx, runner string, in domain.HostingCompleteRequest, op domain.HostingOperation) (domain.HostingOperation, error) {
	if err := hostingRequireMode(ctx, tx, in.HostingAuthority, domain.HostingStackStopMode); err != nil {
		return op, err
	}
	if err := stackStopAuthority(ctx, tx, op, false); err != nil {
		return op, err
	}
	p := op.StackStopPlan
	r := in.Result
	if !op.MutationHold || op.State != "applying" && op.State != "cancel_requested" || r.PlanHash != p.Hash || r.DaemonID != p.Target.Release.DaemonID || r.Ready || r.Restored || r.ContainerID != "" || r.ImageID != "" || r.Stack == nil {
		return op, ErrConflict
	}
	q := sqlcgen.New(tx)
	valid, err := q.HostingApplyAdmissionMatches(ctx, sqlcgen.HostingApplyAdmissionMatchesParams{OperationID: op.ID, RunID: in.RunID, LeaseID: in.LeaseID, Authority: buildJSON(in.HostingAuthority), PlanHash: p.Hash})
	if err != nil {
		return op, err
	}
	if !valid {
		return op, ErrConflict
	}
	if r.Outcome == "succeeded" {
		if !r.StackStopped || r.Uncertain || domain.ValidateHostingStackStoppedState(*p, *r.Stack) != nil {
			return op, ErrConflict
		}
	} else if r.Outcome != "failed" || r.StackStopped || !r.Uncertain || domain.ValidateHostingStackState(p.TargetPlan, p.Target.OperationID, *r.Stack, false) != nil {
		return op, ErrConflict
	}
	if err = insertHostingReceipt(ctx, tx, in.HostingAuthority, "complete", in.ReceiptID, r); err != nil {
		return op, err
	}
	if err = hostingPhaseAudit(ctx, tx, q, op, runner, "stack_stop_completed"); err != nil {
		return op, err
	}
	state := "reconciliation_required"
	if r.Outcome == "succeeded" {
		state = "succeeded"
	}
	if _, err = q.SetHostingOperationState(ctx, sqlcgen.SetHostingOperationStateParams{ID: op.ID, State: state}); err != nil {
		return op, err
	}
	if _, err = q.CompleteHostingLease(ctx, sqlcgen.CompleteHostingLeaseParams{ID: in.LeaseID, Status: r.Outcome, CompletionKey: &in.ReceiptID}); err != nil {
		return op, err
	}
	if _, err = q.CompleteHostingTask(ctx, sqlcgen.CompleteHostingTaskParams{ID: op.RunID, Status: r.Outcome}); err != nil {
		return op, err
	}
	if r.Outcome == "succeeded" {
		if err = q.SetHostingStackStopped(ctx, sqlcgen.SetHostingStackStoppedParams{BindingID: op.BindingID, Stopped: buildJSON(domain.HostingStackStopped{StopOperationID: op.ID, Target: p.Target})}); err != nil {
			return op, err
		}
		if err = q.StopHostingStackReleaseWriters(ctx, p.Target.OperationID); err != nil {
			return op, err
		}
		if _, err = q.ClearHostingMutationHold(ctx, sqlcgen.ClearHostingMutationHoldParams{BindingID: op.BindingID, HoldOperationID: &op.ID}); err != nil {
			return op, err
		}
	}
	if err = hostingAuthorityExists(ctx, tx, runner, in.HostingAuthority, false); err != nil {
		return op, err
	}
	out, err := hostingTaskOperation(ctx, tx, in.HostingAuthority)
	if err != nil {
		return op, err
	}
	return out, tx.Commit(ctx)
}

func stackStopHealthyMatches(raw []byte, target domain.HostingHealthy) bool {
	var current domain.HostingHealthy
	return json.Unmarshal(raw, &current) == nil && domain.HostingDocumentHash(current) == domain.HostingDocumentHash(target)
}

func stackStartReference(ctx context.Context, tx pgx.Tx, binding string, expected string) (*domain.HostingStackStopped, error) {
	stopped, err := readStackStopped(ctx, tx, binding)
	if err != nil {
		return nil, err
	}
	if stopped == nil {
		if expected != "" {
			return nil, ErrConflict
		}
		return nil, nil
	}
	if stopped.StopOperationID != expected {
		return nil, ErrConflict
	}
	return stopped, nil
}

// Only route continuity may account for an intentionally stopped predecessor.
// Ordinary route qualification continues to reject nonrunning release records.
// This also serves repeatable-read options; mutation callers lock route resources.
func stoppedStackRouteTarget(ctx context.Context, tx pgx.Tx, p domain.HostingProxy, row sqlcgen.HostingProxyRoute) (domain.HostingProxyRouteTarget, error) {
	q := sqlcgen.New(tx)
	placement, err := q.ProxyResourceTarget(ctx, row.ResourceID)
	if err != nil {
		return domain.HostingProxyRouteTarget{}, hostingError(err)
	}
	if placement.ProjectID != row.ProjectID || placement.BindingID != row.BindingID || placement.DestinationID != p.DestinationID || placement.ServerID != p.ServerID || placement.RunnerID != p.RunnerID {
		return domain.HostingProxyRouteTarget{}, ErrConflict
	}
	grant, err := q.ProxyGetGrant(ctx, row.GrantID)
	if err != nil {
		return domain.HostingProxyRouteTarget{}, hostingError(err)
	}
	if grant.ProxyID != p.ID || grant.ProjectID != row.ProjectID || grant.RevokedAt != nil || !domain.HostingHostnameGranted(grant.Hostname, grant.Wildcard, row.Hostname) {
		return domain.HostingProxyRouteTarget{}, ErrConflict
	}
	stopped, err := readStackStopped(ctx, tx, row.BindingID)
	if err != nil || stopped == nil {
		return domain.HostingProxyRouteTarget{}, ErrConflict
	}
	release, err := sqlcgen.New(tx).ProxyReleaseTarget(ctx, row.SelectedReleaseOperationID)
	if err != nil {
		return domain.HostingProxyRouteTarget{}, err
	}
	var healthy domain.HostingHealthy
	if release.RetainedRunning || json.Unmarshal(release.Body, &healthy) != nil || domain.HostingDocumentHash(healthy) != domain.HostingDocumentHash(stopped.Target) || !proxyReleaseQualified(p, row, release.State, true, healthy) {
		return domain.HostingProxyRouteTarget{}, ErrConflict
	}
	return domain.HostingProxyRouteTarget{RouteID: row.ID, ProjectID: row.ProjectID, ResourceID: row.ResourceID, BindingID: row.BindingID, ReleaseOperationID: row.SelectedReleaseOperationID, Hostname: row.Hostname, PathPrefix: row.PathPrefix, HostPort: uint16(healthy.Snapshot.HostPort)}, nil
}

func (s *PostgresStore) recordStackStopObservation(ctx context.Context, tx pgx.Tx, runner string, in domain.HostingObservation, op domain.HostingOperation) (domain.HostingOperation, error) {
	if op.StackStopPlan == nil || in.Stack == nil || !in.StackStopped || !in.NoOwnedContainers || in.Ready || in.ContainerID != "" || in.ImageID != "" || in.PlanHash != op.StackStopPlan.Hash || in.ReleaseHash != op.StackStopPlan.Target.Release.Hash || in.DaemonID != op.StackStopPlan.Target.Release.DaemonID || domain.ValidateHostingStackStoppedState(*op.StackStopPlan, *in.Stack) != nil || !op.MutationHold || op.RestoreOwnershipHeld {
		return op, ErrConflict
	}
	if err := stackStopAuthority(ctx, tx, op, false); err != nil {
		return op, err
	}
	q := sqlcgen.New(tx)
	if _, err := q.CreateHostingReconciliationReceipt(ctx, sqlcgen.CreateHostingReconciliationReceiptParams{LeaseID: in.LeaseID, OperationID: op.ID, Body: buildJSON(in), BodyHash: domain.HostingDocumentHash(in)}); err != nil {
		return op, err
	}
	if _, err := q.SetHostingOperationState(ctx, sqlcgen.SetHostingOperationStateParams{ID: op.ID, State: "succeeded"}); err != nil {
		return op, err
	}
	if err := q.SetHostingStackStopped(ctx, sqlcgen.SetHostingStackStoppedParams{BindingID: op.BindingID, Stopped: buildJSON(domain.HostingStackStopped{StopOperationID: op.ID, Target: op.StackStopPlan.Target})}); err != nil {
		return op, err
	}
	if err := q.StopHostingStackReleaseWriters(ctx, op.StackStopPlan.Target.OperationID); err != nil {
		return op, err
	}
	if _, err := q.ClearHostingMutationHold(ctx, sqlcgen.ClearHostingMutationHoldParams{BindingID: op.BindingID, HoldOperationID: &op.ID}); err != nil {
		return op, err
	}
	if _, err := q.ClearHostingObservationRequest(ctx, op.ID); err != nil {
		return op, err
	}
	if _, err := q.CompleteHostingObservationLease(ctx, sqlcgen.CompleteHostingObservationLeaseParams{ID: in.LeaseID, CompletionKey: hostingStringPointer("observed_" + in.LeaseID)}); err != nil {
		return op, err
	}
	if _, err := q.CompleteHostingObservationTask(ctx, in.RunID); err != nil {
		return op, err
	}
	if err := hostingAuthorityExists(ctx, tx, runner, in.HostingAuthority, false); err != nil {
		return op, err
	}
	out, err := hostingTaskOperation(ctx, tx, in.HostingAuthority)
	if err != nil {
		return op, err
	}
	return out, tx.Commit(ctx)
}

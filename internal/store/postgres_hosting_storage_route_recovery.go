package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

// RecoverHostingStorageRouteContinuity retries only the proxy queue transaction.
// It cannot admit a writer, recreate a volume, or change deployment receipts.
func (s *PostgresStore) RecoverHostingStorageRouteContinuity(ctx context.Context, resource, operation, actor string, in domain.HostingStorageRouteRecoveryInput, audit domain.AuditEvent) (domain.HostingStorageRouteContinuityStatus, error) {
	var view domain.HostingStorageRouteContinuityStatus
	if in.Validate() != nil {
		return view, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return view, err
	}
	defer storageDeploymentRollback(ctx, tx)
	before, err := hostingOperation(ctx, tx, operation)
	if err != nil {
		return view, err
	}
	if before.ResourceID != resource || before.Snapshot.StorageRouteContinuity == (domain.HostingStorageRouteContinuityRef{}) {
		return view, ErrNotFound
	}
	// Match completion/cancellation ordering before destination, proxy and the
	// complete sorted resource set. A placement read is only a routing hint.
	if err = hostingLockOperationRuns(ctx, tx, before); err != nil {
		return view, err
	}
	q := sqlcgen.New(tx)
	receipt, err := q.GetHostingReceipt(ctx, sqlcgen.GetHostingReceiptParams{OperationID: operation, Phase: "complete"})
	if err != nil {
		return view, hostingError(err)
	}
	var authority domain.HostingAuthority
	if json.Unmarshal(receipt.Authority, &authority) != nil || authority.OperationID != operation || authority.RunID != before.RunID {
		return view, ErrConflict
	}
	if err = hostingAuthorityExists(ctx, tx, before.Snapshot.RunnerID, authority, false); err != nil {
		return view, err
	}
	if err = q.LockHostingExecutionDestination(ctx, before.Snapshot.DestinationID); err != nil {
		return view, err
	}
	placement, err := q.ProxyResourceTarget(ctx, resource)
	if err != nil {
		return view, hostingError(err)
	}
	if placement.ProjectID != before.ProjectID || placement.DestinationID != before.Snapshot.DestinationID {
		return view, ErrConflict
	}
	p, _, err := storageContinuityPrelock(ctx, tx, resource)
	if err != nil {
		return view, err
	}
	if _, err = q.LockHostingOperation(ctx, operation); err != nil {
		return view, hostingError(err)
	}
	op, err := hostingOperation(ctx, tx, operation)
	if err != nil {
		return view, err
	}
	if op.RunID != before.RunID || op.ReconciliationRunID != before.ReconciliationRunID || op.ResourceID != resource || op.ProjectID != placement.ProjectID {
		return view, ErrConflict
	}
	if err = proxyAuthorize(ctx, tx, actor, op.ProjectID, domain.RoleMaintainer, false); err != nil {
		return view, err
	}
	revoked, err := q.LockHostingProjectGrant(ctx, sqlcgen.LockHostingProjectGrantParams{DestinationID: op.Snapshot.DestinationID, ProjectID: op.ProjectID})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && revoked != nil {
		return view, auth.ErrForbidden
	}
	if err != nil {
		return view, err
	}
	c, state, err := readStorageContinuity(ctx, tx, op)
	if err != nil {
		return view, hostingError(err)
	}
	if c.ProxyID != p.ID || in.ExpectedDescriptorHash != op.Snapshot.StorageRouteContinuity.Hash || op.CompleteReceipt == nil || in.ExpectedCompleteReceiptID != op.CompleteReceipt.ID || in.ExpectedCompleteReceiptHash != op.CompleteReceipt.BodyHash || op.Result == nil || op.CompleteReceipt.BodyHash != domain.HostingDocumentHash(*op.Result) {
		return view, ErrConflict
	}
	successor, err := q.StorageDeploymentContinuitySuccessor(ctx, operation)
	if err != nil {
		return view, err
	}
	if successor != nil {
		// Matching replay reports linked historical status, including terminal
		// failure. It never resets a proxy result or requalifies another writer.
		if *successor != "pop_storage_"+op.ID || state != "proxy_pending" && state != "loaded" && state != "proxy_failed" {
			return view, ErrConflict
		}
		view, err = storageRouteRecoveryStatus(ctx, tx, op, c, state, *successor)
		if err != nil {
			return view, err
		}
		return view, proxyCheckLegacyToken(ctx, tx, actor)
	}
	if state != "unresolved" {
		return view, ErrConflict
	}
	if s.hostingJobs == nil {
		return view, hostingjobs.ErrUnavailable
	}
	if _, err = proxyOperation(ctx, tx, "pop_storage_"+op.ID); !errors.Is(err, ErrNotFound) {
		if err != nil {
			return view, err
		}
		return view, ErrConflict
	}
	if err = storageRouteRecoveryHealthy(ctx, tx, op, c, p, authority); err != nil {
		return view, err
	}
	if err = s.queueStorageContinuity(ctx, tx, op, "unresolved", actor, audit); err != nil {
		return view, err
	}
	// Queue and audit writes may wait. Preserve the pinned predecessor's
	// certificate freshness and actor expiry immediately before commit.
	prior, err := proxyOperation(ctx, tx, c.Request.ExpectedLoadedProxyOperationID)
	if err != nil {
		return view, err
	}
	now, err := q.DatabaseClock(ctx)
	if err != nil {
		return view, err
	}
	if err = storageContinuityTrustedRoutes(prior, c, now); err != nil {
		return view, err
	}
	view = domain.HostingStorageRouteContinuityStatus{ResourceID: resource, OperationID: operation, State: "proxy_pending", SuccessorProxyOperationID: "pop_storage_" + op.ID, Reason: "pending"}
	return view, proxyCommitMutation(ctx, tx, actor)
}

func storageRouteRecoveryHealthy(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, c domain.HostingStorageRouteContinuity, p domain.HostingProxy, authority domain.HostingAuthority) error {
	if op.State != "succeeded" || op.MutationHold || op.RestoreOwnershipHeld || op.ReconcileRequested || op.CleanupPlan != nil || op.Result == nil || op.Result.Outcome != "succeeded" || op.Result.Uncertain || op.Result.Restored || !op.Result.Ready || op.Release == nil || op.Plan == nil || op.Result.PlanHash != op.Plan.Hash || op.Result.DaemonID != c.DaemonID || op.Release.DaemonID != c.DaemonID || !domain.HostingPlanFamilyMatches(op.Snapshot, *op.Plan) {
		return ErrConflict
	}
	run, err := sqlcgen.New(tx).GetRun(ctx, op.RunID)
	if err != nil {
		return hostingError(err)
	}
	if run.Status != "succeeded" || run.FinishedAt == nil || int(run.NextAttempt) != authority.Attempt+1 {
		return ErrConflict
	}
	task, err := taskRunFromSQLC(run)
	if err != nil || domain.ValidateImageDeploymentTask(task) != nil || task.ProjectID != op.ProjectID || task.RunSpec.HostingMode != "deploy" || task.RunSpec.HostingOperationID != op.ID || proxyString(run.RunnerID) != op.Snapshot.RunnerID {
		return ErrConflict
	}
	if err = hostingRequireMode(ctx, tx, authority, "deploy"); err != nil {
		return err
	}
	lease, err := sqlcgen.New(tx).GetLeaseForCompletion(ctx, sqlcgen.GetLeaseForCompletionParams{LeaseID: authority.LeaseID, RunnerID: op.Snapshot.RunnerID, Attempt: int32(authority.Attempt), Fence: authority.Fence})
	if err != nil {
		return hostingError(err)
	}
	if lease.RunID != op.RunID || lease.Status != "succeeded" || lease.CompletedAt == nil || proxyString(lease.CompletionKey) != op.CompleteReceipt.ID {
		return ErrConflict
	}
	if err := hostingStorageMaterializationCompatible(ctx, sqlcgen.New(tx)); err != nil {
		return err
	}
	if err := hostingTargetGateMode(ctx, tx, op.ProjectID, op.Snapshot, false, false); err != nil {
		return err
	}
	if err := proxyRouteBindingAvailable(ctx, tx, p, sqlcgen.HostingProxyRoute{BindingID: op.BindingID, ProjectID: op.ProjectID}); err != nil {
		return err
	}
	foreign, err := sqlcgen.New(tx).StorageContinuityForeignPortReferenced(ctx, sqlcgen.StorageContinuityForeignPortReferencedParams{DestinationID: c.DestinationID, HostPort: int32(c.HostPort), ProxyID: c.ProxyID, ResourceID: c.ResourceID, BindingID: c.BindingID, PreviousOperationID: c.PreviousHealthy.OperationID, LoadedProxyOperationID: c.Request.ExpectedLoadedProxyOperationID, RouteIds: storageContinuityRouteIDs(c)})
	if err != nil {
		return err
	}
	if foreign {
		return ErrConflict
	}
	expected, err := storageRouteRecoveryTarget(ctx, tx, op)
	if err != nil {
		return err
	}
	state, err := sqlcgen.New(tx).StorageDeploymentBindingState(ctx, op.BindingID)
	if err != nil {
		return err
	}
	var healthy domain.HostingHealthy
	if state.DaemonID == nil || *state.DaemonID != c.DaemonID || json.Unmarshal(state.Healthy, &healthy) != nil || !reflect.DeepEqual(healthy, expected) {
		return ErrConflict
	}
	ledger, err := sqlcgen.New(tx).ProxyReleaseTarget(ctx, op.ID)
	if err != nil {
		return hostingError(err)
	}
	if ledger.State != "healthy" || !ledger.RetainedRunning || json.Unmarshal(ledger.Body, &healthy) != nil || !reflect.DeepEqual(healthy, expected) {
		return ErrConflict
	}
	return nil
}

func storageRouteRecoveryTarget(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) (domain.HostingHealthy, error) {
	expected := domain.HostingHealthy{Snapshot: op.Snapshot, OperationID: op.ID, Release: *op.Release, PlanHash: op.Plan.Hash}
	if op.Snapshot.Stack != (domain.HostingStackRef{}) {
		if op.Plan.Stack == nil || op.StackStopPlan != nil || op.Result.Stack == nil || op.Result.StackStopped || op.Result.ContainerID != "" || op.Result.ImageID != "" || op.Result.PreviousContainerID != "" {
			return expected, ErrConflict
		}
		definition, result, err := hostingSelectedStackDefinition(ctx, tx, op.ResourceID, op.ProjectID, op.Snapshot)
		if err != nil {
			return expected, err
		}
		if !reflect.DeepEqual(definition, op.Plan.Stack.Definition) {
			return expected, ErrConflict
		}
		plan, err := domain.CompileHostingStackPlan(op.Snapshot, *op.Release, op.Plan.PreviousHealthy, *op.Plan.Stack, op.Plan.PreviousStopped)
		if err != nil || !reflect.DeepEqual(plan, *op.Plan) || domain.ValidateHostingStackState(*op.Plan, op.ID, *op.Result.Stack, true) != nil {
			return expected, ErrConflict
		}
		if op.Plan.PreviousHealthy != nil && (op.Plan.PreviousHealthy.Stack == nil || op.Plan.PreviousHealthy.Stack.NetworkID != op.Result.Stack.NetworkID) {
			return expected, ErrConflict
		}
		if result != nil {
			for _, image := range op.Plan.Stack.Images {
				artifact, local := domain.HostingComposeBuildArtifact(*result, image.Component)
				if local {
					release, err := domain.NewHostingLocalBuildRelease(artifact)
					if err != nil || image.Release != release {
						return expected, ErrConflict
					}
				} else if image.Release.LocalBuild != (domain.HostingBuildArtifact{}) {
					return expected, ErrConflict
				}
			}
		}
		stopped, err := readStackStopped(ctx, tx, op.BindingID)
		if err != nil {
			return expected, err
		}
		if stopped != nil {
			return expected, ErrConflict
		}
		if err := validateHostingStackMaterialized(ctx, tx, op, *op.Result.Stack); err != nil {
			return expected, err
		}
		expected.Stack = op.Result.Stack
		if domain.ValidateHostingStackHealthy(expected) != nil {
			return expected, ErrConflict
		}
		return expected, nil
	}
	if op.Plan.Storage == nil || op.Result.ContainerID == "" || op.Result.ImageID != op.Release.ImageID {
		return expected, ErrConflict
	}
	storage, err := currentHostingStorage(ctx, tx, op.ProjectID, op.ResourceID)
	if err != nil {
		return expected, hostingError(err)
	}
	if storage.Reference() != op.Snapshot.Storage || storage.BindingID != op.BindingID || storage.DestinationID != op.Snapshot.DestinationID || storage.VolumeName != op.Plan.Storage.VolumeName || storage.MountPath != op.Plan.Storage.MountPath {
		return expected, ErrConflict
	}
	evidence, err := hostingStorageHealthyEvidence(ctx, tx, op)
	if err != nil {
		return expected, err
	}
	expected.ContainerID, expected.Storage = op.Result.ContainerID, evidence
	if domain.ValidateHostingHealthyStorage(expected) != nil {
		return expected, ErrConflict
	}
	return expected, nil
}

func storageRouteRecoveryStatus(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, c domain.HostingStorageRouteContinuity, state, successor string) (domain.HostingStorageRouteContinuityStatus, error) {
	view := domain.HostingStorageRouteContinuityStatus{ResourceID: op.ResourceID, OperationID: op.ID, State: state, SuccessorProxyOperationID: successor, Reason: domain.StorageContinuityStatusReason(state)}
	proxy, err := proxyOperation(ctx, tx, successor)
	if err != nil {
		return view, err
	}
	if proxy.ProxyID != c.ProxyID || proxy.ProjectID != op.ProjectID {
		return view, ErrConflict
	}
	if state == "loaded" {
		if proxy.CompleteReceipt != nil && proxy.Result != nil && proxy.CompleteReceipt.BodyHash == domain.HostingDocumentHash(*proxy.Result) && proxy.State == "applied" && proxy.Result.Outcome == "applied" && proxy.Result.LoadedGeneration == proxy.Generation {
			view.LoadedGeneration = &proxy.Generation
		} else {
			view.State, view.Reason = "unresolved", "reconciliation_required"
		}
	}
	return view, nil
}

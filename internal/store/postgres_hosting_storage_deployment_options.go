package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func storageDeploymentRollback(ctx context.Context, tx pgx.Tx) {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	rollbackTransaction(cleanup, tx)
}

func storageDeploymentGrant(ctx context.Context, tx pgx.Tx, destination, project string) error {
	grant, err := sqlcgen.New(tx).GetDestinationProjectGrant(ctx, sqlcgen.GetDestinationProjectGrantParams{DestinationID: destination, ProjectID: project})
	if errors.Is(err, pgx.ErrNoRows) || err == nil && grant.RevokedAt != nil {
		return auth.ErrForbidden
	}
	return err
}

func storageDeploymentOrdinaryReason(ctx context.Context, q *sqlcgen.Queries, destination, binding, previous string, port int) (string, error) {
	referenced, err := q.HostingReleasePortReferenced(ctx, sqlcgen.HostingReleasePortReferencedParams{DestinationID: destination, HostPort: int32(port)})
	if err != nil {
		return "", err
	}
	if referenced {
		return "foreign_port_reference", nil
	}
	reusable, err := q.HostingReleaseSlotReusable(ctx, sqlcgen.HostingReleaseSlotReusableParams{DestinationID: destination, BindingID: binding, PreviousOperationID: previous, HostPort: int32(port)})
	if err != nil {
		return "", err
	}
	if reusable == nil || !*reusable {
		return "busy", nil
	}
	return "available", nil
}

// HostingStorageDeploymentOptions qualifies current settings in one read-only
// snapshot. It never locks a row, reserves a port, or creates admission evidence.
func (s *PostgresStore) HostingStorageDeploymentOptions(ctx context.Context, resource, actor string) (domain.HostingStorageDeploymentOptions, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	view := domain.HostingStorageDeploymentOptions{ResourceID: resource, Routes: []domain.HostingRouteSwitchRoute{}}
	tx, err := s.switchReadTransaction(ctx)
	if err != nil {
		return view, err
	}
	defer storageDeploymentRollback(ctx, tx)
	project, err := switchReadScope(ctx, tx, resource, actor)
	if err != nil {
		return view, err
	}
	q := sqlcgen.New(tx)
	r, err := q.StorageDeploymentResource(ctx, resource)
	if err != nil {
		return view, hostingError(err)
	}
	view.ResourceGeneration = r.Generation
	finish := func(reason string) (domain.HostingStorageDeploymentOptions, error) {
		view.Reason = reason
		view.Available = reason == "available" || reason == "initial_deployment"
		if !view.Available {
			view.StorageRouteContinuity = nil
		}
		if err := proxyCheckLegacyToken(ctx, tx, actor); err != nil {
			return domain.HostingStorageDeploymentOptions{}, err
		}
		return view, tx.Commit(ctx)
	}
	var source domain.ResourceSource
	if json.Unmarshal(r.Source, &source) != nil || r.Kind != "application" || source.Kind != "image" {
		return finish("unsupported_source")
	}
	if _, err = domain.NormalizeHostingImage(source.Image.Reference); err != nil {
		return finish("unsupported_source")
	}
	placement, err := q.ProxyResourceTarget(ctx, resource)
	if errors.Is(err, pgx.ErrNoRows) {
		return finish("unbound")
	}
	if err != nil {
		return view, err
	}
	if err = storageDeploymentGrant(ctx, tx, placement.DestinationID, project); err != nil {
		return view, err
	}
	if err = hostingStorageMaterializationCompatible(ctx, q); err != nil {
		return view, err
	}
	saved, err := q.ReadHostingStorage(ctx, resource)
	if errors.Is(err, pgx.ErrNoRows) {
		return finish("storage_unconfigured")
	}
	if err != nil {
		return view, err
	}
	storage, err := hostingStorageFromSQL(saved)
	if err != nil || storage.ProjectID != project || storage.BindingID != placement.BindingID || storage.DestinationID != placement.DestinationID {
		return finish("stale_storage")
	}
	ref := storage.Reference()
	view.StorageRef = &ref
	runtime, err := q.StorageDeploymentRuntimeGeneration(ctx, resource)
	if err != nil {
		return view, err
	}
	if runtime > 0 {
		return finish("runtime_configuration_unsupported")
	}
	eligible, err := q.StorageDeploymentRunnerEligible(ctx, sqlcgen.StorageDeploymentRunnerEligibleParams{BindingID: placement.BindingID, ResourceID: resource, DestinationID: placement.DestinationID, ServerID: placement.ServerID, RunnerID: placement.RunnerID, ImageCapability: domain.RunTypeImageDeploy, StorageCapability: domain.HostingOwnedStorageCapability, ReleaseCapability: domain.HostingReleasePortsCapability})
	if err != nil {
		return view, err
	}
	if !eligible {
		return finish("runner_ineligible")
	}
	state, err := q.StorageDeploymentBindingState(ctx, placement.BindingID)
	missingState := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !missingState {
		return view, err
	}
	held, err := q.HostingBindingRestoreHeld(ctx, placement.BindingID)
	if err != nil {
		return view, err
	}
	if held || !missingState && !state.RestoreAdmitted {
		return finish("restore_hold")
	}
	busy, err := q.HostingBindingBusy(ctx, placement.BindingID)
	if err != nil {
		return view, err
	}
	if state.HoldOperationID != nil || busy {
		return finish("busy")
	}
	if missingState {
		// A binding-state row is first created by deployment admission. Any
		// durable history without that row leaves prior writer ownership unknown.
		history, readErr := q.StorageDeploymentInitialHistory(ctx, sqlcgen.StorageDeploymentInitialHistoryParams{ResourceID: resource, BindingID: placement.BindingID, StorageID: storage.ID})
		if readErr != nil {
			return view, readErr
		}
		if history == nil || *history {
			return finish("no_previous_healthy")
		}
		return finish("initial_deployment")
	}
	var healthy domain.HostingHealthy
	if len(state.Healthy) == 0 {
		return finish("no_previous_healthy")
	}
	if json.Unmarshal(state.Healthy, &healthy) != nil {
		return finish("stale_storage")
	}
	if healthy.OperationID == "" || healthy.ContainerID == "" || domain.ValidateHostingHealthyStorage(healthy) != nil || healthy.Storage == nil || healthy.Snapshot.Storage != ref || healthy.Snapshot.ResourceID != resource || healthy.Snapshot.BindingID != placement.BindingID || healthy.Snapshot.DestinationID != placement.DestinationID || healthy.Snapshot.ServerID != placement.ServerID || healthy.Snapshot.RunnerID != placement.RunnerID || domain.ValidateHostingSnapshotRelease(healthy.Snapshot, healthy.Release) != nil || state.DaemonID == nil || *state.DaemonID != healthy.Release.DaemonID {
		return finish("stale_storage")
	}
	ledger, err := q.ProxyReleaseTarget(ctx, healthy.OperationID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return finish("stale_storage")
		}
		return view, err
	}
	var recorded domain.HostingHealthy
	if ledger.State != "healthy" || !ledger.RetainedRunning || json.Unmarshal(ledger.Body, &recorded) != nil || domain.HostingDocumentHash(recorded) != domain.HostingDocumentHash(healthy) {
		return finish("stale_storage")
	}
	if healthy.Storage.Plan.VolumeName != storage.VolumeName || healthy.Storage.Plan.MountPath != storage.MountPath {
		return finish("stale_storage")
	}
	settings := domain.HostingDeploymentInput{ExpectedGeneration: r.Generation, IdempotencyKey: "read-only-options", HostPort: healthy.Snapshot.HostPort, ContainerPort: healthy.Snapshot.ContainerPort, Readiness: healthy.Snapshot.Readiness}
	if domain.ValidateHostingDeployment(settings) != nil {
		return finish("stale_storage")
	}
	view.PreviousHealthyOperationID = &healthy.OperationID
	view.HostPort = &settings.HostPort
	view.ContainerPort = &settings.ContainerPort
	view.Readiness = &settings.Readiness
	reusable, err := q.HostingReleaseSlotReusable(ctx, sqlcgen.HostingReleaseSlotReusableParams{DestinationID: placement.DestinationID, BindingID: placement.BindingID, PreviousOperationID: healthy.OperationID, HostPort: int32(settings.HostPort)})
	if err != nil {
		return view, err
	}
	if reusable == nil || !*reusable {
		return finish("busy")
	}
	proxyRow, err := q.ProxyGetByServer(ctx, placement.ServerID)
	if errors.Is(err, pgx.ErrNoRows) {
		reason, readErr := storageDeploymentOrdinaryReason(ctx, q, placement.DestinationID, placement.BindingID, healthy.OperationID, settings.HostPort)
		if readErr != nil {
			return view, readErr
		}
		return finish(reason)
	}
	if err != nil {
		return view, err
	}
	p, err := proxyLoad(ctx, tx, proxyRow.ID)
	if err != nil {
		return view, err
	}
	exists, err := storageContinuitySchema(ctx, tx)
	if err != nil {
		return view, err
	}
	if !exists {
		return view, ErrHostingStorageUnavailable
	}
	unsettled, err := q.StorageContinuityRouteState(ctx, sqlcgen.StorageContinuityRouteStateParams{ProxyID: p.ID, ResourceID: resource})
	if err != nil {
		return view, err
	}
	if unsettled != "" {
		return finish("continuity_unresolved")
	}
	ids, err := q.ProxySwitchReadEnabledRoutes(ctx, p.ID)
	if err != nil {
		return view, err
	}
	if len(ids) > domain.HostingProxyMaxRoutes {
		return finish("proxy_unavailable")
	}
	rows := make([]sqlcgen.HostingProxyRoute, 0, len(ids))
	selections := make([]domain.HostingRouteSwitchSelection, 0)
	for _, id := range ids {
		row, readErr := q.ProxyGetRoute(ctx, id)
		if readErr != nil {
			return view, readErr
		}
		rows = append(rows, row)
		if row.ResourceID == resource && row.ProjectID == project && row.BindingID == placement.BindingID {
			view.Routes = append(view.Routes, domain.HostingRouteSwitchRoute{ID: row.ID, Hostname: row.Hostname, PathPrefix: row.PathPrefix, Version: row.Version, SelectedReleaseOperationID: row.SelectedReleaseOperationID})
			selections = append(selections, domain.HostingRouteSwitchSelection{RouteID: row.ID, ExpectedVersion: row.Version, ExpectedSelectedReleaseOperationID: row.SelectedReleaseOperationID})
		}
	}
	if len(selections) == 0 {
		reason, readErr := storageDeploymentOrdinaryReason(ctx, q, placement.DestinationID, placement.BindingID, healthy.OperationID, settings.HostPort)
		if readErr != nil {
			return view, readErr
		}
		return finish(reason)
	}
	exclusive, err := q.StorageContinuityExclusive(ctx, p.ID)
	if err != nil {
		return view, err
	}
	if exclusive {
		return finish("busy")
	}
	continuityPending, err := q.StorageContinuityProxyPending(ctx, p.ID)
	if err != nil {
		return view, err
	}
	if continuityPending {
		return finish("busy")
	}
	if !p.Enabled || p.DestinationID != placement.DestinationID || p.RunnerID != placement.RunnerID || p.DaemonID != healthy.Release.DaemonID {
		return finish("proxy_unavailable")
	}
	if p.RestoreOwnershipHeld {
		return finish("restore_hold")
	}
	pending, err := q.ProxyPending(ctx, p.ID)
	if err != nil {
		return view, err
	}
	if pending || p.ActiveOperationID != "" || p.HoldOperationID != "" {
		return finish("busy")
	}
	if p.LoadedGeneration < 1 {
		return finish("no_loaded_generation")
	}
	prior, err := proxyLoadedPredecessor(ctx, tx, p)
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
		return finish("stale_routes")
	}
	if err != nil {
		return view, err
	}
	current := make([]domain.HostingProxyRouteTarget, 0, len(rows))
	for _, row := range rows {
		target, reason, readErr := switchReadRouteTarget(ctx, tx, p, row)
		if readErr != nil {
			if errors.Is(readErr, ErrConflict) || errors.Is(readErr, ErrNotFound) || errors.Is(readErr, ErrHostingRestoreHold) {
				if row.ResourceID != resource && (reason == "busy" || reason == "restore_hold") {
					reason = "busy"
				}
				return finish(reason)
			}
			return view, readErr
		}
		current = append(current, target)
	}
	if !reflect.DeepEqual(proxySortedTargets(current), proxySortedTargets(prior.Snapshot.Routes)) {
		return finish("stale_routes")
	}
	for _, selection := range selections {
		if selection.ExpectedSelectedReleaseOperationID != healthy.OperationID {
			return finish("stale_routes")
		}
	}
	input, err := domain.CanonicalHostingStorageRouteContinuityInput(domain.HostingStorageRouteContinuityInput{ExpectedLoadedGeneration: p.LoadedGeneration, ExpectedLoadedProxyOperationID: prior.ID, Routes: selections})
	if err != nil {
		return finish("stale_routes")
	}
	c := domain.HostingStorageRouteContinuity{Request: input}
	acceptedAt, err := q.DatabaseClock(ctx)
	if err != nil {
		return view, err
	}
	if storageContinuityTrustedRoutes(prior, c, acceptedAt) != nil {
		return finish("https_unavailable")
	}
	foreign, err := q.StorageContinuityForeignPortReferenced(ctx, sqlcgen.StorageContinuityForeignPortReferencedParams{DestinationID: placement.DestinationID, HostPort: int32(settings.HostPort), ProxyID: p.ID, ResourceID: resource, BindingID: placement.BindingID, PreviousOperationID: healthy.OperationID, LoadedProxyOperationID: prior.ID, RouteIds: storageContinuityRouteIDs(c)})
	if err != nil {
		return view, err
	}
	if foreign {
		return finish("foreign_port_reference")
	}
	view.StorageRouteContinuity = &input
	return finish("available")
}

// HostingStorageRouteContinuityStatus reports immutable historical settlement,
// never a substitute for the current route's HTTPS health or admission checks.
func (s *PostgresStore) HostingStorageRouteContinuityStatus(ctx context.Context, resource, actor, operation string) (domain.HostingStorageRouteContinuityStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var view domain.HostingStorageRouteContinuityStatus
	tx, err := s.switchReadTransaction(ctx)
	if err != nil {
		return view, err
	}
	defer storageDeploymentRollback(ctx, tx)
	project, err := switchReadScope(ctx, tx, resource, actor)
	if err != nil {
		return view, err
	}
	op, err := hostingOperation(ctx, tx, operation)
	if err != nil {
		return view, err
	}
	if op.ResourceID != resource || op.ProjectID != project || op.Snapshot.StorageRouteContinuity == (domain.HostingStorageRouteContinuityRef{}) {
		return view, ErrNotFound
	}
	if err = storageDeploymentGrant(ctx, tx, op.Snapshot.DestinationID, project); err != nil {
		return view, err
	}
	c, state, err := readStorageContinuity(ctx, tx, op)
	if err != nil {
		return view, hostingError(err)
	}
	successor, err := sqlcgen.New(tx).StorageDeploymentContinuitySuccessor(ctx, operation)
	if err != nil {
		return view, hostingError(err)
	}
	view = domain.HostingStorageRouteContinuityStatus{ResourceID: resource, OperationID: operation, State: state, SuccessorProxyOperationID: proxyString(successor), Reason: domain.StorageContinuityStatusReason(state)}
	if state == "loaded" && successor == nil {
		view.State = "unresolved"
		view.Reason = "reconciliation_required"
	}
	if state == "loaded" && successor != nil {
		proxy, readErr := proxyOperation(ctx, tx, *successor)
		if readErr != nil {
			return view, readErr
		}
		if proxy.ProxyID == c.ProxyID && proxy.CompleteReceipt != nil && proxy.Result != nil && proxy.CompleteReceipt.BodyHash == domain.HostingDocumentHash(*proxy.Result) && proxy.State == "applied" && proxy.Result.Outcome == "applied" && proxy.Result.LoadedGeneration == proxy.Generation {
			view.LoadedGeneration = &proxy.Generation
		} else {
			view.State = "unresolved"
			view.Reason = "reconciliation_required"
		}
	}
	if err = proxyCheckLegacyToken(ctx, tx, actor); err != nil {
		return domain.HostingStorageRouteContinuityStatus{}, err
	}
	return view, tx.Commit(ctx)
}

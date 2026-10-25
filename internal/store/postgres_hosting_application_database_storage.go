package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func loadApplicationDatabaseStorage(ctx context.Context, db sqlcgen.DBTX, op *domain.HostingOperation) error {
	q := sqlcgen.New(db)
	present, err := q.ApplicationDatabaseStoragePresent(ctx)
	if err != nil || !present {
		return err
	}
	raw, err := q.ReadApplicationDatabaseStorage(ctx, op.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var proof domain.HostingApplicationDatabaseStorage
	if json.Unmarshal(raw, &proof) != nil || proof.Validate(op.Snapshot) != nil {
		return ErrConflict
	}
	op.ApplicationDatabaseStorage = &proof
	raw, err = q.ReadApplicationDatabaseStorageWriter(ctx, op.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var request domain.HostingApplicationDatabaseStorageWriterRequest
	if json.Unmarshal(raw, &request) != nil || request.OperationID != op.ID || op.Plan == nil || request.Receipt.PlanHash != op.Plan.Hash || request.Receipt.StorageRef != proof.StorageRef {
		return ErrConflict
	}
	state := request.Receipt.State()
	stage, e := connectionStage(ctx, q, *op)
	if e != nil {
		return e
	}
	if stage != nil {
		state.StageHash = stage.Hash
		state.CandidateHash = stage.Candidate.Hash
	}
	recovery, e := storageRecoveryPlan(ctx, q, *op)
	if e != nil {
		return e
	}
	if recovery != nil {
		state.RecoveryAction = recovery.Action
		state.RecoveryState = "uncertain"
		progressRun := recovery.RunID
		if recovery.Action == "complete_replacement" && op.ApplicationDatabaseRouteContinuity != nil {
			proxy, e := q.ProxyOperation(ctx, "appdb_storage_proxy_"+op.ID)
			if e == nil && proxy.State == "applied" {
				progressRun = op.ApplicationDatabaseRouteContinuity.FinalizeRunID
			} else if e != nil && !errors.Is(e, pgx.ErrNoRows) {
				return e
			}
		}
		status, e := q.ReadApplicationDatabaseStorageTaskStatus(ctx, progressRun)
		if errors.Is(e, pgx.ErrNoRows) {
			state.RecoveryState = "queued"
		} else if e != nil {
			return e
		} else {
			switch status {
			case "queued":
				fresh, e := q.ApplicationDatabaseFinalizeClaimFresh(ctx, progressRun)
				if e != nil {
					return e
				}
				if fresh {
					state.RecoveryState = "queued"
				}
			case "running":
				live, e := q.ApplicationDatabaseStorageRecoveryLeaseLive(ctx, progressRun)
				if e != nil {
					return e
				}
				if live {
					state.RecoveryState = "running"
				}
			}
		}
		if recovery.Action == "resume_predecessor" && request.Receipt.Phase == "resumed" && !op.MutationHold && op.State == "failed" {
			state.RecoveryState = "succeeded"
		}
		if recovery.Action == "complete_replacement" && op.State == "succeeded" && !op.MutationHold && op.CompleteReceipt != nil && op.Result != nil && op.Result.Ready {
			state.RecoveryState = "succeeded"
		}
	} else if applicationStorageOwnerCurrent(ctx, q, op.CreatedBy, op.ProjectID) {
		for _, action := range []string{"resume_predecessor", "complete_replacement"} {
			copyOp := *op
			copyOp.ApplicationDatabaseStorageState = &state
			if applicationStorageRecoveryAllowed(ctx, db, copyOp, action) == nil {
				state.RecoveryActions = []string{action}
				break
			}
		}
	}

	op.ApplicationDatabaseStorageState = &state
	return nil
}

func applicationDatabaseStorageAgent(ctx context.Context, tx pgx.Tx, runner string) error {
	q := sqlcgen.New(tx)
	compatible, err := q.ApplicationDatabaseStorageCompatible(ctx)
	if err != nil || !compatible {
		return ErrHostingDatabaseConnectionUnavailable
	}
	eligible, err := q.ApplicationDatabaseStorageRunnerEligible(ctx, runner)
	if err != nil {
		return err
	}
	if !eligible {
		return ErrConflict
	}
	return requireConnectionAgent(ctx, tx, runner, false)
}

func admittedApplicationDatabaseStorage(ctx context.Context, tx pgx.Tx, application, project, binding, destination, runner string, in *domain.HostingApplicationDatabaseStorageInput) (*domain.HostingApplicationDatabaseStorage, error) {
	storage, err := currentHostingStorage(ctx, tx, project, application)
	if errors.Is(err, pgx.ErrNoRows) {
		if in != nil {
			return nil, ErrConflict
		}
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if in == nil || in.Validate() != nil || storage.BindingID != binding || storage.DestinationID != destination || in.ExpectedStorageGeneration != storage.Generation || in.ExpectedStorageHash != storage.Hash {
		return nil, ErrConflict
	}
	if err = applicationDatabaseStorageAgent(ctx, tx, runner); err != nil {
		return nil, err
	}
	out := domain.NewHostingApplicationDatabaseStorage(storage.Reference())
	return &out, nil
}

// Connected choices stay inside the same read-only snapshot as storage metadata.
// This read never grants writer or volume-creation authority.
func connectedStorageDeploymentOptions(ctx context.Context, tx pgx.Tx, view *domain.HostingStorageDeploymentOptions, storage domain.HostingStorage) (bool, string, error) {
	q := sqlcgen.New(tx)
	c, err := q.ReadDatabaseConnection(ctx, storage.ResourceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return true, "", err
	}
	if c.State != "active" || c.ProjectID != storage.ProjectID {
		return true, "busy", nil
	}
	binding, err := readDatabaseBinding(ctx, q, storage.ResourceID)
	if err != nil || binding.ID != c.BindingID || binding.ApplicationBindingID != storage.BindingID || binding.DestinationID != storage.DestinationID {
		return true, "stale_storage", nil
	}
	compatible, err := q.ApplicationDatabaseStorageCompatible(ctx)
	if err != nil || !compatible {
		return true, "runner_ineligible", nil
	}
	eligible, err := q.ApplicationDatabaseStorageRunnerEligible(ctx, binding.RunnerID)
	if err != nil {
		return true, "", err
	}
	if !eligible {
		return true, "runner_ineligible", nil
	}
	state, err := q.StorageDeploymentBindingState(ctx, storage.BindingID)
	if err != nil {
		return true, "no_previous_healthy", nil
	}
	held, err := q.HostingBindingRestoreHeld(ctx, storage.BindingID)
	if err != nil {
		return true, "", err
	}
	if held || !state.RestoreAdmitted {
		return true, "restore_hold", nil
	}
	busy, err := q.HostingBindingBusy(ctx, storage.BindingID)
	if err != nil {
		return true, "", err
	}
	if busy || state.HoldOperationID != nil {
		return true, "busy", nil
	}
	head, err := q.ReadDatabaseMembershipHead(ctx, binding.DatabaseBindingID)
	if err != nil || head.ConnectionID != c.ID || head.Revision < 1 {
		return true, "stale_storage", nil
	}
	var members []domain.HostingDatabaseNetworkMemberEvidence
	var healthy domain.HostingHealthy
	if json.Unmarshal(head.Members, &members) != nil || len(members) != 1 || json.Unmarshal(state.Healthy, &healthy) != nil || !networkCaptureDocumentEqual(healthy, members[0].ApplicationHealthy) || healthy.Snapshot.BindingID != storage.BindingID || healthy.Snapshot.RunnerID != binding.RunnerID || domain.ValidateHostingHealthyStorage(healthy) != nil || healthy.Storage != nil && healthy.Snapshot.Storage != storage.Reference() {
		return true, "stale_storage", nil
	}
	dbhold, err := q.ReadDatabaseNetworkHold(ctx, binding.DatabaseBindingID)
	if err == nil && dbhold != nil {
		return true, "busy", nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return true, "", err
	}
	view.PreviousHealthyOperationID = &healthy.OperationID
	port, container, ready := healthy.Snapshot.HostPort, healthy.Snapshot.ContainerPort, healthy.Snapshot.Readiness
	view.HostPort, view.ContainerPort, view.Readiness = &port, &container, &ready
	proxyRow, err := q.ProxyGetByServer(ctx, healthy.Snapshot.ServerID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return true, "", err
	}
	if err == nil {
		proxy, e := proxyLoad(ctx, tx, proxyRow.ID)
		if e != nil {
			return true, "", e
		}
		rows, e := q.ProxyListRoutes(ctx, proxy.ID)
		if e != nil {
			return true, "", e
		}
		own := 0
		for _, row := range rows {
			if row.Enabled && row.ResourceID == storage.ResourceID {
				own++
			}
		}
		if own == 0 {
			view.ApplicationDatabaseStorage = &domain.HostingApplicationDatabaseStorageReview{Version: domain.HostingApplicationDatabaseStorageVersion, Strategy: domain.HostingApplicationDatabaseStorageStrategy, ExpectedStorageGeneration: storage.Generation, ExpectedStorageHash: storage.Hash, AcknowledgeInterruptionRequired: true}
			return true, "available", nil
		}
		scope := make([]domain.HostingProxyRouteTarget, 0, len(rows))
		for _, row := range rows {
			if !row.Enabled {
				continue
			}
			if row.ResourceID == storage.ResourceID {
				view.Routes = append(view.Routes, domain.HostingRouteSwitchRoute{ID: row.ID, Hostname: row.Hostname, PathPrefix: row.PathPrefix, Version: row.Version, SelectedReleaseOperationID: row.SelectedReleaseOperationID})
			}
			target, reason, e := switchReadRouteTarget(ctx, tx, proxy, row)
			if e != nil {
				if errors.Is(e, ErrConflict) || errors.Is(e, ErrHostingRestoreHold) || errors.Is(e, ErrNotFound) {
					return true, reason, nil
				}
				return true, "", e
			}
			scope = append(scope, target)
		}
		if own > 0 {
			strict, e := q.ProxyVerifiedSwitchRunnerEligible(ctx, proxy.RunnerID)
			if e != nil {
				return true, "", e
			}
			if !strict {
				return true, "runner_ineligible", nil
			}
			if !proxy.Enabled || proxy.ActiveOperationID != "" || proxy.HoldOperationID != "" || proxy.RestoreOwnershipHeld {
				return true, "proxy_unavailable", nil
			}
			prior, e := proxyLoadedPredecessor(ctx, tx, proxy)
			if e != nil || !reflect.DeepEqual(proxySortedTargets(scope), proxySortedTargets(prior.Snapshot.Routes)) {
				return true, "stale_routes", nil
			}
		}
	}
	view.ApplicationDatabaseStorage = &domain.HostingApplicationDatabaseStorageReview{Version: domain.HostingApplicationDatabaseStorageVersion, Strategy: domain.HostingApplicationDatabaseStorageStrategy, ExpectedStorageGeneration: storage.Generation, ExpectedStorageHash: storage.Hash, AcknowledgeInterruptionRequired: true}
	return true, "available", nil
}

// This read suppresses impossible offers after original-owner revocation. It is
// never admission: transactional owner/placement/grant checks still gate effects.
func applicationStorageOwnerCurrent(ctx context.Context, q *sqlcgen.Queries, actor, project string) bool {
	current, err := q.ApplicationDatabaseStorageOwnerCurrent(ctx, sqlcgen.ApplicationDatabaseStorageOwnerCurrentParams{ActorID: actor, ProjectID: project})
	return err == nil && current
}

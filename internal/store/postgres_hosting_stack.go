package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func loadHostingStackSource(ctx context.Context, db sqlcgen.DBTX, op *domain.HostingOperation) error {
	if op.Snapshot.Stack == (domain.HostingStackRef{}) {
		return nil
	}
	definition, result, err := hostingSelectedStackDefinition(ctx, db, op.ResourceID, op.ProjectID, op.Snapshot)
	if err != nil {
		return err
	}
	op.StackSource = &definition
	op.ComposeResult = result
	return nil
}

// hostingSelectedStackDefinition verifies historical selected source proof. It is
// not a grant to execute: callers must retain their current actor/lease/placement
// gates. Git configuration is never converted into a pasted Compose resource.
func hostingSelectedStackDefinition(ctx context.Context, db sqlcgen.DBTX, resource, project string, snapshot domain.HostingSnapshot) (domain.HostingStackDefinition, *domain.HostingBuildComposeResult, error) {
	var definition domain.HostingStackDefinition
	if snapshot.ResourceID != resource || snapshot.Stack == (domain.HostingStackRef{}) {
		return definition, nil, ErrConflict
	}
	if snapshot.GitCompose != (domain.HostingGitComposeRef{}) {
		b, err := readBuild(ctx, sqlcgen.New(db), snapshot.GitCompose.BuildID)
		if err != nil {
			return definition, nil, err
		}
		if b.State != "succeeded" || b.ResourceID != resource || b.ProjectID != project || b.ComposeResult == nil || domain.ValidateHostingBuildComposeResult(*b.ComposeResult, b) != nil || b.ComposeResult.Hash != snapshot.GitCompose.ResultHash || b.Snapshot.Source.Generation != snapshot.GitCompose.SourceGeneration || domain.HostingDocumentHash(b.ComposeResult.Definition) != snapshot.Stack.Hash || b.Snapshot.ResourceGeneration != snapshot.ResourceGeneration || b.Snapshot.RunnerID != snapshot.RunnerID || b.Snapshot.BindingID != snapshot.BindingID || b.Snapshot.DestinationID != snapshot.DestinationID || b.Snapshot.ServerID != snapshot.ServerID {
			return definition, nil, ErrConflict
		}
		return b.ComposeResult.Definition, b.ComposeResult, nil
	}
	row, err := sqlcgen.New(db).GetHostingComposeSourceVersion(ctx, sqlcgen.GetHostingComposeSourceVersionParams{ResourceID: resource, Generation: snapshot.Stack.Generation})
	if err != nil {
		return definition, nil, hostingError(err)
	}
	if json.Unmarshal(row.Definition, &definition) != nil || row.DefinitionHash != snapshot.Stack.Hash || domain.HostingDocumentHash(definition) != row.DefinitionHash {
		return definition, nil, ErrConflict
	}
	return definition, nil, nil
}

func requireHostingStackAgent(ctx context.Context, tx pgx.Tx, snapshot domain.HostingSnapshot) error {
	if snapshot.Stack == (domain.HostingStackRef{}) {
		return nil
	}
	if snapshot.StackStoppedOperationID != "" {
		if err := requireStackLifecycleAgent(ctx, tx, snapshot.RunnerID); err != nil {
			return err
		}
	}
	capabilities := []string{domain.HostingStackCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability}
	if snapshot.GitCompose != (domain.HostingGitComposeRef{}) {
		capabilities = append(capabilities, domain.HostingGitComposeBuildCapability)
	}
	for _, capability := range capabilities {
		capable, err := sqlcgen.New(tx).HostingRunnerHasReleasePorts(ctx, sqlcgen.HostingRunnerHasReleasePortsParams{RunnerID: snapshot.RunnerID, Capability: capability})
		if err != nil {
			return err
		}
		if !capable {
			return ErrConflict
		}
	}
	return nil
}

func hostingFreshStackGate(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	if op.Snapshot.Stack == (domain.HostingStackRef{}) {
		return nil
	}
	if op.Snapshot.GitCompose != (domain.HostingGitComposeRef{}) {
		b, r, err := resolveCurrentHostingBuildResultTx(ctx, tx, op.CreatedBy, op.ResourceID, op.Snapshot.GitCompose.BuildID)
		if err != nil {
			return err
		}
		if b.ComposeResult == nil || r.Generation != op.Snapshot.ResourceGeneration || b.ComposeResult.Hash != op.Snapshot.GitCompose.ResultHash || b.Snapshot.Source.Generation != op.Snapshot.GitCompose.SourceGeneration || domain.HostingDocumentHash(b.ComposeResult.Definition) != op.Snapshot.Stack.Hash {
			return ErrConflict
		}
		return requireHostingStackAgent(ctx, tx, op.Snapshot)
	}
	row, err := sqlcgen.New(tx).LockHostingRuntimeResource(ctx, op.ResourceID)
	if err != nil {
		return err
	}
	if row.Generation != op.Snapshot.ResourceGeneration || row.ProjectID != op.ProjectID {
		return ErrConflict
	}
	var source domain.ResourceSource
	if json.Unmarshal(row.Source, &source) != nil || source.Kind != "compose" || source.Compose == nil {
		return ErrConflict
	}
	definition, err := domain.ParseHostingStack(*source.Compose)
	if err != nil || domain.HostingDocumentHash(definition) != op.Snapshot.Stack.Hash {
		return ErrConflict
	}
	if _, err := stackStartReference(ctx, tx, op.BindingID, op.Snapshot.StackStoppedOperationID); err != nil {
		return err
	}
	return requireHostingStackAgent(ctx, tx, op.Snapshot)
}

func stackVolumeFromSQL(row sqlcgen.HostingComposeVolume) domain.HostingStackVolume {
	return domain.HostingStackVolume{Name: row.Name, VolumeName: row.VolumeName, CreationOperationID: row.CreationOperationID, CreationNonce: row.CreationNonce, CreatedAt: row.CreatedAt}
}

func compileHostingStackPlan(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, in domain.HostingPreparedRequest, previous *domain.HostingHealthy) (domain.HostingApplyPlan, error) {
	if op.StackSource == nil || domain.ValidateHostingStackImages(*op.StackSource, in.Release.DaemonID, in.StackImages) != nil || in.Release.Stack.Hash != domain.HostingDocumentHash(in.StackImages) {
		return domain.HostingApplyPlan{}, ErrConflict
	}
	if op.ComposeResult != nil {
		for _, image := range in.StackImages {
			artifact, local := domain.HostingComposeBuildArtifact(*op.ComposeResult, image.Component)
			if local {
				expected, err := domain.NewHostingLocalBuildRelease(artifact)
				if err != nil || image.Release != expected {
					return domain.HostingApplyPlan{}, ErrConflict
				}
			} else if image.Release.LocalBuild != (domain.HostingBuildArtifact{}) {
				return domain.HostingApplyPlan{}, ErrConflict
			}
		}
	}
	stack := domain.HostingStackPlan{Definition: *op.StackSource, Images: in.StackImages, NetworkName: domain.HostingStackObjectID(op.BindingID, "network", "private"), Volumes: []domain.HostingStackVolume{}}
	for _, name := range op.StackSource.Volumes {
		row, err := sqlcgen.New(tx).GetHostingComposeVolume(ctx, sqlcgen.GetHostingComposeVolumeParams{BindingID: op.BindingID, Name: name})
		if errors.Is(err, pgx.ErrNoRows) {
			stack.Volumes = append(stack.Volumes, domain.HostingStackVolume{Name: name, VolumeName: domain.HostingStackObjectID(op.BindingID, "volume", name), CreationOperationID: op.ID, CreationNonce: domain.HostingStackObjectID(op.ID, "creation", name)[4:]})
			continue
		}
		if err != nil {
			return domain.HostingApplyPlan{}, err
		}
		if row.State != "materialized" || row.DaemonID != in.Release.DaemonID || row.DestinationID != op.Snapshot.DestinationID {
			return domain.HostingApplyPlan{}, ErrConflict
		}
		stack.Volumes = append(stack.Volumes, stackVolumeFromSQL(row))
	}
	stopped, err := readStackStopped(ctx, tx, op.BindingID)
	if err != nil {
		return domain.HostingApplyPlan{}, err
	}
	return domain.CompileHostingStackPlan(op.Snapshot, in.Release, previous, stack, stopped)
}

func admitHostingStackVolumes(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	if op.Plan == nil || op.Plan.Stack == nil {
		return ErrConflict
	}
	q := sqlcgen.New(tx)
	for _, volume := range op.Plan.Stack.Volumes {
		row, err := q.GetHostingComposeVolume(ctx, sqlcgen.GetHostingComposeVolumeParams{BindingID: op.BindingID, Name: volume.Name})
		if err == nil {
			if row.State != "materialized" || row.DaemonID != op.Release.DaemonID || row.DestinationID != op.Snapshot.DestinationID || stackVolumeFromSQL(row) != volume {
				return ErrConflict
			}
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if volume.CreatedAt != "" || volume.CreationOperationID != op.ID {
			return ErrConflict
		}
		if err = q.AdmitHostingComposeVolume(ctx, sqlcgen.AdmitHostingComposeVolumeParams{BindingID: op.BindingID, Name: volume.Name, VolumeName: volume.VolumeName, DestinationID: op.Snapshot.DestinationID, DaemonID: op.Release.DaemonID, CreationOperationID: volume.CreationOperationID, CreationPlanHash: op.Plan.Hash, CreationNonce: volume.CreationNonce}); err != nil {
			return err
		}
	}
	return nil
}

// HostingStackVolumes seals nonsecret Docker provenance under the exact live attempt.
func (s *PostgresStore) HostingStackVolumes(ctx context.Context, runner string, in domain.HostingStackVolumeRequest) (domain.HostingOperation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = hostingAuthorityExists(ctx, tx, runner, in.HostingAuthority, true); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingTaskOperation(ctx, tx, in.HostingAuthority)
	if err != nil {
		return op, err
	}
	if !op.MutationHold || op.State != "applying" || op.Plan == nil || op.Plan.Stack == nil || op.Plan.Hash != in.PlanHash || len(in.Volumes) != len(op.Plan.Stack.Volumes) {
		return domain.HostingOperation{}, ErrConflict
	}
	if err = hostingFreshRuntimeGate(ctx, tx, op); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingOperationTargetGate(ctx, tx, op, false); err != nil {
		return domain.HostingOperation{}, err
	}
	q := sqlcgen.New(tx)
	for i, volume := range in.Volumes {
		expected := op.Plan.Stack.Volumes[i]
		if expected.CreatedAt == "" {
			expected.CreatedAt = volume.CreatedAt
		}
		if volume != expected || len(volume.CreatedAt) > 128 {
			return domain.HostingOperation{}, ErrConflict
		}
		if _, err = time.Parse(time.RFC3339Nano, volume.CreatedAt); err != nil {
			return domain.HostingOperation{}, ErrConflict
		}
		row, err := q.GetHostingComposeVolume(ctx, sqlcgen.GetHostingComposeVolumeParams{BindingID: op.BindingID, Name: volume.Name})
		if err != nil {
			return domain.HostingOperation{}, err
		}
		actual := stackVolumeFromSQL(row)
		if row.State == "materialized" {
			if actual != volume {
				return domain.HostingOperation{}, ErrConflict
			}
			continue
		}
		actual.CreatedAt = volume.CreatedAt
		if row.State != "admitted" || actual != volume || row.CreationOperationID != op.ID || row.CreationPlanHash != op.Plan.Hash || row.DaemonID != op.Release.DaemonID {
			return domain.HostingOperation{}, ErrConflict
		}
		count, err := q.MaterializeHostingComposeVolume(ctx, sqlcgen.MaterializeHostingComposeVolumeParams{BindingID: op.BindingID, Name: volume.Name, CreatedAt: volume.CreatedAt})
		if err != nil {
			return domain.HostingOperation{}, err
		}
		if count != 1 {
			return domain.HostingOperation{}, ErrConflict
		}
	}
	if err = hostingAuthorityExists(ctx, tx, runner, in.HostingAuthority, true); err != nil {
		return domain.HostingOperation{}, err
	}
	return op, tx.Commit(ctx)
}

func validateHostingStackMaterialized(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, state domain.HostingStackState) error {
	for _, volume := range state.Volumes {
		row, err := sqlcgen.New(tx).GetHostingComposeVolume(ctx, sqlcgen.GetHostingComposeVolumeParams{BindingID: op.BindingID, Name: volume.Name})
		if err != nil {
			return err
		}
		if row.State != "materialized" || row.DaemonID != op.Release.DaemonID || row.DestinationID != op.Snapshot.DestinationID || stackVolumeFromSQL(row) != volume {
			return ErrConflict
		}
	}
	return nil
}

func (s *PostgresStore) completeHostingStack(ctx context.Context, tx pgx.Tx, runner string, in domain.HostingCompleteRequest, op domain.HostingOperation) (domain.HostingOperation, error) {
	result := in.Result
	if result.ContainerID != "" || result.ImageID != "" || result.PreviousContainerID != "" || result.Restored {
		return domain.HostingOperation{}, ErrConflict
	}
	if !op.MutationHold {
		if result.Outcome != "failed" || result.Uncertain || result.Ready || result.Stack != nil {
			return domain.HostingOperation{}, ErrConflict
		}
	} else {
		if err := hostingFreshRuntimeGate(ctx, tx, op); err != nil {
			return domain.HostingOperation{}, err
		}
		if err := hostingOperationTargetGate(ctx, tx, op, false); err != nil {
			return domain.HostingOperation{}, err
		}
		if op.State != "applying" && op.State != "verifying" && op.State != "cancel_requested" {
			return domain.HostingOperation{}, ErrConflict
		}
		if op.Plan == nil {
			return domain.HostingOperation{}, ErrConflict
		}
		if err := replayHostingApplyAdmission(ctx, tx, in.HostingAuthority, op); err != nil {
			return domain.HostingOperation{}, err
		}
		if op.Plan == nil || op.Plan.Stack == nil || op.Release == nil || result.PlanHash != op.Plan.Hash || result.DaemonID != op.Release.DaemonID {
			return domain.HostingOperation{}, ErrConflict
		}
		if result.Outcome == "succeeded" {
			if result.Uncertain || !result.Ready || result.Stack == nil || domain.ValidateHostingStackState(*op.Plan, op.ID, *result.Stack, true) != nil {
				return domain.HostingOperation{}, ErrConflict
			}
			if op.Plan.PreviousHealthy != nil && op.Plan.PreviousHealthy.Stack.NetworkID != result.Stack.NetworkID {
				return domain.HostingOperation{}, ErrConflict
			}
			if err := validateHostingStackMaterialized(ctx, tx, op, *result.Stack); err != nil {
				return domain.HostingOperation{}, err
			}
		} else if result.Outcome != "failed" || !result.Uncertain || result.Ready || result.Stack == nil || domain.ValidateHostingStackState(*op.Plan, op.ID, *result.Stack, false) != nil {
			return domain.HostingOperation{}, ErrConflict
		}
	}
	if err := insertHostingReceipt(ctx, tx, in.HostingAuthority, "complete", in.ReceiptID, result); err != nil {
		return domain.HostingOperation{}, err
	}
	state := result.Outcome
	if op.MutationHold && result.Uncertain {
		state = "reconciliation_required"
	}
	q := sqlcgen.New(tx)
	if _, err := q.SetHostingOperationState(ctx, sqlcgen.SetHostingOperationStateParams{ID: op.ID, State: state}); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err := q.CompleteHostingLease(ctx, sqlcgen.CompleteHostingLeaseParams{ID: in.LeaseID, Status: result.Outcome, CompletionKey: hostingStringPointer(in.ReceiptID)}); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err := q.CompleteHostingTask(ctx, sqlcgen.CompleteHostingTaskParams{ID: op.RunID, Status: result.Outcome}); err != nil {
		return domain.HostingOperation{}, err
	}
	if result.Outcome == "succeeded" {
		healthy := domain.HostingHealthy{Stack: result.Stack, Snapshot: op.Snapshot, OperationID: op.ID, Release: *op.Release, PlanHash: op.Plan.Hash}
		if domain.ValidateHostingStackHealthy(healthy) != nil {
			return domain.HostingOperation{}, ErrConflict
		}
		if op.Plan.PreviousHealthy != nil {
			if _, err := q.RetainPreviousHostingRelease(ctx, sqlcgen.RetainPreviousHostingReleaseParams{OperationID: op.Plan.PreviousHealthy.OperationID, RetainedRunning: false}); err != nil {
				return domain.HostingOperation{}, err
			}
		}
		raw := buildJSON(healthy)
		if _, err := q.RecordHostingHealthyRelease(ctx, sqlcgen.RecordHostingHealthyReleaseParams{OperationID: op.ID, BindingID: op.BindingID, DestinationID: op.Snapshot.DestinationID, HostPort: int32(op.Snapshot.HostPort), Body: raw}); err != nil {
			return domain.HostingOperation{}, err
		}
		if _, err := q.RetainHostingReleaseClaim(ctx, op.ID); err != nil {
			return domain.HostingOperation{}, err
		}
		if _, err := q.SetHostingHealthy(ctx, sqlcgen.SetHostingHealthyParams{BindingID: op.BindingID, Healthy: raw, HoldOperationID: hostingStringPointer(op.ID)}); err != nil {
			return domain.HostingOperation{}, err
		}
		if op.Plan.PreviousStopped != nil {
			n, err := q.ClearHostingStackStopped(ctx, sqlcgen.ClearHostingStackStoppedParams{BindingID: op.BindingID, Stopped: []byte(op.Plan.PreviousStopped.StopOperationID)})
			if err != nil {
				return op, err
			}
			if n != 1 {
				return op, ErrConflict
			}
		}
		if _, err := q.ClearHostingMutationHold(ctx, sqlcgen.ClearHostingMutationHoldParams{BindingID: op.BindingID, HoldOperationID: hostingStringPointer(op.ID)}); err != nil {
			return domain.HostingOperation{}, err
		}
	} else if !op.MutationHold {
		if _, err := q.ReleaseHostingFailedClaim(ctx, op.ID); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	if err := s.settleStorageContinuity(ctx, tx, op, result); err != nil {
		return domain.HostingOperation{}, err
	}
	if err := hostingPhaseAudit(ctx, tx, s.queries, op, runner, "completed"); err != nil {
		return domain.HostingOperation{}, err
	}
	if err := requireHostingStackAgent(ctx, tx, op.Snapshot); err != nil {
		return domain.HostingOperation{}, err
	}
	if err := runtimeHostingActorAuthority(ctx, tx, op); err != nil {
		return domain.HostingOperation{}, err
	}
	if err := hostingAuthorityExists(ctx, tx, runner, in.HostingAuthority, false); err != nil {
		return domain.HostingOperation{}, err
	}
	completed, err := hostingOperation(ctx, tx, op.ID)
	if err != nil {
		return completed, err
	}
	completed.TaskMode = "deploy"
	return completed, tx.Commit(ctx)
}

// stackDeploymentOptions uses the existing read-only route continuity proof.
func stackDeploymentOptions(ctx context.Context, tx pgx.Tx, resource, project string, source domain.ResourceSource, view *domain.HostingStorageDeploymentOptions, finish func(string) (domain.HostingStorageDeploymentOptions, error)) (domain.HostingStorageDeploymentOptions, error) {
	if source.Compose == nil {
		return finish("unsupported_source")
	}
	definition, err := domain.ParseHostingStack(*source.Compose)
	if err != nil {
		return finish("unsupported_source")
	}
	ref := domain.HostingStackRef{Generation: view.ResourceGeneration, Hash: domain.HostingDocumentHash(definition)}
	view.StackRef = &ref
	q := sqlcgen.New(tx)
	placement, err := q.ProxyResourceTarget(ctx, resource)
	if errors.Is(err, pgx.ErrNoRows) {
		return finish("unbound")
	}
	if err != nil {
		return *view, err
	}
	if err = storageDeploymentGrant(ctx, tx, placement.DestinationID, project); err != nil {
		return *view, err
	}
	eligible, err := q.StorageDeploymentRunnerEligible(ctx, sqlcgen.StorageDeploymentRunnerEligibleParams{BindingID: placement.BindingID, ResourceID: resource, DestinationID: placement.DestinationID, ServerID: placement.ServerID, RunnerID: placement.RunnerID, ImageCapability: domain.RunTypeImageDeploy, StorageCapability: domain.HostingStackCapability, ReleaseCapability: domain.HostingReleasePortsCapability})
	if err != nil {
		return *view, err
	}
	env, err := q.HostingRunnerHasReleasePorts(ctx, sqlcgen.HostingRunnerHasReleasePortsParams{RunnerID: placement.RunnerID, Capability: domain.HostingRuntimeEnvironmentCapability})
	if err != nil {
		return *view, err
	}
	if !eligible || !env {
		return finish("runner_ineligible")
	}
	state, err := q.StorageDeploymentBindingState(ctx, placement.BindingID)
	missing := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !missing {
		return *view, err
	}
	held, err := q.HostingBindingRestoreHeld(ctx, placement.BindingID)
	if err != nil {
		return *view, err
	}
	if held || !missing && !state.RestoreAdmitted {
		return finish("restore_hold")
	}
	busy, err := q.HostingBindingBusy(ctx, placement.BindingID)
	if err != nil {
		return *view, err
	}
	if busy || state.HoldOperationID != nil {
		return finish("busy")
	}
	if missing || len(state.Healthy) == 0 {
		history, readErr := q.HostingStackMutationHistory(ctx, placement.BindingID)
		if readErr != nil {
			return *view, readErr
		}
		if history == nil || *history {
			return finish("no_previous_healthy")
		}
		return finish("initial_deployment")
	}
	var healthy domain.HostingHealthy
	if json.Unmarshal(state.Healthy, &healthy) != nil || domain.ValidateHostingStackHealthy(healthy) != nil || healthy.Snapshot.ResourceID != resource || healthy.Snapshot.BindingID != placement.BindingID || healthy.Snapshot.DestinationID != placement.DestinationID || healthy.Snapshot.ServerID != placement.ServerID || healthy.Snapshot.RunnerID != placement.RunnerID || healthy.Snapshot.ContainerPort != definition.ContainerPort || state.DaemonID == nil || *state.DaemonID != healthy.Release.DaemonID {
		return finish("stale_storage")
	}
	stopped, err := readStackStopped(ctx, tx, placement.BindingID)
	if err != nil {
		return *view, err
	}
	if stopped != nil {
		if domain.HostingDocumentHash(stopped.Target) != domain.HostingDocumentHash(healthy) {
			return finish("stale_storage")
		}
		view.StoppedOperationID = &stopped.StopOperationID
	}
	ledger, err := q.ProxyReleaseTarget(ctx, healthy.OperationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return finish("stale_storage")
	}
	if err != nil {
		return *view, err
	}
	var recorded domain.HostingHealthy
	if ledger.State != "healthy" || !ledger.RetainedRunning && stopped == nil || ledger.RetainedRunning && stopped != nil || json.Unmarshal(ledger.Body, &recorded) != nil || domain.HostingDocumentHash(recorded) != domain.HostingDocumentHash(healthy) {
		return finish("stale_storage")
	}
	settings := domain.HostingDeploymentInput{ExpectedGeneration: view.ResourceGeneration, IdempotencyKey: "read-only-options", HostPort: healthy.Snapshot.HostPort, ContainerPort: healthy.Snapshot.ContainerPort, Readiness: healthy.Snapshot.Readiness}
	if domain.ValidateHostingDeployment(settings) != nil {
		return finish("stale_storage")
	}
	view.PreviousHealthyOperationID = &healthy.OperationID
	view.HostPort = &settings.HostPort
	view.ContainerPort = &settings.ContainerPort
	view.Readiness = &settings.Readiness
	reusable, err := q.HostingReleaseSlotReusable(ctx, sqlcgen.HostingReleaseSlotReusableParams{DestinationID: placement.DestinationID, BindingID: placement.BindingID, PreviousOperationID: healthy.OperationID, HostPort: int32(settings.HostPort)})
	if err != nil {
		return *view, err
	}
	if reusable == nil || !*reusable {
		return finish("busy")
	}
	return storageDeploymentRouteOptions(ctx, tx, resource, project, placement, healthy, settings, view, finish)
}

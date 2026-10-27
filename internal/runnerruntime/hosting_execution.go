package runnerruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

// errSettledImagePreparationFailure is issued only after an exact, durable failed completion.
var errSettledImagePreparationFailure = errors.New("image preparation failed; previous workload was not changed")

func hostingAuthority(runID, operationID string, lease domain.RunLease) domain.HostingAuthority {
	return domain.HostingAuthority{OperationID: operationID, RunID: runID, LeaseID: lease.ID, Attempt: lease.Attempt, Fence: lease.Fence}
}
func validateHostingPlanResponse(op domain.HostingOperation, a domain.HostingAuthority, runnerID, mode string) error {
	if op.ApplicationDatabase != nil || op.DatabaseMaintenance != nil || op.Snapshot.ApplicationDatabase != (domain.HostingApplicationDatabaseRef{}) || op.StorageArchive != nil || runner.HostingJournalUsesDatabase(runner.JournalHosting{Plan: op.Plan, CleanupPlan: op.CleanupPlan}) || op.Snapshot.Database != (domain.HostingDatabaseExecutionRef{}) || op.ID != a.OperationID || op.Snapshot.RunnerID != runnerID || op.Snapshot.BindingID != op.BindingID || op.TaskMode != mode {
		return errors.New("hosting response does not match the exact task target")
	}
	if (mode == "deploy" || mode == "cleanup" || mode == domain.HostingStackStopMode) && op.RunID != a.RunID {
		return errors.New("hosting deployment run changed")
	}
	if mode == "observe" && op.ReconciliationRunID != a.RunID {
		return errors.New("hosting observation run changed")
	}
	if op.Plan != nil {
		if verifyImagePlan(*op.Plan) != nil || op.Plan.Snapshot != op.Snapshot {
			return errors.New("hosting plan does not match immutable snapshot")
		}
	}
	if op.Release != nil && domain.ValidateHostingSnapshotRelease(op.Snapshot, *op.Release) != nil {
		return errors.New("hosting release does not match immutable source")
	}
	return nil
}

func executeImageClaim(supervisor *attemptSupervisor, journal *runner.AttemptJournal, reporter *attemptReporter, storageDrain func(context.Context) error, server, token string, claim domain.ClaimedRun, endpoint string, localEnabled bool, environmentConfig *RuntimeEnvironmentConfig, registryConfigs ...*RegistryPullConfig) error {
	mode := claim.Run.RunSpec.HostingMode
	if claim.Run.RunSpec.HostingOperationID == "" || (mode != "deploy" && mode != "observe" && mode != "cleanup" && mode != domain.HostingStackStopMode) || claim.PrimitivePlan.Process != nil || claim.PrimitivePlan.Checkout != nil || len(claim.PrimitivePlan.Secrets) != 0 || len(claim.PrimitivePlan.Artifacts) != 0 || claim.PrimitivePlan.RunID != claim.Run.ID || claim.Lease.RunID != claim.Run.ID {
		return errors.New("invalid typed image task")
	}
	a := hostingAuthority(claim.Run.ID, claim.Run.RunSpec.HostingOperationID, claim.Lease)
	client := runnerclient.New(server, token)
	var op domain.HostingOperation
	if err := retryAttemptRequest(supervisor, func(ctx context.Context) error { var err error; op, err = client.HostingPlan(ctx, a); return err }); err != nil {
		return err
	}
	if err := validateHostingPlanResponse(op, a, claim.Lease.RunnerID, mode); err != nil {
		return err
	}
	if mode != "observe" && storageArchiveConflict(journal.Snapshot(), op.Snapshot.Storage, op.BindingID, op.ID, "") {
		return errStorageArchiveHeld
	}
	local := hostingOperationUsesLocalBuild(op)
	if local && !localEnabled || claim.Run.RunSpec.Type == domain.RunTypeLocalBuildDeploy && !local {
		return errors.New("local artifact operation requires exact configured capability and source")
	}
	if mode == "deploy" && op.Snapshot.RuntimeConfiguration != (domain.HostingRuntimeConfigurationRef{}) && environmentConfig == nil {
		return errors.New("runtime environment requires explicit configuration")
	}
	engine, err := newImageEngine(endpoint)
	if err != nil {
		return err
	}
	defer func() { _ = engine.Close() }()
	if mode == domain.HostingStackStopMode {
		return executeHostingStackStop(supervisor, journal, reporter, client, engine, a, claim.Lease, op)
	}
	if mode == "cleanup" {
		return executeHostingCleanup(supervisor, journal, reporter, client, engine, a, claim.Lease, op)
	}
	if mode == "observe" {
		return executeHostingObservation(supervisor, client, engine, a, op)
	}
	message := "Preparing public container image"
	if local {
		message = "Verifying exact local build image"
	}
	if err = reporter.Emit("system", message, 4); err != nil {
		return err
	}
	failBeforeApply := func() error {
		drain := reporter.WaitEmpty
		if op.Snapshot.Storage != (domain.HostingStorageRef{}) {
			drain = storageDrain
		}
		result := domain.HostingResult{Outcome: "failed"}
		validate := func(finished domain.HostingOperation) error {
			if supervisor.Context().Err() != nil {
				return errLeaseAuthorityLost
			}
			if finished.ID != a.OperationID || finished.RunID != a.RunID || finished.BindingID != op.BindingID || finished.Snapshot != op.Snapshot || finished.State != "failed" || finished.Result == nil || *finished.Result != result || finished.MutationHold || finished.RestoreOwnershipHeld {
				return errors.New("image preparation failure acknowledgement changed")
			}
			return nil
		}
		_, err := journalHostingResultValidated(supervisor, journal, client, a, claim.Lease, result, drain, local, op.Snapshot.RuntimeConfiguration, validate, op.Snapshot.Storage)
		if err == nil && supervisor.Context().Err() != nil {
			return errLeaseAuthorityLost
		}
		return err
	}
	if op.Release == nil {
		initialSnapshot := op.Snapshot
		var release domain.HostingRelease
		var images []domain.HostingStackImage
		var prepareErr error
		var deliver stackRegistryDelivery
		if op.Snapshot.RegistryCredential != (domain.HostingRegistryCredentialRef{}) {
			if len(registryConfigs) != 1 || registryConfigs[0] == nil {
				return errors.New("registry pull requires explicit configuration")
			}
			deliver = func(ctx context.Context, component string) (*domain.HostingRegistryCredentialDelivery, error) {
				deliveryID := registryPullDeliveryID(a, component)
				return client.HostingRegistryCredential(ctx, claim.Lease.RunnerID, domain.HostingRegistryCredentialRequest{Authority: a, DeliveryID: deliveryID}, runnerclient.RuntimeEnvironmentTransport{TrustedLoopbackHTTP: registryConfigs[0].TrustedLoopbackHTTP})
			}
		}
		switch {
		case op.StackSource != nil:
			release, images, prepareErr = engine.prepareStack(supervisor.Context(), *op.StackSource, deliver)
		case deliver != nil:
			var material *domain.HostingRegistryCredentialDelivery
			material, prepareErr = deliver(supervisor.Context(), "")
			if prepareErr == nil {
				release, prepareErr = engine.prepareRegistryImage(supervisor.Context(), op.Snapshot, material)
				material.Close()
			}
		default:
			release, prepareErr = engine.Prepare(supervisor.Context(), op.Snapshot)
		}
		if prepareErr != nil {
			if supervisor.Context().Err() != nil {
				return prepareErr
			}
			if err = failBeforeApply(); err != nil {
				return err
			}
			return errSettledImagePreparationFailure
		}
		request := domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: attemptMutationKey("hosting_prepared", a.LeaseID, a.Attempt, "release"), Release: release, StackImages: images}
		entry := runner.JournalHosting{ID: request.ReceiptID, Attempt: journalAttemptIdentity(a.RunID, claim.Lease, supervisor), OperationID: a.OperationID, Phase: "prepared", Prepared: &request, CreatedAt: time.Now().UTC()}
		markHostingRuntimeEntry(&entry, initialSnapshot.RuntimeConfiguration)
		entry.StorageRef = initialSnapshot.Storage
		if _, err = journal.AppendHosting(entry); err != nil {
			return err
		}
		if err = retryAttemptRequest(supervisor, func(ctx context.Context) error {
			var err error
			op, err = client.HostingPrepared(ctx, request)
			return err
		}); err != nil {
			return err
		}
		if op.PreparedReceipt == nil || op.PreparedReceipt.ID != request.ReceiptID || op.PreparedReceipt.BodyHash != domain.HostingDocumentHash(release) || op.Release == nil || *op.Release != release {
			return errors.New("hosting preparation acknowledgement changed")
		}
		if validateHostingPreparedResponse(op, a, initialSnapshot, release) != nil {
			return errors.New("hosting preparation snapshot changed")
		}
		if err = journal.AckHosting(entry.ID); err != nil {
			return err
		}
	}
	if op.Plan == nil || op.Release == nil || verifyImagePlan(*op.Plan) != nil {
		return errors.New("committed image apply plan missing")
	}
	if op.Plan.Stack != nil {
		err = engine.prepareCompose(supervisor.Context())
		if err == nil {
			err = engine.ensureStackImages(supervisor.Context(), *op.Plan)
		}
	} else {
		err = engine.EnsurePreparedRelease(supervisor.Context(), *op.Release)
	}
	if err != nil {
		if supervisor.Context().Err() != nil {
			return err
		}
		if err = failBeforeApply(); err != nil {
			return err
		}
		if local {
			return errors.New("local build image unavailable; previous workload was not changed")
		}
		return nil
	}
	if err = reporter.Emit("system", "Prepared immutable image release", 5); err != nil {
		return err
	}
	if op.Snapshot.Storage != (domain.HostingStorageRef{}) {
		return executeHostingStoragePrepared(supervisor, journal, storageDrain, client, engine, a, claim.Lease, op, environmentConfig)
	}
	intentID := attemptMutationKey("hosting_intent", a.LeaseID, a.Attempt, "apply")
	intent := runner.JournalHosting{ID: intentID, Attempt: journalAttemptIdentity(a.RunID, claim.Lease, supervisor), OperationID: a.OperationID, Phase: "apply_intent", Plan: op.Plan, CreatedAt: time.Now().UTC()}
	markHostingRuntimeEntry(&intent, op.Snapshot.RuntimeConfiguration)
	intent.RuntimeEnvironment = runner.HostingJournalUsesRuntimeEnvironment(intent)
	if op.Snapshot.RuntimeConfiguration != (domain.HostingRuntimeConfigurationRef{}) {
		intent.DeliveryID, err = newRuntimeDeliveryID()
		if err != nil {
			return err
		}
	}
	// The durable local intent precedes admission. A lost admission response can
	// never cause this attempt to silently forget possible external effects.
	if _, err = journal.AppendHosting(intent); err != nil {
		return err
	}
	deliveryDeadline := runtimeDeliveryDeadline(supervisor)
	var environment *hostingruntimeprotocol.Environment
	if intent.DeliveryID != "" {
		environment, err = fetchHostingRuntimeEnvironment(supervisor, client, claim.Lease.RunnerID, domain.HostingRuntimeEnvironmentRequest{Authority: a, PlanHash: op.Plan.Hash, RuntimeConfiguration: op.Snapshot.RuntimeConfiguration, DeliveryID: intent.DeliveryID}, *environmentConfig, deliveryDeadline)
		if err != nil {
			return err
		}
		defer environment.Close()
		if op.Plan.Stack != nil {
			metadata, metadataErr := environment.Metadata()
			if metadataErr != nil || metadata.Authority != a || metadata.PlanHash != op.Plan.Hash || metadata.RuntimeConfiguration != op.Snapshot.RuntimeConfiguration {
				return errors.New("stack protected environment target changed")
			}
			if metadata.UseUntil.Before(deliveryDeadline) {
				deliveryDeadline = metadata.UseUntil
			}
		}
	}
	var stackBody []byte
	if op.Plan.Stack != nil {
		stackBody, err = renderStack(op.ID, *op.Plan, environment)
		if err != nil {
			if emitErr := reporter.Emit("system", "Compose preflight: "+err.Error(), 4); emitErr != nil {
				return emitErr
			}
			if failure := failBeforeApply(); failure != nil {
				return failure
			}
			if ack := journal.AckHosting(intentID); ack != nil {
				return ack
			}
			return errors.New("stack variables are incomplete; previous workloads were not changed")
		}
		defer clear(stackBody)
		preflight, stopPreflight := context.WithDeadline(supervisor.Context(), deliveryDeadline)
		preflightErr := engine.stackCompose(preflight, *op.Plan, stackBody, "config", "--quiet")
		stopPreflight()
		if preflightErr != nil {
			if failure := failBeforeApply(); failure != nil {
				return failure
			}
			if ack := journal.AckHosting(intentID); ack != nil {
				return ack
			}
			return errors.New("compose configuration preflight failed; prior workloads were not changed")
		}
	}
	var admitted domain.HostingOperation
	if err = retryAttemptRequest(supervisor, func(ctx context.Context) error {
		var err error
		admitted, err = client.HostingApplyBegin(ctx, a)
		return err
	}); err != nil {
		return err
	}
	if !admitted.MutationHold || admitted.Plan == nil || admitted.Plan.Hash != op.Plan.Hash {
		return errors.New("hosting apply admission did not match plan")
	}
	verifying := func() error {
		return retryAttemptRequest(supervisor, func(ctx context.Context) error {
			_, err := client.HostingStatus(ctx, domain.HostingStatusRequest{HostingAuthority: a, Phase: "verifying"})
			return err
		})
	}
	var result domain.HostingResult
	if op.Plan.Stack != nil {
		seal := func(volumes []domain.HostingStackVolume) error {
			return retryAttemptRequest(supervisor, func(ctx context.Context) error {
				_, sealErr := client.HostingStackVolumes(ctx, domain.HostingStackVolumeRequest{HostingAuthority: a, PlanHash: op.Plan.Hash, Volumes: volumes})
				return sealErr
			})
		}
		result = engine.applyStack(supervisor.Context(), op.ID, *op.Plan, stackBody, deliveryDeadline, admitted.StackCreatePermitted, seal, verifying)
	} else if environment != nil {
		result = engine.ApplyWithRuntimeEnvironment(supervisor.Context(), op.ID, *op.Plan, environment, deliveryDeadline, verifying)
	} else {
		result = engine.Apply(supervisor.Context(), op.ID, *op.Plan, verifying)
	}
	finished, err := journalHostingResultWithRuntimeEnvironment(supervisor, journal, client, a, claim.Lease, result, reporter.WaitEmpty, local, op.Snapshot.RuntimeConfiguration)
	if err != nil {
		return err
	}
	if finished.MutationHold {
		return errors.New("image mutation requires owner reconciliation; durable intent retained")
	}
	if err = journal.AckHosting(intentID); err != nil {
		return err
	}
	if result.Outcome != "succeeded" {
		return errors.New("image HTTP readiness failed; prior workload restoration recorded")
	}
	return nil
}

func journalHostingResult(supervisor *attemptSupervisor, journal *runner.AttemptJournal, client runnerclient.Client, a domain.HostingAuthority, lease domain.RunLease, result domain.HostingResult, drain func(context.Context) error, localBuild ...bool) (domain.HostingOperation, error) {
	return journalHostingResultWithRuntimeEnvironment(supervisor, journal, client, a, lease, result, drain, len(localBuild) > 0 && localBuild[0], domain.HostingRuntimeConfigurationRef{})
}

func journalHostingResultWithRuntimeEnvironment(supervisor *attemptSupervisor, journal *runner.AttemptJournal, client runnerclient.Client, a domain.HostingAuthority, lease domain.RunLease, result domain.HostingResult, drain func(context.Context) error, localBuild bool, ref domain.HostingRuntimeConfigurationRef, storage ...domain.HostingStorageRef) (domain.HostingOperation, error) {
	return journalHostingResultValidated(supervisor, journal, client, a, lease, result, drain, localBuild, ref, nil, storage...)
}

// journalHostingResultValidated checks optional caller proof before discarding durable completion evidence.
func journalHostingResultValidated(supervisor *attemptSupervisor, journal *runner.AttemptJournal, client runnerclient.Client, a domain.HostingAuthority, lease domain.RunLease, result domain.HostingResult, drain func(context.Context) error, localBuild bool, ref domain.HostingRuntimeConfigurationRef, validate func(domain.HostingOperation) error, storage ...domain.HostingStorageRef) (domain.HostingOperation, error) {
	if len(storage) > 1 {
		return domain.HostingOperation{}, errors.New("invalid storage result reference")
	}
	request := domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: attemptMutationKey("hosting_result", a.LeaseID, a.Attempt, "result"), Result: result}
	entry := runner.JournalHosting{ID: request.ReceiptID, Attempt: journalAttemptIdentity(a.RunID, lease, supervisor), OperationID: a.OperationID, Phase: "result", Result: &request, CreatedAt: time.Now().UTC()}
	entry.LocalBuild = localBuild
	markHostingRuntimeEntry(&entry, ref)
	if len(storage) == 1 {
		entry.StorageRef = storage[0]
	}
	if _, err := journal.AppendHosting(entry); err != nil {
		return domain.HostingOperation{}, err
	}
	var op domain.HostingOperation
	if supervisor.Context().Err() == nil {
		// Persist known external effects before any fallible log delivery. Live
		// completion still follows the acknowledged event stream.
		if err := drain(supervisor.Context()); err != nil {
			return domain.HostingOperation{}, err
		}
		if err := retryAttemptRequest(supervisor, func(ctx context.Context) error {
			var err error
			op, err = client.HostingComplete(ctx, request)
			return err
		}); err != nil {
			return domain.HostingOperation{}, err
		}
	} else {
		// Retain the durable local result and intent. Fresh reports cannot gain
		// authority after expiry or cancellation; only committed receipt replay
		// may be acknowledged by the server during startup reconciliation.
		return domain.HostingOperation{}, errLeaseAuthorityLost
	}
	if op.CompleteReceipt == nil || op.CompleteReceipt.ID != request.ReceiptID || op.CompleteReceipt.BodyHash != domain.HostingDocumentHash(result) {
		return domain.HostingOperation{}, errors.New("hosting result acknowledgement changed")
	}
	if validate != nil {
		if err := validate(op); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	if err := journal.AckHosting(entry.ID); err != nil {
		return domain.HostingOperation{}, err
	}
	return op, nil
}

func executeHostingObservation(supervisor *attemptSupervisor, client runnerclient.Client, engine *imageEngine, a domain.HostingAuthority, op domain.HostingOperation) error {
	observation, err := engine.Observe(supervisor.Context(), op)
	if err != nil {
		return err
	}
	observation.HostingAuthority = a
	var observed domain.HostingOperation
	if err = retryAttemptRequest(supervisor, func(ctx context.Context) error {
		var err error
		observed, err = client.HostingReconciliation(ctx, observation)
		return err
	}); err != nil {
		return err
	}
	if observed.ID != op.ID || observed.MutationHold {
		return errors.New("hosting uncertainty remains unresolved")
	}
	return nil
}

// reconcileHostingJournal runs before ordinary heartbeat or claim traffic.
// It may obtain only the owner's exact read-only observation lease; unacknowledged
// mutation intent is retained on every other stale or uncertain outcome.
func reconcileHostingJournal(server, token string, journal *runner.AttemptJournal, endpoint string, parents ...context.Context) error {
	return reconcileHostingJournalWithLocalBuild(server, token, journal, endpoint, false, parents...)
}

func reconcileHostingJournalWithLocalBuild(server, token string, journal *runner.AttemptJournal, endpoint string, localEnabled bool, parents ...context.Context) error {
	if err := validateLocalBuildJournalConfiguration(journal, localEnabled); err != nil {
		return err
	}
	parent := replayParent(parents)
	if parent.Err() != nil {
		return parent.Err()
	}
	snapshot := journal.Snapshot()
	if len(snapshot.Hosting) == 0 {
		return nil
	}
	client := runnerclient.New(server, token)
	if err := reconcileHostingStorageJournal(server, token, journal, endpoint, parent); err != nil {
		return err
	}
	snapshot = journal.Snapshot()
	for _, entry := range snapshot.Hosting {
		if runner.HostingJournalUsesDatabase(entry) || hostingEntryArchiveConflict(journal, entry) {
			continue
		}
		if entry.Phase != "prepared" {
			continue
		}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		op, err := client.HostingPrepared(ctx, *entry.Prepared)
		cancel()
		if err != nil {
			probeCtx, stop := context.WithTimeout(parent, 5*time.Second)
			status, probeErr := client.HostingStatus(probeCtx, domain.HostingStatusRequest{HostingAuthority: entry.Prepared.HostingAuthority})
			stop()
			if probeErr != nil || status.ID != entry.OperationID || status.PreparedReceipt != nil || status.MutationHold {
				return errors.New("hosting preparation replay is unresolved")
			}
			if err = validateLocalBuildReplayOperation(entry, status, localEnabled); err != nil {
				return err
			}
		} else if op.PreparedReceipt == nil || op.PreparedReceipt.ID != entry.Prepared.ReceiptID || op.PreparedReceipt.BodyHash != domain.HostingDocumentHash(entry.Prepared.Release) || op.Release == nil || *op.Release != entry.Prepared.Release || domain.ValidateHostingSnapshotRelease(op.Snapshot, *op.Release) != nil {
			return errors.New("hosting preparation replay receipt changed")
		}
		if err == nil && op.ID != "" {
			if err = validateLocalBuildReplayOperation(entry, op, localEnabled); err != nil {
				return err
			}
		}
		if parent.Err() != nil {
			return parent.Err()
		}
		if err = journal.AckHosting(entry.ID); err != nil {
			return err
		}
	}
	for _, entry := range journal.Snapshot().Hosting {
		if runner.HostingJournalUsesDatabase(entry) || hostingEntryArchiveConflict(journal, entry) {
			continue
		}
		if entry.Phase != "result" {
			continue
		}
		if err := replayHostingEvents(server, token, journal, entry.Attempt, parent); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		op, err := client.HostingComplete(ctx, *entry.Result)
		cancel()
		if err != nil {

			return err
		}
		if err = validateLocalBuildReplayOperation(entry, op, localEnabled); err != nil {
			return err
		}
		if op.CompleteReceipt == nil || op.CompleteReceipt.ID != entry.Result.ReceiptID || op.CompleteReceipt.BodyHash != domain.HostingDocumentHash(entry.Result.Result) {
			return errors.New("hosting result replay receipt changed")
		}
		if parent.Err() != nil {
			return parent.Err()
		}
		if err = journal.AckHosting(entry.ID); err != nil {
			return err
		}
	}
	for _, entry := range journal.Snapshot().Hosting {
		if runner.HostingJournalUsesDatabase(entry) || hostingEntryArchiveConflict(journal, entry) {
			continue
		}
		if entry.Phase == "stack_stop_intent" {
			a := domain.HostingAuthority{OperationID: entry.OperationID, RunID: entry.Attempt.RunID, LeaseID: entry.Attempt.LeaseID, Attempt: entry.Attempt.Attempt, Fence: entry.Attempt.Fence}
			ctx, cancel := context.WithTimeout(parent, 5*time.Second)
			op, err := client.HostingStatus(ctx, domain.HostingStatusRequest{HostingAuthority: a})
			cancel()
			if err != nil {
				return err
			}
			if op.StackStopPlan == nil || entry.StackStopPlan == nil || op.StackStopPlan.Hash != entry.StackStopPlan.Hash || op.RestoreOwnershipHeld {
				return errors.New("stack stop intent retains unresolved ownership")
			}
			if op.MutationHold {
				if !op.ReconcileRequested {
					return errors.New("stack stop intent awaits owner-requested observation")
				}
				if err = reconcileStackStopIntent(parent, client, server, token, endpoint, a); err != nil {
					return err
				}
			} else if op.State != "succeeded" && (op.State != "canceled" || op.CompleteReceipt != nil) {
				return errors.New("stack stop intent has no terminal proof")
			}
			if err = journal.AckHosting(entry.ID); err != nil {
				return err
			}
			continue
		}
		if entry.Phase != "cleanup_intent" {
			continue
		}
		a := domain.HostingAuthority{OperationID: entry.OperationID, RunID: entry.Attempt.RunID, LeaseID: entry.Attempt.LeaseID, Attempt: entry.Attempt.Attempt, Fence: entry.Attempt.Fence}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		op, err := client.HostingStatus(ctx, domain.HostingStatusRequest{HostingAuthority: a})
		cancel()
		if err != nil {
			return err
		}
		if op.ID != entry.OperationID || op.TaskMode != "cleanup" || op.CleanupPlan == nil || entry.CleanupPlan == nil || op.CleanupPlan.Hash != entry.CleanupPlan.Hash || op.RestoreOwnershipHeld || op.MutationHold {
			return errors.New("cleanup intent retains ownership pending a committed result")
		}
		// Cancellation is terminal without a hold only before external admission.
		// Admitted cancellation retains its hold; restored databases retain their
		// separate restore hold. No Docker observation substitutes for that proof.
		notAdmitted := op.State == "canceled" && op.CompleteReceipt == nil && op.Result == nil
		completed := op.State == "succeeded" && op.CompleteReceipt != nil && op.Result != nil && !op.Result.Uncertain && op.Result.Outcome == "succeeded"
		if !notAdmitted && !completed {
			return errors.New("cleanup intent retains ownership pending a committed result")
		}
		if parent.Err() != nil {
			return parent.Err()
		}
		if err = journal.AckHosting(entry.ID); err != nil {
			return err
		}
	}
	for _, entry := range journal.Snapshot().Hosting {
		if runner.HostingJournalUsesDatabase(entry) || hostingEntryArchiveConflict(journal, entry) {
			continue
		}
		if entry.Phase != "apply_intent" {
			continue
		}
		a := domain.HostingAuthority{OperationID: entry.OperationID, RunID: entry.Attempt.RunID, LeaseID: entry.Attempt.LeaseID, Attempt: entry.Attempt.Attempt, Fence: entry.Attempt.Fence}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		op, err := client.HostingStatus(ctx, domain.HostingStatusRequest{HostingAuthority: a})
		cancel()
		if err != nil {
			return err
		}
		if op.ID != entry.OperationID || op.Plan == nil || entry.Plan == nil || op.Plan.Hash != entry.Plan.Hash {
			return errors.New("hosting intent target changed")
		}
		if op.RestoreOwnershipHeld {
			return errors.New("restored hosting ownership remains held; local apply intent retained pending trusted process recovery proof")
		}
		if op.MutationHold {
			if !op.ReconcileRequested {
				return errors.New("hosting apply intent awaits owner-requested reconciliation")
			}
			requestCtx, stop := context.WithTimeout(parent, 5*time.Second)
			claimed, claimErr := client.HostingStatus(requestCtx, domain.HostingStatusRequest{HostingAuthority: a, Phase: "claim_observation"})
			stop()
			if claimErr != nil {
				return claimErr
			}
			if claimed.ObservationLease == nil || claimed.TaskMode != "observe" || claimed.ObservationLease.RunID != claimed.ReconciliationRunID {
				return errors.New("readonly observation lease missing")
			}
			lease := *claimed.ObservationLease
			supervisor := newAttemptSupervisorFrom(parent, lease)
			// Observation keeps the same cancellation and renewal boundaries as execution.
			renewer := startLeaseRenewer(supervisor.Context(), supervisor, server, token, lease, 5*time.Second)
			watcher := startLeaseWatcher(supervisor.Context(), server, token, lease, time.Second, supervisor.cancel)
			observeA := hostingAuthority(lease.RunID, op.ID, lease)
			planCtx, planStop, planErr := supervisor.RequestContext()
			if planErr != nil {
				watcher.Stop()
				renewer.Stop()
				supervisor.Close()
				return planErr
			}
			plan, planErr := client.HostingPlan(planCtx, observeA)
			planStop()
			if planErr == nil {
				planErr = validateHostingPlanResponse(plan, observeA, lease.RunnerID, "observe")
			}
			if planErr == nil {
				engine, engineErr := newImageEngine(endpoint)
				if engineErr != nil {
					planErr = engineErr
				} else {
					planErr = executeHostingObservation(supervisor, client, engine, observeA, plan)
					_ = engine.Close()
				}
			}
			watcher.Stop()
			renewer.Stop()
			supervisor.Close()
			if planErr != nil {
				return fmt.Errorf("readonly hosting observation: %w", planErr)
			}
		}
		if parent.Err() != nil {
			return parent.Err()
		}
		if err = journal.AckHosting(entry.ID); err != nil {
			return err
		}
	}
	return nil
}

// replayHostingEvents preserves events-before-completion for a live attempt.
// A fenced attempt may report only its durable result; stale events remain in
// the journal until its independent mutation intent has been settled.
func replayHostingEvents(server, token string, journal *runner.AttemptJournal, authority runner.AttemptIdentity, parents ...context.Context) error {
	parent := replayParent(parents)
	if parent.Err() != nil {
		return parent.Err()
	}
	for _, pending := range groupReplayAttempts(journal.Snapshot()) {
		if pending.authority.RunID != authority.RunID || pending.authority.LeaseID != authority.LeaseID || pending.authority.Attempt != authority.Attempt || pending.authority.Fence != authority.Fence || len(pending.events) == 0 {
			continue
		}
		lease, fenced, err := probeReplayAuthority(server, token, authority, parent)
		if err != nil || fenced {
			return err
		}
		supervisor := newAttemptSupervisorFrom(parent, lease)
		renewer := startLeaseRenewer(supervisor.Context(), supervisor, server, token, lease, time.Second)
		watcher := startLeaseWatcher(supervisor.Context(), server, token, lease, time.Second, supervisor.cancel)
		pending.completions = nil
		err = reconcileAttempt(server, token, supervisor, pending)
		watcher.Stop()
		renewer.Stop()
		supervisor.Close()
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(pending.events))
		for _, event := range pending.events {
			ids = append(ids, event.ID)
		}
		if parent.Err() != nil {
			return parent.Err()
		}
		if err = journal.AckEvents(ids); err != nil {
			return err
		}
	}
	return nil
}

func executeHostingCleanup(supervisor *attemptSupervisor, journal *runner.AttemptJournal, reporter *attemptReporter, client runnerclient.Client, engine *imageEngine, a domain.HostingAuthority, lease domain.RunLease, op domain.HostingOperation) error {
	if op.CleanupPlan == nil {
		return errors.New("immutable cleanup plan missing")
	}
	expected, err := domain.CompileHostingCleanupPlan(op.CleanupPlan.Target, op.CleanupPlan.CurrentHealthyOperationID)
	if err != nil || expected.Hash != op.CleanupPlan.Hash {
		return errors.New("immutable cleanup plan mismatch")
	}
	intentID := attemptMutationKey("hosting_cleanup_intent", a.LeaseID, a.Attempt, "cleanup")
	entry := runner.JournalHosting{ID: intentID, Attempt: journalAttemptIdentity(a.RunID, lease, supervisor), OperationID: a.OperationID, Phase: "cleanup_intent", CleanupPlan: op.CleanupPlan, CreatedAt: time.Now().UTC()}
	markHostingRuntimeEntry(&entry, op.CleanupPlan.Target.Snapshot.RuntimeConfiguration)
	entry.StorageRef = op.CleanupPlan.Target.Snapshot.Storage
	entry.RuntimeEnvironment = runner.HostingJournalUsesRuntimeEnvironment(entry)
	if _, err = journal.AppendHosting(entry); err != nil {
		return err
	}
	var admitted domain.HostingOperation
	if err = retryAttemptRequest(supervisor, func(ctx context.Context) error {
		var err error
		admitted, err = client.HostingApplyBegin(ctx, a)
		return err
	}); err != nil {
		return err
	}
	if admitted.CleanupPlan == nil || admitted.CleanupPlan.Hash != expected.Hash || !admitted.MutationHold {
		return errors.New("cleanup admission changed")
	}
	result := engine.Cleanup(supervisor.Context(), expected)
	finished, err := journalHostingResultWithRuntimeEnvironment(supervisor, journal, client, a, lease, result, reporter.WaitEmpty, hostingOperationUsesLocalBuild(op), op.CleanupPlan.Target.Snapshot.RuntimeConfiguration, op.CleanupPlan.Target.Snapshot.Storage)
	if err != nil {
		return err
	}
	if finished.MutationHold || finished.RestoreOwnershipHeld || result.Uncertain {
		return errors.New("cleanup effects remain unresolved")
	}
	return journal.AckHosting(intentID)
}

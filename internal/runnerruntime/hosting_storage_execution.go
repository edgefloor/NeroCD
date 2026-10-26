package runnerruntime

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

// This private consumer seam keeps recovery and live execution on the same
// engine operations. It does not grant authority or advertise a capability.
type hostingStorageEngine interface {
	EnsurePreparedRelease(context.Context, domain.HostingRelease) error
	ensureOwnedVolume(context.Context, hostingOwnedVolumeRequest) (hostingOwnedVolumeEvidence, error)
	ApplyWithOwnedStorage(context.Context, string, domain.HostingApplyPlan, hostingOwnedVolumeRequest, hostingOwnedVolumeEvidence, ...func() error) domain.HostingResult
	Observe(context.Context, domain.HostingOperation) (domain.HostingObservation, error)
	Close() error
}

func storageEntryAuthority(entry runner.JournalHosting) domain.HostingAuthority {
	return domain.HostingAuthority{OperationID: entry.OperationID, RunID: entry.Attempt.RunID, LeaseID: entry.Attempt.LeaseID, Attempt: entry.Attempt.Attempt, Fence: entry.Attempt.Fence}
}

func storageOperationMatches(op domain.HostingOperation, a domain.HostingAuthority, ref domain.HostingStorageRef) bool {
	return storageOperationScopeMatches(op, a, ref) && op.Plan != nil && op.Release != nil && op.Plan.Snapshot == op.Snapshot && op.Plan.Release == *op.Release && verifyImagePlan(*op.Plan) == nil && op.Plan.Storage != nil
}

func storageOperationScopeMatches(op domain.HostingOperation, a domain.HostingAuthority, ref domain.HostingStorageRef) bool {
	return op.Snapshot.Database == (domain.HostingDatabaseExecutionRef{}) && !runner.HostingJournalUsesDatabase(runner.JournalHosting{Plan: op.Plan, CleanupPlan: op.CleanupPlan}) && op.ID == a.OperationID && op.RunID == a.RunID && op.TaskMode == "deploy" && op.ResourceID == op.Snapshot.ResourceID && op.Snapshot.Storage == ref && ref != (domain.HostingStorageRef{}) && domain.ValidateHostingStorageSnapshot(op.Snapshot) == nil && op.BindingID == op.Snapshot.BindingID
}

// Reuse the original timestamp and attempt expiry as part of the immutable
// journal body. A renewed lease never changes an already durable receipt.
func storageExistingEntry(journal *runner.AttemptJournal, entry runner.JournalHosting) (runner.JournalHosting, error) {
	for _, old := range journal.Snapshot().Hosting {
		if old.ID != entry.ID {
			continue
		}
		if old.Database != entry.Database || old.DatabaseRef != entry.DatabaseRef || old.Phase != entry.Phase || storageEntryAuthority(old) != storageEntryAuthority(entry) || old.StorageRef != entry.StorageRef || old.Plan == nil || entry.Plan == nil || domain.HostingDocumentHash(old.Plan) != domain.HostingDocumentHash(entry.Plan) {
			return entry, errors.New("storage journal identity changed")
		}
		return old, nil
	}
	return entry, nil
}

func storageVolumeRequest(op domain.HostingOperation, ack domain.HostingStorageCreateAck) (hostingOwnedVolumeRequest, error) {
	p := op.Plan.Storage
	m := ack.Materialization
	s := ack.Storage
	if m.Validate(s) != nil || s.Reference() != p.Reference || s.ProjectID != op.ProjectID || s.BindingID != op.BindingID || s.ResourceID != op.ResourceID || s.DestinationID != op.Snapshot.DestinationID || s.MountPath != p.MountPath || s.VolumeName != p.VolumeName || m.CreationOperationID != p.CreationOperationID || m.CreationNonce != p.CreationNonce || m.DaemonID != p.DaemonID || m.CreationOperationID == op.ID && m.CreationPlanHash != op.Plan.Hash {
		return hostingOwnedVolumeRequest{}, errors.New("storage admission changed immutable identity")
	}
	request := hostingOwnedVolumeRequest{Storage: s, DaemonID: m.DaemonID, OriginOperationID: m.CreationOperationID, OriginPlanHash: m.CreationPlanHash, CreationNonce: m.CreationNonce, CreatePermitted: ack.CreatePermitted}
	for _, healthy := range storagePlanUsers(*op.Plan) {
		if healthy.Storage != nil {
			request.KnownUsers = append(request.KnownUsers, healthy)
		}
	}
	return request, request.validate()
}

func executeHostingStoragePrepared(supervisor *attemptSupervisor, journal *runner.AttemptJournal, drain func(context.Context) error, client runnerclient.Client, engine hostingStorageEngine, a domain.HostingAuthority, lease domain.RunLease, op domain.HostingOperation, runtimeConfig ...*RuntimeEnvironmentConfig) error {
	if !storageOperationMatches(op, a, op.Snapshot.Storage) || op.State != "prepared" || op.RestoreOwnershipHeld || op.Result != nil || op.Snapshot.LocalBuild != (domain.HostingLocalBuildSource{}) || supervisor.Context().Err() != nil {
		return errors.New("storage execution requires a live exact prepared plan")
	}
	if storageArchiveConflict(journal.Snapshot(), op.Snapshot.Storage, op.BindingID, op.ID, "") {
		return errStorageArchiveHeld
	}
	plan := op.Plan
	create := domain.HostingStorageCreateRequest{HostingAuthority: a, PlanHash: plan.Hash, StorageRef: op.Snapshot.Storage, CreationNonce: plan.Storage.CreationNonce}
	entry := runner.JournalHosting{ID: attemptMutationKey("hosting_storage", a.LeaseID, a.Attempt, "create"), Attempt: journalAttemptIdentity(a.RunID, lease, supervisor), OperationID: a.OperationID, Phase: "storage_create_intent", Plan: plan, StorageRef: op.Snapshot.Storage, StorageCreate: &create, CreatedAt: time.Now().UTC()}
	markHostingRuntimeEntry(&entry, op.Snapshot.RuntimeConfiguration)
	entry, err := storageExistingEntry(journal, entry)
	if err != nil {
		return err
	}
	var ack domain.HostingStorageCreateAck
	if err = retryAttemptRequest(supervisor, func(ctx context.Context) error {
		var requestErr error
		ack, requestErr = journalHostingStorageCreate(ctx, journal, client, entry)
		return requestErr
	}); err != nil {
		return err
	}
	volume, err := storageVolumeRequest(op, ack)
	if err != nil {
		return err
	}
	if err = supervisor.Context().Err(); err != nil {
		return err
	}
	// Even a contradictory fresh ACK cannot resurrect permission once local
	// materialization evidence exists. That record can only be inspected/replayed.
	for _, pending := range journal.Snapshot().Hosting {
		if storageEntryAuthority(pending) == a && pending.StorageMaterialized != nil {
			volume.CreatePermitted = false
		}
	}
	evidence, err := engine.ensureOwnedVolume(supervisor.Context(), volume)
	volume.CreatePermitted = false
	if errors.Is(err, errHostingOwnedVolumeUncertain) && supervisor.Context().Err() == nil {
		// The create response may be lost. A bounded read-only inspection can
		// establish its outcome; no second create is ever permitted.
		evidence, err = engine.ensureOwnedVolume(supervisor.Context(), volume)
	}
	if err != nil {
		return fmt.Errorf("storage creation outcome retained for owner recovery: %w", err)
	}
	proof := domain.HostingStorageMaterializedRequest{HostingAuthority: a, ReceiptID: attemptMutationKey("hosting_storage", a.LeaseID, a.Attempt, "materialized"), PlanHash: plan.Hash, StorageRef: evidence.Storage, CreationOperationID: evidence.OriginOperationID, CreationPlanHash: evidence.OriginPlanHash, CreationNonce: evidence.CreationNonce, DaemonID: evidence.DaemonID, VolumeName: evidence.VolumeName, Driver: evidence.Driver, Scope: evidence.Scope, Labels: evidence.Labels, Options: evidence.Options}
	materialized := runner.JournalHosting{ID: proof.ReceiptID, Attempt: entry.Attempt, OperationID: a.OperationID, Phase: "storage_materialized", Plan: plan, StorageRef: op.Snapshot.Storage, StorageMaterialized: &proof, StorageMetadata: &ack.Storage, CreatedAt: time.Now().UTC()}
	markHostingRuntimeEntry(&materialized, op.Snapshot.RuntimeConfiguration)
	materialized, err = storageExistingEntry(journal, materialized)
	if err != nil {
		return err
	}
	if materialized.StorageMaterialized == nil || materialized.StorageMetadata == nil || *materialized.StorageMetadata != ack.Storage || materialized.StorageMaterialized.Matches(ack.Storage, ack.Materialization) != nil || proof.Matches(ack.Storage, ack.Materialization) != nil {
		return errors.New("storage inspection differs from durable evidence")
	}
	// Record a completed inspection even if authority expires before the next
	// request. Its receipt can later be replayed without inventing a new body.
	if _, err = journal.AppendHosting(materialized); err != nil {
		return err
	}
	if err = retryAttemptRequest(supervisor, func(ctx context.Context) error {
		_, requestErr := journalHostingStorageMaterialized(ctx, journal, client, materialized)
		return requestErr
	}); err != nil {
		return err
	}
	intent := runner.JournalHosting{ID: attemptMutationKey("hosting_intent", a.LeaseID, a.Attempt, "apply"), Attempt: entry.Attempt, OperationID: a.OperationID, Phase: "apply_intent", Plan: plan, StorageRef: op.Snapshot.Storage, CreatedAt: time.Now().UTC()}
	markHostingRuntimeEntry(&intent, op.Snapshot.RuntimeConfiguration)
	if intent.RuntimeEnvironment {
		intent.DeliveryID, err = newRuntimeDeliveryID()
		if err != nil {
			return err
		}
	}
	intent, err = storageExistingEntry(journal, intent)
	if err != nil {
		return err
	}
	if _, err = journal.AppendHosting(intent); err != nil {
		return err
	}
	var environment *hostingruntimeprotocol.Environment
	deadline := runtimeDeliveryDeadline(supervisor)
	if intent.RuntimeEnvironment {
		if len(runtimeConfig) != 1 || runtimeConfig[0] == nil {
			return errors.New("owned storage runtime configuration missing")
		}
		environment, err = fetchHostingRuntimeEnvironment(supervisor, client, lease.RunnerID, domain.HostingRuntimeEnvironmentRequest{Authority: a, PlanHash: plan.Hash, RuntimeConfiguration: op.Snapshot.RuntimeConfiguration, DeliveryID: intent.DeliveryID}, *runtimeConfig[0], deadline)
		if err != nil {
			return err
		}
		defer environment.Close()
	}
	var admitted domain.HostingOperation
	if err = retryAttemptRequest(supervisor, func(ctx context.Context) error {
		var requestErr error
		admitted, requestErr = client.HostingApplyBegin(ctx, a)
		return requestErr
	}); err != nil {
		return err
	}
	if !storageOperationMatches(admitted, a, op.Snapshot.Storage) || domain.HostingDocumentHash(admitted.Plan) != domain.HostingDocumentHash(plan) || !admitted.MutationHold || admitted.RestoreOwnershipHeld || admitted.Result != nil || admitted.State != "applying" {
		return errors.New("storage writer admission changed exact plan")
	}
	if err = supervisor.Context().Err(); err != nil {
		return err
	}
	verifying := func() error {
		return retryAttemptRequest(supervisor, func(ctx context.Context) error {
			_, requestErr := client.HostingStatus(ctx, domain.HostingStatusRequest{HostingAuthority: a, Phase: "verifying"})
			return requestErr
		})
	}
	var result domain.HostingResult
	if intent.RuntimeEnvironment {
		combined, ok := engine.(interface {
			ApplyWithOwnedStorageRuntimeEnvironment(context.Context, string, domain.HostingApplyPlan, hostingOwnedVolumeRequest, hostingOwnedVolumeEvidence, *hostingruntimeprotocol.Environment, time.Time, ...func() error) domain.HostingResult
		})
		if !ok {
			return errors.New("owned storage runtime executor unavailable")
		}
		result = combined.ApplyWithOwnedStorageRuntimeEnvironment(supervisor.Context(), op.ID, *plan, volume, evidence, environment, deadline, verifying)
	} else {
		result = engine.ApplyWithOwnedStorage(supervisor.Context(), op.ID, *plan, volume, evidence, verifying)
	}
	finished, err := journalHostingResultWithRuntimeEnvironment(supervisor, journal, client, a, lease, result, drain, false, op.Snapshot.RuntimeConfiguration, op.Snapshot.Storage)
	if err != nil {
		return err
	}
	if !storageOperationSettled(finished, a, op.Snapshot.Storage) {
		return errors.New("storage workload outcome retains its ownership hold")
	}
	if err = retireStorageAttempt(supervisor.Context(), journal, a); err != nil {
		return err
	}
	if result.Outcome != "succeeded" {
		return errors.New("storage workload failed; committed restoration retained")
	}
	return nil
}

func storageOperationSettled(op domain.HostingOperation, a domain.HostingAuthority, ref domain.HostingStorageRef) bool {
	if !storageOperationScopeMatches(op, a, ref) || op.MutationHold || op.RestoreOwnershipHeld || op.Result == nil || op.Result.Uncertain || op.CompleteReceipt == nil || op.CompleteReceipt.ID != attemptMutationKey("hosting_result", a.LeaseID, a.Attempt, "result") || op.CompleteReceipt.BodyHash != domain.HostingDocumentHash(*op.Result) || (op.State != "succeeded" && op.State != "failed" && op.State != "canceled") {
		return false
	}
	// A preparation failure can settle before any plan or volume admission.
	// Only the exact empty failed result has that meaning.
	return storageOperationMatches(op, a, ref) || op.Plan == nil && *op.Result == (domain.HostingResult{Outcome: "failed"}) && op.State == "failed"
}

func retireStorageAttempt(ctx context.Context, journal *runner.AttemptJournal, a domain.HostingAuthority) error {
	for _, entry := range journal.Snapshot().Hosting {
		if storageEntryAuthority(entry) == a && runner.HostingJournalUsesStorage(entry) {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := journal.AckHosting(entry.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func reconcileHostingStorageJournal(server, token string, journal *runner.AttemptJournal, endpoint string, parent context.Context) error {
	return reconcileHostingStorageJournalWithEngine(server, token, journal, endpoint, parent, func(endpoint string) (hostingStorageEngine, error) { return newImageEngine(endpoint) })
}

func reconcileHostingStorageJournalWithEngine(server, token string, journal *runner.AttemptJournal, endpoint string, parent context.Context, open func(string) (hostingStorageEngine, error)) error {
	client := runnerclient.New(server, token)
	seen := map[domain.HostingAuthority]bool{}
	entries := journal.Snapshot().Hosting
	cleanup := map[domain.HostingAuthority]bool{}
	for _, entry := range entries {
		if entry.CleanupPlan != nil {
			cleanup[storageEntryAuthority(entry)] = true
		}
	}
	for _, first := range entries {
		if runner.HostingJournalUsesDatabase(first) || !runner.HostingJournalUsesStorage(first) || cleanup[storageEntryAuthority(first)] {
			continue
		}
		if hostingEntryArchiveConflict(journal, first) {
			continue
		}
		a := storageEntryAuthority(first)
		if seen[a] {
			continue
		}
		seen[a] = true
		if err := reconcileStorageAttempt(parent, server, token, client, journal, endpoint, first, open); err != nil {
			return err
		}
	}
	return nil
}

func reconcileStorageAttempt(parent context.Context, server, token string, client runnerclient.Client, journal *runner.AttemptJournal, endpoint string, first runner.JournalHosting, open func(string) (hostingStorageEngine, error)) error {
	a := storageEntryAuthority(first)
	var rejectedResult error
	for _, entry := range journal.Snapshot().Hosting {
		if storageEntryAuthority(entry) != a || entry.Result == nil && entry.Prepared == nil {
			continue
		}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		if entry.Result != nil {
			cancel()
			if err := replayHostingEvents(server, token, journal, entry.Attempt, parent); err != nil {
				return err
			}
			ctx, cancel = context.WithTimeout(parent, 5*time.Second)
			op, err := client.HostingComplete(ctx, *entry.Result)
			cancel()
			if err != nil {
				// A stale completion can be rejected after its local result was
				// made durable. Only an explicit conflict permits an exact status
				// read toward owner observation; transport, decoding, and other
				// HTTP failures remain failures of this replay.
				if !runnerHTTPStatus(err, http.StatusConflict) {
					return err
				}
				rejectedResult = err
				continue
			}
			if !storageOperationScopeMatches(op, a, first.StorageRef) || op.CompleteReceipt == nil || op.CompleteReceipt.ID != entry.Result.ReceiptID || op.CompleteReceipt.BodyHash != domain.HostingDocumentHash(entry.Result.Result) {
				return errors.New("storage completion replay changed receipt")
			}
		} else {
			op, err := client.HostingPrepared(ctx, *entry.Prepared)
			cancel()
			if err != nil {
				return fmt.Errorf("storage preparation replay: %w", err)
			}
			if !storageOperationMatches(op, a, first.StorageRef) || op.PreparedReceipt == nil || op.PreparedReceipt.ID != entry.Prepared.ReceiptID || op.PreparedReceipt.BodyHash != domain.HostingDocumentHash(entry.Prepared.Release) || *op.Release != entry.Prepared.Release {
				return errors.New("storage preparation replay changed receipt")
			}
		}
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	op, err := client.HostingStatus(ctx, domain.HostingStatusRequest{HostingAuthority: a})
	cancel()
	if err != nil {
		return fmt.Errorf("storage recovery status: %w", err)
	}
	if !storageOperationScopeMatches(op, a, first.StorageRef) {
		return errors.New("storage recovery target changed")
	}
	if rejectedResult != nil && (!op.MutationHold || !op.ReconcileRequested) {
		return fmt.Errorf("storage completion was not acknowledged; exact result retained: %w", rejectedResult)
	}
	if storageOperationSettled(op, a, first.StorageRef) && op.Plan == nil {
		for _, entry := range journal.Snapshot().Hosting {
			if storageEntryAuthority(entry) == a && entry.Plan != nil {
				return errors.New("storage recovery lost its admitted plan")
			}
		}
		return retireStorageAttempt(parent, journal, a)
	}
	if op.State == "canceled" && !op.MutationHold && !op.RestoreOwnershipHeld && op.Result == nil && op.CompleteReceipt == nil && op.Plan == nil {
		for _, entry := range journal.Snapshot().Hosting {
			if storageEntryAuthority(entry) == a && entry.Plan != nil {
				return errors.New("storage cancellation lost its admitted plan")
			}
		}
		return retireStorageAttempt(parent, journal, a)
	}
	if !storageOperationMatches(op, a, first.StorageRef) {
		return errors.New("storage recovery committed plan missing")
	}
	// Retain this conflicting image attempt while unrelated replay and claims
	// continue. Only a fresh archive recovery may settle its storage exclusion.
	if storageArchiveConflict(journal.Snapshot(), op.Snapshot.Storage, op.BindingID, op.ID, "") {
		return nil
	}
	for _, entry := range journal.Snapshot().Hosting {
		if storageEntryAuthority(entry) == a && entry.Plan != nil && domain.HostingDocumentHash(entry.Plan) != domain.HostingDocumentHash(op.Plan) {
			return errors.New("storage recovery immutable plan changed")
		}
	}
	if storageOperationSettled(op, a, first.StorageRef) {
		return retireStorageAttempt(parent, journal, a)
	}
	if op.RestoreOwnershipHeld {
		return errors.New("storage restore ownership awaits trusted process recovery proof")
	}
	if op.State == "canceled" && !op.MutationHold && op.Result == nil && op.CompleteReceipt == nil {
		// Server cancellation without its admission hold proves this operation
		// never received mutation admission. Durable global volume history stays.
		return retireStorageAttempt(parent, journal, a)
	}
	// Owner-requested recovery has separate fresh authority. Reach it before
	// considering continuation of a prepared deployment whose lease may have
	// expired, and never use a rejected result as an acknowledgment.
	if op.MutationHold && op.ReconcileRequested {
		if err := observeStorageRecovery(parent, client, server, token, endpoint, a, op, open); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		settled, err := client.HostingStatus(ctx, domain.HostingStatusRequest{HostingAuthority: a})
		cancel()
		if err != nil {
			return err
		}
		if rejectedResult == nil && storageOperationSettled(settled, a, first.StorageRef) {
			return retireStorageAttempt(parent, journal, a)
		}
		return errors.New("storage owner observation did not establish trusted settlement; exact journal evidence retained")
	}
	if op.State == "prepared" && op.Result == nil {
		lease, fenced, err := probeReplayAuthority(server, token, first.Attempt, parent)
		if err != nil {
			return err
		}
		if fenced {
			return errors.New("storage preparation lost authority; creation evidence retained for owner recovery")
		}
		if lease.RunnerID != op.Snapshot.RunnerID {
			return errors.New("storage recovery runner changed")
		}
		supervisor := newAttemptSupervisorFrom(parent, lease)
		defer supervisor.Close()
		renewer := startLeaseRenewer(supervisor.Context(), supervisor, server, token, lease, time.Second)
		defer renewer.Stop()
		watcher := startLeaseWatcher(supervisor.Context(), server, token, lease, time.Second, supervisor.cancel)
		defer watcher.Stop()
		reporter := startAttemptReporter(supervisor.Context(), supervisor, journal, server, token, a.RunID, lease)
		defer reporter.Stop()
		engine, err := open(endpoint)
		if err != nil {
			return err
		}
		defer func() { _ = engine.Close() }()
		if err = engine.EnsurePreparedRelease(supervisor.Context(), *op.Release); err != nil {
			return err
		}
		return executeHostingStoragePrepared(supervisor, journal, databaseCompletionDrain(supervisor, reporter, watcher, renewer), client, engine, a, lease, op)
	}
	return errors.New("storage creation or possible writer remains unresolved; owner-requested read-only recovery is required before new claims")
}

func observeStorageRecovery(parent context.Context, client runnerclient.Client, server, token, endpoint string, a domain.HostingAuthority, op domain.HostingOperation, open func(string) (hostingStorageEngine, error)) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	claimed, err := client.HostingStatus(ctx, domain.HostingStatusRequest{HostingAuthority: a, Phase: "claim_observation"})
	cancel()
	if err != nil {
		return err
	}
	if claimed.ID != op.ID || claimed.ObservationLease == nil || claimed.TaskMode != "observe" || claimed.ObservationLease.RunID != claimed.ReconciliationRunID || claimed.ObservationLease.RunnerID != op.Snapshot.RunnerID || claimed.ObservationLease.Status != domain.LeaseActive {
		return errors.New("storage read-only observation lease missing")
	}
	lease := *claimed.ObservationLease
	supervisor := newAttemptSupervisorFrom(parent, lease)
	defer supervisor.Close()
	renewer := startLeaseRenewer(supervisor.Context(), supervisor, server, token, lease, time.Second)
	defer renewer.Stop()
	watcher := startLeaseWatcher(supervisor.Context(), server, token, lease, time.Second, supervisor.cancel)
	defer watcher.Stop()
	observeA := hostingAuthority(lease.RunID, op.ID, lease)
	var plan domain.HostingOperation
	if err = retryAttemptRequest(supervisor, func(ctx context.Context) error {
		var requestErr error
		plan, requestErr = client.HostingPlan(ctx, observeA)
		return requestErr
	}); err != nil {
		return err
	}
	if err = validateHostingPlanResponse(plan, observeA, lease.RunnerID, "observe"); err != nil || plan.Plan == nil || domain.HostingDocumentHash(plan.Plan) != domain.HostingDocumentHash(op.Plan) {
		return errors.New("storage read-only observation plan changed")
	}
	engine, err := open(endpoint)
	if err != nil {
		return err
	}
	defer func() { _ = engine.Close() }()
	observation, err := engine.Observe(supervisor.Context(), plan)
	if err != nil {
		return err
	}
	observation.HostingAuthority = observeA
	var observed domain.HostingOperation
	if err = retryAttemptRequest(supervisor, func(ctx context.Context) error {
		var requestErr error
		observed, requestErr = client.HostingReconciliation(ctx, observation)
		return requestErr
	}); err != nil {
		return fmt.Errorf("storage read-only recovery remains held: %w", err)
	}
	if observed.ID != op.ID || observed.MutationHold || observed.RestoreOwnershipHeld {
		return errors.New("storage read-only recovery remains held; observation cannot erase volume history")
	}
	return nil
}

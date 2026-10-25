//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingdatabaseprotocol"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

// The consumer interface keeps unit fixtures out of production host validation.
// Every production mutation receives an exact current-authority callback.
type databaseExecutionEngine interface {
	PrepareDatabase(context.Context, domain.HostingSnapshot, DatabaseEngineConfig, func(context.Context) error) (domain.HostingRelease, error)
	ensureOwnedVolumeWithAuthority(context.Context, hostingOwnedVolumeRequest, ...func(context.Context) error) (hostingOwnedVolumeEvidence, error)
	ApplyDatabase(context.Context, string, domain.HostingApplyPlan, hostingOwnedVolumeRequest, hostingOwnedVolumeEvidence, *DatabaseCredentials, DatabaseEngineConfig, func(context.Context) error) domain.HostingResult
	ObserveDatabase(context.Context, domain.HostingOperation, *DatabaseCredentials, DatabaseEngineConfig) (domain.HostingObservation, error)
	Close() error
}

func validateDatabasePlatformConfig(c DatabaseExecutionConfig) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" {
		return errors.New("database execution requires native Linux arm64")
	}
	_, err := NewDatabaseEngineConfig(c.StateRoot, c.SecretRoot)
	return err
}

func databaseOperationScope(op domain.HostingOperation, a domain.HostingAuthority, runnerID, mode string, ref domain.HostingDatabaseExecutionRef) bool {
	if !databaseOperationSidecarValid(op) || (mode != "deploy" && mode != "observe") || op.TaskMode != mode || op.ID != a.OperationID || op.Snapshot.RunnerID != runnerID || op.ResourceID != op.Snapshot.ResourceID || op.BindingID != op.Snapshot.BindingID || op.Snapshot.Database == (domain.HostingDatabaseExecutionRef{}) || op.Snapshot.Database != ref || domain.ValidateHostingDatabaseSnapshot(op.Snapshot) != nil || op.CleanupPlan != nil {
		return false
	}
	if mode == "deploy" && op.RunID != a.RunID || mode == "observe" && op.ReconciliationRunID != a.RunID {
		return false
	}
	return true
}
func databaseOperationPlan(op domain.HostingOperation) bool {
	return op.Plan != nil && op.Release != nil && op.Plan.Snapshot == op.Snapshot && op.Plan.Release == *op.Release && databasePlanValid(op)
}
func databaseEntry(entry *runner.JournalHosting, op domain.HostingOperation) {
	entry.DatabaseUpgrade = op.DatabaseUpgrade
	entry.DatabaseMaintenance = op.DatabaseMaintenance
	entry.Database = true
	entry.DatabaseRef = op.Snapshot.Database
	entry.StorageRef = op.Snapshot.Storage
}
func databaseCurrentAuthority(s *attemptSupervisor, c runnerclient.Client, a domain.HostingAuthority, op domain.HostingOperation, states ...string) func(context.Context) error {
	return func(parent context.Context) error {
		if parent.Err() != nil || s.Context().Err() != nil {
			return errLeaseAuthorityLost
		}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		var current domain.HostingOperation
		var err error
		switch {
		case op.Plan == nil:
			// Plan rechecks the live task, actor, target and capability before pull.
			current, err = c.HostingPlan(ctx, a)
		case len(states) == 1 && states[0] == "prepared":
			// Empty-phase status is historical. Recheck exact storage admission
			// without consuming or granting another local create permission.
			ack, checkErr := c.HostingStorageCreateBegin(ctx, domain.HostingStorageCreateRequest{HostingAuthority: a, PlanHash: op.Plan.Hash, StorageRef: op.Snapshot.Storage, CreationNonce: op.Plan.Storage.CreationNonce})
			if checkErr != nil {
				return errors.New("database current storage authority unavailable")
			}
			volume, checkErr := storageVolumeRequest(op, ack)
			if checkErr != nil || volume.Storage.Reference() != op.Snapshot.Storage || !databaseUpgradeVolumeMatches(op, ack) {
				return errors.New("database current storage authority changed")
			}
			return parent.Err()
		default:
			// Verifying is a live, final-authority-checked operation. It is
			// idempotent throughout apply and never refreshes credentials.
			current, err = c.HostingStatus(ctx, domain.HostingStatusRequest{HostingAuthority: a, Phase: "verifying"})
		}
		if err != nil || !databaseOperationScope(current, a, op.Snapshot.RunnerID, "deploy", op.Snapshot.Database) || current.RestoreOwnershipHeld || current.Result != nil || current.Snapshot != op.Snapshot || !databaseOperationProofMatches(current, op) || !slices.Contains(states, current.State) {
			return errors.New("database current mutation authority unavailable")
		}
		if op.Plan != nil && (!databaseOperationPlan(current) || domain.HostingDocumentHash(current.Plan) != domain.HostingDocumentHash(op.Plan)) {
			return errors.New("database current plan authority changed")
		}
		if (current.State == "applying" || current.State == "verifying") && !current.MutationHold {
			return errors.New("database mutation hold unavailable")
		}
		return parent.Err()
	}
}

func executeDatabaseClaim(s *attemptSupervisor, journal *runner.AttemptJournal, drain func(context.Context) error, server, token string, claim domain.ClaimedRun, endpoint string, config *DatabaseExecutionConfig) error {
	if config == nil || domain.ValidateDatabaseDeploymentTask(claim.Run) != nil || claim.PrimitivePlan.RunID != claim.Run.ID || claim.Lease.RunID != claim.Run.ID || claim.PrimitivePlan.Process != nil || claim.PrimitivePlan.Checkout != nil || len(claim.PrimitivePlan.Secrets) != 0 || len(claim.PrimitivePlan.Artifacts) != 0 {
		return errors.New("invalid or unconfigured typed database claim")
	}
	engineConfig, err := NewDatabaseEngineConfig(config.StateRoot, config.SecretRoot)
	if err != nil {
		return errors.New("database persistent configuration unavailable")
	}
	engine, err := newImageEngine(endpoint)
	if err != nil {
		return err
	}
	defer func() { _ = engine.Close() }()
	if err = validateDatabaseDaemon(s.Context(), engine, engineConfig); err != nil {
		return err
	}
	c := runnerclient.New(server, token)
	a := hostingAuthority(claim.Run.ID, claim.Run.RunSpec.HostingOperationID, claim.Lease)
	var op domain.HostingOperation
	if err = retryAttemptRequest(s, func(ctx context.Context) error { var e error; op, e = c.HostingPlan(ctx, a); return e }); err != nil {
		return err
	}
	if op.DatabaseUpgrade != nil && !config.NetworkUpgrade || op.DatabaseMaintenance != nil && !config.MembershipMaintenance {
		return errors.New("database network upgrade requires explicit execution configuration")
	}
	mode := claim.Run.RunSpec.HostingMode
	if op.TaskMode != mode || !databaseOperationScope(op, a, claim.Lease.RunnerID, mode, op.Snapshot.Database) {
		return errors.New("database task target changed")
	}
	if mode == "observe" {
		return executeDatabaseObservation(s, c, engine, a, op, *config, engineConfig, drain)
	}
	return executeDatabaseDeployment(s, journal, drain, c, engine, a, claim.Lease, op, *config, engineConfig)
}

// Runtime GOARCH selects the recipe platform. Check the actual daemon separately
// so a forwarded socket or mismatched daemon cannot satisfy native qualification.
func validateDatabaseDaemon(ctx context.Context, e *imageEngine, c DatabaseEngineConfig) error {
	if runtime.GOOS != "linux" || runtime.GOARCH != "arm64" || c.validateHost(ctx, e) != nil {
		return errors.New("database native host profile unavailable")
	}
	raw, err := e.ownedVolumeDocker(ctx, 64, "info", "--format", "{{.Architecture}}")
	if err != nil || string(raw) != "aarch64\n" && string(raw) != "arm64\n" {
		return errors.New("database daemon architecture mismatch")
	}
	return nil
}

func executeDatabaseDeployment(s *attemptSupervisor, j *runner.AttemptJournal, drain func(context.Context) error, c runnerclient.Client, e databaseExecutionEngine, a domain.HostingAuthority, lease domain.RunLease, op domain.HostingOperation, config DatabaseExecutionConfig, ec DatabaseEngineConfig) error {
	if op.DatabaseUpgrade != nil && !config.NetworkUpgrade || op.DatabaseMaintenance != nil && !config.MembershipMaintenance {
		return errors.New("database network upgrade execution disabled")
	}
	if a != hostingAuthority(lease.RunID, op.ID, lease) || !databaseOperationScope(op, a, lease.RunnerID, "deploy", op.Snapshot.Database) || (op.Release == nil) != (op.Plan == nil) || op.State != "preparing" && op.State != "prepared" || op.Result != nil || op.RestoreOwnershipHeld {
		return errors.New("database deployment requires exact preparation authority")
	}
	if op.Release == nil {
		release, err := prepareDatabaseOperation(s, c, e, a, op, ec)
		if err != nil {
			if s.Context().Err() != nil {
				return errLeaseAuthorityLost
			}
			finished, reportErr := journalDatabaseResult(s, j, drain, c, a, lease, op, domain.HostingResult{Outcome: "failed"})
			if reportErr != nil {
				return reportErr
			}
			if !databaseSettled(finished, a, lease.RunnerID, op.Snapshot.Database) {
				return errors.New("database preparation outcome remains unresolved")
			}
			if reportErr = retireDatabaseAttempt(s.Context(), j, a); reportErr != nil {
				return reportErr
			}
			return errors.New("database preparation failed before writer admission")
		}
		req := domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: attemptMutationKey("hosting_prepared", a.LeaseID, a.Attempt, "release"), Release: release}
		entry := runner.JournalHosting{ID: req.ReceiptID, Attempt: journalAttemptIdentity(a.RunID, lease, s), OperationID: a.OperationID, Phase: "prepared", Prepared: &req, CreatedAt: time.Now().UTC()}
		databaseEntry(&entry, op)
		if _, err = j.AppendHosting(entry); err != nil {
			return err
		}
		before := op.Snapshot
		proof := op
		if err = retryAttemptRequest(s, func(ctx context.Context) error { var err error; op, err = c.HostingPrepared(ctx, req); return err }); err != nil {
			return err
		}
		if !databaseOperationProofMatches(proof, op) || op.Snapshot != before || !databaseOperationScope(op, a, lease.RunnerID, "deploy", before.Database) || !databaseOperationPlan(op) || *op.Release != release || op.PreparedReceipt == nil || op.PreparedReceipt.ID != req.ReceiptID || op.PreparedReceipt.BodyHash != domain.HostingDocumentHash(release) {
			return errors.New("database preparation acknowledgement changed")
		}
		// Retain preparation evidence until exact terminal settlement.
	}
	if !databaseOperationPlan(op) || op.State != "prepared" {
		return errors.New("database committed prepared plan missing")
	}
	return executeDatabasePrepared(s, j, drain, c, e, a, lease, op, config, ec)
}

func databaseIntentWindow(entry runner.JournalHosting, s *attemptSupervisor) time.Time {
	deadline := entry.CreatedAt.Add(60 * time.Second)
	if entry.Attempt.ExpiresAt.Before(deadline) {
		deadline = entry.Attempt.ExpiresAt
	}
	if s.GuardDeadline().Before(deadline) {
		deadline = s.GuardDeadline()
	}
	return deadline
}

func fetchDatabaseCredentials(s *attemptSupervisor, c runnerclient.Client, runnerID string, in domain.HostingDatabaseCredentialRequest, plan domain.HostingApplyPlan, config DatabaseExecutionConfig, deadline time.Time) (*DatabaseCredentials, error) {
	ctx, cancel := context.WithDeadline(s.Context(), deadline)
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		protected, err := c.HostingDatabaseCredentials(ctx, runnerID, in, plan, runnerclient.RuntimeEnvironmentTransport{TrustedLoopbackHTTP: config.TrustedLoopbackHTTP})
		if err == nil {
			credentials, err := convertDatabaseCredentials(protected, in, runnerID, plan, deadline)
			protected.Close()
			if ctx.Err() != nil {
				credentials.Close()
				return nil, runnerclient.ErrDatabaseCredentials
			}
			return credentials, err
		}
		if runnerclient.ClassifyFailure(err) != runnerclient.FailureTransient || attempt == 2 || !waitWorkerInterval(ctx, 100*time.Millisecond) {
			break
		}
	}
	return nil, runnerclient.ErrDatabaseCredentials
}
func convertDatabaseCredentials(protected *hostingdatabaseprotocol.Credentials, in domain.HostingDatabaseCredentialRequest, runnerID string, plan domain.HostingApplyPlan, deadline time.Time) (*DatabaseCredentials, error) {
	if domain.ValidateHostingDatabasePlan(plan) != nil {
		return nil, runnerclient.ErrDatabaseCredentials
	}
	return convertValidatedDatabaseCredentials(protected, in, runnerID, plan, deadline, NewDatabaseCredentials)
}
func convertValidatedDatabaseCredentials(protected *hostingdatabaseprotocol.Credentials, in domain.HostingDatabaseCredentialRequest, runnerID string, plan domain.HostingApplyPlan, deadline time.Time, makeCredentials func(domain.HostingApplyPlan, []DatabaseCredentialValue, time.Time) (*DatabaseCredentials, error)) (*DatabaseCredentials, error) {
	m, err := protected.Metadata()
	if err != nil || in.Validate() != nil || m.Authority != in.Authority || m.RunnerID != runnerID || m.DeliveryID != in.DeliveryID || m.PlanHash != plan.Hash || m.Mode != in.Mode || m.Database != plan.Database.Reference || !slices.Equal(m.References, plan.Database.Credentials) || m.RequestHash != hostingdatabaseprotocol.RequestHash(runnerID, in) || m.Purpose != hostingdatabaseprotocol.Purpose {
		return nil, runnerclient.ErrDatabaseCredentials
	}
	if m.UseUntil.Before(deadline) {
		deadline = m.UseUntil
	}
	var out *DatabaseCredentials
	err = protected.WithValues(func(entries []hostingdatabaseprotocol.Credential) error {
		values := make([]DatabaseCredentialValue, len(entries))
		defer func() {
			for i := range values {
				values[i].Close()
			}
		}()
		for i, entry := range entries {
			var err error
			values[i], err = NewDatabaseCredentialValue(entry.Reference, entry.Value)
			if err != nil {
				return runnerclient.ErrDatabaseCredentials
			}
		}
		var err error
		out, err = makeCredentials(plan, values, deadline)
		return err
	})
	if err != nil {
		return nil, runnerclient.ErrDatabaseCredentials
	}
	return out, nil
}

func executeDatabasePrepared(supervisor *attemptSupervisor, journal *runner.AttemptJournal, drain func(context.Context) error, client runnerclient.Client, engine databaseExecutionEngine, a domain.HostingAuthority, lease domain.RunLease, op domain.HostingOperation, config DatabaseExecutionConfig, ec DatabaseEngineConfig, drains ...func(context.Context) error) error {
	if op.DatabaseUpgrade != nil && (!config.NetworkUpgrade || !databaseUpgradeEngineAvailable(engine)) || !databaseMaintenanceEngineConfigured(op, config, engine) {
		return errors.New("database upgrade engine or explicit configuration unavailable")
	}
	if !databaseOperationPlan(op) || !databaseOperationScope(op, a, lease.RunnerID, "deploy", op.Snapshot.Database) || op.State != "prepared" || op.Result != nil || op.RestoreOwnershipHeld || supervisor.Context().Err() != nil {
		return errors.New("database execution requires a live exact prepared plan")
	}
	// Every phase binds the same full sidecar, including plan-free receipts.
	for _, pending := range journal.Snapshot().Hosting {
		if storageEntryAuthority(pending) == a && (!pending.Database || pending.DatabaseRef != op.Snapshot.Database || pending.StorageRef != op.Snapshot.Storage || !databaseJournalProofMatches(pending, op)) {
			return errors.New("database retained operation proof changed")
		}
	}
	// An existing delivery window must still be valid before further storage work.
	for _, pending := range journal.Snapshot().Hosting {
		if storageEntryAuthority(pending) == a && pending.Phase == "apply_intent" && !time.Now().Before(databaseIntentWindow(pending, supervisor)) {
			return errors.New("database original credential window expired; recovery required")
		}
	}
	plan := op.Plan
	create := domain.HostingStorageCreateRequest{HostingAuthority: a, PlanHash: plan.Hash, StorageRef: op.Snapshot.Storage, CreationNonce: plan.Storage.CreationNonce}
	entry := runner.JournalHosting{ID: attemptMutationKey("hosting_storage", a.LeaseID, a.Attempt, "create"), Attempt: journalAttemptIdentity(a.RunID, lease, supervisor), OperationID: a.OperationID, Phase: "storage_create_intent", Plan: plan, StorageRef: op.Snapshot.Storage, StorageCreate: &create, CreatedAt: time.Now().UTC()}
	databaseEntry(&entry, op)
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
	if err != nil || !databaseUpgradeVolumeMatches(op, ack) {
		return errors.New("database storage admission changed existing upgrade identity")
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
	evidence, err := engine.ensureOwnedVolumeWithAuthority(supervisor.Context(), volume, databaseCurrentAuthority(supervisor, client, a, op, "prepared"))
	volume.CreatePermitted = false
	if errors.Is(err, errHostingOwnedVolumeUncertain) && supervisor.Context().Err() == nil {
		// The create response may be lost. A bounded read-only inspection can
		// establish its outcome; no second create is ever permitted.
		evidence, err = engine.ensureOwnedVolumeWithAuthority(supervisor.Context(), volume, databaseCurrentAuthority(supervisor, client, a, op, "prepared"))
	}
	if err != nil {
		return fmt.Errorf("storage creation outcome retained for owner recovery: %w", err)
	}
	proof := domain.HostingStorageMaterializedRequest{HostingAuthority: a, ReceiptID: attemptMutationKey("hosting_storage", a.LeaseID, a.Attempt, "materialized"), PlanHash: plan.Hash, StorageRef: evidence.Storage, CreationOperationID: evidence.OriginOperationID, CreationPlanHash: evidence.OriginPlanHash, CreationNonce: evidence.CreationNonce, DaemonID: evidence.DaemonID, VolumeName: evidence.VolumeName, Driver: evidence.Driver, Scope: evidence.Scope, Labels: evidence.Labels, Options: evidence.Options}
	materialized := runner.JournalHosting{ID: proof.ReceiptID, Attempt: entry.Attempt, OperationID: a.OperationID, Phase: "storage_materialized", Plan: plan, StorageRef: op.Snapshot.Storage, StorageMaterialized: &proof, StorageMetadata: &ack.Storage, CreatedAt: time.Now().UTC()}
	databaseEntry(&materialized, op)
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
	databaseEntry(&intent, op)
	intent, err = storageExistingEntry(journal, intent)
	if err != nil {
		return err
	}
	if intent.DeliveryID == "" {
		intent.DeliveryID, err = newRuntimeDeliveryID()
		if err != nil {
			return err
		}
	}
	if _, err = journal.AppendHosting(intent); err != nil {
		return err
	}

	deadline := databaseIntentWindow(intent, supervisor)
	credentials, err := fetchDatabaseOperationCredentials(supervisor, client, lease.RunnerID, domain.HostingDatabaseCredentialRequest{Authority: a, DeliveryID: intent.DeliveryID, PlanHash: plan.Hash, Mode: "deploy"}, op, config, deadline)
	if err != nil {
		return err
	}
	defer credentials.Close()
	var admitted domain.HostingOperation
	if err = retryAttemptRequest(supervisor, func(ctx context.Context) error {
		var err error
		admitted, err = client.HostingApplyBegin(ctx, a)
		return err
	}); err != nil {
		return err
	}
	if !databaseOperationProofMatches(admitted, op) || !databaseOperationScope(admitted, a, lease.RunnerID, "deploy", op.Snapshot.Database) || !databaseOperationPlan(admitted) || domain.HostingDocumentHash(admitted.Plan) != domain.HostingDocumentHash(plan) || admitted.State != "applying" || !admitted.MutationHold || admitted.RestoreOwnershipHeld || admitted.Result != nil {
		return errors.New("database apply admission changed exact plan")
	}
	result := applyDatabaseOperation(supervisor.Context(), engine, op, volume, evidence, credentials, ec, databaseCurrentAuthority(supervisor, client, a, op, "applying", "verifying"))
	finished, err := journalDatabaseResult(supervisor, journal, drain, client, a, lease, op, result)
	if err != nil {
		return err
	}
	if !databaseSettled(finished, a, op.Snapshot.RunnerID, op.Snapshot.Database) {
		return errors.New("database outcome retains its ownership hold")
	}
	if err = retireDatabaseAttempt(supervisor.Context(), journal, a); err != nil {
		return err
	}
	if result.Outcome != "succeeded" {
		return errors.New("database execution failed; exact outcome retained")
	}
	return nil
}

func journalDatabaseResult(s *attemptSupervisor, j *runner.AttemptJournal, drain func(context.Context) error, c runnerclient.Client, a domain.HostingAuthority, lease domain.RunLease, op domain.HostingOperation, result domain.HostingResult) (domain.HostingOperation, error) {
	req := domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: attemptMutationKey("hosting_result", a.LeaseID, a.Attempt, "result"), Result: result}
	entry := runner.JournalHosting{ID: req.ReceiptID, Attempt: journalAttemptIdentity(a.RunID, lease, s), OperationID: a.OperationID, Phase: "result", Result: &req, CreatedAt: time.Now().UTC()}
	databaseEntry(&entry, op)
	if _, err := j.AppendHosting(entry); err != nil {
		return domain.HostingOperation{}, err
	}
	if s.Context().Err() != nil {
		return domain.HostingOperation{}, errLeaseAuthorityLost
	}
	if err := drain(s.Context()); err != nil {
		return domain.HostingOperation{}, err
	}
	var finished domain.HostingOperation
	err := retryAttemptRequest(s, func(ctx context.Context) error {
		var err error
		finished, err = c.HostingComplete(ctx, req)
		return err
	})
	if err != nil {
		return finished, err
	}
	if !databaseOperationProofMatches(finished, op) || !databaseOperationScope(finished, a, lease.RunnerID, "deploy", op.Snapshot.Database) || finished.CompleteReceipt == nil || finished.CompleteReceipt.ID != req.ReceiptID || finished.CompleteReceipt.BodyHash != domain.HostingDocumentHash(result) || finished.Result == nil || *finished.Result != result {
		return finished, errors.New("database result acknowledgement changed")
	}
	return finished, nil
}
func databaseSettled(op domain.HostingOperation, a domain.HostingAuthority, runnerID string, ref domain.HostingDatabaseExecutionRef) bool {
	if !databaseOperationScope(op, a, runnerID, "deploy", ref) || op.MutationHold || op.RestoreOwnershipHeld || op.Result == nil || op.Result.Uncertain || op.CompleteReceipt == nil || op.CompleteReceipt.ID != attemptMutationKey("hosting_result", a.LeaseID, a.Attempt, "result") || op.CompleteReceipt.BodyHash != domain.HostingDocumentHash(*op.Result) || (op.State != "succeeded" && op.State != "failed" && op.State != "canceled") {
		return false
	}
	return databaseOperationPlan(op) || op.Plan == nil && op.State == "failed" && *op.Result == (domain.HostingResult{Outcome: "failed"})
}

func retireDatabaseAttempt(ctx context.Context, j *runner.AttemptJournal, a domain.HostingAuthority) error {
	for _, entry := range j.Snapshot().Hosting {
		if storageEntryAuthority(entry) == a && runner.HostingJournalUsesDatabase(entry) {
			if entry.DatabaseMaintenance != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
				return j.AckHostingConnection(a)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := j.AckHosting(entry.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func executeDatabaseObservation(s *attemptSupervisor, c runnerclient.Client, e databaseExecutionEngine, a domain.HostingAuthority, op domain.HostingOperation, config DatabaseExecutionConfig, ec DatabaseEngineConfig, drains ...func(context.Context) error) error {
	if op.DatabaseUpgrade != nil && (!config.NetworkUpgrade || !databaseUpgradeEngineAvailable(e)) || !databaseMaintenanceEngineConfigured(op, config, e) {
		return errors.New("database upgrade observation disabled")
	}
	if op.TaskMode != "observe" || !op.ReconcileRequested || !databaseOperationScope(op, a, op.Snapshot.RunnerID, "observe", op.Snapshot.Database) || !databaseOperationPlan(op) || op.Result == nil || op.Result.ContainerID == "" {
		return errors.New("database observation requires new authority and exact durable candidate")
	}
	delivery, err := newRuntimeDeliveryID()
	if err != nil {
		return err
	}
	credentials, err := fetchDatabaseOperationCredentials(s, c, op.Snapshot.RunnerID, domain.HostingDatabaseCredentialRequest{Authority: a, DeliveryID: delivery, PlanHash: op.Plan.Hash, Mode: "observe"}, op, config, runtimeDeliveryDeadline(s))
	if err != nil {
		return err
	}
	defer credentials.Close()
	observation, err := observeDatabaseOperation(s.Context(), e, op, credentials, ec)
	if err != nil {
		return err
	}
	observation.HostingAuthority = a
	if op.DatabaseMaintenance != nil {
		for _, drain := range drains {
			if err := drain(s.Context()); err != nil {
				return err
			}
		}
	}
	var observed domain.HostingOperation
	if err = retryAttemptRequest(s, func(ctx context.Context) error {
		var err error
		observed, err = c.HostingReconciliation(ctx, observation)
		return err
	}); err != nil {
		return err
	}
	if !databaseOperationProofMatches(observed, op) || !databaseOperationScope(observed, a, op.Snapshot.RunnerID, "observe", op.Snapshot.Database) || !databaseOperationPlan(observed) || domain.HostingDocumentHash(observed.Plan) != domain.HostingDocumentHash(op.Plan) {
		return errors.New("database observation target changed")
	}
	if op.DatabaseMaintenance != nil {
		if observed.MutationHold != op.MutationHold || observed.RestoreOwnershipHeld != op.RestoreOwnershipHeld || observed.State != op.State || domain.HostingDocumentHash(observed.Result) != domain.HostingDocumentHash(op.Result) {
			return errors.New("database maintenance observation changed original settlement")
		}
		return nil
	}
	if observed.MutationHold || observed.RestoreOwnershipHeld {
		return errors.New("database observation retains unresolved ownership")
	}
	return nil
}

// ValidateDatabaseExecution verifies the explicit profile without pulling images
// or mutating the daemon. Passing this probe does not qualify the capability.
func ValidateDatabaseExecution(ctx context.Context, c DatabaseExecutionConfig, endpoint string) error {
	if _, err := databaseBackupRemotePolicy(c); err != nil {
		return err
	}
	if err := validateDatabasePlatformConfig(c); err != nil {
		return err
	}
	ec, err := NewDatabaseEngineConfig(c.StateRoot, c.SecretRoot)
	if err != nil {
		return errors.New("database persistent configuration unavailable")
	}
	e, err := newImageEngine(endpoint)
	if err != nil {
		return err
	}
	defer func() { _ = e.Close() }()
	return validateDatabaseDaemon(ctx, e, ec)
}

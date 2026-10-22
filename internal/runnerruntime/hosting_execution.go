package runnerruntime

import (
	"context"
	"errors"
	"fmt"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func hostingAuthority(runID, operationID string, lease domain.RunLease) domain.HostingAuthority {
	return domain.HostingAuthority{OperationID: operationID, RunID: runID, LeaseID: lease.ID, Attempt: lease.Attempt, Fence: lease.Fence}
}
func validateHostingPlanResponse(op domain.HostingOperation, a domain.HostingAuthority, runnerID, mode string) error {
	if op.ID != a.OperationID || op.Snapshot.RunnerID != runnerID || op.Snapshot.BindingID != op.BindingID || op.TaskMode != mode {
		return errors.New("hosting response does not match the exact task target")
	}
	if mode == "deploy" && op.RunID != a.RunID {
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
	return nil
}

func executeImageClaim(supervisor *attemptSupervisor, journal *runner.AttemptJournal, reporter *attemptReporter, server, token string, claim domain.ClaimedRun, endpoint string) error {
	mode := claim.Run.RunSpec.HostingMode
	if claim.Run.RunSpec.HostingOperationID == "" || (mode != "deploy" && mode != "observe") || claim.PrimitivePlan.Process != nil || claim.PrimitivePlan.Checkout != nil || len(claim.PrimitivePlan.Secrets) != 0 || len(claim.PrimitivePlan.Artifacts) != 0 || claim.PrimitivePlan.RunID != claim.Run.ID || claim.Lease.RunID != claim.Run.ID {
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
	engine, err := newImageEngine(endpoint)
	if err != nil {
		return err
	}
	defer func() { _ = engine.Close() }()
	if mode == "observe" {
		return executeHostingObservation(supervisor, client, engine, a, op)
	}
	if err = reporter.Emit("system", "Preparing public container image", 4); err != nil {
		return err
	}
	failBeforeApply := func() error {
		_, err := journalHostingResult(supervisor, journal, client, a, claim.Lease, domain.HostingResult{Outcome: "failed"}, reporter.WaitEmpty)
		return err
	}
	if op.Release == nil {
		release, prepareErr := engine.Prepare(supervisor.Context(), op.Snapshot)
		if prepareErr != nil {
			if supervisor.Context().Err() != nil {
				return prepareErr
			}
			if err = failBeforeApply(); err != nil {
				return err
			}
			return errors.New("image preparation failed; previous workload was not changed")
		}
		request := domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: attemptMutationKey("hosting_prepared", a.LeaseID, a.Attempt, "release"), Release: release}
		entry := runner.JournalHosting{ID: request.ReceiptID, Attempt: journalAttemptIdentity(a.RunID, claim.Lease, supervisor), OperationID: a.OperationID, Phase: "prepared", Prepared: &request, CreatedAt: time.Now().UTC()}
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
		if err = journal.AckHosting(entry.ID); err != nil {
			return err
		}
	}
	if op.Plan == nil || op.Release == nil || verifyImagePlan(*op.Plan) != nil {
		return errors.New("committed image apply plan missing")
	}
	if err = engine.EnsurePreparedRelease(supervisor.Context(), *op.Release); err != nil {
		if supervisor.Context().Err() != nil {
			return err
		}
		return failBeforeApply()
	}
	if err = reporter.Emit("system", "Prepared immutable image release", 5); err != nil {
		return err
	}
	intentID := attemptMutationKey("hosting_intent", a.LeaseID, a.Attempt, "apply")
	intent := runner.JournalHosting{ID: intentID, Attempt: journalAttemptIdentity(a.RunID, claim.Lease, supervisor), OperationID: a.OperationID, Phase: "apply_intent", Plan: op.Plan, CreatedAt: time.Now().UTC()}
	// The durable local intent precedes admission. A lost admission response can
	// never cause this attempt to silently forget possible external effects.
	if _, err = journal.AppendHosting(intent); err != nil {
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
	if !admitted.MutationHold || admitted.Plan == nil || admitted.Plan.Hash != op.Plan.Hash {
		return errors.New("hosting apply admission did not match plan")
	}
	result := engine.Apply(supervisor.Context(), op.ID, *op.Plan, func() error {
		return retryAttemptRequest(supervisor, func(ctx context.Context) error {
			_, err := client.HostingStatus(ctx, domain.HostingStatusRequest{HostingAuthority: a, Phase: "verifying"})
			return err
		})
	})
	finished, err := journalHostingResult(supervisor, journal, client, a, claim.Lease, result, reporter.WaitEmpty)
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

func journalHostingResult(supervisor *attemptSupervisor, journal *runner.AttemptJournal, client runnerclient.Client, a domain.HostingAuthority, lease domain.RunLease, result domain.HostingResult, drain func(context.Context) error) (domain.HostingOperation, error) {
	request := domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: attemptMutationKey("hosting_result", a.LeaseID, a.Attempt, "result"), Result: result}
	entry := runner.JournalHosting{ID: request.ReceiptID, Attempt: journalAttemptIdentity(a.RunID, lease, supervisor), OperationID: a.OperationID, Phase: "result", Result: &request, CreatedAt: time.Now().UTC()}
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
func reconcileHostingJournal(server, token string, journal *runner.AttemptJournal, endpoint string) error {
	snapshot := journal.Snapshot()
	if len(snapshot.Hosting) == 0 {
		return nil
	}
	client := runnerclient.New(server, token)
	for _, entry := range snapshot.Hosting {
		if entry.Phase != "prepared" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		op, err := client.HostingPrepared(ctx, *entry.Prepared)
		cancel()
		if err != nil {
			probeCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			status, probeErr := client.HostingStatus(probeCtx, domain.HostingStatusRequest{HostingAuthority: entry.Prepared.HostingAuthority})
			stop()
			if probeErr != nil || status.ID != entry.OperationID || status.PreparedReceipt != nil || status.MutationHold {
				return errors.New("hosting preparation replay is unresolved")
			}
		} else if op.PreparedReceipt == nil || op.PreparedReceipt.ID != entry.Prepared.ReceiptID || op.PreparedReceipt.BodyHash != domain.HostingDocumentHash(entry.Prepared.Release) {
			return errors.New("hosting preparation replay receipt changed")
		}
		if err = journal.AckHosting(entry.ID); err != nil {
			return err
		}
	}
	for _, entry := range journal.Snapshot().Hosting {
		if entry.Phase != "result" {
			continue
		}
		if err := replayHostingEvents(server, token, journal, entry.Attempt); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		op, err := client.HostingComplete(ctx, *entry.Result)
		cancel()
		if err != nil {

			return err
		}
		if op.CompleteReceipt == nil || op.CompleteReceipt.ID != entry.Result.ReceiptID || op.CompleteReceipt.BodyHash != domain.HostingDocumentHash(entry.Result.Result) {
			return errors.New("hosting result replay receipt changed")
		}
		if err = journal.AckHosting(entry.ID); err != nil {
			return err
		}
	}
	for _, entry := range journal.Snapshot().Hosting {
		if entry.Phase != "apply_intent" {
			continue
		}
		a := domain.HostingAuthority{OperationID: entry.OperationID, RunID: entry.Attempt.RunID, LeaseID: entry.Attempt.LeaseID, Attempt: entry.Attempt.Attempt, Fence: entry.Attempt.Fence}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
			requestCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
			claimed, claimErr := client.HostingStatus(requestCtx, domain.HostingStatusRequest{HostingAuthority: a, Phase: "claim_observation"})
			stop()
			if claimErr != nil {
				return claimErr
			}
			if claimed.ObservationLease == nil || claimed.TaskMode != "observe" || claimed.ObservationLease.RunID != claimed.ReconciliationRunID {
				return errors.New("readonly observation lease missing")
			}
			lease := *claimed.ObservationLease
			supervisor := newAttemptSupervisor(lease)
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
		if err = journal.AckHosting(entry.ID); err != nil {
			return err
		}
	}
	return nil
}

// replayHostingEvents preserves events-before-completion for a live attempt.
// A fenced attempt may report only its durable result; stale events remain in
// the journal until its independent mutation intent has been settled.
func replayHostingEvents(server, token string, journal *runner.AttemptJournal, authority runner.AttemptIdentity) error {
	for _, pending := range groupReplayAttempts(journal.Snapshot()) {
		if pending.authority.RunID != authority.RunID || pending.authority.LeaseID != authority.LeaseID || pending.authority.Attempt != authority.Attempt || pending.authority.Fence != authority.Fence || len(pending.events) == 0 {
			continue
		}
		lease, fenced, err := probeReplayAuthority(server, token, authority)
		if err != nil || fenced {
			return err
		}
		supervisor := newAttemptSupervisor(lease)
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
		if err = journal.AckEvents(ids); err != nil {
			return err
		}
	}
	return nil
}

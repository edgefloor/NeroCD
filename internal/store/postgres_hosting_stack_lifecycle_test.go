package store_test

import (
	"errors"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresHostingStackStopAndStart(t *testing.T) {
	for _, mode := range []string{"confirmed", "uncertain removal", "cancelled before completion"} {
		t.Run(mode, func(t *testing.T) {
			f, deployed, deployment, state := settledStackFixture(t)
			ctx, pg, runner := t.Context(), f.pg, f.server.RunnerID
			input := domain.HostingStackStopInput{IdempotencyKey: "stop-current-stack", ExpectedDeployedOperationID: deployed.ID, ExpectedGeneration: f.resource.Generation}
			if _, err := pg.RequestHostingStackStop(ctx, "hop_unauthorized_stop", f.resource.ID, "usr_unknown", input, destinationEvent("unauthorized_stop")); err == nil {
				t.Fatal("unauthorized Stop admitted")
			}
			staleGeneration := input
			staleGeneration.ExpectedGeneration++
			if _, err := pg.RequestHostingStackStop(ctx, "hop_stale_generation", f.resource.ID, "usr_bootstrap", staleGeneration, destinationEvent("stale_generation")); !errors.Is(err, store.ErrConflict) {
				t.Fatal("stale configuration admitted", err)
			}
			logInput := runtimeLogOpenInput(99)
			logInput.Component = "app"
			logInput.ExpectedOperationID = deployed.ID
			session, err := pg.OpenRuntimeLogSession(ctx, "usr_bootstrap", f.resource.ID, logInput)
			if err != nil {
				t.Fatal(err)
			}
			stale := input
			stale.ExpectedDeployedOperationID = "hop_stale"
			if _, err := pg.RequestHostingStackStop(ctx, "hop_stale_stop", f.resource.ID, "usr_bootstrap", stale, destinationEvent("stale_stop")); !errors.Is(err, store.ErrConflict) {
				t.Fatal("stale deployment admitted", err)
			}
			stop, err := pg.RequestHostingStackStop(ctx, "hop_stack_stop", f.resource.ID, "usr_bootstrap", input, destinationEvent("stack_stop"))
			if err != nil {
				t.Fatal(err)
			}
			replay, err := pg.RequestHostingStackStop(ctx, "hop_other_stop", f.resource.ID, "usr_bootstrap", input, destinationEvent("stack_stop_retry"))
			if err != nil || replay.ID != stop.ID {
				t.Fatal("stop replay changed identity", err)
			}
			stop = awaitHostingDispatch(t, pg, stop.ID)
			f.exec(t, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, runner, domain.HostingStackLifecycleCapability)
			if _, err = pg.ClaimRun(ctx, runner, time.Now().UTC(), time.Minute); !errors.Is(err, store.ErrNotFound) {
				t.Fatal("old agent claimed stop", err)
			}
			f.exec(t, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, runner, domain.HostingStackLifecycleCapability)
			claim, err := pg.ClaimRun(ctx, runner, time.Now().UTC(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			a := domain.HostingAuthority{OperationID: stop.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			if _, err = pg.HostingRunnerPlan(ctx, runner, a); err != nil {
				t.Fatal(err)
			}
			admitted, err := pg.HostingApplyBegin(ctx, runner, a)
			if err != nil {
				t.Fatal(err)
			}
			if page := f.page(t, session.ID); page.Session.State != "target_changed" {
				t.Fatal("Stop left old follow demand active")
			}
			deployment.IdempotencyKey = "while-stopping"
			deployment.HostPort = 0
			if _, err = pg.RequestHostingDeployment(ctx, "hop_competing_start", f.resource.ID, "usr_bootstrap", deployment, destinationEvent("competing_start")); !errors.Is(err, store.ErrConflict) {
				t.Fatal("competing deployment admitted", err)
			}
			stoppedState := state
			stoppedState.Components = []domain.HostingStackComponent{}
			if mode != "confirmed" {
				failed := domain.HostingResult{Outcome: "failed", Uncertain: true, DaemonID: deployed.Release.DaemonID, PlanHash: admitted.StackStopPlan.Hash, Stack: &state}
				if mode == "cancelled before completion" {
					if cancelled, err := pg.CancelHostingOperation(ctx, stop.ID, destinationEvent("cancel_stop")); err != nil || !cancelled.MutationHold {
						t.Fatal("cancellation released stop ownership", err)
					}
					if _, err = pg.HostingComplete(ctx, runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "stop_after_cancel", Result: failed}); !errors.Is(err, store.ErrNotFound) {
						t.Fatal("cancelled lease committed first result", err)
					}
					if _, err = pg.HostingApplyBegin(ctx, runner, a); !errors.Is(err, store.ErrNotFound) {
						t.Fatal("cancelled stop regained mutation authority", err)
					}
				} else {
					held, err := pg.HostingComplete(ctx, runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "stop_uncertain", Result: failed})
					if err != nil || !held.MutationHold || held.State != "reconciliation_required" {
						t.Fatal("uncertainty released stop hold", err)
					}
					if cancelled, err := pg.CancelHostingOperation(ctx, stop.ID, destinationEvent("cancel_stop")); err != nil || !cancelled.MutationHold {
						t.Fatal("cancellation released stop ownership", err)
					}
				}
				if _, err = pg.RequestHostingDeployment(ctx, "hop_held_start", f.resource.ID, "usr_bootstrap", deployment, destinationEvent("held_start")); !errors.Is(err, store.ErrConflict) {
					t.Fatal("held stop allowed deployment", err)
				}
				if _, err = pg.RequestHostingReconciliation(ctx, stop.ID, destinationEvent("stop_observe")); err != nil {
					t.Fatal(err)
				}
				observing, err := pg.ClaimHostingObservation(ctx, runner, a, time.Minute)
				if err != nil {
					t.Fatal(err)
				}
				lease := observing.ObservationLease
				observationAuthority := domain.HostingAuthority{OperationID: stop.ID, RunID: lease.RunID, LeaseID: lease.ID, Attempt: lease.Attempt, Fence: lease.Fence}
				if _, err = pg.HostingRunnerPlan(ctx, runner, observationAuthority); err != nil {
					t.Fatal(err)
				}
				observation := domain.HostingObservation{HostingAuthority: observationAuthority, BindingID: stop.BindingID, DaemonID: deployed.Release.DaemonID, PlanHash: admitted.StackStopPlan.Hash, ReleaseHash: deployed.Release.Hash, Stack: &state, StackStopped: true, NoOwnedContainers: true}
				if _, err = pg.HostingReconciliation(ctx, runner, observation); !errors.Is(err, store.ErrConflict) {
					t.Fatal("remaining writers admitted as stopped", err)
				}
				observation.Stack = &stoppedState
				stop, err = pg.HostingReconciliation(ctx, runner, observation)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				stop, err = pg.HostingComplete(ctx, runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "stack_stopped", Result: domain.HostingResult{Outcome: "succeeded", StackStopped: true, DaemonID: deployed.Release.DaemonID, PlanHash: admitted.StackStopPlan.Hash, Stack: &stoppedState}})
				if err != nil {
					t.Fatal(err)
				}
			}
			lifecycle, err := pg.HostingStackLifecycle(ctx, "usr_bootstrap", f.resource.ID)
			if err != nil || lifecycle.State != "stopped" || lifecycle.StopOperationID != stop.ID {
				t.Fatal("missing settled stop", lifecycle, err)
			}
			if _, err = pg.RuntimeLogTargets(ctx, "usr_bootstrap", f.resource.ID); !errors.Is(err, store.ErrConflict) {
				t.Fatal("stopped stack admitted log target", err)
			}
			deployment.IdempotencyKey = "start-without-proof"
			if _, err = pg.RequestHostingDeployment(ctx, "hop_unproven_start", f.resource.ID, "usr_bootstrap", deployment, destinationEvent("unproven_start")); !errors.Is(err, store.ErrConflict) {
				t.Fatal("start omitted stopped proof", err)
			}
			deployment.IdempotencyKey = "start-stopped-stack"
			deployment.ManagedStack.ExpectedStoppedOperationID = stop.ID
			start, err := pg.RequestHostingDeployment(ctx, "hop_stack_start", f.resource.ID, "usr_bootstrap", deployment, destinationEvent("stack_start"))
			if err != nil {
				t.Fatal(err)
			}
			start = awaitHostingDispatch(t, pg, start.ID)
			f.exec(t, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, runner, domain.HostingStackLifecycleCapability)
			if _, err = pg.ClaimRun(ctx, runner, time.Now().UTC(), time.Minute); !errors.Is(err, store.ErrNotFound) {
				t.Fatal("old agent claimed start", err)
			}
			f.exec(t, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, runner, domain.HostingStackLifecycleCapability)
			claim, err = pg.ClaimRun(ctx, runner, time.Now().UTC(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			a = domain.HostingAuthority{OperationID: start.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			if _, err = pg.HostingRunnerPlan(ctx, runner, a); err != nil {
				t.Fatal(err)
			}
			prepared, err := pg.HostingPrepared(ctx, runner, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "stack_start_prepared", Release: *deployed.Release, StackImages: deployed.Plan.Stack.Images})
			if err != nil {
				t.Fatal(err)
			}
			if prepared.Plan.PreviousStopped == nil || prepared.Plan.PreviousStopped.StopOperationID != stop.ID {
				t.Fatal("start lost stopped proof")
			}
			if _, err = pg.HostingApplyBegin(ctx, runner, a); err != nil {
				t.Fatal(err)
			}
			for i := range state.Components {
				state.Components[i].OperationID = start.ID
			}
			if _, err = pg.HostingComplete(ctx, runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "stack_started", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, DaemonID: deployed.Release.DaemonID, PlanHash: prepared.Plan.Hash, Stack: &state}}); err != nil {
				t.Fatal(err)
			}
			lifecycle, err = pg.HostingStackLifecycle(ctx, "usr_bootstrap", f.resource.ID)
			if err != nil || lifecycle.State != "running" || lifecycle.DeployedOperationID != start.ID || lifecycle.StopOperationID != "" {
				t.Fatal("start did not settle", lifecycle, err)
			}
		})
	}
}

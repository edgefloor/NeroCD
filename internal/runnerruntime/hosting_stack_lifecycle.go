package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

// stackStopInspection borrows immutable plan data; its owned stack copy substitutes
// materialized volume origins for the pre-creation values in the original plan.
func stackStopInspection(p domain.HostingStackStopPlan) domain.HostingApplyPlan {
	plan := p.TargetPlan
	stack := *plan.Stack
	stack.Volumes = append([]domain.HostingStackVolume{}, p.Target.Stack.Volumes...)
	plan.Stack = &stack
	plan.PreviousHealthy = &p.Target
	plan.PreviousStopped = nil
	return plan
}
func (e *imageEngine) observeStoppedStack(ctx context.Context, p domain.HostingStackStopPlan) (domain.HostingStackState, error) {
	if p.Validate() != nil {
		return domain.HostingStackState{}, errors.New("invalid stack stop target")
	}
	state := domain.HostingStackState{PublicComponent: p.Target.Stack.PublicComponent, NetworkID: p.Target.Stack.NetworkID, Components: []domain.HostingStackComponent{}, Volumes: append([]domain.HostingStackVolume{}, p.Target.Stack.Volumes...)}
	if p.Validate() != nil || e.verifyDaemon(ctx, p.Target.Release.DaemonID) != nil {
		return state, errors.New("stack stop daemon or target changed")
	}
	plan := stackStopInspection(p)
	if _, err := e.ensureStackVolumes(ctx, p.Target.OperationID, plan, false); err != nil {
		return state, err
	}
	network, err := e.ensureStackNetwork(ctx, p.Target.OperationID, plan, false)
	if err != nil || network != state.NetworkID {
		return state, errors.New("retained stack network changed")
	}
	containers, err := e.stackContainers(ctx, plan.Snapshot.BindingID)
	if err != nil {
		return state, err
	}
	for _, c := range containers {
		state.Components = append(state.Components, stackComponentState(c))
	}
	raw, err := e.docker(ctx, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=com.docker.compose.project="+plan.Snapshot.RuntimeNamespace)
	if err != nil || len(containers) != 0 || len(strings.Fields(string(raw))) != 0 {
		return state, errors.New("stack containers remain present")
	}
	for _, v := range state.Volumes {
		raw, err = e.docker(ctx, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "volume="+v.VolumeName)
		if err != nil || len(strings.Fields(string(raw))) != 0 {
			return state, errors.New("retained stack volume has users")
		}
	}
	raw, err = e.docker(ctx, "network", "inspect", state.NetworkID, "--format", "{{json .Containers}}")
	var attached map[string]json.RawMessage
	if err != nil || json.Unmarshal(raw, &attached) != nil || len(attached) != 0 {
		return state, errors.New("retained stack network still has container users")
	}
	state.ObservationComplete = true
	return state, domain.ValidateHostingStackStoppedState(p, state)
}
func (e *imageEngine) stopStack(ctx context.Context, p domain.HostingStackStopPlan) domain.HostingResult {
	result := domain.HostingResult{Outcome: "failed", Uncertain: true, DaemonID: p.Target.Release.DaemonID, PlanHash: p.Hash}
	if p.Validate() != nil {
		return result
	}
	state := domain.HostingStackState{PublicComponent: p.Target.Stack.PublicComponent, NetworkID: p.Target.Stack.NetworkID, Components: []domain.HostingStackComponent{}, Volumes: append([]domain.HostingStackVolume{}, p.Target.Stack.Volumes...)}
	result.Stack = &state
	if p.Validate() != nil || e.verifyDaemon(ctx, p.Target.Release.DaemonID) != nil {
		return result
	}
	plan := stackStopInspection(p)
	if _, err := e.ensureStackVolumes(ctx, p.Target.OperationID, plan, false); err != nil {
		return result
	}
	network, err := e.ensureStackNetwork(ctx, p.Target.OperationID, plan, false)
	if err != nil {
		return result
	}
	// Validate all old mounts, images, ports and labels before touching any writer.
	containers, err := e.stackContainers(ctx, plan.Snapshot.BindingID)
	if err != nil {
		return result
	}
	for _, c := range containers {
		if stackContainerMatches(c, p.Target.OperationID, p.TargetPlan, network) != nil {
			return result
		}
	}
	if err = e.stopStackPredecessor(ctx, plan); err != nil {
		observed, observeErr := e.observeStackState(ctx, p.Target.OperationID, plan, network, state.Volumes, false)
		observed.ObservationComplete = observeErr == nil
		result.Stack = &observed
		return result
	}
	observed, err := e.observeStoppedStack(ctx, p)
	result.Stack = &observed
	if err != nil {
		return result
	}
	result.Outcome = "succeeded"
	result.Uncertain = false
	result.StackStopped = true
	return result
}

func executeHostingStackStop(supervisor *attemptSupervisor, journal *runner.AttemptJournal, drain func(context.Context) error, client runnerclient.Client, engine *imageEngine, a domain.HostingAuthority, lease domain.RunLease, op domain.HostingOperation) error {
	if op.StackStopPlan == nil || op.StackStopPlan.Validate() != nil {
		return errors.New("immutable stack stop plan missing")
	}
	intentID := attemptMutationKey("stack_stop_intent", a.LeaseID, a.Attempt, "stop")
	entry := runner.JournalHosting{ID: intentID, Attempt: journalAttemptIdentity(a.RunID, lease, supervisor), OperationID: a.OperationID, Phase: "stack_stop_intent", StackStopPlan: op.StackStopPlan, CreatedAt: time.Now().UTC()}
	if _, err := journal.AppendHosting(entry); err != nil {
		return err
	}
	var admitted domain.HostingOperation
	if err := retryAttemptRequest(supervisor, func(ctx context.Context) error {
		var err error
		admitted, err = client.HostingApplyBegin(ctx, a)
		return err
	}); err != nil {
		return err
	}
	if admitted.StackStopPlan == nil || admitted.StackStopPlan.Hash != op.StackStopPlan.Hash || !admitted.MutationHold {
		return errors.New("stack stop admission changed")
	}
	result := engine.stopStack(supervisor.Context(), *op.StackStopPlan)
	finished, err := journalHostingResult(supervisor, journal, client, a, lease, result, drain)
	if err != nil {
		return err
	}
	if finished.MutationHold || result.Uncertain {
		return errors.New("stack stop remains uncertain; mutation intent retained")
	}
	return journal.AckHosting(intentID)
}

// Replay obtains only the owner's existing read-only observation task. It never
// resumes a removal command after a stale or interrupted attempt.
func reconcileStackStopIntent(parent context.Context, client runnerclient.Client, server, token, endpoint string, a domain.HostingAuthority) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	claimed, err := client.HostingStatus(ctx, domain.HostingStatusRequest{HostingAuthority: a, Phase: "claim_observation"})
	cancel()
	if err != nil {
		return err
	}
	if claimed.StackStopPlan == nil || claimed.ObservationLease == nil || claimed.TaskMode != "observe" || claimed.ObservationLease.RunID != claimed.ReconciliationRunID {
		return errors.New("exact stack stop observation lease missing")
	}
	lease := *claimed.ObservationLease
	supervisor := newAttemptSupervisorFrom(parent, lease)
	renewer := startLeaseRenewer(supervisor.Context(), supervisor, server, token, lease, 5*time.Second)
	watcher := startLeaseWatcher(supervisor.Context(), server, token, lease, time.Second, supervisor.cancel)
	defer func() { watcher.Stop(); renewer.Stop(); supervisor.Close() }()
	observeAuthority := hostingAuthority(lease.RunID, a.OperationID, lease)
	planCtx, finish, err := supervisor.RequestContext()
	if err != nil {
		return err
	}
	op, err := client.HostingPlan(planCtx, observeAuthority)
	finish()
	if err != nil {
		return err
	}
	if err = validateHostingPlanResponse(op, observeAuthority, lease.RunnerID, "observe"); err != nil {
		return err
	}
	engine, err := newImageEngine(endpoint)
	if err != nil {
		return err
	}
	defer func() { _ = engine.Close() }()
	return executeHostingObservation(supervisor, client, engine, observeAuthority, op)
}

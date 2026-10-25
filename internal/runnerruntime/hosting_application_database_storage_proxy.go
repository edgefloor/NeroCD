//go:build unix

package runnerruntime

import (
	"context"
	"path/filepath"

	"nerocd/internal/domain"
	"nerocd/internal/hostingproxy"
	"nerocd/internal/runnerclient"
)

func applicationStorageProxyPlans(config ProxyConfig, plan domain.HostingProxyPlan) (hostingproxy.Plan, *hostingproxy.Plan, error) {
	s := plan.Snapshot
	proof := s.StorageSwitch
	if proof == nil || proof.Validate(s, plan.PreviousGeneration) != nil {
		return hostingproxy.Plan{}, nil, errApplicationDatabaseExecution
	}
	settings := hostingproxy.Settings{Directory: filepath.Join(config.Directory, s.ProxyID), DockerBinary: config.DockerBinary, DockerSocket: config.DockerSocket, Email: s.Configuration.Email, CA: hostingproxy.CA(s.Configuration.CA), ManagementPort: s.Configuration.ManagementPort}
	identity := hostingproxy.Identity{ProxyID: s.ProxyID, ServerID: s.ServerID, DestinationID: s.DestinationID, RunnerID: s.RunnerID, DaemonID: plan.DaemonID}
	priorRoutes := []hostingproxy.Route{}
	for _, r := range domain.HostingProxyPriorRoutes(s) {
		priorRoutes = append(priorRoutes, hostingproxy.Route{ID: r.RouteID, Hostname: r.Hostname, PathPrefix: r.PathPrefix, HostPort: r.HostPort})
	}
	var prior hostingproxy.Plan
	var err error
	if len(proof.PriorRequiredRouteIDs) == 0 {
		prior, err = hostingproxy.Prepare(identity, settings, uint64(proof.PriorGeneration), uint64(proof.PriorPreviousGeneration), priorRoutes)
	} else {
		prior, err = hostingproxy.PrepareVerifiedSwitch(identity, settings, uint64(proof.PriorGeneration), uint64(proof.PriorPreviousGeneration), priorRoutes, proof.PriorRequiredRouteIDs)
	}
	if err != nil || prior.Hash() != proof.PriorEngineHash {
		return hostingproxy.Plan{}, nil, errApplicationDatabaseExecution
	}
	if proof.Action == "switch" {
		return prior, nil, nil
	}
	failedSnapshot := s
	failedSnapshot.Generation = proof.FailedGeneration
	copyProof := *proof
	copyProof.Action = "switch"
	copyProof.FailedProxyOperationID = ""
	copyProof.FailedGeneration = 0
	copyProof.FailedPlanHash = ""
	copyProof.FailedEngineHash = ""
	copyProof.Hash = copyProof.DocumentHash()
	failedSnapshot.StorageSwitch = &copyProof
	// Recovery changes metadata CAS versions by exactly the recorded restoration.
	verified := *s.VerifiedSwitch
	verified.Routes = append([]domain.HostingVerifiedSwitchRoute(nil), verified.Routes...)
	for i := range verified.Routes {
		verified.Routes[i].PriorVersion -= 2
	}
	failedSnapshot.VerifiedSwitch = &verified
	failedPlan, err := domain.CompileHostingProxyPlan(failedSnapshot, plan.PreviousGeneration, plan.DaemonID)
	if err != nil || failedPlan.Hash != proof.FailedPlanHash {
		return hostingproxy.Plan{}, nil, errApplicationDatabaseExecution
	}
	routes := []hostingproxy.Route{}
	for _, r := range s.Routes {
		routes = append(routes, hostingproxy.Route{ID: r.RouteID, Hostname: r.Hostname, PathPrefix: r.PathPrefix, HostPort: r.HostPort})
	}
	failed, err := hostingproxy.PrepareVerifiedSwitch(identity, settings, uint64(proof.FailedGeneration), uint64(proof.PriorGeneration), routes, s.VerifiedSwitch.RouteIDs())
	if err != nil || failed.Hash() != proof.FailedEngineHash {
		return hostingproxy.Plan{}, nil, errApplicationDatabaseExecution
	}
	return prior, &failed, nil
}
func applyApplicationStorageProxy(s *attemptSupervisor, c runnerclient.Client, engine managedProxyEngine, local hostingproxy.Plan, op domain.HostingProxyOperation, a domain.HostingAuthority, config ProxyConfig) (hostingproxy.Result, error) {
	refused := hostingproxy.Result{Outcome: hostingproxy.Refused, DesiredGeneration: uint64(op.Generation), LoadedGeneration: uint64(op.Plan.PreviousGeneration), Hash: local.Hash()}
	if config.storageDatabase == nil || !config.storageDatabase.ApplicationStorage || !config.storageDatabase.RouteContinuity || !config.VerifiedSwitch {
		return refused, errApplicationDatabaseExecution
	}
	special, ok := engine.(interface {
		ApplyInterruptedStorageSwitch(context.Context, hostingproxy.Plan, hostingproxy.Plan, *hostingproxy.Plan, func(context.Context) error) (hostingproxy.Result, error)
	})
	if !ok {
		return refused, errApplicationDatabaseExecution
	}
	prior, failed, err := applicationStorageProxyPlans(config, *op.Plan)
	if err != nil {
		return refused, err
	}
	before := func(ctx context.Context) error {
		state, err := c.HostingApplicationDatabaseStorageProxyPlan(ctx, a)
		proof := op.Snapshot.StorageSwitch
		if err != nil || state.Operation.ID != proof.OperationID || state.Operation.ApplicationDatabaseRouteContinuity.Hash != proof.ContinuationHash || state.Stage.Candidate.Hash != proof.CandidateHash || state.Operation.ApplicationDatabaseStorageState.StateHash != proof.WriterStateHash || domain.HostingDocumentHash(state.Storage.Materialization) != proof.StorageOriginHash {
			return errApplicationDatabaseExecution
		}
		ec, err := NewDatabaseEngineConfig(config.storageDatabase.StateRoot, config.storageDatabase.SecretRoot)
		if err != nil {
			return err
		}
		e, err := newApplicationDatabaseEngine(config.storageEndpoint, ec)
		if err != nil {
			return err
		}
		defer e.Close()
		if err = e.bindStorage(state.Operation, state.Storage); err != nil {
			return err
		}
		in, err := applicationDatabaseInput(state.Operation)
		if err != nil {
			return err
		}
		candidate := privateApplicationDatabaseCandidate(state.Stage.Candidate)
		if e.storageGuard(ctx, in, &candidate, true) != nil || state.Stage.EndpointReceipt == nil || state.Stage.EndpointReceipt.Observation == nil || e.VerifyReady(ctx, in, candidate, *state.Stage.EndpointReceipt.Observation) != nil {
			return errApplicationDatabaseExecution
		}
		return ctx.Err()
	}
	return special.ApplyInterruptedStorageSwitch(s.Context(), local, prior, failed, before)
}

func verifyApplicationStorageProxyReceipt(ctx context.Context, engine managedProxyEngine, current hostingproxy.Plan, durable domain.HostingProxyPlan, config ProxyConfig, result domain.HostingProxyResult) error {
	if config.storageDatabase == nil || !config.storageDatabase.ApplicationStorage || !config.storageDatabase.RouteContinuity || !config.VerifiedSwitch || domain.ValidateHostingProxyPlan(durable) != nil || result.PlanHash != durable.Hash || result.EngineHash != current.Hash() || result.DaemonID != durable.DaemonID || uint64(durable.Snapshot.Generation) != current.Generation() {
		return errApplicationDatabaseExecution
	}
	verifier, ok := engine.(interface {
		VerifyInterruptedStorageReceipt(context.Context, hostingproxy.Plan, hostingproxy.Plan, *hostingproxy.Plan, hostingproxy.Outcome, uint64) error
	})
	if !ok {
		return errApplicationDatabaseExecution
	}
	prior, failed, err := applicationStorageProxyPlans(config, durable)
	if err != nil {
		return err
	}
	return verifier.VerifyInterruptedStorageReceipt(ctx, current, prior, failed, hostingproxy.Outcome(result.Outcome), uint64(result.LoadedGeneration))
}

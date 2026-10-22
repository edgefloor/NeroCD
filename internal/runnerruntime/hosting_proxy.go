package runnerruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingproxy"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

// ProxyConfig contains only explicit operator-owned local execution settings.
// An empty configuration preserves legacy runner behavior without proxy effects.
type ProxyConfig struct {
	Directory, DockerBinary, DockerSocket, Protocol string
	engineFactory                                   func(hostingproxy.Settings) (managedProxyEngine, error)
}

type managedProxyEngine interface {
	PrepareManaged(context.Context, hostingproxy.Identity, uint64, uint64, []hostingproxy.Route) (hostingproxy.Plan, string, error)
	Install(context.Context, hostingproxy.Plan) (hostingproxy.Result, error)
	Apply(context.Context, hostingproxy.Plan) (hostingproxy.Result, error)
	Observe(context.Context, hostingproxy.Plan) (hostingproxy.Result, error)
	VerifyManagedReceipt(context.Context, hostingproxy.Plan, hostingproxy.Outcome, uint64) error
}

// Enabled reports whether the operator selected a persistent proxy state root.
func (c ProxyConfig) Enabled() bool { return c.Directory != "" }

// Validate refuses partial configuration, unsupported protocols and non-Linux hosts.
// It initializes only the operator-owned state root, never a proxy generation.
func (c ProxyConfig) Validate() error {
	if c.Directory == "" && c.DockerBinary == "" && c.DockerSocket == "" && c.Protocol == "" {
		return nil
	}
	if runtime.GOOS != "linux" || c.Protocol != domain.HostingProxyCompilerVersion {
		return errors.New("managed proxy requires Linux and protocol managed-traefik.file.v1")
	}
	for _, path := range []string{c.Directory, c.DockerBinary, c.DockerSocket} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return errors.New("managed proxy requires explicit absolute state, Docker binary and Unix socket paths")
		}
	}
	binary, err := os.Stat(c.DockerBinary)
	if err != nil || !binary.Mode().IsRegular() || binary.Mode().Perm()&0111 == 0 {
		return errors.New("managed proxy Docker binary is not executable")
	}
	socket, err := os.Lstat(c.DockerSocket)
	if err != nil || socket.Mode()&os.ModeSocket == 0 {
		return errors.New("managed proxy Docker endpoint must be a local Unix socket")
	}
	return hostingproxy.ValidateManagedRoot(c.Directory)
}

func proxyResponseMatches(op domain.HostingProxyOperation, a domain.HostingAuthority, runnerID string, plan *domain.HostingProxyPlan) error {
	if op.ID != a.OperationID || op.RunID != a.RunID || op.Plan == nil || op.ProxyID != op.Snapshot.ProxyID || op.Snapshot.RunnerID != runnerID || domain.ValidateHostingProxyPlan(*op.Plan) != nil || plan != nil && !reflect.DeepEqual(*op.Plan, *plan) {
		return errors.New("proxy response changed exact target or immutable plan")
	}
	// Persisted snapshots retain resource order; dispatch plans canonically sort
	// route IDs. Compare their complete compiled content rather than slice order.
	compiled, err := domain.CompileHostingProxyPlan(op.Snapshot, op.Plan.PreviousGeneration, op.Plan.DaemonID)
	if err != nil || !reflect.DeepEqual(compiled, *op.Plan) {
		return errors.New("proxy snapshot differs from immutable dispatch plan")
	}
	return nil
}

func prepareProxyEngine(ctx context.Context, config ProxyConfig, plan domain.HostingProxyPlan, prepared *domain.HostingProxyPreparation) (managedProxyEngine, hostingproxy.Plan, domain.HostingProxyPreparation, error) {
	if !config.Enabled() || config.Protocol != domain.HostingProxyCompilerVersion || domain.ValidateHostingProxyPlan(plan) != nil {
		return nil, hostingproxy.Plan{}, domain.HostingProxyPreparation{}, errors.New("explicit supported proxy configuration and canonical plan are required")
	}
	snapshot := plan.Snapshot
	for _, id := range []string{snapshot.ProxyID, snapshot.ServerID, snapshot.DestinationID, snapshot.RunnerID} {
		if len(id) > 80 {
			return nil, hostingproxy.Plan{}, domain.HostingProxyPreparation{}, errors.New("proxy engine placement identity exceeds its 80-byte limit")
		}
	}
	routes := make([]hostingproxy.Route, 0, len(snapshot.Routes))
	for _, route := range snapshot.Routes {
		if len(route.RouteID) > 80 {
			return nil, hostingproxy.Plan{}, domain.HostingProxyPreparation{}, errors.New("proxy engine route identity exceeds its 80-byte limit")
		}
		routes = append(routes, hostingproxy.Route{ID: route.RouteID, Hostname: route.Hostname, PathPrefix: route.PathPrefix, HostPort: route.HostPort})
	}
	settings := hostingproxy.Settings{Directory: filepath.Join(config.Directory, snapshot.ProxyID), DockerBinary: config.DockerBinary, DockerSocket: config.DockerSocket, Email: snapshot.Configuration.Email, CA: hostingproxy.CA(snapshot.Configuration.CA), ManagementPort: snapshot.Configuration.ManagementPort}
	var engine managedProxyEngine
	var err error
	if config.engineFactory != nil {
		engine, err = config.engineFactory(settings)
	} else {
		engine, err = hostingproxy.New(settings)
	}
	if err != nil {
		return nil, hostingproxy.Plan{}, domain.HostingProxyPreparation{}, err
	}
	identity := hostingproxy.Identity{ProxyID: snapshot.ProxyID, ServerID: snapshot.ServerID, DestinationID: snapshot.DestinationID, RunnerID: snapshot.RunnerID, DaemonID: plan.DaemonID}
	if prepared != nil {
		identity.DaemonID = prepared.DaemonID
	}
	local, daemon, err := engine.PrepareManaged(ctx, identity, uint64(snapshot.Generation), uint64(plan.PreviousGeneration), routes)
	if err != nil {
		return nil, hostingproxy.Plan{}, domain.HostingProxyPreparation{}, err
	}
	preparation := domain.HostingProxyPreparation{DaemonID: daemon, EngineHash: local.Hash(), StaticHash: local.StaticHash(), PlanHash: plan.Hash}
	if prepared != nil && *prepared != preparation || plan.DaemonID != "" && plan.DaemonID != daemon {
		return nil, hostingproxy.Plan{}, domain.HostingProxyPreparation{}, errors.New("local proxy settings, daemon or engine plan changed")
	}
	return engine, local, preparation, nil
}

func proxyJournalEntry(supervisor *attemptSupervisor, lease domain.RunLease, a domain.HostingAuthority, plan domain.HostingProxyPlan, preparation domain.HostingProxyPreparation, phase string) runner.JournalProxy {
	return runner.JournalProxy{ID: attemptMutationKey("proxy_"+phase, a.LeaseID, a.Attempt, "proxy"), Attempt: journalAttemptIdentity(a.RunID, lease, supervisor), OperationID: a.OperationID, Phase: phase, Plan: plan, Preparation: preparation, CreatedAt: time.Now().UTC()}
}

func executeProxyClaim(supervisor *attemptSupervisor, journal *runner.AttemptJournal, reporter *attemptReporter, server, token string, claim domain.ClaimedRun, config ProxyConfig) error {
	if domain.ValidateProxyApplyTask(claim.Run) != nil || !config.Enabled() || claim.Lease.RunID != claim.Run.ID || claim.PrimitivePlan.RunID != claim.Run.ID || claim.PrimitivePlan.Process != nil || claim.PrimitivePlan.Checkout != nil || len(claim.PrimitivePlan.Secrets) != 0 || len(claim.PrimitivePlan.Artifacts) != 0 {
		return errors.New("invalid or locally disabled typed proxy task")
	}
	a := hostingAuthority(claim.Run.ID, claim.Run.RunSpec.ProxyOperationID, claim.Lease)
	client := runnerclient.New(server, token)
	var op domain.HostingProxyOperation
	if err := retryAttemptRequest(supervisor, func(ctx context.Context) error { var err error; op, err = client.ProxyPlan(ctx, a); return err }); err != nil {
		return err
	}
	if err := proxyResponseMatches(op, a, claim.Lease.RunnerID, nil); err != nil {
		return err
	}
	if op.RestoreOwnershipHeld || op.MutationHold {
		return errors.New("proxy ownership is held")
	}
	engine, local, preparation, err := prepareProxyEngine(supervisor.Context(), config, *op.Plan, op.Preparation)
	if err != nil {
		return err // No admission or engine effect has occurred.
	}
	if op.Preparation == nil {
		entry := proxyJournalEntry(supervisor, claim.Lease, a, *op.Plan, preparation, "prepared")
		request := domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: entry.ID, HostingProxyPreparation: preparation}
		entry.Prepared = &request
		if _, err = journal.AppendProxy(entry); err != nil {
			return err
		}
		if err = retryAttemptRequest(supervisor, func(ctx context.Context) error {
			var err error
			op, err = client.ProxyPrepared(ctx, request)
			return err
		}); err != nil {
			return err
		}
		if err = proxyPreparedAcknowledged(op, entry, claim.Lease.RunnerID); err != nil {
			return err
		}
		if err = journal.AckProxy(entry.ID); err != nil {
			return err
		}
	}
	intent := proxyJournalEntry(supervisor, claim.Lease, a, *op.Plan, preparation, "apply_intent")
	if _, err = journal.AppendProxy(intent); err != nil {
		return err
	}
	var admitted domain.HostingProxyOperation
	if err = retryAttemptRequest(supervisor, func(ctx context.Context) error {
		var err error
		admitted, err = client.ProxyApplyBegin(ctx, a)
		return err
	}); err != nil {
		return err
	}
	if err = proxyResponseMatches(admitted, a, claim.Lease.RunnerID, op.Plan); err != nil {
		return err
	}
	if !admitted.MutationHold || admitted.RestoreOwnershipHeld || admitted.State != "applying" || admitted.Preparation == nil || *admitted.Preparation != preparation {
		return errors.New("proxy admission did not acknowledge exact preparation and hold")
	}
	var result hostingproxy.Result
	var engineErr error
	if op.Plan.PreviousGeneration == 0 {
		result, engineErr = engine.Install(supervisor.Context(), local)
	} else {
		result, engineErr = engine.Apply(supervisor.Context(), local)
	}
	result = waitProxyTLS(supervisor.Context(), engine, local, result)
	evidence := proxyResultEvidence(result, *op.Plan, preparation)
	entry := proxyJournalEntry(supervisor, claim.Lease, a, *op.Plan, preparation, "result")
	request := domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: entry.ID, Result: evidence}
	entry.Result = &request
	if _, err = journal.AppendProxy(entry); err != nil {
		return err
	}
	if err = reporter.WaitEmpty(supervisor.Context()); err != nil {
		return err
	}
	var finished domain.HostingProxyOperation
	if err = retryAttemptRequest(supervisor, func(ctx context.Context) error {
		var err error
		finished, err = client.ProxyComplete(ctx, request)
		return err
	}); err != nil {
		if supervisor.Context().Err() == nil {
			return err
		}
		// A committed completion can make the lease watcher cancel this
		// supervisor before its response arrives. Only an exact existing
		// receipt may cross that boundary; never send a fresh terminal report.
		var replayErr error
		finished, replayErr = replayProxyResult(client, entry, claim.Lease.RunnerID, false)
		if replayErr != nil {
			return errors.Join(err, replayErr, engineErr)
		}
	}
	if err = proxyResultAcknowledged(finished, entry, claim.Lease.RunnerID); err != nil {
		return errors.Join(err, engineErr)
	}
	if err = verifyProxyLocalReceipt(engine, local, evidence); err != nil {
		return err
	}
	if err = journal.AckProxy(entry.ID); err != nil {
		return err
	}
	if err = journal.AckProxy(intent.ID); err != nil {
		return err
	}
	if evidence.Outcome != "applied" {
		return errors.New("proxy candidate was refused or rolled back; previous generation retained")
	}
	return nil
}

func waitProxyTLS(ctx context.Context, engine managedProxyEngine, plan hostingproxy.Plan, result hostingproxy.Result) hostingproxy.Result {
	if result.Outcome != hostingproxy.Applied {
		return result
	}
	wait, stop := context.WithTimeout(ctx, time.Minute)
	defer stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		ready := true
		for _, route := range result.Routes {
			ready = ready && route.HTTPSReady
		}
		if ready {
			return result
		}
		select {
		case <-wait.Done():
			return result // Routing applied remains distinct from TLS pending.
		case <-ticker.C:
			if observed, err := engine.Observe(wait, plan); err == nil && observed.Outcome == hostingproxy.Applied {
				result = observed
			}
		}
	}
}

func proxyResultEvidence(result hostingproxy.Result, plan domain.HostingProxyPlan, preparation domain.HostingProxyPreparation) domain.HostingProxyResult {
	evidence := domain.HostingProxyResult{Outcome: string(result.Outcome), LoadedGeneration: int64(result.LoadedGeneration), PlanHash: plan.Hash, EngineHash: preparation.EngineHash, DaemonID: preparation.DaemonID, ContainerID: result.ContainerID}
	if result.Hash != preparation.EngineHash || result.DesiredGeneration != uint64(plan.Snapshot.Generation) || (result.Outcome != hostingproxy.Applied && result.Outcome != hostingproxy.RolledBack && result.Outcome != hostingproxy.Refused && result.Outcome != hostingproxy.Uncertain) {
		evidence.Outcome = "uncertain"
		evidence.LoadedGeneration = 0
		evidence.ContainerID = ""
		return evidence
	}
	if result.Outcome == hostingproxy.Refused {
		evidence.LoadedGeneration = plan.PreviousGeneration
	}
	for _, route := range result.Routes {
		certificate := route.Certificate
		evidence.Routes = append(evidence.Routes, domain.HostingProxyRouteObservation{RouteID: route.RouteID, HTTPReady: route.HTTPReady, HTTPSReady: route.HTTPSReady, Certificate: domain.HostingProxyCertificate{Hostname: certificate.Hostname, Issuer: certificate.Issuer, Fingerprint: certificate.Fingerprint, ExpiresAt: certificate.ExpiresAt, Status: certificate.Status}})
	}
	return evidence
}

func proxyPreparedAcknowledged(op domain.HostingProxyOperation, entry runner.JournalProxy, runnerID string) error {
	if entry.Prepared == nil || op.RestoreOwnershipHeld || proxyResponseMatches(op, entry.Prepared.HostingAuthority, runnerID, &entry.Plan) != nil || op.PreparedReceipt == nil || op.PreparedReceipt.ID != entry.ID || op.PreparedReceipt.BodyHash != domain.HostingDocumentHash(entry.Preparation) || op.Preparation == nil || *op.Preparation != entry.Preparation {
		return errors.New("proxy preparation acknowledgement changed")
	}
	return nil
}

func proxyResultAcknowledged(op domain.HostingProxyOperation, entry runner.JournalProxy, runnerID string) error {
	if entry.Result == nil || proxyResponseMatches(op, entry.Result.HostingAuthority, runnerID, &entry.Plan) != nil || op.Preparation == nil || *op.Preparation != entry.Preparation || op.CompleteReceipt == nil || op.CompleteReceipt.ID != entry.ID || op.CompleteReceipt.BodyHash != domain.HostingDocumentHash(entry.Result.Result) || op.Result == nil || !reflect.DeepEqual(*op.Result, entry.Result.Result) {
		return errors.New("proxy result acknowledgement changed")
	}
	if op.MutationHold || op.RestoreOwnershipHeld || op.State == "uncertain" || op.State != entry.Result.Result.Outcome {
		return errors.New("proxy result retains unresolved or restored ownership")
	}
	return nil
}

func verifyProxyLocalReceipt(engine managedProxyEngine, plan hostingproxy.Plan, result domain.HostingProxyResult) error {
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	return engine.VerifyManagedReceipt(ctx, plan, hostingproxy.Outcome(result.Outcome), uint64(result.LoadedGeneration))
}

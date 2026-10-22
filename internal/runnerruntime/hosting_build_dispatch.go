package runnerruntime

import (
	"context"
	"errors"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func isBuildClaim(claim domain.ClaimedRun) bool {
	return claim.Run.RunSpec.Type == domain.HostingBuildTaskType || claim.Run.RunSpec.HostingBuildID != ""
}
func validateBuildClaim(claim domain.ClaimedRun, runnerID string) error {
	if domain.ValidateHostingBuildTask(claim.Run) != nil || claim.PrimitivePlan.RunID != claim.Run.ID || claim.PrimitivePlan.Process != nil || claim.PrimitivePlan.Checkout != nil || len(claim.PrimitivePlan.Secrets) != 0 || len(claim.PrimitivePlan.Artifacts) != 0 || claim.Lease.RunID != claim.Run.ID || claim.Lease.RunnerID != runnerID || claim.Lease.Status != "active" || claim.Lease.CreatedAt.IsZero() || !claim.Lease.ExpiresAt.After(time.Now()) {
		return errors.New("invalid canonical build claim or target")
	}
	return hostingbuildprotocol.ValidateAuthority(buildAuthority(journalAttemptIdentity(claim.Run.ID, claim.Lease, nil), claim.Run.RunSpec.HostingBuildID))
}
func exactBuildLease(expected domain.RunLease) func(domain.RunLease) error {
	return func(got domain.RunLease) error {
		if got.ID != expected.ID || got.RunID != expected.RunID || got.RunnerID != expected.RunnerID || got.Attempt != expected.Attempt || got.Fence != expected.Fence || !got.CreatedAt.Equal(expected.CreatedAt) || got.Status != "active" || !got.ExpiresAt.After(time.Now()) {
			return errors.New("build lease authority changed")
		}
		return nil
	}
}
func validateBuildRuntimeConfig(c Config) error {
	if c.Build == nil {
		return nil
	}
	if !c.Execute || c.CompleteStatus != "" || c.Build.Target.RunnerID != c.RunnerID {
		return errors.New("build execution requires exact authenticated runner identity and execution mode")
	}
	if c.Build.PrivateGit != nil {
		if err := runnerclient.ValidateSourceCredentialServer(c.Server, runnerclient.SourceCredentialTransport{TrustedLoopbackHTTP: c.Build.PrivateGit.TrustedLoopbackHTTP}); err != nil {
			return err
		}
	}
	return c.Build.Validate()
}
func reconcileRuntimeBuilds(ctx context.Context, c Config, journal *runner.AttemptJournal) (*BuildExecutor, error) {
	if c.Build == nil {
		if snapshot := journal.Snapshot(); len(snapshot.Build)+len(snapshot.BuildLogs)+len(snapshot.BuildLogCursors) > 0 {
			return nil, errors.New("build journal requires explicit build configuration")
		}
		return nil, nil
	}
	executor, err := NewBuildExecutor(*c.Build, runnerclient.New(c.Server, c.Token), journal)
	if err != nil {
		return nil, err
	}
	if err = executor.setBuildLogSecrets(c.Token); err != nil {
		return nil, err
	}
	if err = executor.Reconcile(ctx); err != nil {
		return nil, err
	}
	return executor, nil
}

// executeBuildClaim branches before generic reporters, process plans and terminal
// completion. Only the controller's build completion may settle its task/lease.
func executeBuildClaim(parent context.Context, server, token string, claim domain.ClaimedRun, runnerID string, poll time.Duration, executor *BuildExecutor, counters *runnerOperationalCounters) error {
	if executor == nil {
		return errors.New("hosting_build_v1 requires explicit build execution configuration")
	}
	if executor.config.Target.RunnerID != runnerID {
		return errors.New("build executor runner identity mismatch")
	}
	if err := validateBuildClaim(claim, runnerID); err != nil {
		return err
	}
	supervisor := newAttemptSupervisorFrom(parent, claim.Lease, counters)
	defer supervisor.Close()
	request, cancel, err := supervisor.RequestContext()
	if err != nil {
		return err
	}
	renewed, err := runnerclient.New(server, token).Renew(request, runnerclient.Authority{LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence})
	cancel()
	validator := exactBuildLease(claim.Lease)
	if err != nil {
		return errors.New("confirm build lease authority failed")
	}
	if err = validator(renewed); err != nil {
		return err
	}
	supervisor.Update(renewed)
	operation, stop := context.WithTimeout(supervisor.Context(), executor.config.Policy.Limits.Deadline)
	defer stop()
	every := time.Until(renewed.ExpiresAt) / 3
	renewer := startLeaseRenewer(operation, supervisor, server, token, claim.Lease, every, validator)
	defer renewer.Stop()
	watcher := startLeaseWatcher(operation, server, token, claim.Lease, poll, supervisor.cancel, validator)
	defer watcher.Stop()
	a := buildAuthority(journalAttemptIdentity(claim.Run.ID, claim.Lease, nil), claim.Run.RunSpec.HostingBuildID)
	return executor.Execute(operation, a, journalAttemptIdentity(claim.Run.ID, claim.Lease, nil))
}

func validateRuntimeIdentity(parent context.Context, c Config) error {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	identity, err := runnerclient.New(c.Server, c.Token).RuntimeExecutionIdentity(ctx, c.Proxy.Enabled(), c.Build != nil, false, c.Build != nil && c.Build.PrivateGit != nil)
	if err != nil || identity != c.RunnerID {
		return errors.New("authenticated runner capabilities do not match explicit runtime configuration")
	}
	return nil
}

func replayParent(parents []context.Context) context.Context {
	if len(parents) > 0 && parents[0] != nil {
		return parents[0]
	}
	return context.Background()
}

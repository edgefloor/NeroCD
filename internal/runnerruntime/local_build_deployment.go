package runnerruntime

import (
	"context"
	"errors"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func hostingOperationUsesLocalBuild(op domain.HostingOperation) bool {
	return op.Snapshot.LocalBuild != (domain.HostingLocalBuildSource{}) || op.Release != nil && op.Release.LocalBuild != (domain.HostingBuildArtifact{}) || runner.HostingJournalUsesLocalBuild(runner.JournalHosting{Plan: op.Plan, CleanupPlan: op.CleanupPlan})
}

func hostingPreparedSnapshotMatches(before, after domain.HostingSnapshot) bool {
	// Preparation resolves an unallocated port through the existing server-side
	// allocator. No source, artifact or placement field may change with it.
	if before.HostPort == 0 {
		before.HostPort = after.HostPort
	}
	return before == after && after.HostPort >= 1024 && after.HostPort <= 65535
}

func validateHostingPreparedResponse(op domain.HostingOperation, a domain.HostingAuthority, before domain.HostingSnapshot, release domain.HostingRelease) error {
	// TaskMode is an authority-bearing plan-read annotation, not a field on the
	// preparation receipt response. Validate its immutable receipt and plan here.
	if op.ID != a.OperationID || op.RunID != a.RunID || op.BindingID != before.BindingID || !hostingPreparedSnapshotMatches(before, op.Snapshot) || op.Release == nil || *op.Release != release || domain.ValidateHostingSnapshotRelease(op.Snapshot, release) != nil || op.Plan == nil || op.Plan.Snapshot != op.Snapshot || op.Plan.Release != release || verifyImagePlan(*op.Plan) != nil {
		return errors.New("hosting preparation immutable evidence changed")
	}
	return nil
}

func validateLocalBuildClaim(claim domain.ClaimedRun) error {
	if domain.ValidateLocalBuildDeploymentTask(claim.Run) != nil || claim.PrimitivePlan.RunID != claim.Run.ID || claim.PrimitivePlan.Process != nil || claim.PrimitivePlan.Checkout != nil || len(claim.PrimitivePlan.Secrets) != 0 || len(claim.PrimitivePlan.Artifacts) != 0 || claim.Lease.RunID != claim.Run.ID || claim.Lease.ID == "" || claim.Lease.RunnerID == "" || claim.Lease.Attempt < 1 || claim.Lease.Fence == "" || claim.Lease.CreatedAt.IsZero() || exactBuildLease(claim.Lease)(claim.Lease) != nil {
		return errors.New("invalid canonical local artifact claim")
	}
	return nil
}

func validateLocalBuildReplayOperation(entry runner.JournalHosting, op domain.HostingOperation, enabled bool) error {
	local := runner.HostingJournalUsesLocalBuild(entry)
	if !local && !hostingOperationUsesLocalBuild(op) {
		return nil
	}
	if !enabled || !hostingOperationUsesLocalBuild(op) || op.ID != entry.OperationID {
		return errors.New("local artifact replay target or capability changed")
	}
	if entry.Prepared != nil && domain.ValidateHostingSnapshotRelease(op.Snapshot, entry.Prepared.Release) != nil {
		return errors.New("local artifact replay preparation changed")
	}
	if entry.Result != nil && (op.Result == nil || *op.Result != entry.Result.Result) {
		return errors.New("local artifact replay result changed")
	}
	return nil
}

func validateLocalBuildRuntimeIdentity(parent context.Context, config Config) error {
	if !config.LocalBuildDeploy {
		return validateRuntimeIdentity(parent, config)
	}
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	id, err := runnerclient.New(config.Server, config.Token).RuntimeExecutionIdentity(ctx, config.Proxy.Enabled(), config.Build != nil, true, config.Build != nil && config.Build.PrivateGit != nil)
	if err != nil || id != config.RunnerID {
		return errors.New("authenticated execution capabilities do not match explicit local configuration")
	}
	return nil
}

func validateLocalBuildJournalConfiguration(journal *runner.AttemptJournal, enabled bool) error {
	if enabled {
		return nil
	}
	for _, entry := range journal.Snapshot().Hosting {
		if runner.HostingJournalUsesLocalBuild(entry) {
			return errors.New("local artifact journal requires explicit local build deployment configuration")
		}
	}
	return nil
}

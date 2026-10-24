//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

type databaseMaintenanceExecutionEngine interface {
	ApplyDatabaseMemberMaintenance(context.Context, string, databaseMemberMaintenanceInput, hostingOwnedVolumeRequest, hostingOwnedVolumeEvidence, *DatabaseCredentials, DatabaseEngineConfig, func(context.Context) error) domain.HostingResult
	ObserveDatabaseMemberMaintenance(context.Context, domain.HostingOperation, databaseMemberMaintenanceInput, *DatabaseCredentials, DatabaseEngineConfig) (domain.HostingObservation, error)
}

func databaseMaintenanceEngineConfigured(op domain.HostingOperation, config DatabaseExecutionConfig, e databaseExecutionEngine) bool {
	if op.DatabaseMaintenance == nil {
		return true
	}
	_, ok := e.(databaseMaintenanceExecutionEngine)
	return config.MembershipMaintenance && ok
}

func databaseMaintenanceProofMatches(a, b *domain.HostingDatabaseMaintenanceProof) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Validate() == nil && b.Validate() == nil && a.Hash == b.Hash
}

func databaseOperationProofMatches(a, b domain.HostingOperation) bool {
	return databaseUpgradeProofMatches(a.DatabaseUpgrade, b.DatabaseUpgrade) && databaseMaintenanceProofMatches(a.DatabaseMaintenance, b.DatabaseMaintenance)
}

func databaseJournalProofMatches(a runner.JournalHosting, b domain.HostingOperation) bool {
	return databaseUpgradeProofMatches(a.DatabaseUpgrade, b.DatabaseUpgrade) && databaseMaintenanceProofMatches(a.DatabaseMaintenance, b.DatabaseMaintenance)
}

func databaseOperationSidecarValid(op domain.HostingOperation) bool {
	if op.DatabaseMaintenance != nil {
		return domain.ValidateHostingDatabaseMaintenanceOperation(op) == nil
	}
	return domain.ValidateHostingDatabaseUpgradeOperation(op) == nil
}

func databaseOperationMaintenanceInput(op domain.HostingOperation) (databaseMemberMaintenanceInput, error) {
	if op.DatabaseMaintenance == nil || op.Plan == nil || domain.ValidateHostingDatabaseMaintenanceOperation(op) != nil {
		return databaseMemberMaintenanceInput{}, domain.ErrInvalidHostingDatabaseMaintenance
	}
	proof, err := domain.HostingDatabaseMaintenancePlanInput(*op.DatabaseMaintenance)
	in := databaseMemberMaintenanceInput{Plan: *op.Plan, PreviousPlan: op.DatabaseMaintenance.PreviousPlan, Proof: proof}
	if err != nil || in.validate() != nil {
		return databaseMemberMaintenanceInput{}, domain.ErrInvalidHostingDatabaseMaintenance
	}
	return in, nil
}

func fetchDatabaseMaintenanceCredentials(s *attemptSupervisor, c runnerclient.Client, runnerID string, request domain.HostingDatabaseCredentialRequest, op domain.HostingOperation, config DatabaseExecutionConfig, deadline time.Time) (*DatabaseCredentials, error) {
	in, err := databaseOperationMaintenanceInput(op)
	if err != nil || !config.MembershipMaintenance {
		return nil, runnerclient.ErrDatabaseCredentials
	}
	ctx, cancel := context.WithDeadline(s.Context(), deadline)
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		protected, err := c.HostingDatabaseMaintenanceCredentials(ctx, runnerID, request, op, runnerclient.RuntimeEnvironmentTransport{TrustedLoopbackHTTP: config.TrustedLoopbackHTTP})
		if err == nil {
			credentials, err := convertValidatedDatabaseCredentials(protected, request, runnerID, *op.Plan, deadline, func(_ domain.HostingApplyPlan, values []DatabaseCredentialValue, until time.Time) (*DatabaseCredentials, error) {
				return newDatabaseMemberMaintenanceCredentials(in, values, until)
			})
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

func applyDatabaseMaintenance(ctx context.Context, e databaseExecutionEngine, op domain.HostingOperation, volume hostingOwnedVolumeRequest, evidence hostingOwnedVolumeEvidence, credentials *DatabaseCredentials, ec DatabaseEngineConfig, before func(context.Context) error) domain.HostingResult {
	in, err := databaseOperationMaintenanceInput(op)
	maintenance, ok := e.(databaseMaintenanceExecutionEngine)
	if err != nil || !ok || volume.CreatePermitted {
		return domain.HostingResult{Outcome: "failed", Uncertain: true}
	}
	return maintenance.ApplyDatabaseMemberMaintenance(ctx, op.ID, in, volume, evidence, credentials, ec, before)
}

func observeDatabaseMaintenance(ctx context.Context, e databaseExecutionEngine, op domain.HostingOperation, credentials *DatabaseCredentials, ec DatabaseEngineConfig) (domain.HostingObservation, error) {
	in, err := databaseOperationMaintenanceInput(op)
	maintenance, ok := e.(databaseMaintenanceExecutionEngine)
	if err != nil || !ok || op.Result == nil || op.Result.ContainerID == "" {
		return domain.HostingObservation{}, errors.New("database maintenance durable candidate unavailable")
	}
	return maintenance.ObserveDatabaseMemberMaintenance(ctx, op, in, credentials, ec)
}

// Terminal receipt replay needs no daemon or fresh mutation authority. Uncertain
// outcomes remain in the ordinary database recovery path with their hold intact.
func replaySavedDatabaseMaintenanceCompletions(ctx context.Context, config Config, j *runner.AttemptJournal) error {
	return replaySavedDatabaseMaintenanceSnapshot(ctx, config, j, j.Snapshot())
}

func replaySavedDatabaseMaintenanceSnapshot(ctx context.Context, config Config, j *runner.AttemptJournal, snapshot runner.JournalSnapshot) error {
	c := runnerclient.New(config.Server, config.Token)
	if config.Database != nil {
		ctx = runnerclient.WithDatabaseTransport(ctx, runnerclient.DatabaseTransport{TrustedLoopbackHTTP: config.Database.TrustedLoopbackHTTP})
	}
	for _, entry := range snapshot.Hosting {
		if entry.DatabaseMaintenance == nil || entry.Result == nil {
			continue
		}
		if config.Database == nil || !config.Database.MembershipMaintenance || len(snapshot.DatabaseNetworkCapture) != 0 {
			return errors.New("database maintenance completion requires unmixed explicit recovery configuration")
		}
		a := storageEntryAuthority(entry)
		if entry.DatabaseMaintenance.Validate() != nil || entry.Result.HostingAuthority != a || entry.Result.ReceiptID != entry.ID {
			return errors.New("database maintenance saved completion changed")
		}
		result := entry.Result.Result
		if result.Uncertain {
			continue
		}
		if result.Outcome == "succeeded" && (!result.Ready || result.Restored) {
			return errors.New("database maintenance completion lacks settled result")
		}
		var plan *domain.HostingApplyPlan
		for _, other := range snapshot.Hosting {
			if storageEntryAuthority(other) != a {
				continue
			}
			if !other.Database || other.DatabaseRef != entry.DatabaseRef || other.StorageRef != entry.StorageRef || !databaseMaintenanceProofMatches(other.DatabaseMaintenance, entry.DatabaseMaintenance) || other.DatabaseUpgrade != nil {
				return errors.New("database maintenance saved completion family changed")
			}
			if other.Plan != nil {
				if plan != nil && domain.HostingDocumentHash(plan) != domain.HostingDocumentHash(other.Plan) {
					return errors.New("database maintenance saved plan changed")
				}
				plan = other.Plan
			}
		}
		if result.Ready && (plan == nil || plan.PreviousHealthy == nil || result.PlanHash != plan.Hash || result.ImageID != plan.Release.ImageID || result.DaemonID != plan.Release.DaemonID || result.PreviousContainerID != plan.PreviousHealthy.ContainerID || !dockerObjectID.MatchString(result.ContainerID)) {
			return errors.New("database maintenance result changed original candidate plan")
		}
		if err := replayHostingEvents(config.Server, config.Token, j, entry.Attempt, ctx); err != nil {
			return err
		}
		request, cancel := context.WithTimeout(ctx, 5*time.Second)
		op, err := c.HostingComplete(request, *entry.Result)
		cancel()
		if err != nil {
			return err
		}
		if !databaseJournalProofMatches(entry, op) || !databaseOperationScope(op, a, config.RunnerID, "deploy", entry.DatabaseRef) || op.Snapshot.Storage != entry.StorageRef || op.Result == nil || *op.Result != result || op.CompleteReceipt == nil || op.CompleteReceipt.ID != entry.Result.ReceiptID || op.CompleteReceipt.BodyHash != domain.HostingDocumentHash(result) || !databaseSettled(op, a, config.RunnerID, entry.DatabaseRef) || domain.HostingDocumentHash(op.Plan) != domain.HostingDocumentHash(plan) {
			return errors.New("database maintenance completion replay changed exact settlement")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := j.AckHostingConnection(a); err != nil {
			return err
		}
	}
	return nil
}

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

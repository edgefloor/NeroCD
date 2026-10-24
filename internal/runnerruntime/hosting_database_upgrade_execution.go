//go:build unix

package runnerruntime

import (
	"context"
	"errors"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runnerclient"
)

// Upgrade effects use a separate consumer interface. The v1 engine contract
// continues to reject network plans and cannot silently adopt this family.
type databaseUpgradeExecutionEngine interface {
	ApplyDatabaseNetworkUpgrade(context.Context, string, databaseNetworkUpgradeInput, hostingOwnedVolumeRequest, hostingOwnedVolumeEvidence, *DatabaseCredentials, DatabaseEngineConfig, func(context.Context) error) domain.HostingResult
	ObserveDatabaseNetworkUpgrade(context.Context, domain.HostingOperation, databaseNetworkUpgradeInput, *DatabaseCredentials, DatabaseEngineConfig) (domain.HostingObservation, error)
}

func databaseUpgradeEngineAvailable(e databaseExecutionEngine) bool {
	_, ok := e.(databaseUpgradeExecutionEngine)
	return ok
}

func databaseUpgradeProofMatches(a, b *domain.HostingDatabaseUpgradeProof) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Validate() == nil && b.Validate() == nil && domain.HostingDocumentHash(a) == domain.HostingDocumentHash(b)
}

func databasePlanValid(op domain.HostingOperation) bool {
	if op.DatabaseMaintenance != nil {
		return domain.ValidateHostingDatabaseMaintenanceOperation(op) == nil
	}
	if op.DatabaseUpgrade != nil {
		return domain.ValidateHostingDatabaseUpgradeOperation(op) == nil
	}
	return domain.ValidateHostingDatabasePlan(*op.Plan) == nil
}

func databaseOperationUpgradeInput(op domain.HostingOperation) (databaseNetworkUpgradeInput, error) {
	if op.DatabaseUpgrade == nil || op.Plan == nil || domain.ValidateHostingDatabaseUpgradeOperation(op) != nil {
		return databaseNetworkUpgradeInput{}, domain.ErrInvalidHostingDatabaseUpgrade
	}
	proof, err := domain.HostingDatabaseUpgradePlanInput(*op.DatabaseUpgrade)
	in := databaseNetworkUpgradeInput{Plan: *op.Plan, PreviousPlan: op.DatabaseUpgrade.PreviousPlan, Proof: proof}
	if err != nil || in.validate() != nil {
		return databaseNetworkUpgradeInput{}, domain.ErrInvalidHostingDatabaseUpgrade
	}
	return in, nil
}

func prepareDatabaseOperation(s *attemptSupervisor, c runnerclient.Client, e databaseExecutionEngine, a domain.HostingAuthority, op domain.HostingOperation, ec DatabaseEngineConfig) (domain.HostingRelease, error) {
	before := databaseCurrentAuthority(s, c, a, op, "preparing")
	if op.DatabaseMaintenance != nil {
		if _, ok := e.(databaseMaintenanceExecutionEngine); !ok || domain.ValidateHostingDatabaseMaintenanceOperation(op) != nil || before(s.Context()) != nil {
			return domain.HostingRelease{}, domain.ErrInvalidHostingDatabaseMaintenance
		}
		return op.DatabaseMaintenance.PreviousPlan.Release, nil
	}
	if op.DatabaseUpgrade == nil {
		return e.PrepareDatabase(s.Context(), op.Snapshot, ec, before)
	}
	if !databaseUpgradeEngineAvailable(e) || domain.ValidateHostingDatabaseUpgradeOperation(op) != nil {
		return domain.HostingRelease{}, domain.ErrInvalidHostingDatabaseUpgrade
	}
	if err := before(s.Context()); err != nil {
		return domain.HostingRelease{}, err
	}
	return op.DatabaseUpgrade.PreviousPlan.Release, nil
}

// Upgrade admission must name the already materialized volume, including the
// original creation receipt. Contradictory create permission is an error even
// when a later engine check would refuse to exercise it.
func databaseUpgradeVolumeMatches(op domain.HostingOperation, ack domain.HostingStorageCreateAck) bool {
	if op.DatabaseMaintenance != nil {
		proof := op.DatabaseMaintenance
		return !ack.CreatePermitted && ack.Materialization.State == "materialized" && domain.HostingDocumentHash(ack.Storage) == domain.HostingDocumentHash(proof.CapturePlan.Storage) && domain.HostingDocumentHash(ack.Materialization) == domain.HostingDocumentHash(proof.CapturePlan.Materialization)
	}
	if op.DatabaseUpgrade == nil {
		return true
	}
	proof := op.DatabaseUpgrade
	return !ack.CreatePermitted && ack.Materialization.State == "materialized" && domain.HostingDocumentHash(ack.Storage) == domain.HostingDocumentHash(proof.CapturePlan.Storage) && domain.HostingDocumentHash(ack.Materialization) == domain.HostingDocumentHash(proof.CapturePlan.Materialization)
}

func applyDatabaseOperation(ctx context.Context, e databaseExecutionEngine, op domain.HostingOperation, volume hostingOwnedVolumeRequest, evidence hostingOwnedVolumeEvidence, credentials *DatabaseCredentials, ec DatabaseEngineConfig, before func(context.Context) error) domain.HostingResult {
	if op.DatabaseMaintenance != nil {
		return applyDatabaseMaintenance(ctx, e, op, volume, evidence, credentials, ec, before)
	}
	if op.DatabaseUpgrade == nil {
		return e.ApplyDatabase(ctx, op.ID, *op.Plan, volume, evidence, credentials, ec, before)
	}
	in, err := databaseOperationUpgradeInput(op)
	upgrade, ok := e.(databaseUpgradeExecutionEngine)
	if err != nil || !ok || volume.CreatePermitted {
		return domain.HostingResult{Outcome: "failed", Uncertain: true}
	}
	return upgrade.ApplyDatabaseNetworkUpgrade(ctx, op.ID, in, volume, evidence, credentials, ec, before)
}

func observeDatabaseOperation(ctx context.Context, e databaseExecutionEngine, op domain.HostingOperation, credentials *DatabaseCredentials, ec DatabaseEngineConfig) (domain.HostingObservation, error) {
	if op.DatabaseMaintenance != nil {
		return observeDatabaseMaintenance(ctx, e, op, credentials, ec)
	}
	if op.DatabaseUpgrade == nil {
		return e.ObserveDatabase(ctx, op, credentials, ec)
	}
	in, err := databaseOperationUpgradeInput(op)
	upgrade, ok := e.(databaseUpgradeExecutionEngine)
	if err != nil || !ok || op.Result == nil || op.Result.ContainerID == "" {
		return domain.HostingObservation{}, errors.New("database upgrade durable candidate unavailable")
	}
	return upgrade.ObserveDatabaseNetworkUpgrade(ctx, op, in, credentials, ec)
}

func fetchDatabaseOperationCredentials(s *attemptSupervisor, c runnerclient.Client, runnerID string, request domain.HostingDatabaseCredentialRequest, op domain.HostingOperation, config DatabaseExecutionConfig, deadline time.Time) (*DatabaseCredentials, error) {
	if op.DatabaseMaintenance != nil {
		return fetchDatabaseMaintenanceCredentials(s, c, runnerID, request, op, config, deadline)
	}
	if op.DatabaseUpgrade == nil {
		return fetchDatabaseCredentials(s, c, runnerID, request, *op.Plan, config, deadline)
	}
	in, err := databaseOperationUpgradeInput(op)
	if err != nil || !config.NetworkUpgrade {
		return nil, runnerclient.ErrDatabaseCredentials
	}
	ctx, cancel := context.WithDeadline(s.Context(), deadline)
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		protected, err := c.HostingDatabaseUpgradeCredentials(ctx, runnerID, request, op, runnerclient.RuntimeEnvironmentTransport{TrustedLoopbackHTTP: config.TrustedLoopbackHTTP})
		if err == nil {
			credentials, err := convertValidatedDatabaseCredentials(protected, request, runnerID, *op.Plan, deadline, func(_ domain.HostingApplyPlan, values []DatabaseCredentialValue, until time.Time) (*DatabaseCredentials, error) {
				return newDatabaseNetworkUpgradeCredentials(in, values, until)
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

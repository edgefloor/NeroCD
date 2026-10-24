package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// lockHostingRuntimeConfiguration serializes admission with the first save as well
// as replacement, and never reads or expands managed secret values.
func lockHostingRuntimeConfiguration(ctx context.Context, tx pgx.Tx, project, resource string, expected int64) (domain.HostingRuntimeConfigurationRef, error) {
	var zero domain.HostingRuntimeConfigurationRef
	q := sqlcgen.New(tx)
	r, err := q.LockHostingRuntimeResource(ctx, resource)
	if err != nil {
		return zero, hostingError(err)
	}
	if r.ProjectID != project {
		return zero, ErrConflict
	}
	generation, err := q.LockHostingRuntimeConfiguration(ctx, resource)
	if errors.Is(err, pgx.ErrNoRows) {
		generation, err = 0, nil
	}
	if err != nil {
		return zero, err
	}
	if generation != expected {
		return zero, ErrConflict
	}
	if generation == 0 {
		return zero, nil
	}
	row, err := q.GetHostingRuntimeVersion(ctx, sqlcgen.GetHostingRuntimeVersionParams{ResourceID: resource, Generation: generation})
	if err != nil {
		return zero, hostingError(err)
	}
	var doc domain.HostingRuntimeDocument
	if row.ProjectID != project || json.Unmarshal(row.Body, &doc) != nil {
		return zero, ErrConflict
	}
	canonical, refs, err := domain.CanonicalHostingRuntimeDocument(doc.Entries)
	if err != nil || domain.HostingDocumentHash(canonical) != row.BodyHash {
		return zero, ErrConflict
	}
	if err = validateRuntimeReferences(ctx, q, project, refs); err != nil {
		return zero, err
	}
	return domain.HostingRuntimeConfigurationRef{Generation: generation, Hash: row.BodyHash}, nil
}

func requireHostingRuntimeAgent(ctx context.Context, tx pgx.Tx, snap domain.HostingSnapshot) error {
	if snap.RuntimeConfiguration == (domain.HostingRuntimeConfigurationRef{}) {
		return nil
	}
	if domain.ValidateHostingRuntimeConfigurationRef(snap) != nil {
		return ErrConflict
	}
	if err := requireHostingReleaseAgent(ctx, tx, snap.RunnerID); err != nil {
		return err
	}
	capable, err := sqlcgen.New(tx).HostingRunnerHasReleasePorts(ctx, sqlcgen.HostingRunnerHasReleasePortsParams{RunnerID: snap.RunnerID, Capability: domain.HostingRuntimeEnvironmentCapability})
	if err != nil {
		return err
	}
	if !capable {
		return ErrConflict
	}
	return nil
}

// Fresh admission checks are intentionally separate from historical completion,
// observation and receipt replay. Those paths must retain their immutable evidence.
func hostingFreshRuntimeGate(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	if op.CleanupPlan != nil {
		return nil
	}
	if connectionOperation(op) {
		if err := databaseConnectionAuthority(ctx, tx, op, false, false); err != nil {
			return err
		}
	} else {
		if err := databaseBindingPendingGate(ctx, tx, op.ResourceID); err != nil {
			return err
		}
	}
	if err := validateStorageContinuityFresh(ctx, tx, op); err != nil {
		return err
	}
	// Keep destination -> resource -> configuration order used by request
	// admission, including concurrent exact idempotency replays.
	if err := sqlcgen.New(tx).LockHostingExecutionDestination(ctx, op.Snapshot.DestinationID); err != nil {
		return err
	}
	ref, err := lockHostingRuntimeConfiguration(ctx, tx, op.ProjectID, op.ResourceID, op.Snapshot.RuntimeConfiguration.Generation)
	if err != nil {
		return err
	}
	if ref != op.Snapshot.RuntimeConfiguration {
		return ErrConflict
	}
	if err = hostingFreshStorageGate(ctx, tx, op); err != nil {
		return err
	}
	if err = runtimeHostingActorAuthority(ctx, tx, op); err != nil {
		return err
	}
	return requireHostingRuntimeAgent(ctx, tx, op.Snapshot)
}

func replayHostingApplyAdmission(ctx context.Context, tx pgx.Tx, a domain.HostingAuthority, op domain.HostingOperation) error {
	matches, err := sqlcgen.New(tx).HostingApplyAdmissionMatches(ctx, sqlcgen.HostingApplyAdmissionMatchesParams{
		OperationID: op.ID,
		RunID:       a.RunID,
		LeaseID:     a.LeaseID,
		Authority:   buildJSON(a),
		PlanHash:    op.Plan.Hash,
	})
	if err != nil {
		return err
	}
	if !matches {
		return ErrConflict
	}
	return nil
}

// Runtime admission retains current initiator authority across waits; historical
// evidence and receipt replay deliberately do not call this helper.
func runtimeHostingActorAuthority(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	if connectionOperation(op) {
		return authorizeBuildActor(ctx, tx, op.CreatedBy, op.ProjectID, domain.RoleOwner)
	}
	if op.CleanupPlan != nil || op.Snapshot.RuntimeConfiguration.Generation == 0 {
		return nil
	}
	err := authorizeBuildActor(ctx, tx, op.CreatedBy, op.ProjectID, "maintainer")
	if errors.Is(err, auth.ErrForbidden) || errors.Is(err, auth.ErrUnauthenticated) {
		return ErrConflict
	}
	return err
}

func finalHostingRuntimeAuthority(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, op domain.HostingOperation) error {
	if connectionOperation(op) {
		return finalDatabaseConnectionAuthority(ctx, tx, runner, a, op, false, false)
	}
	if op.CleanupPlan != nil || op.Snapshot.RuntimeConfiguration.Generation == 0 {
		return nil
	}
	if err := runtimeHostingActorAuthority(ctx, tx, op); err != nil {
		return err
	}
	// The target gate already holds the runner row FOR SHARE. Recheck after
	// that lock so a capability change during earlier placement waits is seen.
	if err := requireHostingRuntimeAgent(ctx, tx, op.Snapshot); err != nil {
		return err
	}
	return hostingAuthorityExists(ctx, tx, runner, a, true)
}

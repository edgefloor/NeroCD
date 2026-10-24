package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

func hostingDatabaseExecutionCompatible(ctx context.Context, q *sqlcgen.Queries) error {
	if err := hostingDatabaseCompatible(ctx, q); err != nil {
		return err
	}
	ok, err := q.HostingDatabaseExecutionSchemaCompatible(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil || !ok {
		return ErrHostingDatabaseUnavailable
	}
	return nil
}

func requireHostingDatabaseAgent(ctx context.Context, tx pgx.Tx, snap domain.HostingSnapshot) error {
	if domain.ValidateHostingDatabaseSnapshot(snap) != nil || snap.Database == (domain.HostingDatabaseExecutionRef{}) {
		return ErrConflict
	}
	for _, capability := range []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability} {
		ok, err := sqlcgen.New(tx).HostingRunnerHasReleasePorts(ctx, sqlcgen.HostingRunnerHasReleasePortsParams{RunnerID: snap.RunnerID, Capability: capability})
		if err != nil {
			return fmt.Errorf("read database runner capability: %w", err)
		}
		if !ok {
			return ErrConflict
		}
	}
	return nil
}

// Settlement has tentatively completed its lease; every other first mutation
// requires the same tuple to remain live after audit and lock waits.
func finalDatabaseAuthority(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, op domain.HostingOperation, observe, settled bool) error {
	if connectionOperation(op) {
		if err := finalDatabaseConnectionAuthority(ctx, tx, runner, a, op, observe, settled); err != nil {
			return err
		}
	}
	if op.DatabaseUpgrade != nil {
		if err := sqlcgen.New(tx).FlushDatabaseNetworkUpgrade(ctx); err != nil {
			return err
		}
	}
	if a.OperationID != op.ID || observe && a.RunID != op.ReconciliationRunID || !observe && a.RunID != op.RunID {
		return ErrConflict
	}
	mode := "deploy"
	if observe {
		mode = "observe"
	}
	if err := hostingRequireMode(ctx, tx, a, mode); err != nil {
		return err
	}
	if err := hostingOperationTargetGate(ctx, tx, op, observe); err != nil {
		return err
	}
	if err := hostingAuthorityExists(ctx, tx, runner, a, !settled); err != nil {
		return err
	}
	// The target gate may wait for the runner SHARE lock after reading the
	// actor and capabilities. Re-read both under the completed lock set.
	if err := databaseOperationAuthority(ctx, tx, op, observe); err != nil {
		return fmt.Errorf("recheck locked database authority: %w", err)
	}
	live, err := sqlcgen.New(tx).HostingLeaseUnexpired(ctx, a.LeaseID)
	if err != nil {
		return fmt.Errorf("check final database lease expiry: %w", err)
	}
	if !live {
		return ErrConflict
	}
	return nil
}

// databaseOperationAuthority checks current immutable metadata without reading
// secret values. Marker references pin the exact versions accepted at request.
func databaseOperationAuthority(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, observe bool) (err error) {
	return databaseOperationAuthorityMode(ctx, tx, op, observe, false)
}

func databaseOperationAuthorityMode(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, observe, historical bool) (err error) {
	defer func() {
		if errors.Is(err, auth.ErrForbidden) || errors.Is(err, auth.ErrUnauthenticated) {
			err = ErrConflict
		}
	}()
	if connectionOperation(op) {
		if err = databaseConnectionAuthority(ctx, tx, op, observe, historical); err != nil || op.ApplicationDatabase != nil {
			return err
		}
	}
	if op.Snapshot.Database == (domain.HostingDatabaseExecutionRef{}) {
		return nil
	}
	q := sqlcgen.New(tx)
	if err := hostingDatabaseExecutionCompatible(ctx, q); err != nil {
		return err
	}
	// Match request admission's destination -> resource order before taking
	// current metadata locks, including during runner claim and observation.
	if err = lockHostingResourceDestination(ctx, tx, op.ResourceID); err != nil {
		return err
	}
	target, err := q.LockHostingResourceTarget(ctx, op.ResourceID)
	if err != nil {
		return hostingError(err)
	}
	view, err := hostingDatabaseLoad(ctx, q, op.ResourceID)
	if err != nil {
		return err
	}
	marker, err := q.ReadHostingDatabaseDeployment(ctx, op.ID)
	if err != nil {
		return hostingError(err)
	}
	var reference domain.HostingDatabaseExecutionRef
	var credentials []domain.HostingDatabaseCredentialRef
	if json.Unmarshal(marker.Reference, &reference) != nil || json.Unmarshal(marker.Credentials, &credentials) != nil || reference != op.Snapshot.Database || !slices.Equal(credentials, view.Credentials) || marker.ResourceID != op.ResourceID || marker.BindingID != op.BindingID || marker.DestinationID != op.Snapshot.DestinationID || target.Kind != "database" || target.ProjectID != op.ProjectID || target.Generation != op.Snapshot.ResourceGeneration || target.BindingID != op.BindingID || target.DestinationID != op.Snapshot.DestinationID || target.ServerID != op.Snapshot.ServerID || target.RunnerID != op.Snapshot.RunnerID || target.RuntimeNamespace != op.Snapshot.RuntimeNamespace || view.StorageRef != op.Snapshot.Storage || view.Engine != reference.Engine || view.RecipeVersion != reference.RecipeVersion {
		return ErrConflict
	}
	hash, err := domain.HostingDatabaseConfigurationHash(op.Snapshot, credentials)
	if err != nil || hash != reference.ConfigurationHash {
		return ErrConflict
	}
	actor, role := op.CreatedBy, domain.RoleMaintainer
	if op.DatabaseMaintenance != nil {
		role = domain.RoleOwner
	}
	if observe {
		if op.ReconciliationRunID == "" {
			return ErrConflict
		}
		actor, err = q.HostingTaskActor(ctx, op.ReconciliationRunID)
		if err != nil {
			return hostingError(err)
		}
		role = domain.RoleOwner
	}
	if err = authorizeBuildActor(ctx, tx, actor, op.ProjectID, role); err != nil {
		return err
	}
	if err = requireHostingDatabaseAgent(ctx, tx, op.Snapshot); err != nil {
		return err
	}
	if !connectionOperation(op) && (!historical || op.DatabaseUpgrade != nil) {
		if err = databaseUpgradeAuthority(ctx, tx, op, observe || historical); err != nil {
			return err
		}
	}
	if op.DatabaseUpgrade != nil {
		// Capture, credential and materialization reads can wait after the
		// first actor check. Recheck the selected actor's advancing clock.
		return authorizeBuildActor(ctx, tx, actor, op.ProjectID, role)
	}
	return nil
}

// RequestHostingDatabaseDeployment commits one private operation and River job.
// Runtime support remains a separate qualification boundary.
func (s *PostgresStore) RequestHostingDatabaseDeployment(ctx context.Context, id, resource, actor string, in domain.HostingDatabaseDeploymentInput, audit domain.AuditEvent) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	if s.hostingJobs == nil {
		return zero, hostingjobs.ErrUnavailable
	}
	if in.Validate() != nil || domain.ValidateDatabaseDeploymentTask(domain.TaskRun{ID: "run_" + id, RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseDeploy, HostingOperationID: id, HostingMode: "deploy"}}) != nil {
		return zero, fmt.Errorf("invalid database deployment identity: %w", ErrConflict)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return zero, fmt.Errorf("begin database deployment: %w", err)
	}
	defer storageDeploymentRollback(ctx, tx)
	q := sqlcgen.New(tx)
	if err = hostingDatabaseExecutionCompatible(ctx, q); err != nil {
		return zero, err
	}
	if err = lockHostingResourceDestination(ctx, tx, resource); err != nil {
		return zero, err
	}
	target, err := q.LockHostingResourceTarget(ctx, resource)
	if err != nil {
		return zero, fmt.Errorf("lock database target: %w", hostingError(err))
	}
	if target.Kind != "database" {
		return zero, ErrConflict
	}
	if err = authorizeBuildActor(ctx, tx, actor, target.ProjectID, domain.RoleMaintainer); err != nil {
		return zero, err
	}
	view, err := hostingDatabaseLoad(ctx, q, resource)
	if err != nil {
		return zero, fmt.Errorf("read current database metadata: %w", err)
	}
	recipe, err := domain.HostingDatabaseRecipeFor(view.Engine, view.RecipeVersion)
	if err != nil {
		return zero, ErrConflict
	}
	snap := domain.HostingSnapshot{Database: domain.HostingDatabaseExecutionRef{Engine: view.Engine, RecipeVersion: view.RecipeVersion}, Storage: view.StorageRef, ResourceID: resource, ResourceGeneration: target.Generation, ImageReference: recipe.ImageReference, BindingID: target.BindingID, DestinationID: target.DestinationID, ServerID: target.ServerID, RunnerID: target.RunnerID, RuntimeNamespace: target.RuntimeNamespace, ContainerPort: recipe.ContainerPort, PolicyVersion: domain.HostingDatabasePolicyVersion}
	snap.Database.ConfigurationHash, err = domain.HostingDatabaseConfigurationHash(snap, view.Credentials)
	if err != nil {
		return zero, fmt.Errorf("compile database configuration reference: %w", ErrConflict)
	}
	if err = hostingTargetGate(ctx, tx, target.ProjectID, snap, true); err != nil {
		return zero, fmt.Errorf("admit database target: %w", err)
	}
	requestHash := domain.HostingDocumentHash(struct {
		Kind, Actor string
		Input       domain.HostingDatabaseDeploymentInput
	}{domain.RunTypeDatabaseDeploy, actor, in})
	existing, err := q.GetHostingIdempotency(ctx, sqlcgen.GetHostingIdempotencyParams{ResourceID: resource, IdempotencyKey: in.IdempotencyKey})
	if err == nil {
		if existing.RequestHash != requestHash {
			return zero, ErrConflict
		}
		op, readErr := hostingOperation(ctx, tx, existing.ID)
		if readErr != nil {
			return zero, readErr
		}
		if op.Snapshot != snap || op.CreatedBy != actor {
			return zero, ErrConflict
		}
		if err = databaseOperationAuthorityMode(ctx, tx, op, false, true); err != nil {
			return zero, err
		}
		return op, proxyCheckLegacyToken(ctx, tx, actor)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return zero, fmt.Errorf("read database deployment replay: %w", err)
	}
	if target.Generation != in.ExpectedGeneration {
		return zero, ErrConflict
	}
	if _, err = q.CreateHostingPortReservation(ctx, sqlcgen.CreateHostingPortReservationParams{BindingID: snap.BindingID, DestinationID: snap.DestinationID}); err != nil {
		return zero, fmt.Errorf("create private binding: %w", hostingError(err))
	}
	state, err := q.LockHostingPortReservation(ctx, snap.BindingID)
	if err != nil {
		return zero, fmt.Errorf("lock private database binding: %w", err)
	}
	busy, err := q.HostingBindingBusy(ctx, snap.BindingID)
	if err != nil {
		return zero, fmt.Errorf("read database active operation: %w", err)
	}
	if state.HostPort != 0 || !state.RestoreAdmitted || state.HoldOperationID != nil || busy {
		return zero, fmt.Errorf("database binding is busy or held: %w", ErrConflict)
	}
	if err = databaseUpgradeAuthority(ctx, tx, domain.HostingOperation{BindingID: snap.BindingID}, false); err != nil {
		return zero, err
	}
	if err = q.CreateHostingDatabaseDeployment(ctx, sqlcgen.CreateHostingDatabaseDeploymentParams{OperationID: id, ResourceID: resource, BindingID: snap.BindingID, DestinationID: snap.DestinationID, Reference: buildJSON(snap.Database), Credentials: buildJSON(view.Credentials)}); err != nil {
		return zero, fmt.Errorf("insert database family marker: %w", hostingError(err))
	}
	if _, err = q.CreateHostingOperation(ctx, sqlcgen.CreateHostingOperationParams{ID: id, ProjectID: target.ProjectID, ResourceID: resource, BindingID: snap.BindingID, IdempotencyKey: in.IdempotencyKey, RequestHash: requestHash, CreatedBy: actor}); err != nil {
		return zero, fmt.Errorf("insert database operation: %w", hostingError(err))
	}
	if _, err = q.CreateHostingSnapshot(ctx, sqlcgen.CreateHostingSnapshotParams{OperationID: id, Body: buildJSON(snap), BodyHash: domain.HostingDocumentHash(snap)}); err != nil {
		return zero, fmt.Errorf("insert database snapshot: %w", hostingError(err))
	}
	audit.ActorID, audit.Action, audit.TargetID = actor, "hosting.database_deployment_requested", id
	audit.Metadata = map[string]any{"project_id": target.ProjectID, "resource_id": resource, "binding_id": snap.BindingID, "configuration_hash": snap.Database.ConfigurationHash}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return zero, fmt.Errorf("audit database deployment: %w", err)
	}
	if _, err = s.hostingJobs.EnqueueTx(ctx, tx, hostingjobs.PrepareDeploymentArgs{OperationID: id}); err != nil {
		return zero, fmt.Errorf("enqueue database deployment: %w", err)
	}
	op, err := hostingOperation(ctx, tx, id)
	if err != nil {
		return zero, err
	}
	if err = databaseOperationAuthority(ctx, tx, op, false); err != nil {
		return zero, fmt.Errorf("confirm database operation authority: %w", err)
	}
	if err = hostingTargetGate(ctx, tx, target.ProjectID, snap, false); err != nil {
		return zero, err
	}
	if err = proxyCommitMutation(ctx, tx, actor); err != nil {
		return zero, fmt.Errorf("commit database deployment: %w", hostingError(err))
	}
	return op, nil
}

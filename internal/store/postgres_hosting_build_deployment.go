package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

// RequestHostingBuildDeployment admits only a current, persisted local artifact.
func (s *PostgresStore) RequestHostingBuildDeployment(ctx context.Context, id, resourceID, actorID string, in domain.HostingBuildDeploymentInput, audit domain.AuditEvent) (domain.HostingOperation, error) {
	if s.hostingJobs == nil {
		return domain.HostingOperation{}, hostingjobs.ErrUnavailable
	}
	if err := domain.ValidateHostingBuildDeployment(in); err != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = lockHostingResourceDestination(ctx, tx, resourceID); err != nil {
		return domain.HostingOperation{}, err
	}
	b, r, err := resolveCurrentHostingBuildArtifactTx(ctx, tx, actorID, resourceID, in.BuildID)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if r.Generation != in.ExpectedGeneration || b.Snapshot.Source.Generation != in.ExpectedSourceGeneration {
		return domain.HostingOperation{}, ErrConflict
	}
	projectID := r.ProjectID
	snap := domain.HostingSnapshot{ResourceID: resourceID, ResourceGeneration: r.Generation, BindingID: r.BindingID, DestinationID: r.DestinationID, RuntimeNamespace: r.RuntimeNamespace, ServerID: r.ServerID, RunnerID: r.RunnerID, ContainerPort: in.ContainerPort, HostPort: in.HostPort, Readiness: in.Readiness, PolicyVersion: domain.HostingLocalBuildPolicyVersion, LocalBuild: domain.HostingLocalBuildSource{SourceGeneration: in.ExpectedSourceGeneration, ArtifactHash: domain.HostingDocumentHash(*b.Artifact), Artifact: *b.Artifact}}
	if err = hostingTargetGate(ctx, tx, projectID, snap, true); err != nil {
		return domain.HostingOperation{}, err
	}
	var existingID, requestHash string
	err = func() error {
		row, queryErr := sqlcgen.New(tx).GetHostingIdempotency(ctx, sqlcgen.GetHostingIdempotencyParams{ResourceID: resourceID, IdempotencyKey: in.IdempotencyKey})
		if queryErr != nil {
			return queryErr
		}
		existingID = row.ID
		requestHash = row.RequestHash
		return nil
	}()
	if err == nil {
		if requestHash != domain.HostingDocumentHash(in) {
			return domain.HostingOperation{}, ErrConflict
		}
		op, readErr := hostingOperation(ctx, tx, existingID)
		if readErr != nil {
			return op, readErr
		}
		if err = authorizeBuildActor(ctx, tx, actorID, projectID, "maintainer"); err != nil {
			return domain.HostingOperation{}, err
		}
		return op, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.HostingOperation{}, err
	}
	if err = s.resolveHostingReleasePort(ctx, tx, &snap, in.HostPort); err != nil {
		return domain.HostingOperation{}, err
	}
	_, err = sqlcgen.New(tx).CreateHostingPortReservation(ctx, sqlcgen.CreateHostingPortReservationParams{BindingID: snap.BindingID, DestinationID: snap.DestinationID, HostPort: int32(snap.HostPort)})
	if err != nil {
		return domain.HostingOperation{}, hostingError(err)
	}
	var reserved int
	var admitted bool
	var hold *string
	if err = func() error {
		row, queryErr := sqlcgen.New(tx).LockHostingPortReservation(ctx, snap.BindingID)
		if queryErr != nil {
			return queryErr
		}
		reserved = int(row.HostPort)
		admitted = row.RestoreAdmitted
		hold = row.HoldOperationID
		return nil
	}(); err != nil {
		return domain.HostingOperation{}, err
	}
	if reserved < 1024 || !admitted || hold != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	var busy bool
	if err = func() error {
		row, queryErr := sqlcgen.New(tx).HostingBindingBusy(ctx, snap.BindingID)
		if queryErr != nil {
			return queryErr
		}
		busy = row
		return nil
	}(); err != nil {
		return domain.HostingOperation{}, err
	}
	if busy {
		return domain.HostingOperation{}, ErrConflict
	}
	_, err = sqlcgen.New(tx).CreateHostingOperation(ctx, sqlcgen.CreateHostingOperationParams{ID: id, ProjectID: projectID, ResourceID: resourceID, BindingID: snap.BindingID, IdempotencyKey: in.IdempotencyKey, RequestHash: domain.HostingDocumentHash(in), CreatedBy: actorID})
	if err != nil {
		return domain.HostingOperation{}, hostingError(err)
	}
	if _, err = sqlcgen.New(tx).CreateHostingReleaseClaim(ctx, sqlcgen.CreateHostingReleaseClaimParams{OperationID: id, DestinationID: snap.DestinationID, HostPort: int32(snap.HostPort), BindingID: snap.BindingID}); err != nil {
		return domain.HostingOperation{}, hostingError(err)
	}
	contents, err := json.Marshal(snap)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).CreateHostingSnapshot(ctx, sqlcgen.CreateHostingSnapshotParams{OperationID: id, Body: contents, BodyHash: domain.HostingDocumentHash(snap)}); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = sqlcgen.New(tx).CreateHostingOperationBuildArtifact(ctx, sqlcgen.CreateHostingOperationBuildArtifactParams{OperationID: id, BuildID: b.ID, ArtifactHash: snap.LocalBuild.ArtifactHash}); err != nil {
		return domain.HostingOperation{}, hostingError(err)
	}
	audit.ActorID, audit.Action, audit.TargetID = actorID, "hosting.build_deployment_requested", id
	audit.Metadata = map[string]any{"project_id": projectID, "resource_id": resourceID, "binding_id": snap.BindingID, "build_id": b.ID, "artifact_hash": snap.LocalBuild.ArtifactHash}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = s.hostingJobs.EnqueueTx(ctx, tx, hostingjobs.PrepareDeploymentArgs{OperationID: id}); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, id)
	if err != nil {
		return op, err
	}
	if err = authorizeBuildActor(ctx, tx, actorID, projectID, "maintainer"); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return domain.HostingOperation{}, err
	}
	return op, nil
}

func hostingSnapshotTaskType(snap domain.HostingSnapshot) string {
	if snap.LocalBuild != (domain.HostingLocalBuildSource{}) {
		return domain.RunTypeLocalBuildDeploy
	}
	return domain.RunTypeImageDeploy
}
func hostingRetainedPolicy(snap domain.HostingSnapshot) string {
	if snap.LocalBuild != (domain.HostingLocalBuildSource{}) {
		return domain.HostingLocalBuildPolicyVersion
	}
	return domain.HostingReleasePolicyVersion
}
func validateHostingDeploymentTask(run domain.TaskRun) error {
	if run.RunSpec.Type == domain.RunTypeLocalBuildDeploy {
		return domain.ValidateLocalBuildDeploymentTask(run)
	}
	return domain.ValidateImageDeploymentTask(run)
}

// localHostingOperationAuthority separates fresh application from historical
// observation/cleanup. Acknowledgment replay deliberately does not call it.
func localHostingOperationAuthority(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, observe bool) (err error) {
	defer func() {
		if errors.Is(err, auth.ErrForbidden) || errors.Is(err, auth.ErrUnauthenticated) {
			err = ErrConflict
		}
	}()
	if op.Snapshot.LocalBuild == (domain.HostingLocalBuildSource{}) {
		return nil
	}
	actor := op.CreatedBy
	role := "maintainer"
	if observe {
		if op.ReconciliationRunID == "" {
			return ErrConflict
		}
		var err error
		actor, err = sqlcgen.New(tx).HostingTaskActor(ctx, op.ReconciliationRunID)
		if err != nil {
			return hostingError(err)
		}
		role = "owner"
	}
	if observe || op.CleanupPlan != nil {
		r, err := lockBuildResourceDestination(ctx, tx, op.ResourceID)
		if err != nil {
			return err
		}
		if r.BindingID != op.BindingID || r.DestinationID != op.Snapshot.DestinationID || r.RunnerID != op.Snapshot.RunnerID {
			return ErrConflict
		}
		return authorizeBuildActor(ctx, tx, actor, op.ProjectID, role)
	}
	b, r, err := resolveCurrentHostingBuildArtifactTx(ctx, tx, actor, op.ResourceID, op.Snapshot.LocalBuild.Artifact.BuildID)
	if err != nil {
		return err
	}
	if r.Generation != op.Snapshot.ResourceGeneration || b.Snapshot.Source.Generation != op.Snapshot.LocalBuild.SourceGeneration || *b.Artifact != op.Snapshot.LocalBuild.Artifact || domain.HostingDocumentHash(*b.Artifact) != op.Snapshot.LocalBuild.ArtifactHash {
		return ErrConflict
	}
	return nil
}

func hostingOperationTargetGate(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, observe bool) error {
	if err := localHostingOperationAuthority(ctx, tx, op, observe); err != nil {
		return err
	}
	return hostingTargetGateMode(ctx, tx, op.ProjectID, op.Snapshot, false, observe)
}

func finalLocalHostingAuthority(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, op domain.HostingOperation, observe bool) error {
	if op.Snapshot.LocalBuild == (domain.HostingLocalBuildSource{}) {
		return nil
	}
	if err := hostingOperationTargetGate(ctx, tx, op, observe); err != nil {
		return err
	}
	return hostingAuthorityExists(ctx, tx, runner, a, true)
}

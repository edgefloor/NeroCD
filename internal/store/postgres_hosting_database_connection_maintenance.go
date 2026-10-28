package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

// RequestHostingDatabaseMaintenance preserves the exact current database and member identities.
func (s *PostgresStore) RequestHostingDatabaseMaintenance(ctx context.Context, id, database, actor string, in domain.HostingDatabaseMaintenanceInput, event domain.AuditEvent) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	if in.Validate() != nil {
		return zero, ErrConflict
	}
	if s.hostingJobs == nil {
		return zero, hostingjobs.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingOperation, error) {
		q := sqlcgen.New(tx)
		if err := databaseConnectionSchema(ctx, q); err != nil {
			return zero, err
		}
		c, err := q.ReadDatabaseConnectionByDatabase(ctx, database)
		if err != nil {
			return zero, hostingError(err)
		}
		b, err := lockConnectionTargets(ctx, tx, c, actor, domain.RoleOwner)
		if err != nil {
			return zero, err
		}
		if err = requireConnectionAgent(ctx, tx, b.RunnerID, false, b.Database.Engine); err != nil {
			return zero, err
		}
		requestHash := domain.HostingDocumentHash(struct {
			Kind, Actor, Connection string
			Input                   domain.HostingDatabaseMaintenanceInput
		}{domain.HostingDatabaseMaintenanceVersion, actor, c.ID, in})
		accepted, err := q.GetHostingIdempotency(ctx, sqlcgen.GetHostingIdempotencyParams{ResourceID: database, IdempotencyKey: in.IdempotencyKey})
		if err == nil {
			op, e := hostingOperation(ctx, tx, accepted.ID)
			if e != nil || accepted.RequestHash != requestHash || op.CreatedBy != actor || op.DatabaseMaintenance == nil {
				return zero, ErrConflict
			}
			return op, databaseOperationAuthorityMode(ctx, tx, op, false, true)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		c, err = q.LockDatabaseConnection(ctx, c.ID)
		if err != nil || c.State != "active" || in.ExpectedGeneration != b.DatabaseResourceGeneration {
			return zero, ErrConflict
		}
		head, err := q.LockDatabaseMembershipHead(ctx, b.DatabaseBindingID)
		if err != nil || head.Revision != in.ExpectedMembershipRevision {
			return zero, ErrConflict
		}
		_, err = q.LockDatabaseNetworkHold(ctx, b.DatabaseBindingID)
		if !errors.Is(err, pgx.ErrNoRows) {
			if err != nil {
				return zero, err
			}
			return zero, ErrConflict
		}
		if _, err = q.LockHostingAdmissionState(ctx, b.DatabaseBindingID); err != nil {
			return zero, err
		}
		for _, binding := range []string{b.ApplicationBindingID, b.DatabaseBindingID} {
			safe, e := q.HostingDatabaseBindingTargetSafe(ctx, binding)
			if e != nil || safe == nil || !*safe {
				return zero, ErrConflict
			}
		}
		applicationProof, err := connectionApplicationProof(ctx, tx, id, c, b, head)
		if err != nil || len(applicationProof.Members) != 1 {
			return zero, ErrConflict
		}
		input, err := domain.HostingApplicationDatabaseExecutionPlanInput(applicationProof)
		if err != nil {
			return zero, err
		}
		capture, err := readNetworkCapture(ctx, q, c.CaptureID)
		if err != nil || capture.Receipt == nil {
			return zero, ErrConflict
		}
		proof, err := domain.NewHostingDatabaseMaintenanceProof(capture.Plan, *capture.Receipt, applicationProof.DatabasePlan, applicationProof.DatabaseHealthy, domain.HostingDatabaseNetworkPlanInput{Origin: input.Origin, MembershipRevision: input.MembershipRevision, Members: input.Members, Retired: input.Retired})
		if err != nil {
			return zero, ErrConflict
		}
		op := domain.HostingOperation{ID: id, ProjectID: c.ProjectID, ResourceID: database, BindingID: b.DatabaseBindingID, Snapshot: proof.PreviousPlan.Snapshot, CreatedBy: actor, State: "queued", DatabaseMaintenance: &proof}
		if domain.ValidateHostingDatabaseMaintenanceOperation(op) != nil || connectionPublicationBound(op) != nil {
			return zero, ErrConflict
		}
		event.ID, event.ActorID, event.Action, event.TargetID = "aud_connection_execution_"+id, actor, "hosting.database_maintenance.requested", id
		event.Metadata = map[string]any{"connection_id": c.ID, "project_id": c.ProjectID, "proof_hash": proof.Hash, "membership_revision": head.Revision}
		if event.CreatedAt.IsZero() {
			event.CreatedAt = time.Now().UTC()
		}
		if err = createAuditWithQueries(ctx, q, event); err != nil {
			return zero, err
		}
		if err = q.InsertDatabaseConnectionExecution(ctx, sqlcgen.InsertDatabaseConnectionExecutionParams{OperationID: id, ConnectionID: c.ID, Family: "maintenance", ExpectedMembershipRevision: head.Revision, Proof: buildJSON(proof), ProofHash: proof.Hash, AuditID: event.ID}); err != nil {
			return zero, err
		}
		if err = q.InsertDatabaseNetworkHold(ctx, sqlcgen.InsertDatabaseNetworkHoldParams{DatabaseBindingID: b.DatabaseBindingID, ConnectionID: c.ID, OperationID: &id}); err != nil {
			return zero, err
		}
		if err = q.CreateHostingDatabaseDeployment(ctx, sqlcgen.CreateHostingDatabaseDeploymentParams{OperationID: id, ResourceID: database, BindingID: b.DatabaseBindingID, DestinationID: b.DestinationID, Reference: buildJSON(op.Snapshot.Database), Credentials: buildJSON(proof.PreviousPlan.Database.Credentials)}); err != nil {
			return zero, err
		}
		if _, err = q.CreateHostingOperation(ctx, sqlcgen.CreateHostingOperationParams{ID: id, ProjectID: c.ProjectID, ResourceID: database, BindingID: b.DatabaseBindingID, IdempotencyKey: in.IdempotencyKey, RequestHash: requestHash, CreatedBy: actor}); err != nil {
			return zero, err
		}
		if _, err = q.CreateHostingSnapshot(ctx, sqlcgen.CreateHostingSnapshotParams{OperationID: id, Body: buildJSON(op.Snapshot), BodyHash: domain.HostingDocumentHash(op.Snapshot)}); err != nil {
			return zero, err
		}
		if _, err = s.hostingJobs.EnqueueTx(ctx, tx, hostingjobs.PrepareDeploymentArgs{OperationID: id}); err != nil {
			return zero, err
		}
		if err = finalizeConnectionRequest(ctx, tx, c, b, actor, domain.RoleOwner, false); err != nil {
			return zero, err
		}
		if err = databaseOperationAuthority(ctx, tx, op, false); err != nil {
			return zero, err
		}
		return hostingOperation(ctx, tx, id)
	})
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

// Reserve 48 KiB for stage, retirement and message framing before publication.
// Proof and plan retain their independent tighter bounds as well.
func connectionPublicationBound(op domain.HostingOperation) error {
	raw, err := json.Marshal(op)
	if err != nil || len(raw) > domain.HostingApplicationDatabaseJournalJSONLimit-(48<<10) {
		return ErrConflict
	}
	if op.Plan != nil {
		raw, err = json.Marshal(op.Plan)
		if err != nil || len(raw) > 48<<10 {
			return ErrConflict
		}
	}
	return nil
}

func connectionApplicationProof(ctx context.Context, tx pgx.Tx, id string, c sqlcgen.HostingDatabaseConnection, b domain.HostingDatabaseBinding, head sqlcgen.HostingDatabaseMembershipHead) (domain.HostingApplicationDatabaseExecutionProof, error) {
	var zero domain.HostingApplicationDatabaseExecutionProof
	q := sqlcgen.New(tx)
	if head.ConnectionID != c.ID || head.DatabaseBindingID != b.DatabaseBindingID {
		return zero, ErrConflict
	}
	var members []domain.HostingDatabaseNetworkMemberProof
	var retired []domain.HostingDatabaseNetworkRetirementEvidence
	var memberEvidence []domain.HostingDatabaseNetworkMemberEvidence
	if json.Unmarshal(head.Members, &memberEvidence) != nil || json.Unmarshal(head.Retired, &retired) != nil || memberEvidence == nil || retired == nil || len(memberEvidence) > 1 || len(retired) > 64 {
		return zero, ErrConflict
	}
	for _, member := range memberEvidence {
		members = append(members, domain.HostingDatabaseNetworkMemberProof(member))
	}
	current, healthy, err := databaseUpgradeHealthy(ctx, tx, c.DatabaseResourceID, b.DatabaseBindingID)
	if err != nil || current.Plan == nil || domain.ValidateHostingDatabasePlanV2(*current.Plan) != nil {
		return zero, ErrConflict
	}
	retirements := connectionTransitionRetirements(*current.Plan, memberEvidence, retired)
	capture, err := readNetworkCapture(ctx, q, c.CaptureID)
	if err != nil || capture.Receipt == nil || capture.Receipt.ID != head.OriginReceiptID || capture.Plan.SourceOperationID != c.SourceOperationID {
		return zero, ErrConflict
	}
	var original domain.HostingSnapshot
	if json.Unmarshal(c.BindingApplicationSnapshot, &original) != nil {
		return zero, ErrConflict
	}
	in := domain.HostingApplicationDatabasePlanInput{OperationID: id, CreateNonce: domain.HostingDocumentHash(id)[7:39], Binding: b, ApplicationProjectID: c.ProjectID, DatabaseProjectID: c.ProjectID, BindingApplicationSnapshot: original, BindingDatabasePlan: capture.Plan.SourcePlan, DatabasePlan: *current.Plan, DatabaseHealthy: healthy, Origin: capture.Receipt.Origin, MembershipRevision: head.Revision, Members: members, Retired: retirements}
	return domain.NewHostingApplicationDatabaseExecutionProof(in)
}

// The head retains cumulative evidence; the compiler consumes only removals
// from the exact current DB predecessor. Old retirements are never replayed as
// new removals after a maintenance operation has already incorporated them.
func connectionTransitionRetirements(previous domain.HostingApplyPlan, members []domain.HostingDatabaseNetworkMemberEvidence, ledger []domain.HostingDatabaseNetworkRetirementEvidence) []domain.HostingDatabaseNetworkRetirementProof {
	out := make([]domain.HostingDatabaseNetworkRetirementProof, 0)
	if previous.Database == nil || previous.Database.Network == nil {
		return out
	}
	for _, retired := range ledger {
		wasMember, stillMember := false, false
		for _, prior := range previous.Database.Network.Members {
			if prior.EndpointPlanHash == retired.Attached.EndpointPlan.Hash && prior.ReceiptHash == retired.Attached.Receipt.Hash {
				wasMember = true
			}
		}
		for _, member := range members {
			if member.EndpointPlan.Hash == retired.Attached.EndpointPlan.Hash && member.Receipt.Hash == retired.Attached.Receipt.Hash {
				stillMember = true
			}
		}
		if wasMember && !stillMember {
			out = append(out, domain.HostingDatabaseNetworkRetirementProof{Attached: domain.HostingDatabaseNetworkMemberProof(retired.Attached), RetirePlan: retired.RetirePlan, RetireIntent: retired.RetireIntent, Detached: retired.Detached})
		}
	}
	return out
}

// RequestHostingApplicationDatabaseDeployment admits only the exact saved
// binding family. Replacements retain one predecessor until ready and retired.
func (s *PostgresStore) RequestHostingApplicationDatabaseDeployment(ctx context.Context, id, application, actor string, in domain.HostingDeploymentInput, event domain.AuditEvent) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	if domain.ValidateHostingDeployment(in) != nil || in.StorageRouteContinuity != nil {
		return zero, ErrConflict
	}
	if in.ApplicationDatabaseRouteContinuity != nil {
		canonical, err := domain.CanonicalHostingApplicationDatabaseRouteContinuityInput(*in.ApplicationDatabaseRouteContinuity)
		if err != nil {
			return zero, ErrConflict
		}
		in.ApplicationDatabaseRouteContinuity = &canonical
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
		before, err := q.ReadDatabaseConnection(ctx, application)
		if err != nil {
			return zero, err
		}
		binding, err := lockConnectionTargets(ctx, tx, before, actor, domain.RoleOwner)
		if err != nil {
			return zero, err
		}
		c, err := q.LockDatabaseConnection(ctx, before.ID)
		if err != nil {
			return zero, err
		}
		if err = requireConnectionAgent(ctx, tx, binding.RunnerID, false); err != nil {
			return zero, err
		}
		requestHash := domain.HostingDocumentHash(struct {
			Connection string
			Input      domain.HostingDeploymentInput
		}{c.ID, in})
		accepted, err := q.GetHostingIdempotency(ctx, sqlcgen.GetHostingIdempotencyParams{ResourceID: application, IdempotencyKey: in.IdempotencyKey})
		if err == nil {
			op, e := hostingOperation(ctx, tx, accepted.ID)
			if e != nil || accepted.RequestHash != requestHash || op.CreatedBy != actor || op.ApplicationDatabase == nil || op.ApplicationDatabase.Binding.ID != binding.ID {
				return zero, ErrConflict
			}
			return op, finalizeConnectionRequest(ctx, tx, c, binding, actor, domain.RoleOwner, false)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		head, err := q.LockDatabaseMembershipHead(ctx, binding.DatabaseBindingID)
		if err != nil {
			return zero, err
		}
		proof, err := connectionApplicationProof(ctx, tx, id, c, binding, head)
		if err != nil {
			return zero, err
		}
		initial := len(proof.Members) == 0
		if initial && (id != c.ApplicationOperationID || c.State != "application_requested") || !initial && c.State != "active" {
			return zero, ErrConflict
		}
		app, database, err := lockDatabaseBindingTargets(ctx, tx, application, c.DatabaseResourceID)
		if err != nil {
			return zero, err
		}
		if app.Generation != in.ExpectedGeneration || app.Generation != binding.ApplicationResourceGeneration || database.Generation != binding.DatabaseResourceGeneration || in.ExpectedRuntimeConfigurationGeneration != binding.RuntimeConfiguration.Generation {
			return zero, ErrConflict
		}
		supported, err := connectionDeploymentSupported(ctx, tx, application, c.DatabaseResourceID, in.ApplicationDatabaseRouteContinuity != nil)
		if err != nil || !supported {
			return zero, ErrConflict
		}
		for _, target := range []string{app.BindingID, database.BindingID} {
			safe, e := q.HostingDatabaseBindingTargetSafe(ctx, target)
			if e != nil || safe == nil || !*safe {
				return zero, ErrConflict
			}
		}
		var previous domain.HostingHealthy
		raw, err := q.GetHostingHealthy(ctx, app.BindingID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if initial {
			if len(raw) != 0 {
				return zero, ErrConflict
			}
		} else if json.Unmarshal(raw, &previous) != nil || !networkCaptureDocumentEqual(previous, proof.Members[0].ApplicationHealthy) || in.HostPort != 0 && in.HostPort == previous.Snapshot.HostPort {
			return zero, ErrConflict
		}
		var source domain.ResourceSource
		if json.Unmarshal(app.Source, &source) != nil || source.Kind != "image" {
			return zero, ErrConflict
		}
		if _, err = domain.NormalizeHostingImage(source.Image.Reference); err != nil {
			return zero, ErrConflict
		}
		snap := domain.HostingSnapshot{ResourceID: application, ResourceGeneration: app.Generation, ImageReference: source.Image.Reference, BindingID: app.BindingID, DestinationID: app.DestinationID, RuntimeNamespace: app.RuntimeNamespace, ServerID: app.ServerID, RunnerID: app.RunnerID, ContainerPort: in.ContainerPort, HostPort: in.HostPort, Readiness: in.Readiness, RuntimeConfiguration: binding.RuntimeConfiguration, PolicyVersion: domain.HostingApplicationDatabasePolicyVersion, ApplicationDatabase: domain.HostingApplicationDatabaseRef{BindingID: binding.ID, BindingHash: binding.Hash}}
		runtime, err := lockHostingRuntimeConfiguration(ctx, tx, app.ProjectID, application, in.ExpectedRuntimeConfigurationGeneration)
		if err != nil || runtime != binding.RuntimeConfiguration {
			return zero, ErrConflict
		}
		if err = s.resolveHostingReleasePort(ctx, tx, &snap, in.HostPort); err != nil {
			return zero, err
		}
		snap.PolicyVersion = domain.HostingApplicationDatabasePolicyVersion
		if !initial && snap.HostPort == previous.Snapshot.HostPort {
			return zero, ErrConflict
		}
		if err = binding.ValidateTargets(app.ProjectID, snap, database.ProjectID, proof.BindingDatabasePlan); err != nil {
			return zero, err
		}
		if _, err = q.CreateHostingPortReservation(ctx, sqlcgen.CreateHostingPortReservationParams{BindingID: app.BindingID, DestinationID: app.DestinationID, HostPort: int32(snap.HostPort)}); err != nil {
			return zero, err
		}
		port, err := q.LockHostingPortReservation(ctx, app.BindingID)
		if err != nil || !port.RestoreAdmitted || port.HoldOperationID != nil {
			return zero, ErrConflict
		}
		op := domain.HostingOperation{ID: id, ProjectID: app.ProjectID, ResourceID: application, BindingID: app.BindingID, CreatedBy: actor, State: "queued", Snapshot: snap, ApplicationDatabase: &proof}
		if in.ApplicationDatabaseRouteContinuity != nil {
			if initial {
				return zero, ErrConflict
			}
			continuity, e := admitApplicationDatabaseContinuity(ctx, tx, op, *in.ApplicationDatabaseRouteContinuity, previous)
			if e != nil {
				return zero, e
			}
			op.ApplicationDatabaseRouteContinuity = &continuity
		}
		event.ID, event.ActorID, event.Action, event.TargetID = "aud_connection_execution_"+id, actor, "hosting.application_database.requested", id
		event.Metadata = map[string]any{"connection_id": c.ID, "binding_id": binding.ID, "project_id": c.ProjectID, "proof_hash": proof.Hash, "membership_revision": head.Revision}
		if event.CreatedAt.IsZero() {
			event.CreatedAt = time.Now().UTC()
		}
		if err = createAuditWithQueries(ctx, q, event); err != nil {
			return zero, err
		}
		// The exact sidecar precedes the operation for the BEFORE pending guard.
		// Its operation FK and complete proof associations are deferred until flush.
		if err = q.InsertDatabaseConnectionExecution(ctx, sqlcgen.InsertDatabaseConnectionExecutionParams{OperationID: id, ConnectionID: c.ID, Family: "application", ExpectedMembershipRevision: head.Revision, Proof: buildJSON(proof), ProofHash: proof.Hash, AuditID: event.ID}); err != nil {
			return zero, err
		}
		hold, err := q.LockDatabaseNetworkHold(ctx, binding.DatabaseBindingID)
		if errors.Is(err, pgx.ErrNoRows) && !initial {
			err = q.InsertDatabaseNetworkHold(ctx, sqlcgen.InsertDatabaseNetworkHoldParams{DatabaseBindingID: binding.DatabaseBindingID, ConnectionID: c.ID, OperationID: &id})
		} else if err == nil && (hold.ConnectionID != c.ID || hold.OperationID == nil || *hold.OperationID != id) {
			err = ErrConflict
		}
		if err != nil {
			return zero, err
		}
		if _, err = q.CreateHostingOperation(ctx, sqlcgen.CreateHostingOperationParams{ID: id, ProjectID: app.ProjectID, ResourceID: application, BindingID: app.BindingID, IdempotencyKey: in.IdempotencyKey, RequestHash: requestHash, CreatedBy: actor}); err != nil {
			return zero, err
		}
		if _, err = q.CreateHostingSnapshot(ctx, sqlcgen.CreateHostingSnapshotParams{OperationID: id, Body: buildJSON(snap), BodyHash: domain.HostingDocumentHash(snap)}); err != nil {
			return zero, err
		}
		if _, err = q.CreateHostingReleaseClaim(ctx, sqlcgen.CreateHostingReleaseClaimParams{OperationID: id, DestinationID: snap.DestinationID, HostPort: int32(snap.HostPort), BindingID: snap.BindingID}); err != nil {
			return zero, err
		}
		if continuity := op.ApplicationDatabaseRouteContinuity; continuity != nil {
			if e := q.InsertApplicationDatabaseRouteContinuation(ctx, sqlcgen.InsertApplicationDatabaseRouteContinuationParams{OperationID: id, ProxyID: continuity.ProxyID, ProxyOperationID: continuity.ProxyOperationID, FinalizeRunID: continuity.FinalizeRunID, Body: buildJSON(continuity), BodyHash: continuity.Hash}); e != nil {
				return zero, e
			}
		}
		if domain.ValidateHostingApplicationDatabaseOperation(op) != nil || connectionPublicationBound(op) != nil {
			return zero, ErrConflict
		}
		if _, err = s.hostingJobs.EnqueueTx(ctx, tx, hostingjobs.PrepareDeploymentArgs{OperationID: id}); err != nil {
			return zero, err
		}
		if err = finalizeConnectionRequest(ctx, tx, c, binding, actor, domain.RoleOwner, false); err != nil {
			return zero, err
		}
		if err = requireConnectionAgent(ctx, tx, binding.RunnerID, false); err != nil {
			return zero, err
		}
		if err = proxyCheckLegacyToken(ctx, tx, actor); err != nil {
			return zero, err
		}
		return hostingOperation(ctx, tx, id)
	})
}

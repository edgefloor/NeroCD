package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// RequestHostingDatabaseConnectionRecovery admits observation or one explicit
// composed-storage recovery. Each effect recovery gets a dedicated fresh task;
// admission preserves the original deployment, candidate and held ownership.
func (s *PostgresStore) RequestHostingDatabaseConnectionRecovery(ctx context.Context, id, application, actor string, in domain.HostingDatabaseConnectionRecoveryInput, event domain.AuditEvent) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	if err := in.Validate(); err != nil {
		return zero, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingOperation, error) {
		q := sqlcgen.New(tx)
		if err := databaseConnectionSchema(ctx, q); err != nil {
			return zero, err
		}
		c, err := q.ReadDatabaseConnection(ctx, application)
		if err != nil {
			return zero, hostingError(err)
		}
		before, err := hostingOperation(ctx, tx, in.OperationID)
		if err != nil {
			return zero, err
		}
		if err = hostingLockOperationRuns(ctx, tx, before); err != nil {
			return zero, err
		}
		b, err := lockConnectionTargets(ctx, tx, c, actor, domain.RoleOwner)
		if err != nil {
			return zero, err
		}
		if err = requireConnectionAgent(ctx, tx, b.RunnerID, false, b.Database.Engine); err != nil {
			return zero, err
		}
		requestHash := domain.HostingDocumentHash(in)

		if in.Action != "observe" && before.ApplicationDatabaseStorage != nil {
			saved, e := q.ReadApplicationDatabaseStorageRecovery(ctx, before.ID)
			if e == nil {
				if saved.ActorID != actor || saved.RequestHash != requestHash || !connectionJSONMatches(saved.Request, in) {
					return zero, ErrConflict
				}
				return before, finalizeConnectionRequest(ctx, tx, c, b, actor, domain.RoleOwner, false)
			} else if !errors.Is(e, pgx.ErrNoRows) {
				return zero, e
			}
		}
		old, err := q.ReadDatabaseConnectionRecovery(ctx, sqlcgen.ReadDatabaseConnectionRecoveryParams{ConnectionID: c.ID, IdempotencyKey: in.IdempotencyKey})
		if err == nil {
			if old.ActorID != actor || old.OperationID != in.OperationID || old.RequestHash != requestHash || !connectionJSONMatches(old.AcceptedRequest, in) {
				return zero, ErrConflict
			}
			return before, finalizeConnectionRequest(ctx, tx, c, b, actor, domain.RoleOwner, false)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		execution, err := q.ReadDatabaseConnectionExecution(ctx, before.ID)
		if err != nil || execution.ConnectionID != c.ID || !connectionOperation(before) || before.Plan == nil || before.ReconciliationRunID != "" {
			return zero, ErrConflict
		}
		head, err := q.LockDatabaseMembershipHead(ctx, b.DatabaseBindingID)
		if err != nil || head.Revision != in.ExpectedMembershipRevision {
			return zero, ErrConflict
		}
		hold, err := q.LockDatabaseNetworkHold(ctx, b.DatabaseBindingID)
		if err != nil || hold.OperationID == nil || *hold.OperationID != before.ID || hold.ConnectionID != c.ID {
			return zero, ErrConflict
		}
		if before.ApplicationDatabase != nil {
			stage, e := connectionStage(ctx, q, before)
			if e != nil || stage == nil && (in.StageHash != "" || in.CandidateHash != "") || stage != nil && (in.StageHash != stage.Hash || in.CandidateHash != stage.Candidate.Hash) {
				return zero, ErrConflict
			}
		} else if in.StageHash != "" || in.CandidateHash != "" {
			return zero, ErrConflict
		}
		if _, err = q.LockHostingOperationState(ctx, before.ID); err != nil {
			return zero, err
		}
		if in.Action != "observe" {
			return s.admitApplicationStorageRecovery(ctx, tx, id, actor, in, event, c, b, before)
		}
		// A new task has observation authority only; the original deploy lease
		// and every immutable effect record are deliberately untouched.
		runID := "observe_" + before.ID
		spec := domain.RunSpec{Type: hostingSnapshotTaskType(before.Snapshot), Inputs: map[string]any{}, HostingOperationID: before.ID, HostingMode: "observe"}
		if _, err = q.CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: runID, ProjectID: c.ProjectID, RunSpec: buildJSON(spec), RequestedBy: actor}); err != nil {
			return zero, err
		}
		if _, err = q.CreateHostingObservationTarget(ctx, sqlcgen.CreateHostingObservationTargetParams{RunID: runID, OperationID: before.ID, BindingID: before.BindingID, RunnerID: b.RunnerID}); err != nil {
			return zero, err
		}
		if _, err = q.RequestHostingObservation(ctx, sqlcgen.RequestHostingObservationParams{ID: before.ID, ReconciliationRunID: &runID}); err != nil {
			return zero, err
		}
		event.ID, event.ActorID, event.Action, event.TargetID = "aud_connection_observe_"+id, actor, "hosting.database_connection.observation_requested", before.ID
		event.Metadata = map[string]any{"connection_id": c.ID, "stage_hash": in.StageHash, "candidate_hash": in.CandidateHash, "membership_revision": head.Revision, "observation_run_id": runID, "recovery_limit": "observe_only_once_no_expired_authority_resume"}
		if event.CreatedAt.IsZero() {
			event.CreatedAt = time.Now().UTC()
		}
		if err = createAuditWithQueries(ctx, q, event); err != nil {
			return zero, err
		}
		if err = q.InsertDatabaseConnectionRecovery(ctx, sqlcgen.InsertDatabaseConnectionRecoveryParams{ID: id, ConnectionID: c.ID, OperationID: before.ID, ActorID: actor, IdempotencyKey: in.IdempotencyKey, AcceptedRequest: buildJSON(in), RequestHash: requestHash, AuditID: event.ID}); err != nil {
			return zero, err
		}
		op, err := hostingOperation(ctx, tx, before.ID)
		if err != nil {
			return zero, err
		}
		return op, finalizeConnectionRequest(ctx, tx, c, b, actor, domain.RoleOwner, false)
	})
}

func (s *PostgresStore) recordDatabaseConnectionObservation(ctx context.Context, tx pgx.Tx, runner string, in domain.HostingObservation, op domain.HostingOperation) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	q := sqlcgen.New(tx)
	if !op.ReconcileRequested || op.ReconciliationRunID != in.RunID || op.BindingID != in.BindingID || op.Plan == nil || op.Release == nil || in.DaemonID != op.Release.DaemonID || in.PlanHash != op.Plan.Hash || in.ReleaseHash != op.Release.Hash {
		return zero, ErrConflict
	}
	knownContainer := ""
	if op.ApplicationDatabase != nil {
		stage, err := connectionStage(ctx, q, op)
		if err != nil || stage == nil {
			// No retained candidate means the unknown create outcome cannot be
			// converted into a positive absence claim by this wire protocol.
			return zero, ErrConflict
		}
		knownContainer = stage.Candidate.ContainerID
	} else if op.Result != nil {
		knownContainer = op.Result.ContainerID
	}
	if knownContainer == "" {
		return zero, ErrConflict
	}
	// Known identity must remain exact. Absence and unknown create outcomes are
	// still observations; neither proves it is safe to issue another create.
	if in.ContainerID != "" {
		if in.ContainerID != knownContainer || in.NoOwnedContainers || in.ImageID != op.Release.ImageID {
			return zero, ErrConflict
		}
	} else if !in.NoOwnedContainers || in.ImageID != "" || in.Ready {
		return zero, ErrConflict
	}
	if err := finalDatabaseConnectionAuthority(ctx, tx, runner, in.HostingAuthority, op, true, false); err != nil {
		return zero, err
	}
	if _, err := q.CreateHostingReconciliationReceipt(ctx, sqlcgen.CreateHostingReconciliationReceiptParams{LeaseID: in.LeaseID, OperationID: op.ID, Body: buildJSON(in), BodyHash: domain.HostingDocumentHash(in)}); err != nil {
		return zero, err
	}
	if _, err := q.CompleteHostingObservationLease(ctx, sqlcgen.CompleteHostingObservationLeaseParams{ID: in.LeaseID, CompletionKey: hostingStringPointer("observed_" + in.LeaseID)}); err != nil {
		return zero, err
	}
	if _, err := q.CompleteHostingObservationTask(ctx, in.RunID); err != nil {
		return zero, err
	}
	if _, err := q.ClearHostingObservationRequest(ctx, op.ID); err != nil {
		return zero, err
	}
	if err := hostingPhaseAudit(ctx, tx, s.queries, op, runner, "connection_observed_"+in.LeaseID); err != nil {
		return zero, err
	}
	if err := finalDatabaseConnectionAuthority(ctx, tx, runner, in.HostingAuthority, op, true, true); err != nil {
		return zero, err
	}
	op, err := hostingTaskOperation(ctx, tx, in.HostingAuthority)
	if err != nil {
		return zero, err
	}
	return op, tx.Commit(ctx)
}

var _ HostingDatabaseConnectionRepository = (*PostgresStore)(nil)
var _ HostingApplicationDatabaseRunnerRepository = (*PostgresStore)(nil)

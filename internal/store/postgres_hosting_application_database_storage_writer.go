package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func applicationStorageWriter(ctx context.Context, q *sqlcgen.Queries, op domain.HostingOperation) (*domain.HostingApplicationDatabaseStorageWriterRequest, error) {
	raw, err := q.ReadApplicationDatabaseStorageWriter(ctx, op.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out domain.HostingApplicationDatabaseStorageWriterRequest
	if json.Unmarshal(raw, &out) != nil || out.Validate() != nil || op.Plan == nil || op.ApplicationDatabaseStorage == nil || out.OperationID != op.ID || out.Receipt.PlanHash != op.Plan.Hash || out.Receipt.StorageRef != op.Snapshot.Storage || op.Plan.PreviousHealthy == nil || out.Receipt.PredecessorHash != domain.HostingDocumentHash(op.Plan.PreviousHealthy) {
		return nil, ErrConflict
	}
	return &out, nil
}
func storageRecoveryPlan(ctx context.Context, q *sqlcgen.Queries, op domain.HostingOperation) (*domain.HostingApplicationDatabaseStorageRecoveryPlan, error) {
	row, err := q.ReadApplicationDatabaseStorageRecovery(ctx, op.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var plan domain.HostingApplicationDatabaseStorageRecoveryPlan
	if json.Unmarshal(row.Body, &plan) != nil || plan.Validate() != nil || plan.OperationID != op.ID || plan.RunID != row.RunID || plan.Action != row.Action || plan.RequestHash != row.RequestHash || plan.StorageRef != op.Snapshot.Storage {
		return nil, ErrConflict
	}
	return &plan, nil
}
func storageRecoveryRun(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, run string) bool {
	if op.ApplicationDatabaseStorage == nil {
		return false
	}
	p, err := storageRecoveryPlan(ctx, sqlcgen.New(tx), op)
	return err == nil && p != nil && p.Action == "resume_predecessor" && p.RunID == run
}
func storageWriterTransition(previous *domain.HostingApplicationDatabaseStorageWriterRequest, next domain.HostingApplicationDatabaseStorageWriterRequest, stage *domain.HostingApplicationDatabaseStage, recover bool) bool {
	r := next.Receipt
	if previous == nil {
		return !recover && r.Phase == "stop_intent" && r.PreviousStateHash == "" && stage == nil && r.StageHash == ""
	}
	old := previous.Receipt
	if r.PreviousStateHash != old.Hash {
		return false
	}
	if stage == nil {
		if r.StageHash != "" || r.CandidateHash != "" {
			return false
		}
	} else if r.StageHash != stage.Hash || r.CandidateHash != stage.Candidate.Hash {
		return false
	}
	if recover {
		return old.Phase == "replacement_failed" && r.Phase == "resume_intent" || old.Phase == "resume_intent" && r.Phase == "resumed"
	}
	return old.Phase == "stop_intent" && r.Phase == "stopped" || old.Phase == "stopped" && r.Phase == "candidate_stopped" && stage != nil || old.Phase == "candidate_stopped" && r.Phase == "replacement_failed"
}

// Writer acknowledgments retain the exact historical response. A lost ACK cannot
// repeat a stop/start effect or grant the old attempt fresh authority.
func (s *PostgresStore) HostingApplicationDatabaseStorageWriter(ctx context.Context, runner, fingerprint string, in domain.HostingApplicationDatabaseStorageWriterRequest) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	if in.Validate() != nil {
		return zero, ErrConflict
	}
	ctx, err := BindHostingDatabaseConnectionRunnerContext(ctx, runner, fingerprint)
	if err != nil {
		return zero, err
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingOperation, error) {
		op, err := connectionRunnerOperation(ctx, tx, runner, in.HostingAuthority, false)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		if _, err = q.LockHostingOperationState(ctx, op.ID); err != nil {
			return zero, err
		}
		old, err := q.ReadApplicationDatabaseStorageWriterAck(ctx, sqlcgen.ReadApplicationDatabaseStorageWriterAckParams{OperationID: op.ID, Phase: in.Receipt.Phase})
		if err == nil {
			var response domain.HostingOperation
			if !connectionJSONMatches(old.Request, in) || json.Unmarshal(old.Response, &response) != nil {
				return zero, ErrConflict
			}
			return response, databaseConnectionHistoricalRunnerRead(ctx, tx, runner, op)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if op.ApplicationDatabaseStorage == nil || op.Plan == nil || op.Plan.PreviousHealthy == nil || in.Receipt.PlanHash != op.Plan.Hash || in.Receipt.StorageRef != op.Snapshot.Storage || in.Receipt.PredecessorHash != domain.HostingDocumentHash(op.Plan.PreviousHealthy) || !op.MutationHold || op.Result != nil || op.ApplicationDatabaseYield != nil || op.State != "applying" && op.State != "verifying" && !(op.State == "failed" && storageRecoveryRun(ctx, tx, op, in.RunID)) {
			return zero, ErrConflict
		}
		recovery := storageRecoveryRun(ctx, tx, op, in.RunID)
		mode := "deploy"
		if recovery {
			mode = "storage_recover"
		} else if in.RunID != op.RunID {
			return zero, ErrConflict
		}
		if err = hostingRequireMode(ctx, tx, in.HostingAuthority, mode); err != nil {
			return zero, err
		}
		previous, err := applicationStorageWriter(ctx, q, op)
		if err != nil {
			return zero, err
		}
		stage, err := connectionStage(ctx, q, op)
		if err != nil || !storageWriterTransition(previous, in, stage, recovery) {
			return zero, ErrConflict
		}
		if err = finalDatabaseConnectionAuthority(ctx, tx, runner, in.HostingAuthority, op, false, false); err != nil {
			return zero, err
		}
		response := op
		state := in.Receipt.State()
		response.ApplicationDatabaseStorageState = &state
		if recovery {
			state.RecoveryAction = "resume_predecessor"
			state.RecoveryState = "running"
		}
		terminal := in.Receipt.Phase == "replacement_failed" || in.Receipt.Phase == "resumed"
		if in.Receipt.Phase == "replacement_failed" {
			response.State = "failed"
		}
		if in.Receipt.Phase == "resumed" {
			response.State = "failed"
			response.MutationHold = false
			state.RecoveryState = "succeeded"
		}
		if err = q.DeferApplicationDatabaseStorageWriterSettlement(ctx); err != nil {
			return zero, err
		}
		if err = q.InsertApplicationDatabaseStorageWriter(ctx, sqlcgen.InsertApplicationDatabaseStorageWriterParams{OperationID: op.ID, Phase: in.Receipt.Phase, ReceiptID: in.Receipt.ReceiptID, Request: buildJSON(in), RequestHash: domain.HostingDocumentHash(in), Response: buildJSON(response)}); err != nil {
			return zero, err
		}
		if in.Receipt.Phase == "replacement_failed" {
			if _, err = q.SetHostingOperationState(ctx, sqlcgen.SetHostingOperationStateParams{ID: op.ID, State: "failed"}); err != nil {
				return zero, err
			}
		}
		if in.Receipt.Phase == "resumed" {
			if err = settleStorageResumeMembership(ctx, tx, op, in); err != nil {
				return zero, err
			}
			if n, e := q.SetApplicationDatabaseStorageResumed(ctx, op.ID); e != nil || n != 1 {
				return zero, ErrConflict
			}
			if n, e := q.ClearHostingMutationHold(ctx, sqlcgen.ClearHostingMutationHoldParams{BindingID: op.BindingID, HoldOperationID: &op.ID}); e != nil || n != 1 {
				return zero, ErrConflict
			}
			if _, err = q.ReleaseApplicationDatabaseStorageNetworkHold(ctx, &op.ID); err != nil {
				return zero, err
			}
			if _, err = q.ReleaseHostingFailedClaim(ctx, op.ID); err != nil {
				return zero, err
			}
		}
		if terminal {
			status := "failed"
			if in.Receipt.Phase == "resumed" {
				status = "succeeded"
			}
			if _, err = q.CompleteHostingLease(ctx, sqlcgen.CompleteHostingLeaseParams{ID: in.LeaseID, Status: status, CompletionKey: &in.Receipt.ReceiptID}); err != nil {
				return zero, err
			}
			if _, err = q.CompleteHostingTask(ctx, sqlcgen.CompleteHostingTaskParams{ID: in.RunID, Status: status}); err != nil {
				return zero, err
			}
		}
		if err = finalDatabaseConnectionAuthority(ctx, tx, runner, in.HostingAuthority, op, false, terminal); err != nil {
			return zero, err
		}
		return response, nil
	})
}

func (s *PostgresStore) AuthorizeHostingApplicationDatabaseStorageEffect(ctx context.Context, runner, fingerprint string, in domain.HostingApplicationDatabaseStorageEffectRequest) (domain.HostingApplicationDatabaseStorageEffectAuthorization, error) {
	var zero domain.HostingApplicationDatabaseStorageEffectAuthorization
	if in.Validate() != nil {
		return zero, ErrConflict
	}
	ctx, err := BindHostingDatabaseConnectionRunnerContext(ctx, runner, fingerprint)
	if err != nil {
		return zero, err
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingApplicationDatabaseStorageEffectAuthorization, error) {
		op, err := connectionRunnerOperation(ctx, tx, runner, in.HostingAuthority, true)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		if _, err = q.LockHostingOperationState(ctx, op.ID); err != nil {
			return zero, err
		}
		writer, err := applicationStorageWriter(ctx, q, op)
		if err != nil || writer == nil || writer.Receipt.Hash != in.StateHash || !op.MutationHold || op.Result != nil || op.ApplicationDatabaseYield != nil {
			return zero, ErrConflict
		}
		stage, err := connectionStage(ctx, q, op)
		if err != nil || stage == nil && in.StageHash != "" || stage != nil && stage.Hash != in.StageHash {
			return zero, ErrConflict
		}
		mode := "deploy"
		phase := writer.Receipt.Phase
		switch in.Action {
		case "stop_predecessor":
			if phase != "stop_intent" || stage != nil || in.RunID != op.RunID {
				return zero, ErrConflict
			}
		case "stop_candidate":
			if phase != "stopped" || stage == nil || in.RunID != op.RunID {
				return zero, ErrConflict
			}
		case "resume_predecessor":
			if phase != "resume_intent" || !storageRecoveryRun(ctx, tx, op, in.RunID) {
				return zero, ErrConflict
			}
			mode = "storage_recover"
		default:
			return zero, ErrConflict
		}
		if err = hostingRequireMode(ctx, tx, in.HostingAuthority, mode); err != nil {
			return zero, err
		}
		out := domain.HostingApplicationDatabaseStorageEffectAuthorization{HostingApplicationDatabaseEffectAuthorization: domain.HostingApplicationDatabaseEffectAuthorization{AuthorizationID: in.AuthorizationID, OperationID: op.ID, PlanHash: op.Plan.Hash, Action: in.Action, StageHash: in.StageHash, LeaseID: in.LeaseID, Attempt: in.Attempt, Fence: in.Fence, MembershipRevision: op.ApplicationDatabase.MembershipRevision}, StateHash: in.StateHash}
		saved, err := q.ReadApplicationDatabaseStorageEffect(ctx, in.AuthorizationID)
		if err == nil {
			if !connectionJSONMatches(saved.Request, in) || json.Unmarshal(saved.Response, &out) != nil {
				return zero, ErrConflict
			}
		} else if errors.Is(err, pgx.ErrNoRows) {
			clock, e := q.ApplicationDatabaseEffectClock(ctx, in.LeaseID)
			if e != nil {
				return zero, e
			}
			out.UseUntil = clock.DatabaseNow.Add(30 * time.Second)
			if clock.ExpiresAt.Before(out.UseUntil) {
				out.UseUntil = clock.ExpiresAt
			}
			if out.Validate() != nil {
				return zero, ErrConflict
			}
			if err = q.InsertApplicationDatabaseStorageEffect(ctx, sqlcgen.InsertApplicationDatabaseStorageEffectParams{AuthorizationID: in.AuthorizationID, OperationID: op.ID, Request: buildJSON(in), Response: buildJSON(out), UseUntil: out.UseUntil}); err != nil {
				return zero, err
			}
		} else {
			return zero, err
		}
		if err = finalDatabaseConnectionAuthority(ctx, tx, runner, in.HostingAuthority, op, false, false); err != nil {
			return zero, err
		}
		now, err := q.HostingRuntimeEnvironmentDeliveryNow(ctx)
		if err != nil || !now.Before(out.UseUntil) {
			return zero, ErrConflict
		}
		return out, nil
	})
}

func settleStorageResumeMembership(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, in domain.HostingApplicationDatabaseStorageWriterRequest) error {
	q := sqlcgen.New(tx)
	proof := op.ApplicationDatabase
	if proof == nil || len(proof.Members) != 1 || in.Receipt.PredecessorEndpoint == nil {
		return ErrConflict
	}
	old := proof.Members[0]
	endpoint, _, _, ok := domain.HostingDatabaseMemberEndpoint(domain.HostingDatabaseNetworkMemberProof(old))
	if !ok || in.Receipt.PredecessorEndpoint.Endpoint.Target != endpoint.Target || in.Receipt.PredecessorEndpoint.Endpoint.EndpointID == endpoint.EndpointID {
		return ErrConflict
	}
	head, err := q.LockDatabaseMembershipHead(ctx, proof.Binding.DatabaseBindingID)
	if err != nil || head.Revision != proof.MembershipRevision || !connectionJSONMatches(head.Members, proof.Members) {
		return ErrConflict
	}
	old.Resume = &domain.HostingDatabaseNetworkResumeEvidence{Version: domain.HostingApplicationDatabaseStorageVersion, OperationID: op.ID, MembershipRevision: head.Revision + 1, OriginalReceiptHash: old.Receipt.Hash, WriterReceipt: in.Receipt}
	if _, _, _, ok = domain.HostingDatabaseMemberEndpoint(domain.HostingDatabaseNetworkMemberProof(old)); !ok {
		return ErrConflict
	}
	if err = q.InsertApplicationDatabaseStorageResumeMembership(ctx, sqlcgen.InsertApplicationDatabaseStorageResumeMembershipParams{DatabaseBindingID: head.DatabaseBindingID, Revision: head.Revision + 1, OperationID: op.ID, Body: buildJSON(old), BodyHash: domain.HostingDocumentHash(old)}); err != nil {
		return err
	}
	return casBuild(q.AdvanceDatabaseMembershipHead(ctx, sqlcgen.AdvanceDatabaseMembershipHeadParams{DatabaseBindingID: head.DatabaseBindingID, Revision: head.Revision + 1, Members: buildJSON([]domain.HostingDatabaseNetworkMemberEvidence{old}), Retired: head.Retired, Revision_2: head.Revision}))
}

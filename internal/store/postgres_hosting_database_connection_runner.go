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

func connectionStage(ctx context.Context, q *sqlcgen.Queries, op domain.HostingOperation) (*domain.HostingApplicationDatabaseStage, error) {
	row, err := q.ReadApplicationDatabaseStage(ctx, op.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if op.ApplicationDatabase == nil || op.Plan == nil {
		return nil, ErrConflict
	}
	input, err := domain.HostingApplicationDatabaseExecutionPlanInput(*op.ApplicationDatabase)
	var stage domain.HostingApplicationDatabaseStage
	if err != nil || json.Unmarshal(row.Stage, &stage) != nil || stage.Hash != row.StageHash || stage.ReceiptID != row.ReceiptID || stage.OperationID != op.ID || stage.Phase != row.Phase || stage.Validate(*op.Plan, input) != nil {
		return nil, ErrConflict
	}
	return &stage, nil
}

func connectionRetirement(ctx context.Context, q *sqlcgen.Queries, op domain.HostingOperation) (*domain.HostingDatabaseBindingEndpointPlan, *domain.HostingDatabaseBindingEndpointReceipt, error) {
	row, err := q.ReadApplicationDatabaseRetirement(ctx, op.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	var p domain.HostingDatabaseBindingEndpointPlan
	var r domain.HostingDatabaseBindingEndpointReceipt
	if op.ApplicationDatabase == nil || json.Unmarshal(row.Plan, &p) != nil || json.Unmarshal(row.Receipt, &r) != nil || r.ReceiptID != row.ReceiptID || r.Hash != row.ReceiptHash || row.Phase != r.Phase || r.Validate(p, op.ApplicationDatabase.Binding) != nil {
		return nil, nil, ErrConflict
	}
	return &p, &r, nil
}

func connectionRunnerOperation(ctx context.Context, tx pgx.Tx, runner string, a domain.HostingAuthority, live bool) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	if err := hostingAuthorityExists(ctx, tx, runner, a, live); err != nil {
		return zero, err
	}
	op, err := hostingTaskOperation(ctx, tx, a)
	if err != nil {
		return zero, err
	}
	if op.ApplicationDatabase == nil || op.Plan == nil || op.Snapshot.RunnerID != runner {
		return zero, ErrConflict
	}
	observe := a.RunID == op.ReconciliationRunID
	if err = databaseConnectionRunnerRead(ctx, tx, runner, op, observe); err != nil {
		return zero, err
	}
	return op, nil
}

// HostingApplicationDatabaseStage records ordered evidence without effect permission.
func (s *PostgresStore) HostingApplicationDatabaseStage(ctx context.Context, runner, fingerprint string, in domain.HostingApplicationDatabaseStageRequest) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	var err error
	ctx, err = BindHostingDatabaseConnectionRunnerContext(ctx, runner, fingerprint)
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
		old, err := q.ReadApplicationDatabaseStageReceipt(ctx, in.Stage.ReceiptID)
		if err == nil {
			if old.OperationID != op.ID || old.StageHash != in.Stage.Hash || !connectionJSONMatches(old.Authority, in.HostingAuthority) || !connectionJSONMatches(old.Stage, in.Stage) {
				return zero, ErrConflict
			}
			return op, databaseConnectionHistoricalRunnerRead(ctx, tx, runner, op)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if op.ApplicationDatabaseRouteContinuity != nil && (op.ApplicationDatabaseYield != nil || in.RunID != op.RunID) {
			return zero, ErrConflict
		}
		if op.State != "applying" && op.State != "verifying" || !op.MutationHold {
			return zero, ErrConflict
		}
		if err = hostingRequireMode(ctx, tx, in.HostingAuthority, "deploy"); err != nil {
			return zero, err
		}
		previous, err := connectionStage(ctx, q, op)
		if err != nil {
			return zero, err
		}
		input, err := domain.HostingApplicationDatabaseExecutionPlanInput(*op.ApplicationDatabase)
		if err != nil || in.Stage.ValidateTransition(previous, *op.Plan, input) != nil {
			return zero, ErrConflict
		}
		var previousHash *string
		if previous != nil {
			previousHash = &previous.Hash
		}
		if err = q.InsertApplicationDatabaseStage(ctx, sqlcgen.InsertApplicationDatabaseStageParams{OperationID: op.ID, ReceiptID: in.Stage.ReceiptID, Phase: in.Stage.Phase, PreviousHash: previousHash, Authority: buildJSON(in.HostingAuthority), Stage: buildJSON(in.Stage), StageHash: in.Stage.Hash}); err != nil {
			return zero, err
		}
		return op, finalDatabaseConnectionAuthority(ctx, tx, runner, in.HostingAuthority, op, false, false)
	})
}

// GetHostingApplicationDatabaseState reads retained evidence under current access.
func (s *PostgresStore) GetHostingApplicationDatabaseState(ctx context.Context, runner, fingerprint string, a domain.HostingAuthority) (domain.HostingApplicationDatabaseExecutionState, error) {
	var zero domain.HostingApplicationDatabaseExecutionState
	var err error
	ctx, err = BindHostingDatabaseConnectionRunnerContext(ctx, runner, fingerprint)
	if err != nil {
		return zero, err
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingApplicationDatabaseExecutionState, error) {
		op, err := connectionRunnerOperation(ctx, tx, runner, a, false)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		stage, err := connectionStage(ctx, q, op)
		if err != nil {
			return zero, err
		}
		plan, receipt, err := connectionRetirement(ctx, q, op)
		if err != nil {
			return zero, err
		}
		head, err := q.ReadDatabaseMembershipHead(ctx, op.ApplicationDatabase.Binding.DatabaseBindingID)
		if err != nil {
			return zero, err
		}
		out := domain.HostingApplicationDatabaseExecutionState{OperationID: op.ID, PlanHash: op.Plan.Hash, Stage: stage, RetirementPlan: plan, RetirementReceipt: receipt, MembershipRevision: head.Revision}
		hold, err := q.LockDatabaseNetworkHold(ctx, head.DatabaseBindingID)
		if err == nil {
			// A different current operation's identity is not this state's hold.
			if hold.OperationID != nil && *hold.OperationID == op.ID {
				out.HoldOperationID = op.ID
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if out.Validate() != nil {
			return zero, ErrConflict
		}
		if err = databaseConnectionRunnerRead(ctx, tx, runner, op, a.RunID == op.ReconciliationRunID); err != nil {
			return zero, err
		}
		if err = hostingAuthorityExists(ctx, tx, runner, a, false); err != nil {
			return zero, err
		}
		return out, nil
	})
}

func connectionEffectStage(action, expected string, stage *domain.HostingApplicationDatabaseStage, retirement *domain.HostingDatabaseBindingEndpointReceipt) bool {
	if action == "create" {
		return expected == "" && stage == nil && retirement == nil
	}
	if stage == nil || stage.Hash != expected {
		return false
	}
	switch action {
	case "configure":
		return stage.Phase == "attach_intent" && retirement == nil
	case "start":
		return stage.Phase == "network_configured" && retirement == nil
	case "retire":
		return stage.Phase == "ready" && retirement != nil && retirement.Phase == "retire_intent"
	default:
		return false
	}
}

// AuthorizeHostingApplicationDatabaseEffect checks current phase and fixed lease expiry.
func (s *PostgresStore) AuthorizeHostingApplicationDatabaseEffect(ctx context.Context, runner, fingerprint string, in domain.HostingApplicationDatabaseEffectRequest) (domain.HostingApplicationDatabaseEffectAuthorization, error) {
	var zero domain.HostingApplicationDatabaseEffectAuthorization
	var err error
	if in.Validate() != nil {
		return zero, ErrConflict
	}
	ctx, err = BindHostingDatabaseConnectionRunnerContext(ctx, runner, fingerprint)
	if err != nil {
		return zero, err
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingApplicationDatabaseEffectAuthorization, error) {
		op, err := connectionRunnerOperation(ctx, tx, runner, in.HostingAuthority, true)
		if err != nil {
			return zero, err
		}
		if op.ApplicationDatabaseRouteContinuity != nil {
			if in.Action == "retire" {
				if err = continuityFinalizeAuthority(ctx, tx, runner, in.HostingAuthority, op, false); err != nil {
					return zero, err
				}
			} else if op.ApplicationDatabaseYield != nil || in.RunID != op.RunID {
				return zero, ErrConflict
			}
		}
		if op.State != "applying" && op.State != "verifying" || !op.MutationHold {
			return zero, ErrConflict
		}
		q := sqlcgen.New(tx)
		if _, err = q.LockHostingOperationState(ctx, op.ID); err != nil {
			return zero, err
		}
		stage, err := connectionStage(ctx, q, op)
		if err != nil {
			return zero, err
		}
		_, retirement, err := connectionRetirement(ctx, q, op)
		if err != nil || !connectionEffectStage(in.Action, in.ExpectedStageHash, stage, retirement) {
			return zero, ErrConflict
		}
		mode := "deploy"
		if op.ApplicationDatabaseRouteContinuity != nil && in.Action == "retire" {
			mode = "finalize"
		}
		if err = hostingRequireMode(ctx, tx, in.HostingAuthority, mode); err != nil {
			return zero, err
		}
		out := domain.HostingApplicationDatabaseEffectAuthorization{AuthorizationID: in.AuthorizationID, OperationID: op.ID, PlanHash: op.Plan.Hash, Action: in.Action, StageHash: in.ExpectedStageHash, LeaseID: in.LeaseID, Attempt: in.Attempt, Fence: in.Fence, MembershipRevision: op.ApplicationDatabase.MembershipRevision}
		old, err := q.ReadApplicationDatabaseEffectAuthorization(ctx, in.AuthorizationID)
		if err == nil {
			if old.OperationID != op.ID || old.RequestHash != domain.HostingDocumentHash(in) || !connectionJSONMatches(old.Request, in) {
				return zero, ErrConflict
			}
			out.UseUntil = old.UseUntil
			if !connectionJSONMatches(old.Response, out) {
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
			if err = q.InsertApplicationDatabaseEffectAuthorization(ctx, sqlcgen.InsertApplicationDatabaseEffectAuthorizationParams{AuthorizationID: in.AuthorizationID, OperationID: op.ID, Request: buildJSON(in), RequestHash: domain.HostingDocumentHash(in), Response: buildJSON(out), UseUntil: out.UseUntil}); err != nil {
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

// HostingApplicationDatabaseRetirementPlan durably records the exact predecessor intent.
func (s *PostgresStore) HostingApplicationDatabaseRetirementPlan(ctx context.Context, runner, fingerprint string, a domain.HostingAuthority) (domain.HostingDatabaseBindingEndpointPlan, error) {
	var zero domain.HostingDatabaseBindingEndpointPlan
	var err error
	ctx, err = BindHostingDatabaseConnectionRunnerContext(ctx, runner, fingerprint)
	if err != nil {
		return zero, err
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseBindingEndpointPlan, error) {
		op, err := connectionRunnerOperation(ctx, tx, runner, a, false)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		if _, err = q.LockHostingOperationState(ctx, op.ID); err != nil {
			return zero, err
		}
		if op.ApplicationDatabaseRouteContinuity != nil {
			if err = continuityFinalizeAuthority(ctx, tx, runner, a, op, false); err != nil {
				return zero, err
			}
		}
		old, receipt, err := connectionRetirement(ctx, q, op)
		if err != nil {
			return zero, err
		}
		if old != nil {
			// Existing intent is evidence, never renewed retirement permission.
			_ = receipt
			return *old, databaseConnectionHistoricalRunnerRead(ctx, tx, runner, op)
		}
		stage, err := connectionStage(ctx, q, op)
		proof := op.ApplicationDatabase
		if err != nil || stage == nil || stage.Phase != "ready" || len(proof.Members) != 1 || !op.MutationHold || op.State != "applying" && op.State != "verifying" {
			return zero, ErrConflict
		}
		member := proof.Members[0]
		endpoint, ok := member.Receipt.ObservedMembership(member.EndpointPlan, proof.Binding)
		if !ok {
			return zero, ErrConflict
		}
		plan := domain.HostingDatabaseBindingEndpointPlan{Version: domain.HostingDatabaseBindingVersion, OperationID: op.ID, BindingHash: proof.Binding.Hash, MembershipRevision: proof.MembershipRevision + 2, Action: "retire", Target: endpoint.Target, ExpectedEndpointID: endpoint.EndpointID}
		plan.Hash = plan.DocumentHash()
		intent := domain.HostingDatabaseBindingEndpointReceipt{Version: domain.HostingDatabaseBindingVersion, ReceiptID: "retire_intent_" + domain.HostingDocumentHash(op.ID)[7:39], OperationID: op.ID, PlanHash: plan.Hash, BindingHash: proof.Binding.Hash, MembershipRevision: plan.MembershipRevision, Phase: "retire_intent", Intent: &endpoint}
		intent.Hash = intent.DocumentHash()
		if plan.ValidateApplication(proof.Binding, member.ApplicationHealthy) != nil || intent.ValidateTransition(nil, plan, proof.Binding) != nil {
			return zero, ErrConflict
		}
		if err = q.InsertApplicationDatabaseRetirement(ctx, sqlcgen.InsertApplicationDatabaseRetirementParams{OperationID: op.ID, Phase: intent.Phase, Plan: buildJSON(plan), Receipt: buildJSON(intent), ReceiptID: intent.ReceiptID, ReceiptHash: intent.Hash, Authority: buildJSON(a)}); err != nil {
			return zero, err
		}
		return plan, finalDatabaseConnectionAuthority(ctx, tx, runner, a, op, false, false)
	})
}

// HostingApplicationDatabaseRetirement records detachment under the admitted intent.
func (s *PostgresStore) HostingApplicationDatabaseRetirement(ctx context.Context, runner, fingerprint string, in domain.HostingApplicationDatabaseRetirementRequest) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	var err error
	ctx, err = BindHostingDatabaseConnectionRunnerContext(ctx, runner, fingerprint)
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
		if op.ApplicationDatabaseRouteContinuity != nil {
			if err = continuityFinalizeAuthority(ctx, tx, runner, in.HostingAuthority, op, false); err != nil {
				return zero, err
			}
		}
		plan, previous, err := connectionRetirement(ctx, q, op)
		if err != nil || plan == nil || previous == nil || op.Plan.Hash != in.PlanHash || in.Receipt.PlanHash != plan.Hash {
			return zero, ErrConflict
		}
		if previous.Hash == in.Receipt.Hash {
			if !networkCaptureDocumentEqual(*previous, in.Receipt) {
				return zero, ErrConflict
			}
			return op, databaseConnectionHistoricalRunnerRead(ctx, tx, runner, op)
		}
		if in.Receipt.Phase != "detached" || in.Receipt.ValidateTransition(previous, *plan, op.ApplicationDatabase.Binding) != nil {
			return zero, ErrConflict
		}
		if err = q.InsertApplicationDatabaseRetirement(ctx, sqlcgen.InsertApplicationDatabaseRetirementParams{OperationID: op.ID, Phase: in.Receipt.Phase, Plan: buildJSON(plan), Receipt: buildJSON(in.Receipt), ReceiptID: in.Receipt.ReceiptID, ReceiptHash: in.Receipt.Hash, Authority: buildJSON(in.HostingAuthority)}); err != nil {
			return zero, err
		}
		return op, finalDatabaseConnectionAuthority(ctx, tx, runner, in.HostingAuthority, op, false, false)
	})
}

package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

// Completion settles the operation, healthy pointer, member revision and hold in
// one transaction. A failed or uncertain attempt never releases the DB hold.
func (s *PostgresStore) completeDatabaseConnection(ctx context.Context, tx pgx.Tx, runner string, in domain.HostingCompleteRequest, op domain.HostingOperation) (domain.HostingOperation, error) {
	var zero domain.HostingOperation
	q := sqlcgen.New(tx)
	if err := hostingRequireMode(ctx, tx, in.HostingAuthority, "deploy"); err != nil {
		return zero, err
	}
	if err := finalDatabaseConnectionAuthority(ctx, tx, runner, in.HostingAuthority, op, false, false); err != nil {
		return zero, err
	}
	if _, err := q.LockHostingOperationState(ctx, op.ID); err != nil {
		return zero, err
	}
	if op.State == "succeeded" || op.State == "failed" || op.State == "canceled" || op.State == "reconciliation_required" {
		return zero, ErrConflict
	}
	execution, err := q.ReadDatabaseConnectionExecution(ctx, op.ID)
	if err != nil {
		return zero, err
	}
	c, err := q.ReadDatabaseConnectionByID(ctx, execution.ConnectionID)
	if err != nil {
		return zero, err
	}
	b, err := readDatabaseBinding(ctx, q, c.ApplicationResourceID)
	if err != nil {
		return zero, err
	}
	var stage *domain.HostingApplicationDatabaseStage
	if in.Result.Outcome == "succeeded" {
		if !op.MutationHold || op.Plan == nil || op.Release == nil || !in.Result.Ready || in.Result.Uncertain || in.Result.Restored || in.Result.PlanHash != op.Plan.Hash || in.Result.DaemonID != op.Release.DaemonID || in.Result.ImageID != op.Release.ImageID || in.Result.ContainerID == "" || op.State != "applying" && op.State != "verifying" {
			return zero, ErrConflict
		}
		if err = replayHostingApplyAdmission(ctx, tx, in.HostingAuthority, op); err != nil {
			return zero, err
		}
		if op.ApplicationDatabase != nil {
			stage, err = connectionStage(ctx, q, op)
			if err != nil || stage == nil || stage.Phase != "ready" || in.Result.ContainerID != stage.Candidate.ContainerID || in.Result.ImageID != stage.Candidate.ImageID || in.Result.DaemonID != stage.Candidate.DaemonID {
				return zero, ErrConflict
			}
			if len(op.ApplicationDatabase.Members) == 1 {
				_, receipt, e := connectionRetirement(ctx, q, op)
				if e != nil || receipt == nil || receipt.Phase != "detached" {
					return zero, ErrConflict
				}
			}
		} else if err = hostingStorageWriterReady(ctx, tx, in.HostingAuthority, op); err != nil {
			return zero, err
		}
	} else if in.Result.Ready || in.Result.Restored || !op.MutationHold && in.Result.Uncertain {
		return zero, ErrConflict
	}
	if err = insertHostingReceipt(ctx, tx, in.HostingAuthority, "complete", in.ReceiptID, in.Result); err != nil {
		return zero, err
	}
	state := "succeeded"
	if in.Result.Outcome != "succeeded" {
		state = "reconciliation_required"
	}
	if _, err = q.SetHostingOperationState(ctx, sqlcgen.SetHostingOperationStateParams{ID: op.ID, State: state}); err != nil {
		return zero, err
	}
	if _, err = q.CompleteHostingLease(ctx, sqlcgen.CompleteHostingLeaseParams{ID: in.LeaseID, Status: in.Result.Outcome, CompletionKey: &in.ReceiptID}); err != nil {
		return zero, err
	}
	if _, err = q.CompleteHostingTask(ctx, sqlcgen.CompleteHostingTaskParams{ID: op.RunID, Status: in.Result.Outcome}); err != nil {
		return zero, err
	}
	if state == "succeeded" {
		healthy := domain.HostingHealthy{Snapshot: op.Snapshot, OperationID: op.ID, ContainerID: in.Result.ContainerID, Release: *op.Release, PlanHash: op.Plan.Hash}
		healthy.Storage, err = hostingStorageHealthyEvidence(ctx, tx, op)
		if err != nil {
			return zero, err
		}
		if op.ApplicationDatabase != nil {
			if op.Plan.PreviousHealthy != nil {
				if _, err = q.RetainPreviousHostingRelease(ctx, sqlcgen.RetainPreviousHostingReleaseParams{OperationID: op.Plan.PreviousHealthy.OperationID, RetainedRunning: false}); err != nil {
					return zero, err
				}
			}
			if _, err = q.RecordHostingHealthyRelease(ctx, sqlcgen.RecordHostingHealthyReleaseParams{OperationID: op.ID, BindingID: op.BindingID, DestinationID: op.Snapshot.DestinationID, HostPort: int32(op.Snapshot.HostPort), Body: buildJSON(healthy)}); err != nil {
				return zero, err
			}
			if _, err = q.RetainHostingReleaseClaim(ctx, op.ID); err != nil {
				return zero, err
			}
		}
		if _, err = q.SetHostingHealthy(ctx, sqlcgen.SetHostingHealthyParams{BindingID: op.BindingID, Healthy: buildJSON(healthy), HoldOperationID: &op.ID}); err != nil {
			return zero, err
		}
		if op.ApplicationDatabase != nil {
			if err = settleDatabaseMembership(ctx, tx, op, *stage, healthy); err != nil {
				return zero, err
			}
		}
		if _, err = q.ClearHostingMutationHold(ctx, sqlcgen.ClearHostingMutationHoldParams{BindingID: op.BindingID, HoldOperationID: &op.ID}); err != nil {
			return zero, err
		}
		if err = casBuild(q.ReleaseDatabaseNetworkHold(ctx, sqlcgen.ReleaseDatabaseNetworkHoldParams{DatabaseBindingID: b.DatabaseBindingID, ConnectionID: c.ID, OperationID: &op.ID})); err != nil {
			return zero, err
		}
		if c.State == "application_requested" && op.ID == c.ApplicationOperationID {
			if err = casBuild(q.AdvanceDatabaseConnection(ctx, sqlcgen.AdvanceDatabaseConnectionParams{ID: c.ID, State: "active", ReasonCode: "active", State_2: c.State})); err != nil {
				return zero, err
			}
		}
	} else if c.State != "held" {
		// Active connection rows remain active, with the independently visible
		// operation-owned hold. Initial failed workflows become held.
		if c.State != "active" {
			if err = casBuild(q.AdvanceDatabaseConnection(ctx, sqlcgen.AdvanceDatabaseConnectionParams{ID: c.ID, State: "held", ReasonCode: "uncertainty_held", State_2: c.State})); err != nil {
				return zero, err
			}
		}
	}
	if err = hostingPhaseAudit(ctx, tx, s.queries, op, runner, "completed"); err != nil {
		return zero, err
	}
	op, err = hostingTaskOperation(ctx, tx, in.HostingAuthority)
	if err != nil {
		return zero, err
	}
	if err = finalDatabaseConnectionAuthority(ctx, tx, runner, in.HostingAuthority, op, false, true); err != nil {
		return zero, err
	}
	return op, tx.Commit(ctx)
}

func settleDatabaseMembership(ctx context.Context, tx pgx.Tx, op domain.HostingOperation, stage domain.HostingApplicationDatabaseStage, healthy domain.HostingHealthy) error {
	q := sqlcgen.New(tx)
	p := op.ApplicationDatabase
	head, err := q.LockDatabaseMembershipHead(ctx, p.Binding.DatabaseBindingID)
	if err != nil || head.Revision != p.MembershipRevision || stage.EndpointReceipt == nil {
		return ErrConflict
	}
	member := domain.HostingDatabaseNetworkMemberEvidence{Binding: p.Binding, EndpointPlan: stage.AttachPlan, Receipt: *stage.EndpointReceipt, ApplicationHealthy: healthy, DatabasePlan: p.BindingDatabasePlan, ApplicationProjectID: p.ApplicationProjectID, DatabaseProjectID: p.DatabaseProjectID}
	input, err := domain.HostingApplicationDatabaseExecutionPlanInput(*p)
	if err != nil {
		return err
	}
	// Validate the member through the same closed network compiler used by
	// maintenance, before any new membership evidence is published.
	if _, ok := member.Receipt.ObservedMembership(member.EndpointPlan, member.Binding); !ok || member.EndpointPlan.ValidateApplication(member.Binding, healthy) != nil {
		return ErrConflict
	}
	var retired []domain.HostingDatabaseNetworkRetirementEvidence
	if json.Unmarshal(head.Retired, &retired) != nil || retired == nil {
		return ErrConflict
	}
	revision := p.MembershipRevision + 1
	if err = q.InsertDatabaseMembershipReceipt(ctx, sqlcgen.InsertDatabaseMembershipReceiptParams{DatabaseBindingID: head.DatabaseBindingID, Revision: revision, OperationID: op.ID, Action: "attach", Body: buildJSON(member), BodyHash: domain.HostingDocumentHash(member)}); err != nil {
		return err
	}
	if len(input.Members) == 1 {
		plan, detached, e := connectionRetirement(ctx, q, op)
		intent, ie := q.ReadApplicationDatabaseRetirementPhase(ctx, sqlcgen.ReadApplicationDatabaseRetirementPhaseParams{OperationID: op.ID, Phase: "retire_intent"})
		var intentReceipt domain.HostingDatabaseBindingEndpointReceipt
		if e != nil || ie != nil || plan == nil || detached == nil || detached.Phase != "detached" || json.Unmarshal(intent.Receipt, &intentReceipt) != nil || detached.ValidateTransition(&intentReceipt, *plan, p.Binding) != nil {
			return ErrConflict
		}
		evidence := domain.HostingDatabaseNetworkRetirementEvidence{Attached: p.Members[0], RetirePlan: *plan, RetireIntent: intentReceipt, Detached: *detached}
		retired = append(retired, evidence)
		revision++
		if err = q.InsertDatabaseMembershipReceipt(ctx, sqlcgen.InsertDatabaseMembershipReceiptParams{DatabaseBindingID: head.DatabaseBindingID, Revision: revision, OperationID: op.ID, Action: "retire", Body: buildJSON(evidence), BodyHash: domain.HostingDocumentHash(evidence)}); err != nil {
			return err
		}
	}
	return casBuild(q.AdvanceDatabaseMembershipHead(ctx, sqlcgen.AdvanceDatabaseMembershipHeadParams{DatabaseBindingID: head.DatabaseBindingID, Revision: revision, Members: buildJSON([]domain.HostingDatabaseNetworkMemberEvidence{member}), Retired: buildJSON(retired), Revision_2: head.Revision}))
}

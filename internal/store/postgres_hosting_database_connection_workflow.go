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

type databaseConnectionStep struct {
	connection sqlcgen.HostingDatabaseConnection
	input      domain.HostingDatabaseConnectionInput
	upgrade    domain.HostingDatabaseUpgradeInput
	action     string
}

func (h deploymentOnlyHandler) PrepareHostingDatabaseConnection(ctx context.Context, id string) error {
	return h.store.PrepareHostingDatabaseConnection(ctx, id)
}

// PrepareHostingDatabaseConnection advances at most four persisted boundaries.
// Child identities and inputs remain fixed across job loss and response loss.
// This controller never invokes Docker or expands a credential value.
func (s *PostgresStore) PrepareHostingDatabaseConnection(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	for range 4 {
		step, err := s.databaseConnectionStep(ctx, id)
		if err != nil {
			return err
		}
		c := step.connection
		audit := domain.AuditEvent{ActorID: c.CreatedBy, CreatedAt: time.Now().UTC()}
		switch step.action {
		case "":
			return nil
		case "wait":
			return hostingjobs.ErrDatabaseConnectionWaiting
		case "continue":
			continue
		case "capture":
			if _, err = s.RequestHostingDatabaseNetworkCapture(ctx, c.SourceOperationID, c.CreatedBy, audit); err != nil {
				return err
			}
		case "upgrade":
			audit.ID = "aud_connection_upgrade_" + c.UpgradeOperationID
			if _, err = s.RequestHostingDatabaseNetworkUpgrade(ctx, c.UpgradeOperationID, c.DatabaseResourceID, c.CreatedBy, step.upgrade, audit); err != nil {
				return err
			}
		case "application":
			in := step.input.Deployment
			in.ExpectedRuntimeConfigurationGeneration = step.input.Binding.ExpectedRuntimeGeneration + 1
			if _, err = s.RequestHostingApplicationDatabaseDeployment(ctx, c.ApplicationOperationID, c.ApplicationResourceID, c.CreatedBy, in, audit); err != nil {
				return err
			}
		default:
			return ErrConflict
		}
	}
	return hostingjobs.ErrDatabaseConnectionWaiting
}

func (s *PostgresStore) databaseConnectionStep(ctx context.Context, id string) (databaseConnectionStep, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (databaseConnectionStep, error) {
		var step databaseConnectionStep
		q := sqlcgen.New(tx)
		if err := databaseConnectionSchema(ctx, q); err != nil {
			return step, err
		}
		before, err := q.ReadDatabaseConnectionByID(ctx, id)
		if err != nil {
			return step, err
		}
		if before.State == "active" || before.State == "held" {
			return step, nil
		}
		binding, err := lockConnectionTargets(ctx, tx, before, before.CreatedBy, domain.RoleOwner)
		if err != nil {
			return step, err
		}
		c, err := q.LockDatabaseConnection(ctx, id)
		if err != nil {
			return step, err
		}
		step.connection = c
		if c.State == "active" || c.State == "held" {
			return step, nil
		}
		if json.Unmarshal(c.AcceptedRequest, &step.input) != nil {
			return step, ErrConflict
		}
		if err = requireConnectionAgent(ctx, tx, binding.RunnerID, true, binding.Database.Engine); err != nil {
			return step, err
		}
		advance := func(state, reason string) error {
			if err := casBuild(q.AdvanceDatabaseConnection(ctx, sqlcgen.AdvanceDatabaseConnectionParams{ID: c.ID, State: state, ReasonCode: reason, State_2: c.State})); err != nil {
				return err
			}
			c.State, c.ReasonCode = state, reason
			step.connection = c
			return nil
		}
		hold, err := q.LockDatabaseNetworkHold(ctx, binding.DatabaseBindingID)
		if err != nil || hold.ConnectionID != c.ID || hold.OperationID == nil {
			return step, ErrConflict
		}
		switch c.State {
		case "binding_saved":
			if *hold.OperationID != c.UpgradeOperationID {
				return step, ErrConflict
			}
			exists, e := connectionCapturePrerequisite(ctx, tx, c.SourceOperationID, binding.DatabaseBindingID, c.CreatedBy)
			if e != nil {
				return step, e
			}
			if exists {
				if err = advance("capture_waiting", "capture_waiting"); err != nil {
					return step, err
				}
				step.action = "continue"
				break
			}
			if err = advance("capture_requested", "capture_required"); err != nil {
				return step, err
			}
			step.action = "capture"
		case "capture_requested", "capture_waiting":
			capture, e := readNetworkCapture(ctx, q, c.CaptureID)
			if errors.Is(e, ErrNotFound) {
				step.action = "capture"
			} else if e != nil {
				return step, e
			} else if capture.Receipt == nil {
				if c.State != "capture_waiting" {
					if err = advance("capture_waiting", "capture_waiting"); err != nil {
						return step, err
					}
				}
				step.action = "wait"
			} else {
				if capture.Plan.SourceOperationID != c.SourceOperationID || capture.Plan.BindingID != binding.DatabaseBindingID || *hold.OperationID != c.UpgradeOperationID {
					return step, ErrConflict
				}
				step.upgrade = domain.HostingDatabaseUpgradeInput{ExpectedGeneration: step.input.Binding.ExpectedDatabaseGeneration, CaptureID: c.CaptureID, ExpectedOriginHash: capture.Receipt.OriginHash, IdempotencyKey: "connection_upgrade_" + c.ID}
				if err = advance("upgrade_requested", "upgrade_waiting"); err != nil {
					return step, err
				}
				step.action = "upgrade"
			}
		case "upgrade_requested", "upgrade_waiting":
			op, e := hostingOperation(ctx, tx, c.UpgradeOperationID)
			if errors.Is(e, ErrNotFound) {
				capture, err := readNetworkCapture(ctx, q, c.CaptureID)
				if err != nil || capture.Receipt == nil {
					return step, ErrConflict
				}
				step.upgrade = domain.HostingDatabaseUpgradeInput{ExpectedGeneration: step.input.Binding.ExpectedDatabaseGeneration, CaptureID: c.CaptureID, ExpectedOriginHash: capture.Receipt.OriginHash, IdempotencyKey: "connection_upgrade_" + c.ID}
				step.action = "upgrade"
			} else if e != nil {
				return step, e
			} else if op.State == "succeeded" {
				if op.DatabaseUpgrade == nil || op.Plan == nil || domain.ValidateHostingDatabasePlanV2(*op.Plan) != nil || op.Plan.Database.Network.MembershipRevision != 1 || len(op.Plan.Database.Network.Members) != 0 || op.DatabaseUpgrade.CapturePlan.OperationID != c.CaptureID || *hold.OperationID != c.UpgradeOperationID {
					return step, ErrConflict
				}
				current, _, err := databaseUpgradeHealthy(ctx, tx, c.DatabaseResourceID, binding.DatabaseBindingID)
				if err != nil || current.ID != op.ID {
					return step, ErrConflict
				}
				if err = q.InsertDatabaseMembershipHead(ctx, sqlcgen.InsertDatabaseMembershipHeadParams{DatabaseBindingID: binding.DatabaseBindingID, ConnectionID: c.ID, OriginReceiptID: op.DatabaseUpgrade.OriginReceipt.ID, Revision: 1}); err != nil {
					return step, err
				}
				if err = casBuild(q.AssignDatabaseNetworkHold(ctx, sqlcgen.AssignDatabaseNetworkHoldParams{DatabaseBindingID: binding.DatabaseBindingID, ConnectionID: c.ID, PreviousOperation: &c.UpgradeOperationID, NextOperation: &c.ApplicationOperationID})); err != nil {
					return step, err
				}
				if err = advance("application_requested", "application_waiting"); err != nil {
					return step, err
				}
				step.action = "application"
			} else if op.State == "failed" || op.State == "canceled" || op.State == "reconciliation_required" {
				if err = advance("held", "child_failed"); err != nil {
					return step, err
				}
			} else {
				if c.State != "upgrade_waiting" {
					if err = advance("upgrade_waiting", "upgrade_waiting"); err != nil {
						return step, err
					}
				}
				step.action = "wait"
			}
		case "application_requested":
			op, e := hostingOperation(ctx, tx, c.ApplicationOperationID)
			if errors.Is(e, ErrNotFound) {
				step.action = "application"
			} else if e != nil {
				return step, e
			} else if op.State == "succeeded" {
				// Completion itself atomically records membership and releases the
				// hold; this branch only reports the already committed settlement.
				if err = advance("active", "active"); err != nil {
					return step, err
				}
			} else if op.State == "failed" || op.State == "canceled" || op.State == "reconciliation_required" {
				if err = advance("held", "uncertainty_held"); err != nil {
					return step, err
				}
			} else {
				step.action = "wait"
			}
		default:
			return step, ErrConflict
		}
		return step, finalizeConnectionRequest(ctx, tx, c, binding, c.CreatedBy, domain.RoleOwner, true)
	})
}

func (s *PostgresStore) recoverDatabaseConnectionDispatch(ctx context.Context, tx pgx.Tx) error {
	if s.hostingJobs == nil || !s.hostingJobs.DatabaseConnectionEnabled() {
		return nil
	}
	q := sqlcgen.New(tx)
	present, err := q.DatabaseConnectionSchemaPresent(ctx)
	if err != nil || !present {
		return err
	}
	ids, err := q.PendingDatabaseConnections(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = s.hostingJobs.EnqueueDatabaseConnectionTx(ctx, tx, hostingjobs.DatabaseConnectionArgs{ConnectionID: id}); err != nil {
			return err
		}
		if err = q.TouchDatabaseConnection(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

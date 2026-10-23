package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

var _ ServerBootstrapRepository = (*PostgresStore)(nil)

func bootstrapID(prefix string) (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}
func bootstrapOperation(row sqlcgen.ServerBootstrapOperation) (domain.ServerBootstrapOperation, error) {
	var out domain.ServerBootstrapOperation
	err := json.Unmarshal(row.Body, &out)
	out.Version = row.Version
	out.State = row.State
	out.RemoteHold = row.RemoteHold
	out.RestoreHeld = row.RestoreHeld
	out.CreatedAt = row.CreatedAt
	return out, err
}
func bootstrapCommand(row sqlcgen.ServerBootstrapCommand) (domain.ServerBootstrapCommand, error) {
	var out domain.ServerBootstrapCommand
	err := json.Unmarshal(row.Body, &out)
	return out, err
}
func bootstrapStage(row sqlcgen.ServerBootstrapCommand) (domain.ServerBootstrapStage, error) {
	c, err := bootstrapCommand(row)
	out := domain.ServerBootstrapStage{Command: c, LeaseID: row.LeaseID, Attempt: row.Attempt, Fence: row.Fence, EffectIntent: row.EffectIntent}
	if row.LeaseExpiresAt != nil {
		out.ExpiresAt = *row.LeaseExpiresAt
	}
	return out, err
}
func bootstrapKey(key string) bool {
	return len(key) > 0 && len(key) <= 128 && strings.TrimSpace(key) == key && !strings.ContainsAny(key, "\x00\r\n")
}
func bootstrapAudit(ctx context.Context, q *sqlcgen.Queries, event domain.AuditEvent, actor, action, id string) error {
	if event.ActorID != actor {
		return ErrConflict
	}
	event.Action = "server_bootstrap." + action
	event.TargetID = id
	event.Metadata = map[string]any{"operation_id": id}
	return createAuditWithQueries(ctx, q, event)
}

// CreateServerBootstrap reserves stable identities but creates no enrollment or destination.
func (s *PostgresStore) CreateServerBootstrap(ctx context.Context, actor, key string, in domain.ServerBootstrapInput, event domain.AuditEvent) (domain.ServerBootstrapOperation, error) {
	var zero domain.ServerBootstrapOperation
	if in.Validate() != nil || !bootstrapKey(key) || event.ActorID != actor {
		return zero, ErrConflict
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.ServerBootstrapOperation, error) {
		q := sqlcgen.New(tx)
		if err := proxyAuthorize(ctx, tx, actor, in.ProjectID, domain.RoleOwner, true); err != nil {
			return zero, err
		}
		if err := q.BootstrapSerialize(ctx, domain.HostingDocumentHash([]string{actor, in.ProjectID, key})); err != nil {
			return zero, err
		}
		hash := domain.HostingDocumentHash(in)
		existing, err := q.BootstrapFindRequest(ctx, sqlcgen.BootstrapFindRequestParams{ActorID: actor, ProjectID: in.ProjectID, RequestKey: key})
		if err == nil {
			if existing.RequestHash != hash {
				return zero, ErrConflict
			}
			out, e := bootstrapOperation(existing)
			if e != nil {
				return zero, e
			}
			return out, proxyCheckLegacyToken(ctx, tx, actor)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		target := net.JoinHostPort(in.Target.IP, strconv.Itoa(int(in.Target.Port)))
		if err = q.BootstrapSerialize(ctx, "target:"+target); err != nil {
			return zero, err
		}
		secret, err := q.LockManagedSecret(ctx, in.SSHSecretID)
		if err != nil {
			return zero, hostingError(err)
		}
		if secret.ProjectID != in.ProjectID || secret.Status != "active" {
			return zero, ErrConflict
		}
		if _, err = q.GetManagedSecretRecord(ctx, sqlcgen.GetManagedSecretRecordParams{SecretID: in.SSHSecretID, Version: in.SSHSecretVersion}); err != nil {
			return zero, hostingError(err)
		}
		ids := domain.ServerBootstrapIDs{}
		for _, x := range []struct {
			p string
			v *string
		}{{"sbo_", &ids.OperationID}, {"", &ids.InstallationID}, {"", &ids.InstallOperationID}, {"runner_", &ids.RunnerID}, {"enroll_", &ids.EnrollmentID}, {"sbcred_", &ids.CredentialReferenceID}, {"srv_", &ids.ServerID}, {"dest_", &ids.DestinationID}} {
			*x.v, err = bootstrapID(x.p)
			if err != nil {
				return zero, err
			}
		}
		out := domain.ServerBootstrapOperation{IDs: ids, Input: in, CreatedBy: actor, RequestHash: hash, Version: 1, State: "draft"}
		body, err := json.Marshal(out)
		if err != nil {
			return zero, err
		}
		row, err := q.BootstrapCreate(ctx, sqlcgen.BootstrapCreateParams{ID: ids.OperationID, ProjectID: in.ProjectID, ActorID: actor, RequestKey: key, RequestHash: hash, Target: target, Body: body})
		if err != nil {
			return zero, hostingError(err)
		}
		err = q.BootstrapGrantCreate(ctx, sqlcgen.BootstrapGrantCreateParams{OperationID: ids.OperationID, PrincipalID: actor, TargetHash: domain.HostingDocumentHash(in.Target), SecretID: in.SSHSecretID, SecretVersion: in.SSHSecretVersion, ExpiresAt: row.CreatedAt.Add(domain.ServerBootstrapMaxLifetime)})
		if err != nil {
			return zero, err
		}
		if err = bootstrapAudit(ctx, q, event, actor, "created", ids.OperationID); err != nil {
			return zero, err
		}
		out, err = bootstrapOperation(row)
		if err != nil {
			return zero, err
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}

// GetServerBootstrap rechecks current administrator and project owner authority.
func (s *PostgresStore) GetServerBootstrap(ctx context.Context, actor, id string) (domain.ServerBootstrapOperation, error) {
	// Bound the entire metadata request, including pool, authority and SHARE waits.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.ServerBootstrapOperation, error) {
		q := sqlcgen.New(tx)
		row, err := q.BootstrapGet(ctx, id)
		if err != nil {
			return domain.ServerBootstrapOperation{}, hostingError(err)
		}
		if err = proxyAuthorize(ctx, tx, actor, row.ProjectID, domain.RoleOwner, true); err != nil {
			return domain.ServerBootstrapOperation{}, err
		}
		row, err = q.BootstrapReadLock(ctx, id)
		if err != nil {
			return domain.ServerBootstrapOperation{}, hostingError(err)
		}
		out, err := bootstrapOperation(row)
		if err != nil {
			return out, err
		}
		if err = bootstrapProjection(ctx, q, &out); err != nil {
			return out, err
		}
		expired, err := q.BootstrapExpiredIntent(ctx, id)
		if err != nil {
			return out, err
		}
		if expired && out.State == "running" {
			out.State = "uncertain"
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}

func bootstrapLock(ctx context.Context, tx pgx.Tx, actor, id string) (domain.ServerBootstrapOperation, error) {
	q := sqlcgen.New(tx)
	row, err := q.BootstrapGet(ctx, id)
	if err != nil {
		return domain.ServerBootstrapOperation{}, hostingError(err)
	}
	if err = proxyAuthorize(ctx, tx, actor, row.ProjectID, domain.RoleOwner, true); err != nil {
		return domain.ServerBootstrapOperation{}, err
	}
	if err = q.BootstrapSerialize(ctx, "target:"+row.Target); err != nil {
		return domain.ServerBootstrapOperation{}, err
	}
	row, err = q.BootstrapLock(ctx, id)
	if err != nil {
		return domain.ServerBootstrapOperation{}, err
	}
	return bootstrapOperation(row)
}

// RequestServerBootstrapCommand serializes exact actor/key/body replay before eligibility.
func (s *PostgresStore) RequestServerBootstrapCommand(ctx context.Context, actor, id string, in domain.ServerBootstrapCommand, event domain.AuditEvent) (domain.ServerBootstrapCommand, error) {
	var zero domain.ServerBootstrapCommand
	if !bootstrapKey(in.Key) || event.ActorID != actor || in.ID != "" || in.OperationID != "" || in.ActorID != "" || in.RequestHash != "" || !in.CreatedAt.IsZero() {
		return zero, ErrConflict
	}
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.ServerBootstrapCommand, error) {
		op, err := bootstrapLock(ctx, tx, actor, id)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		hash := domain.HostingDocumentHash(in)
		old, err := q.BootstrapCommandRequest(ctx, sqlcgen.BootstrapCommandRequestParams{OperationID: id, RequestKey: in.Key})
		if err == nil {
			if old.ActorID != actor || old.RequestHash != hash {
				return zero, ErrConflict
			}
			out, e := bootstrapCommand(old)
			if e != nil {
				return zero, e
			}
			return out, proxyCheckLegacyToken(ctx, tx, actor)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if op.RestoreHeld || op.Version != in.ExpectedVersion || !domain.ServerBootstrapActionAllowed(in.Action, op.State, op.RemoteHold) || actor != op.CreatedBy && in.Action != "inspect_owned" {
			return zero, ErrConflict
		}
		count, err := q.BootstrapCountCommands(ctx, id)
		if err != nil {
			return zero, err
		}
		if in.Action == "inspect_owned" {
			count, err = q.BootstrapCountInspections(ctx, id)
			if err != nil {
				return zero, err
			}
			if count >= domain.ServerBootstrapMaxInspections {
				return zero, ErrConflict
			}
		} else if in.Action != "cancel" && count >= domain.ServerBootstrapMaxCommands {
			return zero, ErrConflict
		}
		now, err := q.BootstrapClock(ctx)
		if err != nil {
			return zero, err
		}
		if !in.Deadline.After(now) || in.Deadline.After(now.Add(domain.ServerBootstrapMaxLifetime)) {
			return zero, ErrConflict
		}
		if in.Action == "review" {
			if in.Review == nil || in.Review.Validate(op, now) != nil {
				return zero, ErrConflict
			}
		} else if in.Review != nil {
			return zero, ErrConflict
		}
		if in.Action == "install" {
			if _, err = bootstrapReview(ctx, q, op, now); err != nil {
				return zero, err
			}
		}
		if in.Action == "cancel" {
			intent, e := q.BootstrapHasEffectIntent(ctx, id)
			if e != nil {
				return zero, e
			}
			if intent {
				return zero, ErrConflict
			}
			if err = q.BootstrapRetireReadStages(ctx, id); err != nil {
				return zero, err
			}
			if err = q.BootstrapRevokeUnusedEnrollment(ctx, id); err != nil {
				return zero, err
			}
		}
		in.ID, err = bootstrapID("sbc_")
		if err != nil {
			return zero, err
		}
		in.OperationID = id
		in.ActorID = actor
		in.RequestHash = hash
		in.CreatedAt = now
		body, err := json.Marshal(in)
		if err != nil || len(body) > 32768 {
			return zero, ErrConflict
		}
		_, err = q.BootstrapCommandCreate(ctx, sqlcgen.BootstrapCommandCreateParams{ID: in.ID, OperationID: id, ActorID: actor, RequestKey: in.Key, RequestHash: hash, Action: in.Action, Body: body, Deadline: in.Deadline, CreatedAt: now})
		if err != nil {
			return zero, hostingError(err)
		}
		if in.Action == "review" || in.Action == "cancel" {
			state := "reviewed"
			if in.Action == "cancel" {
				state = "canceled"
				if err = q.BootstrapRevokeGrant(ctx, id); err != nil {
					return zero, err
				}
			}
			if _, err = q.BootstrapUpdate(ctx, sqlcgen.BootstrapUpdateParams{ID: id, State: state}); err != nil {
				return zero, err
			}
			r, _ := json.Marshal(domain.ServerBootstrapReceipt{CommandID: in.ID, Outcome: "accepted"})
			if err = q.BootstrapCommandReceipt(ctx, sqlcgen.BootstrapCommandReceiptParams{ID: in.ID, Receipt: r}); err != nil {
				return zero, err
			}
		}
		if err = bootstrapAudit(ctx, q, event, actor, "command_requested", id); err != nil {
			return zero, err
		}
		now, err = q.BootstrapClock(ctx)
		if err != nil {
			return zero, err
		}
		if !in.Deadline.After(now) || in.Review != nil && in.Review.Validate(op, now) != nil {
			return zero, ErrConflict
		}
		return in, proxyCheckLegacyToken(ctx, tx, actor)
	})
}
func bootstrapReview(ctx context.Context, q *sqlcgen.Queries, op domain.ServerBootstrapOperation, now time.Time) (domain.ServerBootstrapReview, error) {
	var c domain.ServerBootstrapCommand
	raw, err := q.BootstrapLatestReview(ctx, op.IDs.OperationID)
	if err != nil {
		return domain.ServerBootstrapReview{}, hostingError(err)
	}
	if json.Unmarshal(raw, &c) != nil || c.Review == nil || c.Review.Validate(op, now) != nil {
		return domain.ServerBootstrapReview{}, ErrConflict
	}
	return *c.Review, nil
}

// ClaimServerBootstrapStage never treats an expired remote intent as retryable.
func (s *PostgresStore) ClaimServerBootstrapStage(ctx context.Context, actor, id, command string) (domain.ServerBootstrapStage, error) {
	var zero domain.ServerBootstrapStage
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.ServerBootstrapStage, error) {
		op, err := bootstrapLock(ctx, tx, actor, id)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		row, err := q.BootstrapCommandLock(ctx, sqlcgen.BootstrapCommandLockParams{OperationID: id, ID: command})
		if err != nil {
			return zero, hostingError(err)
		}
		now, err := q.BootstrapClock(ctx)
		if err != nil {
			return zero, err
		}
		if op.RestoreHeld || row.ActorID != actor || row.Receipt != nil || !row.Deadline.After(now) || row.Attempt >= domain.ServerBootstrapMaxAttempts || row.EffectIntent || row.LeaseExpiresAt != nil && row.LeaseExpiresAt.After(now) {
			return zero, ErrConflict
		}
		if !domain.ServerBootstrapActionAllowed(row.Action, op.State, op.RemoteHold) {
			return zero, ErrConflict
		}
		if row.Action == "install" {
			if _, err = bootstrapReview(ctx, q, op, now); err != nil {
				return zero, err
			}
		}
		lease, err := bootstrapID("sbl_")
		if err != nil {
			return zero, err
		}
		expires := now.Add(domain.ServerBootstrapLeaseLifetime)
		if expires.After(row.Deadline) {
			expires = row.Deadline
		}
		row, err = q.BootstrapCommandLease(ctx, sqlcgen.BootstrapCommandLeaseParams{ID: command, LeaseID: lease, LeaseExpiresAt: &expires})
		if err != nil {
			return zero, err
		}
		out, err := bootstrapStage(row)
		if err != nil {
			return zero, err
		}
		now, err = q.BootstrapClock(ctx)
		if err != nil {
			return zero, err
		}
		if !expires.After(now) {
			return zero, ErrConflict
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}

func bootstrapStageMatches(row sqlcgen.ServerBootstrapCommand, stage domain.ServerBootstrapStage, now time.Time) bool {
	return row.ID == stage.Command.ID && row.LeaseID == stage.LeaseID && row.Attempt == stage.Attempt && row.Fence == stage.Fence && row.Receipt == nil && row.LeaseExpiresAt != nil && row.LeaseExpiresAt.After(now) && row.Deadline.After(now)
}

// RenewServerBootstrapStage cannot extend the original command deadline.
func (s *PostgresStore) RenewServerBootstrapStage(ctx context.Context, actor, id string, in domain.ServerBootstrapStage) (domain.ServerBootstrapStage, error) {
	var zero domain.ServerBootstrapStage
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.ServerBootstrapStage, error) {
		op, err := bootstrapLock(ctx, tx, actor, id)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		row, err := q.BootstrapCommandLock(ctx, sqlcgen.BootstrapCommandLockParams{OperationID: id, ID: in.Command.ID})
		if err != nil {
			return zero, hostingError(err)
		}
		now, err := q.BootstrapClock(ctx)
		if err != nil {
			return zero, err
		}
		if op.RestoreHeld || row.ActorID != actor || !bootstrapStageMatches(row, in, now) {
			return zero, ErrConflict
		}
		expires := now.Add(domain.ServerBootstrapLeaseLifetime)
		if expires.After(row.Deadline) {
			expires = row.Deadline
		}
		row, err = q.BootstrapCommandRenew(ctx, sqlcgen.BootstrapCommandRenewParams{ID: row.ID, LeaseExpiresAt: &expires})
		if err != nil {
			return zero, err
		}
		out, err := bootstrapStage(row)
		if err != nil {
			return zero, err
		}
		now, err = q.BootstrapClock(ctx)
		if err != nil {
			return zero, err
		}
		if !expires.After(now) {
			return zero, ErrConflict
		}
		return out, proxyCheckLegacyToken(ctx, tx, actor)
	})
}

// InvalidateServerBootstrapForRestoreTx holds authority without relabeling workloads.
// The caller owns the enclosing post-restore transaction; no remote I/O occurs.
func InvalidateServerBootstrapForRestoreTx(ctx context.Context, tx pgx.Tx) error {
	q := sqlcgen.New(tx)
	for _, f := range []func(context.Context) error{q.BootstrapRestoreHold, q.BootstrapRestoreGrants, q.BootstrapRestoreLeases, q.BootstrapRestoreEnrollments} {
		if err := f(ctx); err != nil {
			return fmt.Errorf("hold restored bootstrap authority: %w", err)
		}
	}
	return nil
}

// bootstrapProjection returns all bounded history in creation/ID order. Active
// unfinished mutation intent is preferred to read-only inspection commands.
func bootstrapProjection(ctx context.Context, q *sqlcgen.Queries, op *domain.ServerBootstrapOperation) error {
	rows, err := q.BootstrapCommandHistory(ctx, op.IDs.OperationID)
	if err != nil {
		return err
	}
	for _, row := range rows {
		stage, e := bootstrapStage(row)
		if e != nil {
			return e
		}
		if row.Receipt != nil {
			var stored struct {
				Receipt *domain.ServerBootstrapReceipt `json:"receipt"`
			}
			if json.Unmarshal(row.Receipt, &stored) != nil {
				return ErrConflict
			}
			stage.Receipt = stored.Receipt
			if stage.Receipt == nil {
				var direct domain.ServerBootstrapReceipt
				if json.Unmarshal(row.Receipt, &direct) != nil {
					return ErrConflict
				}
				stage.Receipt = &direct
			}
		}
		op.Commands = append(op.Commands, stage)
		if stage.Command.Action == "review" && stage.Receipt != nil {
			op.Review = stage.Command.Review
		}
		if stage.Receipt == nil && (op.ActiveStage == nil || op.ActiveStage.Command.Action == "inspect_owned" && stage.Command.Action != "inspect_owned") {
			copy := stage
			op.ActiveStage = &copy
			op.ActiveCommandID = row.ID
		}
	}
	return nil
}

package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/distribution/reference"
	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store/sqlcgen"
)

// StartHostingExecution installs and starts the real River consumer. The server
// must call its stop function before closing the store pool.
func (s *PostgresStore) StartHostingExecution(ctx context.Context) (func(context.Context) error, error) {
	client, err := hostingjobs.New(ctx, s.pool, s, hostingjobs.Config{})
	if err != nil {
		return nil, err
	}
	if err = client.Start(ctx); err != nil {
		return nil, err
	}
	s.hostingJobs = client
	recoveryCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			recoveryAttempt, stop := context.WithTimeout(recoveryCtx, 3*time.Second)
			_ = s.recoverHostingDispatch(recoveryAttempt)
			stop()
			select {
			case <-recoveryCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return func(stopCtx context.Context) error {
		cancel()
		select {
		case <-done:
		case <-stopCtx.Done():
			return stopCtx.Err()
		}
		return client.Stop(stopCtx)
	}, nil
}

// Recovery discovers accepted operations independently from River job retention
// or finite retry exhaustion. Each bounded pass rotates the selected prefix.
func (s *PostgresStore) recoverHostingDispatch(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTransaction(ctx, tx)
	rows, err := tx.Query(ctx, `SELECT id FROM hosting_operations WHERE state='queued' AND run_id IS NULL ORDER BY updated_at,id LIMIT 100 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return err
	}
	ids := make([]string, 0, 100)
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if _, err = s.hostingJobs.EnqueueTx(ctx, tx, hostingjobs.PrepareDeploymentArgs{OperationID: id}); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `UPDATE hosting_operations SET updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

var errHostingRestoreHold = errors.New("hosting admission requires restored target ownership reconciliation")

const hostingOperationSelect = `SELECT o.id,o.project_id,o.resource_id,o.binding_id,coalesce(o.run_id,''),coalesce(o.reconciliation_run_id,''),o.state,o.reconcile_requested,o.created_by,o.created_at,o.updated_at,
 sn.body,rel.body,plan.body,
 (SELECT jsonb_build_object('id',receipt_id,'body_hash',body_hash,'created_at',created_at) FROM hosting_receipts WHERE operation_id=o.id AND phase='prepared'),
 (SELECT jsonb_build_object('id',receipt_id,'body_hash',body_hash,'created_at',created_at) FROM hosting_receipts WHERE operation_id=o.id AND phase='complete'),
 (SELECT body FROM hosting_receipts WHERE operation_id=o.id AND phase='complete'),
 EXISTS(SELECT 1 FROM hosting_binding_state bs WHERE bs.hold_operation_id=o.id)
 FROM hosting_operations o JOIN hosting_snapshots sn ON sn.operation_id=o.id
 LEFT JOIN hosting_releases rel ON rel.operation_id=o.id LEFT JOIN hosting_apply_plans plan ON plan.operation_id=o.id WHERE o.id=$1`

type hostingRowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func hostingOperation(ctx context.Context, q hostingRowQuerier, id string) (domain.HostingOperation, error) {
	var op domain.HostingOperation
	var snapshot, release, plan, prepared, complete, result []byte
	err := q.QueryRow(ctx, hostingOperationSelect, id).Scan(&op.ID, &op.ProjectID, &op.ResourceID, &op.BindingID, &op.RunID, &op.ReconciliationRunID, &op.State, &op.ReconcileRequested, &op.CreatedBy, &op.CreatedAt, &op.UpdatedAt, &snapshot, &release, &plan, &prepared, &complete, &result, &op.MutationHold)
	if err != nil {
		return op, hostingError(err)
	}
	for _, doc := range []struct {
		raw    []byte
		target any
	}{{snapshot, &op.Snapshot}, {release, &op.Release}, {plan, &op.Plan}, {prepared, &op.PreparedReceipt}, {complete, &op.CompleteReceipt}, {result, &op.Result}} {
		if len(doc.raw) > 0 {
			if err = json.Unmarshal(doc.raw, doc.target); err != nil {
				return domain.HostingOperation{}, fmt.Errorf("decode hosting evidence: %w", err)
			}
		}
	}
	return op, nil
}

func (s *PostgresStore) GetHostingOperation(ctx context.Context, id string) (domain.HostingOperation, error) {
	return hostingOperation(ctx, s.pool, id)
}

// RequestHostingDeployment captures desired state and inserts the River job in
// one transaction. Reusing a key with changed input is a conflict.
func (s *PostgresStore) RequestHostingDeployment(ctx context.Context, id, resourceID, actorID string, in domain.HostingDeploymentInput, audit domain.AuditEvent) (domain.HostingOperation, error) {
	if s.hostingJobs == nil {
		return domain.HostingOperation{}, hostingjobs.ErrUnavailable
	}
	if err := domain.ValidateHostingDeployment(in); err != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	var snap domain.HostingSnapshot
	var projectID string
	var source []byte
	err = tx.QueryRow(ctx, `SELECT r.project_id,r.generation,r.source,b.id,b.destination_id,b.runtime_namespace,d.server_id,s.runner_id
 FROM resources r JOIN resource_bindings b ON b.resource_id=r.id JOIN destinations d ON d.id=b.destination_id JOIN servers s ON s.id=d.server_id
 WHERE r.id=$1 FOR UPDATE OF r`, resourceID).Scan(&projectID, &snap.ResourceGeneration, &source, &snap.BindingID, &snap.DestinationID, &snap.RuntimeNamespace, &snap.ServerID, &snap.RunnerID)
	if err != nil {
		return domain.HostingOperation{}, hostingError(err)
	}
	var existingID, requestHash string
	err = tx.QueryRow(ctx, `SELECT id,request_hash FROM hosting_operations WHERE resource_id=$1 AND idempotency_key=$2`, resourceID, in.IdempotencyKey).Scan(&existingID, &requestHash)
	if err == nil {
		if requestHash != domain.HostingDocumentHash(in) {
			return domain.HostingOperation{}, ErrConflict
		}
		return hostingOperation(ctx, tx, existingID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.HostingOperation{}, err
	}
	if snap.ResourceGeneration != in.ExpectedGeneration {
		return domain.HostingOperation{}, ErrConflict
	}
	var desired domain.ResourceSource
	if err = json.Unmarshal(source, &desired); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = domain.NormalizeHostingImage(desired.Image.Reference); err != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	snap.ResourceID = resourceID
	snap.ImageReference = desired.Image.Reference
	snap.ContainerPort = in.ContainerPort
	snap.HostPort = in.HostPort
	snap.Readiness = in.Readiness
	snap.PolicyVersion = domain.HostingPolicyVersion
	if err = hostingTargetGate(ctx, tx, projectID, snap, true); err != nil {
		return domain.HostingOperation{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO hosting_binding_state(binding_id,destination_id,host_port) VALUES($1,$2,$3) ON CONFLICT(binding_id) DO NOTHING`, snap.BindingID, snap.DestinationID, snap.HostPort)
	if err != nil {
		return domain.HostingOperation{}, hostingError(err)
	}
	var reserved int
	var admitted bool
	var hold *string
	if err = tx.QueryRow(ctx, `SELECT host_port,restore_admitted,hold_operation_id FROM hosting_binding_state WHERE binding_id=$1 FOR UPDATE`, snap.BindingID).Scan(&reserved, &admitted, &hold); err != nil {
		return domain.HostingOperation{}, err
	}
	if reserved != snap.HostPort || !admitted || hold != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	var busy bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hosting_operations WHERE binding_id=$1 AND state NOT IN ('succeeded','failed','canceled'))`, snap.BindingID).Scan(&busy); err != nil {
		return domain.HostingOperation{}, err
	}
	if busy {
		return domain.HostingOperation{}, ErrConflict
	}
	_, err = tx.Exec(ctx, `INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) VALUES($1,$2,$3,$4,$5,$6,'queued',$7)`, id, projectID, resourceID, snap.BindingID, in.IdempotencyKey, domain.HostingDocumentHash(in), actorID)
	if err != nil {
		return domain.HostingOperation{}, hostingError(err)
	}
	contents, err := json.Marshal(snap)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO hosting_snapshots VALUES($1,$2,$3)`, id, contents, domain.HostingDocumentHash(snap)); err != nil {
		return domain.HostingOperation{}, err
	}
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
	if err = tx.Commit(ctx); err != nil {
		return domain.HostingOperation{}, err
	}
	return op, nil
}

// hostingTargetGate locks grant -> binding state -> runner, always after the
// task/lease locks on attempt paths. Revocation serializes with these locks.
func hostingTargetGate(ctx context.Context, tx pgx.Tx, projectID string, snap domain.HostingSnapshot, allowMissingState bool) error {
	return hostingTargetGateMode(ctx, tx, projectID, snap, allowMissingState, false)
}
func hostingTargetGateMode(ctx context.Context, tx pgx.Tx, projectID string, snap domain.HostingSnapshot, allowMissingState, observe bool) error {
	var revoked *time.Time
	if err := tx.QueryRow(ctx, `SELECT revoked_at FROM destination_project_grants WHERE destination_id=$1 AND project_id=$2 FOR UPDATE`, snap.DestinationID, projectID).Scan(&revoked); err != nil {
		return hostingError(err)
	}
	if revoked != nil {
		return ErrConflict
	}
	var admitted bool
	var hold *string
	err := tx.QueryRow(ctx, `SELECT restore_admitted,hold_operation_id FROM hosting_binding_state WHERE binding_id=$1 FOR UPDATE`, snap.BindingID).Scan(&admitted, &hold)
	if err != nil && !(allowMissingState && errors.Is(err, pgx.ErrNoRows)) {
		return hostingError(err)
	}
	if err == nil && !admitted && !observe {
		return errHostingRestoreHold
	}
	var runnerID string
	err = tx.QueryRow(ctx, `SELECT r.id FROM runners r JOIN servers s ON s.runner_id=r.id JOIN destinations d ON d.server_id=s.id JOIN resource_bindings b ON b.destination_id=d.id JOIN resources res ON res.id=b.resource_id JOIN projects p ON p.id=res.project_id
 WHERE b.id=$1 AND b.resource_id=$2 AND d.id=$3 AND s.id=$4 AND r.id=$5 AND res.project_id=$6 AND p.archived_at IS NULL
 AND r.status='active' AND r.token_hash<>'' AND 'image_deploy'=ANY(r.capabilities) AND NOT('shell'=ANY(r.capabilities))
 AND EXISTS(SELECT 1 FROM runner_enrollments e WHERE e.runner_id=r.id AND e.used_at IS NOT NULL AND e.revoked_at IS NULL AND e.credential_hash=r.token_hash)
 FOR SHARE OF r`, snap.BindingID, snap.ResourceID, snap.DestinationID, snap.ServerID, snap.RunnerID, projectID).Scan(&runnerID)
	if err != nil {
		return hostingError(err)
	}
	return nil
}

// PrepareDeployment is the real River handler: one transaction dispatches one
// typed task and records its immutable exact runner target and dispatch receipt.
func (s *PostgresStore) PrepareDeployment(ctx context.Context, id string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollbackTransaction(ctx, tx)
	op, err := hostingOperation(ctx, tx, id)
	if err != nil {
		return err
	}
	if op.State != "queued" {
		return nil
	}
	if err = hostingTargetGate(ctx, tx, op.ProjectID, op.Snapshot, false); err != nil {
		if errors.Is(err, errHostingRestoreHold) {
			return nil
		}
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			if _, updateErr := tx.Exec(ctx, `UPDATE hosting_operations SET state='failed',updated_at=clock_timestamp() WHERE id=$1 AND state='queued'`, id); updateErr != nil {
				return updateErr
			}
			if auditErr := hostingPhaseAudit(ctx, tx, s.queries, op, op.CreatedBy, "preflight_failed"); auditErr != nil {
				return auditErr
			}
			return tx.Commit(ctx)
		}
		return err
	}
	var state string
	if err = tx.QueryRow(ctx, `SELECT state FROM hosting_operations WHERE id=$1 FOR UPDATE`, id).Scan(&state); err != nil {
		return err
	}
	if state != "queued" {
		return nil
	}
	runID := "run_" + id
	spec := domain.RunSpec{Type: domain.RunTypeImageDeploy, Inputs: map[string]any{}, HostingOperationID: id, HostingMode: "deploy"}
	body, err := json.Marshal(spec)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO task_runs(id,project_id,run_spec,workflow,workflow_state,runner_tags,status,requested_by,started_at,created_at,claim_order_at) VALUES($1,$2,$3,'{}','{}','{}','queued',$4,clock_timestamp(),clock_timestamp(),clock_timestamp())`, runID, op.ProjectID, body, op.CreatedBy)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO hosting_task_targets(run_id,operation_id,binding_id,runner_id,mode) VALUES($1,$2,$3,$4,'deploy')`, runID, id, op.BindingID, op.Snapshot.RunnerID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO hosting_dispatch_receipts(operation_id,run_id) VALUES($1,$2)`, id, runID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE hosting_operations SET state='dispatched',run_id=$2,updated_at=clock_timestamp() WHERE id=$1`, id, runID); err != nil {
		return err
	}
	audit := domain.AuditEvent{ID: "aud_dispatch_" + id, ActorID: op.CreatedBy, Action: "hosting.dispatched", TargetID: id, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"run_id": runID, "runner_id": op.Snapshot.RunnerID}}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func hostingAuthorityExists(ctx context.Context, tx pgx.Tx, runnerID string, a domain.HostingAuthority, live bool) error {
	if _, err := tx.Exec(ctx, `SELECT id FROM task_runs WHERE id=$1 FOR UPDATE`, a.RunID); err != nil {
		return err
	}
	var id string
	query := `SELECT l.id FROM run_leases l JOIN hosting_task_targets t ON t.run_id=l.run_id WHERE l.id=$1 AND l.run_id=$2 AND l.runner_id=$3 AND l.attempt=$4 AND l.fence=$5 AND t.operation_id=$6 AND t.runner_id=$3`
	if live {
		query += ` AND l.status='active' AND l.expires_at>clock_timestamp()`
	}
	query += ` FOR UPDATE OF l`
	if err := tx.QueryRow(ctx, query, a.LeaseID, a.RunID, runnerID, a.Attempt, a.Fence, a.OperationID).Scan(&id); err != nil {
		return hostingError(err)
	}
	return nil
}

func (s *PostgresStore) HostingRunnerPlan(ctx context.Context, runnerID string, a domain.HostingAuthority) (domain.HostingOperation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = hostingAuthorityExists(ctx, tx, runnerID, a, true); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, a.OperationID)
	if err != nil {
		return op, err
	}
	var mode string
	if err = tx.QueryRow(ctx, `SELECT mode FROM hosting_task_targets WHERE run_id=$1`, a.RunID).Scan(&mode); err != nil {
		return domain.HostingOperation{}, err
	}
	op.TaskMode = mode
	if mode == "observe" {
		if !op.ReconcileRequested || op.ReconciliationRunID != a.RunID {
			return domain.HostingOperation{}, ErrConflict
		}
		if err = hostingTargetGateMode(ctx, tx, op.ProjectID, op.Snapshot, false, true); err != nil {
			return domain.HostingOperation{}, err
		}
		var raw []byte
		if err = tx.QueryRow(ctx, `SELECT healthy FROM hosting_binding_state WHERE binding_id=$1`, op.BindingID).Scan(&raw); err != nil {
			return domain.HostingOperation{}, err
		}
		if len(raw) > 0 {
			if err = json.Unmarshal(raw, &op.ReconciliationHealthy); err != nil {
				return domain.HostingOperation{}, err
			}
		}
		return op, nil
	}
	if op.State != "dispatched" && op.State != "preparing" && op.State != "prepared" {
		return domain.HostingOperation{}, ErrConflict
	}
	if err = hostingTargetGate(ctx, tx, op.ProjectID, op.Snapshot, false); err != nil {
		return domain.HostingOperation{}, err
	}
	if op.MutationHold {
		return domain.HostingOperation{}, ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE hosting_operations SET state=CASE WHEN state='dispatched' THEN 'preparing' ELSE state END,updated_at=clock_timestamp() WHERE id=$1`, op.ID); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err = hostingOperation(ctx, tx, op.ID)
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

// replayHostingReceipt checks stable identity/body before live-lease admission.
func replayHostingReceipt(ctx context.Context, tx pgx.Tx, a domain.HostingAuthority, phase, id, hash string) (bool, error) {
	var storedID, storedHash string
	var authority []byte
	err := tx.QueryRow(ctx, `SELECT receipt_id,body_hash,authority FROM hosting_receipts WHERE operation_id=$1 AND phase=$2`, a.OperationID, phase).Scan(&storedID, &storedHash, &authority)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var old domain.HostingAuthority
	if err = json.Unmarshal(authority, &old); err != nil {
		return false, err
	}
	if old != a || storedID != id || storedHash != hash {
		return false, ErrConflict
	}
	return true, nil
}

func insertHostingReceipt(ctx context.Context, tx pgx.Tx, a domain.HostingAuthority, phase, id string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	authority, err := json.Marshal(a)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO hosting_receipts(operation_id,phase,receipt_id,authority,body,body_hash) VALUES($1,$2,$3,$4,$5,$6)`, a.OperationID, phase, id, authority, raw, domain.HostingDocumentHash(body))
	return hostingError(err)
}

func (s *PostgresStore) HostingPrepared(ctx context.Context, runnerID string, in domain.HostingPreparedRequest) (domain.HostingOperation, error) {
	if in.ReceiptID == "" || len(in.ReceiptID) > 128 || domain.ValidateHostingRelease(in.Release) != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = hostingRequireMode(ctx, tx, in.HostingAuthority, "deploy"); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingAuthorityExists(ctx, tx, runnerID, in.HostingAuthority, false); err != nil {
		return domain.HostingOperation{}, err
	}
	replay, err := replayHostingReceipt(ctx, tx, in.HostingAuthority, "prepared", in.ReceiptID, domain.HostingDocumentHash(in.Release))
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if replay {
		return hostingOperation(ctx, tx, in.OperationID)
	}
	if err = hostingAuthorityExists(ctx, tx, runnerID, in.HostingAuthority, true); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, in.OperationID)
	if err != nil {
		return op, err
	}
	if op.State != "preparing" && op.State != "prepared" {
		return domain.HostingOperation{}, ErrConflict
	}
	if err = hostingTargetGate(ctx, tx, op.ProjectID, op.Snapshot, false); err != nil {
		return domain.HostingOperation{}, err
	}
	if op.Release != nil {
		if *op.Release != in.Release {
			return domain.HostingOperation{}, ErrConflict
		}
		return op, nil
	}
	// Supplied digest must match the repository and digest recorded by Docker.
	original, err := domain.NormalizeHostingImage(op.Snapshot.ImageReference)
	if err != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	if !hostingReleaseMatchesSource(original, in.Release.CanonicalImage) {
		return domain.HostingOperation{}, ErrConflict
	}
	var daemon *string
	var previousRaw []byte
	var hold *string
	if err = tx.QueryRow(ctx, `SELECT daemon_id,healthy,hold_operation_id FROM hosting_binding_state WHERE binding_id=$1`, op.BindingID).Scan(&daemon, &previousRaw, &hold); err != nil {
		return domain.HostingOperation{}, err
	}
	if hold != nil || (daemon != nil && *daemon != in.Release.DaemonID) {
		return domain.HostingOperation{}, ErrConflict
	}
	var previous *domain.HostingHealthy
	if len(previousRaw) > 0 {
		if err = json.Unmarshal(previousRaw, &previous); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	plan, err := domain.CompileHostingPlan(op.Snapshot, in.Release, previous)
	if err != nil {
		return domain.HostingOperation{}, ErrConflict
	}
	releaseRaw, err := json.Marshal(in.Release)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	planRaw, err := json.Marshal(plan)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE hosting_binding_state SET daemon_id=coalesce(daemon_id,$2) WHERE binding_id=$1`, op.BindingID, in.Release.DaemonID); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO hosting_releases VALUES($1,$2,$3)`, op.ID, releaseRaw, in.Release.Hash); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO hosting_apply_plans VALUES($1,$2,$3)`, op.ID, planRaw, plan.Hash); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = insertHostingReceipt(ctx, tx, in.HostingAuthority, "prepared", in.ReceiptID, in.Release); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingPhaseAudit(ctx, tx, s.queries, op, runnerID, "prepared"); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE hosting_operations SET state='prepared',updated_at=clock_timestamp() WHERE id=$1`, op.ID); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err = hostingOperation(ctx, tx, op.ID)
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

func hostingReleaseMatchesSource(source, canonical string) bool {
	original, err := reference.ParseNormalizedNamed(source)
	if err != nil {
		return false
	}
	resolved, err := reference.ParseNormalizedNamed(canonical)
	if err != nil || reference.TrimNamed(original).Name() != reference.TrimNamed(resolved).Name() {
		return false
	}
	if digest, ok := original.(reference.Digested); ok {
		actual, ok := resolved.(reference.Digested)
		return ok && actual.Digest() == digest.Digest()
	}
	return true
}

func (s *PostgresStore) HostingApplyBegin(ctx context.Context, runnerID string, a domain.HostingAuthority) (domain.HostingOperation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = hostingRequireMode(ctx, tx, a, "deploy"); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingAuthorityExists(ctx, tx, runnerID, a, true); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, a.OperationID)
	if err != nil {
		return op, err
	}
	if err = hostingTargetGate(ctx, tx, op.ProjectID, op.Snapshot, false); err != nil {
		return domain.HostingOperation{}, err
	}
	if op.Plan == nil || op.Release == nil || op.Plan.CompilerVersion != domain.HostingCompilerVersion || op.Snapshot.PolicyVersion != domain.HostingPolicyVersion {
		return domain.HostingOperation{}, ErrConflict
	}
	// Repeated admission under the same live tuple is safe; runtime never repeats
	// side effects merely because this acknowledgement was lost.
	if op.MutationHold && op.State == "applying" {
		return op, nil
	}
	if op.State != "prepared" || op.MutationHold {
		return domain.HostingOperation{}, ErrConflict
	}
	tag, err := tx.Exec(ctx, `UPDATE hosting_binding_state SET hold_operation_id=$2 WHERE binding_id=$1 AND hold_operation_id IS NULL AND restore_admitted AND daemon_id=$3`, op.BindingID, op.ID, op.Release.DaemonID)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if tag.RowsAffected() != 1 {
		return domain.HostingOperation{}, ErrConflict
	}
	authorityRaw, marshalErr := json.Marshal(a)
	if marshalErr != nil {
		return domain.HostingOperation{}, marshalErr
	}
	if _, err = tx.Exec(ctx, `INSERT INTO hosting_apply_admissions(operation_id,run_id,lease_id,authority,plan_hash) VALUES($1,$2,$3,$4,$5)`, op.ID, a.RunID, a.LeaseID, authorityRaw, op.Plan.Hash); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE hosting_operations SET state='applying',updated_at=clock_timestamp() WHERE id=$1`, op.ID); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingPhaseAudit(ctx, tx, s.queries, op, runnerID, "apply_admitted"); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err = hostingOperation(ctx, tx, op.ID)
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

func hostingPhaseAudit(ctx context.Context, tx pgx.Tx, q *sqlcgen.Queries, op domain.HostingOperation, actor, phase string) error {
	return createAuditWithQueries(ctx, q.WithTx(tx), domain.AuditEvent{ID: "aud_hosting_" + phase + "_" + op.ID, ActorID: actor, Action: "hosting." + phase, TargetID: op.ID, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"run_id": op.RunID, "binding_id": op.BindingID}})
}

func (s *PostgresStore) HostingComplete(ctx context.Context, runnerID string, in domain.HostingCompleteRequest) (domain.HostingOperation, error) {
	if in.ReceiptID == "" || len(in.ReceiptID) > 128 || (in.Result.Outcome != "succeeded" && in.Result.Outcome != "failed") {
		return domain.HostingOperation{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = hostingRequireMode(ctx, tx, in.HostingAuthority, "deploy"); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingAuthorityExists(ctx, tx, runnerID, in.HostingAuthority, false); err != nil {
		return domain.HostingOperation{}, err
	}
	replay, err := replayHostingReceipt(ctx, tx, in.HostingAuthority, "complete", in.ReceiptID, domain.HostingDocumentHash(in.Result))
	if err != nil {
		return domain.HostingOperation{}, err
	}
	if replay {
		return hostingOperation(ctx, tx, in.OperationID)
	}
	op, err := hostingOperation(ctx, tx, in.OperationID)
	if err != nil {
		return op, err
	}
	if op.State == "succeeded" || op.State == "failed" || op.State == "canceled" {
		return domain.HostingOperation{}, ErrConflict
	}
	var live bool
	if err = tx.QueryRow(ctx, `SELECT status='active' AND expires_at>clock_timestamp() FROM run_leases WHERE id=$1`, in.LeaseID).Scan(&live); err != nil {
		return domain.HostingOperation{}, err
	}
	// Pre-apply failure may finish only under live authority and cannot assert
	// external success. Expired post-admission evidence is retained for owner
	// reconciliation, never treated as renewed permission to mutate a workload.
	if !op.MutationHold {
		if !live || in.Result.Outcome != "failed" || in.Result.Uncertain || op.State == "cancel_requested" {
			return domain.HostingOperation{}, ErrConflict
		}
	} else {
		if op.Plan == nil || op.Release == nil || in.Result.PlanHash != op.Plan.Hash || in.Result.DaemonID != op.Release.DaemonID {
			return domain.HostingOperation{}, ErrConflict
		}
		if in.Result.Outcome == "succeeded" && (!in.Result.Ready || in.Result.ImageID != op.Release.ImageID || in.Result.ContainerID == "") {
			return domain.HostingOperation{}, ErrConflict
		}
	}
	if err = insertHostingReceipt(ctx, tx, in.HostingAuthority, "complete", in.ReceiptID, in.Result); err != nil {
		return domain.HostingOperation{}, err
	}
	uncertain := op.MutationHold && (!live || in.Result.Uncertain || (in.Result.Outcome == "failed" && !in.Result.Restored))
	state := in.Result.Outcome
	if uncertain {
		state = "reconciliation_required"
	}
	if _, err = tx.Exec(ctx, `UPDATE hosting_operations SET state=$2,updated_at=clock_timestamp() WHERE id=$1`, op.ID, state); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE run_leases SET status=$2,completed_at=clock_timestamp(),completion_key=$3 WHERE id=$1`, in.LeaseID, in.Result.Outcome, in.ReceiptID); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE task_runs SET status=$2,finished_at=clock_timestamp() WHERE id=$1`, op.RunID, in.Result.Outcome); err != nil {
		return domain.HostingOperation{}, err
	}
	if op.MutationHold && !uncertain {
		if in.Result.Outcome == "succeeded" {
			healthy := domain.HostingHealthy{Snapshot: op.Snapshot, OperationID: op.ID, ContainerID: in.Result.ContainerID, Release: *op.Release, PlanHash: op.Plan.Hash}
			raw, err := json.Marshal(healthy)
			if err != nil {
				return domain.HostingOperation{}, err
			}
			if _, err = tx.Exec(ctx, `UPDATE hosting_binding_state SET healthy=$2 WHERE binding_id=$1 AND hold_operation_id=$3`, op.BindingID, raw, op.ID); err != nil {
				return domain.HostingOperation{}, err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE hosting_binding_state SET hold_operation_id=NULL WHERE binding_id=$1 AND hold_operation_id=$2`, op.BindingID, op.ID); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	if err = hostingPhaseAudit(ctx, tx, s.queries, op, runnerID, "completed"); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err = hostingOperation(ctx, tx, op.ID)
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

func (s *PostgresStore) CancelHostingOperation(ctx context.Context, id string, audit domain.AuditEvent) (domain.HostingOperation, error) {
	return s.hostingControlMutation(ctx, id, audit, false)
}
func (s *PostgresStore) RequestHostingReconciliation(ctx context.Context, id string, audit domain.AuditEvent) (domain.HostingOperation, error) {
	return s.hostingControlMutation(ctx, id, audit, true)
}
func (s *PostgresStore) hostingControlMutation(ctx context.Context, id string, audit domain.AuditEvent, reconcile bool) (domain.HostingOperation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	before, err := hostingOperation(ctx, tx, id)
	if err != nil {
		return before, err
	}
	if err = hostingLockOperationRuns(ctx, tx, before); err != nil {
		return domain.HostingOperation{}, err
	}
	var hold *string
	if err = tx.QueryRow(ctx, `SELECT hold_operation_id FROM hosting_binding_state WHERE binding_id=$1 FOR UPDATE`, before.BindingID).Scan(&hold); err != nil {
		return domain.HostingOperation{}, err
	}
	var locked string
	if err = tx.QueryRow(ctx, `SELECT id FROM hosting_operations WHERE id=$1 FOR UPDATE`, id).Scan(&locked); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, id)
	if err != nil {
		return op, err
	}
	if op.RunID != before.RunID || op.ReconciliationRunID != before.ReconciliationRunID {
		return domain.HostingOperation{}, ErrConflict
	}
	if reconcile {
		if op.ReconcileRequested && op.ReconciliationRunID != "" {
			return op, nil
		}
		if err = hostingTargetGateMode(ctx, tx, op.ProjectID, op.Snapshot, false, true); err != nil {
			return domain.HostingOperation{}, err
		}
		observationRun := op.ReconciliationRunID
		if observationRun == "" {
			observationRun = "observe_" + id
			spec := domain.RunSpec{Type: domain.RunTypeImageDeploy, Inputs: map[string]any{}, HostingOperationID: id, HostingMode: "observe"}
			raw, marshalErr := json.Marshal(spec)
			if marshalErr != nil {
				return domain.HostingOperation{}, marshalErr
			}
			if _, err = tx.Exec(ctx, `INSERT INTO task_runs(id,project_id,run_spec,workflow,workflow_state,runner_tags,status,requested_by,started_at,created_at,claim_order_at) VALUES($1,$2,$3,'{}','{}','{}','queued',$4,clock_timestamp(),clock_timestamp(),clock_timestamp())`, observationRun, op.ProjectID, raw, audit.ActorID); err != nil {
				return domain.HostingOperation{}, err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO hosting_task_targets(run_id,operation_id,binding_id,runner_id,mode) VALUES($1,$2,$3,$4,'observe')`, observationRun, id, op.BindingID, op.Snapshot.RunnerID); err != nil {
				return domain.HostingOperation{}, err
			}
		} else {
			if _, err = tx.Exec(ctx, `UPDATE task_runs SET status='queued',runner_id=NULL,finished_at=NULL,claim_order_at=clock_timestamp() WHERE id=$1`, observationRun); err != nil {
				return domain.HostingOperation{}, err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE hosting_operations SET reconcile_requested=true,reconciliation_run_id=$2,updated_at=clock_timestamp() WHERE id=$1`, id, observationRun); err != nil {
			return domain.HostingOperation{}, err
		}
	} else {
		if op.State == "succeeded" || op.State == "failed" || op.State == "canceled" {
			return domain.HostingOperation{}, ErrConflict
		}
		state := "canceled"
		if hold != nil && *hold == id {
			state = "cancel_requested"
		}
		if _, err = tx.Exec(ctx, `UPDATE hosting_operations SET state=$2,reconcile_requested=false,updated_at=clock_timestamp() WHERE id=$1`, id, state); err != nil {
			return domain.HostingOperation{}, err
		}
		for _, runID := range []string{op.RunID, op.ReconciliationRunID} {
			if runID != "" {
				if _, err = tx.Exec(ctx, `UPDATE run_leases SET status='canceled',completed_at=clock_timestamp() WHERE run_id=$1 AND status='active'`, runID); err != nil {
					return domain.HostingOperation{}, err
				}
				if _, err = tx.Exec(ctx, `UPDATE task_runs SET status='canceled',finished_at=clock_timestamp() WHERE id=$1`, runID); err != nil {
					return domain.HostingOperation{}, err
				}
			}
		}
	}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err = hostingOperation(ctx, tx, id)
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}
func hostingLockOperationRuns(ctx context.Context, tx pgx.Tx, op domain.HostingOperation) error {
	for _, id := range []string{op.RunID, op.ReconciliationRunID} {
		if id != "" {
			var locked string
			if err := tx.QueryRow(ctx, `SELECT id FROM task_runs WHERE id=$1 FOR UPDATE`, id).Scan(&locked); err != nil {
				return hostingError(err)
			}
		}
	}
	return nil
}

func (s *PostgresStore) HostingReconciliation(ctx context.Context, runnerID string, in domain.HostingObservation) (domain.HostingOperation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	before, err := hostingOperation(ctx, tx, in.OperationID)
	if err != nil {
		return before, err
	}
	if err = hostingLockOperationRuns(ctx, tx, before); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingRequireMode(ctx, tx, in.HostingAuthority, "observe"); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingAuthorityExists(ctx, tx, runnerID, in.HostingAuthority, false); err != nil {
		return domain.HostingOperation{}, err
	}
	var committedHash string
	err = tx.QueryRow(ctx, `SELECT body_hash FROM hosting_reconciliation_receipts WHERE lease_id=$1 AND operation_id=$2`, in.LeaseID, in.OperationID).Scan(&committedHash)
	if err == nil {
		if committedHash != domain.HostingDocumentHash(in) {
			return domain.HostingOperation{}, ErrConflict
		}
		return hostingOperation(ctx, tx, in.OperationID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.HostingOperation{}, err
	}
	if err = hostingAuthorityExists(ctx, tx, runnerID, in.HostingAuthority, true); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingTargetGateMode(ctx, tx, before.ProjectID, before.Snapshot, false, true); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, in.OperationID)
	if err != nil {
		return op, err
	}
	if op.RunID != before.RunID || op.ReconciliationRunID != before.ReconciliationRunID || !op.ReconcileRequested || op.ReconciliationRunID != in.RunID || op.BindingID != in.BindingID {
		return domain.HostingOperation{}, ErrConflict
	}
	var daemon *string
	var healthyRaw []byte
	if err = tx.QueryRow(ctx, `SELECT daemon_id,healthy FROM hosting_binding_state WHERE binding_id=$1`, op.BindingID).Scan(&daemon, &healthyRaw); err != nil {
		return domain.HostingOperation{}, err
	}
	if in.DaemonID == "" || len(in.DaemonID) > 128 || (daemon != nil && *daemon != in.DaemonID) {
		return domain.HostingOperation{}, ErrConflict
	}
	var admission bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hosting_apply_admissions WHERE operation_id=$1)`, op.ID).Scan(&admission); err != nil {
		return domain.HostingOperation{}, err
	}
	var expected *domain.HostingHealthy
	if op.MutationHold {
		if op.Result == nil || op.Result.Uncertain || op.Plan == nil || op.Release == nil {
			return domain.HostingOperation{}, ErrConflict
		}
		var liveDeployment bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM run_leases WHERE run_id=$1 AND status='active' AND expires_at>clock_timestamp())`, op.RunID).Scan(&liveDeployment); err != nil {
			return domain.HostingOperation{}, err
		}
		if liveDeployment {
			return domain.HostingOperation{}, ErrConflict
		}
		expected = &domain.HostingHealthy{Snapshot: op.Snapshot, OperationID: op.ID, ContainerID: op.Result.ContainerID, Release: *op.Release, PlanHash: op.Plan.Hash}
		if op.Result.Outcome != "succeeded" {
			if !op.Result.Restored || op.Plan.PreviousHealthy == nil {
				return domain.HostingOperation{}, ErrConflict
			}
			expected = op.Plan.PreviousHealthy
		}
	} else {
		// No admission receipt plus no hold is durable proof that this operation
		// never acquired mutation permission. A listing alone is not that proof.
		if admission && op.CompleteReceipt == nil {
			return domain.HostingOperation{}, ErrConflict
		}
		if len(healthyRaw) > 0 {
			if err = json.Unmarshal(healthyRaw, &expected); err != nil {
				return domain.HostingOperation{}, err
			}
		}
	}
	if expected == nil {
		if op.MutationHold || !in.NoOwnedContainers || in.ContainerID != "" || in.ImageID != "" {
			return domain.HostingOperation{}, ErrConflict
		}
	} else {
		if in.NoOwnedContainers || !in.Ready || in.ContainerID != expected.ContainerID || in.ImageID != expected.Release.ImageID || in.PlanHash != expected.PlanHash || in.ReleaseHash != expected.Release.Hash {
			return domain.HostingOperation{}, ErrConflict
		}
	}
	if op.MutationHold {
		raw, marshalErr := json.Marshal(expected)
		if marshalErr != nil {
			return domain.HostingOperation{}, marshalErr
		}
		if _, err = tx.Exec(ctx, `UPDATE hosting_binding_state SET healthy=$2,hold_operation_id=NULL WHERE binding_id=$1 AND hold_operation_id=$3`, op.BindingID, raw, op.ID); err != nil {
			return domain.HostingOperation{}, err
		}
		if _, err = tx.Exec(ctx, `UPDATE hosting_operations SET state=$2,updated_at=clock_timestamp() WHERE id=$1`, op.ID, op.Result.Outcome); err != nil {
			return domain.HostingOperation{}, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE hosting_binding_state SET restore_admitted=true,daemon_id=coalesce(daemon_id,$2) WHERE binding_id=$1`, op.BindingID, in.DaemonID); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE hosting_operations SET reconcile_requested=false,updated_at=clock_timestamp() WHERE id=$1`, op.ID); err != nil {
		return domain.HostingOperation{}, err
	}
	raw, marshalErr := json.Marshal(in)
	if marshalErr != nil {
		return domain.HostingOperation{}, marshalErr
	}
	if _, err = tx.Exec(ctx, `INSERT INTO hosting_reconciliation_receipts(lease_id,operation_id,body,body_hash) VALUES($1,$2,$3,$4)`, in.LeaseID, op.ID, raw, domain.HostingDocumentHash(in)); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE run_leases SET status='succeeded',completed_at=clock_timestamp(),completion_key=$2 WHERE id=$1`, in.LeaseID, "observed_"+in.LeaseID); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE task_runs SET status='succeeded',finished_at=clock_timestamp() WHERE id=$1`, in.RunID); err != nil {
		return domain.HostingOperation{}, err
	}
	audit := domain.AuditEvent{ID: "aud_observed_" + in.LeaseID, ActorID: runnerID, Action: "hosting.target_observed", TargetID: op.ID, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"runner_id": runnerID, "destination_id": op.Snapshot.DestinationID, "binding_id": op.BindingID, "task_mode": "observe"}}
	if err = createAuditWithQueries(ctx, s.queries.WithTx(tx), audit); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err = hostingOperation(ctx, tx, op.ID)
	if err != nil {
		return op, err
	}
	return op, tx.Commit(ctx)
}

func (s *PostgresStore) hostingClaimAllowed(ctx context.Context, tx pgx.Tx, run domain.TaskRun, runnerID string) (bool, error) {
	if run.RunSpec.Type != domain.RunTypeImageDeploy {
		return true, nil
	}
	var operationID, mode string
	err := tx.QueryRow(ctx, `SELECT operation_id,mode FROM hosting_task_targets WHERE run_id=$1 AND runner_id=$2`, run.ID, runnerID).Scan(&operationID, &mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	op, err := hostingOperation(ctx, tx, operationID)
	if err != nil {
		return false, err
	}
	if op.Snapshot.RunnerID != runnerID || run.RunSpec.HostingOperationID != op.ID || run.RunSpec.HostingMode != mode {
		return false, nil
	}
	if mode == "observe" {
		if !op.ReconcileRequested || op.ReconciliationRunID != run.ID {
			return false, nil
		}
	} else {
		if op.RunID != run.ID || op.MutationHold || (op.State != "dispatched" && op.State != "prepared" && op.State != "preparing") {
			return false, nil
		}
	}
	err = hostingTargetGateMode(ctx, tx, op.ProjectID, op.Snapshot, false, mode == "observe")
	if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) || errors.Is(err, errHostingRestoreHold) {
		return false, nil
	}
	return err == nil, err
}

func rejectGenericHostingRun(ctx context.Context, q *sqlcgen.Queries, runID string) error {
	owned, err := q.IsHostingRun(ctx, runID)
	if err != nil {
		return err
	}
	if owned != nil && *owned {
		return ErrConflict
	}
	return nil
}

func hostingRequireMode(ctx context.Context, tx pgx.Tx, a domain.HostingAuthority, expected string) error {
	var mode string
	if err := tx.QueryRow(ctx, `SELECT mode FROM hosting_task_targets WHERE run_id=$1 AND operation_id=$2`, a.RunID, a.OperationID).Scan(&mode); err != nil {
		return hostingError(err)
	}
	if mode != expected {
		return ErrConflict
	}
	return nil
}

func (s *PostgresStore) HostingStatus(ctx context.Context, runnerID string, in domain.HostingStatusRequest) (domain.HostingOperation, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	if err = hostingAuthorityExists(ctx, tx, runnerID, in.HostingAuthority, in.Phase != ""); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, in.OperationID)
	if err != nil {
		return op, err
	}
	var mode string
	if err = tx.QueryRow(ctx, `SELECT mode FROM hosting_task_targets WHERE run_id=$1`, in.RunID).Scan(&mode); err != nil {
		return domain.HostingOperation{}, err
	}
	op.TaskMode = mode
	if in.Phase == "" {
		return op, nil
	}
	if in.Phase != "verifying" || mode != "deploy" || !op.MutationHold || (op.State != "applying" && op.State != "verifying") {
		return domain.HostingOperation{}, ErrConflict
	}
	if _, err = tx.Exec(ctx, `UPDATE hosting_operations SET state='verifying',updated_at=clock_timestamp() WHERE id=$1`, op.ID); err != nil {
		return domain.HostingOperation{}, err
	}
	op.State = "verifying"
	return op, tx.Commit(ctx)
}

// ClaimHostingObservation lets startup reconciliation obtain only its owner's
// exact read-only task before normal heartbeat/claim traffic. The old full
// fenced tuple authenticates the retained intent; it never regains apply rights.
func (s *PostgresStore) ClaimHostingObservation(ctx context.Context, runnerID string, a domain.HostingAuthority, ttl time.Duration) (domain.HostingOperation, error) {
	if ttl <= 0 || ttl > 10*time.Minute {
		return domain.HostingOperation{}, ErrConflict
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return domain.HostingOperation{}, err
	}
	defer rollbackTransaction(ctx, tx)
	before, err := hostingOperation(ctx, tx, a.OperationID)
	if err != nil {
		return before, err
	}
	if err = hostingLockOperationRuns(ctx, tx, before); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingRequireMode(ctx, tx, a, "deploy"); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingAuthorityExists(ctx, tx, runnerID, a, false); err != nil {
		return domain.HostingOperation{}, err
	}
	if err = hostingTargetGateMode(ctx, tx, before.ProjectID, before.Snapshot, false, true); err != nil {
		return domain.HostingOperation{}, err
	}
	op, err := hostingOperation(ctx, tx, a.OperationID)
	if err != nil {
		return op, err
	}
	if !op.ReconcileRequested || op.ReconciliationRunID == "" || op.RunID != before.RunID || op.ReconciliationRunID != before.ReconciliationRunID {
		return domain.HostingOperation{}, ErrConflict
	}
	q := s.queries.WithTx(tx)
	existing, err := q.GetActiveLeaseForRun(ctx, op.ReconciliationRunID)
	if err == nil {
		lease := runLeaseFromSQLC(existing)
		if lease.RunnerID != runnerID {
			return domain.HostingOperation{}, ErrConflict
		}
		op.TaskMode = "observe"
		op.ObservationLease = &lease
		return op, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.HostingOperation{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE run_leases SET status='expired',completed_at=clock_timestamp() WHERE run_id=$1 AND status='active' AND expires_at<=clock_timestamp()`, op.ReconciliationRunID); err != nil {
		return domain.HostingOperation{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE task_runs SET status='queued',runner_id=NULL,finished_at=NULL WHERE id=$1`, op.ReconciliationRunID); err != nil {
		return domain.HostingOperation{}, err
	}
	attempt, err := q.ClaimQueuedRun(ctx, sqlcgen.ClaimQueuedRunParams{RunID: op.ReconciliationRunID, RunnerID: &runnerID})
	if err != nil {
		return domain.HostingOperation{}, err
	}
	leaseID, err := newLeaseToken("lease")
	if err != nil {
		return domain.HostingOperation{}, err
	}
	fence, err := newLeaseToken("fence")
	if err != nil {
		return domain.HostingOperation{}, err
	}
	row, err := q.CreateActiveRunLease(ctx, sqlcgen.CreateActiveRunLeaseParams{ID: leaseID, RunID: op.ReconciliationRunID, RunnerID: runnerID, Status: domain.LeaseActive, TtlMicroseconds: ttl.Microseconds(), Attempt: attempt, Fence: fence})
	if err != nil {
		return domain.HostingOperation{}, err
	}
	lease := runLeaseFromSQLC(row)
	op.TaskMode = "observe"
	op.ObservationLease = &lease
	audit := domain.AuditEvent{ID: "aud_observe_claim_" + lease.ID, ActorID: runnerID, Action: "hosting.observation_claimed", TargetID: op.ID, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"runner_id": runnerID, "destination_id": op.Snapshot.DestinationID, "task_mode": "observe"}}
	if err = createAuditWithQueries(ctx, q, audit); err != nil {
		return domain.HostingOperation{}, err
	}
	return op, tx.Commit(ctx)
}

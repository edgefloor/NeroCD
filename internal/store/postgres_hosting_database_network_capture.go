package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func networkCaptureSchema(ctx context.Context, q *sqlcgen.Queries) error {
	ok, err := q.DatabaseNetworkCaptureSchema(ctx)
	if err != nil || !ok {
		return ErrHostingDatabaseNetworkCaptureUnavailable
	}
	return nil
}

func readNetworkCapture(ctx context.Context, q *sqlcgen.Queries, id string) (domain.HostingDatabaseNetworkCapture, error) {
	var c domain.HostingDatabaseNetworkCapture
	row, err := q.ReadDatabaseNetworkCapture(ctx, id)
	if err != nil {
		return c, hostingError(err)
	}
	if json.Unmarshal(row.Plan, &c.Plan) != nil || c.Plan.Hash != row.PlanHash || c.Plan.OperationID != row.ID || c.Plan.SourceOperationID != row.SourceOperationID || c.Plan.ResourceID != row.ResourceID || c.Plan.BindingID != row.BindingID || c.Plan.ProjectID != row.ProjectID {
		return c, ErrConflict
	}
	c.ID, c.RunID, c.CreatedBy, c.CreatedAt, c.UpdatedAt = row.ID, row.RunID, row.CreatedBy, row.CreatedAt, row.CreatedAt
	run, err := q.GetRun(ctx, c.RunID)
	if err != nil {
		return c, err
	}
	c.State = run.Status
	if len(row.Receipt) > 0 {
		var r domain.HostingDatabaseNetworkOriginReceipt
		if json.Unmarshal(row.Receipt, &r) != nil || r.OperationID != c.ID || r.SourceOperationID != c.Plan.SourceOperationID || r.PlanHash != c.Plan.Hash || r.OriginHash != r.Origin.DocumentHash() || c.Plan.ValidateOrigin(r.Origin) != nil {
			return c, ErrConflict
		}
		c.Receipt = &r
		c.UpdatedAt = r.CreatedAt
	}
	return c, nil
}

// networkCaptureSource takes only controller metadata locks. It never invokes
// Docker, issues a create permit, expands credentials or changes healthy state.
func networkCaptureSource(ctx context.Context, tx pgx.Tx, source, actor string) (domain.HostingDatabaseNetworkCapturePlan, error) {
	var p domain.HostingDatabaseNetworkCapturePlan
	q := sqlcgen.New(tx)
	before, err := hostingOperation(ctx, tx, source)
	if err != nil {
		return p, err
	}
	if before.Plan == nil || domain.ValidateHostingDatabasePlan(*before.Plan) != nil {
		return p, ErrConflict
	}
	if err = lockHostingResourceDestination(ctx, tx, before.ResourceID); err != nil {
		return p, err
	}
	target, err := q.LockHostingResourceTarget(ctx, before.ResourceID)
	if err != nil {
		return p, err
	}
	if target.ProjectID != before.ProjectID || target.Generation != before.Snapshot.ResourceGeneration || target.BindingID != before.BindingID || target.DestinationID != before.Snapshot.DestinationID || target.ServerID != before.Snapshot.ServerID || target.RunnerID != before.Snapshot.RunnerID || target.Kind != "database" {
		return p, ErrConflict
	}
	if err = authorizeBuildActor(ctx, tx, actor, before.ProjectID, "owner"); err != nil {
		return p, err
	}
	if err = databaseBindingGrant(ctx, q, before.ProjectID, target.DestinationID); err != nil {
		return p, err
	}
	if _, err = q.LockHostingAdmissionState(ctx, before.BindingID); err != nil {
		return p, err
	}
	safe, err := q.HostingDatabaseBindingTargetSafe(ctx, before.BindingID)
	if err != nil || safe == nil || !*safe {
		return p, ErrConflict
	}
	healthy, err := databaseBindingHealthy(ctx, tx, before.ResourceID, before.BindingID)
	if err != nil || healthy.ID != source {
		return p, ErrConflict
	}
	raw, err := q.GetHostingHealthy(ctx, before.BindingID)
	if err != nil {
		return p, err
	}
	var h domain.HostingHealthy
	if json.Unmarshal(raw, &h) != nil {
		return p, ErrConflict
	}
	database, err := hostingDatabaseLoad(ctx, q, before.ResourceID)
	if err != nil {
		return p, err
	}
	hash, err := domain.HostingDatabaseConfigurationHash(healthy.Snapshot, database.Credentials)
	if err != nil || hash != healthy.Snapshot.Database.ConfigurationHash || !slices.Equal(database.Credentials, healthy.Plan.Database.Credentials) {
		return p, ErrConflict
	}
	storage, err := currentHostingStorage(ctx, tx, before.ProjectID, before.ResourceID)
	if err != nil {
		return p, err
	}
	m, err := readHostingStorageMaterialization(ctx, tx, storage)
	if err != nil || m.State != "materialized" || m.MaterializedReceipt == nil {
		return p, ErrConflict
	}
	receipt, err := q.ReadDatabaseNetworkCaptureCreation(ctx, storage.ID)
	if err != nil {
		return p, err
	}
	var creation domain.HostingStorageMaterializedRequest
	if json.Unmarshal(receipt.Body, &creation) != nil || creation.Matches(storage, m) != nil || domain.HostingDocumentHash(creation) != receipt.BodyHash || receipt.ReceiptID != m.MaterializedReceipt.ID || receipt.BodyHash != m.MaterializedReceipt.BodyHash {
		return p, ErrConflict
	}
	p = domain.HostingDatabaseNetworkCapturePlan{Version: domain.HostingDatabaseNetworkCaptureVersion, OperationID: domain.HostingDatabaseNetworkCaptureID(source), SourceOperationID: source, ProjectID: before.ProjectID, ResourceID: before.ResourceID, BindingID: before.BindingID, DestinationID: target.DestinationID, ServerID: target.ServerID, RunnerID: target.RunnerID, SourcePlan: *healthy.Plan, SourceHealthy: h, Storage: storage, Materialization: m, Creation: domain.HostingDatabaseNetworkCaptureCreation{Receipt: *m.MaterializedReceipt, VolumeName: creation.VolumeName, Driver: creation.Driver, Scope: creation.Scope, Labels: creation.Labels, Options: map[string]string{}}}
	p.Hash = p.DocumentHash()
	return p, p.Validate()
}

func networkCaptureRunner(ctx context.Context, q *sqlcgen.Queries, runner, credential string, p domain.HostingDatabaseNetworkCapturePlan) error {
	if runner != p.RunnerID || credential == "" {
		return ErrConflict
	}
	r, err := q.LockObservationRunner(ctx, runner)
	if err != nil {
		return err
	}
	if r.Status != "active" || r.TokenHash != credential || !slices.Contains(r.Capabilities, domain.HostingDatabaseNetworkCaptureCapability) || slices.Contains(r.Capabilities, "shell") {
		return ErrConflict
	}
	_, err = q.LockObservationEnrollment(ctx, sqlcgen.LockObservationEnrollmentParams{RunnerID: runner, CredentialHash: &credential})
	return hostingError(err)
}

// Historical origin evidence survives legitimate later deployments. Current
// ownership, grants and referenced credentials remain mandatory, but no current
// healthy match or live lease is inferred from this immutable receipt.
func networkCaptureHistorical(ctx context.Context, tx pgx.Tx, c domain.HostingDatabaseNetworkCapture, actor string) error {
	if err := lockHostingResourceDestination(ctx, tx, c.Plan.ResourceID); err != nil {
		return err
	}
	q := sqlcgen.New(tx)
	target, err := q.LockHostingResourceTarget(ctx, c.Plan.ResourceID)
	if err != nil {
		return err
	}
	p := c.Plan
	if target.ProjectID != p.ProjectID || target.BindingID != p.BindingID || target.DestinationID != p.DestinationID || target.ServerID != p.ServerID || target.RunnerID != p.RunnerID || target.Kind != "database" {
		return ErrConflict
	}
	if err = authorizeBuildActor(ctx, tx, actor, p.ProjectID, "owner"); err != nil {
		return err
	}
	if err = databaseBindingGrant(ctx, q, p.ProjectID, p.DestinationID); err != nil {
		return err
	}
	storage, err := currentHostingStorage(ctx, tx, p.ProjectID, p.ResourceID)
	if err != nil || !reflect.DeepEqual(storage, p.Storage) {
		return ErrConflict
	}
	materialization, err := readHostingStorageMaterialization(ctx, tx, storage)
	if err != nil || !reflect.DeepEqual(materialization, p.Materialization) {
		return ErrConflict
	}
	refs := make([]domain.HostingRuntimeSecretReference, 0, len(p.SourcePlan.Database.Credentials))
	for _, ref := range p.SourcePlan.Database.Credentials {
		refs = append(refs, domain.HostingRuntimeSecretReference{SecretID: ref.SecretID, Version: ref.Version})
	}
	return validateRuntimeReferences(ctx, q, p.ProjectID, refs)
}

func finalizeNetworkCapture(ctx context.Context, tx pgx.Tx, c domain.HostingDatabaseNetworkCapture, lease string, live bool) error {
	q := sqlcgen.New(tx)
	if err := q.FlushDatabaseNetworkCapture(ctx); err != nil {
		return err
	}
	if err := authorizeBuildActor(ctx, tx, c.CreatedBy, c.Plan.ProjectID, "owner"); err != nil {
		return err
	}
	if err := databaseBindingGrant(ctx, q, c.Plan.ProjectID, c.Plan.DestinationID); err != nil {
		return err
	}
	clock, err := q.DatabaseNetworkCaptureFinalClock(ctx, sqlcgen.DatabaseNetworkCaptureFinalClockParams{ActorID: c.CreatedBy, LeaseID: lease, RequireLive: live})
	if err != nil {
		return err
	}
	if !clock.ActorValid {
		return auth.ErrForbidden
	}
	if !clock.LeaseValid {
		return ErrConflict
	}
	return nil
}

// RequestHostingDatabaseNetworkCapture deduplicates immutable origin capture by
// source operation. Existing evidence remains historical on every retry.
func (s *PostgresStore) RequestHostingDatabaseNetworkCapture(ctx context.Context, source, actor string, audit domain.AuditEvent) (domain.HostingDatabaseNetworkCapture, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseNetworkCapture, error) {
		var zero domain.HostingDatabaseNetworkCapture
		q := sqlcgen.New(tx)
		if err := networkCaptureSchema(ctx, q); err != nil {
			return zero, err
		}
		c, err := readNetworkCapture(ctx, q, domain.HostingDatabaseNetworkCaptureID(source))
		if err == nil {
			if err = networkCaptureHistorical(ctx, tx, c, actor); err != nil {
				return zero, err
			}
			if c.CreatedBy != actor {
				return zero, ErrConflict
			}
			if c.Receipt == nil {
				p, e := networkCaptureSource(ctx, tx, source, actor)
				if e != nil || !reflect.DeepEqual(c.Plan, p) {
					return zero, ErrConflict
				}
			}
			return c, finalizeNetworkCapture(ctx, tx, c, "", false)
		}
		if !errors.Is(err, ErrNotFound) {
			return zero, err
		}
		p, err := networkCaptureSource(ctx, tx, source, actor)
		if err != nil {
			return zero, err
		}
		run := domain.TaskRun{ID: "capture_" + p.OperationID, RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseNetworkCapture, HostingOperationID: p.OperationID, HostingMode: "capture", Inputs: map[string]any{}}}
		if domain.ValidateHostingDatabaseNetworkCaptureTask(run) != nil {
			return zero, ErrConflict
		}
		if _, err = q.CreateHostingTask(ctx, sqlcgen.CreateHostingTaskParams{ID: run.ID, ProjectID: p.ProjectID, RunSpec: buildJSON(run.RunSpec), RequestedBy: actor}); err != nil {
			return zero, err
		}
		if err = q.InsertDatabaseNetworkCapture(ctx, sqlcgen.InsertDatabaseNetworkCaptureParams{ID: p.OperationID, SourceOperationID: source, ResourceID: p.ResourceID, BindingID: p.BindingID, ProjectID: p.ProjectID, RunID: run.ID, Plan: buildJSON(p), PlanHash: p.Hash, CreatedBy: actor}); err != nil {
			return zero, err
		}
		audit.ID, audit.ActorID, audit.Action, audit.TargetID = "aud_capture_request_"+p.OperationID, actor, "hosting.database_network_capture.requested", p.OperationID
		audit.Metadata = map[string]any{"project_id": p.ProjectID, "resource_id": p.ResourceID, "source_operation_id": source, "plan_hash": p.Hash}
		if audit.CreatedAt.IsZero() {
			audit.CreatedAt = time.Now().UTC()
		}
		if err = createAuditWithQueries(ctx, q, audit); err != nil {
			return zero, err
		}
		c, err = readNetworkCapture(ctx, q, p.OperationID)
		if err != nil {
			return zero, err
		}
		return c, finalizeNetworkCapture(ctx, tx, c, "", false)
	})
}

// lockNetworkCapture starts with the task lock, matching ordinary task/lease
// ordering. Generic claims reject this family and cannot invert these locks.
func lockNetworkCapture(ctx context.Context, tx pgx.Tx, id string, current bool) (domain.HostingDatabaseNetworkCapture, error) {
	q := sqlcgen.New(tx)
	var zero domain.HostingDatabaseNetworkCapture
	if err := networkCaptureSchema(ctx, q); err != nil {
		return zero, err
	}
	if _, err := q.LockHostingTaskForAuthority(ctx, "capture_"+id); err != nil {
		return zero, hostingError(err)
	}
	c, err := readNetworkCapture(ctx, q, id)
	if err != nil {
		return zero, err
	}
	if !current && c.Receipt != nil {
		if err = networkCaptureHistorical(ctx, tx, c, c.CreatedBy); err != nil {
			return zero, err
		}
	} else {
		p, e := networkCaptureSource(ctx, tx, c.Plan.SourceOperationID, c.CreatedBy)
		if e != nil || !reflect.DeepEqual(p, c.Plan) {
			return zero, ErrConflict
		}
	}
	row, err := q.GetRun(ctx, c.RunID)
	if err != nil {
		return zero, err
	}
	run, err := taskRunFromSQLC(row)
	if err != nil || domain.ValidateHostingDatabaseNetworkCaptureTask(run) != nil {
		return zero, ErrConflict
	}
	return c, nil
}

// ClaimHostingDatabaseNetworkCapture is an internal, explicit read-only claim.
// It is deliberately not registered with generic ClaimRun or runtime adapters.
func (s *PostgresStore) ClaimHostingDatabaseNetworkCapture(ctx context.Context, runner, credential, id string, ttl time.Duration) (domain.HostingDatabaseNetworkCaptureClaim, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseNetworkCaptureClaim, error) {
		var zero domain.HostingDatabaseNetworkCaptureClaim
		if ttl <= 0 || ttl > 10*time.Minute {
			return zero, ErrConflict
		}
		c, err := lockNetworkCapture(ctx, tx, id, true)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		if err = networkCaptureRunner(ctx, q, runner, credential, c.Plan); err != nil {
			return zero, err
		}
		if c.Receipt != nil {
			return zero, ErrConflict
		}
		existing, err := q.GetActiveLeaseForRun(ctx, c.RunID)
		if err == nil {
			if existing.RunnerID != runner {
				return zero, ErrConflict
			}
			return domain.HostingDatabaseNetworkCaptureClaim{Capture: c, Lease: runLeaseFromSQLC(existing)}, finalizeNetworkCapture(ctx, tx, c, existing.ID, true)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if _, err = q.ExpireHostingObservationLease(ctx, c.RunID); err != nil {
			return zero, err
		}
		if _, err = q.ResetHostingObservation(ctx, c.RunID); err != nil {
			return zero, err
		}
		attempt, err := q.ClaimQueuedRun(ctx, sqlcgen.ClaimQueuedRunParams{RunID: c.RunID, RunnerID: &runner})
		if err != nil {
			return zero, err
		}
		lease, err := newLeaseToken("lease")
		if err != nil {
			return zero, err
		}
		fence, err := newLeaseToken("fence")
		if err != nil {
			return zero, err
		}
		row, err := q.CreateActiveRunLease(ctx, sqlcgen.CreateActiveRunLeaseParams{ID: lease, RunID: c.RunID, RunnerID: runner, Status: domain.LeaseActive, TtlMicroseconds: ttl.Microseconds(), Attempt: attempt, Fence: fence})
		if err != nil {
			return zero, err
		}
		if err = createAuditWithQueries(ctx, q, domain.AuditEvent{ID: "aud_capture_claim_" + lease, ActorID: runner, Action: "hosting.database_network_capture.claimed", TargetID: c.ID, CreatedAt: time.Now().UTC(), Metadata: map[string]any{"plan_hash": c.Plan.Hash}}); err != nil {
			return zero, err
		}
		c.State = "running"
		return domain.HostingDatabaseNetworkCaptureClaim{Capture: c, Lease: runLeaseFromSQLC(row)}, finalizeNetworkCapture(ctx, tx, c, lease, true)
	})
}

func networkCaptureAuthority(ctx context.Context, tx pgx.Tx, runner, credential string, a domain.HostingAuthority, live bool) (domain.HostingDatabaseNetworkCapture, sqlcgen.RunLease, error) {
	var zero domain.HostingDatabaseNetworkCapture
	var lease sqlcgen.RunLease
	if a.Attempt < 1 || a.Attempt > 2147483647 {
		return zero, lease, ErrConflict
	}
	c, err := lockNetworkCapture(ctx, tx, a.OperationID, live)
	if err != nil {
		return zero, lease, err
	}
	if c.RunID != a.RunID {
		return zero, lease, ErrConflict
	}
	q := sqlcgen.New(tx)
	if err = networkCaptureRunner(ctx, q, runner, credential, c.Plan); err != nil {
		return zero, lease, err
	}
	lease, err = q.CheckDatabaseNetworkCaptureAuthority(ctx, sqlcgen.CheckDatabaseNetworkCaptureAuthorityParams{CaptureID: c.ID, LeaseID: a.LeaseID, RunID: a.RunID, RunnerID: runner, Attempt: int32(a.Attempt), Fence: a.Fence, RequireLive: live})
	return c, lease, hostingError(err)
}

// HostingDatabaseNetworkCapturePlan requires a current capture lease; replay is not new authority.
func (s *PostgresStore) HostingDatabaseNetworkCapturePlan(ctx context.Context, runner, credential string, a domain.HostingAuthority) (domain.HostingDatabaseNetworkCapture, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseNetworkCapture, error) {
		c, _, err := networkCaptureAuthority(ctx, tx, runner, credential, a, true)
		if err != nil {
			return domain.HostingDatabaseNetworkCapture{}, err
		}
		return c, finalizeNetworkCapture(ctx, tx, c, a.LeaseID, true)
	})
}

// RecordHostingDatabaseNetworkOrigin atomically retains attestation and completes only the capture task.
func (s *PostgresStore) RecordHostingDatabaseNetworkOrigin(ctx context.Context, runner, credential string, in domain.HostingDatabaseNetworkOriginRequest) (domain.HostingDatabaseNetworkOriginReceipt, error) {
	return buildTransaction(ctx, s, func(tx pgx.Tx) (domain.HostingDatabaseNetworkOriginReceipt, error) {
		var zero domain.HostingDatabaseNetworkOriginReceipt
		if in.Validate() != nil {
			return zero, ErrConflict
		}
		c, lease, err := networkCaptureAuthority(ctx, tx, runner, credential, in.HostingAuthority, false)
		if err != nil {
			return zero, err
		}
		q := sqlcgen.New(tx)
		if in.PlanHash != c.Plan.Hash || c.Plan.ValidateOrigin(in.Origin) != nil {
			return zero, ErrConflict
		}
		bodyHash := domain.HostingDocumentHash(in)
		prior, err := q.ReadDatabaseNetworkOriginReceipt(ctx, c.ID)
		if err == nil {
			var old domain.HostingAuthority
			if json.Unmarshal(prior.Authority, &old) != nil || old != in.HostingAuthority || prior.ReceiptID != in.ReceiptID || prior.BodyHash != bodyHash || c.Receipt == nil {
				return zero, ErrConflict
			}
			return *c.Receipt, finalizeNetworkCapture(ctx, tx, c, "", false)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return zero, err
		}
		if lease.Status != "active" || c.State != "running" {
			return zero, ErrConflict
		}
		now, err := q.DatabaseClock(ctx)
		if err != nil {
			return zero, err
		}
		if in.ObservedAt.Before(lease.CreatedAt.Add(-30*time.Second)) || in.ObservedAt.After(now.Add(30*time.Second)) {
			return zero, ErrConflict
		}
		result := domain.HostingDatabaseNetworkOriginReceipt{HostingReceipt: domain.HostingReceipt{ID: in.ReceiptID, BodyHash: bodyHash, CreatedAt: now}, OperationID: c.ID, SourceOperationID: c.Plan.SourceOperationID, PlanHash: c.Plan.Hash, OriginHash: in.Origin.DocumentHash(), Origin: in.Origin, ObservedAt: in.ObservedAt, RunnerID: runner}
		audit := domain.AuditEvent{ID: "aud_capture_origin_" + c.ID, ActorID: runner, Action: "hosting.database_network_capture.recorded", TargetID: c.ID, CreatedAt: now, Metadata: map[string]any{"source_operation_id": c.Plan.SourceOperationID, "plan_hash": c.Plan.Hash, "origin_hash": result.OriginHash}}
		if err = createAuditWithQueries(ctx, q, audit); err != nil {
			return zero, err
		}
		if err = q.InsertDatabaseNetworkOriginReceipt(ctx, sqlcgen.InsertDatabaseNetworkOriginReceiptParams{CaptureID: c.ID, ReceiptID: in.ReceiptID, Authority: buildJSON(in.HostingAuthority), Body: buildJSON(in), BodyHash: bodyHash, Receipt: buildJSON(result), AuditID: audit.ID}); err != nil {
			return zero, err
		}
		if _, err = q.CompleteHostingObservationTask(ctx, c.RunID); err != nil {
			return zero, err
		}
		if _, err = q.CompleteHostingObservationLease(ctx, sqlcgen.CompleteHostingObservationLeaseParams{ID: in.LeaseID, CompletionKey: hostingStringPointer(in.ReceiptID)}); err != nil {
			return zero, err
		}
		return result, finalizeNetworkCapture(ctx, tx, c, in.LeaseID, true)
	})
}

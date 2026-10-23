package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type storageMaterializationFixture struct {
	pg        *store.PostgresStore
	db        *pgxpool.Pool
	resource  domain.Resource
	runner    string
	storage   domain.HostingStorage
	op        domain.HostingOperation
	authority domain.HostingAuthority
}

func (f storageMaterializationFixture) complete(t *testing.T, outcome string, uncertain bool) domain.HostingOperation {
	t.Helper()
	op, err := f.pg.HostingComplete(t.Context(), f.runner, domain.HostingCompleteRequest{HostingAuthority: f.authority, ReceiptID: "complete_" + f.op.ID, Result: domain.HostingResult{Outcome: outcome, Uncertain: uncertain, Ready: outcome == "succeeded", ContainerID: "container_" + f.op.ID, ImageID: f.op.Release.ImageID, DaemonID: f.op.Release.DaemonID, PlanHash: f.op.Plan.Hash}})
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func TestHostingStorageMaterializationPostgresExpiredACKCannotAdmitWriter(t *testing.T) {
	f := newStorageMaterializationFixture(t)
	ack, err := f.pg.HostingStorageCreateBegin(t.Context(), f.runner, f.request())
	if err != nil {
		t.Fatal(err)
	}
	proof := f.proof(ack)
	first, err := f.pg.HostingStorageMaterialized(t.Context(), f.runner, proof)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, f.authority.LeaseID); err != nil {
		t.Fatal(err)
	}
	replay, err := f.pg.HostingStorageMaterialized(t.Context(), f.runner, proof)
	if err != nil || replay.Receipt != first.Receipt {
		t.Fatalf("historical exact ACK unavailable: %+v %v", replay, err)
	}
	if _, err = f.pg.HostingApplyBegin(t.Context(), f.runner, f.authority); err == nil {
		t.Fatal("historical ACK revived expired writer authority")
	}
	if _, err = f.pg.HostingStorageCreateBegin(t.Context(), f.runner, f.request()); err == nil {
		t.Fatal("expired create admission returned permission")
	}
	f.count(t, 1, 1, 1)
}

func TestHostingStorageMaterializationPostgresRevocation(t *testing.T) {
	for _, revoke := range []string{"membership", "grant", "capability"} {
		t.Run(revoke, func(t *testing.T) {
			f := newStorageMaterializationFixture(t)
			ack, err := f.pg.HostingStorageCreateBegin(t.Context(), f.runner, f.request())
			if err != nil {
				t.Fatal(err)
			}
			switch revoke {
			case "membership":
				_, err = f.db.Exec(t.Context(), `UPDATE users SET global_role='user' WHERE id='usr_bootstrap'; UPDATE project_members SET role='viewer' WHERE user_id='usr_bootstrap'`)
			case "grant":
				_, err = f.pg.RevokeDestinationProject(t.Context(), f.op.Snapshot.DestinationID, f.resource.ProjectID, destinationEvent("storage_revoke"))
			case "capability":
				_, err = f.db.Exec(t.Context(), `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingOwnedStorageCapability)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.pg.HostingStorageCreateBegin(t.Context(), f.runner, f.request()); err == nil {
				t.Fatal("revocation returned create admission")
			}
			if _, err = f.pg.HostingStorageMaterialized(t.Context(), f.runner, f.proof(ack)); err == nil {
				t.Fatal("revocation published receipt")
			}
			f.count(t, 1, 1, 0)
		})
	}
}

func TestHostingStorageMaterializationPostgresFailureRetainsHistory(t *testing.T) {
	f := newStorageMaterializationFixture(t)
	if _, err := f.pg.HostingStorageCreateBegin(t.Context(), f.runner, f.request()); err != nil {
		t.Fatal(err)
	}
	failed := f.complete(t, "failed", true)
	if failed.State != "reconciliation_required" || !failed.MutationHold {
		t.Fatalf("failed creation unlocked unresolved storage: %+v", failed)
	}
	f.count(t, 1, 1, 0)
	if _, err := f.pg.RequestHostingReconciliation(t.Context(), f.op.ID, destinationEvent("storage_failed_observe")); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(t.Context(), f.runner, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: f.op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingReconciliation(t.Context(), f.runner, domain.HostingObservation{HostingAuthority: a, BindingID: f.op.BindingID, DaemonID: f.op.Release.DaemonID, NoOwnedContainers: true}); err == nil {
		t.Fatal("no-owned-containers erased unresolved creation hold")
	}
	f.count(t, 1, 1, 0)
}

func TestHostingStorageMaterializationPostgresSecondReleasePreservesOrigin(t *testing.T) {
	f := newStorageMaterializationFixture(t)
	ack, err := f.pg.HostingStorageCreateBegin(t.Context(), f.runner, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingStorageMaterialized(t.Context(), f.runner, f.proof(ack)); err != nil {
		t.Fatal(err)
	}
	// Materialization alone cannot be reported as application success.
	if _, err = f.pg.HostingComplete(t.Context(), f.runner, domain.HostingCompleteRequest{HostingAuthority: f.authority, ReceiptID: "premature_success", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: "premature", ImageID: f.op.Release.ImageID, DaemonID: f.op.Release.DaemonID, PlanHash: f.op.Plan.Hash}}); err == nil {
		t.Fatal("success accepted before application admission")
	}
	if _, err = f.pg.HostingApplyBegin(t.Context(), f.runner, f.authority); err != nil {
		t.Fatal(err)
	}
	f.complete(t, "succeeded", false)
	original := ack.Materialization
	input := hostingDeploymentRequest()
	input.IdempotencyKey, input.HostPort = "storage_second", 0
	next, err := f.pg.RequestHostingDeployment(t.Context(), "hop_storage_second", f.resource.ID, "usr_bootstrap", input, destinationEvent("storage_second"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pg.PrepareDeployment(t.Context(), next.ID); err != nil {
		t.Fatal(err)
	}
	f.authority, _ = prepareHostingAttempt(t, f.pg, next, f.runner)
	f.op, err = f.pg.GetHostingOperation(t.Context(), next.ID)
	if err != nil || f.op.Plan.RetainPrevious || f.op.Plan.Hash == original.CreationPlanHash || f.op.Plan.Storage.CreationOperationID != original.CreationOperationID || f.op.Plan.PreviousHealthy.Storage.CreationPlanHash != original.CreationPlanHash {
		t.Fatalf("second plan lost fixed origin or two-writer exclusion: %+v %v", f.op, err)
	}
	ack, err = f.pg.HostingStorageCreateBegin(t.Context(), f.runner, f.request())
	if err != nil || ack.CreatePermitted || ack.Materialization.CreationPlanHash != original.CreationPlanHash {
		t.Fatalf("second release recreated storage: %+v %v", ack, err)
	}
	if _, err = f.pg.HostingApplyBegin(t.Context(), f.runner, f.authority); err == nil {
		t.Fatal("historical first-release receipt admitted second writer")
	}
	if _, err = f.pg.HostingStorageMaterialized(t.Context(), f.runner, f.proof(ack)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingApplyBegin(t.Context(), f.runner, f.authority); err != nil {
		t.Fatal(err)
	}
	f.complete(t, "succeeded", false)
	var healthy domain.HostingHealthy
	var body []byte
	if err = f.db.QueryRow(t.Context(), `SELECT healthy FROM hosting_binding_state WHERE binding_id=$1`, f.op.BindingID).Scan(&body); err != nil || json.Unmarshal(body, &healthy) != nil || healthy.Storage == nil || healthy.Storage.CreationPlanHash != original.CreationPlanHash || healthy.Storage.CreationPlanHash == healthy.PlanHash {
		t.Fatalf("healthy origin changed to newest plan: %+v %v", healthy, err)
	}
	if _, err = f.pg.RequestHostingReconciliation(t.Context(), f.op.ID, destinationEvent("storage_healthy_observe")); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(t.Context(), f.runner, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: f.op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	observed, err := f.pg.HostingRunnerPlan(t.Context(), f.runner, a)
	if err != nil || observed.ReconciliationHealthy == nil || observed.ReconciliationHealthy.Storage == nil || observed.ReconciliationHealthy.Storage.CreationPlanHash != original.CreationPlanHash {
		t.Fatalf("observation plan lost original identity: %+v %v", observed, err)
	}
	if _, err = f.pg.HostingReconciliation(t.Context(), f.runner, domain.HostingObservation{HostingAuthority: a, BindingID: f.op.BindingID, DaemonID: healthy.Release.DaemonID, Ready: true, ContainerID: healthy.ContainerID, ImageID: healthy.Release.ImageID, PlanHash: healthy.PlanHash, ReleaseHash: healthy.Release.Hash}); err != nil {
		t.Fatal(err)
	}
	f.count(t, 1, 2, 2)
}

func newStorageMaterializationFixture(t *testing.T) storageMaterializationFixture {
	t.Helper()
	pg, db, r, server, _ := hostingExecutionFixture(t)
	if _, err := db.Exec(t.Context(), `UPDATE runners SET capabilities=array_append(array_append(capabilities,$2),$3) WHERE id=$1`, server.RunnerID, domain.HostingReleasePortsCapability, domain.HostingOwnedStorageCapability); err != nil {
		t.Fatal(err)
	}
	storage := saveHostingStorage(t, pg, r.ID, hostingStorageInput())
	input := hostingDeploymentRequest()
	input.HostPort = 0
	op, err := pg.RequestHostingDeployment(t.Context(), "hop_storage_origin", r.ID, "usr_bootstrap", input, destinationEvent("storage_origin"))
	if err != nil {
		t.Fatal(err)
	}
	if err = pg.PrepareDeployment(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := pg.ClaimRun(t.Context(), server.RunnerID, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = pg.HostingRunnerPlan(t.Context(), server.RunnerID, a); err != nil {
		t.Fatal(err)
	}
	release := domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "amd64", DaemonID: "daemon_image_host"}
	release.Hash = domain.HostingDocumentHash(release)
	op, err = pg.HostingPrepared(t.Context(), server.RunnerID, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "storage_prepared", Release: release})
	if err != nil {
		t.Fatal(err)
	}
	return storageMaterializationFixture{pg, db, r, server.RunnerID, storage, op, a}
}
func (f storageMaterializationFixture) request() domain.HostingStorageCreateRequest {
	return domain.HostingStorageCreateRequest{HostingAuthority: f.authority, PlanHash: f.op.Plan.Hash, StorageRef: f.storage.Reference(), CreationNonce: f.op.Plan.Storage.CreationNonce}
}
func (f storageMaterializationFixture) proof(ack domain.HostingStorageCreateAck) domain.HostingStorageMaterializedRequest {
	m := ack.Materialization
	return domain.HostingStorageMaterializedRequest{HostingAuthority: f.authority, ReceiptID: "storage_materialized", PlanHash: f.op.Plan.Hash, StorageRef: f.storage.Reference(), CreationOperationID: m.CreationOperationID, CreationPlanHash: m.CreationPlanHash, CreationNonce: m.CreationNonce, DaemonID: m.DaemonID, VolumeName: f.storage.VolumeName, Driver: "local", Scope: "local", Labels: maps.Clone(m.Labels)}
}
func (f storageMaterializationFixture) count(t *testing.T, origins, admissions, receipts int) {
	t.Helper()
	var a, b, c int
	if err := f.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM hosting_storage_materializations),(SELECT count(*) FROM hosting_storage_operation_admissions),(SELECT count(*) FROM hosting_storage_materialized_receipts)`).Scan(&a, &b, &c); err != nil {
		t.Fatal(err)
	}
	if a != origins || b != admissions || c != receipts {
		t.Fatalf("origin=%d admission=%d receipt=%d", a, b, c)
	}
}
func TestHostingStorageMaterializationPostgresAdmissionAndWriterGate(t *testing.T) {
	f := newStorageMaterializationFixture(t)
	ctx := t.Context()
	if _, err := f.pg.HostingApplyBegin(ctx, f.runner, f.authority); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("writer admitted before create admission: %v", err)
	}
	ack, err := f.pg.HostingStorageCreateBegin(ctx, f.runner, f.request())
	if err != nil || !ack.CreatePermitted || ack.Materialization.State != "create_admitted" {
		t.Fatalf("first admission: %+v %v", ack, err)
	}
	op, err := f.pg.GetHostingOperation(ctx, f.op.ID)
	if err != nil || op.State != "prepared" || !op.MutationHold {
		t.Fatalf("storage preparation failed to retain independent hold: %+v %v", op, err)
	}
	f.count(t, 1, 1, 0)
	if _, err = f.pg.HostingApplyBegin(ctx, f.runner, f.authority); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("writer admitted before materialization ACK: %v", err)
	}
	// A lost admission ACK is conservative: no new permission, even while no
	// volume observation exists. No fixture pretends that Docker was called.
	replay, err := f.pg.HostingStorageCreateBegin(ctx, f.runner, f.request())
	if err != nil || replay.CreatePermitted || replay.Materialization.CreationNonce != ack.Materialization.CreationNonce {
		t.Fatalf("replayed permission: %+v %v", replay, err)
	}
	proof := f.proof(ack)
	ready, err := f.pg.HostingStorageMaterialized(ctx, f.runner, proof)
	if err != nil || ready.Materialization.State != "materialized" || ready.Receipt.BodyHash != domain.HostingDocumentHash(proof) {
		t.Fatalf("materialized: %+v %v", ready, err)
	}
	exact, err := f.pg.HostingStorageMaterialized(ctx, f.runner, proof)
	if err != nil || exact.Receipt != ready.Receipt {
		t.Fatalf("lost materialization ACK: %+v %v", exact, err)
	}
	op, err = f.pg.HostingApplyBegin(ctx, f.runner, f.authority)
	if err != nil || op.State != "applying" || !op.MutationHold {
		t.Fatalf("writer after ACK: %+v %v", op, err)
	}
	f.count(t, 1, 1, 1)
}
func TestHostingStorageMaterializationPostgresDuplicateAndMismatch(t *testing.T) {
	f := newStorageMaterializationFixture(t)
	var wg sync.WaitGroup
	results := make(chan domain.HostingStorageCreateAck, 2)
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			ack, err := f.pg.HostingStorageCreateBegin(t.Context(), f.runner, f.request())
			results <- ack
			errs <- err
		})
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	permits := 0
	var ack domain.HostingStorageCreateAck
	for result := range results {
		ack = result
		if result.CreatePermitted {
			permits++
		}
	}
	if permits != 1 {
		t.Fatalf("create permissions=%d", permits)
	}
	for _, mutate := range []func(*domain.HostingStorageMaterializedRequest){func(p *domain.HostingStorageMaterializedRequest) { p.DaemonID = "different" }, func(p *domain.HostingStorageMaterializedRequest) { p.PlanHash = "sha256:" + strings.Repeat("0", 64) }, func(p *domain.HostingStorageMaterializedRequest) { p.Labels["io.nerocd.storage.id"] = "foreign" }, func(p *domain.HostingStorageMaterializedRequest) {
		p.Options = map[string]string{"device": "/host/path"}
	}} {
		in := f.proof(ack)
		mutate(&in)
		if _, err := f.pg.HostingStorageMaterialized(t.Context(), f.runner, in); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("foreign observation accepted: %v", err)
		}
	}
	stale := f.proof(ack)
	stale.Fence = "stale"
	if _, err := f.pg.HostingStorageMaterialized(t.Context(), f.runner, stale); err == nil {
		t.Fatal("stale authority published materialization")
	}
	f.count(t, 1, 1, 0)
}
func TestHostingStorageMaterializationPostgresCancellationRetainsHistory(t *testing.T) {
	f := newStorageMaterializationFixture(t)
	ctx := t.Context()
	ack, err := f.pg.HostingStorageCreateBegin(ctx, f.runner, f.request())
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := f.pg.CancelHostingOperation(ctx, f.op.ID, destinationEvent("cancel_storage"))
	if err != nil || canceled.State != "cancel_requested" || !canceled.MutationHold {
		t.Fatalf("cancellation lost hold: %+v %v", canceled, err)
	}
	if _, err = f.pg.HostingStorageMaterialized(ctx, f.runner, f.proof(ack)); err == nil {
		t.Fatal("canceled attempt published new receipt")
	}
	f.count(t, 1, 1, 0)
	next := hostingDeploymentRequest()
	next.IdempotencyKey = "next"
	next.HostPort = 0
	if _, err = f.pg.RequestHostingDeployment(ctx, "hop_next", f.resource.ID, "usr_bootstrap", next, destinationEvent("next")); err == nil {
		t.Fatal("unresolved creation unlocked later deployment")
	}
}
func TestHostingStorageMaterializationPostgresAuditRollback(t *testing.T) {
	f := newStorageMaterializationFixture(t)
	ctx := t.Context()
	if _, err := f.db.Exec(ctx, `CREATE FUNCTION fail_storage_creation_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.storage.create_admitted' THEN RAISE EXCEPTION 'test admission rollback' USING ERRCODE='23514'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_storage_creation_audit AFTER INSERT ON audit_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_storage_creation_audit()`); err != nil {
		t.Fatal(err)
	}
	if ack, err := f.pg.HostingStorageCreateBegin(ctx, f.runner, f.request()); !errors.Is(err, store.ErrConflict) || ack.CreatePermitted {
		t.Fatalf("failed commit granted permission: %+v %v", ack, err)
	}
	f.count(t, 0, 0, 0)
	op, err := f.pg.GetHostingOperation(ctx, f.op.ID)
	if err != nil || op.MutationHold || op.State != "prepared" {
		t.Fatalf("rollback left hold: %+v %v", op, err)
	}
}

func TestHostingStorageMaterializationPostgresReceiptAuditRollbackAndImmutableSchema(t *testing.T) {
	f := newStorageMaterializationFixture(t)
	ctx := t.Context()
	ack, err := f.pg.HostingStorageCreateBegin(ctx, f.runner, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `CREATE FUNCTION fail_storage_receipt_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.storage.materialized' THEN RAISE EXCEPTION 'test receipt rollback' USING ERRCODE='23514'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER fail_storage_receipt_audit AFTER INSERT ON audit_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_storage_receipt_audit()`); err != nil {
		t.Fatal(err)
	}
	if result, err := f.pg.HostingStorageMaterialized(ctx, f.runner, f.proof(ack)); err == nil || result.Receipt.ID != "" {
		t.Fatalf("failed receipt commit returned ACK: %+v %v", result, err)
	}
	f.count(t, 1, 1, 0)
	if _, err = f.db.Exec(ctx, `DROP TRIGGER fail_storage_receipt_audit ON audit_events; DROP FUNCTION fail_storage_receipt_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingStorageMaterialized(ctx, f.runner, f.proof(ack)); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`UPDATE hosting_storage_materializations SET origin_plan_hash='sha256:'||repeat('0',64)`,
		`UPDATE hosting_storage_materializations SET materialized_receipt_id=NULL,materialized_operation_id=NULL`,
		`DELETE FROM hosting_storage_materializations`,
		`DELETE FROM hosting_storage_operation_admissions`,
		`UPDATE hosting_storage_operation_admissions SET authority='{}'::jsonb`,
		`DELETE FROM hosting_storage_materialized_receipts`,
		`UPDATE hosting_storage_materialized_receipts SET body='{}'::jsonb`,
	} {
		if _, err = f.db.Exec(ctx, statement); err == nil {
			t.Fatalf("mutable storage history accepted: %s", statement)
		}
	}
	if _, err = f.db.Exec(ctx, `ALTER TABLE run_leases DISABLE TRIGGER hosting_storage_lease`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingStorageCreateBegin(ctx, f.runner, f.request()); !errors.Is(err, store.ErrHostingStorageUnavailable) {
		t.Fatalf("admission accepted incompatible schema: %v", err)
	}
	if _, err = f.pg.HostingStorageMaterialized(ctx, f.runner, f.proof(ack)); !errors.Is(err, store.ErrHostingStorageUnavailable) {
		t.Fatalf("historical receipt accepted incompatible schema: %v", err)
	}
	if _, err = f.pg.HostingApplyBegin(ctx, f.runner, f.authority); !errors.Is(err, store.ErrHostingStorageUnavailable) {
		t.Fatalf("writer accepted incompatible schema: %v", err)
	}
	f.count(t, 1, 1, 1)
}

func TestHostingStorageMaterializationPostgresExpiryDuringOriginLock(t *testing.T) {
	f := newStorageMaterializationFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ack, err := f.pg.HostingStorageCreateBegin(ctx, f.runner, f.request())
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rollbackBlocker := func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := blocker.Rollback(cleanup); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("rollback origin blocker: %v", err)
		}
	}
	t.Cleanup(rollbackBlocker)
	var pid int
	if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid() FROM hosting_storage_materializations WHERE storage_id=$1 FOR UPDATE`, f.storage.ID).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '600 milliseconds' WHERE id=$1`, f.authority.LeaseID); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, callErr := f.pg.HostingStorageMaterialized(ctx, f.runner, f.proof(ack))
		finished <- callErr
	}()
	defer func() {
		cancel()
		rollbackBlocker()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("materialization call did not join")
		}
	}()
	for {
		var waiting bool
		if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("materialization never waited on locked origin")
		case <-time.After(5 * time.Millisecond):
		}
	}
	for {
		var expired bool
		if err = blocker.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() FROM run_leases WHERE id=$1`, f.authority.LeaseID).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("lease did not expire")
		case <-time.After(5 * time.Millisecond):
		}
	}
	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-finished; err == nil {
		t.Fatal("expired blocked attempt committed new materialization")
	}
	f.count(t, 1, 1, 0)
}

package store_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

// This fixture uses persisted registry-image evidence, without Docker effects.
func registryCleanupFixture(t *testing.T) (*store.PostgresStore, *pgxpool.Pool, domain.Resource, domain.Server, domain.HostingOperation) {
	t.Helper()
	pg, db, resource, server, _ := hostingExecutionFixture(t)
	if _, err := db.Exec(t.Context(), `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	old := settleProxyImage(t, pg, resource, server, "cleanup-registry-old", 27001)
	current := settleProxyImage(t, pg, resource, server, "cleanup-registry-current", 27002)
	if !current.Plan.RetainPrevious {
		t.Fatal("fixture did not retain registry release")
	}
	cleanup, err := pg.RequestHostingReleaseCleanup(t.Context(), "cleanup-registry", resource.ID, old.ID, "usr_bootstrap", domain.HostingCleanupInput{ExpectedGeneration: resource.Generation, IdempotencyKey: "cleanup-registry"}, destinationEvent("cleanup-registry"))
	if err != nil {
		t.Fatal(err)
	}
	cleanup = awaitHostingDispatch(t, pg, cleanup.ID)
	if cleanup.Snapshot.LocalBuild != (domain.HostingLocalBuildSource{}) {
		t.Fatal("registry fixture became local build")
	}
	return pg, db, resource, server, cleanup
}
func claimRegistryCleanup(t *testing.T, pg *store.PostgresStore, runner string, op domain.HostingOperation) (domain.HostingAuthority, domain.HostingOperation) {
	t.Helper()
	claim, err := pg.ClaimRun(t.Context(), runner, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Run.ID != op.RunID || claim.Run.RunSpec.HostingMode != "cleanup" {
		t.Fatalf("unexpected cleanup task %+v", claim.Run)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	planned, err := pg.HostingRunnerPlan(t.Context(), runner, a)
	if err != nil {
		t.Fatal(err)
	}
	return a, planned
}
func TestPostgresRegistryCleanupExpiryRetainsUncertainty(t *testing.T) {
	pg, db, resource, server, op := registryCleanupFixture(t)
	a, planned := claimRegistryCleanup(t, pg, server.RunnerID, op)
	admitted, err := pg.HostingApplyBegin(t.Context(), server.RunnerID, a)
	if err != nil || !admitted.MutationHold {
		t.Fatal("cleanup not admitted", err)
	}
	if _, err = db.Exec(t.Context(), `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err = pg.ExpireLeases(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	after, err := pg.GetHostingOperation(t.Context(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	var task, lease string
	if err = db.QueryRow(t.Context(), `SELECT t.status,l.status FROM task_runs t JOIN run_leases l ON l.run_id=t.id WHERE l.id=$1`, a.LeaseID).Scan(&task, &lease); err != nil {
		t.Fatal(err)
	}
	t.Logf("expired registry cleanup: operation=%s task=%s lease=%s hold=%v", after.State, task, lease, after.MutationHold)
	if after.State != "reconciliation_required" || !after.MutationHold || task != "failed" || lease != "expired" {
		t.Fatal("admitted registry cleanup lost durable uncertainty or was requeued")
	}
	if !reflect.DeepEqual(after.CleanupPlan, planned.CleanupPlan) {
		t.Fatal("expiry changed immutable cleanup target")
	}
	if _, err = pg.ClaimRun(t.Context(), server.RunnerID, time.Now(), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("uncertain cleanup acquired a second lease", err)
	}
	if err = pg.PrepareDeployment(t.Context(), op.ID); err != nil {
		t.Fatal(err)
	}
	next := hostingDeploymentRequest()
	next.IdempotencyKey = "conflicting-with-cleanup"
	next.HostPort = 27003
	if _, err = pg.RequestHostingDeployment(t.Context(), "conflicting-registry-deploy", resource.ID, "usr_bootstrap", next, destinationEvent("conflicting-registry-deploy")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("uncertain cleanup allowed conflicting deployment", err)
	}
	var leases, admissions int
	var hold string
	if err = db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM run_leases WHERE run_id=$1),(SELECT count(*) FROM hosting_apply_admissions WHERE operation_id=$2),(SELECT hold_operation_id FROM hosting_binding_state WHERE binding_id=$3)`, a.RunID, op.ID, op.BindingID).Scan(&leases, &admissions, &hold); err != nil || leases != 1 || admissions != 1 || hold != op.ID {
		t.Fatal("cleanup authority was replaced", leases, admissions, hold, err)
	}
	retained, err := pg.ListHostingRetainedReleases(t.Context(), resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, release := range retained {
		if release.Healthy.OperationID == planned.CleanupPlan.Target.OperationID {
			found = true
			if release.State != "cleanup_pending" {
				t.Fatal("expiry released retained ownership", release.State)
			}
		}
	}
	if !found {
		t.Fatal("retained cleanup target disappeared")
	}
}

func TestPostgresRegistryCleanupPreAdmissionExpiryAndCommittedReplay(t *testing.T) {
	pg, db, resource, server, op := registryCleanupFixture(t)
	first, planned := claimRegistryCleanup(t, pg, server.RunnerID, op)
	if _, err := db.Exec(t.Context(), `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, first.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err := pg.ExpireLeases(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	queued, err := pg.GetHostingOperation(t.Context(), op.ID)
	if err != nil || queued.State != "dispatched" || queued.MutationHold {
		t.Fatal("pre-admission cleanup could not requeue", queued.State, err)
	}
	second, replanned := claimRegistryCleanup(t, pg, server.RunnerID, op)
	if second.RunID != first.RunID || second.Attempt != first.Attempt+1 || second.Fence == first.Fence || !reflect.DeepEqual(replanned.CleanupPlan, planned.CleanupPlan) {
		t.Fatal("pre-admission retry changed target or reused authority")
	}
	if _, err = pg.HostingApplyBegin(t.Context(), server.RunnerID, first); err == nil {
		t.Fatal("expired preparation admitted cleanup")
	}
	if _, err = pg.HostingApplyBegin(t.Context(), server.RunnerID, second); err != nil {
		t.Fatal(err)
	}
	result := domain.HostingResult{Outcome: "succeeded", PlanHash: planned.CleanupPlan.Hash, DaemonID: planned.CleanupPlan.Target.Release.DaemonID, ImageID: planned.CleanupPlan.Target.Release.ImageID, ContainerID: planned.CleanupPlan.Target.ContainerID}
	report := domain.HostingCompleteRequest{HostingAuthority: second, ReceiptID: "registry-cleanup-complete", Result: result}
	complete, err := pg.HostingComplete(t.Context(), server.RunnerID, report)
	if err != nil || complete.State != "succeeded" || complete.MutationHold {
		t.Fatal("cleanup completion", complete.State, err)
	}
	if _, err = db.Exec(t.Context(), `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, second.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err = pg.ExpireLeases(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	replay, err := pg.HostingComplete(t.Context(), server.RunnerID, report)
	if err != nil || replay.ID != op.ID || replay.State != "succeeded" || replay.MutationHold {
		t.Fatal("exact terminal receipt replay", err)
	}
	changed := report
	changed.Result.ContainerID = "another-container"
	if _, err = pg.HostingComplete(t.Context(), server.RunnerID, changed); !errors.Is(err, store.ErrConflict) {
		t.Fatal("different completion replay accepted", err)
	}
	if _, err = pg.ClaimRun(t.Context(), server.RunnerID, time.Now(), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("successful cleanup recreated work", err)
	}
	var leases, admissions, receipts int
	var task string
	if err = db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM run_leases WHERE run_id=$1),(SELECT count(*) FROM hosting_apply_admissions WHERE operation_id=$2),(SELECT count(*) FROM hosting_receipts WHERE operation_id=$2 AND phase='complete'),(SELECT status FROM task_runs WHERE id=$1)`, op.RunID, op.ID).Scan(&leases, &admissions, &receipts, &task); err != nil || leases != 2 || admissions != 1 || receipts != 1 || task != "succeeded" {
		t.Fatal("cleanup replay duplicated lifecycle writes", leases, admissions, receipts, task, err)
	}
	next := hostingDeploymentRequest()
	next.IdempotencyKey = "after-cleanup"
	next.HostPort = 27003
	if _, err = pg.RequestHostingDeployment(t.Context(), "registry-after-cleanup", resource.ID, "usr_bootstrap", next, destinationEvent("registry-after-cleanup")); err != nil {
		t.Fatal("successful cleanup retained conflicting hold", err)
	}
}

func TestPostgresRegistryQueuedCleanupCancellationStaysTerminal(t *testing.T) {
	pg, db, resource, server, op := registryCleanupFixture(t)
	canceled, err := pg.CancelHostingOperation(t.Context(), op.ID, destinationEvent("cancel-queued-registry-cleanup"))
	if err != nil || canceled.State != "canceled" || canceled.MutationHold {
		t.Fatal("queued cleanup cancellation", err)
	}
	if err = pg.ExpireLeases(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	replay, err := pg.RequestHostingReleaseCleanup(t.Context(), "ignored-cleanup-replay", resource.ID, op.CleanupPlan.Target.OperationID, "usr_bootstrap", domain.HostingCleanupInput{ExpectedGeneration: resource.Generation, IdempotencyKey: "cleanup-registry"}, destinationEvent("replay-canceled-registry-cleanup"))
	if err != nil || replay.ID != op.ID || replay.State != "canceled" {
		t.Fatal("canceled request replay created cleanup work", err)
	}
	if _, err = pg.ClaimRun(t.Context(), server.RunnerID, time.Now(), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("canceled cleanup claimed", err)
	}
	var leases, admissions int
	if err = db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM run_leases WHERE run_id=$1),(SELECT count(*) FROM hosting_apply_admissions WHERE operation_id=$2)`, op.RunID, op.ID).Scan(&leases, &admissions); err != nil || leases != 0 || admissions != 0 {
		t.Fatal("queued cancellation acquired mutation authority", leases, admissions, err)
	}
	next, err := pg.RequestHostingReleaseCleanup(t.Context(), "new-registry-cleanup", resource.ID, op.CleanupPlan.Target.OperationID, "usr_bootstrap", domain.HostingCleanupInput{ExpectedGeneration: resource.Generation, IdempotencyKey: "new-registry-cleanup"}, destinationEvent("new-registry-cleanup"))
	if err != nil || next.ID == op.ID || !reflect.DeepEqual(next.CleanupPlan, op.CleanupPlan) {
		t.Fatal("safe new cleanup after cancellation", err)
	}
}

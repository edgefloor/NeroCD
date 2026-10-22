package store_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

func hostingExecutionFixture(t *testing.T, stopOut ...*func(context.Context) error) (*store.PostgresStore, *pgxpool.Pool, domain.Resource, domain.Server, domain.Destination) {
	t.Helper()
	ctx := t.Context()
	pg, database := openPostgresIntegrationStore(t, ctx, "image_execution")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	resource := createDestinationResource(t, pg, "image_web", "proj_platform")
	now := time.Now().UTC()
	credential := fmt.Sprintf("%x", sha256.Sum256([]byte("image-hosting-fixture-token")))
	enrollment, err := pg.CreateRunnerEnrollment(ctx, domain.RunnerEnrollment{ID: "enroll_runner_image_host", TokenHash: "enrollhash_runner_image_host", RunnerID: "runner_image_host", RunnerName: "runner_image_host", Tags: []string{"shared-tag"}, Capabilities: []string{domain.RunTypeComposeDeploy, domain.RunTypeImageDeploy}, CreatedBy: "usr_bootstrap", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, destinationEvent("enroll_runner_image_host"))
	if err != nil {
		t.Fatal(err)
	}
	runner, err := pg.ConsumeRunnerEnrollment(ctx, domain.RunnerEnrollmentConsume{TokenHash: enrollment.TokenHash, RequestID: "consume_runner_image_host", CredentialHash: credential}, destinationEvent("consume_runner_image_host"))
	if err != nil {
		t.Fatal(err)
	}
	event := destinationEvent("image_server")
	server, destination, err := pg.CreateServerWithDestination(ctx, domain.Server{ID: "server_image_host", RunnerID: runner.ID, Name: "image-host", CreatedBy: event.ActorID, CreatedAt: event.CreatedAt}, domain.Destination{ID: "dest_image_host", ServerID: "server_image_host", Name: "default", RuntimeKind: "docker_compose", CreatedAt: event.CreatedAt}, event)
	if err != nil {
		t.Fatal(err)
	}
	grantDestination(t, pg, destination.ID, resource.ProjectID, "image_grant")
	if _, err = pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_image_host", ResourceID: resource.ID, DestinationID: destination.ID, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("image_binding")); err != nil {
		t.Fatal(err)
	}
	in := hostingDeploymentRequest()
	if _, err = pg.RequestHostingDeployment(ctx, "hop_before_queue", resource.ID, "usr_bootstrap", in, destinationEvent("before_queue")); !errors.Is(err, hostingjobs.ErrUnavailable) {
		t.Fatalf("missing consumer accepted request: %v", err)
	}
	if stop, err := pg.StartHostingExecution(ctx); err == nil {
		_ = stop(context.Background())
		t.Fatal("missing River schema accepted")
	}
	for {
		tx, err := database.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		changed, err := hostingjobs.MigrateNextTx(ctx, tx)
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if !changed {
			break
		}
	}
	stop, err := pg.StartHostingExecution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stopOut) > 0 {
		*stopOut[0] = stop
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := stop(stopCtx); err != nil {
			t.Error(err)
		}
	})
	return pg, database, resource, server, destination
}
func hostingDeploymentRequest() domain.HostingDeploymentInput {
	return domain.HostingDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "deploy-image", ContainerPort: 80, HostPort: 18080, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}}
}

func TestPostgresHostingUnadmittedCancelReleasesOnlyItsPortClaim(t *testing.T) {
	pg, database, resource, _, _ := hostingExecutionFixture(t)
	ctx := t.Context()
	in := hostingDeploymentRequest()
	op, err := pg.RequestHostingDeployment(ctx, "hop_cancel_port", resource.ID, "usr_bootstrap", in, destinationEvent("cancel_port"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pg.CancelHostingOperation(ctx, op.ID, destinationEvent("cancel_port_now")); err != nil {
		t.Fatal(err)
	}
	var claim string
	if err = database.QueryRow(ctx, `SELECT state FROM hosting_release_claims WHERE operation_id=$1`, op.ID).Scan(&claim); err != nil || claim != "released" {
		t.Fatalf("unadmitted claim=%s %v", claim, err)
	}
	in.IdempotencyKey = "after-cancel"
	if _, err = pg.RequestHostingDeployment(ctx, "hop_after_cancel_port", resource.ID, "usr_bootstrap", in, destinationEvent("after_cancel_port")); err != nil {
		t.Fatalf("canceled reservation blocked same port: %v", err)
	}
}

func TestPostgresHostingReleasePortsRequireCapabilityAndStableAllocation(t *testing.T) {
	pg, database, resource, server, _ := hostingExecutionFixture(t)
	ctx := t.Context()
	in := hostingDeploymentRequest()
	in.HostPort = 0
	if _, err := pg.RequestHostingDeployment(ctx, "hop_old_auto", resource.ID, "usr_bootstrap", in, destinationEvent("old_auto")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("old agent accepted allocated port protocol: %v", err)
	}
	if _, err := database.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	op, err := pg.RequestHostingDeployment(ctx, "hop_auto", resource.ID, "usr_bootstrap", in, destinationEvent("auto"))
	if err != nil {
		t.Fatal(err)
	}
	if op.Snapshot.HostPort < 20000 || op.Snapshot.HostPort > 60999 || op.Snapshot.PolicyVersion != domain.HostingReleasePolicyVersion {
		t.Fatalf("uncontrolled allocation: %+v", op.Snapshot)
	}
	replayed, err := pg.RequestHostingDeployment(ctx, "hop_auto_replay", resource.ID, "usr_bootstrap", in, destinationEvent("auto_replay"))
	if err != nil || replayed.ID != op.ID || replayed.Snapshot.HostPort != op.Snapshot.HostPort {
		t.Fatalf("raw auto input lost idempotency: %+v %v", replayed, err)
	}
	op = awaitHostingDispatch(t, pg, op.ID)
	if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old agent claimed new release task: %v", err)
	}
	if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	a, prepared := prepareHostingAttempt(t, pg, op, server.RunnerID)
	if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.HostingApplyBegin(ctx, server.RunnerID, a); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("admission ignored removed capability: %v", err)
	}
	if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	admitted, err := pg.HostingApplyBegin(ctx, server.RunnerID, a)
	if err != nil || admitted.Plan == nil || admitted.Plan.CompilerVersion != domain.HostingReleaseCompilerVersion {
		t.Fatalf("new protocol admission: %+v %v", admitted, err)
	}
	complete := domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "auto_result", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: strings.Repeat("c", 64), ImageID: prepared.Release.ImageID, DaemonID: prepared.Release.DaemonID, PlanHash: admitted.Plan.Hash}}
	if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.HostingComplete(ctx, server.RunnerID, complete); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("fresh result ignored removed capability: %v", err)
	}
	if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.HostingComplete(ctx, server.RunnerID, complete); err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.HostingComplete(ctx, server.RunnerID, complete); err != nil {
		t.Fatalf("committed receipt replay required fresh capability: %v", err)
	}
}

func TestPostgresHostingRetainedReleaseRequiresExactCleanupReceipt(t *testing.T) {
	pg, database, resource, server, _ := hostingExecutionFixture(t)
	ctx := t.Context()
	if _, err := database.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingReleasePortsCapability); err != nil {
		t.Fatal(err)
	}
	var first domain.HostingOperation
	for index, id := range []string{"hop_retained_first", "hop_retained_second"} {
		in := hostingDeploymentRequest()
		in.IdempotencyKey = id
		in.HostPort += index
		op, err := pg.RequestHostingDeployment(ctx, id, resource.ID, "usr_bootstrap", in, destinationEvent(id))
		if err != nil {
			t.Fatal(err)
		}
		op = awaitHostingDispatch(t, pg, op.ID)
		a, prepared := prepareHostingAttempt(t, pg, op, server.RunnerID)
		admitted, err := pg.HostingApplyBegin(ctx, server.RunnerID, a)
		if err != nil {
			t.Fatal(err)
		}
		if index == 1 && (!admitted.Plan.RetainPrevious || admitted.Plan.PreviousHealthy == nil || admitted.Plan.PreviousHealthy.OperationID != first.ID) {
			t.Fatalf("old serving release not retained: %+v", admitted.Plan)
		}
		finished, err := pg.HostingComplete(ctx, server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: id + "_result", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: strings.Repeat(string(rune('c'+index)), 64), ImageID: prepared.Release.ImageID, DaemonID: prepared.Release.DaemonID, PlanHash: admitted.Plan.Hash}})
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 {
			first = finished
		}
	}
	retained, err := pg.ListHostingRetainedReleases(ctx, resource.ID)
	if err != nil || len(retained) != 1 || retained[0].Healthy.OperationID != first.ID || !retained[0].RetainedRunning {
		t.Fatalf("retained ledger: %+v %v", retained, err)
	}
	cleanup, err := pg.RequestHostingReleaseCleanup(ctx, "hop_cleanup", resource.ID, first.ID, "usr_bootstrap", domain.HostingCleanupInput{ExpectedGeneration: 1, IdempotencyKey: "cleanup-first"}, destinationEvent("cleanup_first"))
	if err != nil {
		t.Fatal(err)
	}
	cleanup = awaitHostingDispatch(t, pg, cleanup.ID)
	claim, err := pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: cleanup.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	prepared, err := pg.HostingRunnerPlan(ctx, server.RunnerID, a)
	if err != nil || prepared.CleanupPlan == nil || prepared.TaskMode != "cleanup" {
		t.Fatalf("cleanup preparation: %+v %v", prepared, err)
	}
	admitted, err := pg.HostingApplyBegin(ctx, server.RunnerID, a)
	if err != nil || !admitted.MutationHold {
		t.Fatalf("cleanup admission: %+v %v", admitted, err)
	}
	var state string
	if err = database.QueryRow(ctx, `SELECT state FROM hosting_retained_releases WHERE operation_id=$1`, first.ID).Scan(&state); err != nil || state != "cleanup_pending" {
		t.Fatalf("target released before receipt: %s %v", state, err)
	}
	result := domain.HostingResult{Outcome: "succeeded", ContainerID: prepared.CleanupPlan.Target.ContainerID, ImageID: prepared.CleanupPlan.Target.Release.ImageID, DaemonID: prepared.CleanupPlan.Target.Release.DaemonID, PlanHash: prepared.CleanupPlan.Hash}
	wrong := result
	wrong.ContainerID = strings.Repeat("f", 64)
	if _, err = pg.HostingComplete(ctx, server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "wrong_cleanup", Result: wrong}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("wrong target receipt accepted: %v", err)
	}
	request := domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "exact_cleanup", Result: result}
	finished, err := pg.HostingComplete(ctx, server.RunnerID, request)
	if err != nil || finished.MutationHold || finished.State != "succeeded" {
		t.Fatalf("cleanup completion: %+v %v", finished, err)
	}
	if _, err = pg.HostingComplete(ctx, server.RunnerID, request); err != nil {
		t.Fatalf("cleanup replay: %v", err)
	}
	if err = database.QueryRow(ctx, `SELECT state FROM hosting_retained_releases WHERE operation_id=$1`, first.ID).Scan(&state); err != nil || state != "removed" {
		t.Fatalf("target not released after exact receipt: %s %v", state, err)
	}
}
func awaitHostingDispatch(t *testing.T, pg *store.PostgresStore, id string) domain.HostingOperation {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		op, err := pg.GetHostingOperation(t.Context(), id)
		if err != nil {
			t.Fatal(err)
		}
		if op.RunID != "" {
			return op
		}
		if op.State == "failed" {
			t.Fatal("dispatch preflight failed")
		}
		select {
		case <-deadline.C:
			t.Fatalf("operation did not dispatch: %+v", op)
		case <-ticker.C:
		}
	}
}
func prepareHostingAttempt(t *testing.T, pg *store.PostgresStore, op domain.HostingOperation, runnerID string) (domain.HostingAuthority, domain.HostingPreparedRequest) {
	t.Helper()
	claim, err := pg.ClaimRun(t.Context(), runnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = pg.HostingRunnerPlan(t.Context(), runnerID, a); err != nil {
		t.Fatal(err)
	}
	release := domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), DaemonID: "daemon_image_host", OS: "linux", Architecture: "amd64"}
	release.Hash = domain.HostingDocumentHash(release)
	request := domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "prepared_" + op.ID, Release: release}
	if _, err = pg.HostingPrepared(t.Context(), runnerID, request); err != nil {
		t.Fatal(err)
	}
	return a, request
}

func TestPostgresHostingExecutionAtomicDispatchTargetAndSettlement(t *testing.T) {
	pg, database, resource, server, _ := hostingExecutionFixture(t)
	ctx := t.Context()
	in := hostingDeploymentRequest()
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	failures := make(chan error, 8)
	for i := range 8 {
		wg.Go(func() {
			op, err := pg.RequestHostingDeployment(ctx, fmt.Sprintf("hop_concurrent_%d", i), resource.ID, "usr_bootstrap", in, destinationEvent(fmt.Sprintf("image_request_%d", i)))
			if err != nil {
				failures <- err
			} else {
				ids <- op.ID
			}
		})
	}
	wg.Wait()
	close(ids)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	id := ""
	for got := range ids {
		if id != "" && id != got {
			t.Fatal("idempotent request created multiple operations")
		}
		id = got
	}
	op := awaitHostingDispatch(t, pg, id)
	var targets, receipts, operations int
	if err := database.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_task_targets),(SELECT count(*) FROM hosting_dispatch_receipts),(SELECT count(*) FROM hosting_operations)`).Scan(&targets, &receipts, &operations); err != nil || targets != 1 || receipts != 1 || operations != 1 {
		t.Fatalf("dispatch counts %d %d %d err=%v", targets, receipts, operations, err)
	}
	if err := pg.PrepareDeployment(ctx, id); err != nil {
		t.Fatal(err)
	}
	wrong := enrollDestinationRunner(t, pg, "runner_image_wrong", []string{"compose_deploy", domain.RunTypeImageDeploy})
	if _, err := pg.ClaimRun(ctx, wrong.ID, time.Now().UTC(), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("wrong agent claimed target: %v", err)
	}
	changed := in
	changed.ContainerPort = 8080
	if _, err := pg.RequestHostingDeployment(ctx, "hop_changed", resource.ID, "usr_bootstrap", changed, destinationEvent("changed")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("idempotency body changed: %v", err)
	}
	a, prepared := prepareHostingAttempt(t, pg, op, server.RunnerID)
	if _, err := pg.HostingPrepared(ctx, server.RunnerID, prepared); err != nil {
		t.Fatalf("prepared response-loss replay: %v", err)
	}
	begun, err := pg.HostingApplyBegin(ctx, server.RunnerID, a)
	if err != nil || !begun.MutationHold {
		t.Fatalf("apply did not persist hold: %+v %v", begun, err)
	}
	if _, err = pg.UpdateRunStatus(ctx, a.RunID, domain.RunSucceeded, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("generic status bypass: %v", err)
	}
	if _, err = pg.CancelRunRequest(ctx, a.RunID, time.Now().UTC(), domain.RunLog{}, destinationEvent("generic_cancel")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("generic cancel bypass: %v", err)
	}
	result := domain.HostingResult{Outcome: "succeeded", ContainerID: "container_owned", ImageID: prepared.Release.ImageID, DaemonID: prepared.Release.DaemonID, PlanHash: begun.Plan.Hash, Ready: true}
	complete := domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "complete_" + id, Result: result}
	finished, err := pg.HostingComplete(ctx, server.RunnerID, complete)
	if err != nil || finished.State != "succeeded" || finished.MutationHold {
		t.Fatalf("settlement: %+v %v", finished, err)
	}
	if _, err = pg.HostingComplete(ctx, server.RunnerID, complete); err != nil {
		t.Fatalf("completion response loss: %v", err)
	}
	var healthy string
	if err = database.QueryRow(ctx, `SELECT healthy->>'operation_id' FROM hosting_binding_state WHERE binding_id=$1`, op.BindingID).Scan(&healthy); err != nil || healthy != id {
		t.Fatalf("healthy pointer=%s err=%v", healthy, err)
	}
}

func TestPostgresHostingExecutionExpiredAdmissionRetainsIndependentHold(t *testing.T) {
	pg, database, resource, server, _ := hostingExecutionFixture(t)
	ctx := t.Context()
	op, err := pg.RequestHostingDeployment(ctx, "hop_expiry", resource.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("image_expiry"))
	if err != nil {
		t.Fatal(err)
	}
	op = awaitHostingDispatch(t, pg, op.ID)
	a, prepared := prepareHostingAttempt(t, pg, op, server.RunnerID)
	begun, err := pg.HostingApplyBegin(ctx, server.RunnerID, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.HostingPrepared(ctx, server.RunnerID, prepared); err != nil {
		t.Fatalf("expired committed preparation replay: %v", err)
	}
	if err = pg.ExpireLeases(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	observed, err := pg.GetHostingOperation(ctx, op.ID)
	if err != nil || !observed.MutationHold || observed.State != "reconciliation_required" {
		t.Fatalf("expiry lost uncertainty: %+v %v", observed, err)
	}
	if _, err = pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("post-admission blindly requeued: %v", err)
	}
	observation := domain.HostingObservation{HostingAuthority: a, DaemonID: prepared.Release.DaemonID, BindingID: op.BindingID, ContainerID: "container_owned", ImageID: prepared.Release.ImageID, PlanHash: begun.Plan.Hash, ReleaseHash: prepared.Release.Hash, Ready: true}
	if _, err = pg.RequestHostingReconciliation(ctx, op.ID, destinationEvent("owner_reconcile")); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.HostingReconciliation(ctx, server.RunnerID, observation); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("listing alone unlocked uncertainty: %v", err)
	}
}

func TestPostgresHostingRestoreObservationModeCannotApply(t *testing.T) {
	var stop func(context.Context) error
	pg, database, resource, server, _ := hostingExecutionFixture(t, &stop)
	ctx := t.Context()
	if err := stop(ctx); err != nil {
		t.Fatal(err)
	}
	op, err := pg.RequestHostingDeployment(ctx, "hop_restored_queued", resource.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("restore_queued"))
	if err != nil {
		t.Fatal(err)
	}
	if op.RunID != "" {
		t.Fatal("stopped worker dispatched")
	}
	if _, err = database.Exec(ctx, `UPDATE hosting_binding_state SET restore_admitted=false WHERE binding_id=$1`, op.BindingID); err != nil {
		t.Fatal(err)
	}
	if err = pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	after, err := pg.GetHostingOperation(ctx, op.ID)
	if err != nil || after.RunID != "" || after.State != "queued" {
		t.Fatalf("restored job dispatched: %+v %v", after, err)
	}
	requested, err := pg.RequestHostingReconciliation(ctx, op.ID, destinationEvent("restore_owner"))
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := pg.RequestHostingReconciliation(ctx, op.ID, destinationEvent("restore_owner_duplicate"))
	if err != nil || duplicate.ReconciliationRunID != requested.ReconciliationRunID {
		t.Fatalf("reconciliation was not deduplicated: %+v %v", duplicate, err)
	}
	claim, err := pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Run.RunSpec.HostingMode != "observe" || claim.Run.ID != requested.ReconciliationRunID {
		t.Fatal("restore granted ordinary deployment task")
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	plan, err := pg.HostingRunnerPlan(ctx, server.RunnerID, a)
	if err != nil || plan.TaskMode != "observe" {
		t.Fatalf("read-only plan: %+v %v", plan, err)
	}
	if _, err = pg.HostingApplyBegin(ctx, server.RunnerID, a); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("observation applied: %v", err)
	}
	if _, err = pg.HostingPrepared(ctx, server.RunnerID, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "forged", Release: domain.HostingRelease{}}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("observation prepared: %v", err)
	}
	if _, err = pg.HostingComplete(ctx, server.RunnerID, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "forged_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true}}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("observation published deployment success: %v", err)
	}
	// Model a backup taken before a later admission: the restored database has
	// no receipt or hold, while an old external command can still be in flight.
	// A transient empty Docker listing from even the correct agent cannot prove
	// that the command will not create a container after this observation.
	_, err = pg.HostingReconciliation(ctx, server.RunnerID, domain.HostingObservation{HostingAuthority: a, BindingID: op.BindingID, DaemonID: "daemon_restored", NoOwnedContainers: true})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("restored absence proof lifted ownership hold: %v", err)
	}
	var admitted bool
	if err = database.QueryRow(ctx, `SELECT restore_admitted FROM hosting_binding_state WHERE binding_id=$1`, op.BindingID).Scan(&admitted); err != nil || admitted {
		t.Fatalf("restore admission changed: admitted=%v err=%v", admitted, err)
	}
	if err = pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	dispatched, err := pg.GetHostingOperation(ctx, op.ID)
	if err != nil || dispatched.RunID != "" || dispatched.State != "queued" {
		t.Fatalf("restored queue dispatched without trusted recovery proof: %+v %v", dispatched, err)
	}
}

func TestPostgresHostingPreAdmissionExpiryReusesCommittedRelease(t *testing.T) {
	pg, database, resource, server, _ := hostingExecutionFixture(t)
	ctx := t.Context()
	op, err := pg.RequestHostingDeployment(ctx, "hop_preexpiry", resource.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("preexpiry"))
	if err != nil {
		t.Fatal(err)
	}
	op = awaitHostingDispatch(t, pg, op.ID)
	a, prepared := prepareHostingAttempt(t, pg, op, server.RunnerID)
	if _, err = database.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err = pg.ExpireLeases(ctx, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	claim, err := pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Run.ID != a.RunID || claim.Lease.Attempt != a.Attempt+1 || claim.Lease.Fence == a.Fence {
		t.Fatal("retry changed target or reused authority")
	}
	next := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	plan, err := pg.HostingRunnerPlan(ctx, server.RunnerID, next)
	if err != nil || plan.Release == nil || *plan.Release != prepared.Release || plan.Plan == nil || plan.TaskMode != "deploy" {
		t.Fatalf("retry re-resolved release: %+v %v", plan, err)
	}
	if _, err = pg.HostingApplyBegin(ctx, server.RunnerID, a); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale attempt admitted: %v", err)
	}
	if _, err = pg.HostingApplyBegin(ctx, server.RunnerID, next); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresHostingCancellationBeforeAndAfterAdmission(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		t.Run(fmt.Sprint(admitted), func(t *testing.T) {
			pg, _, resource, server, _ := hostingExecutionFixture(t)
			ctx := t.Context()
			op, err := pg.RequestHostingDeployment(ctx, "hop_cancel", resource.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("cancel_request"))
			if err != nil {
				t.Fatal(err)
			}
			op = awaitHostingDispatch(t, pg, op.ID)
			a, _ := prepareHostingAttempt(t, pg, op, server.RunnerID)
			if admitted {
				if _, err = pg.HostingApplyBegin(ctx, server.RunnerID, a); err != nil {
					t.Fatal(err)
				}
			}
			canceled, err := pg.CancelHostingOperation(ctx, op.ID, destinationEvent("cancel_operation"))
			if err != nil {
				t.Fatal(err)
			}
			want := "canceled"
			if admitted {
				want = "cancel_requested"
			}
			if canceled.State != want || canceled.MutationHold != admitted {
				t.Fatalf("cancel erased admission boundary: %+v", canceled)
			}
			if _, err = pg.HostingApplyBegin(ctx, server.RunnerID, a); err == nil {
				t.Fatal("canceled lease admitted")
			}
			if _, err = pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("canceled task reclaimed: %v", err)
			}
		})
	}
}

func TestPostgresHostingRevokedGrantDeniesClaimAndApply(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		t.Run(fmt.Sprint(claimed), func(t *testing.T) {
			pg, _, resource, server, destination := hostingExecutionFixture(t)
			ctx := t.Context()
			op, err := pg.RequestHostingDeployment(ctx, "hop_grant", resource.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("grant_request"))
			if err != nil {
				t.Fatal(err)
			}
			op = awaitHostingDispatch(t, pg, op.ID)
			var a domain.HostingAuthority
			if claimed {
				a, _ = prepareHostingAttempt(t, pg, op, server.RunnerID)
			}
			if _, err = pg.RevokeDestinationProject(ctx, destination.ID, resource.ProjectID, destinationEvent("grant_revoke")); err != nil {
				t.Fatal(err)
			}
			if claimed {
				if _, err = pg.HostingApplyBegin(ctx, server.RunnerID, a); !errors.Is(err, store.ErrConflict) {
					t.Fatalf("revoked grant admitted apply: %v", err)
				}
			} else if _, err = pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("revoked grant claimed: %v", err)
			}
			state, err := pg.GetHostingOperation(ctx, op.ID)
			if err != nil || state.MutationHold {
				t.Fatalf("denied grant acquired hold: %+v %v", state, err)
			}
		})
	}
}

func TestPostgresHostingRecoveryFindsAcceptedOperationAfterRiverCleanup(t *testing.T) {
	var stop func(context.Context) error
	pg, database, resource, _, _ := hostingExecutionFixture(t, &stop)
	ctx := t.Context()
	if err := stop(ctx); err != nil {
		t.Fatal(err)
	}
	op, err := pg.RequestHostingDeployment(ctx, "hop_orphaned", resource.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("orphaned_request"))
	if err != nil {
		t.Fatal(err)
	}
	// Model a final failed attempt followed by retention cleanup, while the
	// independently durable accepted operation still has no dispatch receipt.
	if _, err = database.Exec(ctx, `DELETE FROM river_job`); err != nil {
		t.Fatal(err)
	}
	restartedStop, err := pg.StartHostingExecution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := restartedStop(stopCtx); err != nil {
			t.Error(err)
		}
	}()
	op = awaitHostingDispatch(t, pg, op.ID)
	if err = pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	var receipts int
	if err = database.QueryRow(ctx, `SELECT count(*) FROM hosting_dispatch_receipts WHERE operation_id=$1`, op.ID).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("recovery duplicated dispatch: %d %v", receipts, err)
	}
}

func TestPostgresHostingRestoreHoldBlocksBindingBeforeFirstOperation(t *testing.T) {
	pg, database, resource, _, _ := hostingExecutionFixture(t)
	ctx := t.Context()
	if _, err := database.Exec(ctx, `INSERT INTO hosting_binding_restore_holds(binding_id) SELECT id FROM resource_bindings`); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.RequestHostingDeployment(ctx, "hop_postbackup", resource.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("postbackup")); !errors.Is(err, store.ErrHostingRestoreHold) {
		t.Fatalf("restored binding admitted first operation: %v", err)
	}
	var operations, states int
	if err := database.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_operations),(SELECT count(*) FROM hosting_binding_state)`).Scan(&operations, &states); err != nil || operations != 0 || states != 0 {
		t.Fatalf("denied restore created intent: operations=%d states=%d err=%v", operations, states, err)
	}
	if _, err := database.Exec(ctx, `DELETE FROM hosting_binding_restore_holds`); err == nil {
		t.Fatal("restore holds can be silently removed")
	}
}

func TestPostgresHostingRestoreHoldBlocksExistingClaimAndApply(t *testing.T) {
	for _, claimed := range []bool{false, true} {
		t.Run(fmt.Sprint(claimed), func(t *testing.T) {
			pg, database, resource, server, _ := hostingExecutionFixture(t)
			ctx := t.Context()
			op, err := pg.RequestHostingDeployment(ctx, "hop_restore_existing", resource.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("restore_existing"))
			if err != nil {
				t.Fatal(err)
			}
			op = awaitHostingDispatch(t, pg, op.ID)
			var a domain.HostingAuthority
			if claimed {
				a, _ = prepareHostingAttempt(t, pg, op, server.RunnerID)
			}
			if _, err = database.Exec(ctx, `INSERT INTO hosting_binding_restore_holds(binding_id) SELECT id FROM resource_bindings`); err != nil {
				t.Fatal(err)
			}
			if claimed {
				if _, err = pg.HostingApplyBegin(ctx, server.RunnerID, a); !errors.Is(err, store.ErrHostingRestoreHold) {
					t.Fatalf("restored admission applied: %v", err)
				}
			} else if _, err = pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute); !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("restored target claimed: %v", err)
			}
			status, err := pg.GetHostingOperation(ctx, op.ID)
			if err != nil || !status.RestoreOwnershipHeld || status.MutationHold {
				t.Fatalf("public restore status: %+v %v", status, err)
			}
		})
	}
}

func TestPostgresHostingNonRestoredNeverDispatchedObservationRetainsQueuedIntent(t *testing.T) {
	var stop func(context.Context) error
	pg, _, resource, server, _ := hostingExecutionFixture(t, &stop)
	ctx := t.Context()
	if err := stop(ctx); err != nil {
		t.Fatal(err)
	}
	op, err := pg.RequestHostingDeployment(ctx, "hop_never_dispatched", resource.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("never_dispatched"))
	if err != nil {
		t.Fatal(err)
	}
	if op.RunID != "" {
		t.Fatal("stopped consumer dispatched")
	}
	if _, err = pg.RequestHostingReconciliation(ctx, op.ID, destinationEvent("observe_normal")); err != nil {
		t.Fatal(err)
	}
	claim, err := pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	observed, err := pg.HostingReconciliation(ctx, server.RunnerID, domain.HostingObservation{HostingAuthority: a, BindingID: op.BindingID, DaemonID: "daemon_unrestored", NoOwnedContainers: true})
	if err != nil || observed.State != "queued" || observed.RunID != "" || observed.Release != nil || observed.MutationHold || observed.RestoreOwnershipHeld {
		t.Fatalf("normal observation fabricated or blocked deployment: %+v %v", observed, err)
	}
	if err = pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	after, err := pg.GetHostingOperation(ctx, op.ID)
	if err != nil || after.State != "dispatched" || after.RunID == "" {
		t.Fatalf("normal queued operation did not dispatch: %+v %v", after, err)
	}
}

func TestPostgresHostingAuditFailureRollsBackIntentAndSettlement(t *testing.T) {
	pg, database, resource, server, _ := hostingExecutionFixture(t)
	ctx := t.Context()
	// Reuse an existing audit ID to force failure after operation/snapshot writes.
	event := destinationEvent("image_binding")
	if _, err := pg.RequestHostingDeployment(ctx, "hop_audit_rejected", resource.ID, "usr_bootstrap", hostingDeploymentRequest(), event); err == nil {
		t.Fatal("duplicate audit accepted")
	}
	var operations, snapshots, jobs int
	if err := database.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_operations),(SELECT count(*) FROM hosting_snapshots),(SELECT count(*) FROM river_job)`).Scan(&operations, &snapshots, &jobs); err != nil || operations != 0 || snapshots != 0 || jobs != 0 {
		t.Fatalf("failed audit leaked accepted intent: %d %d %d err=%v", operations, snapshots, jobs, err)
	}
	requestAudit := destinationEvent("audit_atomic")
	requestAudit.Metadata = map[string]any{"project_id": "proj_security", "resource_id": "forged_resource", "binding_id": "forged_binding"}
	op, err := pg.RequestHostingDeployment(ctx, "hop_audit_atomic", resource.ID, "usr_bootstrap", hostingDeploymentRequest(), requestAudit)
	if err != nil {
		t.Fatal(err)
	}
	op = awaitHostingDispatch(t, pg, op.ID)
	a, prepared := prepareHostingAttempt(t, pg, op, server.RunnerID)
	admitted, err := pg.HostingApplyBegin(ctx, server.RunnerID, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, `CREATE FUNCTION reject_hosting_completion_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.completed' THEN RAISE EXCEPTION 'injected audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_hosting_completion_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_hosting_completion_audit()`); err != nil {
		t.Fatal(err)
	}
	complete := domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "atomic_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: "container_atomic", ImageID: prepared.Release.ImageID, DaemonID: prepared.Release.DaemonID, PlanHash: admitted.Plan.Hash}}
	if _, err = pg.HostingComplete(ctx, server.RunnerID, complete); err == nil {
		t.Fatal("settlement ignored audit failure")
	}
	current, err := pg.GetHostingOperation(ctx, op.ID)
	if err != nil || current.State != "applying" || !current.MutationHold || current.Result != nil || current.CompleteReceipt != nil {
		t.Fatalf("failed audit partially settled: %+v %v", current, err)
	}
	var live, healthy bool
	if err = database.QueryRow(ctx, `SELECT (SELECT status='active' FROM run_leases WHERE id=$1),(SELECT healthy IS NOT NULL FROM hosting_binding_state WHERE binding_id=$2)`, a.LeaseID, op.BindingID).Scan(&live, &healthy); err != nil || !live || healthy {
		t.Fatalf("audit rollback changed authority/healthy: live=%v healthy=%v err=%v", live, healthy, err)
	}
	if _, err = database.Exec(ctx, `DROP TRIGGER reject_hosting_completion_audit ON audit_events; DROP FUNCTION reject_hosting_completion_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.HostingComplete(ctx, server.RunnerID, complete); err != nil {
		t.Fatalf("receipt could not commit after rollback: %v", err)
	}
	page, err := pg.ListAuditEventsPageForScope(ctx, store.AuditAuthorizationScope{ActorID: "usr_viewer", ProjectIDs: []string{resource.ProjectID}}, store.Page{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, event := range page.Items {
		if event.TargetID == op.ID {
			actions[event.Action] = true
			if event.Metadata["project_id"] != resource.ProjectID || event.Metadata["resource_id"] != resource.ID || event.Metadata["binding_id"] != op.BindingID {
				t.Fatal("audit target metadata was not derived from operation")
			}
		}
	}
	for _, action := range []string{"hosting.deployment_requested", "hosting.dispatched", "hosting.prepared", "hosting.apply_admitted", "hosting.completed"} {
		if !actions[action] {
			t.Fatalf("project viewer cannot see %s", action)
		}
	}
}

func TestPostgresHostingFirstCompletionRequiresLiveUncanceledAuthority(t *testing.T) {
	for _, stale := range []string{"expired", "canceled"} {
		for _, outcome := range []string{"succeeded", "failed"} {
			t.Run(stale+"/"+outcome, func(t *testing.T) {
				pg, database, resource, server, _ := hostingExecutionFixture(t)
				ctx := t.Context()
				op, err := pg.RequestHostingDeployment(ctx, "hop_stale_complete", resource.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("stale_complete"))
				if err != nil {
					t.Fatal(err)
				}
				op = awaitHostingDispatch(t, pg, op.ID)
				a, prepared := prepareHostingAttempt(t, pg, op, server.RunnerID)
				admitted, err := pg.HostingApplyBegin(ctx, server.RunnerID, a)
				if err != nil {
					t.Fatal(err)
				}
				if stale == "expired" {
					if _, err = database.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
						t.Fatal(err)
					}
					if err = pg.ExpireLeases(ctx, time.Now().UTC()); err != nil {
						t.Fatal(err)
					}
				} else if _, err = pg.CancelHostingOperation(ctx, op.ID, destinationEvent("stale_cancel")); err != nil {
					t.Fatal(err)
				}
				before, err := pg.GetHostingOperation(ctx, op.ID)
				if err != nil {
					t.Fatal(err)
				}
				type state struct {
					lease, task      string
					audits, receipts int
					healthy          bool
				}
				readState := func() state {
					t.Helper()
					var value state
					if err := database.QueryRow(ctx, `SELECT (SELECT status FROM run_leases WHERE id=$1),(SELECT status FROM task_runs WHERE id=$2),(SELECT count(*) FROM audit_events),(SELECT count(*) FROM hosting_receipts WHERE operation_id=$3 AND phase='complete'),(SELECT healthy IS NOT NULL FROM hosting_binding_state WHERE binding_id=$4)`, a.LeaseID, a.RunID, op.ID, op.BindingID).Scan(&value.lease, &value.task, &value.audits, &value.receipts, &value.healthy); err != nil {
						t.Fatal(err)
					}
					return value
				}
				prior := readState()
				request := domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "first_stale_complete", Result: domain.HostingResult{Outcome: outcome, Ready: outcome == "succeeded", Restored: outcome == "failed", ContainerID: "container_stale", ImageID: prepared.Release.ImageID, DaemonID: prepared.Release.DaemonID, PlanHash: admitted.Plan.Hash}}
				if _, err = pg.HostingComplete(ctx, server.RunnerID, request); !errors.Is(err, store.ErrNotFound) {
					t.Fatalf("fresh stale result accepted: %v", err)
				}
				after, err := pg.GetHostingOperation(ctx, op.ID)
				if err != nil || !reflect.DeepEqual(before, after) || readState() != prior || !after.MutationHold || after.Result != nil || after.CompleteReceipt != nil {
					t.Fatalf("stale completion mutated durable authority/evidence: before=%+v after=%+v err=%v", before, after, err)
				}
			})
		}
	}
}

func TestPostgresHostingCommittedCompletionReplaysAfterAuthorityEnds(t *testing.T) {
	pg, database, resource, server, _ := hostingExecutionFixture(t)
	ctx := t.Context()
	op, err := pg.RequestHostingDeployment(ctx, "hop_committed_replay", resource.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("committed_replay"))
	if err != nil {
		t.Fatal(err)
	}
	op = awaitHostingDispatch(t, pg, op.ID)
	a, prepared := prepareHostingAttempt(t, pg, op, server.RunnerID)
	admitted, err := pg.HostingApplyBegin(ctx, server.RunnerID, a)
	if err != nil {
		t.Fatal(err)
	}
	request := domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "committed_replay", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: "container_committed", ImageID: prepared.Release.ImageID, DaemonID: prepared.Release.DaemonID, PlanHash: admitted.Plan.Hash}}
	committed, err := pg.HostingComplete(ctx, server.RunnerID, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, a.LeaseID); err != nil {
		t.Fatal(err)
	}
	var before int
	if err = database.QueryRow(ctx, `SELECT count(*) FROM audit_events`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	replayed, err := pg.HostingComplete(ctx, server.RunnerID, request)
	if err != nil || !reflect.DeepEqual(committed, replayed) {
		t.Fatalf("committed replay failed: %+v %v", replayed, err)
	}
	var after int
	if err = database.QueryRow(ctx, `SELECT count(*) FROM audit_events`).Scan(&after); err != nil || before != after {
		t.Fatalf("replay created mutation: %d->%d %v", before, after, err)
	}
	request.ReceiptID = "new_receipt_after_expiry"
	if _, err = pg.HostingComplete(ctx, server.RunnerID, request); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("new receipt replaced committed evidence: %v", err)
	}
}

func TestPostgresClaimRejectsPersistedInternalWorkflowWithoutTarget(t *testing.T) {
	pg, database, _, server, _ := hostingExecutionFixture(t)
	ctx := t.Context()
	image := domain.RunSpec{Type: domain.RunTypeImageDeploy}
	nested := &domain.Workflow{Steps: []domain.WorkflowStep{{ID: "internal", RunSpec: image}}}
	for i, run := range []domain.TaskRun{
		{RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy}, Workflow: *nested},
		{RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy, Workflow: nested}},
		{RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{{ID: "wrapper", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy, Workflow: nested}}}}},
		{RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy, HostingMode: "deploy"}},
		{ID: "run_hop_no_target", RunSpec: domain.RunSpec{Type: domain.RunTypeImageDeploy, HostingOperationID: "hop_no_target", HostingMode: "deploy"}},
	} {
		if run.ID == "" {
			run.ID = fmt.Sprintf("run_persisted_internal_%d", i)
		}
		run.ProjectID = "proj_platform"
		run.RequestedBy = "usr_bootstrap"
		run.Status = domain.RunQueued
		run.StartedAt = time.Now().UTC()
		if _, err := pg.CreateRun(ctx, run); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("generic creation accepted internal workflow: %v", err)
		}
		spec, _ := json.Marshal(run.RunSpec)
		workflow, _ := json.Marshal(run.Workflow)
		// Simulate a previously persisted malformed row. There is no command or
		// execution demonstration, only a claim admission assertion.
		if _, err := database.Exec(ctx, `INSERT INTO task_runs(id,project_id,run_spec,workflow,workflow_state,runner_tags,status,requested_by,started_at,created_at,claim_order_at) VALUES($1,'proj_platform',$2,$3,'{}','{}','queued','usr_bootstrap',clock_timestamp(),clock_timestamp(),clock_timestamp())`, run.ID, spec, workflow); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pg.ClaimRun(ctx, server.RunnerID, time.Now().UTC(), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("persisted internal workflow acquired authority: %v", err)
	}
	var leases int
	if err := database.QueryRow(ctx, `SELECT count(*) FROM run_leases`).Scan(&leases); err != nil || leases != 0 {
		t.Fatalf("rejected internal tasks created %d leases: %v", leases, err)
	}
}

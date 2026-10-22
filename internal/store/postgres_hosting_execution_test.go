package store_test

import (
	"context"
	"errors"
	"fmt"
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
	runner := enrollDestinationRunner(t, pg, "runner_image_host", []string{"compose_deploy", domain.RunTypeImageDeploy})
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
	observed, err := pg.HostingReconciliation(ctx, server.RunnerID, domain.HostingObservation{HostingAuthority: a, BindingID: op.BindingID, DaemonID: "daemon_restored", NoOwnedContainers: true})
	if err != nil {
		t.Fatal(err)
	}
	if observed.State != "queued" || observed.Release != nil || observed.RunID != "" || observed.MutationHold {
		t.Fatalf("observation fabricated deployment: %+v", observed)
	}
	if err = pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	dispatched, err := pg.GetHostingOperation(ctx, op.ID)
	if err != nil || dispatched.RunID == "" || dispatched.State != "dispatched" {
		t.Fatalf("reconciled queue did not dispatch: %+v %v", dispatched, err)
	}
}

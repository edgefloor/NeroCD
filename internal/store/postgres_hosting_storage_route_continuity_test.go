package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

// cleanupContinuityTransaction bounds cleanup even after the test context is canceled.
func cleanupContinuityTransaction(t *testing.T, tx pgx.Tx) func() {
	t.Helper()
	cleanup := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Errorf("rollback continuity fixture: %v", err)
		}
	}
	t.Cleanup(cleanup)
	return cleanup
}

type storageContinuityFixture struct {
	storageMaterializationFixture
	loaded domain.HostingProxyOperation
	route  domain.HostingRoute
}

func newStorageContinuityFixture(t *testing.T, customize ...func(*domain.HostingProxyResult)) storageContinuityFixture {
	t.Helper()
	f := newStorageMaterializationFixture(t)
	ack, err := f.pg.HostingStorageCreateBegin(t.Context(), f.runner, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingStorageMaterialized(t.Context(), f.runner, f.proof(ack)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingApplyBegin(t.Context(), f.runner, f.authority); err != nil {
		t.Fatal(err)
	}
	f.op = f.complete(t, "succeeded", false)
	if _, err = f.db.Exec(t.Context(), `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.RunTypeProxyApply); err != nil {
		t.Fatal(err)
	}
	in := domain.HostingProxyInput{ControlProjectID: f.resource.ProjectID, Configuration: domain.HostingProxyConfiguration{Email: "operator@nerocd.invalid", CA: "letsencrypt-staging", ManagementPort: 19080}, IdempotencyKey: "continuity-proxy"}
	if _, err = f.pg.ConfigureHostingProxy(t.Context(), "proxy_continuity", f.op.Snapshot.ServerID, "usr_bootstrap", in, destinationEvent("continuity_configure")); err != nil {
		t.Fatal(err)
	}
	var first string
	if err = f.db.QueryRow(t.Context(), `SELECT id FROM hosting_proxy_operations WHERE proxy_id='proxy_continuity' AND generation=1`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	settleProxyGeneration(t, f.pg, first, f.runner, f.op.Release.DaemonID)
	if _, err = f.pg.GrantHostingHostname(t.Context(), "proxy_continuity", "grant_continuity", "usr_bootstrap", domain.HostingHostnameGrantInput{ProjectID: f.resource.ProjectID, Hostname: "apps.nerocd.invalid", Wildcard: true}, destinationEvent("continuity_grant")); err != nil {
		t.Fatal(err)
	}
	route, err := f.pg.RequestHostingRoute(t.Context(), f.resource.ID, "", "pop_continuity_route", "usr_bootstrap", domain.HostingRouteInput{IdempotencyKey: "continuity-route", Hostname: "uploads.apps.nerocd.invalid", PathPrefix: "/", ExpectedHealthyOperationID: f.op.ID}, destinationEvent("continuity_route"))
	if err != nil {
		t.Fatal(err)
	}
	loaded := settleStorageContinuityGeneration(t, f, route.OperationID, customize...)
	return storageContinuityFixture{f, loaded, route}
}

func settleStorageContinuityGeneration(t *testing.T, f storageMaterializationFixture, id string, customize ...func(*domain.HostingProxyResult)) domain.HostingProxyOperation {
	t.Helper()
	a, op, preparation := admitSwitchOperation(t, switchControl{pg: f.pg, server: domain.Server{RunnerID: f.runner}, a: f.op}, id)
	result := trustedProxyResult(op, preparation, "applied")
	for _, change := range customize {
		change(&result)
	}
	finished, err := f.pg.RunnerProxyComplete(t.Context(), f.runner, domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "trusted_" + id, Result: result})
	if err != nil {
		t.Fatal(err)
	}
	return finished
}

func (f storageContinuityFixture) input(key string) domain.HostingDeploymentInput {
	in := hostingDeploymentRequest()
	in.IdempotencyKey = key
	in.HostPort = f.op.Snapshot.HostPort
	in.StorageRouteContinuity = &domain.HostingStorageRouteContinuityInput{ExpectedLoadedGeneration: f.loaded.Generation, ExpectedLoadedProxyOperationID: f.loaded.ID, Routes: []domain.HostingRouteSwitchSelection{{RouteID: f.route.ID, ExpectedVersion: f.route.Version, ExpectedSelectedReleaseOperationID: f.op.ID}}}
	return in
}

func (f storageContinuityFixture) successor(t *testing.T) storageMaterializationFixture {
	t.Helper()
	op, err := f.pg.RequestHostingDeployment(t.Context(), "hop_continuity_next", f.resource.ID, "usr_bootstrap", f.input("continuity-next"), destinationEvent("continuity_next"))
	if err != nil {
		t.Fatal(err)
	}
	op = awaitHostingDispatch(t, f.pg, op.ID)
	a, prepared := prepareHostingAttempt(t, f.pg, op, f.runner)
	op, err = f.pg.GetHostingOperation(t.Context(), op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Release.DaemonID != f.op.Release.DaemonID || op.Plan.Snapshot.StorageRouteContinuity.Hash == "" {
		t.Fatal("successor not bound to loaded daemon and reservation")
	}
	return storageMaterializationFixture{f.pg, f.db, f.resource, f.runner, f.storage, op, a}
}

func continuityState(t *testing.T, f storageContinuityFixture, id, want string) string {
	t.Helper()
	var state string
	var successor *string
	if err := f.db.QueryRow(t.Context(), `SELECT state,successor_proxy_operation_id FROM hosting_storage_route_continuities WHERE operation_id=$1`, id).Scan(&state, &successor); err != nil {
		t.Fatal(err)
	}
	if state != want {
		t.Fatalf("continuity state=%s want=%s", state, want)
	}
	if successor != nil {
		return *successor
	}
	return ""
}

func admitContinuityWriter(t *testing.T, f storageMaterializationFixture) {
	t.Helper()
	ack, err := f.pg.HostingStorageCreateBegin(t.Context(), f.runner, f.request())
	if err != nil {
		t.Fatal(err)
	}
	if ack.CreatePermitted {
		t.Fatal("replacement reauthorized volume creation")
	}
	if _, err = f.pg.HostingStorageMaterialized(t.Context(), f.runner, f.proof(ack)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingApplyBegin(t.Context(), f.runner, f.authority); err != nil {
		t.Fatal(err)
	}
}

func TestHostingStorageRouteContinuityPostgresSuccessReplayAndProxyReceipt(t *testing.T) {
	f := newStorageContinuityFixture(t)
	next := f.successor(t)
	// Request replay is exact even while its own reservation holds the proxy.
	replay, err := f.pg.RequestHostingDeployment(t.Context(), "hop_ignored", f.resource.ID, "usr_bootstrap", f.input("continuity-next"), destinationEvent("continuity_replay"))
	if err != nil || replay.ID != next.op.ID {
		t.Fatalf("request replay %+v %v", replay, err)
	}
	changed := f.input("continuity-next")
	changed.StorageRouteContinuity.ExpectedLoadedGeneration++
	if _, err = f.pg.RequestHostingDeployment(t.Context(), "hop_changed", f.resource.ID, "usr_bootstrap", changed, destinationEvent("continuity_changed")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("changed descriptor replay: %v", err)
	}
	admitContinuityWriter(t, next)
	finished := next.complete(t, "succeeded", false)
	if finished.State != "succeeded" || finished.MutationHold || finished.CompleteReceipt == nil {
		t.Fatalf("runtime result lost %+v", finished)
	}
	proxyID := continuityState(t, f, next.op.ID, "proxy_pending")
	var loaded int64
	if err = f.db.QueryRow(t.Context(), `SELECT loaded_generation FROM hosting_proxies WHERE id='proxy_continuity'`).Scan(&loaded); err != nil || loaded != f.loaded.Generation {
		t.Fatalf("runtime forged loaded receipt: %d %v", loaded, err)
	}
	if _, err = f.pg.RequestHostingRoute(t.Context(), f.resource.ID, f.route.ID, "pop_forbidden_supersede", "usr_bootstrap", domain.HostingRouteInput{ExpectedRouteVersion: f.route.Version + 1, ExpectedHealthyOperationID: next.op.ID, Hostname: f.route.Hostname, PathPrefix: f.route.PathPrefix, IdempotencyKey: "forbidden-supersede"}, destinationEvent("forbidden_supersede")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("pending generation superseded: %v", err)
	}
	settleProxyGeneration(t, f.pg, proxyID, f.runner, f.op.Release.DaemonID)
	continuityState(t, f, next.op.ID, "loaded")
	if _, err = f.pg.HostingComplete(t.Context(), next.runner, domain.HostingCompleteRequest{HostingAuthority: next.authority, ReceiptID: "complete_" + next.op.ID, Result: *finished.Result}); err != nil {
		t.Fatalf("completion replay %v", err)
	}
	route, err := f.pg.GetHostingRoute(t.Context(), f.route.ID)
	if err != nil || route.Route.SelectedReleaseOperationID != next.op.ID || route.Route.Version != f.route.Version+1 || !route.Route.HTTPReady || route.Route.Reason != "" {
		t.Fatalf("route not moved once %+v %v", route, err)
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE hosting_storage_route_continuities SET descriptor=descriptor||'{"host_port":12345}'::jsonb WHERE operation_id=$1`, next.op.ID); err == nil {
		t.Fatal("immutable reservation changed")
	}
}

func TestHostingStorageRouteContinuityPostgresAdmissionRechecksAndAtomicity(t *testing.T) {
	for _, change := range []string{"route_version", "grant", "proxy_generation", "foreign_reference", "audit", "partial_schema"} {
		t.Run(change, func(t *testing.T) {
			f := newStorageContinuityFixture(t)
			next := f.successor(t)
			switch change {
			case "partial_schema":
				if _, err := f.db.Exec(t.Context(), `ALTER TABLE hosting_storage_route_continuities DISABLE TRIGGER hosting_storage_route_continuity_immutable`); err != nil {
					t.Fatal(err)
				}
			case "route_version":
				_, err := f.db.Exec(t.Context(), `UPDATE hosting_proxy_routes SET version=version+1 WHERE id=$1`, f.route.ID)
				if err != nil {
					t.Fatal(err)
				}
			case "grant":
				_, err := f.db.Exec(t.Context(), `UPDATE hosting_proxy_hostname_grants SET revoked_at=clock_timestamp() WHERE id='grant_continuity'`)
				if err != nil {
					t.Fatal(err)
				}
			case "proxy_generation":
				_, err := f.db.Exec(t.Context(), `UPDATE hosting_proxies SET loaded_generation=loaded_generation+1 WHERE id='proxy_continuity'`)
				if err != nil {
					t.Fatal(err)
				}
			case "foreign_reference":
				_, err := f.db.Exec(t.Context(), `INSERT INTO hosting_proxy_routes(id,proxy_id,project_id,resource_id,binding_id,grant_id,hostname,path_prefix,version,selected_release_operation_id,enabled,created_by) SELECT 'foreign_route',proxy_id,project_id,resource_id,binding_id,grant_id,'foreign.apps.nerocd.invalid',path_prefix,1,selected_release_operation_id,false,created_by FROM hosting_proxy_routes WHERE id=$1`, f.route.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.Exec(t.Context(), `INSERT INTO hosting_proxy_release_references(operation_id,route_id,release_operation_id) VALUES($1,'foreign_route',$2)`, f.loaded.ID, f.op.ID); err != nil {
					t.Fatal(err)
				}
			case "audit":
				_, err := f.db.Exec(t.Context(), `CREATE FUNCTION reject_continuity_audit() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN IF NEW.action=''hosting.storage.create_admitted'' THEN RAISE EXCEPTION ''fixture audit rejection''; END IF; RETURN NEW; END'; CREATE TRIGGER reject_continuity_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_continuity_audit()`)
				if err != nil {
					t.Fatal(err)
				}
			}
			if ack, err := f.pg.HostingStorageCreateBegin(t.Context(), f.runner, next.request()); err == nil || ack.CreatePermitted {
				t.Fatalf("drift admitted storage side effects %+v %v", ack, err)
			}
			var admission, holds int
			if err := f.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM hosting_storage_operation_admissions WHERE operation_id=$1),(SELECT count(*) FROM hosting_binding_state WHERE hold_operation_id=$1)`, next.op.ID).Scan(&admission, &holds); err != nil || admission != 0 || holds != 0 {
				t.Fatalf("partial admission %d %d %v", admission, holds, err)
			}
		})
	}
}

func TestHostingStorageRouteContinuityPostgresRuntimeSuccessMetadataConflict(t *testing.T) {
	for _, change := range []string{"route_version", "queue_audit"} {
		t.Run(change, func(t *testing.T) {
			f := newStorageContinuityFixture(t)
			next := f.successor(t)
			admitContinuityWriter(t, next)
			if change == "route_version" {
				if _, err := f.db.Exec(t.Context(), `UPDATE hosting_proxy_routes SET version=version+1 WHERE id=$1`, f.route.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.db.Exec(t.Context(), `CREATE FUNCTION reject_continuity_queue() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN IF NEW.action=''hosting.proxy_generation_requested'' THEN RAISE EXCEPTION ''fixture queue audit rejection''; END IF; RETURN NEW; END'; CREATE TRIGGER reject_continuity_queue BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_continuity_queue()`); err != nil {
					t.Fatal(err)
				}
			}
			finished := next.complete(t, "succeeded", false)
			if finished.CompleteReceipt == nil || finished.Result == nil || finished.Result.Outcome != "succeeded" {
				t.Fatalf("external success rolled back %+v", finished)
			}
			continuityState(t, f, next.op.ID, "unresolved")
			route, routeErr := f.pg.GetHostingRoute(t.Context(), f.route.ID)
			if routeErr != nil || route.Route.State != "uncertain" || route.Route.HTTPReady || route.Route.HTTPSReady || route.Route.Reason != "storage replacement requires route reconciliation" {
				t.Fatalf("historical readiness claimed stopped predecessor %+v %v", route, routeErr)
			}
			var body []byte
			var healthy domain.HostingHealthy
			if err := f.db.QueryRow(t.Context(), `SELECT healthy FROM hosting_binding_state WHERE binding_id=$1`, next.op.BindingID).Scan(&body); err != nil || json.Unmarshal(body, &healthy) != nil || healthy.OperationID != next.op.ID {
				t.Fatalf("healthy claims stopped predecessor %+v %v", healthy, err)
			}
			var pending int
			if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM hosting_proxy_operations WHERE id=$1`, "pop_storage_"+next.op.ID).Scan(&pending); err != nil || pending != 0 {
				t.Fatalf("savepoint leaked generation %d %v", pending, err)
			}
		})
	}
}

func TestHostingStorageRouteContinuityPostgresConcurrentRequestAndCancellation(t *testing.T) {
	f := newStorageContinuityFixture(t)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for n := range 2 {
		wg.Go(func() {
			_, err := f.pg.RequestHostingDeployment(t.Context(), fmt.Sprintf("hop_compete_%d", n), f.resource.ID, "usr_bootstrap", f.input(fmt.Sprintf("compete-%d", n)), destinationEvent(fmt.Sprintf("compete_%d", n)))
			results <- err
		})
	}
	wg.Wait()
	close(results)
	ok, conflicts := 0, 0
	for err := range results {
		if err == nil {
			ok++
		} else if errors.Is(err, store.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if ok != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflict=%d", ok, conflicts)
	}
	var id string
	if err := f.db.QueryRow(t.Context(), `SELECT operation_id FROM hosting_storage_route_continuities WHERE state='reserved'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	awaitHostingDispatch(t, f.pg, id)
	if _, err := f.pg.CancelHostingOperation(t.Context(), id, destinationEvent("continuity_cancel")); err != nil {
		t.Fatal(err)
	}
	continuityState(t, f, id, "released")
	route, err := f.pg.GetHostingRoute(t.Context(), f.route.ID)
	if err != nil || !route.Route.HTTPReady || route.Route.Reason != "" {
		t.Fatalf("unmutated cancellation lost prior readiness %+v %v", route, err)
	}
}

func TestHostingStorageRouteContinuityPostgresLockOrderBeforeResource(t *testing.T) {
	f := newStorageContinuityFixture(t)
	next := f.successor(t)
	// A blocked destination must not leave the individual resource locked.
	tx, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	cleanupContinuityTransaction(t, tx)
	if _, err = tx.Exec(t.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:'||$1::text,0))`, f.op.Snapshot.DestinationID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.pg.HostingStorageCreateBegin(ctx, f.runner, next.request()); done <- err }()
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("admission goroutine did not exit")
			}
		}
	})
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	// Query PostgreSQL's waiter state instead of relying on a scheduling sleep.
	waitCtx, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	for {
		var waiting bool
		if err = f.db.QueryRow(waitCtx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%LockHostingExecutionDestination%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-waitCtx.Done():
			t.Fatal("no destination waiter")
		case <-ticker.C:
		}
	}
	probe, err := f.db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rollbackProbe := cleanupContinuityTransaction(t, probe)
	_, probeErr := probe.Exec(t.Context(), `SELECT id FROM resources WHERE id=$1 FOR UPDATE NOWAIT`, f.resource.ID)
	rollbackProbe()
	cancel()
	var callErr error
	select {
	case callErr = <-done:
		joined = true
	case <-time.After(5 * time.Second):
		t.Fatal("admission goroutine did not exit")
	}
	if probeErr != nil || callErr == nil {
		t.Fatalf("resource locked before destination or admission escaped wait: probe=%v call=%v", probeErr, callErr)
	}
}

func TestHostingStorageRouteContinuityPostgresPreservesUnrelatedRoutes(t *testing.T) {
	f := newStorageContinuityFixture(t)
	other := createDestinationResource(t, f.pg, "continuity_other", f.resource.ProjectID)
	if _, err := f.pg.BindResourceDestination(t.Context(), store.ResourceBindingInput{ID: "binding_continuity_other", ResourceID: other.ID, DestinationID: f.op.Snapshot.DestinationID, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("continuity_other_binding")); err != nil {
		t.Fatal(err)
	}
	healthy := settleProxyImage(t, f.pg, other, domain.Server{ID: f.op.Snapshot.ServerID, RunnerID: f.runner}, "hop_continuity_other", 18081)
	route, err := f.pg.RequestHostingRoute(t.Context(), other.ID, "", "pop_continuity_other", "usr_bootstrap", domain.HostingRouteInput{IdempotencyKey: "continuity-other-route", Hostname: "other.apps.nerocd.invalid", PathPrefix: "/", ExpectedHealthyOperationID: healthy.ID}, destinationEvent("continuity_other_route"))
	if err != nil {
		t.Fatal(err)
	}
	f.loaded = settleStorageContinuityGeneration(t, f.storageMaterializationFixture, route.OperationID)
	next := f.successor(t)
	admitContinuityWriter(t, next)
	next.complete(t, "succeeded", false)
	proxyID := continuityState(t, f, next.op.ID, "proxy_pending")
	queued, err := f.pg.GetHostingProxyOperation(t.Context(), proxyID)
	if err != nil || len(queued.Snapshot.Routes) != 2 {
		t.Fatalf("full scope lost %+v %v", queued, err)
	}
	for _, target := range queued.Snapshot.Routes {
		if target.RouteID == route.ID && target.ReleaseOperationID != healthy.ID {
			t.Fatal("unrelated release changed")
		}
	}
	settleProxyGeneration(t, f.pg, proxyID, f.runner, f.op.Release.DaemonID)
	observed, err := f.pg.GetHostingRoute(t.Context(), route.ID)
	if err != nil || observed.Route.Version != route.Version || observed.Route.SelectedReleaseOperationID != healthy.ID || !observed.Route.HTTPReady || observed.Route.Reason != "" {
		t.Fatalf("unrelated route mutated %+v %v", observed, err)
	}
}

func TestHostingStorageRouteContinuityPostgresProxyFailureRetainsReservation(t *testing.T) {
	f := newStorageContinuityFixture(t)
	next := f.successor(t)
	admitContinuityWriter(t, next)
	finished := next.complete(t, "succeeded", false)
	proxyID := continuityState(t, f, next.op.ID, "proxy_pending")
	a, proxy, preparation := admitSwitchOperation(t, switchControl{pg: f.pg, server: domain.Server{RunnerID: f.runner}, a: f.op}, proxyID)
	result := domain.HostingProxyResult{Outcome: "refused", PlanHash: proxy.Plan.Hash, EngineHash: preparation.EngineHash, DaemonID: preparation.DaemonID, LoadedGeneration: proxy.Plan.PreviousGeneration}
	if _, err := f.pg.RunnerProxyComplete(t.Context(), f.runner, domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "continuity_proxy_failed", Result: result}); err != nil {
		t.Fatal(err)
	}
	continuityState(t, f, next.op.ID, "proxy_failed")
	route, err := f.pg.GetHostingRoute(t.Context(), f.route.ID)
	if err != nil || route.Route.HTTPReady || route.Route.HTTPSReady || route.Route.State != "uncertain" || route.Route.Reason != "storage replacement requires route reconciliation" {
		t.Fatalf("failed proxy readiness %+v %v", route, err)
	}
	if finished.Result.Outcome != "succeeded" {
		t.Fatal("reload failure erased runtime success")
	}
	var loaded int64
	if err := f.db.QueryRow(t.Context(), `SELECT loaded_generation FROM hosting_proxies WHERE id='proxy_continuity'`).Scan(&loaded); err != nil || loaded != f.loaded.Generation {
		t.Fatalf("failed reload changed loaded evidence %d %v", loaded, err)
	}
	if _, err := f.pg.RequestHostingRoute(t.Context(), f.resource.ID, f.route.ID, "pop_failed_supersede", "usr_bootstrap", domain.HostingRouteInput{ExpectedRouteVersion: f.route.Version + 1, ExpectedHealthyOperationID: next.op.ID, Hostname: f.route.Hostname, PathPrefix: f.route.PathPrefix, IdempotencyKey: "failed-supersede"}, destinationEvent("failed_supersede")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("failed reload reservation lost: %v", err)
	}
}

func TestHostingStorageRouteContinuityPostgresRestoredWriterReleasesReservation(t *testing.T) {
	f := newStorageContinuityFixture(t)
	next := f.successor(t)
	admitContinuityWriter(t, next)
	request := domain.HostingCompleteRequest{HostingAuthority: next.authority, ReceiptID: "restored_continuity", Result: domain.HostingResult{Outcome: "failed", Restored: true, PreviousContainerID: f.op.Result.ContainerID, DaemonID: next.op.Release.DaemonID, PlanHash: next.op.Plan.Hash}}
	finished, err := f.pg.HostingComplete(t.Context(), f.runner, request)
	if err != nil || finished.MutationHold {
		t.Fatalf("restored writer settlement %+v %v", finished, err)
	}
	continuityState(t, f, next.op.ID, "released")
	route, err := f.pg.GetHostingRoute(t.Context(), f.route.ID)
	if err != nil || !route.Route.HTTPReady || route.Route.Reason != "" {
		t.Fatalf("restored readiness not retained %+v %v", route, err)
	}
}

func TestHostingStorageRouteContinuityPostgresRequiresLoadedHTTPS(t *testing.T) {
	for _, kind := range []string{"expired", "missing", "mismatched"} {
		t.Run(kind, func(t *testing.T) {
			f := newStorageContinuityFixture(t)
			next := f.successor(t)
			// Simulate historical/corrupt receipt contents inside this owned fixture.
			// Re-enable immutability before exercising admission.
			result := *f.loaded.Result
			result.Routes = append([]domain.HostingProxyRouteObservation(nil), result.Routes...)
			switch kind {
			case "expired":
				result.Routes[0].Certificate.ExpiresAt = time.Now().Add(-time.Second)
			case "missing":
				result.Routes = nil
			case "mismatched":
				result.Routes[0].Certificate.Hostname = "foreign.nerocd.invalid"
			}
			body, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			tx, err := f.db.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			cleanupContinuityTransaction(t, tx)
			if _, err = tx.Exec(t.Context(), `ALTER TABLE hosting_proxy_receipts DISABLE TRIGGER hosting_proxy_receipts_immutable`); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(t.Context(), `UPDATE hosting_proxy_receipts SET body=$2,body_hash=$3 WHERE operation_id=$1 AND phase='complete'`, f.loaded.ID, body, domain.HostingDocumentHash(result)); err != nil {
				t.Fatal(err)
			}
			if _, err = tx.Exec(t.Context(), `ALTER TABLE hosting_proxy_receipts ENABLE TRIGGER hosting_proxy_receipts_immutable`); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if ack, err := f.pg.HostingStorageCreateBegin(t.Context(), f.runner, next.request()); err == nil || ack.CreatePermitted {
				t.Fatalf("invalid HTTPS admitted creation %+v %v", ack, err)
			}
			var count int
			if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM hosting_storage_operation_admissions WHERE operation_id=$1`, next.op.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("admission committed %d %v", count, err)
			}
		})
	}
}

func TestHostingStorageRouteContinuityPostgresCertificateExpiryAfterOriginWait(t *testing.T) {
	f := newStorageContinuityFixture(t, func(result *domain.HostingProxyResult) {
		for n := range result.Routes {
			result.Routes[n].Certificate.ExpiresAt = time.Now().Add(3 * time.Second)
		}
	})
	next := f.successor(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cleanupContinuityTransaction(t, blocker)
	if _, err = blocker.Exec(ctx, `SELECT storage_id FROM hosting_storage_materializations WHERE storage_id=$1 FOR UPDATE`, f.storage.ID); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		ack domain.HostingStorageCreateAck
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		ack, callErr := f.pg.HostingStorageCreateBegin(ctx, f.runner, next.request())
		done <- outcome{ack, callErr}
	}()
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("admission goroutine did not exit")
			}
		}
	})
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%LockHostingStorageMaterialization%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("admission did not reach origin lock")
		case <-ticker.C:
		}
	}
	expiry := f.loaded.Result.Routes[0].Certificate.ExpiresAt
	if _, err = blocker.Exec(ctx, `SELECT pg_sleep(greatest(0,extract(epoch FROM ($1::timestamptz-clock_timestamp())))+0.05)`, expiry); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var result outcome
	select {
	case result = <-done:
		joined = true
	case <-time.After(5 * time.Second):
		t.Fatal("admission goroutine did not exit")
	}
	if !errors.Is(result.err, store.ErrConflict) || result.ack.CreatePermitted {
		t.Fatalf("expired certificate survived blocked admission %+v %v", result.ack, result.err)
	}
	var count, holds int
	if err = f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_storage_operation_admissions WHERE operation_id=$1),(SELECT count(*) FROM hosting_binding_state WHERE hold_operation_id=$1)`, next.op.ID).Scan(&count, &holds); err != nil || count != 0 || holds != 0 {
		t.Fatalf("partial expired admission %d %d %v", count, holds, err)
	}
}

func TestHostingStorageRouteContinuityPostgresCertificateExpiryPreservesRuntimeResult(t *testing.T) {
	f := newStorageContinuityFixture(t, func(result *domain.HostingProxyResult) {
		for n := range result.Routes {
			result.Routes[n].Certificate.ExpiresAt = time.Now().Add(2 * time.Second)
		}
	})
	next := f.successor(t)
	admitContinuityWriter(t, next)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if _, err := f.db.Exec(ctx, `SELECT pg_sleep(greatest(0,extract(epoch FROM ($1::timestamptz-clock_timestamp())))+0.05)`, f.loaded.Result.Routes[0].Certificate.ExpiresAt); err != nil {
		t.Fatal(err)
	}
	finished := next.complete(t, "succeeded", false)
	if finished.CompleteReceipt == nil || finished.Result == nil || finished.Result.Outcome != "succeeded" {
		t.Fatalf("expired certificate erased runtime evidence %+v", finished)
	}
	continuityState(t, f, next.op.ID, "proxy_pending")
}

func TestHostingStorageRouteContinuityPostgresUncertainCancellationRetainsWriterHold(t *testing.T) {
	f := newStorageContinuityFixture(t)
	next := f.successor(t)
	admitContinuityWriter(t, next)
	if _, err := f.pg.CancelHostingOperation(t.Context(), next.op.ID, destinationEvent("continuity_cancel_admitted")); err != nil {
		t.Fatal(err)
	}
	continuityState(t, f, next.op.ID, "reserved")
	op, err := f.pg.GetHostingOperation(t.Context(), next.op.ID)
	if err != nil || !op.MutationHold || op.State != "cancel_requested" {
		t.Fatalf("uncertain writer released %+v %v", op, err)
	}
}

func TestHostingStorageRouteContinuityPostgresRequiresExplicitExactLoadedScope(t *testing.T) {
	f := newStorageContinuityFixture(t)
	for _, kind := range []string{"absent", "generation", "operation", "version", "release", "port", "pending_proxy", "request_audit"} {
		t.Run(kind, func(t *testing.T) {
			in := f.input("invalid-" + kind)
			switch kind {
			case "absent":
				in.StorageRouteContinuity = nil
			case "generation":
				in.StorageRouteContinuity.ExpectedLoadedGeneration++
			case "operation":
				in.StorageRouteContinuity.ExpectedLoadedProxyOperationID = "pop_foreign"
			case "version":
				in.StorageRouteContinuity.Routes[0].ExpectedVersion++
			case "release":
				in.StorageRouteContinuity.Routes[0].ExpectedSelectedReleaseOperationID = "hop_foreign"
			case "port":
				in.HostPort = 0
			case "pending_proxy":
				if _, err := f.db.Exec(t.Context(), `UPDATE hosting_proxies SET active_operation_id=$1 WHERE id='proxy_continuity'`, f.loaded.ID); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, err := f.db.Exec(t.Context(), `UPDATE hosting_proxies SET active_operation_id=NULL WHERE id='proxy_continuity'`); err != nil {
						t.Error(err)
					}
				}()
			case "request_audit":
				if _, err := f.db.Exec(t.Context(), `CREATE FUNCTION reject_continuity_request() RETURNS trigger LANGUAGE plpgsql AS 'BEGIN IF NEW.action=''hosting.deployment_requested'' THEN RAISE EXCEPTION ''fixture request rejection''; END IF; RETURN NEW; END'; CREATE TRIGGER reject_continuity_request BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_continuity_request()`); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.pg.RequestHostingDeployment(t.Context(), "hop_invalid_"+kind, f.resource.ID, "usr_bootstrap", in, destinationEvent("invalid_"+kind)); err == nil {
				t.Fatal("unsafe request accepted")
			}
			var count int
			if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM hosting_storage_route_continuities`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("partial request reservation %d %v", count, err)
			}
		})
	}
}

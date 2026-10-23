package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type switchControl struct {
	pg       *store.PostgresStore
	db       *pgxpool.Pool
	resource domain.Resource
	server   domain.Server
	a, b     domain.HostingOperation
	routes   []domain.HostingRoute
	loaded   domain.HostingProxyOperation
}

func trustedProxyResult(op domain.HostingProxyOperation, preparation domain.HostingProxyPreparation, outcome string) domain.HostingProxyResult {
	result := domain.HostingProxyResult{Outcome: outcome, LoadedGeneration: op.Generation, PlanHash: op.Plan.Hash, EngineHash: preparation.EngineHash, DaemonID: preparation.DaemonID, ContainerID: strings.Repeat("d", 64)}
	if outcome != "applied" {
		result.LoadedGeneration = op.Plan.PreviousGeneration
	}
	if outcome == "applied" || outcome == "rolled_back" {
		for _, r := range op.Snapshot.Routes {
			result.Routes = append(result.Routes, domain.HostingProxyRouteObservation{RouteID: r.RouteID, HTTPReady: true, HTTPSReady: true, Certificate: domain.HostingProxyCertificate{Hostname: r.Hostname, Issuer: "fixture", Fingerprint: strings.Repeat("e", 64), ExpiresAt: time.Now().Add(time.Hour), Status: "trusted"}})
		}
	}
	return result
}

func admitSwitchOperation(t *testing.T, f switchControl, id string) (domain.HostingAuthority, domain.HostingProxyOperation, domain.HostingProxyPreparation) {
	t.Helper()
	a, op := claimProxyPlan(t, f.pg, id, f.server.RunnerID)
	preparation := domain.HostingProxyPreparation{DaemonID: f.a.Release.DaemonID, PlanHash: op.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}
	if _, err := f.pg.RunnerProxyPrepared(t.Context(), f.server.RunnerID, domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "prepare_" + id, HostingProxyPreparation: preparation}); err != nil {
		t.Fatal(err)
	}
	op, err := f.pg.RunnerProxyApplyBegin(t.Context(), f.server.RunnerID, a)
	if err != nil {
		t.Fatal(err)
	}
	return a, op, preparation
}

func switchControlFixture(t *testing.T, stopOut ...*func(context.Context) error) switchControl {
	t.Helper()
	pg, db, resource, server, _, a := proxyControlFixture(t, stopOut...)
	f := switchControl{pg: pg, db: db, resource: resource, server: server, a: a}
	if _, err := db.Exec(t.Context(), `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, server.RunnerID, domain.HostingProxyVerifiedSwitchCapability); err != nil {
		t.Fatal(err)
	}
	for n, path := range []string{"/", "/api", "/unselected"} {
		id := "pop_switch_route_" + strings.Repeat("x", n+1)
		route, err := pg.RequestHostingRoute(t.Context(), resource.ID, "", id, "usr_bootstrap", domain.HostingRouteInput{Hostname: "switch.apps.nerocd.invalid", PathPrefix: path, ExpectedHealthyOperationID: a.ID, IdempotencyKey: id}, destinationEvent(id))
		if err != nil {
			t.Fatal(err)
		}
		f.routes = append(f.routes, route)
		authority, op, prepared := admitSwitchOperation(t, f, id)
		finished, err := pg.RunnerProxyComplete(t.Context(), server.RunnerID, domain.HostingProxyCompleteRequest{HostingAuthority: authority, ReceiptID: "complete_" + id, Result: trustedProxyResult(op, prepared, "applied")})
		if err != nil {
			t.Fatal(err)
		}
		f.loaded = finished
	}
	f.b = settleProxyImage(t, pg, resource, server, "hop_switch_b", 18081)
	return f
}

func (f switchControl) input(key string) domain.HostingRouteSwitchInput {
	in := domain.HostingRouteSwitchInput{IdempotencyKey: key, ExpectedLoadedGeneration: f.loaded.Generation, TargetReleaseOperationID: f.b.ID}
	for _, r := range f.routes[:2] {
		in.Routes = append(in.Routes, domain.HostingRouteSwitchSelection{RouteID: r.ID, ExpectedVersion: r.Version, ExpectedSelectedReleaseOperationID: f.a.ID})
	}
	return in
}

func TestPostgresVerifiedRouteSwitch(t *testing.T) {
	t.Run("exact_scope_and_no_legacy_drift", func(t *testing.T) {
		f := switchControlFixture(t)
		in := f.input("scope")
		op, err := f.pg.RequestHostingRouteSwitch(t.Context(), f.resource.ID, "pop_switch_scope", "usr_bootstrap", in, destinationEvent("switch_scope"))
		if err != nil {
			t.Fatal(err)
		}
		if op.Snapshot.VerifiedSwitch == nil || len(op.Snapshot.VerifiedSwitch.Routes) != 2 || len(op.Snapshot.Routes) != 3 {
			t.Fatalf("scope %+v", op.Snapshot)
		}
		for _, r := range op.Snapshot.Routes {
			if r.RouteID == f.routes[2].ID && r.ReleaseOperationID != f.a.ID {
				t.Fatal("unselected release changed")
			}
		}
		if _, err = f.pg.RequestHostingRoute(t.Context(), f.resource.ID, f.routes[2].ID, "pop_legacy_bypass", "usr_bootstrap", domain.HostingRouteInput{Hostname: f.routes[2].Hostname, PathPrefix: f.routes[2].PathPrefix, ExpectedRouteVersion: f.routes[2].Version, ExpectedHealthyOperationID: f.b.ID, IdempotencyKey: "legacy-bypass"}, destinationEvent("legacy-bypass")); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("legacy drift: %v", err)
		}
		replay, err := f.pg.RequestHostingRouteSwitch(t.Context(), f.resource.ID, "pop_ignored_replay", "usr_bootstrap", in, destinationEvent("replay"))
		if err != nil || replay.ID != op.ID {
			t.Fatalf("request replay: %+v %v", replay, err)
		}
		if _, err = f.pg.RequestHostingReleaseCleanup(t.Context(), "hop_cleanup_a", f.resource.ID, f.a.ID, "usr_bootstrap", domain.HostingCleanupInput{ExpectedGeneration: f.resource.Generation, IdempotencyKey: "cleanup-a"}, destinationEvent("cleanup-a")); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("prior reference cleanup admitted: %v", err)
		}
		if _, err = f.db.Exec(t.Context(), `UPDATE hosting_proxy_switch_prior_references SET prior_version=prior_version+1 WHERE operation_id=$1`, op.ID); err == nil {
			t.Fatal("prior sidecar mutated")
		}
	})
	t.Run("tls_and_rollback_metadata_cas_lost_response", func(t *testing.T) {
		f := switchControlFixture(t)
		op, err := f.pg.RequestHostingRouteSwitch(t.Context(), f.resource.ID, "pop_switch_rollback", "usr_bootstrap", f.input("rollback"), destinationEvent("rollback"))
		if err != nil {
			t.Fatal(err)
		}
		a, op, prepared := admitSwitchOperation(t, f, op.ID)
		result := trustedProxyResult(op, prepared, "rolled_back")
		bad := result
		bad.Routes = append([]domain.HostingProxyRouteObservation(nil), result.Routes...)
		for n := range bad.Routes {
			if bad.Routes[n].RouteID == f.routes[0].ID {
				bad.Routes[n].HTTPSReady = false
			}
		}
		if _, err = f.pg.RunnerProxyComplete(t.Context(), f.server.RunnerID, domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "bad_tls", Result: bad}); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("unverified rollback admitted: %v", err)
		}
		request := domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "rollback_complete", Result: result}
		finished, err := f.pg.RunnerProxyComplete(t.Context(), f.server.RunnerID, request)
		if err != nil || finished.MutationHold || finished.State != "rolled_back" {
			t.Fatalf("rollback %+v %v", finished, err)
		}
		for _, r := range f.routes[:2] {
			row, err := f.pg.GetHostingRoute(t.Context(), r.ID)
			if err != nil || row.Route.SelectedReleaseOperationID != f.a.ID || row.Route.Version != r.Version+2 || !row.Route.HTTPSReady {
				t.Fatalf("metadata %+v %v", row, err)
			}
		}
		before, err := f.pg.ListHostingRoutes(t.Context(), f.resource.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.db.Exec(t.Context(), `UPDATE hosting_proxy_receipts SET body=body WHERE operation_id=$1`, op.ID); err == nil {
			t.Fatal("receipt mutated")
		}
		replayed, err := f.pg.RunnerProxyComplete(t.Context(), f.server.RunnerID, request)
		after, listErr := f.pg.ListHostingRoutes(t.Context(), f.resource.ID)
		if err != nil || listErr != nil || !reflect.DeepEqual(before, after) || !reflect.DeepEqual(finished.Result, replayed.Result) {
			t.Fatalf("receipt replay changed metadata %v %v", err, listErr)
		}
		// A rollback leaves desired ahead of loaded. A later switch still uses
		// the exact old committed operation and fresh desired versions.
		next := f.input("after-rollback")
		for n := range next.Routes {
			next.Routes[n].ExpectedVersion += 2
		}
		if _, err = f.pg.RequestHostingRouteSwitch(t.Context(), f.resource.ID, "pop_after_rollback", "usr_bootstrap", next, destinationEvent("after-rollback")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("retained_a", func(t *testing.T) {
		f := switchControlFixture(t)
		in := f.input("retained-a")
		in.TargetReleaseOperationID = f.a.ID
		if _, err := f.pg.RequestHostingRouteSwitch(t.Context(), f.resource.ID, "pop_retained_a", "usr_bootstrap", in, destinationEvent("retained-a")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("old_agent_denied", func(t *testing.T) {
		f := switchControlFixture(t)
		if _, err := f.db.Exec(t.Context(), `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.server.RunnerID, domain.HostingProxyVerifiedSwitchCapability); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pg.RequestHostingRouteSwitch(t.Context(), f.resource.ID, "pop_old_agent", "usr_bootstrap", f.input("old-agent"), destinationEvent("old-agent")); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("old agent admitted: %v", err)
		}
	})
	t.Run("revoke_selected_then_rollback_then_enable", func(t *testing.T) {
		f := switchControlFixture(t)
		op, err := f.pg.RequestHostingRouteSwitch(t.Context(), f.resource.ID, "pop_revoke_switch", "usr_bootstrap", f.input("revoke-switch"), destinationEvent("revoke-switch"))
		if err != nil {
			t.Fatal(err)
		}
		a, op, prepared := admitSwitchOperation(t, f, op.ID)
		if _, err = f.pg.RevokeHostingHostname(t.Context(), "proxy_fixture", "grant_proxy_hostname", "usr_bootstrap", "revoke-switch", destinationEvent("revoke-during-switch")); err != nil {
			t.Fatal(err)
		}
		request := domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "revoked_rollback", Result: trustedProxyResult(op, prepared, "rolled_back")}
		finished, err := f.pg.RunnerProxyComplete(t.Context(), f.server.RunnerID, request)
		if err != nil || !finished.MutationHold || !finished.MetadataConflict || finished.Result == nil || finished.Result.Outcome != "rolled_back" {
			t.Fatalf("conflict lost original result/hold %+v %v", finished, err)
		}
		view, readErr := f.pg.HostingRouteSwitchOperation(t.Context(), f.resource.ID, "usr_bootstrap", op.ID)
		if readErr != nil || view.EngineOutcome != "rolled_back" || view.State != "uncertain" || !view.MetadataConflict || !view.MutationHold || view.Switch == nil || len(view.Switch.Routes) != 2 {
			t.Fatalf("safe conflict recovery %+v %v", view, readErr)
		}
		if _, err = f.pg.SetHostingProxyEnabled(t.Context(), "proxy_fixture", "usr_bootstrap", "cannot-revive", true, destinationEvent("cannot-revive")); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("unverified B revived: %v", err)
		}
		replay, err := f.pg.RunnerProxyComplete(t.Context(), f.server.RunnerID, request)
		if err != nil || !replay.MutationHold || !replay.MetadataConflict || !reflect.DeepEqual(replay.Result, finished.Result) {
			t.Fatalf("lost response cleared conflict %+v %v", replay, err)
		}
		for _, r := range f.routes {
			row, err := f.pg.GetHostingRoute(t.Context(), r.ID)
			if err != nil || row.Route.Enabled {
				t.Fatalf("revocation reverted: %+v %v", row, err)
			}
		}
	})
}

func TestPostgresVerifiedSwitchCancellation(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unadmitted_restore", true: "cancel_hold"}[admitted], func(t *testing.T) {
			f := switchControlFixture(t)
			op, err := f.pg.RequestHostingRouteSwitch(t.Context(), f.resource.ID, "pop_cancel_switch", "usr_bootstrap", f.input("cancel"), destinationEvent("cancel_switch"))
			if err != nil {
				t.Fatal(err)
			}
			if admitted {
				_, _, _ = admitSwitchOperation(t, f, op.ID)
			} else {
				// Dispatch may race cancellation's preflight run-ID compare. Wait
				// for dispatch without claiming or admitting the unheld operation.
				op = awaitProxyDispatch(t, f.pg, op.ID)
				if op.State != "dispatched" || op.MutationHold || op.PreparedReceipt != nil || op.Preparation != nil {
					t.Fatalf("unadmitted cancellation state=%q hold=%t", op.State, op.MutationHold)
				}
				var leases, admissions int
				if err := f.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM run_leases WHERE run_id=$1),(SELECT count(*) FROM hosting_proxy_admissions WHERE operation_id=$2)`, op.RunID, op.ID).Scan(&leases, &admissions); err != nil || leases != 0 || admissions != 0 {
					t.Fatalf("unadmitted fixture leases=%d admissions=%d err=%v", leases, admissions, err)
				}
			}
			canceled, err := f.pg.CancelHostingProxyOperation(t.Context(), op.ID, "usr_bootstrap", destinationEvent("cancel_switch_request"))
			if err != nil {
				current, readErr := f.pg.GetHostingProxyOperation(t.Context(), op.ID)
				t.Fatalf("cancel: %v; requested run=%q state=%q; current run=%q state=%q hold=%t; read=%v", err, op.RunID, op.State, current.RunID, current.State, current.MutationHold, readErr)
			}
			if canceled.MutationHold != admitted {
				t.Fatalf("hold %+v", canceled)
			}
			want := f.a.ID
			version := int64(2)
			if admitted {
				want = f.b.ID
				version = 1
			}
			for _, r := range f.routes[:2] {
				row, err := f.pg.GetHostingRoute(t.Context(), r.ID)
				if err != nil || row.Route.SelectedReleaseOperationID != want || row.Route.Version != r.Version+version {
					t.Fatalf("cancel metadata %+v %v", row, err)
				}
			}
			if admitted {
				if _, err = f.pg.SetHostingProxyEnabled(t.Context(), "proxy_fixture", "usr_bootstrap", "cancel-enable", true, destinationEvent("cancel_enable")); !errors.Is(err, store.ErrConflict) {
					t.Fatalf("cancel released hold: %v", err)
				}
			}
		})
	}
}

func TestPostgresVerifiedSwitchQueuedCancellation(t *testing.T) {
	var stop func(context.Context) error
	f := switchControlFixture(t, &stop)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := stop(ctx); err != nil {
		t.Fatal(err)
	}
	op, err := f.pg.RequestHostingRouteSwitch(ctx, f.resource.ID, "pop_queued_cancel_switch", "usr_bootstrap", f.input("queued-cancel"), destinationEvent("queued_cancel_switch"))
	if err != nil {
		t.Fatal(err)
	}
	if op.State != "queued" || op.RunID != "" || op.MutationHold {
		t.Fatalf("queued fixture state=%q run=%q hold=%t", op.State, op.RunID, op.MutationHold)
	}
	canceled, err := f.pg.CancelHostingProxyOperation(ctx, op.ID, "usr_bootstrap", destinationEvent("queued_cancel_request"))
	if err != nil || canceled.State != "canceled" || canceled.RunID != "" || canceled.MutationHold {
		t.Fatalf("queued cancellation state=%q run=%q hold=%t err=%v", canceled.State, canceled.RunID, canceled.MutationHold, err)
	}
	if err := f.pg.PrepareProxy(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	var leases, admissions int
	if err := f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM run_leases WHERE run_id=$1),(SELECT count(*) FROM hosting_proxy_admissions WHERE operation_id=$2)`, "proxy_"+op.ID, op.ID).Scan(&leases, &admissions); err != nil || leases != 0 || admissions != 0 {
		t.Fatalf("queued cancellation leases=%d admissions=%d err=%v", leases, admissions, err)
	}
	for _, prior := range f.routes[:2] {
		record, err := f.pg.GetHostingRoute(ctx, prior.ID)
		if err != nil || record.Route.SelectedReleaseOperationID != f.a.ID || record.Route.Version != prior.Version+2 {
			t.Fatalf("queued metadata route=%+v err=%v", record.Route, err)
		}
	}
}

func TestPostgresVerifiedSwitchConcurrentAdmission(t *testing.T) {
	f := switchControlFixture(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	var work sync.WaitGroup
	for _, key := range []string{"one", "two"} {
		work.Go(func() {
			<-start
			_, err := f.pg.RequestHostingRouteSwitch(t.Context(), f.resource.ID, "pop_concurrent_"+key, "usr_bootstrap", f.input(key), destinationEvent("concurrent_"+key))
			results <- err
		})
	}
	close(start)
	work.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, store.ErrConflict):
			conflict++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("admission success=%d conflict=%d", success, conflict)
	}
}

func TestPostgresVerifiedSwitchAuthorityAfterResourceWait(t *testing.T) {
	for _, revoke := range []string{"capability", "actor"} {
		t.Run(revoke, func(t *testing.T) {
			f := switchControlFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			op, err := f.pg.RequestHostingRouteSwitch(ctx, f.resource.ID, "pop_wait_switch", "usr_bootstrap", f.input("wait"), destinationEvent("wait_switch"))
			if err != nil {
				t.Fatal(err)
			}
			a, op := claimProxyPlan(t, f.pg, op.ID, f.server.RunnerID)
			prepared := domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "wait_prepare_switch", HostingProxyPreparation: domain.HostingProxyPreparation{DaemonID: f.a.Release.DaemonID, PlanHash: op.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}}
			if _, err = f.pg.RunnerProxyPrepared(ctx, f.server.RunnerID, prepared); err != nil {
				t.Fatal(err)
			}
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				_ = blocker.Rollback(cleanup)
			}()
			var pid int
			if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if _, err = blocker.Exec(ctx, `SELECT id FROM resources WHERE id=$1 FOR UPDATE`, f.resource.ID); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			var work sync.WaitGroup
			work.Go(func() { _, err := f.pg.RunnerProxyApplyBegin(ctx, f.server.RunnerID, a); done <- err })
			defer func() {
				cancel()
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				_ = blocker.Rollback(cleanup)
				work.Wait()
			}()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("did not wait: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-ticker.C:
				}
			}
			if revoke == "capability" {
				_, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.server.RunnerID, domain.HostingProxyVerifiedSwitchCapability)
			} else {
				_, err = f.db.Exec(ctx, `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err == nil {
				t.Fatal("revoked authority admitted mutation")
			}
			current, err := f.pg.GetHostingProxyOperation(ctx, op.ID)
			if err != nil || current.MutationHold {
				t.Fatalf("rejected operation gained hold %+v %v", current, err)
			}
		})
	}
}

func TestPostgresVerifiedSwitchHistoricalCleanupReferences(t *testing.T) {
	f := switchControlFixture(t)
	in := f.input("all")
	r := f.routes[2]
	in.Routes = append(in.Routes, domain.HostingRouteSwitchSelection{RouteID: r.ID, ExpectedVersion: r.Version, ExpectedSelectedReleaseOperationID: f.a.ID})
	op, err := f.pg.RequestHostingRouteSwitch(t.Context(), f.resource.ID, "pop_all_switch", "usr_bootstrap", in, destinationEvent("all_switch"))
	if err != nil {
		t.Fatal(err)
	}
	a, op, prepared := admitSwitchOperation(t, f, op.ID)
	if _, err = f.pg.RunnerProxyComplete(t.Context(), f.server.RunnerID, domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "all_complete", Result: trustedProxyResult(op, prepared, "applied")}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.RequestHostingReleaseCleanup(t.Context(), "hop_cleanup_historical", f.resource.ID, f.a.ID, "usr_bootstrap", domain.HostingCleanupInput{ExpectedGeneration: f.resource.Generation, IdempotencyKey: "cleanup-historical"}, destinationEvent("cleanup_historical")); err != nil {
		t.Fatalf("historical prior reference blocked cleanup: %v", err)
	}
}

func TestPostgresVerifiedSwitchWithdrawal(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unadmitted_superseded", true: "deferred_safe_snapshot"}[admitted], func(t *testing.T) {
			f := switchControlFixture(t)
			op, err := f.pg.RequestHostingRouteSwitch(t.Context(), f.resource.ID, "pop_withdraw_switch", "usr_bootstrap", f.input("withdraw"), destinationEvent("withdraw_switch"))
			if err != nil {
				t.Fatal(err)
			}
			if admitted {
				_, _, _ = admitSwitchOperation(t, f, op.ID)
			}
			first, err := f.pg.RevokeHostingHostname(t.Context(), "proxy_fixture", "grant_proxy_hostname", "usr_bootstrap", "withdraw", destinationEvent("withdraw_first"))
			if err != nil {
				t.Fatal(err)
			}
			replay, err := f.pg.RevokeHostingHostname(t.Context(), "proxy_fixture", "grant_proxy_hostname", "usr_bootstrap", "withdraw", destinationEvent("withdraw_replay"))
			if err != nil || replay.DesiredGeneration != first.DesiredGeneration {
				t.Fatalf("withdrawal replay %+v %v", replay, err)
			}
			current, err := f.pg.GetHostingProxyOperation(t.Context(), op.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.MutationHold != admitted {
				t.Fatalf("withdrawal hold %+v", current)
			}
			if !admitted && current.State != "superseded" {
				t.Fatalf("strict operation still dispatchable %+v", current)
			}
			var raw []byte
			if err = f.db.QueryRow(t.Context(), `SELECT snapshot FROM hosting_proxy_operations WHERE proxy_id='proxy_fixture' ORDER BY generation DESC LIMIT 1`).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var snapshot domain.HostingProxySnapshot
			if err = json.Unmarshal(raw, &snapshot); err != nil {
				t.Fatal(err)
			}
			if snapshot.VerifiedSwitch != nil || len(snapshot.Routes) != 0 {
				t.Fatalf("withdrawal publishes unauthorized route %+v", snapshot)
			}
			for _, r := range f.routes {
				row, err := f.pg.GetHostingRoute(t.Context(), r.ID)
				if err != nil || row.Route.Enabled {
					t.Fatalf("route not withdrawn %+v %v", row, err)
				}
				if !admitted && row.Route.SelectedReleaseOperationID != f.a.ID {
					t.Fatal("unadmitted candidate metadata survived")
				}
			}
		})
	}
}

func TestPostgresVerifiedSwitchSchemaParity(t *testing.T) {
	f := switchControlFixture(t)
	if ok, err := f.pg.SchemaCompatible(t.Context()); err != nil || !ok {
		t.Fatalf("migrated strict schema incompatible: %v %v", ok, err)
	}
	if _, err := f.db.Exec(t.Context(), `ALTER TABLE hosting_proxy_operations DISABLE TRIGGER hosting_proxy_switch_operation_consistent`); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.pg.SchemaCompatible(t.Context()); err != nil || ok {
		t.Fatalf("disabled strict consistency accepted: %v %v", ok, err)
	}
}

func TestPostgresVerifiedSwitchDestinationRevocation(t *testing.T) {
	f := switchControlFixture(t)
	ctx := t.Context()
	other := createDestinationResource(t, f.pg, "switch_other", "proj_security")
	grantDestination(t, f.pg, f.a.Snapshot.DestinationID, other.ProjectID, "switch_other_grant")
	if _, err := f.pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_switch_other", ResourceID: other.ID, DestinationID: f.a.Snapshot.DestinationID, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("switch_other_binding")); err != nil {
		t.Fatal(err)
	}
	healthy := settleProxyImage(t, f.pg, other, f.server, "hop_switch_other", 18082)
	if _, err := f.pg.GrantHostingHostname(ctx, "proxy_fixture", "grant_switch_other", "usr_bootstrap", domain.HostingHostnameGrantInput{ProjectID: other.ProjectID, Hostname: "other.example.test"}, destinationEvent("switch_other_hostname")); err != nil {
		t.Fatal(err)
	}
	route, err := f.pg.RequestHostingRoute(ctx, other.ID, "", "pop_other_route", "usr_bootstrap", domain.HostingRouteInput{IdempotencyKey: "other-route", Hostname: "other.example.test", PathPrefix: "/", ExpectedHealthyOperationID: healthy.ID}, destinationEvent("other_route"))
	if err != nil {
		t.Fatal(err)
	}
	authority, prior, prepared := admitSwitchOperation(t, f, route.OperationID)
	f.loaded, err = f.pg.RunnerProxyComplete(ctx, f.server.RunnerID, domain.HostingProxyCompleteRequest{HostingAuthority: authority, ReceiptID: "other_route_complete", Result: trustedProxyResult(prior, prepared, "applied")})
	if err != nil {
		t.Fatal(err)
	}
	op, err := f.pg.RequestHostingRouteSwitch(ctx, f.resource.ID, "pop_destination_switch", "usr_bootstrap", f.input("destination"), destinationEvent("destination_switch"))
	if err != nil {
		t.Fatal(err)
	}
	authority, op, prepared = admitSwitchOperation(t, f, op.ID)
	// A failed audit rolls back the grant, route flags, and deferred operation together.
	bad := destinationEvent("destination_bad_audit")
	bad.ID = ""
	if _, err = f.pg.RevokeDestinationProject(ctx, f.a.Snapshot.DestinationID, f.resource.ProjectID, bad); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("bad audit committed: %v", err)
	}
	selected, err := f.pg.GetHostingRoute(ctx, f.routes[0].ID)
	if err != nil || !selected.Route.Enabled {
		t.Fatalf("failed revocation changed route %+v %v", selected, err)
	}
	if _, err = f.pg.RevokeDestinationProject(ctx, f.a.Snapshot.DestinationID, f.resource.ProjectID, destinationEvent("destination_revoke")); err != nil {
		t.Fatal(err)
	}
	unaffected, err := f.pg.GetHostingRoute(ctx, route.ID)
	if err != nil || !unaffected.Route.Enabled || unaffected.Route.SelectedReleaseOperationID != healthy.ID {
		t.Fatalf("unrelated project changed %+v %v", unaffected, err)
	}
	var raw []byte
	if err = f.db.QueryRow(ctx, `SELECT snapshot FROM hosting_proxy_operations WHERE proxy_id='proxy_fixture' ORDER BY generation DESC LIMIT 1`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var withdrawal domain.HostingProxySnapshot
	if err = json.Unmarshal(raw, &withdrawal); err != nil {
		t.Fatal(err)
	}
	if len(withdrawal.Routes) != 1 || withdrawal.Routes[0].RouteID != route.ID {
		t.Fatalf("withdrawal scope %+v", withdrawal)
	}
	request := domain.HostingProxyCompleteRequest{HostingAuthority: authority, ReceiptID: "destination_rollback", Result: trustedProxyResult(op, prepared, "rolled_back")}
	finished, err := f.pg.RunnerProxyComplete(ctx, f.server.RunnerID, request)
	if err != nil || !finished.MetadataConflict || !finished.MutationHold {
		t.Fatalf("destination rollback lost hold %+v %v", finished, err)
	}
	grantDestination(t, f.pg, f.a.Snapshot.DestinationID, f.resource.ProjectID, "destination_regrant")
	if _, err = f.pg.SetHostingProxyEnabled(ctx, "proxy_fixture", "usr_bootstrap", "destination_enable", true, destinationEvent("destination_enable")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("regrant published B: %v", err)
	}
}

func TestPostgresVerifiedSwitchConcurrentDestinationWithdrawal(t *testing.T) {
	f := switchControlFixture(t)
	op, err := f.pg.RequestHostingRouteSwitch(t.Context(), f.resource.ID, "pop_concurrent_withdraw", "usr_bootstrap", f.input("concurrent-withdraw"), destinationEvent("concurrent_withdraw"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = admitSwitchOperation(t, f, op.ID)
	start := make(chan struct{})
	results := make(chan error, 2)
	var work sync.WaitGroup
	for _, key := range []string{"one", "two"} {
		work.Go(func() {
			<-start
			_, err := f.pg.RevokeDestinationProject(t.Context(), f.a.Snapshot.DestinationID, f.resource.ProjectID, destinationEvent("concurrent_destination_"+key))
			results <- err
		})
	}
	close(start)
	work.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		switch {
		case err == nil:
			success++
		case errors.Is(err, store.ErrConflict):
			conflict++
		default:
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("withdrawal success=%d conflict=%d", success, conflict)
	}
	var count int
	if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM hosting_proxy_operations WHERE proxy_id='proxy_fixture' AND state='queued' AND NOT(snapshot ? 'verified_switch')`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("queued withdrawal count=%d err=%v", count, err)
	}
	current, err := f.pg.GetHostingProxyOperation(t.Context(), op.ID)
	if err != nil || !current.MutationHold {
		t.Fatalf("concurrent revocation released hold %+v %v", current, err)
	}
}

func TestPostgresVerifiedSwitchAuthorityAfterAdmissionAuditWait(t *testing.T) {
	for _, revoke := range []string{"expiry", "capability", "token_expiry"} {
		t.Run(revoke, func(t *testing.T) {
			f := switchControlFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			actor := "usr_bootstrap"
			if revoke == "token_expiry" {
				principal, _ := proxyLegacyToken(t, f.pg, proxyTokenApplication(t, f.pg), "tok_strict_admission", []string{domain.RoleSystemAdmin})
				actor = principal.ID
			}
			op, err := f.pg.RequestHostingRouteSwitch(ctx, f.resource.ID, "pop_audit_wait_switch", actor, f.input("audit-wait"), destinationEvent("audit_wait_switch"))
			if err != nil {
				t.Fatal(err)
			}
			a, op := claimProxyPlan(t, f.pg, op.ID, f.server.RunnerID)
			prepared := domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "audit_wait_prepare", HostingProxyPreparation: domain.HostingProxyPreparation{DaemonID: f.a.Release.DaemonID, PlanHash: op.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}}
			if _, err = f.pg.RunnerProxyPrepared(ctx, f.server.RunnerID, prepared); err != nil {
				t.Fatal(err)
			}
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				_ = blocker.Rollback(cleanup)
			}()
			var key int64
			var pid int
			if err = blocker.QueryRow(ctx, `SELECT hashtextextended(current_schema()||':strict-admission-audit',0),pg_backend_pid()`).Scan(&key, &pid); err != nil {
				t.Fatal(err)
			}
			if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(ctx, `CREATE FUNCTION strict_admission_audit_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.proxy_apply_admitted' THEN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':strict-admission-audit',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER strict_admission_audit_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION strict_admission_audit_wait()`); err != nil {
				t.Fatal(err)
			}
			if revoke == "expiry" {
				if _, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1`, a.LeaseID); err != nil {
					t.Fatal(err)
				}
			}
			if revoke == "token_expiry" {
				if _, err = f.db.Exec(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1`, actor); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			var work sync.WaitGroup
			work.Go(func() { _, err := f.pg.RunnerProxyApplyBegin(ctx, f.server.RunnerID, a); done <- err })
			defer func() {
				cancel()
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				_ = blocker.Rollback(cleanup)
				work.Wait()
			}()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%CreateAuditEvent%')`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("admission did not wait on audit: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-ticker.C:
				}
			}
			if revoke == "capability" {
				_, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.server.RunnerID, domain.HostingProxyVerifiedSwitchCapability)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				for {
					var expired bool
					query, identity := `SELECT expires_at<=clock_timestamp() FROM run_leases WHERE id=$1`, a.LeaseID
					if revoke == "token_expiry" {
						query, identity = `SELECT expires_at<=clock_timestamp() FROM api_tokens WHERE id=$1`, actor
					}
					if err = f.db.QueryRow(ctx, query, identity).Scan(&expired); err != nil {
						t.Fatal(err)
					}
					if expired {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-ticker.C:
					}
				}
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err == nil {
				t.Fatal("authority lost during audit still admitted mutation")
			}
			current, err := f.pg.GetHostingProxyOperation(ctx, op.ID)
			if err != nil || current.MutationHold || current.State != "prepared" {
				t.Fatalf("failed admission committed state %+v %v", current, err)
			}
			var count int
			if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_proxy_admissions WHERE operation_id=$1`, op.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed admission persisted ownership count=%d %v", count, err)
			}
			if err = f.db.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE id=$1`, "aud_proxy_admit_"+op.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed admission retained audit count=%d %v", count, err)
			}

		})
	}
}

func TestPostgresVerifiedSwitchRunnerTailLockWithRenewAndClaim(t *testing.T) {
	f := switchControlFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	op, err := f.pg.RequestHostingRouteSwitch(ctx, f.resource.ID, "pop_tail_lock", "usr_bootstrap", f.input("tail-lock"), destinationEvent("tail_lock"))
	if err != nil {
		t.Fatal(err)
	}
	a, op := claimProxyPlan(t, f.pg, op.ID, f.server.RunnerID)
	preparation := domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "tail_prepare", HostingProxyPreparation: domain.HostingProxyPreparation{DaemonID: f.a.Release.DaemonID, PlanHash: op.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}}
	if _, err = f.pg.RunnerProxyPrepared(ctx, f.server.RunnerID, preparation); err != nil {
		t.Fatal(err)
	}
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var work sync.WaitGroup
	defer func() {
		cancel()
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_ = blocker.Rollback(cleanup)
		work.Wait()
	}()
	var pid int
	if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `UPDATE runners SET last_heartbeat_at=clock_timestamp() WHERE id=$1`, f.server.RunnerID); err != nil {
		t.Fatal(err)
	}
	admitted := make(chan error, 1)
	work.Go(func() { _, err := f.pg.RunnerProxyApplyBegin(ctx, f.server.RunnerID, a); admitted <- err })
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var admissionPID int
	for {
		err = f.db.QueryRow(ctx, `SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%ProxyLockVerifiedSwitchRunner%'`, pid).Scan(&admissionPID)
		if err == nil {
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		select {
		case err := <-admitted:
			t.Fatalf("did not wait for runner update: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	renewed := make(chan error, 1)
	work.Go(func() {
		_, err := f.pg.RenewLease(ctx, f.server.RunnerID, a.LeaseID, a.Fence, a.Attempt, time.Now(), time.Minute)
		renewed <- err
	})
	for {
		var waiting bool
		if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%RenewAuthorizedLease%')`, admissionPID).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-renewed:
			t.Fatalf("renewal did not serialize with admission: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	claimed := make(chan error, 1)
	work.Go(func() { _, err := f.pg.ClaimRun(ctx, f.server.RunnerID, time.Now(), time.Minute); claimed <- err })
	select {
	case err = <-claimed:
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("concurrent claim: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("claim waited on held attempt instead of skipping it")
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-admitted; err != nil {
		t.Fatalf("admission deadlocked with runner update: %v", err)
	}
	if err = <-renewed; err != nil {
		t.Fatalf("renewal deadlocked with runner admission: %v", err)
	}
	current, err := f.pg.GetHostingProxyOperation(ctx, op.ID)
	if err != nil || !current.MutationHold || current.State != "applying" {
		t.Fatalf("valid admission failed %+v %v", current, err)
	}
}

func TestPostgresVerifiedSwitchCertificateFreshAtCompletionCommit(t *testing.T) {
	for _, outcome := range []string{"applied", "rolled_back"} {
		t.Run(outcome, func(t *testing.T) {
			f := switchControlFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			op, err := f.pg.RequestHostingRouteSwitch(ctx, f.resource.ID, "pop_certificate_wait", "usr_bootstrap", f.input("certificate-wait"), destinationEvent("certificate_wait"))
			if err != nil {
				t.Fatal(err)
			}
			a, op, prepared := admitSwitchOperation(t, f, op.ID)
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var work sync.WaitGroup
			defer func() {
				cancel()
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				_ = blocker.Rollback(cleanup)
				work.Wait()
			}()
			var key int64
			var pid int
			if err = blocker.QueryRow(ctx, `SELECT hashtextextended(current_schema()||':strict-completion-audit',0),pg_backend_pid()`).Scan(&key, &pid); err != nil {
				t.Fatal(err)
			}
			if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(ctx, `CREATE FUNCTION strict_completion_audit_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.proxy_completed' THEN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':strict-completion-audit',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER strict_completion_audit_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION strict_completion_audit_wait()`); err != nil {
				t.Fatal(err)
			}
			request := domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "certificate_complete", Result: trustedProxyResult(op, prepared, outcome)}
			expires := time.Now().Add(2 * time.Second)
			for n := range request.Result.Routes {
				request.Result.Routes[n].Certificate.ExpiresAt = expires
			}
			done := make(chan error, 1)
			work.Go(func() { _, err := f.pg.RunnerProxyComplete(ctx, f.server.RunnerID, request); done <- err })
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%CreateAuditEvent%')`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err := <-done:
					t.Fatalf("completion did not wait: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-ticker.C:
				}
			}
			for {
				var expired bool
				if err = f.db.QueryRow(ctx, `SELECT $1::timestamptz<=clock_timestamp()`, expires).Scan(&expired); err != nil {
					t.Fatal(err)
				}
				if expired {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-ticker.C:
				}
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; !errors.Is(err, store.ErrConflict) {
				t.Fatalf("expired completion admitted: %v", err)
			}
			current, err := f.pg.GetHostingProxyOperation(ctx, op.ID)
			if err != nil || !current.MutationHold || current.State != "applying" || current.CompleteReceipt != nil || current.Result != nil {
				t.Fatalf("expired completion changed state %+v %v", current, err)
			}
			var receipts, audits int
			var lease, task string
			if err = f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_proxy_receipts WHERE operation_id=$1 AND phase='complete'),(SELECT count(*) FROM audit_events WHERE id=$2),l.status,r.status FROM run_leases l JOIN task_runs r ON r.id=l.run_id WHERE l.id=$3`, op.ID, "aud_proxy_complete_"+op.ID, a.LeaseID).Scan(&receipts, &audits, &lease, &task); err != nil || receipts != 0 || audits != 0 || lease != "active" || task != "running" {
				t.Fatalf("completion was not atomic receipts=%d audits=%d lease=%s task=%s err=%v", receipts, audits, lease, task, err)
			}
			for _, route := range f.routes[:2] {
				row, err := f.pg.GetHostingRoute(ctx, route.ID)
				if err != nil || row.Route.SelectedReleaseOperationID != f.b.ID || row.Route.Version != route.Version+1 {
					t.Fatalf("expired rollback changed metadata %+v %v", row, err)
				}
			}
			// Fresh acceptance succeeds; replay of that exact immutable receipt remains
			// historical after its certificate expiry and cannot repeat metadata CAS.
			expires = time.Now().Add(time.Second)
			for n := range request.Result.Routes {
				request.Result.Routes[n].Certificate.ExpiresAt = expires
			}
			accepted, err := f.pg.RunnerProxyComplete(ctx, f.server.RunnerID, request)
			if err != nil || accepted.MutationHold {
				t.Fatalf("fresh completion %+v %v", accepted, err)
			}
			before, err := f.pg.ListHostingRoutes(ctx, f.resource.ID)
			if err != nil {
				t.Fatal(err)
			}
			for {
				var expired bool
				if err = f.db.QueryRow(ctx, `SELECT $1::timestamptz<=clock_timestamp()`, expires).Scan(&expired); err != nil {
					t.Fatal(err)
				}
				if expired {
					break
				}
				select {
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-ticker.C:
				}
			}
			replayed, err := f.pg.RunnerProxyComplete(ctx, f.server.RunnerID, request)
			if err != nil || !reflect.DeepEqual(accepted.Result, replayed.Result) {
				t.Fatalf("historical replay rejected %+v %v", replayed, err)
			}
			after, err := f.pg.ListHostingRoutes(ctx, f.resource.ID)
			if err != nil {
				t.Fatal(err)
			}
			for n := range before {
				if before[n].Version != after[n].Version || before[n].SelectedReleaseOperationID != after[n].SelectedReleaseOperationID {
					t.Fatal("historical replay repeated metadata CAS")
				}
			}
		})
	}
}

func TestPostgresVerifiedSwitchOuterTransaction(t *testing.T) {
	var stop func(context.Context) error
	f := switchControlFixture(t, &stop)
	ctx := t.Context()
	if err := stop(ctx); err != nil {
		t.Fatal(err)
	}
	type effects struct {
		Counts     [5]int
		Routes     string
		Generation int64
	}
	read := func(db interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	}) effects {
		t.Helper()
		var out effects
		err := db.QueryRow(ctx, `SELECT
   (SELECT count(*) FROM hosting_proxy_operations),
   (SELECT count(*) FROM hosting_proxy_release_references),
   (SELECT count(*) FROM hosting_proxy_switch_prior_references),
   (SELECT count(*) FROM audit_events),
   (SELECT count(*) FROM river_job),
   (SELECT jsonb_agg(jsonb_build_array(id,version,selected_release_operation_id) ORDER BY id)::text FROM hosting_proxy_routes),
   (SELECT desired_generation FROM hosting_proxies WHERE id='proxy_fixture')`).Scan(&out.Counts[0], &out.Counts[1], &out.Counts[2], &out.Counts[3], &out.Counts[4], &out.Routes, &out.Generation)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	// Placement resolution precedes the ordered admission locks, as in the wrapper.
	placement, err := store.HostingRouteSwitchPlacementForTest(ctx, f.pg, f.resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	before := read(f.db)
	in := f.input("outer-switch")
	event := destinationEvent("outer-switch-audit")
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tx.Rollback(cleanup); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Error("outer transaction cleanup", err)
		}
	}()
	pending, replayed, err := store.RequestHostingRouteSwitchTxForTest(ctx, f.pg, tx, placement, f.resource.ID, "pop_outer_switch", "usr_bootstrap", in, event)
	if err != nil || replayed {
		t.Fatal("outer switch", replayed, err)
	}
	inside := read(tx)
	if inside.Counts[0] != before.Counts[0]+1 || inside.Counts[2] != before.Counts[2]+2 || inside.Counts[4] != before.Counts[4]+1 || inside.Routes == before.Routes {
		t.Fatal("tentative switch effects missing", inside, before)
	}
	if outside := read(f.db); outside != before {
		t.Fatal("uncommitted switch escaped outer transaction", outside, before)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if after := read(f.db); after != before {
		t.Fatal("outer rollback left route/ref/generation/audit/job changes", after, before)
	}
	tx, err = f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	admitted, replayed, err := store.RequestHostingRouteSwitchTxForTest(ctx, f.pg, tx, placement, f.resource.ID, pending.ID, "usr_bootstrap", in, event)
	if err != nil || replayed {
		t.Fatal("switch retry after rollback", replayed, err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	committed := read(f.db)
	if committed != inside {
		t.Fatal("committed switch differs from tentative effects", committed, inside)
	}
	replay, err := f.pg.RequestHostingRouteSwitch(ctx, f.resource.ID, "ignored-outer-switch", "usr_bootstrap", in, destinationEvent("ignored-outer-switch-audit"))
	if err != nil || replay.ID != admitted.ID || !reflect.DeepEqual(replay.Snapshot, admitted.Snapshot) {
		t.Fatal("public switch commit equivalence", err)
	}
	tx, err = f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, replayed, err = store.RequestHostingRouteSwitchTxForTest(ctx, f.pg, tx, placement, f.resource.ID, "ignored-helper-switch", "usr_bootstrap", in, destinationEvent("ignored-helper-switch-audit"))
	if err != nil || !replayed {
		t.Fatal("helper switch replay", replayed, err)
	}
	if _, err = tx.Exec(ctx, `SELECT 1`); err != nil {
		t.Fatal("helper replay closed outer transaction", err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if after := read(f.db); after != committed {
		t.Fatal("switch replay changed durable effects", after, committed)
	}
}

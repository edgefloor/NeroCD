package store_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

func connectedRouteFixture(t *testing.T, engine string) (connectionFixture, domain.HostingApplicationDatabaseRouteContinuityInput) {
	t.Helper()
	f := newConnectionFixture(t, engine).connect(t).prepareApp(t).ready(t).complete(t)
	caps := []string{domain.RunTypeProxyApply, domain.HostingProxyVerifiedSwitchCapability, domain.HostingApplicationDatabaseRouteContinuityCapability}
	if _, err := f.db.Exec(f.ctx, "UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1", f.runner, caps); err != nil {
		t.Fatal(err)
	}
	proxy := "proxy_connected_route"
	config := domain.HostingProxyInput{ControlProjectID: f.application.ProjectID, Configuration: domain.HostingProxyConfiguration{Email: "operator@nerocd.invalid", CA: "letsencrypt-staging", ManagementPort: 19080}, IdempotencyKey: "connected_proxy"}
	if _, err := f.pg.ConfigureHostingProxy(f.ctx, proxy, f.appOperation.Snapshot.ServerID, "usr_bootstrap", config, destinationEvent("connected_proxy")); err != nil {
		t.Fatal(err)
	}
	var initial string
	if err := f.db.QueryRow(f.ctx, "SELECT id FROM hosting_proxy_operations WHERE proxy_id=$1 AND generation=1", proxy).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	loaded := settleProxyGeneration(t, f.pg, initial, f.runner, f.appOperation.Release.DaemonID)
	if _, err := f.pg.GrantHostingHostname(f.ctx, proxy, "connected_hostname_grant", "usr_bootstrap", domain.HostingHostnameGrantInput{ProjectID: f.application.ProjectID, Hostname: "connected.apps.nerocd.invalid"}, destinationEvent("connected_hostname")); err != nil {
		t.Fatal(err)
	}
	in := domain.HostingApplicationDatabaseRouteContinuityInput{}
	for _, path := range []string{"/", "/api"} {
		id := "connected_route_" + domain.HostingDocumentHash(path)[7:23]
		route, err := f.pg.RequestHostingRoute(f.ctx, f.application.ID, "", id, "usr_bootstrap", domain.HostingRouteInput{Hostname: "connected.apps.nerocd.invalid", PathPrefix: path, ExpectedHealthyOperationID: f.appOperation.ID, IdempotencyKey: id}, destinationEvent(id))
		if err != nil {
			t.Fatal(err)
		}
		loaded = settleProxyGeneration(t, f.pg, id, f.runner, f.appOperation.Release.DaemonID)
		in.Routes = append(in.Routes, domain.HostingRouteSwitchSelection{RouteID: route.ID, ExpectedVersion: route.Version, ExpectedSelectedReleaseOperationID: f.appOperation.ID})
	}
	in.ExpectedLoadedGeneration, in.ExpectedLoadedProxyOperationID = loaded.Generation, loaded.ID
	canonical, err := domain.CanonicalHostingApplicationDatabaseRouteContinuityInput(in)
	if err != nil {
		t.Fatal(err)
	}
	return f, canonical
}

func connectedRouteReplace(t *testing.T, f connectionFixture, in domain.HostingApplicationDatabaseRouteContinuityInput) connectionFixture {
	t.Helper()
	request := f.input.Deployment
	request.IdempotencyKey = "connected_route_replace"
	request.ExpectedRuntimeConfigurationGeneration = f.appOperation.Snapshot.RuntimeConfiguration.Generation
	request.HostPort = 0
	request.ApplicationDatabaseRouteContinuity = &in
	op, err := f.pg.RequestHostingApplicationDatabaseDeployment(f.ctx, "connected_replace", f.application.ID, "usr_bootstrap", request, destinationEvent("connected_replace"))
	if err != nil {
		t.Fatal("connected route admission", err)
	}
	f.appOperation = awaitHostingDispatch(t, f.pg, op.ID)
	f.stage = domain.HostingApplicationDatabaseStage{}
	return f.prepareApp(t).ready(t)
}

func routeYield(f connectionFixture) domain.HostingApplicationDatabaseYieldRequest {
	return domain.HostingApplicationDatabaseYieldRequest{HostingAuthority: f.appAuthority, Version: domain.HostingApplicationDatabaseRouteContinuityVersion, ReceiptID: "yield_" + f.appOperation.ID, PlanHash: f.appOperation.Plan.Hash, StageHash: f.stage.Hash, CandidateHash: f.stage.Candidate.DocumentHash(), ContinuationHash: f.appOperation.ApplicationDatabaseRouteContinuity.Hash}
}
func assertConnectedRouteHeld(t *testing.T, f connectionFixture, previous domain.HostingOperation) {
	t.Helper()
	op, err := f.pg.GetHostingOperation(f.ctx, f.appOperation.ID)
	if err != nil || op.State != "applying" && op.State != "verifying" || !op.MutationHold || op.RunID != "run_"+op.ID {
		t.Fatal("original operation prematurely settled or reassigned", err)
	}
	view, err := f.service.GetHostingDatabaseConnection(f.ctx, f.application.ID)
	if err != nil || view.HoldOperationID != op.ID || view.MembershipRevision != op.ApplicationDatabase.MembershipRevision {
		t.Fatal("membership/DB hold changed before final settlement", err)
	}
	var healthy domain.HostingHealthy
	var raw []byte
	if err = f.db.QueryRow(f.ctx, "SELECT healthy FROM hosting_binding_state WHERE binding_id=$1", op.BindingID).Scan(&raw); err != nil || json.Unmarshal(raw, &healthy) != nil || healthy.OperationID != previous.ID {
		t.Fatal("healthy predecessor was replaced before final settlement", err)
	}
}

func TestPostgresApplicationDatabaseRouteSerialContinuation(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f, intent := connectedRouteFixture(t, engine)
			previous := f.appOperation
			options, err := f.pg.HostingRouteSwitchOptions(f.ctx, f.application.ID, "usr_bootstrap", "")
			if err != nil || !options.Available || options.LoadedProxyOperationID == nil || *options.LoadedProxyOperationID != intent.ExpectedLoadedProxyOperationID {
				t.Fatal("safe loaded predecessor projection", err)
			}
			// A missing intent retains the existing no-route admission restriction.
			ordinary := f.input.Deployment
			ordinary.IdempotencyKey = "missing_route_intent"
			ordinary.ExpectedRuntimeConfigurationGeneration = previous.Snapshot.RuntimeConfiguration.Generation
			if _, err = f.pg.RequestHostingApplicationDatabaseDeployment(f.ctx, "missing_intent", f.application.ID, "usr_bootstrap", ordinary, destinationEvent("missing_intent")); !errors.Is(err, store.ErrConflict) {
				t.Fatal("route deployment without explicit intent", err)
			}
			f = connectedRouteReplace(t, f, intent)
			assertConnectedRouteHeld(t, f, previous)
			if _, err = f.pg.HostingApplicationDatabaseRetirementPlan(f.ctx, f.runner, f.credential, f.appAuthority); err == nil {
				t.Fatal("prepare retired before strict switch")
			}
			request := routeYield(f)
			op, err := f.pg.HostingApplicationDatabaseYield(f.ctx, f.runner, f.credential, request)
			if err != nil || op.ApplicationDatabaseYield == nil || *op.ApplicationDatabaseYield != request.Receipt() {
				t.Fatal("prepare yield", err)
			}
			assertConnectedRouteHeld(t, f, previous)
			var taskState, leaseState string
			if err = f.db.QueryRow(f.ctx, "SELECT t.status,l.status FROM task_runs t JOIN run_leases l ON l.run_id=t.id WHERE l.id=$1", request.LeaseID).Scan(&taskState, &leaseState); err != nil || taskState != "succeeded" || leaseState != "succeeded" {
				t.Fatal("yield did not close only prepare task/lease", err)
			}
			if _, err = f.pg.HostingApplicationDatabaseYield(f.ctx, f.runner, f.credential, request); err != nil {
				t.Fatal("lost yield ACK historical replay", err)
			}
			changed := request
			changed.StageHash = "sha256:" + strings.Repeat("f", 64)
			if _, err = f.pg.HostingApplicationDatabaseYield(f.ctx, f.runner, f.credential, changed); err == nil {
				t.Fatal("yield receipt substitution accepted")
			}
			if _, err = f.pg.AuthorizeHostingApplicationDatabaseEffect(f.ctx, f.runner, f.credential, domain.HostingApplicationDatabaseEffectRequest{HostingAuthority: request.HostingAuthority, AuthorizationID: strings.Repeat("1", 32), Action: "retire", ExpectedStageHash: f.stage.Hash}); err == nil {
				t.Fatal("yielded original authority regained retirement")
			}
			if _, err = f.pg.CompleteLeaseRequest(f.ctx, request.LeaseID, f.runner, "succeeded", request.Attempt, request.Fence, "generic_complete", time.Now(), "succeeded", nil, nil, nil, destinationEvent("generic_complete")); err == nil {
				t.Fatal("generic completion accepted hosting task")
			}
			if err = f.pg.PrepareApplicationDatabaseRoute(f.ctx, op.ID); err != nil && !errors.Is(err, hostingjobs.ErrDatabaseConnectionWaiting) {
				t.Fatal("durable route wakeup", err)
			}
			c := op.ApplicationDatabaseRouteContinuity
			proxyAuthority, proxyOp := claimProxyPlan(t, f.pg, c.ProxyOperationID, f.runner)
			if proxyOp.Snapshot.VerifiedSwitch == nil || len(proxyOp.Snapshot.Routes) != len(intent.Routes) {
				t.Fatal("missing complete strict snapshot")
			}
			assertConnectedRouteHeld(t, f, previous)
			var finalizeCount int
			if err = f.db.QueryRow(f.ctx, "SELECT count(*) FROM hosting_task_targets WHERE operation_id=$1 AND mode='finalize'", op.ID).Scan(&finalizeCount); err != nil || finalizeCount != 0 {
				t.Fatal("finalize queued before applied switch", err)
			}
			preparation := domain.HostingProxyPreparation{DaemonID: previous.Release.DaemonID, PlanHash: proxyOp.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}
			if _, err = f.pg.RunnerProxyPrepared(f.ctx, f.runner, domain.HostingProxyPreparedRequest{HostingAuthority: proxyAuthority, ReceiptID: "prepare_" + proxyOp.ID, HostingProxyPreparation: preparation}); err != nil {
				t.Fatal("proxy preparation", err)
			}
			proxyOp, err = f.pg.RunnerProxyApplyBegin(f.ctx, f.runner, proxyAuthority)
			if err != nil {
				t.Fatal("strict candidate admission", err)
			}
			complete := domain.HostingProxyCompleteRequest{HostingAuthority: proxyAuthority, ReceiptID: "complete_" + proxyOp.ID, Result: trustedProxyResult(proxyOp, preparation, "applied")}
			if _, err = f.pg.RunnerProxyComplete(f.ctx, f.runner, complete); err != nil {
				t.Fatal("strict proxy proof", err)
			}
			if _, err = f.pg.RunnerProxyComplete(f.ctx, f.runner, complete); err != nil {
				t.Fatal("lost proxy Complete ACK", err)
			}
			assertConnectedRouteHeld(t, f, previous)
			for range 2 {
				if err = f.pg.PrepareApplicationDatabaseRoute(f.ctx, op.ID); err != nil {
					t.Fatal("finalize wakeup", err)
				}
			}
			claim, err := f.pg.ClaimRun(f.runnerContext, f.runner, time.Now(), time.Minute)
			if err != nil || claim.Run.ID != c.FinalizeRunID || claim.Run.RunSpec.HostingMode != "finalize" {
				t.Fatal("third serial claim is exact finalize task", err)
			}
			f.appAuthority = domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			f.appOperation, err = f.pg.HostingRunnerPlan(f.runnerContext, f.runner, f.appAuthority)
			if err != nil || f.appOperation.RunID != request.RunID || f.appOperation.TaskMode != "finalize" {
				t.Fatal("finalize changed original identity", err)
			}
			// Fresh finalize cannot borrow prepare/secret delivery authority.
			if _, err = f.pg.HostingApplyBegin(f.runnerContext, f.runner, f.appAuthority); err == nil {
				t.Fatal("finalize reused original apply admission")
			}
			if _, err = f.pg.AuthorizeHostingRuntimeEnvironment(f.ctx, f.runner, f.credential, domain.HostingRuntimeEnvironmentRequest{Authority: f.appAuthority, DeliveryID: strings.Repeat("2", 32), PlanHash: op.Plan.Hash, RuntimeConfiguration: op.Snapshot.RuntimeConfiguration}); err == nil {
				t.Fatal("finalize delivered secrets")
			}
			assertConnectedRouteHeld(t, f, previous)
			f = f.retire(t).complete(t)
			if f.appOperation.RunID != request.RunID || f.appOperation.TaskMode != "finalize" {
				t.Fatal("settlement replaced original RunID")
			}
			if err = f.db.QueryRow(f.ctx, "SELECT count(*) FROM hosting_task_targets WHERE operation_id=$1 AND mode='finalize'", op.ID).Scan(&finalizeCount); err != nil || finalizeCount != 1 {
				t.Fatal("duplicate finalize task", err)
			}
			if _, err = f.pg.HostingApplicationDatabaseYield(f.ctx, f.runner, f.credential, request); err != nil {
				t.Fatal("late historical yield ACK after settlement", err)
			}
		})
	}
}

func TestPostgresApplicationDatabaseRouteCutoverFailureHolds(t *testing.T) {
	for _, outcome := range []string{"refused", "rolled_back", "uncertain"} {
		t.Run(outcome, func(t *testing.T) {
			f, intent := connectedRouteFixture(t, "postgresql")
			previous := f.appOperation
			f = connectedRouteReplace(t, f, intent)
			yielded, err := f.pg.HostingApplicationDatabaseYield(f.ctx, f.runner, f.credential, routeYield(f))
			if err != nil {
				t.Fatal(err)
			}
			if err = f.pg.PrepareApplicationDatabaseRoute(f.ctx, yielded.ID); err != nil && !errors.Is(err, hostingjobs.ErrDatabaseConnectionWaiting) {
				t.Fatal(err)
			}
			a, proxy := claimProxyPlan(t, f.pg, yielded.ApplicationDatabaseRouteContinuity.ProxyOperationID, f.runner)
			prep := domain.HostingProxyPreparation{DaemonID: previous.Release.DaemonID, PlanHash: proxy.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}
			if _, err = f.pg.RunnerProxyPrepared(f.ctx, f.runner, domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "prepare_" + proxy.ID, HostingProxyPreparation: prep}); err != nil {
				t.Fatal(err)
			}
			if _, err = f.pg.RunnerProxyApplyBegin(f.ctx, f.runner, a); err != nil {
				t.Fatal(err)
			}
			result := trustedProxyResult(proxy, prep, outcome)
			if _, err = f.pg.RunnerProxyComplete(f.ctx, f.runner, domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "complete_" + proxy.ID, Result: result}); err != nil {
				t.Fatal(err)
			}
			if err = f.pg.PrepareApplicationDatabaseRoute(f.ctx, yielded.ID); err != nil {
				t.Fatal("failed switch wakeup", err)
			}
			assertConnectedRouteHeld(t, f, previous)
			var count int
			var loaded int64
			if err = f.db.QueryRow(f.ctx, "SELECT count(*) FROM hosting_task_targets WHERE operation_id=$1 AND mode='finalize'", yielded.ID).Scan(&count); err != nil || count != 0 {
				t.Fatal("failed cutover admitted finalize", err)
			}
			if err = f.db.QueryRow(f.ctx, "SELECT loaded_generation FROM hosting_proxies WHERE id=$1", yielded.ApplicationDatabaseRouteContinuity.ProxyID).Scan(&loaded); err != nil || loaded != intent.ExpectedLoadedGeneration {
				t.Fatal("failure published candidate generation", err)
			}
		})
	}
}

func TestPostgresApplicationDatabaseRouteAdmissionCAS(t *testing.T) {
	f, intent := connectedRouteFixture(t, "redis")
	for name, change := range map[string]func(*domain.HostingApplicationDatabaseRouteContinuityInput){
		"loaded_generation": func(in *domain.HostingApplicationDatabaseRouteContinuityInput) { in.ExpectedLoadedGeneration++ },
		"loaded_identity": func(in *domain.HostingApplicationDatabaseRouteContinuityInput) {
			in.ExpectedLoadedProxyOperationID = "other_proxy_operation"
		},
		"partial_routes": func(in *domain.HostingApplicationDatabaseRouteContinuityInput) { in.Routes = in.Routes[:1] },
		"route_version":  func(in *domain.HostingApplicationDatabaseRouteContinuityInput) { in.Routes[0].ExpectedVersion++ },
		"predecessor": func(in *domain.HostingApplicationDatabaseRouteContinuityInput) {
			in.Routes[0].ExpectedSelectedReleaseOperationID = "other_application"
		},
	} {
		t.Run(name, func(t *testing.T) {
			in := intent
			in.Routes = append([]domain.HostingRouteSwitchSelection(nil), intent.Routes...)
			change(&in)
			request := f.input.Deployment
			request.IdempotencyKey = "invalid_" + name
			request.ExpectedRuntimeConfigurationGeneration = f.appOperation.Snapshot.RuntimeConfiguration.Generation
			request.HostPort = 0
			request.ApplicationDatabaseRouteContinuity = &in
			before := connectionCounts(t, f)
			if _, err := f.pg.RequestHostingApplicationDatabaseDeployment(f.ctx, "invalid_"+name, f.application.ID, "usr_bootstrap", request, destinationEvent("invalid_"+name)); err == nil {
				t.Fatal("stale or partial route scope admitted")
			}
			if connectionCounts(t, f) != before {
				t.Fatal("rejected route scope mutated connection state")
			}
		})
	}
}

func TestPostgresApplicationDatabaseRouteRevocationBeforeFinalize(t *testing.T) {
	f, intent := connectedRouteFixture(t, "redis")
	previous := f.appOperation
	f = connectedRouteReplace(t, f, intent)
	yielded, err := f.pg.HostingApplicationDatabaseYield(f.ctx, f.runner, f.credential, routeYield(f))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pg.PrepareApplicationDatabaseRoute(f.ctx, yielded.ID); err != nil && !errors.Is(err, hostingjobs.ErrDatabaseConnectionWaiting) {
		t.Fatal(err)
	}
	a, proxy := claimProxyPlan(t, f.pg, yielded.ApplicationDatabaseRouteContinuity.ProxyOperationID, f.runner)
	prep := domain.HostingProxyPreparation{DaemonID: previous.Release.DaemonID, PlanHash: proxy.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}
	if _, err = f.pg.RunnerProxyPrepared(f.ctx, f.runner, domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "prepare_" + proxy.ID, HostingProxyPreparation: prep}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.RunnerProxyApplyBegin(f.ctx, f.runner, a); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.RunnerProxyComplete(f.ctx, f.runner, domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "complete_" + proxy.ID, Result: trustedProxyResult(proxy, prep, "applied")}); err != nil {
		t.Fatal(err)
	}
	if err = f.pg.PrepareApplicationDatabaseRoute(f.ctx, yielded.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(f.runnerContext, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.ID != yielded.ApplicationDatabaseRouteContinuity.FinalizeRunID {
		t.Fatal(err)
	}
	fresh := domain.HostingAuthority{OperationID: yielded.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(f.runnerContext, f.runner, fresh); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(f.ctx, "UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2", yielded.Snapshot.DestinationID, yielded.ProjectID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingApplicationDatabaseRetirementPlan(f.ctx, f.runner, f.credential, fresh); err == nil {
		t.Fatal("revoked grant retained retirement authority")
	}
	var healthy, hold string
	if err = f.db.QueryRow(f.ctx, "SELECT healthy->>'operation_id',hold_operation_id FROM hosting_binding_state WHERE binding_id=$1", yielded.BindingID).Scan(&healthy, &hold); err != nil || healthy != previous.ID || hold != yielded.ID {
		t.Fatal("revocation lost original evidence or hold", err)
	}
	var retirements int
	if err = f.db.QueryRow(f.ctx, "SELECT count(*) FROM hosting_application_database_retirements WHERE operation_id=$1", yielded.ID).Scan(&retirements); err != nil || retirements != 0 {
		t.Fatal("revoked actor wrote retirement intent", err)
	}
}

func TestPostgresApplicationDatabaseRouteExpiredFinalizeCannotReclaim(t *testing.T) {
	f, intent := connectedRouteFixture(t, "redis")
	previous := f.appOperation
	f = connectedRouteReplace(t, f, intent)
	yielded, err := f.pg.HostingApplicationDatabaseYield(f.ctx, f.runner, f.credential, routeYield(f))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pg.PrepareApplicationDatabaseRoute(f.ctx, yielded.ID); err != nil && !errors.Is(err, hostingjobs.ErrDatabaseConnectionWaiting) {
		t.Fatal(err)
	}
	a, proxy := claimProxyPlan(t, f.pg, yielded.ApplicationDatabaseRouteContinuity.ProxyOperationID, f.runner)
	prep := domain.HostingProxyPreparation{DaemonID: previous.Release.DaemonID, PlanHash: proxy.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}
	if _, err = f.pg.RunnerProxyPrepared(f.ctx, f.runner, domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "prepare_" + proxy.ID, HostingProxyPreparation: prep}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.RunnerProxyApplyBegin(f.ctx, f.runner, a); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.RunnerProxyComplete(f.ctx, f.runner, domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "complete_" + proxy.ID, Result: trustedProxyResult(proxy, prep, "applied")}); err != nil {
		t.Fatal(err)
	}
	if err = f.pg.PrepareApplicationDatabaseRoute(f.ctx, yielded.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(f.runnerContext, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.ID != yielded.ApplicationDatabaseRouteContinuity.FinalizeRunID {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(f.ctx, "UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", claim.Lease.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.pg.ExpireLeases(f.ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err = f.pg.ClaimRun(f.runnerContext, f.runner, time.Now(), time.Minute); !errors.Is(err, store.ErrNotFound) {
			t.Fatal("expired finalize automatically reclaimed", err)
		}
	}
	var attempts int
	if err = f.db.QueryRow(f.ctx, "SELECT count(*) FROM run_leases WHERE run_id=$1", claim.Run.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatal("expired finalize acquired another lease", err)
	}
	assertConnectedRouteHeld(t, f, previous)
}

package store_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
)

func TestPostgresApplicationDatabaseStorageRoutedRedeployAfterHealthyStorage(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			// Settle the real A0 -> attached-volume A bootstrap before routing A.
			f := newConnectedStorageFixture(t, engine).prepare(t).write(t, "stop_intent", nil)
			f.effect(t, "stop_predecessor")
			f = f.write(t, "stopped", nil)
			f.connectionFixture = f.connectionFixture.ready(t).retire(t).complete(t)
			previous := f.appOperation
			intent := loadedHealthyStorageRoutes(t, f.connectionFixture)
			storageOptions, err := f.service.HostingStorageDeploymentOptions(f.ctx, f.application.ID)
			if err != nil || !storageOptions.Available || storageOptions.ApplicationDatabaseStorage == nil || storageOptions.StorageRef == nil || *storageOptions.StorageRef != f.storage.Reference() {
				t.Fatal("public connected storage review unavailable", err)
			}
			routeOptions, err := f.service.HostingRouteSwitchOptions(f.ctx, f.application.ID, "")
			if err != nil || !routeOptions.Available || routeOptions.LoadedGeneration == nil || *routeOptions.LoadedGeneration != intent.ExpectedLoadedGeneration || routeOptions.LoadedProxyOperationID == nil || *routeOptions.LoadedProxyOperationID != intent.ExpectedLoadedProxyOperationID || len(routeOptions.Routes) != len(intent.Routes) {
				t.Fatal("public all-route review unavailable", err)
			}
			in := f.input.Deployment
			in.IdempotencyKey = "healthy_storage_routed_replace"
			in.HostPort = 0
			in.ExpectedRuntimeConfigurationGeneration = previous.Snapshot.RuntimeConfiguration.Generation
			in.ApplicationDatabaseRouteContinuity = &intent
			in.ApplicationDatabaseStorage = &domain.HostingApplicationDatabaseStorageInput{ExpectedStorageGeneration: storageOptions.ApplicationDatabaseStorage.ExpectedStorageGeneration, ExpectedStorageHash: storageOptions.ApplicationDatabaseStorage.ExpectedStorageHash, AcknowledgeInterruption: true}
			counts := func() string {
				var result string
				if err := f.db.QueryRow(f.ctx, `SELECT jsonb_build_array((SELECT count(*) FROM hosting_operations),(SELECT count(*) FROM hosting_task_targets),(SELECT count(*) FROM hosting_database_connection_executions),(SELECT count(*) FROM hosting_application_database_route_continuations),(SELECT count(*) FROM hosting_proxy_operations),(SELECT count(*) FROM audit_events))::text`).Scan(&result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			before := counts()
			for _, mode := range []string{"storage_hash", "storage_generation", "missing_ack", "loaded_generation", "loaded_proxy", "route_version", "route_predecessor", "missing_route"} {
				bad := in
				bad.IdempotencyKey = "stale_storage_route_" + mode
				ack := *in.ApplicationDatabaseStorage
				routes := intent
				routes.Routes = append([]domain.HostingRouteSwitchSelection(nil), intent.Routes...)
				bad.ApplicationDatabaseStorage, bad.ApplicationDatabaseRouteContinuity = &ack, &routes
				switch mode {
				case "storage_hash":
					ack.ExpectedStorageHash = domain.HostingDocumentHash("foreign storage")
				case "storage_generation":
					ack.ExpectedStorageGeneration++
				case "missing_ack":
					bad.ApplicationDatabaseStorage = nil
				case "loaded_generation":
					routes.ExpectedLoadedGeneration++
				case "loaded_proxy":
					routes.ExpectedLoadedProxyOperationID = "foreign_proxy"
				case "route_version":
					routes.Routes[0].ExpectedVersion++
				case "route_predecessor":
					routes.Routes[0].ExpectedSelectedReleaseOperationID = "foreign_predecessor"
				case "missing_route":
					routes.Routes = routes.Routes[:1]
				}
				if _, err := f.service.RequestHostingApplicationDatabaseDeployment(f.ctx, f.application.ID, bad); err == nil || counts() != before {
					t.Fatal("stale storage/route review admitted extra state", mode, err)
				}
			}
			op, err := f.service.RequestHostingApplicationDatabaseDeployment(f.ctx, f.application.ID, in)
			if err != nil {
				t.Fatal("public routed redeploy from healthy storage A", err)
			}
			op = awaitHostingDispatch(t, f.pg, op.ID)
			if op.ApplicationDatabaseRouteContinuity == nil || op.ApplicationDatabase == nil || len(op.ApplicationDatabase.Members) != 1 || op.ApplicationDatabaseRouteContinuity.PreviousHealthy.Storage == nil || op.ApplicationDatabase.Members[0].ApplicationHealthy.Storage == nil || op.ApplicationDatabaseRouteContinuity.Validate(op) != nil {
				t.Fatal("admitted storage continuation does not validate after readback")
			}
			var bindingHealthy, membershipHealthy domain.HostingHealthy
			for _, target := range []struct {
				value domain.HostingHealthy
				out   *domain.HostingHealthy
			}{{op.ApplicationDatabaseRouteContinuity.PreviousHealthy, &bindingHealthy}, {op.ApplicationDatabase.Members[0].ApplicationHealthy, &membershipHealthy}} {
				raw, err := json.Marshal(target.value)
				if err != nil || json.Unmarshal(raw, target.out) != nil {
					t.Fatal("decode independent admitted healthy evidence", err)
				}
			}
			if bindingHealthy.Storage == membershipHealthy.Storage || domain.HostingDocumentHash(bindingHealthy) != domain.HostingDocumentHash(membershipHealthy) || bindingHealthy.OperationID != previous.ID {
				t.Fatal("admission lost independently persisted healthy value identity")
			}
			after := counts()
			replayed, err := f.service.RequestHostingApplicationDatabaseDeployment(f.ctx, f.application.ID, in)
			if err != nil || replayed.ID != op.ID || counts() != after {
				t.Fatal("exact public admission replay created another operation", err)
			}
		})
	}
}

func loadedHealthyStorageRoutes(t *testing.T, f connectionFixture) domain.HostingApplicationDatabaseRouteContinuityInput {
	t.Helper()
	caps := []string{domain.RunTypeProxyApply, domain.HostingProxyVerifiedSwitchCapability, domain.HostingApplicationDatabaseRouteContinuityCapability}
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, f.runner, caps); err != nil {
		t.Fatal(err)
	}
	proxy := "proxy_healthy_storage"
	config := domain.HostingProxyInput{ControlProjectID: f.application.ProjectID, Configuration: domain.HostingProxyConfiguration{Email: "operator@nerocd.invalid", CA: "letsencrypt-staging", ManagementPort: 19080}, IdempotencyKey: "healthy_storage_proxy"}
	if _, err := f.pg.ConfigureHostingProxy(f.ctx, proxy, f.appOperation.Snapshot.ServerID, "usr_bootstrap", config, destinationEvent("healthy_storage_proxy")); err != nil {
		t.Fatal(err)
	}
	var initial string
	if err := f.db.QueryRow(f.ctx, `SELECT id FROM hosting_proxy_operations WHERE proxy_id=$1 AND generation=1`, proxy).Scan(&initial); err != nil {
		t.Fatal(err)
	}
	loaded := settleProxyGeneration(t, f.pg, initial, f.runner, f.appOperation.Release.DaemonID)
	if _, err := f.pg.GrantHostingHostname(f.ctx, proxy, "healthy_storage_hostname", "usr_bootstrap", domain.HostingHostnameGrantInput{ProjectID: f.application.ProjectID, Hostname: "storage.apps.nerocd.invalid"}, destinationEvent("healthy_storage_hostname")); err != nil {
		t.Fatal(err)
	}
	intent := domain.HostingApplicationDatabaseRouteContinuityInput{}
	for _, path := range []string{"/", "/api"} {
		id := "healthy_storage_route_" + domain.HostingDocumentHash(path)[7:23]
		route, err := f.pg.RequestHostingRoute(f.ctx, f.application.ID, "", id, "usr_bootstrap", domain.HostingRouteInput{Hostname: "storage.apps.nerocd.invalid", PathPrefix: path, ExpectedHealthyOperationID: f.appOperation.ID, IdempotencyKey: id}, destinationEvent(id))
		if err != nil {
			t.Fatal(err)
		}
		loaded = settleProxyGeneration(t, f.pg, id, f.runner, f.appOperation.Release.DaemonID)
		intent.Routes = append(intent.Routes, domain.HostingRouteSwitchSelection{RouteID: route.ID, ExpectedVersion: route.Version, ExpectedSelectedReleaseOperationID: f.appOperation.ID})
	}
	intent.ExpectedLoadedGeneration, intent.ExpectedLoadedProxyOperationID = loaded.Generation, loaded.ID
	intent, err := domain.CanonicalHostingApplicationDatabaseRouteContinuityInput(intent)
	if err != nil {
		t.Fatal(err)
	}
	return intent
}

func TestPostgresApplicationDatabaseStorageFailedProxyForwardRecovery(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f, in, op, yielded := readyRoutedStorageFixture(t, engine)
			err := error(nil)
			c := yielded.ApplicationDatabaseRouteContinuity
			a, p := claimConnectedStorageProxyPlan(t, f, c.ProxyOperationID)
			if p.Snapshot.StorageSwitch == nil || p.Snapshot.StorageSwitch.Action != "switch" || p.Snapshot.StorageSwitch.WriterStateHash != f.writer.Receipt.Hash {
				t.Fatal("initial P lacks stopped writer contract")
			}
			preparation := domain.HostingProxyPreparation{DaemonID: f.appOperation.Release.DaemonID, PlanHash: p.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}
			if _, err = f.pg.RunnerProxyPrepared(f.ctx, f.runner, domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "prepared_" + p.ID, HostingProxyPreparation: preparation}); err != nil {
				t.Fatal(err)
			}
			p, err = f.pg.RunnerProxyApplyBegin(f.ctx, f.runner, a)
			if err != nil {
				t.Fatal("initial storage P admission", err)
			}
			if _, err = f.pg.HostingApplicationDatabaseStorageProxyPlan(f.ctx, f.runner, f.credential, a); err != nil {
				t.Fatal("fresh proxy writer reinspection authority", err)
			}
			refusal := domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "refused_" + p.ID, Result: domain.HostingProxyResult{Outcome: "refused", PlanHash: p.Plan.Hash, EngineHash: preparation.EngineHash, DaemonID: preparation.DaemonID, LoadedGeneration: p.Plan.PreviousGeneration}}
			failed, err := f.pg.RunnerProxyComplete(f.ctx, f.runner, refusal)
			if err != nil || failed.State != "refused" {
				t.Fatal("closed storage P refusal", err)
			}
			current, err := f.pg.GetHostingOperation(f.ctx, op.ID)
			if err != nil || current.ApplicationDatabaseStorageState == nil || len(current.ApplicationDatabaseStorageState.RecoveryActions) != 1 || current.ApplicationDatabaseStorageState.RecoveryActions[0] != "complete_replacement" {
				t.Fatal("missing explicit forward recovery offer", err)
			}
			recovery := domain.HostingDatabaseConnectionRecoveryInput{OperationID: op.ID, Action: "complete_replacement", StageHash: f.stage.Hash, CandidateHash: f.stage.Candidate.Hash, ExpectedMembershipRevision: 2, IdempotencyKey: "complete_storage_replacement", StorageStateHash: f.writer.Receipt.Hash, ApplicationDatabaseStorage: in.ApplicationDatabaseStorage}
			for _, mutate := range []func(*domain.HostingDatabaseConnectionRecoveryInput){
				func(in *domain.HostingDatabaseConnectionRecoveryInput) {
					in.StorageStateHash = "sha256:" + strings.Repeat("0", 64)
				},
				func(in *domain.HostingDatabaseConnectionRecoveryInput) {
					in.StageHash = "sha256:" + strings.Repeat("0", 64)
				},
				func(in *domain.HostingDatabaseConnectionRecoveryInput) {
					in.CandidateHash = "sha256:" + strings.Repeat("0", 64)
				},
				func(in *domain.HostingDatabaseConnectionRecoveryInput) { in.ExpectedMembershipRevision++ },
			} {
				wrong := recovery
				mutate(&wrong)
				if _, err := f.service.RequestHostingDatabaseConnectionRecovery(f.ctx, f.application.ID, wrong); err == nil {
					t.Fatal("stale recovery correlation admitted")
				}
				var count int
				if err := f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_application_database_storage_recoveries WHERE operation_id=$1`, op.ID).Scan(&count); err != nil || count != 0 {
					t.Fatal("rejected recovery mutated association", err)
				}
			}
			admitted, err := f.service.RequestHostingDatabaseConnectionRecovery(f.ctx, f.application.ID, recovery)
			if err != nil || admitted.ApplicationDatabaseStorageState.RecoveryAction != "complete_replacement" || admitted.ApplicationDatabaseStorageState.RecoveryState != "queued" {
				t.Fatal("forward recovery admission", err)
			}
			id := "appdb_storage_proxy_" + op.ID
			a2, p2 := claimConnectedStorageProxyPlan(t, f, id)
			var associatedRun string
			if err = f.db.QueryRow(f.ctx, `SELECT run_id FROM hosting_application_database_storage_recoveries WHERE operation_id=$1`, op.ID).Scan(&associatedRun); err != nil || associatedRun != a2.RunID {
				t.Fatal("P2 association does not identify actual immutable dispatched task", err)
			}
			progress, err := f.pg.GetHostingOperation(f.ctx, op.ID)
			if err != nil || progress.ApplicationDatabaseStorageState.RecoveryState != "running" {
				t.Fatal("claimed P2 is not projected running", err)
			}
			if a2.RunID == a.RunID || a2.LeaseID == a.LeaseID || p2.Snapshot.StorageSwitch == nil || p2.Snapshot.StorageSwitch.FailedProxyOperationID != p.ID || p2.Snapshot.StorageSwitch.Action != "complete_replacement" {
				t.Fatal("P2 reused failed authority or proof")
			}
			preparation.PlanHash = p2.Plan.Hash
			if _, err = f.pg.RunnerProxyPrepared(f.ctx, f.runner, domain.HostingProxyPreparedRequest{HostingAuthority: a2, ReceiptID: "prepared_" + p2.ID, HostingProxyPreparation: preparation}); err != nil {
				t.Fatal(err)
			}
			p2, err = f.pg.RunnerProxyApplyBegin(f.ctx, f.runner, a2)
			if err != nil {
				t.Fatal("fresh P2 apply admission", err)
			}
			if _, err = f.pg.HostingApplicationDatabaseStorageProxyPlan(f.ctx, f.runner, f.credential, a2); err != nil {
				t.Fatal("fresh P2 writer authority", err)
			}
			complete := domain.HostingProxyCompleteRequest{HostingAuthority: a2, ReceiptID: "complete_" + p2.ID, Result: trustedProxyResult(p2, preparation, "applied")}
			if _, err = f.pg.RunnerProxyComplete(f.ctx, f.runner, complete); err != nil {
				t.Fatal("P2 strict success", err)
			}
			if err = f.pg.PrepareApplicationDatabaseRoute(f.ctx, op.ID); err != nil {
				t.Fatal("P2 finalize wakeup", err)
			}
			claim, err := f.pg.ClaimRun(f.runnerContext, f.runner, time.Now(), time.Minute)
			if err != nil || claim.Run.ID != c.FinalizeRunID {
				t.Fatal("same original finalize", err)
			}
			f.appAuthority = domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			f.appOperation, err = f.pg.HostingRunnerPlan(f.runnerContext, f.runner, f.appAuthority)
			if err != nil || f.appOperation.RunID != yielded.RunID {
				t.Fatal("finalize consumed wrong association", err)
			}
			f.connectionFixture = f.connectionFixture.retire(t).complete(t)
			final, err := f.pg.GetHostingOperation(f.ctx, op.ID)
			if err != nil || final.ApplicationDatabaseStorageState.RecoveryState != "succeeded" || final.ApplicationDatabaseRouteContinuity.ProxyOperationID != p.ID {
				t.Fatal("recovery rewrote original P or lacks final settlement", err)
			}
		})
	}
}

func claimConnectedStorageProxyPlan(t *testing.T, f connectedStorageFixture, id string) (domain.HostingAuthority, domain.HostingProxyOperation) {
	t.Helper()
	awaitProxyDispatch(t, f.pg, id)
	if _, err := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute); err == nil {
		t.Fatal("composed proxy claim omitted authenticated fingerprint")
	}
	claim, err := f.pg.ClaimRun(f.runnerContext, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.ProxyOperationID != id {
		t.Fatal("composed proxy claim", err)
	}
	a := domain.HostingAuthority{OperationID: id, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	op, err := f.pg.RunnerProxyPlan(f.ctx, f.runner, a)
	if err != nil {
		t.Fatal(err)
	}
	return a, op
}

func readyRoutedStorageFixture(t *testing.T, engine string) (connectedStorageFixture, domain.HostingDeploymentInput, domain.HostingOperation, domain.HostingOperation) {
	t.Helper()
	base, intent := connectedRouteFixture(t, engine)
	if _, err := base.db.Exec(base.ctx, `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, base.runner, []string{domain.HostingApplicationDatabaseStorageCapability, domain.HostingOwnedStorageCapability}); err != nil {
		t.Fatal(err)
	}
	storage := saveHostingStorage(t, base.pg, base.application.ID, hostingStorageInput())
	in := base.input.Deployment
	in.IdempotencyKey = "storage_routed_replace"
	in.HostPort = 0
	in.ExpectedRuntimeConfigurationGeneration = 1
	in.ApplicationDatabaseRouteContinuity = &intent
	in.ApplicationDatabaseStorage = &domain.HostingApplicationDatabaseStorageInput{ExpectedStorageGeneration: storage.Generation, ExpectedStorageHash: storage.Hash, AcknowledgeInterruption: true}
	op, err := base.service.RequestHostingApplicationDatabaseDeployment(base.ctx, base.application.ID, in)
	if err != nil {
		t.Fatal("routed storage admission", err)
	}
	base.appOperation = awaitHostingDispatch(t, base.pg, op.ID)
	base.stage = domain.HostingApplicationDatabaseStage{}
	f := connectedStorageFixture{connectionFixture: base, storage: storage}
	f = f.prepare(t).write(t, "stop_intent", nil)
	f.effect(t, "stop_predecessor")
	f = f.write(t, "stopped", nil)
	f.connectionFixture = f.connectionFixture.ready(t)
	yielded, err := f.pg.HostingApplicationDatabaseYield(f.ctx, f.runner, f.credential, routeYield(f.connectionFixture))
	if err != nil {
		t.Fatal("storage yield", err)
	}
	if err = f.pg.PrepareApplicationDatabaseRoute(f.ctx, op.ID); err != nil && !errors.Is(err, hostingjobs.ErrDatabaseConnectionWaiting) {
		t.Fatal("storage P wakeup", err)
	}

	return f, in, op, yielded
}

func TestPostgresApplicationDatabaseStorageUnknownProxyRetainsHolds(t *testing.T) {
	for _, mode := range []string{"active", "uncertain", "route_drift", "expired_recovery"} {
		t.Run(mode, func(t *testing.T) {
			f, in, op, yielded := readyRoutedStorageFixture(t, "redis")
			c := yielded.ApplicationDatabaseRouteContinuity
			a, p := claimConnectedStorageProxyPlan(t, f, c.ProxyOperationID)
			prep := domain.HostingProxyPreparation{DaemonID: f.appOperation.Release.DaemonID, PlanHash: p.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}
			if _, err := f.pg.RunnerProxyPrepared(f.ctx, f.runner, domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "prepared_" + p.ID, HostingProxyPreparation: prep}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.pg.RunnerProxyApplyBegin(f.ctx, f.runner, a); err != nil {
				t.Fatal(err)
			}
			if mode != "active" {
				outcome := "uncertain"
				if mode == "route_drift" || mode == "expired_recovery" {
					outcome = "refused"
				}
				result := domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "result_" + p.ID, Result: domain.HostingProxyResult{Outcome: outcome, PlanHash: p.Plan.Hash, EngineHash: prep.EngineHash, DaemonID: prep.DaemonID, LoadedGeneration: p.Plan.PreviousGeneration}}
				if _, err := f.pg.RunnerProxyComplete(f.ctx, f.runner, result); err != nil {
					t.Fatal(err)
				}
				if mode == "route_drift" {
					if _, err := f.db.Exec(f.ctx, `UPDATE hosting_proxy_routes SET version=version+1 WHERE id=$1`, c.Request.Routes[0].RouteID); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "expired_recovery" {
				request := domain.HostingDatabaseConnectionRecoveryInput{OperationID: op.ID, Action: "complete_replacement", StageHash: f.stage.Hash, CandidateHash: f.stage.Candidate.Hash, ExpectedMembershipRevision: 2, IdempotencyKey: "expired_recovery", StorageStateHash: f.writer.Receipt.Hash, ApplicationDatabaseStorage: in.ApplicationDatabaseStorage}
				if _, err := f.service.RequestHostingDatabaseConnectionRecovery(f.ctx, f.application.ID, request); err != nil {
					t.Fatal(err)
				}
				fresh, recoveryProxy := claimConnectedStorageProxyPlan(t, f, "appdb_storage_proxy_"+op.ID)
				prep.PlanHash = recoveryProxy.Plan.Hash
				if _, err := f.pg.RunnerProxyPrepared(f.ctx, f.runner, domain.HostingProxyPreparedRequest{HostingAuthority: fresh, ReceiptID: "prepared_" + recoveryProxy.ID, HostingProxyPreparation: prep}); err != nil {
					t.Fatal(err)
				}
				if _, err := f.pg.RunnerProxyApplyBegin(f.ctx, f.runner, fresh); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.Exec(f.ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, fresh.LeaseID); err != nil {
					t.Fatal(err)
				}
				if _, err := f.pg.HostingApplicationDatabaseStorageProxyPlan(f.ctx, f.runner, f.credential, fresh); err == nil {
					t.Fatal("expired fresh proxy lease authorized writer inspection")
				}
			}
			view, err := f.pg.GetHostingOperation(f.ctx, op.ID)
			if err != nil || !view.MutationHold || len(view.ApplicationDatabaseStorageState.RecoveryActions) != 0 {
				t.Fatal("uncertain/drifted switch offered recovery or released hold", err)
			}
			request := domain.HostingDatabaseConnectionRecoveryInput{OperationID: op.ID, Action: "complete_replacement", StageHash: f.stage.Hash, CandidateHash: f.stage.Candidate.Hash, ExpectedMembershipRevision: 2, IdempotencyKey: "invalid_storage_recovery", StorageStateHash: f.writer.Receipt.Hash, ApplicationDatabaseStorage: in.ApplicationDatabaseStorage}
			if _, err = f.service.RequestHostingDatabaseConnectionRecovery(f.ctx, f.application.ID, request); err == nil {
				t.Fatal("unknown switch authorized forward recovery")
			}
			var associations int
			wantAssociations := 0
			if mode == "expired_recovery" {
				wantAssociations = 1
				if view.ApplicationDatabaseStorageState.RecoveryState != "uncertain" {
					t.Fatal("expired recovery did not retain uncertainty")
				}
			}
			if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_application_database_storage_recoveries WHERE operation_id=$1`, op.ID).Scan(&associations); err != nil || associations != wantAssociations {
				t.Fatal("rejected recovery changed association", err)
			}
		})
	}
}

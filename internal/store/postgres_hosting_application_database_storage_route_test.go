package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
)

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

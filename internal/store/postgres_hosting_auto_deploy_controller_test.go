package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func enabledAutomaticFixture(t *testing.T) automaticStoreFixture {
	t.Helper()
	f := newAutomaticStoreFixture(t)
	var err error
	f.policy, err = f.pg.ConfigureGitHubPushPolicy(t.Context(), "usr_bootstrap", f.src.ResourceID, f.configuration(), destinationEvent("automatic-enabled-review"))
	if err != nil {
		t.Fatal(err)
	}
	f.enable(t)
	return f
}
func (f automaticStoreFixture) push(t *testing.T, n int, commit string) domain.GitHubPushPolicy {
	t.Helper()
	a := f.accept(t, n, commit)
	if err := f.pg.CommitGitHubPushDelivery(t.Context(), a, pushEvidence(a.Policy, commit), ""); err != nil {
		t.Fatal(err)
	}
	p, err := f.pg.GetGitHubPushPolicy(t.Context(), "usr_bootstrap", f.src.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func (f automaticStoreFixture) finishBuild(t *testing.T, id, commit string) domain.HostingBuild {
	t.Helper()
	ctx := t.Context()
	audit := destinationEvent("dispatch_" + id)
	audit.ActorID = "push_bot"
	b, err := f.pg.DispatchHostingBuild(ctx, id, buildDispatch, audit)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	a := domain.HostingBuildAuthority{BuildID: id, RunID: b.RunID, LeaseID: "lease_" + id, Attempt: 1, Fence: "fence_" + id}
	if _, err = f.db.Exec(ctx, `UPDATE task_runs SET status='running',runner_id=$2 WHERE id=$1`, b.RunID, f.runner); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `INSERT INTO run_leases(id,run_id,runner_id,status,expires_at,created_at,attempt,fence) VALUES($1,$2,$3,'active',clock_timestamp()+interval '1 hour',clock_timestamp(),$4,$5)`, a.LeaseID, a.RunID, f.runner, a.Attempt, a.Fence); err != nil {
		t.Fatal(err)
	}
	b, err = f.pg.BeginHostingBuildSource(ctx, f.runner, a, b.Revision)
	if err != nil {
		t.Fatalf("begin source: %v", err)
	}
	r := sourceBuildReceipt(b)
	r.Commit = commit
	b, err = f.pg.CommitHostingBuildSource(ctx, f.runner, a, "source_"+id, r)
	if err != nil {
		t.Fatalf("finish build at %s: %v", b.State, err)
	}
	b, err = f.pg.PrepareHostingBuild(ctx, f.runner, a, b.Revision, "local-daemon")
	if err != nil {
		t.Fatalf("finish build at %s: %v", b.State, err)
	}
	artifact := buildArtifact(b)
	b, err = f.pg.CompleteHostingBuild(ctx, f.runner, a, "artifact_"+id, b.Revision, store.HostingBuildCompletion{State: "succeeded", Artifact: &artifact})
	if err != nil {
		t.Fatalf("finish build at %s: %v", b.State, err)
	}
	return b
}
func (f automaticStoreFixture) prepareAutomaticDeployment(t *testing.T, id string) (domain.HostingAuthority, domain.HostingOperation) {
	t.Helper()
	ctx := t.Context()
	if err := f.pg.PrepareDeployment(ctx, id); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: id, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	op, err := f.pg.HostingRunnerPlan(ctx, f.runner, a)
	if err != nil {
		t.Fatal(err)
	}
	release, err := domain.NewHostingLocalBuildRelease(op.Snapshot.LocalBuild.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	op, err = f.pg.HostingPrepared(ctx, f.runner, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "prepared_" + id, Release: release})
	if err != nil {
		t.Fatal(err)
	}
	return a, op
}
func (f automaticStoreFixture) status(t *testing.T) *domain.HostingAutoDeployStatus {
	t.Helper()
	p, err := f.pg.GetGitHubPushPolicy(t.Context(), "usr_bootstrap", f.src.ResourceID)
	if err != nil {
		t.Fatal(err)
	}
	return p.AutomaticDeployment
}
func TestPostgresAutomaticDeploymentPipeline(t *testing.T) {
	f := enabledAutomaticFixture(t)
	ctx := t.Context()
	commit := strings.Repeat("b", 40)
	p := f.push(t, 110, commit)
	if p.AutomaticDeployment.State != "waiting_build" {
		t.Fatal(p.AutomaticDeployment)
	}
	f.finishBuild(t, p.LastBuildID, commit)
	if err := f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID); err != nil {
		t.Fatal(err)
	}
	s := f.status(t)
	if s.State != "waiting_deployment" {
		t.Fatal(s)
	}
	a, op := f.prepareAutomaticDeployment(t, s.DeploymentOperationID)
	if op.Snapshot.HostPort == f.bootstrap.Snapshot.HostPort {
		t.Fatal("automatic release reused live port")
	}
	op, err := f.pg.HostingApplyBegin(ctx, f.runner, a)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := f.pg.HostingComplete(ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "complete_" + op.ID, Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: strings.Repeat("c", 64), ImageID: op.Release.ImageID, DaemonID: op.Release.DaemonID, PlanHash: op.Plan.Hash}})
	if err != nil {
		t.Fatal(err)
	}
	if completed.State != "succeeded" {
		t.Fatal(completed.State)
	}
	if err = f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID); err != nil {
		t.Fatal(err)
	}
	s = f.status(t)
	if s.State != "waiting_switch" {
		t.Fatal(s)
	}
	if err = f.pg.PrepareProxy(ctx, s.SwitchOperationID); err != nil {
		t.Fatal(err)
	}
	at, proxy, prepared := admitSwitchOperation(t, switchControl{pg: f.pg, server: domain.Server{RunnerID: f.runner}, a: f.bootstrap}, s.SwitchOperationID)
	if _, err = f.pg.RunnerProxyComplete(ctx, f.runner, domain.HostingProxyCompleteRequest{HostingAuthority: at, ReceiptID: "complete_" + proxy.ID, Result: trustedProxyResult(proxy, prepared, "applied")}); err != nil {
		t.Fatal(err)
	}
	if err = f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID); err != nil {
		t.Fatal(err)
	}
	s = f.status(t)
	if s.State != "succeeded" || s.Reason != "https_verified" || s.ObservedLoadedGeneration != proxy.Generation {
		t.Fatal(s)
	}
	var origins int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_auto_deploy_origins WHERE resource_id=$1`, f.src.ResourceID).Scan(&origins); err != nil || origins != 2 {
		t.Fatal(origins, err)
	}
	var epoch int64
	if err = f.db.QueryRow(ctx, `SELECT manual_epoch FROM hosting_auto_deploy_controls WHERE resource_id=$1`, f.src.ResourceID).Scan(&epoch); err != nil || epoch != 0 {
		t.Fatal("automatic admission changed manual epoch", epoch, err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE hosting_auto_deploy_origins SET body=body WHERE resource_id=$1`, f.src.ResourceID); err == nil {
		t.Fatal("origin mutable")
	}
}
func TestPostgresAutomaticDeploymentSupersededBeforeFirstMutation(t *testing.T) {
	f := enabledAutomaticFixture(t)
	ctx := t.Context()
	p := f.push(t, 120, strings.Repeat("b", 40))
	f.finishBuild(t, p.LastBuildID, strings.Repeat("b", 40))
	if err := f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID); err != nil {
		t.Fatal(err)
	}
	s := f.status(t)
	a, _ := f.prepareAutomaticDeployment(t, s.DeploymentOperationID)
	newer := f.push(t, 121, strings.Repeat("c", 40))
	if _, err := f.pg.HostingApplyBegin(ctx, f.runner, a); !errors.Is(err, store.ErrConflict) {
		t.Fatal("old desired admitted", err)
	}
	if err := f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID); err != nil {
		t.Fatal(err)
	}
	var state string
	var hold bool
	if err := f.db.QueryRow(ctx, `SELECT state,EXISTS(SELECT 1 FROM hosting_apply_admissions WHERE operation_id=$1) FROM hosting_operations WHERE id=$1`, a.OperationID).Scan(&state, &hold); err != nil || state != "canceled" || hold {
		t.Fatal("nonadmitted cancellation", state, hold, err)
	}
	if err := f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID); err != nil {
		t.Fatal("cleanup replay", err)
	}
	if f.status(t).BuildID != newer.LastBuildID {
		t.Fatal("cleanup overwrote current demand")
	}
	var body json.RawMessage
	if err := f.db.QueryRow(ctx, `SELECT body FROM hosting_auto_deploy_origins WHERE operation_id=$1`, a.OperationID).Scan(&body); err != nil || !strings.Contains(string(body), p.LastBuildID) {
		t.Fatal("old provenance erased", err)
	}
}

func TestPostgresAutomaticDeploymentCurrentAuthorityBeforeFirstMutation(t *testing.T) {
	for _, tc := range []struct{ name, sql string }{
		{"actor", `UPDATE service_accounts SET status='revoked',revoked_at=clock_timestamp() WHERE id='push_bot'`},
		{"owner", `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`},
		{"secret", `UPDATE project_managed_secrets SET status='revoked',revoked_at=clock_timestamp() WHERE id='push_signing'`},
		{"capability", `UPDATE runners SET capabilities=ARRAY['hosting_build_v1']::text[] WHERE id='runner_build'`},
		{"enrollment", `UPDATE runner_enrollments SET revoked_at=clock_timestamp() WHERE runner_id='runner_build'`},
		{"resource_generation", `UPDATE resources SET generation=generation+1 WHERE id=(SELECT resource_id FROM hosting_auto_deploy_controls)`},
		{"manual_epoch", `UPDATE hosting_auto_deploy_controls SET manual_epoch=manual_epoch+1`},
		{"deadline", `UPDATE hosting_auto_deploy_demands SET expires_at=created_at+interval '1 microsecond',body=jsonb_set(body,'{expires_at}',to_jsonb(created_at+interval '1 microsecond'))`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := enabledAutomaticFixture(t)
			ctx := t.Context()
			p := f.push(t, 130, strings.Repeat("b", 40))
			f.finishBuild(t, p.LastBuildID, strings.Repeat("b", 40))
			if err := f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID); err != nil {
				t.Fatal(err)
			}
			a, _ := f.prepareAutomaticDeployment(t, f.status(t).DeploymentOperationID)
			if tag, err := f.db.Exec(ctx, tc.sql); err != nil || tag.RowsAffected() != 1 {
				t.Fatal("fixture must change exactly one authority row", tag.RowsAffected(), err)
			}
			if _, err := f.pg.HostingApplyBegin(ctx, f.runner, a); err == nil {
				t.Fatal("changed authority admitted")
			}
			var admitted bool
			if err := f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM hosting_apply_admissions WHERE operation_id=$1)`, a.OperationID).Scan(&admitted); err != nil || admitted {
				t.Fatal(admitted, err)
			}
		})
	}
}

func TestPostgresAutomaticDeploymentDeadlineAfterAuditWait(t *testing.T) {
	for _, phase := range []string{"admission", "first_apply"} {
		t.Run(phase, func(t *testing.T) {
			f := enabledAutomaticFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			p := f.push(t, 140, strings.Repeat("b", 40))
			f.finishBuild(t, p.LastBuildID, strings.Repeat("b", 40))
			id := f.status(t).DeploymentOperationID
			var authority domain.HostingAuthority
			action := "hosting.build_deployment_requested"
			if phase == "first_apply" {
				if err := f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID); err != nil {
					t.Fatal(err)
				}
				authority, _ = f.prepareAutomaticDeployment(t, id)
				action = "hosting.apply_admitted"
			}
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				cleanup, stop := context.WithTimeout(context.Background(), 2*time.Second)
				defer stop()
				if e := blocker.Rollback(cleanup); e != nil && !errors.Is(e, pgx.ErrTxClosed) {
					t.Error(e)
				}
			}()
			var pid int
			if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_schema()||':auto-test-audit',0))`); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(ctx, `CREATE FUNCTION auto_audit_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action=TG_ARGV[0] THEN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':auto-test-audit',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER auto_audit_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION auto_audit_wait('`+action+`')`); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Exec(ctx, `WITH stamp AS (SELECT clock_timestamp()+interval '700 milliseconds' AS deadline) UPDATE hosting_auto_deploy_demands SET expires_at=stamp.deadline,body=jsonb_set(body,'{expires_at}',to_jsonb(stamp.deadline)) FROM stamp`); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				if phase == "admission" {
					done <- f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID)
				} else {
					_, e := f.pg.HostingApplyBegin(ctx, f.runner, authority)
					done <- e
				}
			}()
			waiting := false
			for !waiting && ctx.Err() == nil {
				if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%CreateAuditEvent%')`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if !waiting {
					time.Sleep(5 * time.Millisecond)
				}
			}
			if !waiting {
				t.Fatal("missing audit blocking boundary")
			}
			expired := false
			for !expired && ctx.Err() == nil {
				if err = f.db.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() FROM hosting_auto_deploy_demands WHERE resource_id=$1`, f.src.ResourceID).Scan(&expired); err != nil {
					t.Fatal(err)
				}
				if !expired {
					time.Sleep(5 * time.Millisecond)
				}
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-done; !errors.Is(err, store.ErrConflict) {
				t.Fatal("expired transaction admitted", err)
			}
			var accepted int
			if err = f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_apply_admissions WHERE operation_id=$1`, id).Scan(&accepted); err != nil || accepted != 0 {
				t.Fatal(accepted, err)
			}
			if phase == "admission" {
				if err = f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_operations WHERE id=$1)+(SELECT count(*) FROM hosting_auto_deploy_origins WHERE operation_id=$1)+(SELECT count(*) FROM river_job WHERE args->>'operation_id'=$1)`, id).Scan(&accepted); err != nil || accepted != 0 {
					t.Fatal("partial admission committed", accepted, err)
				}
			}
		})
	}
}

func (f automaticStoreFixture) settleAutomaticDeployment(t *testing.T, id string) domain.HostingOperation {
	t.Helper()
	a, _ := f.prepareAutomaticDeployment(t, id)
	op, err := f.pg.HostingApplyBegin(t.Context(), f.runner, a)
	if err != nil {
		t.Fatal(err)
	}
	op, err = f.pg.HostingComplete(t.Context(), f.runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "complete_" + id, Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: strings.Repeat("c", 64), ImageID: op.Release.ImageID, DaemonID: op.Release.DaemonID, PlanHash: op.Plan.Hash}})
	if err != nil {
		t.Fatal(err)
	}
	return op
}
func (f automaticStoreFixture) pendingAutomaticSwitch(t *testing.T) string {
	t.Helper()
	ctx := t.Context()
	p := f.push(t, 150, strings.Repeat("b", 40))
	f.finishBuild(t, p.LastBuildID, strings.Repeat("b", 40))
	if err := f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID); err != nil {
		t.Fatal(err)
	}
	f.settleAutomaticDeployment(t, f.status(t).DeploymentOperationID)
	if err := f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID); err != nil {
		t.Fatal(err)
	}
	id := f.status(t).SwitchOperationID
	if err := f.pg.PrepareProxy(ctx, id); err != nil {
		t.Fatal(err)
	}
	return id
}
func TestPostgresAutomaticDeploymentOlderAdmittedSwitch(t *testing.T) {
	f := enabledAutomaticFixture(t)
	ctx := t.Context()
	id := f.pendingAutomaticSwitch(t)
	a, op, prepared := admitSwitchOperation(t, switchControl{pg: f.pg, server: domain.Server{RunnerID: f.runner}, a: f.bootstrap}, id)
	newer := f.push(t, 151, strings.Repeat("c", 40))
	// Exact apply replay remains the old admitted operation after supersession.
	if replay, err := f.pg.RunnerProxyApplyBegin(ctx, f.runner, a); err != nil || replay.ID != op.ID {
		t.Fatal("admitted replay lost", err)
	}
	if _, err := f.pg.RunnerProxyComplete(ctx, f.runner, domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "complete_" + id, Result: trustedProxyResult(op, prepared, "applied")}); err != nil {
		t.Fatal(err)
	}
	if err := f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID); err != nil {
		t.Fatal(err)
	}
	s := f.status(t)
	if s.State != "waiting_build" || s.BuildID != newer.LastBuildID || s.DesiredCommit != strings.Repeat("c", 40) || s.ObservedLoadedGeneration != 0 {
		t.Fatal("older receipt became newer success", s)
	}
	var baseline string
	if err := f.db.QueryRow(ctx, `SELECT body->>'baseline_operation_id' FROM hosting_auto_deploy_controls WHERE resource_id=$1`, f.src.ResourceID).Scan(&baseline); err != nil || baseline != id {
		t.Fatal("exact admitted origin did not advance accepted baseline", baseline, err)
	}
	f.finishBuild(t, newer.LastBuildID, strings.Repeat("c", 40))
	if err := f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID); err != nil {
		t.Fatal(err)
	}
	if f.status(t).State != "waiting_deployment" {
		t.Fatal("new demand could not use accepted predecessor", f.status(t))
	}
}
func TestPostgresAutomaticDeploymentSwitchSupersession(t *testing.T) {
	f := enabledAutomaticFixture(t)
	ctx := t.Context()
	id := f.pendingAutomaticSwitch(t)
	a, op := claimProxyPlan(t, f.pg, id, f.runner)
	prepared := domain.HostingProxyPreparation{DaemonID: f.bootstrap.Release.DaemonID, PlanHash: op.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}
	if _, err := f.pg.RunnerProxyPrepared(ctx, f.runner, domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "prepared_" + id, HostingProxyPreparation: prepared}); err != nil {
		t.Fatal(err)
	}
	f.push(t, 152, strings.Repeat("c", 40))
	if _, err := f.pg.RunnerProxyApplyBegin(ctx, f.runner, a); !errors.Is(err, store.ErrConflict) {
		t.Fatal("superseded switch admitted", err)
	}
	if err := f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID); err != nil {
		t.Fatal(err)
	}
	var state string
	var admitted bool
	if err := f.db.QueryRow(ctx, `SELECT state,EXISTS(SELECT 1 FROM hosting_proxy_admissions WHERE operation_id=$1) FROM hosting_proxy_operations WHERE id=$1`, id).Scan(&state, &admitted); err != nil || state != "canceled" || admitted {
		t.Fatal(state, admitted, err)
	}
	for _, route := range f.routes {
		var release string
		if err := f.db.QueryRow(ctx, `SELECT selected_release_operation_id FROM hosting_proxy_routes WHERE id=$1`, route.ID).Scan(&release); err != nil || release != f.bootstrap.ID {
			t.Fatal("supersession did not CAS restore prior metadata", release, err)
		}
	}
	if err := f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID); err != nil {
		t.Fatal("cleanup replay", err)
	}
}
func TestPostgresAutomaticDeploymentRecoveryWithoutWakeup(t *testing.T) {
	f := enabledAutomaticFixture(t)
	ctx := t.Context()
	p := f.push(t, 153, strings.Repeat("b", 40))
	f.finishBuild(t, p.LastBuildID, strings.Repeat("b", 40))
	if _, err := f.db.Exec(ctx, `DELETE FROM river_job WHERE kind='hosting.auto_deploy.v1'`); err != nil {
		t.Fatal(err)
	}
	stop, err := f.pg.StartHostingExecution(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e := stop(cleanup); e != nil {
			t.Error(e)
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if f.status(t).State == "waiting_deployment" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("durable demand stranded after lost opaque wakeup")
}

func TestPostgresAutomaticDeploymentVersionChangesInvalidateWithoutEpochWrites(t *testing.T) {
	for _, kind := range []string{"source", "runtime"} {
		t.Run(kind, func(t *testing.T) {
			f := enabledAutomaticFixture(t)
			ctx := t.Context()
			p := f.push(t, 160, strings.Repeat("b", 40))
			f.finishBuild(t, p.LastBuildID, strings.Repeat("b", 40))
			if err := f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID); err != nil {
				t.Fatal(err)
			}
			a, _ := f.prepareAutomaticDeployment(t, f.status(t).DeploymentOperationID)
			if kind == "source" {
				src := f.src
				src.Generation++
				src.Settings.DockerfilePath = "Dockerfile.next"
				if _, err := configureBuildFixtureSource(f.pg, ctx, "usr_bootstrap", f.src.Generation, src, destinationEvent("automatic-source-change")); err != nil {
					t.Fatal(err)
				}
			} else {
				value := "next"
				if _, err := f.pg.PutHostingRuntimeConfiguration(ctx, "usr_bootstrap", f.src.ResourceID, domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: 2, ExpectedRuntimeGeneration: 0, RequestID: strings.Repeat("1", 32), Entries: []domain.HostingRuntimeEntry{{Name: "APP", Kind: "literal", Value: &value}}}, destinationEvent("automatic-runtime-change")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.pg.HostingApplyBegin(ctx, f.runner, a); err == nil {
				t.Fatal("changed frozen version admitted")
			}
			var epoch int64
			if err := f.db.QueryRow(ctx, `SELECT manual_epoch FROM hosting_auto_deploy_controls WHERE resource_id=$1`, f.src.ResourceID).Scan(&epoch); err != nil || epoch != 0 {
				t.Fatal("version invalidation added unnecessary epoch write", epoch, err)
			}
		})
	}
}

func TestPostgresAutomaticDeploymentDemandRollback(t *testing.T) {
	f := enabledAutomaticFixture(t)
	ctx := t.Context()
	a := f.accept(t, 170, strings.Repeat("b", 40))
	if _, err := f.db.Exec(ctx, `CREATE FUNCTION reject_auto_demand() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture rejects demand'; END $$; CREATE TRIGGER reject_auto_demand BEFORE INSERT ON hosting_auto_deploy_demands FOR EACH ROW EXECUTE FUNCTION reject_auto_demand()`); err != nil {
		t.Fatal(err)
	}
	if err := f.pg.CommitGitHubPushDelivery(ctx, a, pushEvidence(a.Policy, strings.Repeat("b", 40)), ""); err == nil {
		t.Fatal("failed demand committed")
	}
	var effects int
	if err := f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_auto_deploy_demands)+(SELECT count(*) FROM hosting_github_push_commit_intents)+(SELECT count(*) FROM hosting_builds WHERE created_by='push_bot')+(SELECT count(*) FROM river_job WHERE kind='hosting.prepare_build.v1' AND args->>'build_id' LIKE 'push-build-%')`).Scan(&effects); err != nil || effects != 0 {
		t.Fatal("partial push demand effects", effects, err)
	}
	p, err := f.pg.GetGitHubPushPolicy(ctx, "usr_bootstrap", f.src.ResourceID)
	if err != nil || p.DesiredCommit != strings.Repeat("a", 40) {
		t.Fatal("desired pointer advanced on rollback", err)
	}
}
func TestPostgresAutomaticDeploymentFirstApplySupersessionRace(t *testing.T) {
	for n := 0; n < 3; n++ {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			f := enabledAutomaticFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			p := f.push(t, 180, strings.Repeat("b", 40))
			f.finishBuild(t, p.LastBuildID, strings.Repeat("b", 40))
			if err := f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID); err != nil {
				t.Fatal(err)
			}
			a, _ := f.prepareAutomaticDeployment(t, f.status(t).DeploymentOperationID)
			push := f.accept(t, 181, strings.Repeat("c", 40))
			start := make(chan struct{})
			applyDone, pushDone := make(chan error, 1), make(chan error, 1)
			go func() { <-start; _, err := f.pg.HostingApplyBegin(ctx, f.runner, a); applyDone <- err }()
			go func() {
				<-start
				pushDone <- f.pg.CommitGitHubPushDelivery(ctx, push, pushEvidence(push.Policy, strings.Repeat("c", 40)), "")
			}()
			close(start)
			applyErr, pushErr := <-applyDone, <-pushDone
			if pushErr != nil {
				t.Fatal(pushErr)
			}
			if err := f.pg.PrepareHostingAutoDeploy(ctx, f.src.ResourceID); err != nil {
				t.Fatal(err)
			}
			var state string
			var admitted, held bool
			if err := f.db.QueryRow(ctx, `SELECT state,EXISTS(SELECT 1 FROM hosting_apply_admissions WHERE operation_id=$1),EXISTS(SELECT 1 FROM hosting_binding_state WHERE hold_operation_id=$1) FROM hosting_operations WHERE id=$1`, a.OperationID).Scan(&state, &admitted, &held); err != nil {
				t.Fatal(err)
			}
			if applyErr == nil {
				if !admitted || !held || state != "applying" {
					t.Fatal("admitted effect unlocked", state, admitted, held)
				}
			} else if !errors.Is(applyErr, store.ErrConflict) || admitted || held || state != "canceled" {
				t.Fatal("nonadmitted race not settled safely", state, admitted, held, applyErr)
			}
		})
	}
}

func TestPostgresAutomaticDeploymentSwitchRequiresCurrentDeploymentCapability(t *testing.T) {
	f := enabledAutomaticFixture(t)
	ctx := t.Context()
	id := f.pendingAutomaticSwitch(t)
	a, op := claimProxyPlan(t, f.pg, id, f.runner)
	prepared := domain.HostingProxyPreparation{DaemonID: f.bootstrap.Release.DaemonID, PlanHash: op.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}
	if _, err := f.pg.RunnerProxyPrepared(ctx, f.runner, domain.HostingProxyPreparedRequest{HostingAuthority: a, ReceiptID: "prepared_" + id, HostingProxyPreparation: prepared}); err != nil {
		t.Fatal(err)
	}
	if tag, err := f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.RunTypeLocalBuildDeploy); err != nil || tag.RowsAffected() != 1 {
		t.Fatal(err)
	}
	if _, err := f.pg.RunnerProxyApplyBegin(ctx, f.runner, a); !errors.Is(err, store.ErrConflict) {
		t.Fatal("policy capability drift admitted first switch mutation", err)
	}
	var count int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_proxy_admissions WHERE operation_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

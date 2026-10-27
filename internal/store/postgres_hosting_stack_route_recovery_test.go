package store_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func stackRouteRecoveryFixture(t *testing.T, git bool) (storageContinuityFixture, domain.HostingOperation, domain.HostingStorageRouteRecoveryInput) {
	t.Helper()
	ctx := t.Context()
	var f storageContinuityFixture
	var buildInput domain.HostingBuildDeploymentInput
	var deployment domain.HostingDeploymentInput
	if git {
		build, prior, input, loaded, route := routedGitComposeFixture(t)
		resource, err := build.pg.GetResource(ctx, prior.ResourceID)
		if err != nil {
			t.Fatal(err)
		}
		f = storageContinuityFixture{storageMaterializationFixture: storageMaterializationFixture{pg: build.pg, db: build.db, resource: resource, runner: build.runner, op: prior}, loaded: loaded, route: route}
		buildInput = input
	} else {
		log, prior, input, _ := settledStackFixture(t)
		f.storageMaterializationFixture = storageMaterializationFixture{pg: log.pg, db: log.db, resource: log.resource, runner: log.server.RunnerID, op: prior}
		deployment = input
		if _, err := f.db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.RunTypeProxyApply); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pg.ConfigureHostingProxy(ctx, "proxy_stack_recovery", prior.Snapshot.ServerID, "usr_bootstrap", domain.HostingProxyInput{ControlProjectID: prior.ProjectID, Configuration: domain.HostingProxyConfiguration{Email: "operator@nerocd.invalid", CA: "letsencrypt-staging", ManagementPort: 19080}, IdempotencyKey: "stack-recovery-proxy"}, destinationEvent("stack-recovery-proxy")); err != nil {
			t.Fatal(err)
		}
		var first string
		if err := f.db.QueryRow(ctx, `SELECT id FROM hosting_proxy_operations WHERE proxy_id='proxy_stack_recovery' AND generation=1`).Scan(&first); err != nil {
			t.Fatal(err)
		}
		settleProxyGeneration(t, f.pg, first, f.runner, prior.Release.DaemonID)
		if _, err := f.pg.GrantHostingHostname(ctx, "proxy_stack_recovery", "grant_stack_recovery", "usr_bootstrap", domain.HostingHostnameGrantInput{ProjectID: prior.ProjectID, Hostname: "apps.nerocd.invalid", Wildcard: true}, destinationEvent("stack-recovery-grant")); err != nil {
			t.Fatal(err)
		}
		route, err := f.pg.RequestHostingRoute(ctx, prior.ResourceID, "", "pop_stack_recovery_route", "usr_bootstrap", domain.HostingRouteInput{IdempotencyKey: "stack-recovery-route", Hostname: "recovery.apps.nerocd.invalid", PathPrefix: "/", ExpectedHealthyOperationID: prior.ID}, destinationEvent("stack-recovery-route"))
		if err != nil {
			t.Fatal(err)
		}
		f.route = route
		f.loaded = settleStorageContinuityGeneration(t, f.storageMaterializationFixture, route.OperationID)
	}
	proof := &domain.HostingStorageRouteContinuityInput{ExpectedLoadedGeneration: f.loaded.Generation, ExpectedLoadedProxyOperationID: f.loaded.ID, Routes: []domain.HostingRouteSwitchSelection{{RouteID: f.route.ID, ExpectedVersion: f.route.Version, ExpectedSelectedReleaseOperationID: f.op.ID}}}
	var next domain.HostingOperation
	var err error
	if git {
		buildInput.IdempotencyKey = "stack-recovery-next"
		buildInput.HostPort = f.op.Snapshot.HostPort
		buildInput.StorageRouteContinuity = proof
		next, err = f.pg.RequestHostingBuildDeployment(ctx, "hop_stack_recovery_next", f.resource.ID, "usr_bootstrap", buildInput, destinationEvent("stack-recovery-next"))
	} else {
		deployment.IdempotencyKey = "stack-recovery-next"
		deployment.HostPort = f.op.Snapshot.HostPort
		deployment.StorageRouteContinuity = proof
		next, err = f.pg.RequestHostingDeployment(ctx, "hop_stack_recovery_next", f.resource.ID, "usr_bootstrap", deployment, destinationEvent("stack-recovery-next"))
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pg.PrepareDeployment(ctx, next.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := domain.HostingAuthority{OperationID: next.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	if _, err = f.pg.HostingRunnerPlan(ctx, f.runner, a); err != nil {
		t.Fatal(err)
	}
	next, err = f.pg.HostingPrepared(ctx, f.runner, domain.HostingPreparedRequest{HostingAuthority: a, ReceiptID: "stack-recovery-prepared", Release: *f.op.Release, StackImages: f.op.Plan.Stack.Images})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingApplyBegin(ctx, f.runner, a); err != nil {
		t.Fatal(err)
	}
	state := *f.op.Result.Stack
	state.Components = append([]domain.HostingStackComponent(nil), state.Components...)
	for i := range state.Components {
		state.Components[i].OperationID = next.ID
		state.Components[i].ContainerID = strings.Repeat("e", 63) + fmt.Sprint(i+1)
	}
	state.Volumes = next.Plan.Stack.Volumes
	if _, err = f.pg.HostingStackVolumes(ctx, f.runner, domain.HostingStackVolumeRequest{HostingAuthority: a, PlanHash: next.Plan.Hash, Volumes: state.Volumes}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `CREATE FUNCTION reject_stack_recovery_queue() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.proxy_generation_requested' THEN RAISE EXCEPTION 'fixture queue rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_stack_recovery_queue BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_stack_recovery_queue()`); err != nil {
		t.Fatal(err)
	}
	next, err = f.pg.HostingComplete(ctx, f.runner, domain.HostingCompleteRequest{HostingAuthority: a, ReceiptID: "stack-recovery-complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, DaemonID: f.op.Release.DaemonID, PlanHash: next.Plan.Hash, Stack: &state}})
	if err != nil {
		t.Fatal(err)
	}
	if next.State != "succeeded" || next.MutationHold || next.CompleteReceipt == nil {
		t.Fatal("queue fault lost runtime success")
	}
	if successor := continuityState(t, f, next.ID, "unresolved"); successor != "" {
		t.Fatal("failed queue leaked successor")
	}
	if _, err = f.db.Exec(ctx, `DROP TRIGGER reject_stack_recovery_queue ON audit_events;DROP FUNCTION reject_stack_recovery_queue()`); err != nil {
		t.Fatal(err)
	}
	return f, next, domain.HostingStorageRouteRecoveryInput{ExpectedDescriptorHash: next.Snapshot.StorageRouteContinuity.Hash, ExpectedCompleteReceiptID: next.CompleteReceipt.ID, ExpectedCompleteReceiptHash: next.CompleteReceipt.BodyHash}
}

func TestPostgresStackRouteRecoveryAfterQueueFailure(t *testing.T) {
	for _, git := range []bool{false, true} {
		t.Run(fmt.Sprintf("git=%t", git), func(t *testing.T) {
			f, next, input := stackRouteRecoveryFixture(t, git)
			before, err := f.pg.GetHostingOperation(t.Context(), next.ID)
			if err != nil {
				t.Fatal(err)
			}
			var runtimeBefore string
			const runtimeEvidence = `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(o) ORDER BY id) FROM hosting_operations o),(SELECT jsonb_agg(to_jsonb(v) ORDER BY binding_id,name) FROM hosting_compose_volumes v),(SELECT jsonb_agg(to_jsonb(a) ORDER BY operation_id) FROM hosting_apply_admissions a),(SELECT jsonb_agg(to_jsonb(r) ORDER BY operation_id,phase) FROM hosting_receipts r))::text`
			if err := f.db.QueryRow(t.Context(), runtimeEvidence).Scan(&runtimeBefore); err != nil {
				t.Fatal(err)
			}
			view, err := f.pg.RecoverHostingStorageRouteContinuity(t.Context(), next.ResourceID, next.ID, "usr_bootstrap", input, destinationEvent("stack-route-recover"))
			if err != nil {
				t.Fatal("healthy stack route recovery rejected", err)
			}
			if view.State != "proxy_pending" || view.SuccessorProxyOperationID != "pop_storage_"+next.ID {
				t.Fatal("missing successor", view)
			}
			after, err := f.pg.GetHostingOperation(t.Context(), next.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("recovery changed runtime evidence", err)
			}
			replay, err := f.pg.RecoverHostingStorageRouteContinuity(t.Context(), next.ResourceID, next.ID, "usr_bootstrap", input, destinationEvent("stack-route-replay"))
			if err != nil || replay.SuccessorProxyOperationID != view.SuccessorProxyOperationID {
				t.Fatal("recovery retry duplicated successor", err)
			}
			var runtimeAfter string
			if err := f.db.QueryRow(t.Context(), runtimeEvidence).Scan(&runtimeAfter); err != nil || runtimeBefore != runtimeAfter {
				t.Fatal("recovery mutated writers, volume origins, admissions or receipts", err)
			}
			var stored []byte
			if err = f.db.QueryRow(t.Context(), `SELECT body FROM hosting_receipts WHERE operation_id=$1 AND phase='complete'`, next.ID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			var result domain.HostingResult
			if err = json.Unmarshal(stored, &result); err != nil || domain.HostingDocumentHash(result) != next.CompleteReceipt.BodyHash {
				t.Fatal("completion rewritten", err)
			}
		})
	}
}

func TestPostgresStackRouteRecoveryRejectsDrift(t *testing.T) {
	for _, change := range []string{"receipt", "component", "network", "volume", "release", "hold", "stopped", "route", "grant", "task", "lease"} {
		t.Run(change, func(t *testing.T) {
			f, op, input := stackRouteRecoveryFixture(t, true)
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := f.db.Exec(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
			}
			switch change {
			case "receipt":
				input.ExpectedCompleteReceiptHash = "sha256:" + strings.Repeat("c", 64)
			case "component":
				exec(`UPDATE hosting_binding_state SET healthy=jsonb_set(healthy,'{stack,components,0,container_id}','"foreign"') WHERE binding_id=$1`, op.BindingID)
			case "network":
				exec(`UPDATE hosting_binding_state SET healthy=jsonb_set(healthy,'{stack,network_id}','"foreign"') WHERE binding_id=$1`, op.BindingID)
			case "volume":
				exec(`UPDATE hosting_binding_state SET healthy=jsonb_set(healthy,'{stack,volumes,0,volume_name}','"foreign"') WHERE binding_id=$1`, op.BindingID)
			case "release":
				exec(`UPDATE hosting_retained_releases SET retained_running=false WHERE operation_id=$1`, op.ID)
			case "hold":
				exec(`UPDATE hosting_binding_state SET hold_operation_id=$1 WHERE binding_id=$2`, op.ID, op.BindingID)
			case "stopped":
				target := domain.HostingHealthy{Snapshot: op.Snapshot, OperationID: op.ID, Release: *op.Release, PlanHash: op.Plan.Hash, Stack: op.Result.Stack}
				stopped := domain.HostingStackStopped{Target: target, StopOperationID: "hop_recovery_stop"}
				raw, err := json.Marshal(stopped)
				if err != nil || stopped.Validate() != nil {
					t.Fatal("invalid stopped fixture", err)
				}
				// A stopped target cannot be fabricated beside an unresolved read:
				// the lifecycle constraint requires the exact completed stop receipt.
				if _, err := f.db.Exec(t.Context(), `INSERT INTO hosting_stack_lifecycle(binding_id,stopped) VALUES($1,$2) ON CONFLICT(binding_id) DO UPDATE SET stopped=excluded.stopped`, op.BindingID, raw); err == nil || !strings.Contains(err.Error(), "stopped stack requires exact completed removal") {
					t.Fatal("unproven stopped target admitted", err)
				}
				return
			case "route":
				exec(`UPDATE hosting_proxy_routes SET version=version+1 WHERE id=$1`, f.route.ID)
			case "grant":
				exec(`UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2`, op.Snapshot.DestinationID, op.ProjectID)
			case "task":
				exec(`UPDATE task_runs SET next_attempt=next_attempt+1 WHERE id=$1`, op.RunID)
			case "lease":
				exec(`UPDATE run_leases SET completion_key='other' WHERE run_id=$1`, op.RunID)
			}
			next := f.storageMaterializationFixture
			next.op = op
			before := storageRecoveryCounts(t, f, next)
			if _, err := f.pg.RecoverHostingStorageRouteContinuity(t.Context(), op.ResourceID, op.ID, "usr_bootstrap", input, destinationEvent("stack-recovery-denied")); err == nil {
				t.Fatal("drift admitted")
			}
			if storageRecoveryCounts(t, f, next) != before {
				t.Fatal("denied recovery changed queue, routes or admissions")
			}
		})
	}
}

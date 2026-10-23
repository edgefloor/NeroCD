package store_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func storageRouteRecoveryFixture(t *testing.T, configure ...func(*storageContinuityFixture)) (storageContinuityFixture, storageMaterializationFixture, domain.HostingStorageRouteRecoveryInput) {
	t.Helper()
	f := newStorageContinuityFixture(t)
	for _, change := range configure {
		change(&f)
	}
	next := f.successor(t)
	admitContinuityWriter(t, next)
	if _, err := f.db.Exec(t.Context(), `CREATE FUNCTION reject_recovery_queue() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.proxy_generation_requested' THEN RAISE EXCEPTION 'fixture queue rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_recovery_queue BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_recovery_queue()`); err != nil {
		t.Fatal(err)
	}
	next.op = next.complete(t, "succeeded", false)
	if next.op.State != "succeeded" || next.op.CompleteReceipt == nil || next.op.MutationHold {
		t.Fatalf("runtime success lost: %+v", next.op)
	}
	if successor := continuityState(t, f, next.op.ID, "unresolved"); successor != "" {
		t.Fatal("fault leaked proxy successor")
	}
	if _, err := f.db.Exec(t.Context(), `DROP TRIGGER reject_recovery_queue ON audit_events; DROP FUNCTION reject_recovery_queue()`); err != nil {
		t.Fatal(err)
	}
	in := domain.HostingStorageRouteRecoveryInput{ExpectedDescriptorHash: next.op.Snapshot.StorageRouteContinuity.Hash, ExpectedCompleteReceiptID: next.op.CompleteReceipt.ID, ExpectedCompleteReceiptHash: next.op.CompleteReceipt.BodyHash}
	return f, next, in
}

func TestHostingStorageRouteRecoveryPostgresPreservesForeignScope(t *testing.T) {
	for _, drift := range []bool{false, true} {
		t.Run(fmt.Sprint(drift), func(t *testing.T) {
			var foreign domain.HostingRoute
			var healthy domain.HostingOperation
			f, next, in := storageRouteRecoveryFixture(t, func(f *storageContinuityFixture) {
				other := createDestinationResource(t, f.pg, "recovery_foreign", "proj_security")
				grantDestination(t, f.pg, f.op.Snapshot.DestinationID, other.ProjectID, "recovery_foreign_grant")
				if _, err := f.pg.BindResourceDestination(t.Context(), store.ResourceBindingInput{ID: "binding_recovery_foreign", ResourceID: other.ID, DestinationID: f.op.Snapshot.DestinationID, CreatedBy: "usr_bootstrap", CreatedAt: time.Now().UTC()}, destinationEvent("recovery_foreign_binding")); err != nil {
					t.Fatal(err)
				}
				healthy = settleProxyImage(t, f.pg, other, domain.Server{ID: f.op.Snapshot.ServerID, RunnerID: f.runner}, "hop_recovery_foreign", 18081)
				if _, err := f.pg.GrantHostingHostname(t.Context(), "proxy_continuity", "grant_recovery_foreign", "usr_bootstrap", domain.HostingHostnameGrantInput{ProjectID: other.ProjectID, Hostname: "foreign.nerocd.invalid"}, destinationEvent("recovery_foreign_hostname")); err != nil {
					t.Fatal(err)
				}
				var err error
				foreign, err = f.pg.RequestHostingRoute(t.Context(), other.ID, "", "pop_recovery_foreign", "usr_bootstrap", domain.HostingRouteInput{IdempotencyKey: "recovery-foreign", Hostname: "foreign.nerocd.invalid", PathPrefix: "/", ExpectedHealthyOperationID: healthy.ID}, destinationEvent("recovery_foreign_route"))
				if err != nil {
					t.Fatal(err)
				}
				f.loaded = settleStorageContinuityGeneration(t, f.storageMaterializationFixture, foreign.OperationID)
			})
			if drift {
				if _, err := f.db.Exec(t.Context(), `UPDATE hosting_proxy_routes SET path_prefix='/changed' WHERE id=$1`, foreign.ID); err != nil {
					t.Fatal(err)
				}
			}
			before := storageRecoveryCounts(t, f, next)
			view, err := f.pg.RecoverHostingStorageRouteContinuity(t.Context(), f.resource.ID, next.op.ID, "usr_bootstrap", in, destinationEvent("recover_foreign_scope"))
			if drift {
				if !errors.Is(err, store.ErrConflict) || storageRecoveryCounts(t, f, next) != before {
					t.Fatalf("foreign drift admitted or leaked writes: %+v %v", view, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			queued, err := f.pg.GetHostingProxyOperation(t.Context(), view.SuccessorProxyOperationID)
			if err != nil || len(queued.Snapshot.Routes) != 2 {
				t.Fatalf("foreign scope lost: %+v %v", queued, err)
			}
			for _, route := range queued.Snapshot.Routes {
				if route.RouteID == foreign.ID && (route.ProjectID != "proj_security" || route.ReleaseOperationID != healthy.ID || route.PathPrefix != foreign.PathPrefix) {
					t.Fatal("foreign route was changed by recovery")
				}
			}
			current, err := f.pg.GetHostingRoute(t.Context(), foreign.ID)
			if err != nil || current.Route.Version != foreign.Version || current.Route.SelectedReleaseOperationID != healthy.ID {
				t.Fatalf("foreign metadata changed: %+v %v", current, err)
			}
		})
	}
}

func storageRecoveryCounts(t *testing.T, f storageContinuityFixture, next storageMaterializationFixture) string {
	t.Helper()
	var counts string
	if err := f.db.QueryRow(t.Context(), `SELECT jsonb_build_array((SELECT count(*) FROM hosting_proxy_operations),(SELECT count(*) FROM river_job),(SELECT count(*) FROM audit_events),(SELECT count(*) FROM hosting_apply_admissions),(SELECT count(*) FROM hosting_storage_operation_admissions),(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM hosting_proxy_routes r),(SELECT to_jsonb(c) FROM hosting_storage_route_continuities c WHERE operation_id=$1))::text`, next.op.ID).Scan(&counts); err != nil {
		t.Fatal(err)
	}
	return counts
}

func TestHostingStorageRouteRecoveryPostgresQueueReplayAndConcurrentDuplicate(t *testing.T) {
	for _, terminal := range []string{"loaded", "proxy_failed"} {
		t.Run(terminal, func(t *testing.T) {
			f, next, in := storageRouteRecoveryFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			results := make(chan error, 2) // One result per concurrent recovery caller.
			exited := [2]chan struct{}{make(chan struct{}), make(chan struct{})}
			t.Cleanup(func() {
				cancel()
				deadline := time.NewTimer(5 * time.Second)
				defer deadline.Stop()
				for _, done := range exited {
					select {
					case <-done:
					case <-deadline.C:
						t.Error("concurrent recovery callers did not exit after cancellation")
						return
					}
				}
			})
			for n := range 2 {
				go func() {
					defer close(exited[n])
					view, err := f.pg.RecoverHostingStorageRouteContinuity(ctx, f.resource.ID, next.op.ID, "usr_bootstrap", in, destinationEvent(fmt.Sprintf("storage_recovery_%d", n)))
					if err == nil && (view.State != "proxy_pending" || view.LoadedGeneration != nil || view.SuccessorProxyOperationID != "pop_storage_"+next.op.ID) {
						err = fmt.Errorf("invalid pending status: %+v", view)
					}
					results <- err
				}()
			}
			for range 2 {
				select {
				case err := <-results:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatalf("concurrent recovery results exceeded deadline: %v", ctx.Err())
				}
			}
			proxyID := continuityState(t, f, next.op.ID, "proxy_pending")
			var jobs, audits, admissions, storageAdmissions int
			if err := f.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM river_job WHERE args->>'operation_id'=$1),(SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='hosting.proxy_generation_requested'),(SELECT count(*) FROM hosting_apply_admissions WHERE operation_id=$2),(SELECT count(*) FROM hosting_storage_operation_admissions WHERE operation_id=$2)`, proxyID, next.op.ID).Scan(&jobs, &audits, &admissions, &storageAdmissions); err != nil || jobs != 1 || audits != 1 || admissions != 1 || storageAdmissions != 1 {
				t.Fatalf("duplicate queue/admission %d %d %d %d %v", jobs, audits, admissions, storageAdmissions, err)
			}
			if terminal == "loaded" {
				settleProxyGeneration(t, f.pg, proxyID, f.runner, f.op.Release.DaemonID)
			} else {
				a, op, prepared := admitSwitchOperation(t, switchControl{pg: f.pg, server: domain.Server{RunnerID: f.runner}, a: f.op}, proxyID)
				result := domain.HostingProxyResult{Outcome: "refused", PlanHash: op.Plan.Hash, EngineHash: prepared.EngineHash, DaemonID: prepared.DaemonID, LoadedGeneration: op.Plan.PreviousGeneration}
				if _, err := f.pg.RunnerProxyComplete(t.Context(), f.runner, domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "failed_recovery", Result: result}); err != nil {
					t.Fatal(err)
				}
			}
			continuityState(t, f, next.op.ID, terminal)
			before := storageRecoveryCounts(t, f, next)
			view, err := f.pg.RecoverHostingStorageRouteContinuity(t.Context(), f.resource.ID, next.op.ID, "usr_bootstrap", in, destinationEvent("replay_recovery"))
			if err != nil || view.State != terminal || (terminal == "loaded") != (view.LoadedGeneration != nil) {
				t.Fatalf("terminal replay %+v %v", view, err)
			}
			if storageRecoveryCounts(t, f, next) != before {
				t.Fatal("replay changed history or repeated queue/writer work")
			}
			stored, err := f.pg.GetHostingOperation(t.Context(), next.op.ID)
			if err != nil || !reflect.DeepEqual(stored, next.op) {
				t.Fatalf("recovery changed deployment receipt/evidence: %v", err)
			}
			in.ExpectedCompleteReceiptHash = "sha256:" + strings.Repeat("c", 64)
			if _, err = f.pg.RecoverHostingStorageRouteContinuity(t.Context(), f.resource.ID, next.op.ID, "usr_bootstrap", in, destinationEvent("changed_replay")); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("changed replay: %v", err)
			}
			if _, err = f.db.Exec(t.Context(), `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2`, next.op.Snapshot.DestinationID, next.op.ProjectID); err != nil {
				t.Fatal(err)
			}
			in.ExpectedCompleteReceiptHash = next.op.CompleteReceipt.BodyHash
			if _, err = f.pg.RecoverHostingStorageRouteContinuity(t.Context(), f.resource.ID, next.op.ID, "usr_bootstrap", in, destinationEvent("revoked_replay")); !errors.Is(err, auth.ErrForbidden) {
				t.Fatalf("revoked replay: %v", err)
			}
		})
	}
}

func TestHostingStorageRouteRecoveryPostgresRejectsDriftWithoutWrites(t *testing.T) {
	for _, change := range []string{"descriptor_hash", "receipt_id", "receipt_hash", "actor", "destination_grant", "hostname_grant", "route_version", "route_scope", "loaded_generation", "healthy_container", "healthy_volume", "healthy_storage", "healthy_port", "healthy_daemon", "release_ownership", "binding_hold", "proxy_hold", "restore_hold", "active_work", "uncertain_state", "task_state", "attempt", "completion_key", "queue_audit", "link_failure", "zero_row_link"} {
		t.Run(change, func(t *testing.T) {
			f, next, in := storageRouteRecoveryFixture(t)
			actor := "usr_bootstrap"
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := f.db.Exec(t.Context(), query, args...); err != nil {
					t.Fatal(err)
				}
			}
			switch change {
			case "descriptor_hash":
				in.ExpectedDescriptorHash = "sha256:" + strings.Repeat("c", 64)
			case "receipt_id":
				in.ExpectedCompleteReceiptID = "complete_other"
			case "receipt_hash":
				in.ExpectedCompleteReceiptHash = "sha256:" + strings.Repeat("c", 64)
			case "actor":
				actor = "usr_viewer"
			case "destination_grant":
				exec(`UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2`, next.op.Snapshot.DestinationID, next.op.ProjectID)
			case "hostname_grant":
				exec(`UPDATE hosting_proxy_hostname_grants SET revoked_at=clock_timestamp() WHERE id='grant_continuity'`)
			case "route_version":
				exec(`UPDATE hosting_proxy_routes SET version=version+1 WHERE id=$1`, f.route.ID)
			case "route_scope":
				exec(`UPDATE hosting_proxy_routes SET hostname='changed.apps.nerocd.invalid' WHERE id=$1`, f.route.ID)
			case "loaded_generation":
				exec(`UPDATE hosting_proxies SET loaded_generation=loaded_generation+1 WHERE id='proxy_continuity'`)
			case "healthy_container":
				exec(`UPDATE hosting_binding_state SET healthy=jsonb_set(healthy,'{container_id}','"foreign"') WHERE binding_id=$1`, next.op.BindingID)
			case "healthy_volume":
				exec(`UPDATE hosting_binding_state SET healthy=jsonb_set(healthy,'{storage,plan,volume_name}','"foreign"') WHERE binding_id=$1`, next.op.BindingID)
			case "healthy_storage":
				exec(`UPDATE hosting_binding_state SET healthy=jsonb_set(healthy,'{snapshot,storage,storage_id}','"foreign"') WHERE binding_id=$1`, next.op.BindingID)
			case "healthy_port":
				exec(`UPDATE hosting_binding_state SET healthy=jsonb_set(healthy,'{snapshot,host_port}','19000') WHERE binding_id=$1`, next.op.BindingID)
			case "healthy_daemon":
				exec(`UPDATE hosting_binding_state SET healthy=jsonb_set(healthy,'{release,daemon_id}','"foreign"') WHERE binding_id=$1`, next.op.BindingID)
			case "release_ownership":
				exec(`UPDATE hosting_retained_releases SET retained_running=false WHERE operation_id=$1`, next.op.ID)
			case "binding_hold":
				exec(`UPDATE hosting_binding_state SET hold_operation_id=$1 WHERE binding_id=$2`, next.op.ID, next.op.BindingID)
			case "proxy_hold":
				exec(`UPDATE hosting_proxies SET hold_operation_id=$1 WHERE id='proxy_continuity'`, f.loaded.ID)
			case "restore_hold":
				exec(`UPDATE hosting_binding_state SET restore_admitted=false WHERE binding_id=$1`, next.op.BindingID)
			case "active_work":
				exec(`INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) VALUES('hop_active',$1,$2,$3,'active',$4,'queued','usr_bootstrap')`, next.op.ProjectID, next.op.ResourceID, next.op.BindingID, domain.HostingDocumentHash("active"))
			case "task_state":
				exec(`UPDATE task_runs SET status='running' WHERE id=$1`, next.op.RunID)
			case "uncertain_state":
				exec(`UPDATE hosting_operations SET state='reconciliation_required' WHERE id=$1`, next.op.ID)
			case "attempt":
				exec(`UPDATE task_runs SET next_attempt=next_attempt+1 WHERE id=$1`, next.op.RunID)
			case "completion_key":
				exec(`UPDATE run_leases SET completion_key='other' WHERE id=$1`, next.authority.LeaseID)
			case "queue_audit":
				exec(`CREATE FUNCTION recovery_queue_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.proxy_generation_requested' THEN RAISE EXCEPTION 'queue audit fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER recovery_queue_fault BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION recovery_queue_fault()`)
			case "link_failure":
				exec(`CREATE FUNCTION recovery_link_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='proxy_pending' THEN RAISE EXCEPTION 'link fault'; END IF; RETURN NEW; END $$; CREATE TRIGGER recovery_link_fault AFTER UPDATE ON hosting_storage_route_continuities FOR EACH ROW EXECUTE FUNCTION recovery_link_fault()`)
			case "zero_row_link":
				exec(`CREATE FUNCTION recovery_link_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state='proxy_pending' THEN RETURN NULL; END IF; RETURN NEW; END $$; CREATE TRIGGER recovery_link_fault BEFORE UPDATE ON hosting_storage_route_continuities FOR EACH ROW EXECUTE FUNCTION recovery_link_fault()`)
			}
			before := storageRecoveryCounts(t, f, next)
			if view, err := f.pg.RecoverHostingStorageRouteContinuity(t.Context(), f.resource.ID, next.op.ID, actor, in, destinationEvent("reject_recovery")); err == nil {
				t.Fatalf("drift admitted: %+v", view)
			}
			if storageRecoveryCounts(t, f, next) != before {
				t.Fatal("rejected recovery leaked queue, link, route, audit, or writer writes")
			}
		})
	}
}

func TestHostingStorageRouteRecoveryPostgresTokenExpiryAfterAuditWait(t *testing.T) {
	f, next, in := storageRouteRecoveryFixture(t)
	principal, _ := proxyLegacyToken(t, f.pg, proxyTokenApplication(t, f.pg), "tok_storage_recovery_expiry", []string{domain.RoleSystemAdmin})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rollback := cleanupContinuityTransaction(t, blocker)
	var key int64
	var pid int
	if err = blocker.QueryRow(ctx, `SELECT hashtextextended(current_schema()||':storage-recovery-audit',0),pg_backend_pid()`).Scan(&key, &pid); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `CREATE FUNCTION recovery_audit_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.proxy_generation_requested' THEN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':storage-recovery-audit',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER recovery_audit_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION recovery_audit_wait()`); err != nil {
		t.Fatal(err)
	}
	var expires time.Time
	if err = f.db.QueryRow(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1 RETURNING expires_at`, principal.ID).Scan(&expires); err != nil {
		t.Fatal(err)
	}
	before := storageRecoveryCounts(t, f, next)
	done := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Go(func() {
		_, callErr := f.pg.RecoverHostingStorageRouteContinuity(ctx, f.resource.ID, next.op.ID, principal.ID, in, destinationEvent("expiry_recovery"))
		done <- callErr
	})
	defer func() { cancel(); rollback(); wg.Wait() }()
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
		case err = <-done:
			t.Fatalf("recovery did not wait: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	if _, err = blocker.Exec(ctx, `SELECT pg_sleep(greatest(0,extract(epoch FROM ($1::timestamptz-clock_timestamp())))+0.05)`, expires); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("expired actor survived audit wait: %v", err)
	}
	if storageRecoveryCounts(t, f, next) != before {
		t.Fatal("expired recovery leaked writes")
	}
}

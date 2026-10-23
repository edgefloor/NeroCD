package store_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

// Both requests must miss the initial receipt lookup before either can publish.
// PostgreSQL may queue the second tuple waiter behind the first, so follow the
// blocker chain rather than depending on a particular lock implementation.
func identicalRecoveryRequestsBehindTaskLock(t *testing.T, f switchControl, op domain.HostingProxyOperation, request func(context.Context, string) (any, error)) []any {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
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
	if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid() FROM task_runs WHERE id=$1 FOR UPDATE`, op.RunID).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	type result struct {
		value any
		err   error
	}
	// One buffered slot per launched request permits deterministic cleanup.
	done := make(chan result, 2)
	for _, id := range []string{"first", "second"} {
		work.Go(func() { value, e := request(ctx, id); done <- result{value, e} })
	}
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting int
		err = f.db.QueryRow(ctx, `WITH RECURSIVE blocked(pid) AS (
 SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid))
 UNION SELECT a.pid FROM pg_stat_activity a JOIN blocked b ON b.pid=ANY(pg_blocking_pids(a.pid))
) SELECT count(*) FROM pg_stat_activity a JOIN blocked b USING(pid)
 WHERE a.state='active' AND a.wait_event_type='Lock' AND a.query LIKE '%-- name: LockRunID%'`, pid).Scan(&waiting)
		if err != nil {
			t.Fatal(err)
		}
		if waiting == 2 {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("request returned before both initial misses queued: %v", r.err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	values := make([]any, 0, 2)
	for range 2 {
		select {
		case r := <-done:
			if r.err != nil {
				t.Fatalf("identical concurrent replay failed: %v", r.err)
			}
			values = append(values, r.value)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	work.Wait()
	if !reflect.DeepEqual(values[0], values[1]) {
		t.Fatal("identical requests returned different immutable receipts")
	}
	return values
}

func TestPostgresHostingProxyRecoveryConcurrentExactReplay(t *testing.T) {
	t.Run("plan", func(t *testing.T) {
		f, op, _ := recoveryConflictFixture(t)
		status, err := f.pg.HostingProxyRecovery(t.Context(), op.ID, "usr_bootstrap")
		if err != nil || !status.Eligible {
			t.Fatalf("status %+v %v", status, err)
		}
		in := recoveryPlanInput(status)
		values := identicalRecoveryRequestsBehindTaskLock(t, f, op, func(ctx context.Context, id string) (any, error) {
			return f.pg.PlanHostingProxyRecovery(ctx, op.ID, "prp_concurrent_"+id, "usr_bootstrap", in, destinationEvent("concurrent-plan-"+id))
		})
		plan := values[0].(domain.HostingProxyRecoveryPlan)
		if !plan.ExpiresAt.Equal(plan.CreatedAt.Add(domain.HostingProxyRecoveryPlanLifetime)) {
			t.Fatal("concurrent replay extended expiry")
		}
		var plans, audits int
		if err = f.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM hosting_proxy_recovery_plans WHERE operation_id=$1),(SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='hosting.proxy_recovery_planned')`, op.ID).Scan(&plans, &audits); err != nil || plans != 1 || audits != 1 {
			t.Fatalf("plan publication plans=%d audits=%d err=%v", plans, audits, err)
		}
		changed := in
		changed.ExpectedDesiredGeneration++
		if _, err = f.pg.PlanHostingProxyRecovery(t.Context(), op.ID, "prp_changed_body", "usr_bootstrap", changed, destinationEvent("changed-body")); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("plan replay accepted changed body: %v", err)
		}
		principal, _ := proxyLegacyToken(t, f.pg, proxyTokenApplication(t, f.pg), "tok_concurrent_plan", []string{domain.RoleSystemAdmin})
		if _, err = f.pg.PlanHostingProxyRecovery(t.Context(), op.ID, "prp_changed_actor", principal.ID, in, destinationEvent("changed-actor")); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("plan replay accepted changed actor: %v", err)
		}
		if _, err = f.db.Exec(t.Context(), `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`); err != nil {
			t.Fatal(err)
		}
		if _, err = f.pg.PlanHostingProxyRecovery(t.Context(), op.ID, "prp_revoked_actor", "usr_bootstrap", in, destinationEvent("revoked-actor")); !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("plan replay skipped current authorization: %v", err)
		}
	})
	t.Run("apply", func(t *testing.T) {
		f, op, _ := recoveryConflictFixture(t)
		status, err := f.pg.HostingProxyRecovery(t.Context(), op.ID, "usr_bootstrap")
		if err != nil || !status.Eligible {
			t.Fatalf("status %+v %v", status, err)
		}
		plan, err := f.pg.PlanHostingProxyRecovery(t.Context(), op.ID, "prp_concurrent_apply", "usr_bootstrap", recoveryPlanInput(status), destinationEvent("concurrent-apply-plan"))
		if err != nil {
			t.Fatal(err)
		}
		in := domain.HostingProxyRecoveryApplyInput{IdempotencyKey: "same-apply", PlanHash: plan.PlanHash}
		values := identicalRecoveryRequestsBehindTaskLock(t, f, op, func(ctx context.Context, id string) (any, error) {
			return f.pg.ApplyHostingProxyRecovery(ctx, op.ID, plan.ID, "prr_concurrent_"+id, "usr_bootstrap", in, destinationEvent("concurrent-apply-"+id))
		})
		resolution := values[0].(domain.HostingProxyRecoveryResolution)
		for _, change := range resolution.Routes {
			row, e := f.pg.GetHostingRoute(t.Context(), change.RouteID)
			if e != nil || row.Route.Version != change.AfterVersion || row.Route.SelectedReleaseOperationID != change.AfterReleaseOperationID {
				t.Fatalf("second CAS %+v %v", row.Route, e)
			}
		}
		var resolutions, queues, audits int
		if err = f.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM hosting_proxy_recovery_resolutions WHERE operation_id=$1),(SELECT count(*) FROM hosting_proxy_operations WHERE id IN (SELECT convergence_operation_id FROM hosting_proxy_recovery_resolutions WHERE operation_id=$1)),(SELECT count(*) FROM audit_events WHERE target_id=$1 AND action='hosting.proxy_metadata_recovered')`, op.ID).Scan(&resolutions, &queues, &audits); err != nil || resolutions != 1 || queues != 1 || audits != 1 {
			t.Fatalf("apply publication resolutions=%d queues=%d audits=%d err=%v", resolutions, queues, audits, err)
		}
		changed := in
		changed.PlanHash = domain.HostingDocumentHash("different-plan")
		if _, err = f.pg.ApplyHostingProxyRecovery(t.Context(), op.ID, plan.ID, "prr_changed_body", "usr_bootstrap", changed, destinationEvent("changed-apply-body")); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("apply replay accepted changed body: %v", err)
		}
		changed = in
		changed.IdempotencyKey = "different-key"
		if _, err = f.pg.ApplyHostingProxyRecovery(t.Context(), op.ID, plan.ID, "prr_changed_key", "usr_bootstrap", changed, destinationEvent("changed-apply-key")); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("apply replay accepted changed key: %v", err)
		}
		if _, err = f.db.Exec(t.Context(), `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`); err != nil {
			t.Fatal(err)
		}
		if _, err = f.pg.ApplyHostingProxyRecovery(t.Context(), op.ID, plan.ID, "prr_revoked_actor", "usr_bootstrap", in, destinationEvent("revoked-apply-actor")); !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("apply replay skipped current authorization: %v", err)
		}
	})
}

func recoveryEnabledConflict(t *testing.T, outcome string) (switchControl, domain.HostingProxyOperation) {
	t.Helper()
	f := switchControlFixture(t)
	op, err := f.pg.RequestHostingRouteSwitch(t.Context(), f.resource.ID, "pop_enabled_recovery", "usr_bootstrap", f.input("enabled-recovery"), destinationEvent("enabled-recovery"))
	if err != nil {
		t.Fatal(err)
	}
	var a domain.HostingAuthority
	var prepared domain.HostingProxyPreparation
	if outcome == "refused" {
		a, op = claimProxyPlan(t, f.pg, op.ID, f.server.RunnerID)
		prepared = domain.HostingProxyPreparation{DaemonID: f.a.Release.DaemonID, PlanHash: op.Plan.Hash, EngineHash: strings.Repeat("a", 64), StaticHash: strings.Repeat("b", 64)}
	} else {
		a, op, prepared = admitSwitchOperation(t, f, op.ID)
	}
	if _, err = f.db.Exec(t.Context(), `UPDATE hosting_proxy_routes SET version=version+1 WHERE id=$1`, f.routes[0].ID); err != nil {
		t.Fatal(err)
	}
	op, err = f.pg.RunnerProxyComplete(t.Context(), f.server.RunnerID, domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "enabled_recovery_complete", Result: trustedProxyResult(op, prepared, outcome)})
	if err != nil || !op.MetadataConflict || !op.MutationHold {
		t.Fatalf("conflict %+v %v", op, err)
	}
	return f, op
}

func TestPostgresHostingProxyRecoveryFinishedProofAndScope(t *testing.T) {
	for _, outcome := range []string{"rolled_back", "refused"} {
		t.Run(outcome, func(t *testing.T) {
			f, op := recoveryEnabledConflict(t, outcome)
			status, err := f.pg.HostingProxyRecovery(t.Context(), op.ID, "usr_bootstrap")
			if err != nil || !status.Eligible {
				t.Fatalf("eligibility %+v %v", status, err)
			}
			unselected, err := f.pg.GetHostingRoute(t.Context(), f.routes[2].ID)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := f.pg.PlanHostingProxyRecovery(t.Context(), op.ID, "prp_enabled", "usr_bootstrap", recoveryPlanInput(status), destinationEvent("enabled-review"))
			if err != nil {
				t.Fatal(err)
			}
			resolution, err := f.pg.ApplyHostingProxyRecovery(t.Context(), op.ID, plan.ID, "prr_enabled", "usr_bootstrap", domain.HostingProxyRecoveryApplyInput{IdempotencyKey: "enabled", PlanHash: plan.PlanHash}, destinationEvent("enabled-apply"))
			if err != nil {
				t.Fatal(err)
			}
			queued, err := f.pg.GetHostingProxyOperation(t.Context(), resolution.ConvergenceOperationID)
			if err != nil || !reflect.DeepEqual(queued.Snapshot.Routes, f.loaded.Snapshot.Routes) {
				t.Fatalf("loaded scope %+v %v", queued.Snapshot, err)
			}
			after, err := f.pg.GetHostingRoute(t.Context(), f.routes[2].ID)
			if err != nil || after.Route.Version != unselected.Route.Version || after.Route.SelectedReleaseOperationID != unselected.Route.SelectedReleaseOperationID {
				t.Fatalf("unselected changed %+v %v", after, err)
			}
			for _, change := range resolution.Routes {
				if change.AfterVersion < change.BeforeVersion || change.AfterReleaseOperationID != f.a.ID || !change.Enabled {
					t.Fatalf("metadata evidence %+v", change)
				}
			}
			if _, err = f.pg.RequestHostingReleaseCleanup(t.Context(), "hop_cleanup_recovery_a", f.resource.ID, f.a.ID, "usr_bootstrap", domain.HostingCleanupInput{ExpectedGeneration: f.resource.Generation, IdempotencyKey: "recovery-cleanup"}, destinationEvent("recovery-cleanup")); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("loaded/queued refs lost %v", err)
			}
		})
	}
	for _, proof := range []string{"completion_key", "next_attempt", "uncertain_result"} {
		t.Run(proof, func(t *testing.T) {
			if proof == "uncertain_result" {
				f := switchControlFixture(t)
				op, err := f.pg.RequestHostingRouteSwitch(t.Context(), f.resource.ID, "pop_uncertain_recovery", "usr_bootstrap", f.input("uncertain-recovery"), destinationEvent("uncertain-recovery"))
				if err != nil {
					t.Fatal(err)
				}
				a, op, p := admitSwitchOperation(t, f, op.ID)
				_, err = f.pg.RunnerProxyComplete(t.Context(), f.server.RunnerID, domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "uncertain_complete", Result: trustedProxyResult(op, p, "uncertain")})
				if err != nil {
					t.Fatal(err)
				}
				status, err := f.pg.HostingProxyRecovery(t.Context(), op.ID, "usr_bootstrap")
				if err != nil || status.Eligible {
					t.Fatalf("uncertain eligible %+v %v", status, err)
				}
				return
			}
			f, op, _ := recoveryConflictFixture(t)
			var err error
			if proof == "completion_key" {
				_, err = f.db.Exec(t.Context(), `UPDATE run_leases SET completion_key='foreign' WHERE run_id=$1`, op.RunID)
			} else {
				_, err = f.db.Exec(t.Context(), `UPDATE task_runs SET next_attempt=next_attempt+1 WHERE id=$1`, op.RunID)
			}
			if err != nil {
				t.Fatal(err)
			}
			status, err := f.pg.HostingProxyRecovery(t.Context(), op.ID, "usr_bootstrap")
			if err != nil || status.Eligible {
				t.Fatalf("bad proof %+v %v", status, err)
			}
		})
	}
}

func TestPostgresHostingProxyRecoveryExpiryAfterAuditWait(t *testing.T) {
	t.Run("plan_expiry", func(t *testing.T) { testRecoveryExpiryAfterAuditWait(t, false) })
	t.Run("actor_token_expiry", func(t *testing.T) { testRecoveryExpiryAfterAuditWait(t, true) })
}

func testRecoveryExpiryAfterAuditWait(t *testing.T, tokenExpiry bool) {
	f, op, _ := recoveryConflictFixture(t)
	actor := "usr_bootstrap"
	if tokenExpiry {
		principal, _ := proxyLegacyToken(t, f.pg, proxyTokenApplication(t, f.pg), "tok_recovery_expiry", []string{domain.RoleSystemAdmin})
		actor = principal.ID
	}
	status, err := f.pg.HostingProxyRecovery(t.Context(), op.ID, actor)
	if err != nil || !status.Eligible {
		t.Fatalf("eligibility %+v %v", status, err)
	}
	plan, err := f.pg.PlanHostingProxyRecovery(t.Context(), op.ID, "prp_expiry", actor, recoveryPlanInput(status), destinationEvent("expiry-review"))
	if err != nil {
		t.Fatal(err)
	}
	expires := plan.ExpiresAt
	if tokenExpiry {
		if err = f.db.QueryRow(t.Context(), `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1 RETURNING expires_at`, actor).Scan(&expires); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
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
	if err = blocker.QueryRow(ctx, `SELECT hashtextextended(current_schema()||':recovery-audit',0),pg_backend_pid()`).Scan(&key, &pid); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `CREATE FUNCTION recovery_audit_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.proxy_metadata_recovered' THEN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':recovery-audit',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER recovery_audit_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION recovery_audit_wait()`); err != nil {
		t.Fatal(err)
	}
	before, err := f.pg.ListHostingRoutes(ctx, f.resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	work.Go(func() {
		_, e := f.pg.ApplyHostingProxyRecovery(ctx, op.ID, plan.ID, "prr_expiry", actor, domain.HostingProxyRecoveryApplyInput{IdempotencyKey: "expiry", PlanHash: plan.PlanHash}, destinationEvent("expiry-apply"))
		done <- e
	})
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
		case e := <-done:
			t.Fatalf("did not wait %v", e)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	for {
		var expired bool
		if err = f.db.QueryRow(ctx, `SELECT clock_timestamp()>=$1`, expires).Scan(&expired); err != nil {
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
	expected := store.ErrConflict
	if tokenExpiry {
		expected = auth.ErrForbidden
	}
	if err = <-done; !errors.Is(err, expected) {
		t.Fatalf("expired recovery committed %v", err)
	}
	after, err := f.pg.ListHostingRoutes(ctx, f.resource.ID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("metadata rollback %v", err)
	}
	current, err := f.pg.GetHostingProxyOperation(ctx, op.ID)
	if err != nil || !current.MutationHold || !current.MetadataConflict {
		t.Fatalf("hold lost %+v %v", current, err)
	}
	var resolutions, queues, audits int
	if err = f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_proxy_recovery_resolutions WHERE operation_id=$1),(SELECT count(*) FROM hosting_proxy_operations WHERE id=$2),(SELECT count(*) FROM audit_events WHERE id=$3)`, op.ID, "pop_recover_prr_expiry", destinationEvent("expiry-apply").ID).Scan(&resolutions, &queues, &audits); err != nil || resolutions != 0 || queues != 0 || audits != 0 {
		t.Fatalf("atomicity %d %d %d %v", resolutions, queues, audits, err)
	}
	if tokenExpiry {
		return
	}
	replayed, err := f.pg.PlanHostingProxyRecovery(ctx, op.ID, "prp_expired_replay", actor, recoveryPlanInput(status), destinationEvent("expired-replay"))
	if err != nil || !reflect.DeepEqual(replayed, plan) {
		t.Fatalf("expired replay renewed %+v %v", replayed, err)
	}
}

func recoveryConflictFixture(t *testing.T, destination ...bool) (switchControl, domain.HostingProxyOperation, domain.HostingProxyCompleteRequest) {
	t.Helper()
	f := switchControlFixture(t)
	op, err := f.pg.RequestHostingRouteSwitch(t.Context(), f.resource.ID, "pop_recovery_switch", "usr_bootstrap", f.input("recovery-switch"), destinationEvent("recovery-switch"))
	if err != nil {
		t.Fatal(err)
	}
	a, op, prepared := admitSwitchOperation(t, f, op.ID)
	if len(destination) > 0 && destination[0] {
		_, err = f.pg.RevokeDestinationProject(t.Context(), op.Snapshot.DestinationID, f.resource.ProjectID, destinationEvent("recovery-revoke"))
	} else {
		_, err = f.pg.RevokeHostingHostname(t.Context(), "proxy_fixture", "grant_proxy_hostname", "usr_bootstrap", "recovery-revoke", destinationEvent("recovery-revoke"))
	}
	if err != nil {
		t.Fatal(err)
	}
	request := domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: "complete_recovery_switch", Result: trustedProxyResult(op, prepared, "rolled_back")}
	op, err = f.pg.RunnerProxyComplete(t.Context(), f.server.RunnerID, request)
	if err != nil || !op.MetadataConflict || !op.MutationHold {
		t.Fatalf("conflict %+v %v", op, err)
	}
	return f, op, request
}

func TestPostgresHostingProxyRecoveryDestinationRevocationAndConcurrentApply(t *testing.T) {
	f, op, _ := recoveryConflictFixture(t, true)
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	status, err := f.pg.HostingProxyRecovery(ctx, op.ID, "usr_bootstrap")
	if err != nil || !status.Eligible {
		t.Fatalf("revoked destination admin recovery %+v %v", status, err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	plans := make(chan domain.HostingProxyRecoveryPlan, 2)
	var work sync.WaitGroup
	for _, key := range []string{"one", "two"} {
		work.Go(func() {
			<-start
			in := recoveryPlanInput(status)
			in.IdempotencyKey = key
			p, e := f.pg.PlanHostingProxyRecovery(ctx, op.ID, "prp_"+key, "usr_bootstrap", in, destinationEvent("plan_"+key))
			if e == nil {
				plans <- p
			}
			results <- e
		})
	}
	close(start)
	work.Wait()
	close(results)
	close(plans)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, store.ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("plans success=%d conflict=%d", success, conflict)
	}
	plan := <-plans
	start = make(chan struct{})
	results = make(chan error, 2)
	for _, key := range []string{"one", "two"} {
		work.Go(func() {
			<-start
			_, e := f.pg.ApplyHostingProxyRecovery(ctx, op.ID, plan.ID, "prr_"+key, "usr_bootstrap", domain.HostingProxyRecoveryApplyInput{IdempotencyKey: key, PlanHash: plan.PlanHash}, destinationEvent("apply_"+key))
			results <- e
		})
	}
	close(start)
	work.Wait()
	close(results)
	success, conflict = 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if errors.Is(e, store.ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("applies success=%d conflict=%d", success, conflict)
	}
	var revoked bool
	var receipts, queues int
	if err = f.db.QueryRow(ctx, `SELECT (SELECT revoked_at IS NOT NULL FROM destination_project_grants WHERE destination_id=$1 AND project_id=$2),(SELECT count(*) FROM hosting_proxy_recovery_resolutions WHERE operation_id=$3),(SELECT count(*) FROM hosting_proxy_operations WHERE proxy_id=$4 AND left(id,length('pop_recover_prr_'))='pop_recover_prr_')`, op.Snapshot.DestinationID, f.resource.ProjectID, op.ID, op.ProxyID).Scan(&revoked, &receipts, &queues); err != nil || !revoked || receipts != 1 || queues != 1 {
		t.Fatalf("authority/once revoked=%t receipts=%d queues=%d %v", revoked, receipts, queues, err)
	}
}

func recoveryPlanInput(status domain.HostingProxyRecoveryStatus) domain.HostingProxyRecoveryInput {
	r := status.Review
	return domain.HostingProxyRecoveryInput{IdempotencyKey: "review", ExpectedLoadedGeneration: r.LoadedGeneration, ExpectedDesiredGeneration: r.DesiredGeneration, ExpectedCompleteReceiptID: r.CompleteReceiptID, ExpectedCompleteReceiptHash: r.CompleteReceiptHash}
}

func TestPostgresHostingProxyRecovery(t *testing.T) {
	t.Run("revoked_grant_atomic_queue_response_loss_and_original_replay", func(t *testing.T) {
		f, op, request := recoveryConflictFixture(t)
		status, err := f.pg.HostingProxyRecovery(t.Context(), op.ID, "usr_bootstrap")
		if err != nil || !status.Eligible {
			t.Fatalf("eligibility %+v %v", status, err)
		}
		in := recoveryPlanInput(status)
		plan, err := f.pg.PlanHostingProxyRecovery(t.Context(), op.ID, "prp_recovery", "usr_bootstrap", in, destinationEvent("review"))
		if err != nil {
			t.Fatal(err)
		}
		replay, err := f.pg.PlanHostingProxyRecovery(t.Context(), op.ID, "prp_ignored", "usr_bootstrap", in, destinationEvent("review-replay"))
		if err != nil || !reflect.DeepEqual(plan, replay) {
			t.Fatalf("review replay %v", err)
		}
		other := in
		other.IdempotencyKey = "different"
		if _, err = f.pg.PlanHostingProxyRecovery(t.Context(), op.ID, "prp_other", "usr_bootstrap", other, destinationEvent("other")); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("outstanding plan bound %v", err)
		}
		apply := domain.HostingProxyRecoveryApplyInput{IdempotencyKey: "apply", PlanHash: plan.PlanHash}
		resolution, err := f.pg.ApplyHostingProxyRecovery(t.Context(), op.ID, plan.ID, "prr_recovery", "usr_bootstrap", apply, destinationEvent("apply"))
		if err != nil {
			t.Fatal(err)
		}
		again, err := f.pg.ApplyHostingProxyRecovery(t.Context(), op.ID, plan.ID, "prr_ignored", "usr_bootstrap", apply, destinationEvent("apply-replay"))
		if err != nil || !reflect.DeepEqual(resolution, again) {
			t.Fatalf("apply replay %+v %v", again, err)
		}
		settled, err := f.pg.GetHostingProxyOperation(t.Context(), op.ID)
		if err != nil || settled.MetadataConflict || settled.MutationHold || settled.State != "rolled_back" || !reflect.DeepEqual(settled.Result, op.Result) || !reflect.DeepEqual(settled.CompleteReceipt, op.CompleteReceipt) {
			t.Fatalf("settled %+v %v", settled, err)
		}
		for _, r := range f.routes {
			row, err := f.pg.GetHostingRoute(t.Context(), r.ID)
			if err != nil || row.Route.Enabled || row.Route.SelectedReleaseOperationID != f.a.ID {
				t.Fatalf("repaired disabled %+v %v", row, err)
			}
		}
		queued, err := f.pg.GetHostingProxyOperation(t.Context(), resolution.ConvergenceOperationID)
		if err != nil || len(queued.Snapshot.Routes) != 0 || queued.CreatedBy != "usr_bootstrap" {
			t.Fatalf("safe withdrawal %+v %v", queued, err)
		}
		var count int
		if err = f.db.QueryRow(t.Context(), `SELECT count(*) FROM hosting_proxy_recovery_resolutions WHERE operation_id=$1`, op.ID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("receipts %d %v", count, err)
		}
		original, err := f.pg.RunnerProxyComplete(t.Context(), f.server.RunnerID, request)
		if err != nil || original.MutationHold || !reflect.DeepEqual(original.Result, op.Result) {
			t.Fatalf("original replay %+v %v", original, err)
		}
		if _, err = f.db.Exec(t.Context(), `UPDATE hosting_proxy_recovery_resolutions SET body=body WHERE operation_id=$1`, op.ID); err == nil {
			t.Fatal("resolution mutated")
		}
		otherActor, _ := proxyLegacyToken(t, f.pg, proxyTokenApplication(t, f.pg), "tok_recovery_other_actor", []string{domain.RoleSystemAdmin})
		if _, err = f.pg.ApplyHostingProxyRecovery(t.Context(), op.ID, plan.ID, "prr_other_actor", otherActor.ID, apply, destinationEvent("other-actor-replay")); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("actor-bound replay %v", err)
		}
		if _, err = f.db.Exec(t.Context(), `UPDATE users SET status='disabled' WHERE id='usr_bootstrap'`); err != nil {
			t.Fatal(err)
		}
		if _, err = f.pg.ApplyHostingProxyRecovery(t.Context(), op.ID, plan.ID, "prr_disabled_actor", "usr_bootstrap", apply, destinationEvent("disabled-replay")); !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("replay skipped current read authority %v", err)
		}
	})
	t.Run("stale_plan_is_atomic", func(t *testing.T) {
		f, op, _ := recoveryConflictFixture(t)
		status, err := f.pg.HostingProxyRecovery(t.Context(), op.ID, "usr_bootstrap")
		if err != nil || !status.Eligible {
			t.Fatalf("status %+v %v", status, err)
		}
		plan, err := f.pg.PlanHostingProxyRecovery(t.Context(), op.ID, "prp_stale", "usr_bootstrap", recoveryPlanInput(status), destinationEvent("stale-review"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.db.Exec(t.Context(), `UPDATE hosting_proxy_routes SET version=version+1 WHERE id=$1`, f.routes[0].ID); err != nil {
			t.Fatal(err)
		}
		if _, err = f.pg.ApplyHostingProxyRecovery(t.Context(), op.ID, plan.ID, "prr_stale", "usr_bootstrap", domain.HostingProxyRecoveryApplyInput{IdempotencyKey: "stale", PlanHash: plan.PlanHash}, destinationEvent("stale-apply")); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("stale accepted %v", err)
		}
		current, err := f.pg.GetHostingProxyOperation(t.Context(), op.ID)
		if err != nil || !current.MutationHold || !current.MetadataConflict {
			t.Fatalf("hold lost %+v %v", current, err)
		}
	})
	t.Run("current_authority_required", func(t *testing.T) {
		f, op, _ := recoveryConflictFixture(t)
		if _, err := f.pg.HostingProxyRecovery(t.Context(), op.ID, "unknown_actor"); !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("unauthorized %v", err)
		}
		if _, err := f.db.Exec(t.Context(), `UPDATE run_leases SET status='expired' WHERE run_id=$1`, op.RunID); err != nil {
			t.Fatal(err)
		}
		status, err := f.pg.HostingProxyRecovery(t.Context(), op.ID, "usr_bootstrap")
		if err != nil || status.Eligible {
			t.Fatalf("expired authority %+v %v", status, err)
		}
	})
}

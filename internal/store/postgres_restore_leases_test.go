package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func restoreLeases(t *testing.T, db *pgxpool.Pool) {
	t.Helper()
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if err = store.InvalidateRunnerLeasesForRestoreTx(t.Context(), tx); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresRestoreLeasesDrainsFutureDeadlinesAndRollsBackEveryBatch(t *testing.T) {
	ctx := t.Context()
	pg, db := openPostgresIntegrationStore(t, ctx, "restore_leases")
	defer closePostgresStore(t, pg)
	now := time.Now().UTC()
	const count = 129 // More than two online expiry batches.
	runner := domain.Runner{ID: "restore_runner", Name: "restore", Tags: []string{"local"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}
	if _, err := pg.RegisterRunner(ctx, runner); err != nil {
		t.Fatal(err)
	}
	for i := range count {
		id := fmt.Sprintf("restore_run_%03d", i)
		if _, err := pg.CreateRun(ctx, raceRun(id, now)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `UPDATE task_runs SET status='running',runner_id=$2,next_attempt=2 WHERE id=$1`, id, runner.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(ctx, `INSERT INTO run_leases(id,run_id,runner_id,status,expires_at,attempt,fence) VALUES($1,$2,$3,'active',clock_timestamp()+CASE WHEN $4::int%2=0 THEN interval '1 day' ELSE interval '-1 hour' END,1,$1)`, "lease_"+id, id, runner.ID, i); err != nil {
			t.Fatal(err)
		}
	}
	// An arbitrary caller clock cannot expire future production authority.
	if err := pg.ExpireLeases(ctx, now.Add(48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var future int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM run_leases WHERE status='active' AND expires_at>clock_timestamp()`).Scan(&future); err != nil || future != 65 {
		t.Fatalf("caller clock expired future leases: %d %v", future, err)
	}
	// Recreate a fully restored snapshot before testing all-batch atomicity.
	if _, err := db.Exec(ctx, `UPDATE task_runs SET status='running',runner_id=$1 WHERE id LIKE 'restore_run_%'`, runner.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE run_leases SET status='active',completed_at=NULL WHERE run_id LIKE 'restore_run_%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `CREATE FUNCTION reject_last_restored_run() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.id='restore_run_128' AND NEW.status='queued' THEN RAISE EXCEPTION 'restore settlement failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_restore BEFORE UPDATE ON task_runs FOR EACH ROW EXECUTE FUNCTION reject_last_restored_run()`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if err = store.InvalidateRunnerLeasesForRestoreTx(ctx, tx); err == nil {
		t.Fatal("late batch failure accepted")
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var active, running int
	if err = db.QueryRow(ctx, `SELECT (SELECT count(*) FROM run_leases WHERE status='active'),(SELECT count(*) FROM task_runs WHERE id LIKE 'restore_run_%' AND status='running')`).Scan(&active, &running); err != nil || active != count || running != count {
		t.Fatalf("partial recovery committed: active=%d running=%d %v", active, running, err)
	}
	if _, err = db.Exec(ctx, `DROP TRIGGER reject_restore ON task_runs; DROP FUNCTION reject_last_restored_run()`); err != nil {
		t.Fatal(err)
	}
	restoreLeases(t, db)
	var expired, queued int
	if err = db.QueryRow(ctx, `SELECT (SELECT count(*) FROM run_leases WHERE status='expired' AND completed_at IS NOT NULL),(SELECT count(*) FROM task_runs WHERE id LIKE 'restore_run_%' AND status='queued' AND runner_id IS NULL AND finished_at IS NULL)`).Scan(&expired, &queued); err != nil || expired != count || queued != count {
		t.Fatalf("incomplete restore: expired=%d queued=%d %v", expired, queued, err)
	}
	// A second invocation is harmless; a fresh claim gets a new authority tuple.
	restoreLeases(t, db)
	claim, err := pg.ClaimRun(ctx, runner.ID, now, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claim.Lease.Attempt != 2 || claim.Lease.Fence == "lease_"+claim.Run.ID {
		t.Fatal("restored authority reused", claim.Lease)
	}
	oldID := "lease_" + claim.Run.ID
	if _, err = pg.RenewLease(ctx, runner.ID, oldID, oldID, 1, now, time.Hour); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old restore authority renewed: %v", err)
	}
}

func TestPostgresRestoreLeasesBuildCleanupIsTransactional(t *testing.T) {
	f := newBuildStoreFixture(t)
	b, a := f.start(t, "restore-build")
	ctx := t.Context()
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if err = store.InvalidateRunnerLeasesForRestoreTx(ctx, tx); err != nil {
		t.Fatal(err)
	}
	var state, task, lease string
	if err = tx.QueryRow(ctx, `SELECT b.state,r.status,l.status FROM hosting_builds b JOIN task_runs r ON r.id=b.run_id JOIN run_leases l ON l.run_id=r.id WHERE b.id=$1`, b.ID).Scan(&state, &task, &lease); err != nil || state != "cleanup_required" || task != "failed" || lease != "expired" {
		t.Fatal(state, task, lease, err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	status, err := f.pg.HostingBuildStatus(ctx, f.runner, a)
	if err != nil || !status.MayExecute || status.Build.Revision != b.Revision {
		t.Fatal("rollback lost build authority", status, err)
	}
	restoreLeases(t, f.db)
	status, err = f.pg.HostingBuildStatus(ctx, f.runner, a)
	if err != nil || status.MayExecute || status.Build.State != "cleanup_required" || status.Build.FailureCode != "authority_lost" || status.Build.Revision != b.Revision+1 {
		t.Fatal("restore did not retain cleanup", status, err)
	}
	restoreLeases(t, f.db)
	again, err := f.pg.HostingBuildStatus(ctx, f.runner, a)
	if err != nil || again.Build.Revision != status.Build.Revision {
		t.Fatal("restore repeated build cleanup", again, err)
	}
}

func TestPostgresRestoreLeasesRetainsHostingMutationAndRestoreHolds(t *testing.T) {
	for _, admitted := range []bool{false, true} {
		t.Run(fmt.Sprintf("admitted_%t", admitted), func(t *testing.T) {
			pg, db, resource, server, _ := hostingExecutionFixture(t)
			ctx := t.Context()
			op, err := pg.RequestHostingDeployment(ctx, "restore_hosting", resource.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("restore_hosting"))
			if err != nil {
				t.Fatal(err)
			}
			op = awaitHostingDispatch(t, pg, op.ID)
			a, _ := prepareHostingAttempt(t, pg, op, server.RunnerID)
			if admitted {
				if _, err = pg.HostingApplyBegin(ctx, server.RunnerID, a); err != nil {
					t.Fatal(err)
				}
			}
			tx, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
			if _, err = tx.Exec(ctx, `INSERT INTO hosting_binding_restore_holds(binding_id) SELECT id FROM resource_bindings ON CONFLICT DO NOTHING; UPDATE hosting_binding_state SET restore_admitted=false`); err != nil {
				t.Fatal(err)
			}
			if err = store.InvalidateRunnerLeasesForRestoreTx(ctx, tx); err != nil {
				t.Fatal(err)
			}
			if err = tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			after, err := pg.GetHostingOperation(ctx, op.ID)
			if err != nil {
				t.Fatal(err)
			}
			wantOp, wantTask := "prepared", "queued"
			if admitted {
				wantOp, wantTask = "reconciliation_required", "failed"
			}
			var task string
			var cleared, finished, held bool
			if err = db.QueryRow(ctx, `SELECT status,runner_id IS NULL,finished_at IS NOT NULL,EXISTS(SELECT 1 FROM hosting_binding_restore_holds WHERE binding_id=$2) FROM task_runs WHERE id=$1`, a.RunID, op.BindingID).Scan(&task, &cleared, &finished, &held); err != nil {
				t.Fatal(err)
			}
			if after.State != wantOp || after.MutationHold != admitted || task != wantTask || !cleared || finished != admitted || !held {
				t.Fatalf("op=%s mutation=%v task=%s cleared=%v finished=%v restore_hold=%v", after.State, after.MutationHold, task, cleared, finished, held)
			}
			if _, err = pg.HostingApplyBegin(ctx, server.RunnerID, a); err == nil {
				t.Fatal("restored fence admitted mutation")
			}
			if _, err = pg.ClaimRun(ctx, server.RunnerID, time.Now(), time.Minute); !errors.Is(err, store.ErrNotFound) {
				t.Fatal("restore hold allowed claim", err)
			}
		})
	}
}

func TestPostgresRestoreLeasesSettlesDeploymentAttempt(t *testing.T) {
	ctx := t.Context()
	pg, db := openPostgresIntegrationStore(t, ctx, "restore_attempt")
	defer closePostgresStore(t, pg)
	now := time.Now().UTC()
	if _, err := pg.CreateService(ctx, domain.Service{ID: "svc_restore", ProjectID: "proj_platform", Name: "restore", RepositoryID: "repo_platform_runbooks", ComposePath: "compose.yml", Profiles: []string{}, OwnerID: "usr_bootstrap", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateEnvironment(ctx, domain.Environment{ID: "env_restore", ServiceID: "svc_restore", Name: "prod", ComposeProject: "restore", TimeoutSeconds: 60, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.CreateRevision(ctx, domain.Revision{ID: "rev_restore", ServiceID: "svc_restore", RequestedRef: "main", CreatedBy: "usr_bootstrap", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.RegisterRunner(ctx, domain.Runner{ID: "runner_restore", Name: "restore", Tags: []string{"local"}, Capabilities: []string{domain.RunTypeComposeDeploy}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}
	run := raceRun("run_restore_deploy", now)
	run.RunSpec.Type = domain.RunTypeComposeDeploy
	if _, err := pg.CreateDeploymentRequest(ctx, domain.Deployment{ID: "dep_restore", EnvironmentID: "env_restore", DesiredRevisionID: "rev_restore", IdempotencyKey: "restore", Status: domain.DeploymentQueued, RequestedBy: "usr_bootstrap", FenceRequired: true, CreatedAt: now, UpdatedAt: now}, run, destinationEvent("restore_deploy")); err != nil {
		t.Fatal(err)
	}
	first, err := pg.ClaimRun(ctx, "runner_restore", now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	restoreLeases(t, db)
	var attempt, task string
	var finished bool
	if err = db.QueryRow(ctx, `SELECT a.status,a.finished_at IS NOT NULL,r.status FROM deployment_attempts a JOIN task_runs r ON r.id=a.run_id WHERE a.lease_id=$1`, first.Lease.ID).Scan(&attempt, &finished, &task); err != nil || attempt != "failed" || !finished || task != "queued" {
		t.Fatal(attempt, finished, task, err)
	}
	second, err := pg.ClaimRun(ctx, "runner_restore", now, time.Hour)
	if err != nil || second.Lease.Attempt != first.Lease.Attempt+1 || second.Lease.Fence == first.Lease.Fence {
		t.Fatal("reclaim authority", second, err)
	}
	var active int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM deployment_attempts WHERE deployment_id='dep_restore' AND status='active'`).Scan(&active); err != nil || active != 1 {
		t.Fatal("attempts remained active", active, err)
	}
}

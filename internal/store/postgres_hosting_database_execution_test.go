package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/internal/store/sqlcgen"
)

func databaseExecutionFixture(t *testing.T, engine string) (storageMaterializationFixture, *app.Service, context.Context) {
	t.Helper()
	pg, db, service, ctx, input := hostingDatabaseFixture(t, engine)
	view, err := service.CreateHostingDatabase(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	runner := "runner_image_host"
	if _, err = db.Exec(ctx, `UPDATE runners SET capabilities=ARRAY['compose_deploy',$2,$3,$4]::text[] WHERE id=$1`, runner, domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability); err != nil {
		t.Fatal(err)
	}
	row, err := sqlcgen.New(db).ReadHostingStorage(ctx, view.Resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	storage := domain.HostingStorage{ID: row.ID, ProjectID: row.ProjectID, ResourceID: row.ResourceID, BindingID: row.BindingID, DestinationID: row.DestinationID, Kind: row.Kind, Driver: row.Driver, VolumeName: row.VolumeName, MountPath: row.MountPath, AccessMode: row.AccessMode, Retention: row.Retention, Generation: row.Generation, Hash: row.BodyHash, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt}
	return storageMaterializationFixture{pg: pg, db: db, resource: view.Resource, runner: runner, storage: storage}, service, ctx
}

func databaseExecutionInput(key string) domain.HostingDatabaseDeploymentInput {
	return domain.HostingDatabaseDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: key}
}

func prepareDatabaseExecution(t *testing.T, f storageMaterializationFixture, operation domain.HostingOperation) storageMaterializationFixture {
	t.Helper()
	if err := f.pg.PrepareDeployment(t.Context(), operation.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(t.Context(), f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.Type != domain.RunTypeDatabaseDeploy || claim.Run.RunSpec.HostingOperationID != operation.ID {
		t.Fatalf("private database claim: %+v %v", claim, err)
	}
	f.authority = domain.HostingAuthority{OperationID: operation.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
	op, err := f.pg.HostingRunnerPlan(t.Context(), f.runner, f.authority)
	if err != nil || op.TaskMode != "deploy" {
		t.Fatalf("database runner plan: %+v %v", op, err)
	}
	recipe, err := domain.HostingDatabaseRecipeFor(op.Snapshot.Database.Engine, op.Snapshot.Database.RecipeVersion)
	if err != nil {
		t.Fatal(err)
	}
	platform := recipe.Platforms[0]
	release := domain.HostingRelease{CanonicalImage: strings.Split(recipe.ImageReference, "@")[0] + "@" + platform.Digest, ImageID: "sha256:" + strings.Repeat("b", 64), OS: platform.OS, Architecture: platform.Architecture, Variant: platform.Variant, DaemonID: "database_fixture_daemon"}
	release.Hash = domain.HostingDocumentHash(release)
	bad := release
	bad.CanonicalImage = recipe.ImageReference
	bad.Hash = ""
	bad.Hash = domain.HostingDocumentHash(bad)
	if _, err = f.pg.HostingPrepared(t.Context(), f.runner, domain.HostingPreparedRequest{HostingAuthority: f.authority, ReceiptID: "wrong_child", Release: bad}); err == nil {
		t.Fatal("recipe index accepted instead of exact child manifest")
	}
	request := domain.HostingPreparedRequest{HostingAuthority: f.authority, ReceiptID: "prepared_" + operation.ID, Release: release}
	f.op, err = f.pg.HostingPrepared(t.Context(), f.runner, request)
	if err != nil || f.op.TaskMode != "deploy" || f.op.Plan == nil || domain.ValidateHostingDatabasePlan(*f.op.Plan) != nil {
		t.Fatalf("database preparation: %+v %v", f.op, err)
	}
	if replay, e := f.pg.HostingPrepared(t.Context(), f.runner, request); e != nil || !reflect.DeepEqual(replay, f.op) {
		t.Fatalf("prepared replay changed plan: %v", e)
	}
	return f
}

func TestHostingDatabaseExecutionPostgresPrivateLifecycle(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f, service, ctx := databaseExecutionFixture(t, engine)
			input := databaseExecutionInput("database_deployment")
			op, err := service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, input)
			if err != nil || op.Snapshot.HostPort != 0 || op.Snapshot.Database.ConfigurationHash == "" {
				t.Fatalf("private operation: %+v %v", op, err)
			}
			if replay, e := service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, input); e != nil || replay.ID != op.ID {
				t.Fatalf("request replay: %+v %v", replay, e)
			}
			changed := input
			changed.ExpectedGeneration = 2
			if _, err = service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, changed); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("changed request replay: %v", err)
			}
			f = prepareDatabaseExecution(t, f, op)
			stale := f.authority
			stale.Fence += "_stale"
			if _, err = f.pg.HostingApplyBegin(t.Context(), f.runner, stale); err == nil {
				t.Fatal("stale database fence admitted")
			}
			if _, err = f.pg.HostingApplyBegin(t.Context(), f.runner, f.authority); err == nil {
				t.Fatal("database writer admitted without materialized storage")
			}
			ack, err := f.pg.HostingStorageCreateBegin(t.Context(), f.runner, f.request())
			if err != nil || !ack.CreatePermitted {
				t.Fatalf("database storage origin: %+v %v", ack, err)
			}
			if replay, e := f.pg.HostingStorageCreateBegin(t.Context(), f.runner, f.request()); e != nil || replay.CreatePermitted {
				t.Fatalf("storage retry recreated volume: %+v %v", replay, e)
			}
			if _, err = f.pg.HostingStorageMaterialized(t.Context(), f.runner, f.proof(ack)); err != nil {
				t.Fatal(err)
			}
			if _, err = f.pg.HostingApplyBegin(t.Context(), f.runner, f.authority); err != nil {
				t.Fatal(err)
			}
			complete := domain.HostingCompleteRequest{HostingAuthority: f.authority, ReceiptID: "complete_" + op.ID, Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: "database_fixture_container", ImageID: f.op.Release.ImageID, DaemonID: f.op.Release.DaemonID, PlanHash: f.op.Plan.Hash}}
			settled, err := f.pg.HostingComplete(t.Context(), f.runner, complete)
			if err != nil || settled.State != "succeeded" || settled.MutationHold || settled.TaskMode != "deploy" {
				t.Fatalf("database settlement: %+v %v", settled, err)
			}
			if replay, e := f.pg.HostingComplete(t.Context(), f.runner, complete); e != nil || !reflect.DeepEqual(replay, settled) {
				t.Fatalf("complete replay: %v", e)
			}
			var public, markers int
			if err = f.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM hosting_release_slots)+(SELECT count(*) FROM hosting_release_claims)+(SELECT count(*) FROM hosting_retained_releases),(SELECT count(*) FROM hosting_database_deployments)`).Scan(&public, &markers); err != nil || public != 0 || markers != 1 {
				t.Fatalf("private operation acquired public ports: public=%d markers=%d error=%v", public, markers, err)
			}
			metadata, err := service.GetHostingDatabase(ctx, f.resource.ID)
			if err != nil || metadata.RuntimeState != domain.HostingDatabaseUnqualified {
				t.Fatal("controller advertised runtime qualification", err)
			}
			if _, err = service.ReconcileHostingOperation(ctx, op.ID); err != nil {
				t.Fatal(err)
			}
			claim, err := f.pg.ClaimRun(t.Context(), f.runner, time.Now(), time.Minute)
			if err != nil || claim.Run.RunSpec.Type != domain.RunTypeDatabaseDeploy || claim.Run.RunSpec.HostingMode != "observe" {
				t.Fatalf("database observation claim: %+v %v", claim, err)
			}
			a := domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			observed, err := f.pg.HostingRunnerPlan(t.Context(), f.runner, a)
			if err != nil || observed.ReconciliationHealthy == nil || observed.ReconciliationHealthy.Storage == nil {
				t.Fatalf("private healthy observation: %+v %v", observed, err)
			}
			if _, err = f.pg.HostingReconciliation(t.Context(), f.runner, domain.HostingObservation{HostingAuthority: a, BindingID: f.op.BindingID, DaemonID: f.op.Release.DaemonID, ContainerID: complete.Result.ContainerID, ImageID: f.op.Release.ImageID, PlanHash: f.op.Plan.Hash, ReleaseHash: f.op.Release.Hash, Ready: true}); err != nil {
				t.Fatal(err)
			}
			next, err := service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("second_private_writer"))
			if err != nil {
				t.Fatal(err)
			}
			second := prepareDatabaseExecution(t, f, next)
			if second.op.Plan.PreviousHealthy == nil || second.op.Plan.PreviousHealthy.Storage == nil || second.op.Plan.PreviousHealthy.Storage.CreationPlanHash != ack.Materialization.CreationPlanHash || second.op.Plan.Storage.CreationOperationID != op.ID {
				t.Fatal("replacement lost its healthy predecessor or immutable volume origin")
			}
			if reused, e := second.pg.HostingStorageCreateBegin(ctx, second.runner, second.request()); e != nil || reused.CreatePermitted {
				t.Fatalf("database replacement recreated volume: %+v %v", reused, e)
			}
			if _, err = f.pg.RequestHostingReleaseCleanup(ctx, "hop_database_cleanup", f.resource.ID, op.ID, "usr_bootstrap", domain.HostingCleanupInput{ExpectedGeneration: 1, IdempotencyKey: "cleanup"}, destinationEvent("database_cleanup")); !errors.Is(err, store.ErrConflict) {
				t.Fatalf("generic cleanup accepted database: %v", err)
			}
		})
	}
}

func TestHostingDatabaseExecutionPostgresAdmissionRejections(t *testing.T) {
	for _, failure := range []string{"generation", "grant", "capability", "runner", "restore_hold", "viewer", "foreign_project", "audit"} {
		t.Run(failure, func(t *testing.T) {
			f, service, ctx := databaseExecutionFixture(t, "postgresql")
			in := databaseExecutionInput("rejected")
			var err error
			switch failure {
			case "generation":
				in.ExpectedGeneration = 2
			case "grant":
				_, err = f.pg.RevokeDestinationProject(ctx, f.storage.DestinationID, f.resource.ProjectID, destinationEvent("database_revoke"))
			case "capability":
				_, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseCapability)
			case "runner":
				_, err = f.db.Exec(ctx, `UPDATE runners SET status='disabled' WHERE id=$1`, f.runner)
			case "restore_hold":
				_, err = f.db.Exec(ctx, `INSERT INTO hosting_binding_restore_holds(binding_id) VALUES($1)`, f.storage.BindingID)
			case "viewer", "foreign_project":
				project, role := f.resource.ProjectID, domain.RoleViewer
				if failure == "foreign_project" {
					project, role = "proj_security", domain.RoleOwner
				}
				account, e := service.CreateServiceAccount(ctx, app.ServiceAccountCreateInput{ProjectID: project, Name: "database rejection", Role: role})
				if e != nil {
					t.Fatal(e)
				}
				ctx = auth.WithPrincipal(t.Context(), auth.Principal{ID: account.ServiceAccount.ID, Provider: domain.PrincipalServiceAccount})
			case "audit":
				_, err = f.db.Exec(ctx, `CREATE FUNCTION database_execution_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.database_deployment_requested' THEN RAISE EXCEPTION 'database fixture audit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER database_execution_fault AFTER INSERT ON audit_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION database_execution_fault()`)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := hostingDatabaseCounts(t, f.db)
			if _, err = service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, in); err == nil {
				t.Fatal("rejected database deployment accepted")
			}
			if hostingDatabaseCounts(t, f.db) != before {
				t.Fatal("rejection left operation, job or audit")
			}
			var markers, bindings int
			if err = f.db.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM hosting_database_deployments),(SELECT count(*) FROM hosting_binding_state)`).Scan(&markers, &bindings); err != nil || markers != 0 || bindings != 0 {
				t.Fatalf("rejection leaked private metadata: %d %d %v", markers, bindings, err)
			}
		})
	}
}

func TestHostingDatabaseExecutionPostgresConcurrencyAndCurrentReplay(t *testing.T) {
	f, service, ctx := databaseExecutionFixture(t, "redis")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	type result struct {
		op  domain.HostingOperation
		err error
	}
	results := make(chan result, 2)
	exited := make(chan struct{}, 2)
	defer func() {
		cancel()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for range 2 {
			select {
			case <-exited:
			case <-deadline.C:
				t.Error("database replay callers did not exit after cancellation")
				return
			}
		}
	}()
	start := make(chan struct{})
	for range 2 {
		go func() {
			defer func() { exited <- struct{}{} }()
			<-start
			op, err := service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("concurrent"))
			results <- result{op, err}
		}()
	}
	close(start)
	var id string
	for range 2 {
		select {
		case r := <-results:
			if r.err != nil || id != "" && r.op.ID != id {
				t.Fatalf("duplicate private operation: %+v %v", r.op, r.err)
			}
			id = r.op.ID
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	var markers, audits, jobs int
	if err := f.db.QueryRow(ctx, `SELECT (SELECT count(*) FROM hosting_database_deployments),(SELECT count(*) FROM audit_events WHERE action='hosting.database_deployment_requested'),(SELECT count(*) FROM river_job WHERE kind='nerocd_prepare_deployment')`).Scan(&markers, &audits, &jobs); err != nil || markers != 1 || audits != 1 || jobs > 1 {
		t.Fatalf("duplicate persisted work: %d %d %d %v", markers, audits, jobs, err)
	}
	account, err := service.CreateServiceAccount(ctx, app.ServiceAccountCreateInput{ProjectID: f.resource.ProjectID, Name: "another maintainer", Role: domain.RoleMaintainer})
	if err != nil {
		t.Fatal(err)
	}
	other := auth.WithPrincipal(ctx, auth.Principal{ID: account.ServiceAccount.ID, Provider: domain.PrincipalServiceAccount})
	if _, err = service.RequestHostingDatabaseDeployment(other, f.resource.ID, databaseExecutionInput("concurrent")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("different actor replay: %v", err)
	}
	if _, err = f.pg.RevokeDestinationProject(ctx, f.storage.DestinationID, f.resource.ProjectID, destinationEvent("database_replay_revoke")); err != nil {
		t.Fatal(err)
	}
	if _, err = service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("concurrent")); err == nil {
		t.Fatal("revoked grant retained replay authority")
	}
}

func TestHostingDatabaseExecutionPostgresMixedFamilyAndStorageUncertainty(t *testing.T) {
	f, service, ctx := databaseExecutionFixture(t, "postgresql")
	op, err := service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("uncertain"))
	if err != nil {
		t.Fatal(err)
	}
	f = prepareDatabaseExecution(t, f, op)
	if _, err = f.db.Exec(ctx, `UPDATE task_runs SET run_spec=jsonb_set(run_spec,'{type}','"image_deploy"') WHERE id=$1`, f.authority.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingRunnerPlan(ctx, f.runner, f.authority); err == nil {
		t.Fatal("database authority accepted mixed task family")
	}
	if _, err = f.db.Exec(ctx, `UPDATE task_runs SET run_spec=jsonb_set(run_spec,'{type}','"database_deploy"') WHERE id=$1`, f.authority.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingStorageCreateBegin(ctx, f.runner, f.request()); err != nil {
		t.Fatal(err)
	}
	failed := f.complete(t, "failed", true)
	if failed.State != "reconciliation_required" || !failed.MutationHold {
		t.Fatal("database storage uncertainty lost writer hold")
	}
	if _, err = service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("cannot_restart")); err == nil {
		t.Fatal("new private writer bypassed unknown storage")
	}
	for _, query := range []string{
		`INSERT INTO hosting_release_slots(destination_id,host_port,binding_id) VALUES($1,19000,$2)`,
		`UPDATE hosting_database_deployments SET reference='{}' WHERE destination_id=$1 AND binding_id=$2`,
	} {
		// Both statements accept the same target arguments; no caller-selected SQL.
		if _, err = f.db.Exec(ctx, query, f.storage.DestinationID, f.storage.BindingID); err == nil {
			t.Fatal("SQL bypass weakened private operation scope")
		}
	}
	if _, err = f.db.Exec(ctx, `ALTER TABLE hosting_database_deployments DISABLE TRIGGER hosting_database_deployment_consistent`); err != nil {
		t.Fatal(err)
	}
	if ok, e := f.pg.SchemaCompatible(ctx); e != nil || ok {
		t.Fatalf("missing database execution guard remained ready: %v %v", ok, e)
	}
	if _, err = store.OpenPostgres(ctx, f.db.Config().ConnConfig.ConnString()); err == nil {
		t.Fatal("missing database execution guard allowed startup")
	}
}

func TestHostingDatabaseExecutionPostgresDirectSQLFamilyConstraint(t *testing.T) {
	f, service, ctx := databaseExecutionFixture(t, "redis")
	op, err := service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("sql_scope"))
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"public_port", "storage", "engine", "application"} {
		tx, err := f.db.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		rollback := cleanupContinuityTransaction(t, tx)
		id := "hop_sql_" + change
		snapshot := op.Snapshot
		switch change {
		case "public_port":
			snapshot.HostPort = 19000
		case "storage":
			snapshot.Storage.Hash = "sha256:" + strings.Repeat("c", 64)
		case "engine":
			snapshot.Database.Engine = "postgresql"
		}
		resource := f.resource.ID
		if change == "application" {
			if err = tx.QueryRow(ctx, `SELECT id FROM resources WHERE kind='application' LIMIT 1`).Scan(&resource); err != nil {
				t.Fatal(err)
			}
		}
		_, err = tx.Exec(ctx, `INSERT INTO hosting_database_deployments(operation_id,resource_id,binding_id,destination_id,reference,credentials) SELECT $1,$2,binding_id,destination_id,reference,credentials FROM hosting_database_deployments WHERE operation_id=$3`, id, resource, op.ID)
		if err == nil {
			_, err = tx.Exec(ctx, `INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) VALUES($1,'proj_platform',$2,$3,$1,$4,'queued','usr_bootstrap')`, id, resource, f.storage.BindingID, "sha256:"+strings.Repeat("a", 64))
		}
		if err == nil {
			body, e := json.Marshal(snapshot)
			if e != nil {
				t.Fatal(e)
			}
			_, err = tx.Exec(ctx, `INSERT INTO hosting_snapshots(operation_id,body,body_hash) VALUES($1,$2,$3)`, id, body, domain.HostingDocumentHash(snapshot))
		}
		if err == nil {
			err = tx.Commit(ctx)
		} else {
			rollback()
		}
		if err == nil {
			t.Fatal("direct SQL accepted mismatched database family", change)
		}
	}
	var count int
	if err := f.db.QueryRow(ctx, `SELECT count(*) FROM hosting_database_deployments`).Scan(&count); err != nil || count != 1 {
		t.Fatal(fmt.Sprintf("partial family marker remained: %d", count), err)
	}
	// Reading the snapshot never exposes sealed credential bytes.
	if raw, err := json.Marshal(op); err != nil || strings.Contains(string(raw), "ciphertext") {
		t.Fatal("private operation disclosed ciphertext", err)
	}
}

func TestHostingDatabaseExecutionPostgresCurrentAuthorityAfterPreparation(t *testing.T) {
	for _, revoke := range []string{"actor", "grant", "database_capability", "lease"} {
		t.Run(revoke, func(t *testing.T) {
			f, service, ctx := databaseExecutionFixture(t, "redis")
			op, err := service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("current_authority"))
			if err != nil {
				t.Fatal(err)
			}
			f = prepareDatabaseExecution(t, f, op)
			ack, err := f.pg.HostingStorageCreateBegin(ctx, f.runner, f.request())
			if err != nil {
				t.Fatal(err)
			}
			switch revoke {
			case "actor":
				_, err = f.db.Exec(ctx, `UPDATE users SET global_role='user' WHERE id='usr_bootstrap'; UPDATE project_members SET role='viewer' WHERE user_id='usr_bootstrap'`)
			case "grant":
				_, err = f.pg.RevokeDestinationProject(ctx, f.storage.DestinationID, f.resource.ProjectID, destinationEvent("database_grant_revoke"))
			case "database_capability":
				_, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseCapability)
			case "lease":
				_, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, f.authority.LeaseID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.pg.HostingStorageMaterialized(ctx, f.runner, f.proof(ack)); err == nil {
				t.Fatal("revoked database authority published storage receipt")
			}
			if _, err = f.pg.HostingApplyBegin(ctx, f.runner, f.authority); err == nil {
				t.Fatal("revoked database authority admitted writer")
			}
			f.count(t, 1, 1, 0)
		})
	}
}

func TestHostingDatabaseExecutionPostgresExpiryDuringAuditRollsBackQueue(t *testing.T) {
	f, service, admin := databaseExecutionFixture(t, "postgresql")
	principal, _ := proxyLegacyToken(t, f.pg, service, "tok_database_execution_expiry", []string{domain.RoleSystemAdmin})
	ctx, cancel := context.WithTimeout(auth.WithPrincipal(admin, principal), 10*time.Second)
	defer cancel()
	blocker, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rollback := cleanupContinuityTransaction(t, blocker)
	var lock int64
	var pid int
	if err = blocker.QueryRow(ctx, `SELECT hashtextextended(current_schema()||':database-execution-audit',0),pg_backend_pid()`).Scan(&lock, &pid); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lock); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `CREATE FUNCTION database_execution_audit_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.database_deployment_requested' THEN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':database-execution-audit',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER database_execution_audit_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION database_execution_audit_wait()`); err != nil {
		t.Fatal(err)
	}
	var expiry time.Time
	if err = f.db.QueryRow(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1 RETURNING expires_at`, principal.ID).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	before := hostingDatabaseCounts(t, f.db)
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() {
		defer close(exited)
		_, e := service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("expiry"))
		done <- e
	}()
	defer func() {
		cancel()
		rollback()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("expired database admission did not exit after cancellation")
		}
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
		case err = <-done:
			t.Fatalf("database deployment did not reach audit wait: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	if _, err = blocker.Exec(ctx, `SELECT pg_sleep(greatest(0,extract(epoch FROM ($1::timestamptz-clock_timestamp())))+0.05)`, expiry); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err == nil {
			t.Fatal("expired creator committed database operation")
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if hostingDatabaseCounts(t, f.db) != before {
		t.Fatal("expired database admission leaked operation, queue or audit")
	}
}

func TestHostingDatabaseExecutionPostgresFinalExpiryDuringClaimStatusObservation(t *testing.T) {
	for _, phase := range []string{"claim", "status", "observation"} {
		t.Run(phase, func(t *testing.T) {
			f, service, admin := databaseExecutionFixture(t, "redis")
			principal, _ := proxyLegacyToken(t, f.pg, service, "tok_database_final_expiry", []string{domain.RoleSystemAdmin})
			ctx, cancel := context.WithTimeout(auth.WithPrincipal(admin, principal), 10*time.Second)
			defer cancel()
			creator := ctx
			if phase == "observation" {
				creator = admin
			}
			op, err := service.RequestHostingDatabaseDeployment(creator, f.resource.ID, databaseExecutionInput("final_expiry"))
			if err != nil {
				t.Fatal(err)
			}
			if err = f.pg.PrepareDeployment(ctx, op.ID); err != nil {
				t.Fatal(err)
			}
			if phase != "claim" {
				f = prepareDatabaseExecution(t, f, op)
				ack, e := f.pg.HostingStorageCreateBegin(ctx, f.runner, f.request())
				if e != nil {
					t.Fatal(e)
				}
				if _, e = f.pg.HostingStorageMaterialized(ctx, f.runner, f.proof(ack)); e != nil {
					t.Fatal(e)
				}
				if _, e = f.pg.HostingApplyBegin(ctx, f.runner, f.authority); e != nil {
					t.Fatal(e)
				}
				if phase == "observation" {
					f.complete(t, "succeeded", false)
					if _, e = service.ReconcileHostingOperation(ctx, op.ID); e != nil {
						t.Fatal(e)
					}
				}
			}
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			rollback := cleanupContinuityTransaction(t, blocker)
			var pid int
			if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			if phase == "status" {
				if _, err = blocker.Exec(ctx, `SELECT id FROM hosting_operations WHERE id=$1 FOR UPDATE`, op.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(current_schema()||':database-final-audit',0))`); err != nil {
					t.Fatal(err)
				}
				if _, err = f.db.Exec(ctx, `CREATE FUNCTION database_final_audit_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('database.fixture_claim','hosting.observation_claimed') THEN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':database-final-audit',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER database_final_audit_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION database_final_audit_wait()`); err != nil {
					t.Fatal(err)
				}
			}
			var expiry time.Time
			if err = f.db.QueryRow(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1 RETURNING expires_at`, principal.ID).Scan(&expiry); err != nil {
				t.Fatal(err)
			}
			var before string
			counts := `SELECT jsonb_build_array((SELECT count(*) FROM run_leases),(SELECT count(*) FROM audit_events),(SELECT state FROM hosting_operations WHERE id=$1),(SELECT next_attempt FROM task_runs WHERE id=$2))::text`
			run := "run_" + op.ID
			if phase == "observation" {
				run = "observe_" + op.ID
			}
			if err = f.db.QueryRow(ctx, counts, op.ID, run).Scan(&before); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			exited := make(chan struct{})
			go func() {
				defer close(exited)
				var e error
				switch phase {
				case "claim":
					event := destinationEvent("database_final_claim")
					event.Action = "database.fixture_claim"
					_, e = f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute, store.WithAudit(event))
				case "status":
					_, e = f.pg.HostingStatus(ctx, f.runner, domain.HostingStatusRequest{HostingAuthority: f.authority, Phase: "verifying"})
				case "observation":
					_, e = f.pg.ClaimHostingObservation(ctx, f.runner, f.authority, time.Minute)
				}
				done <- e
			}()
			defer func() {
				cancel()
				rollback()
				select {
				case <-exited:
				case <-time.After(5 * time.Second):
					t.Error("database authority call did not exit after cancellation")
				}
			}()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case err = <-done:
					t.Fatalf("database call did not reach expiry wait: %v", err)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-ticker.C:
				}
			}
			if _, err = blocker.Exec(ctx, `SELECT pg_sleep(greatest(0,extract(epoch FROM ($1::timestamptz-clock_timestamp())))+0.05)`, expiry); err != nil {
				t.Fatal(err)
			}
			if err = blocker.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err = <-done:
				if !errors.Is(err, store.ErrConflict) {
					t.Fatalf("expired database actor committed final mutation: %v", err)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			var after string
			if err = f.db.QueryRow(ctx, counts, op.ID, run).Scan(&after); err != nil || after != before {
				t.Fatalf("expiry changed lease/audit/attempt/state: %s -> %s %v", before, after, err)
			}
		})
	}
}

func TestHostingDatabaseExecutionPostgresStatusAndObservationRevocation(t *testing.T) {
	for _, phase := range []string{"status", "observation"} {
		for _, revoke := range []string{"actor", "grant"} {
			t.Run(phase+"/"+revoke, func(t *testing.T) {
				f, service, ctx := databaseExecutionFixture(t, "postgresql")
				op, err := service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("revoked_phase"))
				if err != nil {
					t.Fatal(err)
				}
				f = prepareDatabaseExecution(t, f, op)
				ack, err := f.pg.HostingStorageCreateBegin(ctx, f.runner, f.request())
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.pg.HostingStorageMaterialized(ctx, f.runner, f.proof(ack)); err != nil {
					t.Fatal(err)
				}
				if _, err = f.pg.HostingApplyBegin(ctx, f.runner, f.authority); err != nil {
					t.Fatal(err)
				}
				a := f.authority
				if phase == "observation" {
					f.complete(t, "succeeded", false)
					if _, err = service.ReconcileHostingOperation(ctx, op.ID); err != nil {
						t.Fatal(err)
					}
					claim, e := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
					if e != nil {
						t.Fatal(e)
					}
					a = domain.HostingAuthority{OperationID: op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
				}
				if revoke == "actor" {
					_, err = f.db.Exec(ctx, `UPDATE users SET global_role='user' WHERE id='usr_bootstrap'; UPDATE project_members SET role='viewer' WHERE user_id='usr_bootstrap'`)
				} else {
					_, err = f.pg.RevokeDestinationProject(ctx, f.storage.DestinationID, f.resource.ProjectID, destinationEvent("database_phase_revoke"))
				}
				if err != nil {
					t.Fatal(err)
				}
				if phase == "status" {
					if _, err = f.pg.HostingStatus(ctx, f.runner, domain.HostingStatusRequest{HostingAuthority: a, Phase: "verifying"}); err == nil {
						t.Fatal("revoked database actor advanced status")
					}
					current, e := f.pg.GetHostingOperation(ctx, op.ID)
					if e != nil || current.State != "applying" {
						t.Fatalf("rejected status left writes: %+v %v", current, e)
					}
				} else {
					if _, err = f.pg.HostingRunnerPlan(ctx, f.runner, a); err == nil {
						t.Fatal("revoked database observer read a new authoritative plan")
					}
					observation := domain.HostingObservation{HostingAuthority: a, BindingID: f.op.BindingID, DaemonID: f.op.Release.DaemonID, ContainerID: "container_" + f.op.ID, ImageID: f.op.Release.ImageID, PlanHash: f.op.Plan.Hash, ReleaseHash: f.op.Release.Hash, Ready: true}
					if _, err = f.pg.HostingReconciliation(ctx, f.runner, observation); err == nil {
						t.Fatal("revoked database observer settled current evidence")
					}
				}
			})
		}
	}
}

func TestHostingDatabaseExecutionPostgresStatusRechecksAuthorityAfterRunnerLock(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, change := range []string{"actor_expiry", "database_capability", "storage_capability"} {
			t.Run(engine+"/"+change, func(t *testing.T) {
				f, service, admin := databaseExecutionFixture(t, engine)
				principal, _ := proxyLegacyToken(t, f.pg, service, "tok_database_runner_wait", []string{domain.RoleSystemAdmin})
				ctx, cancel := context.WithTimeout(auth.WithPrincipal(admin, principal), 10*time.Second)
				defer cancel()
				op, err := service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("runner_wait"))
				if err != nil {
					t.Fatal(err)
				}
				f = prepareDatabaseExecution(t, f, op)
				ack, err := f.pg.HostingStorageCreateBegin(ctx, f.runner, f.request())
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.pg.HostingStorageMaterialized(ctx, f.runner, f.proof(ack)); err != nil {
					t.Fatal(err)
				}
				if _, err = f.pg.HostingApplyBegin(ctx, f.runner, f.authority); err != nil {
					t.Fatal(err)
				}
				blocker, err := f.db.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				rollback := cleanupContinuityTransaction(t, blocker)
				var pid int
				if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
					t.Fatal(err)
				}
				if _, err = blocker.Exec(ctx, `SELECT id FROM runners WHERE id=$1 FOR UPDATE`, f.runner); err != nil {
					t.Fatal(err)
				}
				var expiry time.Time
				if change == "actor_expiry" {
					if err = f.db.QueryRow(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1 RETURNING expires_at`, principal.ID).Scan(&expiry); err != nil {
						t.Fatal(err)
					}
				}
				done := make(chan error, 1)
				exited := make(chan struct{})
				go func() {
					defer close(exited)
					_, e := f.pg.HostingStatus(ctx, f.runner, domain.HostingStatusRequest{HostingAuthority: f.authority, Phase: "verifying"})
					done <- e
				}()
				defer func() {
					cancel()
					rollback()
					select {
					case <-exited:
					case <-time.After(5 * time.Second):
						t.Error("database status did not exit after cancellation")
					}
				}()
				ticker := time.NewTicker(10 * time.Millisecond)
				defer ticker.Stop()
				for {
					var waiting bool
					if err = f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%LockHostingExactRunner%')`, pid).Scan(&waiting); err != nil {
						t.Fatal(err)
					}
					if waiting {
						break
					}
					select {
					case err = <-done:
						t.Fatalf("database status did not reach runner lock: %v", err)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-ticker.C:
					}
				}
				if change == "actor_expiry" {
					_, err = blocker.Exec(ctx, `SELECT pg_sleep(greatest(0,extract(epoch FROM ($1::timestamptz-clock_timestamp())))+0.05)`, expiry)
				} else {
					capability := domain.HostingDatabaseCapability
					if change == "storage_capability" {
						capability = domain.HostingOwnedStorageCapability
					}
					_, err = blocker.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, capability)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = blocker.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case err = <-done:
					if !errors.Is(err, store.ErrConflict) {
						t.Fatalf("database status used stale authority after runner wait: %v", err)
					}
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				current, err := f.pg.GetHostingOperation(ctx, op.ID)
				if err != nil || current.State != "applying" || !current.MutationHold {
					t.Fatalf("stale database status was not rolled back: %+v %v", current, err)
				}
			})
		}
	}
}

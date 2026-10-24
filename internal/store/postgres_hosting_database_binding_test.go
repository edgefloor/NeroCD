package store_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/internal/store/sqlcgen"
)

type databaseBindingControlFixture struct {
	storageMaterializationFixture
	service *app.Service
	ctx     context.Context
	app     domain.Resource
}

func newDatabaseBindingControlFixture(t *testing.T, engine string) databaseBindingControlFixture {
	t.Helper()
	f, service, actor := databaseExecutionFixture(t, engine)
	ctx, cancel := context.WithTimeout(actor, 45*time.Second)
	t.Cleanup(cancel)
	op, err := service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("binding_database"))
	if err != nil {
		t.Fatal("database admission fixture failed", err)
	}
	f = prepareDatabaseExecution(t, f, op)
	ack, err := f.pg.HostingStorageCreateBegin(ctx, f.runner, f.request())
	if err != nil {
		t.Fatal("database storage intent fixture failed", err)
	}
	if _, err = f.pg.HostingStorageMaterialized(ctx, f.runner, f.proof(ack)); err != nil {
		t.Fatal("database materialization fixture failed", err)
	}
	if _, err = f.pg.HostingApplyBegin(ctx, f.runner, f.authority); err != nil {
		t.Fatal("database apply fixture failed", err)
	}
	result := domain.HostingCompleteRequest{HostingAuthority: f.authority, ReceiptID: "binding_database_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: "database_binding_fixture_container", ImageID: f.op.Release.ImageID, DaemonID: f.op.Release.DaemonID, PlanHash: f.op.Plan.Hash}}
	if _, err = f.pg.HostingComplete(ctx, f.runner, result); err != nil {
		t.Fatal("database healthy fixture failed", err)
	}
	application, err := f.pg.GetResource(ctx, "resource_image_web")
	if err != nil {
		t.Fatal("application fixture unavailable", err)
	}
	return databaseBindingControlFixture{storageMaterializationFixture: f, service: service, ctx: ctx, app: application}
}

func (f databaseBindingControlFixture) input(request int, runtime int64) domain.HostingDatabaseBindingInput {
	return domain.HostingDatabaseBindingInput{DatabaseResourceID: f.resource.ID, ExpectedResourceGeneration: f.app.Generation, ExpectedRuntimeGeneration: runtime, ExpectedDatabaseGeneration: f.resource.Generation, RequestID: fmt.Sprintf("%032x", request)}
}

func (f databaseBindingControlFixture) put(input domain.HostingDatabaseBindingInput) (domain.HostingDatabaseBinding, error) {
	return f.pg.PutHostingDatabaseBinding(f.ctx, "usr_bootstrap", f.app.ID, input, destinationEvent("binding_save"))
}

func bindingControlCounts(t *testing.T, db *pgxpool.Pool) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var counts string
	if err := db.QueryRow(ctx, `SELECT jsonb_build_array((SELECT count(*) FROM hosting_database_bindings),(SELECT count(*) FROM hosting_database_binding_receipts),(SELECT count(*) FROM hosting_runtime_configuration_versions),(SELECT count(*) FROM hosting_runtime_configuration_receipts),(SELECT count(*) FROM audit_events WHERE metadata->>'purpose'='database_binding_pending'))::text`).Scan(&counts); err != nil {
		t.Fatal("binding count observation failed", err)
	}
	return counts
}

func TestPostgresHostingDatabaseBindingProfilesReplayAndPending(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, prior := range []int64{0, 1} {
			t.Run(fmt.Sprintf("%s/runtime_%d", engine, prior), func(t *testing.T) {
				f := newDatabaseBindingControlFixture(t, engine)
				if prior > 0 {
					if _, err := f.pg.PutHostingRuntimeConfiguration(f.ctx, "usr_bootstrap", f.app.ID, runtimeInput(1, 0), destinationEvent("prior_runtime")); err != nil {
						t.Fatal("prior runtime fixture failed", err)
					}
				}
				input := f.input(2, prior)
				binding, err := f.put(input)
				if err != nil || binding.Validate() != nil || binding.Credential.Role != "app" || binding.RuntimeConfiguration.Generation != prior+1 || binding.NetworkName != f.op.Plan.Database.NetworkName {
					t.Fatal("pending binding profile save failed", err)
				}
				counts := bindingControlCounts(t, f.db)
				replay, err := f.put(input)
				if err != nil || replay.Hash != binding.Hash || bindingControlCounts(t, f.db) != counts {
					t.Fatal("identical retry did not return original binding after runtime CAS", err)
				}
				if got, err := f.pg.GetHostingDatabaseBinding(f.ctx, "usr_bootstrap", f.app.ID); err != nil || got.Hash != binding.Hash {
					t.Fatal("desired binding read failed", err)
				}
				runtime, err := f.pg.GetHostingRuntimeConfiguration(f.ctx, "usr_bootstrap", f.app.ID)
				if err != nil || runtime.RuntimeGeneration != prior+1 || len(runtime.Entries) != len(binding.EnvironmentNames)+int(prior) {
					t.Fatal("injected runtime profile or preserved entry changed", err)
				}
				seen := []string{}
				for _, entry := range runtime.Entries {
					if entry.Name == "APP" {
						if entry.Value == nil || *entry.Value != " viewer-readable " {
							t.Fatal("unrelated literal not preserved")
						}
						continue
					}
					seen = append(seen, entry.Name)
					if strings.HasSuffix(entry.Name, "PASSWORD") {
						if entry.Kind != "managed_secret" || entry.Value != nil || entry.Secret == nil || entry.Secret.SecretID != binding.Credential.SecretID || entry.Secret.Version != binding.Credential.Version {
							t.Fatal("password was not the exact app-role reference")
						}
					}
				}
				if !slices.Equal(seen, binding.EnvironmentNames) {
					t.Fatal("generated names differ from closed engine profile")
				}
				for _, changed := range []domain.HostingDatabaseBindingInput{f.input(3, prior+1), f.input(2, prior+1)} {
					if _, err = f.put(changed); !errors.Is(err, store.ErrConflict) {
						t.Fatal("changed/new request did not conflict", err)
					}
				}
				if _, err = f.db.Exec(f.ctx, `INSERT INTO project_members(id,project_id,user_id,role) VALUES('binding_other_owner','proj_platform','usr_viewer','owner')`); err != nil {
					t.Fatal("second owner fixture failed", err)
				}
				if _, err = f.pg.PutHostingDatabaseBinding(f.ctx, "usr_viewer", f.app.ID, input, destinationEvent("other_actor")); !errors.Is(err, store.ErrConflict) {
					t.Fatal("changed actor reused owner request")
				}
				if _, err = f.db.Exec(f.ctx, `UPDATE project_members SET role='viewer' WHERE id='binding_other_owner'`); err != nil {
					t.Fatal("viewer fixture failed", err)
				}
				if got, err := f.pg.GetHostingDatabaseBinding(f.ctx, "usr_viewer", f.app.ID); err != nil || got.Hash != binding.Hash {
					t.Fatal("current viewer could not read desired binding", err)
				}
				if _, err = f.pg.PutHostingRuntimeConfiguration(f.ctx, "usr_bootstrap", f.app.ID, runtimeInput(4, prior+1), destinationEvent("forbidden_runtime")); !errors.Is(err, store.ErrConflict) {
					t.Fatal("new generic runtime version bypassed pending binding", err)
				}
				if prior > 0 {
					if old, err := f.pg.PutHostingRuntimeConfiguration(f.ctx, "usr_bootstrap", f.app.ID, runtimeInput(1, 0), destinationEvent("old_runtime")); err != nil || old.RuntimeGeneration != 1 {
						t.Fatal("historical runtime receipt replay was blocked", err)
					}
				}
				if _, err = f.pg.RequestHostingDeployment(f.ctx, "pending_app_deploy", f.app.ID, "usr_bootstrap", hostingDeploymentRequest(), destinationEvent("pending_deploy")); !errors.Is(err, store.ErrConflict) {
					t.Fatal("new deployment bypassed pending binding", err)
				}
				if _, err = f.db.Exec(f.ctx, `INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) VALUES('pending_sql',$1,$2,$3,'pending_sql',$4,'queued','usr_bootstrap')`, binding.ProjectID, f.app.ID, binding.ApplicationBindingID, binding.Hash); err == nil {
					t.Fatal("direct SQL deployment bypassed pending binding")
				}
				if _, err = sqlcgen.New(f.db).InsertHostingRuntimeVersion(f.ctx, sqlcgen.InsertHostingRuntimeVersionParams{ResourceID: f.app.ID, ProjectID: binding.ProjectID, Generation: prior + 2, Body: []byte(`{"entries":[]}`), BodyHash: binding.Hash, CreatedBy: "usr_bootstrap"}); err == nil {
					t.Fatal("direct SQL runtime bypassed pending binding")
				}
				if bindingControlCounts(t, f.db) != counts {
					t.Fatal("rejected pending effects left partial state")
				}
			})
		}
	}
}

func TestPostgresHostingDatabaseBindingConcurrentAtomicSave(t *testing.T) {
	f := newDatabaseBindingControlFixture(t, "postgresql")
	var group sync.WaitGroup
	results := make(chan error, 6)
	for range 6 {
		group.Go(func() {
			_, err := f.put(f.input(2, 0))
			results <- err
		})
	}
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal("concurrent exact retry failed", err)
		}
	}
	if bindingControlCounts(t, f.db) != "[1, 1, 1, 1, 1]" {
		t.Fatal("concurrent request was not one atomic binding/version/receipt/audit")
	}
}

func TestPostgresHostingDatabaseBindingPreservesDatabaseV1Redeploy(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f := newDatabaseBindingControlFixture(t, engine)
			input := f.input(2, 0)
			binding, err := f.put(input)
			if err != nil {
				t.Fatal("pending binding fixture failed", err)
			}
			next, err := f.service.RequestHostingDatabaseDeployment(f.ctx, f.resource.ID, databaseExecutionInput("binding_database_redeploy"))
			if err != nil {
				t.Fatal("pending app binding blocked database redeploy", err)
			}
			second := prepareDatabaseExecution(t, f.storageMaterializationFixture, next)
			if second.op.Plan.PreviousHealthy == nil || second.op.Plan.Database.Reference != binding.Database || second.op.Plan.Database.NetworkName != binding.NetworkName || second.op.Plan.Storage.CreationOperationID != f.op.Plan.Storage.CreationOperationID {
				t.Fatal("database redeploy changed immutable credentials/network/volume")
			}
			ack, err := second.pg.HostingStorageCreateBegin(f.ctx, second.runner, second.request())
			if err != nil || ack.CreatePermitted {
				t.Fatal("database redeploy tried to recreate owned storage", err)
			}
			if _, err = second.pg.HostingStorageMaterialized(f.ctx, second.runner, second.proof(ack)); err != nil {
				t.Fatal("database redeploy materialization acknowledgement failed", err)
			}
			if _, err = second.pg.HostingApplyBegin(f.ctx, second.runner, second.authority); err != nil {
				t.Fatal("pending application binding blocked database apply", err)
			}
			result := domain.HostingCompleteRequest{HostingAuthority: second.authority, ReceiptID: "binding_database_redeploy_complete", Result: domain.HostingResult{Outcome: "succeeded", Ready: true, ContainerID: "database_binding_fixture_replacement", ImageID: second.op.Release.ImageID, DaemonID: second.op.Release.DaemonID, PlanHash: second.op.Plan.Hash}}
			if _, err = second.pg.HostingComplete(f.ctx, second.runner, result); err != nil {
				t.Fatal("database redeploy completion failed", err)
			}
			if replay, err := f.put(input); err != nil || replay.Hash != binding.Hash {
				t.Fatal("historical binding proof did not survive database redeploy", err)
			}
		})
	}
}

func TestPostgresHostingDatabaseBindingRejectionsAndRollback(t *testing.T) {
	for _, denial := range []string{"collision", "stale_app", "stale_database", "stale_runtime", "viewer", "revoked_grant", "revoked_secret", "active_operation", "mutation_hold", "restore_hold", "foreign_project", "foreign_destination", "missing_healthy", "audit_failure"} {
		t.Run(denial, func(t *testing.T) {
			f := newDatabaseBindingControlFixture(t, "postgresql")
			input, actor := f.input(2, 0), "usr_bootstrap"
			exec := func(query string, args ...any) {
				t.Helper()
				if _, err := f.db.Exec(f.ctx, query, args...); err != nil {
					t.Fatal("denial fixture setup failed", err)
				}
			}
			switch denial {
			case "collision":
				prior := runtimeInput(1, 0)
				prior.Entries[0].Name = "PGHOST"
				if _, err := f.pg.PutHostingRuntimeConfiguration(f.ctx, actor, f.app.ID, prior, destinationEvent("collision")); err != nil {
					t.Fatal("collision fixture failed", err)
				}
				input.ExpectedRuntimeGeneration = 1
			case "stale_app":
				input.ExpectedResourceGeneration++
			case "stale_database":
				input.ExpectedDatabaseGeneration++
			case "stale_runtime":
				input.ExpectedRuntimeGeneration++
			case "viewer":
				exec(`INSERT INTO project_members(id,project_id,user_id,role) VALUES('binding_viewer','proj_platform','usr_viewer','viewer')`)
				actor = "usr_viewer"
			case "revoked_grant":
				exec(`UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, f.op.Snapshot.DestinationID)
			case "revoked_secret":
				item, record, event := managedFixture(t, "binding_prior_secret", 1)
				if _, err := f.pg.CreateManagedSecret(f.ctx, item, record, event); err != nil {
					t.Fatal("prior secret fixture failed", err)
				}
				prior := runtimeInput(1, 0)
				prior.Entries = []domain.HostingRuntimeEntry{{Name: "PRIOR_SECRET", Kind: "managed_secret", Secret: &domain.HostingRuntimeSecretReference{SecretID: item.ID, Version: 1}}}
				if _, err := f.pg.PutHostingRuntimeConfiguration(f.ctx, actor, f.app.ID, prior, destinationEvent("prior_secret")); err != nil {
					t.Fatal("prior secret runtime fixture failed", err)
				}
				exec(`UPDATE project_managed_secrets SET status='revoked',revoked_at=clock_timestamp() WHERE id=$1`, item.ID)
				input.ExpectedRuntimeGeneration = 1
			case "active_operation":
				if _, err := f.service.RequestHostingDatabaseDeployment(f.ctx, f.resource.ID, databaseExecutionInput("binding_active")); err != nil {
					t.Fatal("active database operation fixture failed", err)
				}
			case "mutation_hold":
				exec(`UPDATE hosting_binding_state SET hold_operation_id=$1 WHERE binding_id=$2`, f.op.ID, f.op.BindingID)
			case "restore_hold":
				exec(`INSERT INTO hosting_binding_restore_holds(binding_id) VALUES($1)`, f.op.BindingID)
			case "foreign_project", "foreign_destination":
				project, destination := "proj_security", f.op.Snapshot.DestinationID
				if denial == "foreign_destination" {
					project = f.app.ProjectID
					_, target := createDestinationTarget(t, f.pg, "binding_other")
					destination = target.ID
				}
				f.app = createDestinationResource(t, f.pg, "binding_other", project)
				grantDestination(t, f.pg, destination, project, "binding_other_grant")
				if _, err := f.pg.BindResourceDestination(f.ctx, store.ResourceBindingInput{ID: "binding_other_app", ResourceID: f.app.ID, DestinationID: destination, CreatedBy: actor, CreatedAt: time.Now().UTC()}, destinationEvent("binding_other_app")); err != nil {
					t.Fatal("foreign target fixture failed", err)
				}
			case "missing_healthy":
				exec(`UPDATE hosting_binding_state SET healthy=NULL WHERE binding_id=$1`, f.op.BindingID)
			case "audit_failure":
				exec(`CREATE FUNCTION binding_audit_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.metadata->>'purpose'='database_binding_pending' THEN RAISE EXCEPTION 'binding audit failure fixture' USING ERRCODE='23514'; END IF; RETURN NEW; END $$; CREATE TRIGGER binding_audit_failure BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION binding_audit_failure()`)
			}
			before := bindingControlCounts(t, f.db)
			if _, err := f.pg.PutHostingDatabaseBinding(f.ctx, actor, f.app.ID, input, destinationEvent("denied_binding")); err == nil {
				t.Fatal("invalid binding admission succeeded")
			}
			if bindingControlCounts(t, f.db) != before {
				t.Fatal("denied binding left partial runtime/binding/receipt/audit state")
			}
		})
	}
}

func TestPostgresHostingDatabaseBindingSQLCredentialAndHashBackstop(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		for _, alteration := range []string{"credential_role", "hash", "generated_entry"} {
			t.Run(engine+"/"+alteration, func(t *testing.T) {
				f := newDatabaseBindingControlFixture(t, engine)
				ddl := `CREATE FUNCTION binding_corruption() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN `
				table := "hosting_database_bindings"
				switch alteration {
				case "credential_role":
					role := "admin"
					if engine == "redis" {
						role = "probe"
					}
					ddl += fmt.Sprintf(`NEW.body:=jsonb_set(NEW.body,'{credential,role}','"%s"'); NEW.body_hash:=nerocd_database_binding_hash(NEW.body); NEW.body:=jsonb_set(NEW.body,'{hash}',to_jsonb(NEW.body_hash)); `, role)
				case "hash":
					ddl += `NEW.body_hash:='sha256:'||repeat('0',64); NEW.body:=jsonb_set(NEW.body,'{hash}',to_jsonb(NEW.body_hash)); `
				case "generated_entry":
					table = "hosting_runtime_configuration_versions"
					ddl += `NEW.body:=jsonb_set(NEW.body,'{entries,0,value}','"unapproved-host"'); `
				}
				ddl += fmt.Sprintf(`RETURN NEW; END $$; CREATE TRIGGER binding_corruption BEFORE INSERT ON %s FOR EACH ROW EXECUTE FUNCTION binding_corruption()`, table)
				if _, err := f.db.Exec(f.ctx, ddl); err != nil {
					t.Fatal("SQL backstop fixture failed", err)
				}
				before := bindingControlCounts(t, f.db)
				if _, err := f.put(f.input(2, 0)); !errors.Is(err, store.ErrConflict) || bindingControlCounts(t, f.db) != before {
					t.Fatal("SQL canonical/credential/generated-entry backstop failed", err)
				}
			})
		}
	}
}

func TestPostgresHostingDatabaseBindingPendingRowFamiliesAndHistoricalReplay(t *testing.T) {
	f := newDatabaseBindingControlFixture(t, "postgresql")
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_append(capabilities,'image_deploy') WHERE id=$1`, f.runner); err != nil {
		t.Fatal("image capability fixture failed", err)
	}
	in := hostingDeploymentRequest()
	history, err := f.pg.RequestHostingDeployment(f.ctx, "binding_app_history", f.app.ID, "usr_bootstrap", in, destinationEvent("binding_app_history"))
	if err != nil || f.pg.PrepareDeployment(f.ctx, history.ID) != nil {
		t.Fatal("historical app dispatch fixture failed", err)
	}
	history, err = f.pg.GetHostingOperation(f.ctx, history.ID)
	if err != nil || history.RunID == "" {
		t.Fatal("historical app target fixture failed", err)
	}
	if _, err = f.pg.CancelHostingOperation(f.ctx, history.ID, destinationEvent("cancel_app_history")); err != nil {
		t.Fatal("historical app cancellation fixture failed", err)
	}
	if _, err = f.put(f.input(2, 0)); err != nil {
		t.Fatal("pending fixture save failed", err)
	}
	if old, err := f.pg.RequestHostingDeployment(f.ctx, "ignored_historical_id", f.app.ID, "usr_bootstrap", in, destinationEvent("historical_replay")); err != nil || old.ID != history.ID {
		t.Fatal("historical admission replay was not preserved", err)
	}
	for name, statement := range map[string]string{
		"operation": `INSERT INTO hosting_operations SELECT (jsonb_populate_record(NULL::hosting_operations,to_jsonb(o)||jsonb_build_object('id','pending_operation','idempotency_key','pending_operation'))).* FROM hosting_operations o WHERE id=$1`,
		"release":   `INSERT INTO hosting_releases SELECT (jsonb_populate_record(NULL::hosting_releases,to_jsonb(r)||jsonb_build_object('operation_id',$1::text))).* FROM hosting_releases r WHERE operation_id=$2`,
		"plan":      `INSERT INTO hosting_apply_plans SELECT (jsonb_populate_record(NULL::hosting_apply_plans,to_jsonb(p)||jsonb_build_object('operation_id',$1::text))).* FROM hosting_apply_plans p WHERE operation_id=$2`,
		"admission": `INSERT INTO hosting_apply_admissions SELECT (jsonb_populate_record(NULL::hosting_apply_admissions,to_jsonb(a)||jsonb_build_object('operation_id',$1::text))).* FROM hosting_apply_admissions a WHERE operation_id=$2`,
		"target":    `INSERT INTO hosting_task_targets SELECT (jsonb_populate_record(NULL::hosting_task_targets,to_jsonb(t)||jsonb_build_object('operation_id',$1::text))).* FROM hosting_task_targets t WHERE operation_id=$2 AND mode='deploy'`,
		"lease":     `INSERT INTO run_leases SELECT (jsonb_populate_record(NULL::run_leases,to_jsonb(l)||jsonb_build_object('id','pending_lease','run_id',$3::text))).* FROM run_leases l WHERE id=$4`,
		"delivery":  `INSERT INTO audit_events SELECT (jsonb_populate_record(NULL::audit_events,to_jsonb(a)||jsonb_build_object('id','pending_delivery','action','hosting.runtime_environment_authorized','target_id',$1::text))).* FROM audit_events a LIMIT 1`,
	} {
		t.Run(name, func(t *testing.T) {
			// All placeholders are typed, even when a family does not use one.
			query := `WITH arguments AS (SELECT $1::text,$2::text,$3::text,$4::text) ` + statement
			_, err := f.db.Exec(f.ctx, query, history.ID, f.op.ID, history.RunID, f.authority.LeaseID)
			var rejection *pgconn.PgError
			if !errors.As(err, &rejection) || rejection.Code != "23514" || rejection.Message != "database binding connectivity is pending" {
				t.Fatal("guarded row family did not reach the closed pending rejection", err)
			}
		})
	}
}

func TestPostgresHostingDatabaseBindingAuthorityAfterWait(t *testing.T) {
	for _, path := range []string{"audit", "deferred", "replay", "read", "member_revocation", "grant_revocation"} {
		t.Run(path, func(t *testing.T) {
			f := newDatabaseBindingControlFixture(t, "postgresql")
			ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
			defer cancel()
			principal, _ := proxyLegacyToken(t, f.pg, f.service, "tok_binding_wait", []string{domain.RoleSystemAdmin})
			actorID := principal.ID
			input := f.input(2, 0)
			call := func() error {
				_, err := f.pg.PutHostingDatabaseBinding(ctx, actorID, f.app.ID, input, destinationEvent("binding_wait"))
				return err
			}
			if path == "replay" || path == "read" {
				if _, err := f.pg.PutHostingDatabaseBinding(ctx, actorID, f.app.ID, input, destinationEvent("binding_wait")); err != nil {
					t.Fatal("historical authority fixture failed", err)
				}
				if path == "read" {
					call = func() error { _, err := f.pg.GetHostingDatabaseBinding(ctx, actorID, f.app.ID); return err }
				}
			}
			before := bindingControlCounts(t, f.db)
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal("authority blocker setup failed", err)
			}
			joined, started := false, false
			done := make(chan error, 1)
			defer func() {
				cancel()
				cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
				defer stop()
				if err := blocker.Rollback(cleanup); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
					t.Error("authority blocker rollback failed")
				}
				if started && !joined {
					select {
					case <-done:
					case <-cleanup.Done():
						t.Error("authority request did not join")
					}
				}
			}()
			var key int64
			var pid int
			if blocker.QueryRow(ctx, `SELECT hashtextextended(current_schema()||':binding-wait',0),pg_backend_pid()`).Scan(&key, &pid) != nil {
				t.Fatal("authority blocker identity unavailable")
			}
			switch path {
			case "audit", "deferred":
				if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
					t.Fatal("authority wait lock failed", err)
				}
				ddl := `CREATE FUNCTION binding_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.metadata->>'purpose'='database_binding_pending' THEN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':binding-wait',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER binding_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION binding_wait()`
				if path == "deferred" {
					ddl = `CREATE FUNCTION binding_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':binding-wait',0)); RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER binding_wait AFTER INSERT ON hosting_database_bindings DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION binding_wait()`
				}
				if _, err = f.db.Exec(ctx, ddl); err != nil {
					t.Fatal("authority barrier fixture failed", err)
				}
			case "replay":
				_, err = blocker.Exec(ctx, `LOCK TABLE hosting_database_binding_receipts IN ACCESS EXCLUSIVE MODE`)
			case "read":
				_, err = blocker.Exec(ctx, `LOCK TABLE hosting_database_bindings IN ACCESS EXCLUSIVE MODE`)
			case "member_revocation", "grant_revocation":
				if _, err = f.db.Exec(ctx, `INSERT INTO project_members(id,project_id,user_id,role) VALUES('binding_wait_owner','proj_platform','usr_viewer','owner')`); err != nil {
					t.Fatal("waiting owner fixture failed", err)
				}
				actorID = "usr_viewer"
				_, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('nerocd-hosting-destination:'||$1::text,0))`, f.op.Snapshot.DestinationID)
			}
			if err != nil {
				t.Fatal("authority barrier lock failed", err)
			}
			var expiry time.Time
			if path != "member_revocation" && path != "grant_revocation" {
				if f.db.QueryRow(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '700 milliseconds' WHERE id=$1 RETURNING expires_at`, principal.ID).Scan(&expiry) != nil {
					t.Fatal("token expiry fixture failed")
				}
			}
			started = true
			go func() { done <- call() }()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				var waiting bool
				if f.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting) != nil {
					t.Fatal("authority wait observation failed")
				}
				if waiting {
					break
				}
				select {
				case <-done:
					joined = true
					t.Fatal("authority request did not reach its barrier")
				case <-ctx.Done():
					t.Fatal("authority barrier deadline")
				case <-ticker.C:
				}
			}
			switch path {
			case "member_revocation":
				_, err = f.db.Exec(ctx, `DELETE FROM project_members WHERE id='binding_wait_owner'`)
			case "grant_revocation":
				_, err = f.db.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1`, f.op.Snapshot.DestinationID)
			default:
				for {
					var expired bool
					if f.db.QueryRow(ctx, `SELECT clock_timestamp()>=$1`, expiry).Scan(&expired) != nil {
						t.Fatal("expiry clock observation failed")
					}
					if expired {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("expiry clock deadline")
					case <-ticker.C:
					}
				}
			}
			if err != nil || blocker.Commit(ctx) != nil {
				t.Fatal("authority blocker release failed")
			}
			select {
			case err = <-done:
				joined = true
				if err == nil {
					t.Fatal("expired or revoked authority survived its final wait")
				}
			case <-ctx.Done():
				t.Fatal("authority request join deadline")
			}
			if bindingControlCounts(t, f.db) != before {
				t.Fatal("lost authority published partial binding state")
			}
		})
	}
}

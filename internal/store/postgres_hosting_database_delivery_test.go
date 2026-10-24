package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingdatabaseprotocol"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store/sqlcgen"
)

func databaseCredentialFixture(t *testing.T, engine string) (storageMaterializationFixture, *app.Service, context.Context, domain.HostingDatabaseCredentialRequest) {
	t.Helper()
	f, service, ctx := databaseExecutionFixture(t, engine)
	op, err := service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("credential_delivery"))
	if err != nil {
		t.Fatal(err)
	}
	f = prepareDatabaseExecution(t, f, op)
	return f, service, ctx, domain.HostingDatabaseCredentialRequest{Authority: f.authority, DeliveryID: strings.Repeat("a", 32), PlanHash: f.op.Plan.Hash, Mode: "deploy"}
}

func databaseCredentialHash() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte("image-hosting-fixture-token")))
}

func databaseCredentialAuditCount(t *testing.T, f storageMaterializationFixture) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='hosting.database_credentials_authorized'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func denyDatabaseCredential(t *testing.T, f storageMaterializationFixture, in domain.HostingDatabaseCredentialRequest) {
	t.Helper()
	before := hostingDatabaseCounts(t, f.db)
	got, err := f.pg.AuthorizeHostingDatabaseCredentials(t.Context(), f.runner, databaseCredentialHash(), in)
	if err == nil {
		got.Close()
		t.Fatal("invalid authority released encrypted records")
	}
	defer got.Close()
	if len(got.Records) != 0 {
		t.Fatal("invalid authority released encrypted records")
	}
	if hostingDatabaseCounts(t, f.db) != before {
		t.Fatal("rejected delivery left writes")
	}
}

func TestPostgresDatabaseCredentialDeliveryLifecycle(t *testing.T) {
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			f, service, ctx, in := databaseCredentialFixture(t, engine)
			// Volume admission/materialization may finish before credential fetch.
			ack, err := f.pg.HostingStorageCreateBegin(ctx, f.runner, f.request())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.pg.HostingStorageMaterialized(ctx, f.runner, f.proof(ack)); err != nil {
				t.Fatal(err)
			}
			first, err := f.pg.AuthorizeHostingDatabaseCredentials(ctx, f.runner, databaseCredentialHash(), in)
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close()
			if len(first.Records) != 2 || first.Metadata.Database != f.op.Snapshot.Database || first.Metadata.RequestHash != hostingdatabaseprotocol.RequestHash(f.runner, in) {
				t.Fatal("incorrect immutable credential bindings")
			}
			for ref, record := range first.Records {
				plain, e := managedRing(t).Open(secretBinding(first.ProjectID, ref), record.Envelope)
				if e != nil || len(plain) != 43 {
					t.Fatal("wrong encrypted credential")
				}
				clear(plain)
			}
			if _, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, in.Authority.LeaseID); err != nil {
				t.Fatal(err)
			}
			replay, err := f.pg.AuthorizeHostingDatabaseCredentials(ctx, f.runner, databaseCredentialHash(), in)
			if err != nil {
				t.Fatal(err)
			}
			defer replay.Close()
			if !replay.Metadata.UseUntil.Equal(first.Metadata.UseUntil) || databaseCredentialAuditCount(t, f) != 1 {
				t.Fatal("replay extended deadline or duplicated audit")
			}
			if err = f.pg.RevalidateHostingDatabaseCredentials(ctx, f.runner, databaseCredentialHash(), in, first.Metadata); err != nil {
				t.Fatal(err)
			}
			changed := first.Metadata
			changed.UseUntil = changed.UseUntil.Add(time.Second)
			if f.pg.RevalidateHostingDatabaseCredentials(ctx, f.runner, databaseCredentialHash(), in, changed) == nil {
				t.Fatal("revalidation replaced original deadline")
			}
			if _, err = f.pg.HostingApplyBegin(ctx, f.runner, f.authority); err != nil {
				t.Fatal(err)
			}
			denyDatabaseCredential(t, f, in)
			f.complete(t, "succeeded", false)
			if _, err = service.ReconcileHostingOperation(ctx, f.op.ID); err != nil {
				t.Fatal(err)
			}
			claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			in.Authority = domain.HostingAuthority{OperationID: f.op.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
			in.Mode = "observe"
			in.DeliveryID = strings.Repeat("b", 32)
			observed, err := f.pg.AuthorizeHostingDatabaseCredentials(ctx, f.runner, databaseCredentialHash(), in)
			if err != nil {
				t.Fatal("live explicit observation denied", err)
			}
			defer observed.Close()
			if len(observed.Records) != 2 || observed.Metadata.Mode != "observe" || databaseCredentialAuditCount(t, f) != 2 {
				t.Fatal("observation selected wrong scope")
			}
			if err = f.pg.RevalidateHostingDatabaseCredentials(ctx, f.runner, databaseCredentialHash(), in, observed.Metadata); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPostgresDatabaseCredentialDeliveryDenials(t *testing.T) {
	for _, name := range []string{"fence", "lease", "attempt", "mode", "plan", "cross_operation", "cross_resource", "runner", "database_capability", "storage_capability", "task_capability", "grant", "secret_revoked", "secret_version", "actor_revoked", "restore_hold", "global_id"} {
		t.Run(name, func(t *testing.T) {
			f, _, ctx, in := databaseCredentialFixture(t, "postgresql")
			var err error
			switch name {
			case "fence":
				in.Authority.Fence += "stale"
			case "lease":
				_, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, in.Authority.LeaseID)
			case "attempt":
				in.Authority.Attempt++
			case "mode":
				in.Mode = "observe"
				in.Authority.RunID = "observe_" + in.Authority.OperationID
			case "plan":
				in.PlanHash = "sha256:" + strings.Repeat("f", 64)
			case "cross_operation":
				in.Authority.OperationID = "foreign"
				in.Authority.RunID = "run_foreign"
			case "cross_resource":
				in.PlanHash = domain.HostingDocumentHash(struct{ Resource string }{"foreign"})
			case "runner":
				_, err = f.db.Exec(ctx, `UPDATE runners SET token_hash='replacement' WHERE id=$1`, f.runner)
			case "database_capability", "storage_capability", "task_capability":
				capability := domain.HostingDatabaseCapability
				if name == "storage_capability" {
					capability = domain.HostingOwnedStorageCapability
				}
				if name == "task_capability" {
					capability = domain.RunTypeDatabaseDeploy
				}
				_, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, capability)
			case "grant":
				_, err = f.pg.RevokeDestinationProject(ctx, f.storage.DestinationID, f.resource.ProjectID, destinationEvent("delivery_grant"))
			case "secret_revoked":
				err = injectDatabaseCredentialChange(ctx, f, false)
			case "secret_version":
				err = injectDatabaseCredentialChange(ctx, f, true)
			case "actor_revoked":
				_, err = f.db.Exec(ctx, `UPDATE users SET status='disabled' WHERE id=$1`, f.op.CreatedBy)
			case "restore_hold":
				_, err = f.db.Exec(ctx, `INSERT INTO hosting_binding_restore_holds(binding_id) VALUES($1)`, f.op.BindingID)
			case "global_id":
				_, err = f.db.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,target_id,metadata,created_at) VALUES($1,$2,'other','other','{}',clock_timestamp())`, "aud_database_delivery_"+in.DeliveryID, f.runner)
			}
			if err != nil {
				t.Fatal(err)
			}
			denyDatabaseCredential(t, f, in)
		})
	}
}

func TestPostgresDatabaseCredentialDeliveryAuditFailure(t *testing.T) {
	for _, deferred := range []bool{false, true} {
		t.Run(fmt.Sprint(deferred), func(t *testing.T) {
			f, _, ctx, in := databaseCredentialFixture(t, "redis")
			trigger := `CREATE TRIGGER database_delivery_fault BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION database_delivery_fault()`
			if deferred {
				trigger = `CREATE CONSTRAINT TRIGGER database_delivery_fault AFTER INSERT ON audit_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION database_delivery_fault()`
			}
			if _, err := f.db.Exec(ctx, `CREATE FUNCTION database_delivery_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.database_credentials_authorized' THEN RAISE EXCEPTION 'private audit fault'; END IF;RETURN NEW;END $$;`+trigger); err != nil {
				t.Fatal(err)
			}
			denyDatabaseCredential(t, f, in)
			if databaseCredentialAuditCount(t, f) != 0 {
				t.Fatal("failed audit committed")
			}
		})
	}
}

func TestPostgresDatabaseCredentialDeliveryConcurrentReplay(t *testing.T) {
	f, _, ctx, in := databaseCredentialFixture(t, "redis")
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	type result struct {
		metadata hostingdatabaseprotocol.Metadata
		err      error
	}
	done := make(chan result, 2)
	exited := make(chan struct{}, 2)
	defer func() {
		cancel()
		for range 2 {
			select {
			case <-exited:
			case <-time.After(3 * time.Second):
				t.Error("concurrent delivery did not exit")
				return
			}
		}
	}()
	for range 2 {
		go func() {
			defer func() { exited <- struct{}{} }()
			a, e := f.pg.AuthorizeHostingDatabaseCredentials(ctx, f.runner, databaseCredentialHash(), in)
			if e != nil {
				a.Close()
				done <- result{err: e}
				return
			}
			defer a.Close()
			done <- result{a.Metadata, e}
		}()
	}
	var first hostingdatabaseprotocol.Metadata
	for i := range 2 {
		select {
		case r := <-done:
			if r.err != nil {
				t.Fatal(r.err)
			}
			if i == 0 {
				first = r.metadata
			} else if domain.HostingDocumentHash(first) != domain.HostingDocumentHash(r.metadata) {
				t.Fatal("concurrent retry changed window")
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	if databaseCredentialAuditCount(t, f) != 1 {
		t.Fatal("concurrent duplicate audit")
	}
}

func TestPostgresDatabaseCredentialDeliveryDecryptAfterAudit(t *testing.T) {
	f, service, _, in := databaseCredentialFixture(t, "redis")
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: f.runner, Provider: domain.PrincipalRunner})
	delivery, err := app.NewHostingDatabaseCredentialDelivery(auth.ContextProvider{}, service, f.pg, nil, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if out, e := delivery.Deliver(ctx, in, "image-hosting-fixture-token"); e != app.ErrHostingDatabaseCredentialsUnavailable || out != nil {
		if out != nil {
			out.Close()
		}
		t.Fatal("missing key delivery did not fail safely")
	}
	if databaseCredentialAuditCount(t, f) != 1 {
		t.Fatal("decryption failure preceded committed audit")
	}
	delivery, err = app.NewHostingDatabaseCredentialDelivery(auth.ContextProvider{}, service, f.pg, managedRing(t), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	out, err := delivery.Deliver(ctx, in, "image-hosting-fixture-token")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err = out.WithValues(func(values []hostingdatabaseprotocol.Credential) error {
		if len(values) != 2 || len(values[0].Value) != 43 {
			t.Fatal("wrong generated values")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if databaseCredentialAuditCount(t, f) != 1 {
		t.Fatal("retry/decrypt duplicated audit")
	}
}

func TestPostgresDatabaseCredentialDeliveryCrossProjectReplay(t *testing.T) {
	f, service, ctx, in := databaseCredentialFixture(t, "redis")
	first, err := f.pg.AuthorizeHostingDatabaseCredentials(ctx, f.runner, databaseCredentialHash(), in)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
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
	f.complete(t, "succeeded", false)
	grantDestination(t, f.pg, f.storage.DestinationID, "proj_security", "database_delivery_foreign_grant")
	environment, err := service.CreateProjectEnvironment(ctx, app.ProjectEnvironmentInput{ProjectID: "proj_security", Name: "database delivery"})
	if err != nil {
		t.Fatal(err)
	}
	view, err := service.CreateHostingDatabase(ctx, domain.HostingDatabaseCreateInput{RequestID: strings.Repeat("c", 32), ProjectID: "proj_security", ProjectEnvironmentID: environment.ID, Name: "foreign delivery", DestinationID: f.storage.DestinationID, Engine: "redis", RecipeVersion: domain.HostingDatabaseRedisRecipe})
	if err != nil {
		t.Fatal(err)
	}
	row, err := sqlcgen.New(f.db).ReadHostingStorage(ctx, view.Resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	second := f
	second.resource = view.Resource
	second.storage = domain.HostingStorage{ID: row.ID, ProjectID: row.ProjectID, ResourceID: row.ResourceID, BindingID: row.BindingID, DestinationID: row.DestinationID, Kind: row.Kind, Driver: row.Driver, VolumeName: row.VolumeName, MountPath: row.MountPath, AccessMode: row.AccessMode, Retention: row.Retention, Generation: row.Generation, Hash: row.BodyHash, CreatedBy: row.CreatedBy, CreatedAt: row.CreatedAt}
	op, err := service.RequestHostingDatabaseDeployment(ctx, view.Resource.ID, databaseExecutionInput("foreign_delivery"))
	if err != nil {
		t.Fatal(err)
	}
	second = prepareDatabaseExecution(t, second, op)
	cross := in
	cross.Authority = second.authority
	cross.PlanHash = second.op.Plan.Hash
	// Both operations are real and authorized, but an ID cannot be rebound to
	// another project's operation, resource or pinned credential versions.
	denyDatabaseCredential(t, second, cross)
	cross.DeliveryID = strings.Repeat("b", 32)
	authorized, err := second.pg.AuthorizeHostingDatabaseCredentials(ctx, second.runner, databaseCredentialHash(), cross)
	if err != nil {
		t.Fatal(err)
	}
	defer authorized.Close()
	for ref := range authorized.Records {
		if _, ok := first.Records[ref]; ok {
			t.Fatal("foreign project received first resource's secret")
		}
	}
	if authorized.ProjectID == first.ProjectID || databaseCredentialAuditCount(t, f) != 2 {
		t.Fatal("delivery scope or audit changed")
	}
}

func TestPostgresDatabaseCredentialDeliveryPreparedStorageHold(t *testing.T) {
	for _, change := range []string{"missing_admission", "missing_hold", "foreign_hold", "attempt", "fence", "plan", "origin"} {
		t.Run(change, func(t *testing.T) {
			f, service, ctx, in := databaseCredentialFixture(t, "postgresql")
			if change == "missing_admission" {
				if _, err := f.db.Exec(ctx, `UPDATE hosting_binding_state SET hold_operation_id=$2 WHERE binding_id=$1`, f.op.BindingID, f.op.ID); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.pg.HostingStorageCreateBegin(ctx, f.runner, f.request()); err != nil {
					t.Fatal(err)
				}
				var err error
				switch change {
				case "missing_hold":
					_, err = f.db.Exec(ctx, `UPDATE hosting_binding_state SET hold_operation_id=NULL WHERE binding_id=$1`, f.op.BindingID)
				case "foreign_hold":
					view, e := service.CreateHostingDatabase(ctx, domain.HostingDatabaseCreateInput{RequestID: strings.Repeat("c", 32), ProjectID: f.resource.ProjectID, ProjectEnvironmentID: f.resource.ProjectEnvironmentID, Name: "foreign hold", DestinationID: f.storage.DestinationID, Engine: "postgresql", RecipeVersion: domain.HostingDatabasePostgresRecipe})
					if e != nil {
						t.Fatal(e)
					}
					other, e := service.RequestHostingDatabaseDeployment(ctx, view.Resource.ID, databaseExecutionInput("foreign_hold"))
					if e != nil {
						t.Fatal(e)
					}
					if e = f.pg.PrepareDeployment(ctx, other.ID); e != nil {
						t.Fatal(e)
					}
					_, err = f.db.Exec(ctx, `UPDATE hosting_binding_state SET hold_operation_id=$2 WHERE binding_id=$1`, f.op.BindingID, other.ID)
				case "plan":
					in.PlanHash = "sha256:" + strings.Repeat("f", 64)
				case "origin":
					tx, e := f.db.Begin(ctx)
					if e != nil {
						t.Fatal(e)
					}
					rollback := cleanupContinuityTransaction(t, tx)
					if _, e = tx.Exec(ctx, `ALTER TABLE hosting_storage_materializations DISABLE TRIGGER hosting_storage_materialization_transition`); e != nil {
						t.Fatal(e)
					}
					if _, e = tx.Exec(ctx, `UPDATE hosting_storage_materializations SET origin=jsonb_set(origin,'{daemon_id}','"foreign_daemon"'::jsonb) WHERE storage_id=$1`, f.storage.ID); e != nil {
						t.Fatal(e)
					}
					if _, e = tx.Exec(ctx, `ALTER TABLE hosting_storage_materializations ENABLE TRIGGER hosting_storage_materialization_transition`); e != nil {
						t.Fatal(e)
					}
					err = tx.Commit(ctx)
					rollback()
				case "attempt", "fence":
					// Fault injection is confined to this disposable schema; re-enable
					// the immutable guard before exercising production authorization.
					tx, e := f.db.Begin(ctx)
					if e != nil {
						t.Fatal(e)
					}
					rollback := cleanupContinuityTransaction(t, tx)
					if _, e = tx.Exec(ctx, `ALTER TABLE hosting_storage_operation_admissions DISABLE TRIGGER hosting_storage_operation_admissions_immutable`); e != nil {
						t.Fatal(e)
					}
					key, value := "attempt", `2`
					if change == "fence" {
						key, value = "fence", `"other"`
					}
					_, e = tx.Exec(ctx, `UPDATE hosting_storage_operation_admissions SET authority=jsonb_set(authority,ARRAY[$2::text],$3::jsonb) WHERE operation_id=$1`, f.op.ID, key, value)
					if e != nil {
						t.Fatal(e)
					}
					if _, e = tx.Exec(ctx, `ALTER TABLE hosting_storage_operation_admissions ENABLE TRIGGER hosting_storage_operation_admissions_immutable`); e != nil {
						t.Fatal(e)
					}
					err = tx.Commit(ctx)
					rollback()
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			denyDatabaseCredential(t, f, in)
		})
	}
}

func TestPostgresDatabaseCredentialDeliveryExpiredWindow(t *testing.T) {
	f, _, ctx, in := databaseCredentialFixture(t, "postgresql")
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	if _, err := f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '0.5 seconds' WHERE id=$1`, in.Authority.LeaseID); err != nil {
		t.Fatal(err)
	}
	a, err := f.pg.AuthorizeHostingDatabaseCredentials(ctx, f.runner, databaseCredentialHash(), in)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err = f.db.Exec(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '1 hour' WHERE id=$1`, in.Authority.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `SELECT pg_sleep(greatest(0,extract(epoch FROM ($1::timestamptz-clock_timestamp())))+0.05)`, a.Metadata.UseUntil); err != nil {
		t.Fatal(err)
	}
	denyDatabaseCredential(t, f, in)
	if f.pg.RevalidateHostingDatabaseCredentials(ctx, f.runner, databaseCredentialHash(), in, a.Metadata) == nil {
		t.Fatal("post-decrypt check extended expired original window")
	}
}

func TestPostgresDatabaseCredentialDeliveryRevalidationRevocation(t *testing.T) {
	for _, name := range []string{"bearer", "secret", "grant", "phase"} {
		t.Run(name, func(t *testing.T) {
			f, _, ctx, in := databaseCredentialFixture(t, "redis")
			a, err := f.pg.AuthorizeHostingDatabaseCredentials(ctx, f.runner, databaseCredentialHash(), in)
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close()
			switch name {
			case "bearer":
				_, err = f.db.Exec(ctx, `UPDATE runners SET token_hash='replacement' WHERE id=$1`, f.runner)
			case "secret":
				err = injectDatabaseCredentialChange(ctx, f, false)
			case "grant":
				_, err = f.pg.RevokeDestinationProject(ctx, f.storage.DestinationID, f.resource.ProjectID, destinationEvent("post_decrypt_grant"))
			case "phase":
				ack, e := f.pg.HostingStorageCreateBegin(ctx, f.runner, f.request())
				if e != nil {
					t.Fatal(e)
				}
				if _, e = f.pg.HostingStorageMaterialized(ctx, f.runner, f.proof(ack)); e != nil {
					t.Fatal(e)
				}
				_, err = f.pg.HostingApplyBegin(ctx, f.runner, f.authority)
			}
			if err != nil {
				t.Fatal(err)
			}
			if f.pg.RevalidateHostingDatabaseCredentials(ctx, f.runner, databaseCredentialHash(), in, a.Metadata) == nil {
				t.Fatal("revoked authorization survived decrypt boundary")
			}
			if databaseCredentialAuditCount(t, f) != 1 {
				t.Fatal("revalidation appended audit")
			}
		})
	}
}

// Keep compiler-checked test bindings explicit rather than exporting plaintext helpers.
func secretBinding(project string, ref domain.HostingDatabaseCredentialRef) secretcrypto.Binding {
	return secretcrypto.Binding{ProjectID: project, SecretID: ref.SecretID, Version: ref.Version}
}

// Simulate a future qualified engine operation in this disposable schema. The
// existing generic mutation guard itself remains unchanged and is re-enabled.
func injectDatabaseCredentialChange(ctx context.Context, f storageMaterializationFixture, version bool) (err error) {
	tx, err := f.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if rollbackErr := tx.Rollback(cleanup); rollbackErr != nil && !errors.Is(rollbackErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("rollback database credential injection: %w", rollbackErr))
		}
	}()
	if _, err = tx.Exec(ctx, `ALTER TABLE project_managed_secrets DISABLE TRIGGER hosting_database_secret_update_guard; ALTER TABLE project_managed_secret_versions DISABLE TRIGGER hosting_database_secret_version_guard`); err != nil {
		return err
	}
	id := f.op.Plan.Database.Credentials[0].SecretID
	if version {
		_, err = tx.Exec(ctx, `INSERT INTO project_managed_secret_versions(secret_id,version,envelope_version,key_id,ciphertext,created_by,created_at) SELECT secret_id,2,envelope_version,key_id,ciphertext,created_by,clock_timestamp() FROM project_managed_secret_versions WHERE secret_id=$1 AND version=1`, id)
		if err == nil {
			_, err = tx.Exec(ctx, `UPDATE project_managed_secrets SET current_version=2 WHERE id=$1`, id)
		}
	} else {
		_, err = tx.Exec(ctx, `UPDATE project_managed_secrets SET status='revoked',revoked_at=clock_timestamp() WHERE id=$1`, id)
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE; ALTER TABLE project_managed_secrets ENABLE TRIGGER hosting_database_secret_update_guard; ALTER TABLE project_managed_secret_versions ENABLE TRIGGER hosting_database_secret_version_guard`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func TestPostgresDatabaseCredentialDeliveryLockWaitAuthority(t *testing.T) {
	for _, change := range []string{"actor_expiry", "lease_expiry", "database_capability", "storage_capability"} {
		t.Run(change, func(t *testing.T) {
			f, service, admin := databaseExecutionFixture(t, "postgresql")
			principal, _ := proxyLegacyToken(t, f.pg, service, "tok_database_delivery_wait", []string{domain.RoleSystemAdmin})
			ctx, cancel := context.WithTimeout(auth.WithPrincipal(admin, principal), 10*time.Second)
			defer cancel()
			op, err := service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("delivery_wait"))
			if err != nil {
				t.Fatal(err)
			}
			f = prepareDatabaseExecution(t, f, op)
			in := domain.HostingDatabaseCredentialRequest{Authority: f.authority, DeliveryID: strings.Repeat("a", 32), PlanHash: f.op.Plan.Hash, Mode: "deploy"}
			blocker, err := f.db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			rollback := cleanupContinuityTransaction(t, blocker)
			var pid int
			if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			var expiry time.Time
			if change == "lease_expiry" {
				if _, err = f.db.Exec(ctx, `CREATE FUNCTION database_delivery_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.database_credentials_authorized' THEN PERFORM pg_advisory_xact_lock(592763); END IF;RETURN NEW;END $$;CREATE TRIGGER database_delivery_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION database_delivery_wait()`); err != nil {
					t.Fatal(err)
				}
				if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(592763)`); err != nil {
					t.Fatal(err)
				}
				if err = f.db.QueryRow(ctx, `UPDATE run_leases SET expires_at=clock_timestamp()+interval '1 second' WHERE id=$1 RETURNING expires_at`, f.authority.LeaseID).Scan(&expiry); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err = blocker.Exec(ctx, `SELECT id FROM runners WHERE id=$1 FOR UPDATE`, f.runner); err != nil {
					t.Fatal(err)
				}
				if change == "actor_expiry" {
					if err = f.db.QueryRow(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '1 second' WHERE id=$1 RETURNING expires_at`, principal.ID).Scan(&expiry); err != nil {
						t.Fatal(err)
					}
				}
			}
			done := make(chan error, 1)
			exited := make(chan struct{})
			go func() {
				defer close(exited)
				a, e := f.pg.AuthorizeHostingDatabaseCredentials(ctx, f.runner, databaseCredentialHash(), in)
				if e == nil {
					a.Close()
					done <- fmt.Errorf("released ciphertext after authority change")
					return
				}
				defer a.Close()
				if len(a.Records) != 0 {
					done <- fmt.Errorf("released ciphertext after authority change")
					return
				}
				done <- e
			}()
			defer func() {
				cancel()
				rollback()
				select {
				case <-exited:
				case <-time.After(3 * time.Second):
					t.Error("delivery did not exit after cancellation")
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
				case e := <-done:
					t.Fatalf("delivery did not reach lock: %v", e)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-ticker.C:
				}
			}
			if !expiry.IsZero() {
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
				if err == nil {
					t.Fatal("stale authority accepted after wait")
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if databaseCredentialAuditCount(t, f) != 0 {
				t.Fatal("stale authority committed audit")
			}
		})
	}
}

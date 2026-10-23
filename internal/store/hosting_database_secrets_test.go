package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestHostingDatabasePostgresOwnedSecretMutationGuards(t *testing.T) {
	pg, db, s, ctx, in := hostingDatabaseFixture(t, "postgresql")
	view, err := s.CreateHostingDatabase(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range view.Credentials {
		_, record, event := managedFixture(t, ref.SecretID, 2)
		before := hostingDatabaseCounts(t, db)
		if _, err = pg.RotateManagedSecret(ctx, ref.SecretID, 1, record, event); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("store rotated database-owned credential: %v", err)
		}
		if _, err = pg.RevokeManagedSecret(ctx, ref.SecretID, 1, event); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("store revoked database-owned credential: %v", err)
		}
		if _, err = s.RotateManagedSecret(ctx, ref.SecretID, app.ManagedSecretRotateInput{ExpectedVersion: 1, Value: "rejected-password"}); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("app rotated database-owned credential: %v", err)
		}
		if _, err = s.RevokeManagedSecret(ctx, ref.SecretID, app.ManagedSecretRevokeInput{ExpectedVersion: 1}); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("app revoked database-owned credential: %v", err)
		}
		if hostingDatabaseCounts(t, db) != before {
			t.Fatal("credential rejection appended version or audit")
		}
		for _, query := range []string{`UPDATE project_managed_secrets SET status='revoked',revoked_at=clock_timestamp() WHERE id=$1`, `UPDATE project_managed_secrets SET current_version=2 WHERE id=$1`, `INSERT INTO project_managed_secret_versions(secret_id,version,envelope_version,key_id,ciphertext,created_by,created_at) SELECT secret_id,2,envelope_version,key_id,ciphertext,created_by,clock_timestamp() FROM project_managed_secret_versions WHERE secret_id=$1 AND version=1`, `DELETE FROM hosting_database_credentials WHERE secret_id=$1`} {
			if _, err = db.Exec(ctx, query, ref.SecretID); err == nil {
				t.Fatal("SQL bypass changed database credential ownership")
			}
		}
		if hostingDatabaseCounts(t, db) != before {
			t.Fatal("SQL credential bypass leaked writes")
		}
		metadata, readErr := pg.GetManagedSecret(ctx, ref.SecretID)
		if readErr != nil || metadata.CurrentVersion != 1 || metadata.Status != "active" {
			t.Fatalf("database credential metadata changed: %+v %v", metadata, readErr)
		}
	}
}

func TestHostingDatabasePostgresSchemaAndGenericSQLBypass(t *testing.T) {
	pg, db, s, ctx, in := hostingDatabaseFixture(t, "redis")
	view, err := s.CreateHostingDatabase(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	before := hostingDatabaseCounts(t, db)
	for _, query := range []string{`UPDATE resources SET name='bypass',generation=generation+1 WHERE id=$1`, `UPDATE hosting_databases SET runtime_state='ready' WHERE resource_id=$1`, `UPDATE hosting_databases SET engine='postgresql' WHERE resource_id=$1`, `DELETE FROM hosting_database_receipts WHERE resource_id=$1`, `INSERT INTO hosting_operations(id,project_id,resource_id,binding_id,idempotency_key,request_hash,state,created_by) SELECT 'hop_database_bypass',project_id,resource_id,binding_id,'bypass','sha256:'||repeat('a',64),'queued',created_by FROM hosting_databases WHERE resource_id=$1`} {
		if _, err = db.Exec(ctx, query, view.Resource.ID); err == nil {
			t.Fatal("database generic SQL bypass succeeded")
		}
	}
	if _, err = db.Exec(ctx, `INSERT INTO resources(id,project_id,project_environment_id,name,kind,source,generation,created_by,created_at,updated_at) SELECT 'res_database_bypass',project_id,project_environment_id,'sidecar missing','database',source,1,created_by,clock_timestamp(),clock_timestamp() FROM resources WHERE id=$1`, view.Resource.ID); err == nil {
		t.Fatal("database resource committed without validated connected sidecar")
	}
	if hostingDatabaseCounts(t, db) != before {
		t.Fatal("SQL database bypass left durable writes")
	}
	if _, err = db.Exec(ctx, `ALTER TABLE project_managed_secrets DISABLE TRIGGER hosting_database_secret_update_guard`); err != nil {
		t.Fatal(err)
	}
	compatible, err := pg.SchemaCompatible(ctx)
	if err != nil || compatible {
		t.Fatalf("partial database schema stayed ready: %v %v", compatible, err)
	}
	if _, err = s.GetHostingDatabase(ctx, view.Resource.ID); !errors.Is(err, store.ErrHostingDatabaseUnavailable) {
		t.Fatalf("partial schema read: %v", err)
	}
}

func TestHostingDatabasePostgresTokenExpiryAfterCreationAuditWait(t *testing.T) {
	pg, db, s, admin, in := hostingDatabaseFixture(t, "postgresql")
	principal, _ := proxyLegacyToken(t, pg, s, "tok_database_expiry", []string{domain.RoleSystemAdmin})
	ctx, cancel := context.WithTimeout(auth.WithPrincipal(admin, principal), 10*time.Second)
	defer cancel()
	blocker, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rollback := cleanupContinuityTransaction(t, blocker)
	var lock int64
	var pid int
	if err = blocker.QueryRow(ctx, `SELECT hashtextextended(current_schema()||':database-audit',0),pg_backend_pid()`).Scan(&lock, &pid); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lock); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(ctx, `CREATE FUNCTION database_audit_wait() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.database.created' THEN PERFORM pg_advisory_xact_lock(hashtextextended(current_schema()||':database-audit',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER database_audit_wait BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION database_audit_wait()`); err != nil {
		t.Fatal(err)
	}
	var expiry time.Time
	if err = db.QueryRow(ctx, `UPDATE api_tokens SET expires_at=clock_timestamp()+interval '2 seconds' WHERE id=$1 RETURNING expires_at`, principal.ID).Scan(&expiry); err != nil {
		t.Fatal(err)
	}
	before := hostingDatabaseCounts(t, db)
	done := make(chan error, 1)
	exited := make(chan struct{})
	go func() { defer close(exited); _, callErr := s.CreateHostingDatabase(ctx, in); done <- callErr }()
	defer func() {
		cancel()
		rollback()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			t.Error("database creation did not exit after cancellation")
		}
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting bool
		if err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND query LIKE '%CreateAuditEvent%')`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err = <-done:
			t.Fatalf("creation did not reach audit wait: %v", err)
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
		if !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("expired actor committed database creation: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if hostingDatabaseCounts(t, db) != before {
		t.Fatal("expired creation leaked connected metadata or ciphertext")
	}
}

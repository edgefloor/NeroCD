package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func runtimeFixture(t *testing.T) (*store.PostgresStore, *pgxpool.Pool, domain.Resource) {
	t.Helper()
	pg, db := openPostgresIntegrationStore(t, t.Context(), "runtime_config")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	resource, _ := createBuildGitResource(t, pg, "runtime", "proj_platform", domain.HostingGitSource{})
	return pg, db, resource
}
func runtimeInput(request int, generation int64) domain.HostingRuntimeConfigurationInput {
	value := " viewer-readable "
	return domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: 1, ExpectedRuntimeGeneration: generation, RequestID: fmt.Sprintf("%032x", request), Entries: []domain.HostingRuntimeEntry{{Name: "APP", Kind: "literal", Value: &value}}}
}
func runtimeSave(t *testing.T, pg *store.PostgresStore, resource string, in domain.HostingRuntimeConfigurationInput) domain.HostingRuntimeConfiguration {
	t.Helper()
	saved, err := pg.PutHostingRuntimeConfiguration(t.Context(), "usr_bootstrap", resource, in, destinationEvent("ignored"))
	if err != nil {
		t.Fatal(err)
	}
	return saved
}
func runtimeCount(t *testing.T, db *pgxpool.Pool, table string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != want {
		t.Fatalf("%s count=%d want=%d err=%v", table, count, want, err)
	}
}
func TestHostingRuntimePostgresCASReplayAndIsolation(t *testing.T) {
	pg, db, resource := runtimeFixture(t)
	ctx := t.Context()
	zero, err := pg.GetHostingRuntimeConfiguration(ctx, "usr_bootstrap", resource.ID)
	if err != nil || zero.RuntimeGeneration != 0 || zero.Entries == nil || zero.CreatedAt != nil {
		t.Fatalf("zero %+v %v", zero, err)
	}
	runtimeCount(t, db, "hosting_runtime_configurations", 0)
	saved := runtimeSave(t, pg, resource.ID, runtimeInput(1, 0))
	if saved.RuntimeGeneration != 1 {
		t.Fatal(saved)
	}
	next := runtimeSave(t, pg, resource.ID, runtimeInput(2, 1))
	if next.RuntimeGeneration != 2 {
		t.Fatal(next)
	}
	replay := runtimeSave(t, pg, resource.ID, runtimeInput(1, 0))
	if replay.RuntimeGeneration != 1 || !replay.CreatedAt.Equal(*saved.CreatedAt) {
		t.Fatalf("replay %+v", replay)
	}
	for _, input := range []domain.HostingRuntimeConfigurationInput{runtimeInput(3, 0), runtimeInput(1, 1)} {
		if _, err = pg.PutHostingRuntimeConfiguration(ctx, "usr_bootstrap", resource.ID, input, destinationEvent("ignored")); !errors.Is(err, store.ErrConflict) {
			t.Fatalf("changed/stale: %v", err)
		}
	}
	clear := runtimeInput(3, 2)
	clear.Entries = []domain.HostingRuntimeEntry{}
	runtimeSave(t, pg, resource.ID, clear)
	runtimeCount(t, db, "hosting_runtime_configuration_versions", 3)
	runtimeCount(t, db, "hosting_runtime_configuration_receipts", 3)
	current, err := pg.GetResource(ctx, resource.ID)
	if err != nil || current.Generation != resource.Generation || !current.UpdatedAt.Equal(resource.UpdatedAt) || current.Source.Git.Generation != 1 {
		t.Fatalf("resource changed %+v %v", current, err)
	}
	var audits int
	var metadata []byte
	if err = db.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action='hosting.runtime_configuration.updated'`).Scan(&audits); err != nil || audits != 3 {
		t.Fatalf("audits %d %v", audits, err)
	}
	if err = db.QueryRow(ctx, `SELECT metadata FROM audit_events WHERE id=$1`, domain.HostingRuntimeAuditID(resource.ID, runtimeInput(1, 0).RequestID)).Scan(&metadata); err != nil || strings.Contains(string(metadata), "viewer-readable") || strings.Contains(string(metadata), "sha256") {
		t.Fatalf("audit: %s %v", metadata, err)
	}
	if _, err = db.Exec(ctx, `UPDATE resources SET generation=generation+1 WHERE id=$1`, resource.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = pg.PutHostingRuntimeConfiguration(ctx, "usr_bootstrap", resource.ID, runtimeInput(1, 0), destinationEvent("ignored")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("resource changed replay %v", err)
	}
}
func TestHostingRuntimePostgresConcurrency(t *testing.T) {
	pg, db, resource := runtimeFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() {
			_, err := pg.PutHostingRuntimeConfiguration(t.Context(), "usr_bootstrap", resource.ID, runtimeInput(1, 0), destinationEvent("ignored"))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	runtimeCount(t, db, "hosting_runtime_configuration_versions", 1)
	runtimeCount(t, db, "hosting_runtime_configuration_receipts", 1)
	errs = make(chan error, 2)
	for _, request := range []int{2, 3} {
		wg.Go(func() {
			_, err := pg.PutHostingRuntimeConfiguration(t.Context(), "usr_bootstrap", resource.ID, runtimeInput(request, 1), destinationEvent("ignored"))
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, store.ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("CAS winners %d conflicts %d", success, conflict)
	}
}
func TestHostingRuntimePostgresSecretReferencesAndPermissions(t *testing.T) {
	pg, db, resource := runtimeFixture(t)
	ctx := t.Context()
	item, record, event := managedFixture(t, "runtime_secret", 1)
	if _, err := pg.CreateManagedSecret(ctx, item, record, event); err != nil {
		t.Fatal(err)
	}
	in := runtimeInput(1, 0)
	in.Entries = []domain.HostingRuntimeEntry{{Name: "PASSWORD", Kind: "managed_secret", Secret: &domain.HostingRuntimeSecretReference{SecretID: item.ID, Version: 1}}}
	runtimeSave(t, pg, resource.ID, in)
	if _, err := db.Exec(ctx, `INSERT INTO project_members(id,project_id,user_id,role) VALUES('runtime_viewer','proj_platform','usr_viewer','viewer')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.GetHostingRuntimeConfiguration(ctx, "usr_viewer", resource.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.PutHostingRuntimeConfiguration(ctx, "usr_viewer", resource.ID, in, event); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("viewer save %v", err)
	}
	if _, err := db.Exec(ctx, `UPDATE project_members SET role='maintainer' WHERE id='runtime_viewer'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.PutHostingRuntimeConfiguration(ctx, "usr_viewer", resource.ID, in, event); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("maintainer save %v", err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM project_members WHERE id='runtime_viewer'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.GetHostingRuntimeConfiguration(ctx, "usr_viewer", resource.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("outsider read %v", err)
	}
	// Rotating preserves the exact historical version; missing versions fail closed.
	_, second, rotate := managedFixture(t, item.ID, 2)
	if _, err := pg.RotateManagedSecret(ctx, item.ID, 1, second, rotate); err != nil {
		t.Fatal(err)
	}
	runtimeSave(t, pg, resource.ID, in)
	missing := in
	missing.RequestID = fmt.Sprintf("%032x", 2)
	missing.ExpectedRuntimeGeneration = 1
	missing.Entries = []domain.HostingRuntimeEntry{{Name: "PASSWORD", Kind: "managed_secret", Secret: &domain.HostingRuntimeSecretReference{SecretID: item.ID, Version: 3}}}
	if _, err := pg.PutHostingRuntimeConfiguration(ctx, "usr_bootstrap", resource.ID, missing, event); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("missing version %v", err)
	}
	other, otherRecord, otherEvent := managedFixture(t, "other_runtime_secret", 1)
	other.ProjectID = "proj_security"
	if _, err := pg.CreateManagedSecret(ctx, other, otherRecord, otherEvent); err != nil {
		t.Fatal(err)
	}
	missing.Entries[0].Secret = &domain.HostingRuntimeSecretReference{SecretID: other.ID, Version: 1}
	if _, err := pg.PutHostingRuntimeConfiguration(ctx, "usr_bootstrap", resource.ID, missing, event); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("cross project ref %v", err)
	}
	revoke := destinationEvent("revoke_runtime_secret")
	if _, err := pg.RevokeManagedSecret(ctx, item.ID, 2, revoke); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.PutHostingRuntimeConfiguration(ctx, "usr_bootstrap", resource.ID, in, event); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("revoked replay %v", err)
	}
	got, err := pg.GetHostingRuntimeConfiguration(ctx, "usr_bootstrap", resource.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	if !strings.Contains(string(raw), item.ID) || strings.Contains(string(raw), "private-value") || strings.Contains(string(raw), "ciphertext") {
		t.Fatalf("unsafe projection %s", raw)
	}
	runtimeCount(t, db, "hosting_runtime_configuration_versions", 1)
}
func TestHostingRuntimePostgresAuditRollbackAndGuards(t *testing.T) {
	pg, db, resource := runtimeFixture(t)
	ctx := t.Context()
	if _, err := db.Exec(ctx, `CREATE FUNCTION fail_runtime_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='hosting.runtime_configuration.updated' THEN RAISE EXCEPTION 'test commit failure' USING ERRCODE='23514'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER runtime_audit_failure AFTER INSERT ON audit_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_runtime_audit()`); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.PutHostingRuntimeConfiguration(ctx, "usr_bootstrap", resource.ID, runtimeInput(1, 0), destinationEvent("ignored")); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("audit failure %v", err)
	}
	runtimeCount(t, db, "hosting_runtime_configurations", 0)
	runtimeCount(t, db, "hosting_runtime_configuration_versions", 0)
	runtimeCount(t, db, "hosting_runtime_configuration_receipts", 0)
	if _, err := db.Exec(ctx, `DROP TRIGGER runtime_audit_failure ON audit_events`); err != nil {
		t.Fatal(err)
	}
	runtimeSave(t, pg, resource.ID, runtimeInput(1, 0))
	for _, sql := range []string{`UPDATE hosting_runtime_configuration_versions SET body='{"entries":[]}'`, `DELETE FROM hosting_runtime_configuration_versions`, `UPDATE hosting_runtime_configuration_receipts SET request_hash='sha256:'||repeat('0',64)`, `DELETE FROM hosting_runtime_configuration_receipts`, `UPDATE hosting_runtime_configurations SET generation=3`, `DELETE FROM hosting_runtime_configurations`, `INSERT INTO hosting_runtime_configuration_secret_refs VALUES('` + resource.ID + `','proj_platform',1,'missing',1)`} {
		if _, err := db.Exec(ctx, sql); err == nil {
			t.Fatalf("mutation accepted %s", sql)
		}
	}
	if ok, err := pg.SchemaCompatible(ctx); err != nil || !ok {
		t.Fatalf("ready %v %v", ok, err)
	}
	if _, err := db.Exec(ctx, `ALTER TABLE hosting_runtime_configuration_secret_refs DISABLE TRIGGER hosting_runtime_ref_consistency`); err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.SchemaCompatible(ctx); err != nil || ok {
		t.Fatalf("missing consistency ready %v %v", ok, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := pg.GetHostingRuntimeConfiguration(cancelled, "usr_bootstrap", resource.ID); err == nil {
		t.Fatal("cancelled read accepted")
	}
}

func TestHostingRuntimePostgresDocumentReferenceCommitGuards(t *testing.T) {
	pg, db, resource := runtimeFixture(t)
	ctx := t.Context()
	item, record, event := managedFixture(t, "runtime_guard_secret", 1)
	if _, err := pg.CreateManagedSecret(ctx, item, record, event); err != nil {
		t.Fatal(err)
	}
	runtimeSave(t, pg, resource.ID, runtimeInput(1, 0))
	// Each attempt constructs a complete transaction except for its chosen broken
	// reference projection. A deferred failure must roll the pointer and audit back.
	for _, mode := range []string{"missing", "extra", "late_after_immediate"} {
		t.Run(mode, func(t *testing.T) {
			tx, err := db.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			body := `{"entries":[]}`
			if mode == "missing" {
				body = `{"entries":[{"name":"PASSWORD","kind":"managed_secret","secret":{"secret_id":"runtime_guard_secret","version":1}}]}`
			}
			for _, statement := range []string{
				`UPDATE hosting_runtime_configurations SET generation=2 WHERE resource_id='` + resource.ID + `'`,
				`INSERT INTO hosting_runtime_configuration_versions(resource_id,project_id,generation,body,body_hash,created_by) VALUES('` + resource.ID + `','proj_platform',2,'` + body + `','sha256:'||repeat('0',64),'usr_bootstrap')`,
				`INSERT INTO audit_events(id,actor_id,action,target_id,metadata,created_at) VALUES('runtime_guard_audit','usr_bootstrap','hosting.runtime_configuration.updated','` + resource.ID + `','{"project_id":"proj_platform","resource_id":"` + resource.ID + `","runtime_generation":2}',clock_timestamp())`,
				`INSERT INTO hosting_runtime_configuration_receipts VALUES('` + resource.ID + `','proj_platform','00000000000000000000000000000002','usr_bootstrap','sha256:'||repeat('0',64),1,1,2,'runtime_guard_audit')`,
			} {
				if _, err = tx.Exec(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "late_after_immediate" {
				if _, err = tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "missing" {
				_, err = tx.Exec(ctx, `INSERT INTO hosting_runtime_configuration_secret_refs VALUES($1,'proj_platform',2,'runtime_guard_secret',1)`, resource.ID)
			}
			if err == nil {
				err = tx.Commit(ctx)
			}
			if err == nil {
				t.Fatal("inconsistent normalized references committed")
			}
		})
	}
	runtimeCount(t, db, "hosting_runtime_configuration_versions", 1)
	// Wrong JSON shapes must be rejected even for direct database writers.
	for _, body := range []string{`{}`, `{"entries":[{}]}`, `{"entries":[{"name":"X","kind":"literal","extra":"x"}]}`, `{"entries":[{"name":"X","kind":"managed_secret","secret":{}}]}`, `{"entries":[{"name":"NEROCD_X","kind":"literal","value":"x"}]}`} {
		var valid bool
		if err := db.QueryRow(ctx, `SELECT nerocd_hosting_runtime_document_valid($1::jsonb)`, body).Scan(&valid); err != nil || valid {
			t.Fatalf("shape accepted %s valid=%v err=%v", body, valid, err)
		}
	}
}

func TestHostingRuntimePostgresOwnerExpiresAtAuditWait(t *testing.T) {
	pg, db, resource := runtimeFixture(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	const actor = "runtime_expiring_admin"
	if _, err := db.Exec(ctx, `INSERT INTO api_tokens(id,name,kind,token_hash,roles,status,created_by,expires_at) VALUES($1,$1,'service_account',$1,ARRAY['system_admin'],'active','usr_bootstrap',clock_timestamp()+interval '1 second')`, actor); err != nil {
		t.Fatal(err)
	}
	in := runtimeInput(1, 0)
	blocker, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	var pid int
	if err = blocker.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(ctx, `INSERT INTO audit_events(id,actor_id,action,target_id,metadata,created_at) VALUES($1,'usr_bootstrap','fixture','fixture','{}',clock_timestamp())`, domain.HostingRuntimeAuditID(resource.ID, in.RequestID)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, e := pg.PutHostingRuntimeConfiguration(ctx, actor, resource.ID, in, destinationEvent("ignored"))
		done <- e
	}()
	joined := false
	defer func() {
		cancel()
		_ = blocker.Rollback(context.Background())
		if !joined {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("save did not join")
			}
		}
	}()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var waiting bool
		if err = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err = <-done:
			joined = true
			t.Fatalf("save did not block at audit %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
	for {
		var expired bool
		if err = db.QueryRow(ctx, `SELECT expires_at<=clock_timestamp() FROM api_tokens WHERE id=$1`, actor).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-tick.C:
		}
	}
	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		joined = true
		if !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("expired owner %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	runtimeCount(t, db, "hosting_runtime_configurations", 0)
	runtimeCount(t, db, "hosting_runtime_configuration_versions", 0)
	runtimeCount(t, db, "hosting_runtime_configuration_receipts", 0)
}

func TestHostingRuntimePostgresStartupRequiresMigration(t *testing.T) {
	pg, db, _ := runtimeFixture(t)
	ctx := t.Context()
	var definition string
	if err := db.QueryRow(ctx, `SELECT pg_get_functiondef('nerocd_hosting_runtime_configuration_schema_compatible()'::regprocedure)`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DROP FUNCTION nerocd_hosting_runtime_configuration_schema_compatible()`); err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.SchemaCompatible(ctx); ok || err == nil {
		t.Fatalf("missing migration ready %v %v", ok, err)
	}
	if reopened, err := store.OpenPostgres(ctx, db.Config().ConnConfig.ConnString()); err == nil {
		_ = reopened.Close()
		t.Fatal("startup without runtime schema guard")
	}
	if _, err := db.Exec(ctx, definition); err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.SchemaCompatible(ctx); !ok || err != nil {
		t.Fatalf("restored migration %v %v", ok, err)
	}
}

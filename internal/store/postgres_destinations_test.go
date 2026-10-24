package store_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"nerocd/db"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresDatabaseServerAdmissionUpgradeAndReadiness(t *testing.T) {
	ctx := t.Context()
	pg, database := openPostgresIntegrationStore(t, ctx, "database_server_upgrade")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	runner := enrollDestinationRunner(t, pg, "runner_database_upgrade", []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability})
	previous, err := db.Files.ReadFile("migrations/0044_destination_capability_compatibility.sql")
	if err != nil {
		t.Fatal(err)
	}
	oldAdmission := strings.Split(string(previous), "-- Readiness")[0]
	if _, err := database.Exec(ctx, oldAdmission+"DROP FUNCTION nerocd_hosting_database_server_schema_compatible()"); err != nil {
		t.Fatal(err)
	}
	if ready, err := pg.SchemaCompatible(ctx); ready || err == nil {
		t.Fatalf("missing0067 readiness=%v err=%v", ready, err)
	}
	if opened, err := store.OpenPostgres(ctx, database.Config().ConnConfig.ConnString()); err == nil {
		closePostgresStore(t, opened)
		t.Fatal("missing0067 startup accepted")
	}
	event := destinationEvent("database_upgrade")
	server := domain.Server{ID: "server_database_upgrade", RunnerID: runner.ID, Name: "database upgrade", CreatedBy: event.ActorID, CreatedAt: event.CreatedAt}
	destination := domain.Destination{ID: "destination_database_upgrade", ServerID: server.ID, Name: "database upgrade", RuntimeKind: "docker_compose", CreatedAt: event.CreatedAt}
	_, err = database.Exec(ctx, `INSERT INTO servers(id,name,runner_id,created_by,created_at) VALUES($1,$2,$3,$4,$5)`, server.ID, server.Name, server.RunnerID, server.CreatedBy, server.CreatedAt)
	var admissionError *pgconn.PgError
	if !errors.As(err, &admissionError) || admissionError.Code != "23514" {
		t.Fatalf("0066 did not reproduce database capability rejection: %v", err)
	}
	if err := applyEmbeddedMigrations(ctx, database, "0066_hosting_database_execution.sql", "0067_hosting_database_server_admission.sql"); err != nil {
		t.Fatal(err)
	}
	upgraded, err := store.OpenPostgres(ctx, database.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePostgresStore(t, upgraded) })
	if _, _, err := upgraded.CreateServerWithDestination(ctx, server, destination, event); err != nil {
		t.Fatalf("0067 did not admit the same consumed enrollment: %v", err)
	}
	if ready, err := upgraded.SchemaCompatible(ctx); err != nil || !ready {
		t.Fatalf("0067 readiness=%v err=%v", ready, err)
	}
	if _, err := database.Exec(ctx, oldAdmission); err != nil {
		t.Fatal(err)
	}
	if ready, err := upgraded.SchemaCompatible(ctx); err != nil || ready {
		t.Fatalf("restored Compose-only admission readiness=%v err=%v", ready, err)
	}
	if opened, err := store.OpenPostgres(ctx, database.Config().ConnConfig.ConnString()); err == nil {
		closePostgresStore(t, opened)
		t.Fatal("restored Compose-only admission startup accepted")
	}
	current, err := db.Files.ReadFile("migrations/0067_hosting_database_server_admission.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, string(current)); err != nil {
		t.Fatal(err)
	}
	if ready, err := upgraded.SchemaCompatible(ctx); err != nil || !ready {
		t.Fatalf("restored0067 readiness=%v err=%v", ready, err)
	}
	if _, err := database.Exec(ctx, `ALTER TABLE servers DISABLE TRIGGER servers_runner_admission`); err != nil {
		t.Fatal(err)
	}
	if ready, err := upgraded.SchemaCompatible(ctx); err != nil || ready {
		t.Fatalf("disabled server admission readiness=%v err=%v", ready, err)
	}
}

func TestPostgresDestinationCapabilityUpgradeFrom0043(t *testing.T) {
	ctx := t.Context()
	// The ordinary helper proves a fresh migration succeeds as well.
	fresh, source := openPostgresIntegrationStore(t, ctx, "dest_cap_fresh")
	t.Cleanup(func() { closePostgresStore(t, fresh) })
	createDestinationTarget(t, fresh, "fresh_canonical")
	schema := fmt.Sprintf("nerocd_dest_cap_upgrade_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := source.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := source.Exec(cleanup, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	upgradeURL := databaseURLWithSearchPath(t, source.Config().ConnConfig.ConnString(), schema)
	database, err := pgxpool.New(ctx, upgradeURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	if err := applyEmbeddedMigrations(ctx, database, "", "0043_legacy_api_token_principals.sql"); err != nil {
		t.Fatal(err)
	}
	seed, err := os.ReadFile("../../db/seeds/dev.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, string(seed)); err != nil {
		t.Fatal(err)
	}
	// Seed an actual consumed enrollment in the old schema, before the new
	// store is allowed to open. These are the enrollment creation, runner
	// issuance, and consumption mutations used by the repository.
	runnerID := "runner_upgrade_canonical"
	if _, err := database.Exec(ctx, `INSERT INTO runner_enrollments (id,token_hash,runner_id,runner_name,tags,capabilities,created_by,created_at,expires_at) VALUES ('enroll_upgrade','enroll_hash',$1,'canonical','{}',$2,'usr_bootstrap',now(),now()+interval '1 hour')`, runnerID, []string{domain.RunTypeComposeDeploy}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO runners (id,name,tags,capabilities,status,registered_at,last_heartbeat_at,token_hash) VALUES ($1,'canonical','{}',$2,'active',now(),now(),'credential_hash')`, runnerID, []string{domain.RunTypeComposeDeploy}); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `UPDATE runner_enrollments SET used_at=now(),consume_request_id='consume_upgrade',credential_hash='credential_hash' WHERE id='enroll_upgrade'`); err != nil {
		t.Fatal(err)
	}
	event := destinationEvent("upgrade_canonical")
	server := domain.Server{ID: "server_upgrade_canonical", Name: "canonical", RunnerID: runnerID, CreatedBy: event.ActorID, CreatedAt: event.CreatedAt}
	dest := domain.Destination{ID: "destination_upgrade_canonical", ServerID: server.ID, Name: "default", RuntimeKind: "docker_compose", CreatedAt: event.CreatedAt}
	_, err = database.Exec(ctx, `INSERT INTO servers (id,name,runner_id,created_by,created_at) VALUES ($1,$2,$3,$4,$5)`, server.ID, server.Name, server.RunnerID, server.CreatedBy, server.CreatedAt)
	var admissionError *pgconn.PgError
	if !errors.As(err, &admissionError) || admissionError.Code != "23514" {
		t.Fatalf("0043 did not reproduce canonical capability rejection: %v", err)
	}
	if old, err := store.OpenPostgres(ctx, upgradeURL); err == nil {
		closePostgresStore(t, old)
		t.Fatal("missing0044 startup accepted")
	}
	if err := applyEmbeddedMigrations(ctx, database, "0043_legacy_api_token_principals.sql", "0044_destination_capability_compatibility.sql"); err != nil {
		t.Fatal(err)
	}
	// Preserve the historical 0043 rejection and 0044 upgrade above, then
	// install additive current migrations before opening today's store.
	if err := applyEmbeddedMigrations(ctx, database, "0044_destination_capability_compatibility.sql", ""); err != nil {
		t.Fatal(err)
	}
	upgraded, err := store.OpenPostgres(ctx, upgradeURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closePostgresStore(t, upgraded) })
	if _, _, err := upgraded.CreateServerWithDestination(ctx, server, dest, event); err != nil {
		t.Fatalf("0044 did not admit the same consumed enrollment: %v", err)
	}
	if compatible, err := upgraded.SchemaCompatible(ctx); err != nil || !compatible {
		t.Fatalf("upgraded schema compatibility=%v err=%v", compatible, err)
	}
	oldMigration, err := db.Files.ReadFile("migrations/0042_servers_destinations.sql")
	if err != nil {
		t.Fatal(err)
	}
	oldAdmission := strings.Split(strings.Split(string(oldMigration), "CREATE FUNCTION nerocd_server_runner_admission()")[1], "CREATE TRIGGER servers_runner_admission")[0]
	if _, err := database.Exec(ctx, "CREATE OR REPLACE FUNCTION nerocd_server_runner_admission()"+oldAdmission); err != nil {
		t.Fatal(err)
	}
	if compatible, err := upgraded.SchemaCompatible(ctx); err != nil || compatible {
		t.Fatalf("restored old admission stayed ready=%v err=%v", compatible, err)
	}
}

func TestPostgresDestinationConstraintsAndReadiness(t *testing.T) {
	ctx := t.Context()
	pg, db := openPostgresIntegrationStore(t, ctx, "dest_constraints")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	server, dest := createDestinationTarget(t, pg, "constraints")
	resource := createDestinationResource(t, pg, "constraints", "proj_platform")
	grantDestination(t, pg, dest.ID, resource.ProjectID, "constraint_grant")
	event := destinationEvent("constraint_bind")
	binding, err := pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_constraints", ResourceID: resource.ID, DestinationID: dest.ID, CreatedBy: event.ActorID, CreatedAt: event.CreatedAt}, event)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE servers SET runner_id='missing' WHERE id='` + server.ID + `'`,
		`DELETE FROM servers WHERE id='` + server.ID + `'`,
		`UPDATE destinations SET server_id='missing' WHERE id='` + dest.ID + `'`,
		`DELETE FROM destinations WHERE id='` + dest.ID + `'`,
		`UPDATE resource_bindings SET destination_id='missing' WHERE id='` + binding.ID + `'`,
		`UPDATE resource_bindings SET runtime_namespace='ncd_000000000000000000000000000000000000' WHERE id='` + binding.ID + `'`,
		`DELETE FROM resource_bindings WHERE id='` + binding.ID + `'`,
		`UPDATE destination_project_grants SET project_id='proj_security' WHERE destination_id='` + dest.ID + `'`,
		`DELETE FROM destination_project_grants WHERE destination_id='` + dest.ID + `'`,
		`INSERT INTO destination_project_grants VALUES ('` + dest.ID + `','missing','usr_bootstrap',now(),NULL)`,
		`INSERT INTO destination_project_grants VALUES ('missing','proj_platform','usr_bootstrap',now(),NULL)`,
		`INSERT INTO destinations VALUES ('destination_duplicate','` + server.ID + `','other','docker_compose',now())`,
		`DELETE FROM resources WHERE id='` + resource.ID + `'`,
	} {
		if _, err := db.Exec(ctx, query); err == nil {
			t.Fatalf("unsafe target mutation accepted: %s", query)
		}
	}
	cross := createDestinationResource(t, pg, "constraints_cross", "proj_security")
	if _, err := db.Exec(ctx, `INSERT INTO resource_bindings VALUES ('binding_cross',$1,$2,'ncd_111111111111111111111111111111111111','usr_bootstrap',now())`, cross.ID, dest.ID); err == nil {
		t.Fatal("direct crossproject insert bypassed grant")
	}
	grantDestination(t, pg, dest.ID, cross.ProjectID, "namespace_grant")
	if _, err := db.Exec(ctx, `INSERT INTO resource_bindings VALUES ('binding_namespace',$1,$2,$3,'usr_bootstrap',now())`, cross.ID, dest.ID, binding.RuntimeNamespace); err == nil {
		t.Fatal("namespace reuse accepted")
	}
	if ok, err := pg.SchemaCompatible(ctx); err != nil || !ok {
		t.Fatalf("complete0042 readiness=%v %v", ok, err)
	}
	if _, err := db.Exec(ctx, `ALTER TABLE resource_bindings DISABLE TRIGGER resource_bindings_admission`); err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.SchemaCompatible(ctx); err != nil || ok {
		t.Fatal("disabled admission trigger stays ready")
	}
	if _, err := db.Exec(ctx, `ALTER TABLE resource_bindings ENABLE TRIGGER resource_bindings_admission`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `ALTER TABLE resource_bindings DROP CONSTRAINT resource_bindings_resource_id_fkey`); err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.SchemaCompatible(ctx); err != nil || ok {
		t.Fatal("missing resource FK stays ready")
	}
}

func TestPostgresDestinationMissingMigrationBlocksStartup(t *testing.T) {
	ctx := t.Context()
	pg, db := openPostgresIntegrationStore(t, ctx, "dest_missing")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	removeHostingBuildMigrationLayer(t, db)
	// Remove the newest additive dependencies first; missing proxy/port schema
	// must fail readiness before the older destination regression is exercised.
	if _, err := db.Exec(ctx, `DROP TABLE hosting_release_claims,hosting_release_slots,hosting_cleanup_targets,hosting_retained_releases,hosting_proxy_release_references,hosting_proxy_receipts,hosting_proxy_admissions,hosting_proxy_task_targets,hosting_proxy_operations,hosting_proxy_routes,hosting_proxy_hostname_grants,hosting_proxy_restore_holds,hosting_proxies; DROP FUNCTION nerocd_hosting_proxy_schema_compatible(); DROP FUNCTION nerocd_hosting_release_identity(); DROP FUNCTION nerocd_hosting_release_claim_identity(); DROP FUNCTION nerocd_hosting_proxy_identity(); DROP FUNCTION nerocd_hosting_proxy_operation_identity()`); err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.SchemaCompatible(ctx); ok || err == nil {
		t.Fatalf("missing0046 readiness=%v %v", ok, err)
	}
	// Remove the additive hosting execution dependencies before reproducing
	// an absent 0042 schema, then restore both migration layers below.
	if _, err := db.Exec(ctx, `DROP TABLE hosting_binding_restore_holds,hosting_reconciliation_receipts,hosting_apply_admissions,hosting_binding_state,hosting_receipts,hosting_dispatch_receipts,hosting_task_targets,hosting_apply_plans,hosting_releases,hosting_snapshots,hosting_operations; DROP FUNCTION nerocd_hosting_execution_schema_compatible(); DROP FUNCTION nerocd_hosting_binding_identity(); DROP FUNCTION nerocd_hosting_operation_identity(); DROP FUNCTION nerocd_hosting_execution_immutable()`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DROP TABLE resource_bindings,destination_project_grants,destinations,servers; DROP FUNCTION nerocd_destinations_schema_compatible(); DROP FUNCTION nerocd_destination_capability_schema_compatible(); DROP FUNCTION nerocd_hosting_target_immutable(); DROP FUNCTION nerocd_destination_grant_identity(); DROP FUNCTION nerocd_server_runner_admission(); DROP FUNCTION nerocd_resource_binding_admission()`); err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.SchemaCompatible(ctx); ok || err == nil {
		t.Fatalf("missing0042 readiness=%v %v", ok, err)
	}
	if opened, err := store.OpenPostgres(ctx, db.Config().ConnConfig.ConnString()); err == nil {
		_ = opened.Close()
		t.Fatal("missing0042 startup accepted")
	}
	if err := applyEmbeddedMigrations(ctx, db, "0041_project_managed_secrets.sql", "0042_servers_destinations.sql"); err != nil {
		t.Fatal(err)
	}
	if err := applyEmbeddedMigrations(ctx, db, "0043_legacy_api_token_principals.sql", "0044_destination_capability_compatibility.sql"); err != nil {
		t.Fatal(err)
	}
	if err := applyEmbeddedMigrations(ctx, db, "0044_destination_capability_compatibility.sql", ""); err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.SchemaCompatible(ctx); err != nil || !ok {
		t.Fatalf("restored0042 readiness=%v %v", ok, err)
	}
	reopened, err := store.OpenPostgres(ctx, db.Config().ConnConfig.ConnString())
	if err != nil {
		t.Fatalf("latest schema reopen: %v", err)
	}
	closePostgresStore(t, reopened)

}
func TestPostgresDestinationRevokeSerializesWithBinding(t *testing.T) {
	ctx := t.Context()
	pg, db := openPostgresIntegrationStore(t, ctx, "dest_revoke_race")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	_, dest := createDestinationTarget(t, pg, "race")
	resource := createDestinationResource(t, pg, "race", "proj_platform")
	grantDestination(t, pg, dest.ID, resource.ProjectID, "race_grant")
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT id FROM destinations WHERE id=$1 FOR UPDATE`, dest.ID); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	done := make(chan error, 1)
	event := destinationEvent("race_bind")
	go func() {
		close(started)
		_, err := pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_race", ResourceID: resource.ID, DestinationID: dest.ID, CreatedBy: event.ActorID, CreatedAt: event.CreatedAt}, event)
		done <- err
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("bind bypassed destination lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if _, err = tx.Exec(ctx, `UPDATE destination_project_grants SET revoked_at=clock_timestamp() WHERE destination_id=$1 AND project_id=$2`, dest.ID, resource.ProjectID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, store.ErrConflict) {
			t.Fatalf("waiting bind after revoke: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("binding did not resume")
	}
	if _, err = pg.GetResourceBinding(ctx, resource.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("revocation race created binding")
	}
	events, err := pg.ListAuditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, audit := range events {
		if audit.ID == event.ID {
			t.Fatal("failed binding retained audit")
		}
	}
}
func TestPostgresDestinationBindingCommitFailureRollsBack(t *testing.T) {
	ctx := t.Context()
	pg, db := openPostgresIntegrationStore(t, ctx, "dest_commit")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	_, dest := createDestinationTarget(t, pg, "commit")
	resource := createDestinationResource(t, pg, "commit", "proj_platform")
	grantDestination(t, pg, dest.ID, resource.ProjectID, "commit_grant")
	if _, err := db.Exec(ctx, `CREATE FUNCTION fail_destination_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='resource.destination_bound' THEN RAISE EXCEPTION 'test binding audit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER destination_commit_failure AFTER INSERT ON audit_events DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION fail_destination_commit()`); err != nil {
		t.Fatal(err)
	}
	event := destinationEvent("commit_bind")
	binding, err := pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_commit", ResourceID: resource.ID, DestinationID: dest.ID, CreatedBy: event.ActorID, CreatedAt: event.CreatedAt}, event)
	if err == nil || binding.ID != "" {
		t.Fatal("commit failure returned binding")
	}
	if _, err = pg.GetResourceBinding(ctx, resource.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("failed commit retained binding")
	}
	events, err := pg.ListAuditEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range events {
		if strings.HasPrefix(a.Action, "resource.destination_bound") {
			t.Fatal("failed commit retained binding audit")
		}
	}
}

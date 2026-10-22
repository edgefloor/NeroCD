package store_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"nerocd/internal/store"
)

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
	if _, err := db.Exec(ctx, `DROP TABLE resource_bindings,destination_project_grants,destinations,servers; DROP FUNCTION nerocd_destinations_schema_compatible(); DROP FUNCTION nerocd_hosting_target_immutable(); DROP FUNCTION nerocd_destination_grant_identity(); DROP FUNCTION nerocd_server_runner_admission(); DROP FUNCTION nerocd_resource_binding_admission()`); err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.SchemaCompatible(ctx); ok || err == nil {
		t.Fatalf("missing0042 readiness=%v %v", ok, err)
	}
	if opened, err := store.OpenPostgres(ctx, db.Config().ConnConfig.ConnString()); err == nil {
		_ = opened.Close()
		t.Fatal("missing0042 startup accepted")
	}
	if err := applyEmbeddedMigrations(ctx, db, "0041_project_managed_secrets.sql", ""); err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.SchemaCompatible(ctx); err != nil || !ok {
		t.Fatalf("restored0042 readiness=%v %v", ok, err)
	}
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

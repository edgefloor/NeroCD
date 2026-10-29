package store_test

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresComposeMySQLBackupMigrationUpgrade(t *testing.T) {
	address := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if address == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL")
	}
	admin, err := pgxpool.New(t.Context(), address)
	if err != nil {
		t.Fatal("open owned migration database")
	}
	defer admin.Close()
	schema := "nerocd_compose_mysql_upgrade_" + strings.ToLower(rand.Text()[:16])
	created := false
	defer func() {
		if created {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, e := admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); e != nil {
				t.Error("drop owned schema", e)
			}
		}
	}()
	if _, err = admin.Exec(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	created = true
	db, err := pgxpool.New(t.Context(), databaseURLWithSearchPath(t, address, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = applyEmbeddedMigrations(t.Context(), db, "", "0103_hosting_mysql_backup_retention.sql"); err != nil {
		t.Fatal(err)
	}
	if err = applyEmbeddedMigrations(t.Context(), db, "0103_hosting_mysql_backup_retention.sql", "0104_hosting_compose_mysql_backups.sql"); err != nil {
		var detail *pgconn.PgError
		if errors.As(err, &detail) {
			t.Logf("migration SQL detail position=%d internal_position=%d query=%s", detail.Position, detail.InternalPosition, detail.InternalQuery)
		}
		t.Fatal(err)
	}
	var compatible bool
	if err = db.QueryRow(t.Context(), "SELECT nerocd_mysql_component_backup_schema_compatible()").Scan(&compatible); err != nil || !compatible {
		t.Fatal("schema incompatible", err)
	}
}

func TestPostgresComposeMySQLScheduleMigrationBackfill(t *testing.T) {
	ctx := t.Context()
	pg, database := openPostgresEmptyIntegrationStore(t, ctx, "schedule_upgrade", "0104_hosting_compose_mysql_backups.sql")
	t.Cleanup(func() { closePostgresStore(t, pg) })
	seed, err := os.ReadFile("../../db/seeds/dev.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, string(seed)); err != nil {
		t.Fatal(err)
	}
	resource := createDestinationResource(t, pg, "schedule_upgrade", "proj_platform")
	_, destination := createDestinationTarget(t, pg, "schedule_upgrade")
	grantDestination(t, pg, destination.ID, resource.ProjectID, "schedule_upgrade_grant")
	binding, err := pg.BindResourceDestination(ctx, store.ResourceBindingInput{ID: "binding_schedule_upgrade", ResourceID: resource.ID, DestinationID: destination.ID, CreatedBy: "usr_bootstrap", CreatedAt: destination.CreatedAt}, destinationEvent("binding_schedule_upgrade"))
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range []string{"", "postgresql"} {
		id := "dbs_upgrade_" + component
		tx, e := database.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		_, e = tx.Exec(ctx, `INSERT INTO hosting_database_backup_schedules(id,project_id,resource_id,binding_id,destination_id,actor_id,revision,frequency,timezone,enabled,component) VALUES($1,$2,$3,$4,$5,'usr_bootstrap',1,'daily','UTC',false,$6)`, id, resource.ProjectID, resource.ID, binding.ID, destination.ID, component)
		if e == nil {
			_, e = tx.Exec(ctx, `INSERT INTO hosting_database_backup_schedule_versions(schedule_id,revision,actor_id,expected_resource_generation,frequency,timezone,enabled,component) VALUES($1,1,'usr_bootstrap',1,'daily','UTC',false,$2)`, id, component)
		}
		if e != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
	}
	if err = applyEmbeddedMigrations(ctx, database, "0104_hosting_compose_mysql_backups.sql", "0105_hosting_compose_mysql_backup_schedules.sql"); err != nil {
		t.Fatal(err)
	}
	for _, component := range []string{"", "postgresql"} {
		var current, history string
		if err = database.QueryRow(ctx, `SELECT s.component_backup_version,v.component_backup_version FROM hosting_database_backup_schedules s JOIN hosting_database_backup_schedule_versions v ON v.schedule_id=s.id AND v.revision=s.revision WHERE s.id=$1`, "dbs_upgrade_"+component).Scan(&current, &history); err != nil {
			t.Fatal(err)
		}
		want := ""
		if component != "" {
			want = domain.HostingDatabaseBackupComponentVersion
		}
		if current != want || history != want {
			t.Fatal("historical engine backfill mismatch")
		}
	}
	for _, table := range []string{"hosting_database_backup_schedules", "hosting_database_backup_schedule_versions"} {
		if _, err = database.Exec(ctx, "UPDATE "+table+" SET component_backup_version='mysql.compose-logical-local.v1' WHERE component='postgresql'"); err == nil {
			t.Fatal("backfilled engine identity changed", table)
		}
	}
	var protected bool
	if err = database.QueryRow(ctx, `SELECT bool_and(proconfig[1] LIKE 'search_path=pg_catalog,%') FROM pg_proc WHERE pronamespace=current_schema()::regnamespace AND proname IN ('nerocd_database_backup_schedule_identity','nerocd_database_backup_schedule_version','nerocd_database_backup_schedule_occurrence')`).Scan(&protected); err != nil || !protected {
		t.Fatal("schedule guards lost protected search path", err)
	}
}

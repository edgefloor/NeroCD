package store_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/db"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func composeMySQLRetentionFixture(t *testing.T) networkCaptureFixture {
	t.Helper()
	f := newComposeMySQLBackupFixture(t)
	runner := f.sourceOp.Snapshot.RunnerID
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3]::text[] WHERE id=$1`, runner, domain.RunTypeDatabaseBackupRetention, domain.HostingDatabaseMySQLBackupRetentionCapability); err != nil {
		t.Fatal(err)
	}
	return networkCaptureFixture{storageMaterializationFixture: storageMaterializationFixture{pg: f.pg, db: f.db, resource: f.source.Resource, runner: runner, op: f.sourceOp}, service: f.service, ctx: f.ctx}
}

func TestPostgresComposeMySQLRetentionPolicyPreviewExecutionReplay(t *testing.T) {
	f := composeMySQLRetentionFixture(t)
	testBackupRetentionPolicyPreviewExecutionReplay(t, f, domain.HostingDatabaseMySQLBackupRetentionVersion)
	policy, err := f.service.GetDatabaseBackupRetention(f.ctx, f.resource.ID)
	if err != nil || policy == nil || policy.SourceScope == nil || policy.SourceScope.BackupVersion != domain.HostingDatabaseMySQLBackupComponentVersion || policy.SourceScope.Component != "mysql" {
		t.Fatal("saved component retention scope lost", err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_retention_policies SET body=jsonb_set(body,'{source_scope,component}','"postgresql"') WHERE resource_id=$1`, f.resource.ID); err == nil {
		t.Fatal("saved component scope changed")
	}
}

func TestPostgresComposeMySQLRetentionAutomaticPublicationOrdering(t *testing.T) {
	f := composeMySQLRetentionFixture(t)
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLBackupRemoteCapability); err != nil {
		t.Fatal(err)
	}
	network, target := remoteBackupFixtureFor(t, f)
	testBackupRetentionAutomaticPublicationFixture(t, network, target, domain.HostingDatabaseMySQLBackupRetentionCapability)
}

func TestPostgresComposeMySQLRetentionUnavailableSourceReadDisableReplay(t *testing.T) {
	f := composeMySQLRetentionFixture(t)
	create := domain.HostingDatabaseBackupRetentionInput{RequestID: "retention_create", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Automatic: true, Local: domain.BackupRetentionLimits{KeepCount: 1}}
	policy, err := f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, create)
	if err != nil || policy.SourceScope == nil {
		t.Fatal("component scope before first backup", err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE hosting_binding_state SET restore_admitted=false WHERE binding_id=$1`, f.op.BindingID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLBackupComponentCapability); err != nil {
		t.Fatal(err)
	}
	read, err := f.service.GetDatabaseBackupRetention(f.ctx, f.resource.ID)
	if err != nil || !reflect.DeepEqual(read, &policy) {
		t.Fatal("saved policy required usable source", err)
	}
	disable := create
	disable.RequestID = "retention_disable"
	disable.ExpectedRevision = 1
	disable.Enabled = false
	disabled, err := f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, disable)
	if err != nil || disabled.Enabled || disabled.Revision != 2 || !reflect.DeepEqual(disabled.SourceScope, policy.SourceScope) {
		t.Fatal("historical component retention disable", err)
	}
	replay, err := f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, disable)
	if err != nil || !reflect.DeepEqual(replay, disabled) {
		t.Fatal("historical disable replay", err)
	}
	original, err := f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, create)
	if err != nil || !reflect.DeepEqual(original, policy) {
		t.Fatal("committed enabled save replay required live source", err)
	}
	enable := disable
	enable.RequestID = "retention_reenable"
	enable.ExpectedRevision = 2
	enable.Enabled = true
	if _, err = f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, enable); err == nil {
		t.Fatal("unqualified source reenabled retention")
	}
}

func TestPostgresComposeMySQLRetentionDoesNotRelabelLegacyPolicy(t *testing.T) {
	f := composeMySQLRetentionFixture(t)
	for i := range 3 {
		ownedRetentionBackup(t, f, i)
	}
	now := time.Now().UTC()
	legacy := domain.HostingDatabaseBackupRetentionPolicy{Version: domain.HostingDatabaseBackupRetentionVersion, ProjectID: f.resource.ProjectID, ResourceID: f.resource.ID, ResourceGeneration: f.resource.Generation, Revision: 1, Enabled: true, Local: domain.BackupRetentionLimits{KeepCount: 1}, CreatedAt: now, UpdatedAt: now}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(f.ctx, `INSERT INTO hosting_database_backup_retention_policies(resource_id,project_id,actor_id,revision,body) VALUES($1,$2,'usr_bootstrap',1,$3)`, f.resource.ID, f.resource.ProjectID, raw); err != nil {
		t.Fatal(err)
	}
	read, err := f.service.GetDatabaseBackupRetention(f.ctx, f.resource.ID)
	if err != nil || read == nil || read.Version != legacy.Version || read.SourceScope != nil {
		t.Fatal("legacy PG policy reinterpreted as current MySQL", err)
	}
	preview, err := f.service.PreviewDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionPreviewInput{RequestID: "legacy_scope_preview", ExpectedPolicyRevision: 1})
	if err != nil || preview.SourceScope != nil || len(preview.Items) != 0 {
		t.Fatal("legacy PG policy selected MySQL archive history", err)
	}
	var available int
	if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backup_copies WHERE resource_id=$1 AND availability='available'`, f.resource.ID).Scan(&available); err != nil || available != 3 {
		t.Fatal("legacy policy altered MySQL copies", err)
	}
}

func TestPostgresComposeMySQLRetentionEnabledSaveRequiresCurrentAuthority(t *testing.T) {
	for _, scenario := range []string{"schema", "component_capability", "retention_capability", "component_profile"} {
		t.Run(scenario, func(t *testing.T) {
			f := composeMySQLRetentionFixture(t)
			switch scenario {
			case "schema":
				if _, err := f.db.Exec(f.ctx, `ALTER FUNCTION nerocd_mysql_component_retention_schema_compatible() RENAME TO unavailable_component_retention_schema`); err != nil {
					t.Fatal(err)
				}
			case "component_capability":
				if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLBackupComponentCapability); err != nil {
					t.Fatal(err)
				}
			case "retention_capability":
				if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLBackupRetentionCapability); err != nil {
					t.Fatal(err)
				}
			case "component_profile":
				if _, err := f.db.Exec(f.ctx, `UPDATE hosting_binding_state SET restore_admitted=false WHERE binding_id=$1`, f.op.BindingID); err != nil {
					t.Fatal(err)
				}
			}
			_, err := f.service.SaveDatabaseBackupRetention(f.ctx, f.resource.ID, domain.HostingDatabaseBackupRetentionInput{RequestID: "scope_denied", ExpectedResourceGeneration: f.resource.Generation, Enabled: true, Automatic: true, Local: domain.BackupRetentionLimits{KeepCount: 1}})
			expected := store.ErrConflict
			if scenario == "retention_capability" {
				expected = store.ErrNotFound
			}
			if !errors.Is(err, expected) {
				t.Fatal("unqualified component retention policy admitted", err)
			}
			var n int
			if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backup_retention_policies WHERE resource_id=$1`, f.resource.ID).Scan(&n); err != nil || n != 0 {
				t.Fatal("denied policy retained work", err)
			}
		})
	}
}

func TestPostgresComposePostgreSQLRetentionFreshPolicyRemainsQualified(t *testing.T) {
	pg, db, base, server, destination := hostingExecutionFixture(t)
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	caps := []string{domain.HostingStackCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.HostingDatabaseBackupComponentCapability, domain.RunTypeDatabaseBackupRetention, domain.HostingDatabaseBackupRetentionCapability}
	if _, err := db.Exec(ctx, `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, server.RunnerID, caps); err != nil {
		t.Fatal(err)
	}
	source, _ := deployBackupComponentStack(t, pg, base, server, destination, "a")
	service := hostingDatabaseApplication(t, pg)
	policy, err := service.SaveDatabaseBackupRetention(ctx, source.ID, domain.HostingDatabaseBackupRetentionInput{RequestID: "fresh_pg_component_retention", ExpectedResourceGeneration: source.Generation, Enabled: true, Automatic: true, Local: domain.BackupRetentionLimits{KeepCount: 1}})
	if err != nil || policy.Version != domain.HostingDatabaseBackupRetentionVersion || policy.SourceScope != nil {
		t.Fatal("qualified fresh PostgreSQL component policy regressed", err)
	}
	read, err := service.GetDatabaseBackupRetention(ctx, source.ID)
	if err != nil || !reflect.DeepEqual(read, &policy) {
		t.Fatal("fresh PostgreSQL component retention readback", err)
	}
}

func TestPostgresComposeMySQLRetentionPopulatedMigrationPreservesLegacyScope(t *testing.T) {
	f := composeMySQLRetentionFixture(t)
	if _, err := f.db.Exec(f.ctx, `DROP FUNCTION nerocd_backup_retention_source_scope() CASCADE; DROP FUNCTION nerocd_mysql_component_retention_schema_compatible()`); err != nil {
		t.Fatal(err)
	}
	historical, err := db.Files.ReadFile("migrations/0103_hosting_mysql_backup_retention.sql")
	if err != nil {
		t.Fatal(err)
	}
	lease, _, found := strings.Cut(string(historical), "CREATE OR REPLACE FUNCTION nerocd_backup_retention_receipt()")
	if !found {
		t.Fatal("historical retention lease source missing")
	}
	if _, err = f.db.Exec(f.ctx, lease); err != nil {
		t.Fatal("restore exact predecessor lease guard", err)
	}
	for i := range 3 {
		ownedRetentionBackup(t, f, i)
	}
	now := time.Now().UTC()
	legacy := domain.HostingDatabaseBackupRetentionPolicy{Version: domain.HostingDatabaseBackupRetentionVersion, ProjectID: f.resource.ProjectID, ResourceID: f.resource.ID, ResourceGeneration: f.resource.Generation, Revision: 1, Enabled: false, Local: domain.BackupRetentionLimits{KeepCount: 1}, CreatedAt: now, UpdatedAt: now}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(f.ctx, `INSERT INTO hosting_database_backup_retention_policies(resource_id,project_id,actor_id,revision,body) VALUES($1,$2,'usr_bootstrap',1,$3)`, f.resource.ID, f.resource.ProjectID, raw); err != nil {
		t.Fatal(err)
	}
	const snapshot = `SELECT jsonb_build_array((SELECT jsonb_agg(to_jsonb(b) ORDER BY b.id) FROM hosting_database_backups b),(SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id) FROM hosting_database_backup_copies c),(SELECT body FROM hosting_database_backup_retention_policies WHERE resource_id=$1))::text`
	var before, after string
	if err = f.db.QueryRow(f.ctx, snapshot, f.resource.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err = applyEmbeddedMigrations(f.ctx, f.db, "0107_hosting_compose_mysql_remote_restore.sql", "0108_hosting_compose_mysql_retention.sql"); err != nil {
		t.Fatal("populated component retention upgrade", err)
	}
	if err = f.db.QueryRow(f.ctx, snapshot, f.resource.ID).Scan(&after); err != nil || before != after {
		t.Fatal("retention upgrade rewrote saved evidence", err)
	}
	policy, err := f.service.GetDatabaseBackupRetention(f.ctx, f.resource.ID)
	if err != nil || policy == nil || policy.Version != legacy.Version || policy.SourceScope != nil {
		t.Fatal("upgrade relabeled historical policy", err)
	}
	var qualified bool
	if err = f.db.QueryRow(f.ctx, `SELECT count(*)=3 AND bool_and(proconfig[1] LIKE 'search_path=pg_catalog,%') FROM pg_proc WHERE pronamespace=current_schema()::regnamespace AND proname IN ('nerocd_backup_retention_source_scope','nerocd_backup_retention_lease','nerocd_mysql_component_retention_schema_compatible')`).Scan(&qualified); err != nil || !qualified {
		t.Fatal("component retention functions not schema-bound", err)
	}
}

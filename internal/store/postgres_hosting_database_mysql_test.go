package store_test

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestPostgresMySQLCreationCapabilityAndAtomicReplay(t *testing.T) {
	f, service, ctx := databaseExecutionFixture(t, "mysql")
	view, err := service.GetHostingDatabase(ctx, f.resource.ID)
	if err != nil || view.Engine != "mysql" || view.RecipeVersion != domain.HostingDatabaseMySQLRecipe {
		t.Fatal("MySQL metadata", err)
	}
	if view.Credentials[0].Role != "admin" || view.Credentials[0].Username != "root" || view.Credentials[1].Role != "app" || view.Credentials[1].Username != "ncd_app" || view.Credentials[0].SecretID == view.Credentials[1].SecretID {
		t.Fatal("MySQL credential separation")
	}
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLCapability); err != nil {
		t.Fatal(err)
	}
	before := hostingDatabaseCounts(t, f.db)
	if _, err = service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("mysql_missing_cap")); err == nil {
		t.Fatal("old database runner admitted MySQL")
	}
	if hostingDatabaseCounts(t, f.db) != before {
		t.Fatal("denied MySQL admission mutated state")
	}
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLCapability); err != nil {
		t.Fatal(err)
	}
	op, err := service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("mysql_private"))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("mysql_private"))
	if err != nil || op.ID != replay.ID {
		t.Fatal("MySQL deployment replay", err)
	}
	f = prepareDatabaseExecution(t, f, op)
	if f.op.Snapshot.ContainerPort != 3306 || f.op.Plan.Storage.MountPath != "/var/lib/mysql" || f.op.Snapshot.HostPort != 0 {
		t.Fatal("MySQL private recipe changed")
	}
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLCapability); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.HostingRunnerPlan(ctx, f.runner, f.authority); err == nil {
		t.Fatal("removed MySQL capability retained live execution authority")
	}
}
func TestPostgresMySQLApplicationBindingAndSeparateRestore(t *testing.T) {
	f := newConnectionFixture(t, "mysql").connect(t).prepareApp(t).ready(t).complete(t)
	binding, err := f.service.GetHostingDatabaseBinding(f.ctx, f.application.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(binding.EnvironmentNames, []string{"MYSQL_DATABASE", "MYSQL_HOST", "MYSQL_PASSWORD", "MYSQL_PORT", "MYSQL_USER"}) {
		t.Fatal("MySQL application names")
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=capabilities||ARRAY[$2,$3]::text[] WHERE id=$1`, f.runner, domain.RunTypeDatabaseBackup, domain.HostingDatabaseMySQLBackupCapability); err != nil {
		t.Fatal(err)
	}
	backup, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "mysql_backup", ExpectedResourceGeneration: 1})
	if err != nil {
		t.Fatal("MySQL backup admission", err)
	}
	if backup.Version != domain.HostingDatabaseMySQLBackupVersion {
		t.Fatal("wrong backup protocol")
	}
	a, p := claimBackup(t, f.networkCaptureFixture, backup.ID)
	mutation := domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "mysql_intent", PlanHash: p.Hash, Phase: "intent"}
	if _, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, mutation); err != nil {
		t.Fatal(err)
	}
	mutation.Phase = "complete"
	mutation.ReceiptID = "mysql_complete"
	mutation.Blob = &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("c", 64), Bytes: 65537, Format: "postgresql_custom"}
	if _, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, mutation); err == nil {
		t.Fatal("MySQL accepted PostgreSQL artifact")
	}
	mutation.Blob.Format = "mysql_sql"
	done, err := f.pg.DatabaseBackupMutation(f.ctx, f.runner, mutation)
	if err != nil || done.OwnershipHeld {
		t.Fatal("MySQL backup settle", err)
	}
	replay, err := f.pg.DatabaseBackupMutation(f.ctx, f.runner, mutation)
	if err != nil || !reflect.DeepEqual(done, replay) {
		t.Fatal("MySQL completion replay", err)
	}
	restored, err := f.service.RequestDatabaseBackupRestore(f.ctx, backup.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "mysql_restore", Name: "Restored MySQL"})
	if err != nil {
		t.Fatal("MySQL restore admission", err)
	}
	target, err := f.service.GetHostingDatabase(f.ctx, restored.TargetResourceID)
	if err != nil || target.Engine != "mysql" || target.Binding.ID == f.op.BindingID || target.StorageRef == f.storage.Reference() {
		t.Fatal("restore reused source or changed engine", err)
	}
	prepareBackupRestoreChild(t, f.networkCaptureFixture, restored, target)
	a, p = claimBackup(t, f.networkCaptureFixture, restored.ID)
	mutation = domain.HostingDatabaseBackupMutation{HostingAuthority: a, ReceiptID: "mysql_restore_intent", PlanHash: p.Hash, Phase: "intent"}
	if _, err = f.pg.DatabaseBackupMutation(f.ctx, f.runner, mutation); err != nil {
		t.Fatal(err)
	}
	mutation.Phase = "uncertain"
	mutation.ReceiptID = "mysql_restore_uncertain"
	mutation.FailureCode = "execution_failed"
	failed, err := f.pg.DatabaseBackupMutation(f.ctx, f.runner, mutation)
	if err != nil || !failed.OwnershipHeld || failed.State != "uncertain" {
		t.Fatal("failed MySQL restore released target", err)
	}
	if _, err = f.service.RequestHostingDatabaseDeployment(f.ctx, target.Resource.ID, databaseExecutionInput("must_remain_held")); err == nil {
		t.Fatal("failed target was deployable")
	}
	original, err := f.service.GetHostingDatabase(f.ctx, f.resource.ID)
	if err != nil || original.Resource.ID == target.Resource.ID || original.Engine != "mysql" {
		t.Fatal("restore changed original", err)
	}
	raw, _ := json.Marshal(done)
	if strings.Contains(string(raw), "admin.cnf") || strings.Contains(string(raw), "credentials") {
		t.Fatal("MySQL public backup exposed protected metadata")
	}
}
func TestPostgresMySQLClaimRequiresEngineCapability(t *testing.T) {
	f, service, ctx := databaseExecutionFixture(t, "mysql")
	op, err := service.RequestHostingDatabaseDeployment(ctx, f.resource.ID, databaseExecutionInput("mysql_claim"))
	if err != nil {
		t.Fatal(err)
	}
	if err = f.pg.PrepareDeployment(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLCapability); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(ctx, f.runner, time.Now(), time.Minute)
	if err == nil && claim.Run.RunSpec.HostingOperationID == op.ID {
		t.Fatal("old runner claimed MySQL")
	}
	var count int
	if err = f.db.QueryRow(ctx, `SELECT count(*) FROM run_leases WHERE run_id=$1`, op.RunID).Scan(&count); err != nil || count != 0 {
		t.Fatal("denied MySQL claim retained lease", err, count)
	}
}

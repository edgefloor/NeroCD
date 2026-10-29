package store_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresComposeDatabaseRemoteBackupAndRestore(t *testing.T) {
	pg, db, base, server, destination := hostingExecutionFixture(t)
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	caps := []string{domain.HostingStackCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingReleasePortsCapability, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.HostingDatabaseBackupComponentCapability}
	if _, err := db.Exec(ctx, `UPDATE runners SET capabilities=capabilities||$2::text[] WHERE id=$1`, server.RunnerID, caps); err != nil {
		t.Fatal(err)
	}
	source, sourceOp := deployBackupComponentStack(t, pg, base, server, destination, "a")
	target, targetOp := deployBackupComponentStack(t, pg, base, server, destination, "b")
	f, remote := remoteBackupFixtureFor(t, networkCaptureFixture{storageMaterializationFixture: storageMaterializationFixture{pg: pg, db: db, resource: source, runner: server.RunnerID, op: sourceOp}, service: hostingDatabaseApplication(t, pg), ctx: ctx})
	if _, err := f.service.SaveDatabaseBackupSchedule(ctx, source.ID, "", domain.HostingDatabaseBackupScheduleInput{RequestID: "pg_component_remote_schedule_denied", ExpectedResourceGeneration: source.Generation, Component: "postgresql", ExpectedDeployedOperationID: sourceOp.ID, Frequency: "every_minute", Timezone: "UTC", Enabled: true, RemoteTarget: &domain.BackupTargetSelection{TargetID: remote.ID, Revision: remote.Revision}}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("PostgreSQL component remote schedule widened", err)
	}
	localSchedule, err := f.service.SaveDatabaseBackupSchedule(ctx, source.ID, "", domain.HostingDatabaseBackupScheduleInput{RequestID: "pg_component_local_schedule", ExpectedResourceGeneration: source.Generation, Component: "postgresql", ExpectedDeployedOperationID: sourceOp.ID, Frequency: "every_minute", Timezone: "UTC", Enabled: true})
	if err != nil {
		t.Fatal("existing PostgreSQL local schedule", err)
	}
	// Even a historical/direct-SQL selection cannot enable the unqualified PG component due path.
	if _, err = db.Exec(ctx, `INSERT INTO hosting_database_backup_schedule_remote_targets(schedule_id,schedule_revision,target_id,target_revision) VALUES($1,1,$2,$3)`, localSchedule.ID, remote.ID, remote.Revision); err != nil {
		t.Fatal(err)
	}
	if err = pg.ReconcileDatabaseBackupSchedules(ctx); err != nil {
		t.Fatal(err)
	}
	history, err := f.service.ListDatabaseBackupOccurrences(ctx, source.ID, localSchedule.ID)
	if err != nil || len(history) != 1 || history[0].OperationID != "" || history[0].State != "skipped" {
		t.Fatal("stored PG component remote selection admitted due work", err)
	}
	input := domain.HostingDatabaseBackupInput{RequestID: "component_remote_backup", ExpectedResourceGeneration: source.Generation, Component: "postgresql", ExpectedDeployedOperationID: sourceOp.ID, RemoteTarget: &domain.BackupTargetSelection{TargetID: remote.ID, Revision: remote.Revision}}
	backup, err := f.service.RequestDatabaseBackup(ctx, source.ID, input)
	if err != nil {
		t.Fatal("component remote admission", err)
	}
	backup = completeBackup(t, f, backup.ID)
	transfers, err := f.service.ListDatabaseBackupTransfers(ctx, backup.ID, false)
	if err != nil || len(transfers) != 1 || transfers[0].Version != domain.HostingDatabaseBackupTransferVersion || transfers[0].Blob == nil || transfers[0].Blob.Format != "postgresql_custom" {
		t.Fatal("component local publication did not queue PostgreSQL upload", err)
	}
	upload := settleRemoteTransfer(t, f, transfers[0].ID, "")
	var uploadPlan domain.HostingDatabaseBackupTransferPlan
	if err = db.QueryRow(ctx, `SELECT plan FROM hosting_database_backup_transfers WHERE id=$1`, upload.ID).Scan(&uploadPlan); err != nil || uploadPlan.ObjectKey != "nerocd/postgresql/"+backup.ID+"/"+strings.TrimPrefix(backup.Blob.Digest, "sha256:")+".dump" {
		t.Fatal("component PostgreSQL object identity changed", err)
	}
	restored, err := f.service.RequestDatabaseBackupRestore(ctx, backup.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "component_remote_restore", ComponentTarget: &domain.HostingDatabaseBackupComponentTarget{ResourceID: target.ID, Component: "postgresql", ExpectedDeployedOperationID: targetOp.ID, ExpectedResourceGeneration: target.Generation, AcknowledgeReplacement: true}, RemoteSource: &domain.BackupRemoteSource{TransferID: upload.ID, ExpectedTargetRevision: remote.Revision}})
	if err != nil || restored.Version != domain.HostingDatabaseBackupComponentVersion {
		t.Fatal("component remote restore admission", err)
	}
	transfers, err = f.service.ListDatabaseBackupTransfers(ctx, backup.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	var download domain.HostingDatabaseBackupTransfer
	for _, transfer := range transfers {
		if transfer.Mode == "download" {
			download = transfer
		}
	}
	if download.ID == "" || download.RestoreOperationID != restored.ID || download.Version != domain.HostingDatabaseBackupTransferVersion {
		t.Fatal("component restore lost exact PostgreSQL download")
	}
	// Finish this exact download before preparing import. This regression covers
	// transfer compatibility, not concurrent component download/import ordering.
	settleRemoteTransfer(t, f, download.ID, "")
	restored = completeBackup(t, f, restored.ID)
	if restored.State != "succeeded" || restored.OwnershipHeld || restored.TargetResourceID != target.ID {
		t.Fatal("component remote restore did not settle separate target")
	}
}

func TestPostgresComposeMySQLRemoteUploadRetryKeepsLocalArchive(t *testing.T) {
	f := newComposeMySQLBackupFixture(t)
	runner := f.sourceOp.Snapshot.RunnerID
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, runner, domain.HostingDatabaseMySQLBackupRemoteCapability); err != nil {
		t.Fatal(err)
	}
	network, target := remoteBackupFixtureFor(t, networkCaptureFixture{storageMaterializationFixture: storageMaterializationFixture{pg: f.pg, db: f.db, resource: f.source.Resource, runner: runner, op: f.sourceOp}, service: f.service, ctx: f.ctx})
	input := f.input()
	input.RemoteTarget = &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision}
	// MySQL upload authority remains required even when the shared target validation passed.
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, runner, domain.HostingDatabaseMySQLBackupRemoteCapability); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.RequestDatabaseBackup(f.ctx, f.source.Resource.ID, input); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("missing MySQL upload capability accepted", err)
	}
	var deniedBackups, deniedSelections int
	if err := f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_database_backups),(SELECT count(*) FROM hosting_database_backup_remote_selections)`).Scan(&deniedBackups, &deniedSelections); err != nil || deniedBackups != 0 || deniedSelections != 0 {
		t.Fatal("denied upload retained work", err)
	}
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, runner, domain.HostingDatabaseMySQLBackupRemoteCapability); err != nil {
		t.Fatal(err)
	}
	backup, err := f.service.RequestDatabaseBackup(f.ctx, f.source.Resource.ID, input)
	if err != nil {
		t.Fatal("component remote admission", err)
	}
	replay, err := f.service.RequestDatabaseBackup(f.ctx, f.source.Resource.ID, input)
	if err != nil || replay.ID != backup.ID {
		t.Fatal("backup request replay", err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_remote_selections SET target_revision=target_revision WHERE operation_id=$1`, backup.ID); err == nil {
		t.Fatal("remote selection mutation accepted")
	}
	backup = completeBackup(t, network, backup.ID)
	transfers, err := f.service.ListDatabaseBackupTransfers(f.ctx, backup.ID, false)
	if err != nil || len(transfers) != 1 || transfers[0].Version != domain.HostingDatabaseMySQLBackupTransferVersion || transfers[0].Blob == nil || transfers[0].Blob.Format != "mysql_sql" {
		t.Fatal("component MySQL upload identity", err)
	}
	failed := settleRemoteTransfer(t, network, transfers[0].ID, "access_denied")
	local, err := f.service.GetDatabaseBackup(f.ctx, backup.ID)
	if err != nil || !reflect.DeepEqual(local, backup) {
		t.Fatal("upload failure changed local archive", err)
	}
	retryInput := domain.HostingDatabaseBackupTransferRetryInput{RequestID: "component_mysql_retry", ExpectedTransferID: failed.ID}
	retry, err := f.service.RetryDatabaseBackupTransfer(f.ctx, backup.ID, retryInput)
	if err != nil || retry.ID == failed.ID || !reflect.DeepEqual(retry.Blob, backup.Blob) {
		t.Fatal("retry changed artifact", err)
	}
	retried, err := f.service.RetryDatabaseBackupTransfer(f.ctx, backup.ID, retryInput)
	if err != nil || retried.ID != retry.ID || retried.Version != retry.Version || !reflect.DeepEqual(retried.Blob, retry.Blob) || retried.TargetID != retry.TargetID || retried.TargetRevision != retry.TargetRevision {
		t.Fatal("retry request replay", err)
	}
	var previous, next domain.HostingDatabaseBackupTransferPlan
	if err = f.db.QueryRow(f.ctx, `SELECT plan FROM hosting_database_backup_transfers WHERE id=$1`, failed.ID).Scan(&previous); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow(f.ctx, `SELECT plan FROM hosting_database_backup_transfers WHERE id=$1`, retry.ID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next.LocationID != previous.LocationID || next.ObjectKey != previous.ObjectKey || !reflect.DeepEqual(next.Target, previous.Target) || next.ObjectKey != "nerocd/mysql/"+backup.ID+"/"+strings.TrimPrefix(backup.Blob.Digest, "sha256:")+".sql" {
		t.Fatal("retry redirected artifact or destination")
	}
	upload := settleRemoteTransfer(t, network, retry.ID, "")
	var count int
	if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backups WHERE source_resource_id=$1 AND mode='backup'`, f.source.Resource.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("upload retry admitted another dump", err)
	}
	restore := domain.HostingDatabaseBackupRestoreInput{RequestID: "mysql_remote_denied", ComponentTarget: &domain.HostingDatabaseBackupComponentTarget{ResourceID: f.target.Resource.ID, Component: "mysql", ExpectedDeployedOperationID: f.targetOp.ID, ExpectedResourceGeneration: f.target.Resource.Generation, AcknowledgeReplacement: true}, RemoteSource: &domain.BackupRemoteSource{TransferID: upload.ID, ExpectedTargetRevision: target.Revision}}
	if _, err = f.service.RequestDatabaseBackupRestore(f.ctx, backup.ID, restore); !errors.Is(err, store.ErrConflict) {
		t.Fatal("remote component restore widened", err)
	}
}

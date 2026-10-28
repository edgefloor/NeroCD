package store_test

import (
	"strings"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
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

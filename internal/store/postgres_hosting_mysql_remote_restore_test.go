package store_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

func mysqlRemoteRestoreFixture(t *testing.T) (networkCaptureFixture, domain.HostingDatabaseBackupTarget, domain.HostingDatabaseBackupOperation, domain.HostingDatabaseBackupTransfer) {
	t.Helper()
	f, target := mysqlRemoteFixture(t)
	if _, err := f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLRemoteRestoreCapability); err != nil {
		t.Fatal(err)
	}
	backup, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "remote_restore_source", ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision}})
	if err != nil {
		t.Fatal(err)
	}
	backup = completeBackup(t, f, backup.ID)
	transfers, err := f.service.ListDatabaseBackupTransfers(f.ctx, backup.ID, false)
	if err != nil || len(transfers) != 1 {
		t.Fatal("upload child", err)
	}
	upload := settleRemoteTransfer(t, f, transfers[0].ID, "")
	return f, target, backup, upload
}

func mysqlRemoteRestoreInput(upload domain.HostingDatabaseBackupTransfer) domain.HostingDatabaseBackupRestoreInput {
	return domain.HostingDatabaseBackupRestoreInput{RequestID: "mysql_remote_restore", Name: "Remote MySQL target", RemoteSource: &domain.BackupRemoteSource{TransferID: upload.ID, ExpectedTargetRevision: upload.TargetRevision}}
}

func mysqlRestoreDownload(t *testing.T, f networkCaptureFixture, backup, restore string) domain.HostingDatabaseBackupTransfer {
	t.Helper()
	transfers, err := f.service.ListDatabaseBackupTransfers(f.ctx, backup, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, transfer := range transfers {
		if transfer.Mode == "download" && transfer.RestoreOperationID == restore {
			return transfer
		}
	}
	t.Fatal("exact restore download missing")
	return domain.HostingDatabaseBackupTransfer{}
}

func TestPostgresMySQLRemoteRestoreSeparateTargetAndPurpose(t *testing.T) {
	f, target, backup, upload := mysqlRemoteRestoreFixture(t)
	input := mysqlRemoteRestoreInput(upload)
	restored, err := f.service.RequestDatabaseBackupRestore(f.ctx, backup.ID, input)
	if err != nil || restored.Version != domain.HostingDatabaseMySQLBackupVersion || restored.TargetResourceID == f.resource.ID {
		t.Fatal("native MySQL remote restore", err)
	}
	replay, err := f.service.RequestDatabaseBackupRestore(f.ctx, backup.ID, input)
	if err != nil || replay.ID != restored.ID || replay.TargetResourceID != restored.TargetResourceID {
		t.Fatal("restore request replay", err)
	}
	if err = f.pg.PrepareDatabaseBackup(f.ctx, restored.ID); !errors.Is(err, hostingjobs.ErrDatabaseBackupWaiting) {
		t.Fatal("import escaped download barrier", err)
	}
	destination, err := f.service.GetHostingDatabase(f.ctx, restored.TargetResourceID)
	if err != nil || destination.Engine != "mysql" || destination.RecipeVersion != domain.HostingDatabaseMySQLRecipe || destination.Binding.ID == f.op.BindingID {
		t.Fatal("independent target engine/binding", err)
	}
	for _, ref := range destination.Credentials {
		for _, source := range f.op.Plan.Database.Credentials {
			if ref.SecretID == source.SecretID {
				t.Fatal("target reused source credential")
			}
		}
	}
	prepareBackupRestoreChild(t, f, restored, destination)
	download := mysqlRestoreDownload(t, f, backup.ID, restored.ID)
	a, p := claimRemoteTransfer(t, f, download.ID)
	if p.Operation.Version != domain.HostingDatabaseMySQLBackupTransferVersion || p.Operation.Blob.Format != "mysql_sql" || p.ObjectKey != domain.BackupRemoteObjectKey(backup.ID, *backup.Blob) || p.Target.Target.ID != target.ID {
		t.Fatal("download lost immutable MySQL upload identity")
	}
	request := domain.BackupRemoteCredentialRequest{Authority: a, DeliveryID: "mysql_restore_credentials", PlanHash: p.Hash, TargetID: target.ID, TargetRevision: target.Revision, DaemonID: p.DaemonID}
	grant, err := f.pg.AuthorizeBackupRemoteCredentials(f.ctx, f.runner, f.credential, request)
	if err != nil || grant.Metadata.Purpose != domain.HostingDatabaseMySQLRemoteRestoreCredentialPurpose {
		t.Fatal("download credential purpose", err)
	}
	runnerContext := auth.WithPrincipal(f.ctx, auth.Principal{ID: f.runner, Provider: domain.PrincipalRunner, Roles: []string{domain.RoleRunner}})
	credentials, err := f.service.RunnerBackupRemoteCredentials(runnerContext, request, "image-hosting-fixture-token")
	if err != nil {
		t.Fatal("authenticated download credential delivery", err)
	}
	if credentials.Metadata().Purpose != domain.HostingDatabaseMySQLRemoteRestoreCredentialPurpose {
		t.Fatal("service lost download credential purpose")
	}
	if err = credentials.Use(func(access, secret []byte) error {
		if string(access) != "synthetic-access-canary" || string(secret) != "synthetic-secret-canary" {
			t.Fatal("download decrypted wrong pinned keys")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	credentials.Close()
	if got, e := f.service.RunnerBackupRemoteCredentials(runnerContext, request, "wrong-synthetic-token"); e == nil || got != nil {
		t.Fatal("wrong bearer disclosed download keys")
	}
	wrong := grant.Metadata
	wrong.Purpose = domain.HostingDatabaseMySQLBackupRemoteCredentialPurpose
	if err = f.pg.RevalidateBackupRemoteCredentials(f.ctx, f.runner, f.credential, request, wrong); err == nil {
		t.Fatal("upload purpose authorized download")
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLRemoteRestoreCapability); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.DatabaseBackupTransferPlan(f.ctx, f.runner, a); err == nil {
		t.Fatal("live download survived capability revocation")
	}
	if _, err = f.pg.AuthorizeBackupRemoteCredentials(f.ctx, f.runner, f.credential, request); err == nil {
		t.Fatal("revoked capability refetched keys")
	}
	if got, e := f.service.RunnerBackupRemoteCredentials(runnerContext, request, "image-hosting-fixture-token"); e == nil || got != nil {
		t.Fatal("revoked adapter disclosed download keys through service")
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLRemoteRestoreCapability); err != nil {
		t.Fatal(err)
	}
	mutation := domain.HostingDatabaseBackupTransferMutation{HostingAuthority: a, ReceiptID: "mysql_download_intent", PlanHash: p.Hash, Phase: "intent", DaemonID: p.DaemonID}
	if _, err = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation); err != nil {
		t.Fatal(err)
	}
	mutation.Phase, mutation.ReceiptID = "complete", "mysql_download_complete"
	mutation.LocalCopy = &domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("a", 64), Device: 1, Inode: 2, Bytes: backup.Blob.Bytes, Created: true}
	for range 2 {
		if _, err = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation); err != nil {
			t.Fatal("download publication replay", err)
		}
	}
	restored = completeBackup(t, f, restored.ID)
	if restored.State != "succeeded" || restored.OwnershipHeld {
		t.Fatal("remote MySQL import did not settle")
	}
	source, err := f.service.GetDatabaseBackup(f.ctx, backup.ID)
	if err != nil || source.State != "succeeded" || !reflect.DeepEqual(source.Blob, backup.Blob) {
		t.Fatal("restore changed original backup", err)
	}
	var copies, events int
	if err = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_database_backup_copies WHERE id=$1),(SELECT count(*) FROM hosting_notification_events)`, "dbc_download_"+download.ID).Scan(&copies, &events); err != nil || copies != 1 || events != 0 {
		t.Fatal("download replay duplicated local copy or notified restore", err)
	}
}

func TestPostgresMySQLRemoteRestoreAdmissionDenials(t *testing.T) {
	for _, scenario := range []string{"schema", "capability", "target_revision", "credential"} {
		t.Run(scenario, func(t *testing.T) {
			f, target, backup, upload := mysqlRemoteRestoreFixture(t)
			input := mysqlRemoteRestoreInput(upload)
			var err error
			switch scenario {
			case "schema":
				_, err = f.db.Exec(f.ctx, `ALTER FUNCTION nerocd_mysql_remote_restore_schema_compatible() RENAME TO unavailable_mysql_restore_schema`)
			case "capability":
				_, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLRemoteRestoreCapability)
			case "target_revision":
				input.RemoteSource.ExpectedTargetRevision++
			case "credential":
				_, err = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_remote_credentials SET status='revoked' WHERE id IN(SELECT access_id FROM hosting_database_backup_target_versions WHERE target_id=$1)`, target.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			var before int
			if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM resources`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.RequestDatabaseBackupRestore(f.ctx, backup.ID, input); err == nil {
				t.Fatal("unavailable restore authority admitted effects")
			}
			var after, restores, downloads int
			if err = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM resources),(SELECT count(*) FROM hosting_database_backups WHERE mode='restore'),(SELECT count(*) FROM hosting_database_backup_transfers WHERE mode='download')`).Scan(&after, &restores, &downloads); err != nil || after != before || restores != 0 || downloads != 0 {
				t.Fatal("denied restore retained target or queue effects", err)
			}
		})
	}
}

func TestPostgresMySQLRemoteRestoreFailedDownloadHoldsTarget(t *testing.T) {
	f, _, backup, upload := mysqlRemoteRestoreFixture(t)
	restored, err := f.service.RequestDatabaseBackupRestore(f.ctx, backup.ID, mysqlRemoteRestoreInput(upload))
	if err != nil {
		t.Fatal(err)
	}
	target, err := f.service.GetHostingDatabase(f.ctx, restored.TargetResourceID)
	if err != nil {
		t.Fatal(err)
	}
	prepareBackupRestoreChild(t, f, restored, target)
	download := mysqlRestoreDownload(t, f, backup.ID, restored.ID)
	failed := settleRemoteTransfer(t, f, download.ID, "object_mismatch")
	if failed.State != "failed" {
		t.Fatal("corrupt download classification")
	}
	if err = f.pg.PrepareDatabaseBackup(f.ctx, restored.ID); err != nil {
		t.Fatal(err)
	}
	current, err := f.service.GetDatabaseBackup(f.ctx, restored.ID)
	if err != nil || current.State != "uncertain" || current.FailureCode != "execution_failed" || !current.OwnershipHeld {
		t.Fatal("failed download released separate target", err)
	}
	var intents, events int
	if err = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_database_backup_receipts WHERE operation_id=$1 AND phase='intent'),(SELECT count(*) FROM hosting_notification_events)`, restored.ID).Scan(&intents, &events); err != nil || intents != 0 || events != 0 {
		t.Fatal("failed download started import or backup alert", err)
	}
	if _, err = f.service.RequestHostingDatabaseDeployment(f.ctx, target.Resource.ID, databaseExecutionInput("after_bad_download")); !errors.Is(err, store.ErrConflict) {
		t.Fatal("held target admitted replacement", err)
	}
	original, err := f.service.GetDatabaseBackup(f.ctx, backup.ID)
	if err != nil || original.State != "succeeded" || !reflect.DeepEqual(original.Blob, backup.Blob) {
		t.Fatal("failed recovery changed original", err)
	}
}

func TestPostgresMySQLRemoteRestoreClaimRequiresAdapter(t *testing.T) {
	f, _, backup, upload := mysqlRemoteRestoreFixture(t)
	restored, err := f.service.RequestDatabaseBackupRestore(f.ctx, backup.ID, mysqlRemoteRestoreInput(upload))
	if err != nil {
		t.Fatal(err)
	}
	download := mysqlRestoreDownload(t, f, backup.ID, restored.ID)
	if err = f.pg.PrepareDatabaseBackupTransfer(f.ctx, download.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLRemoteRestoreCapability); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("upload-only runner claimed native MySQL download", err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLRemoteRestoreCapability); err != nil {
		t.Fatal(err)
	}
	claim, err := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
	if err != nil || claim.Run.RunSpec.HostingOperationID != download.ID || claim.Run.RunSpec.HostingMode != "download" {
		t.Fatal("configured adapter did not claim exact download", err)
	}
}

package store_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func mysqlRemoteFixture(t *testing.T) (networkCaptureFixture, domain.HostingDatabaseBackupTarget) {
	t.Helper()
	f := scheduledBackupFixtureFor(t, "mysql")
	f.credential = databaseCredentialHash()
	return remoteBackupFixtureFor(t, f)
}

func TestPostgresMySQLRemoteBackupRetryKeepsLocalArtifact(t *testing.T) {
	f, target := mysqlRemoteFixture(t)
	input := domain.HostingDatabaseBackupInput{RequestID: "mysql_remote", ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision}}
	backup, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, input)
	if err != nil {
		t.Fatal("MySQL remote backup admission", err)
	}
	backup = completeBackup(t, f, backup.ID)
	uploads, err := f.service.ListDatabaseBackupTransfers(f.ctx, backup.ID, false)
	if err != nil || len(uploads) != 1 || uploads[0].Version != domain.HostingDatabaseMySQLBackupTransferVersion || uploads[0].Blob.Format != "mysql_sql" {
		t.Fatal("MySQL upload was not derived from local completion", err)
	}
	failed := settleRemoteTransfer(t, f, uploads[0].ID, "access_denied")
	local, err := f.service.GetDatabaseBackup(f.ctx, backup.ID)
	if err != nil || local.State != "succeeded" || local.OwnershipHeld || !reflect.DeepEqual(local.Blob, backup.Blob) {
		t.Fatal("failed upload changed local artifact", err)
	}
	var before int
	if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backups`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	retryInput := domain.HostingDatabaseBackupTransferRetryInput{RequestID: "mysql_remote_retry", ExpectedTransferID: failed.ID}
	retry, err := f.service.RetryDatabaseBackupTransfer(f.ctx, backup.ID, retryInput)
	if err != nil || retry.BackupID != backup.ID || retry.Version != failed.Version || !reflect.DeepEqual(retry.Blob, backup.Blob) || retry.TargetRevision != 1 {
		t.Fatal("same-artifact retry changed identity", err)
	}
	replay, err := f.service.RetryDatabaseBackupTransfer(f.ctx, backup.ID, retryInput)
	if err != nil || replay.ID != retry.ID {
		t.Fatal("retry request did not converge", err)
	}
	a, plan := claimRemoteTransfer(t, f, retry.ID)
	if plan.ObjectKey != "nerocd/mysql/"+backup.ID+"/"+strings.TrimPrefix(backup.Blob.Digest, "sha256:")+".sql" {
		t.Fatal("MySQL object namespace")
	}
	var previous domain.HostingDatabaseBackupTransferPlan
	if err = f.db.QueryRow(f.ctx, `SELECT plan FROM hosting_database_backup_transfers WHERE id=$1`, failed.ID).Scan(&previous); err != nil || previous.Target != plan.Target || previous.ObjectKey != plan.ObjectKey {
		t.Fatal("retry redirected pinned target or credential references", err)
	}
	request := domain.BackupRemoteCredentialRequest{Authority: a, DeliveryID: "mysql_remote_delivery", PlanHash: plan.Hash, TargetID: target.ID, TargetRevision: 1, DaemonID: plan.DaemonID}
	grant, err := f.pg.AuthorizeBackupRemoteCredentials(f.ctx, f.runner, f.credential, request)
	if err != nil || grant.Metadata.Purpose != domain.HostingDatabaseMySQLBackupRemoteCredentialPurpose || grant.Records.Access.Metadata.SecretID != plan.Target.Access.SecretID || grant.Records.Secret.Metadata.SecretID != plan.Target.Secret.SecretID {
		t.Fatal("MySQL credential grant lost persisted purpose or refs", err)
	}
	if err = f.pg.RevalidateBackupRemoteCredentials(f.ctx, f.runner, f.credential, request, grant.Metadata); err != nil {
		t.Fatal("MySQL grant revalidation", err)
	}
	wrong := grant.Metadata
	wrong.Purpose = domain.HostingDatabaseBackupRemoteCredentialPurpose
	if err = f.pg.RevalidateBackupRemoteCredentials(f.ctx, f.runner, f.credential, request, wrong); err == nil {
		t.Fatal("PostgreSQL credential purpose accepted for persisted MySQL upload")
	}
	mutation := domain.HostingDatabaseBackupTransferMutation{HostingAuthority: a, ReceiptID: "mysql_retry_intent", PlanHash: plan.Hash, Phase: "intent", DaemonID: plan.DaemonID}
	if _, err = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation); err != nil {
		t.Fatal(err)
	}
	mutation.Phase, mutation.ReceiptID = "complete", "mysql_retry_complete"
	if _, err = f.pg.DatabaseBackupTransferMutation(f.ctx, f.runner, mutation); err != nil {
		t.Fatal("MySQL upload settlement", err)
	}
	var after, notifications int
	if err = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM hosting_database_backups),(SELECT count(*) FROM hosting_notification_events)`).Scan(&after, &notifications); err != nil || before != after || notifications != 0 {
		t.Fatal("upload retry redumped source or enabled notifications", err, before, after, notifications)
	}
	if _, err = f.service.GetDatabaseBackupRetention(f.ctx, f.resource.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatal("remote MySQL gained retention", err)
	}
	if _, err = f.service.RequestDatabaseBackupRestore(f.ctx, backup.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "mysql_remote_restore_denied", Name: "Unsupported remote restore", RemoteSource: &domain.BackupRemoteSource{TransferID: retry.ID, ExpectedTargetRevision: 1}}); !errors.Is(err, store.ErrConflict) {
		t.Fatal("MySQL remote-only restore admitted", err)
	}
}

func TestPostgresMySQLScheduledRemoteBackup(t *testing.T) {
	f, target := mysqlRemoteFixture(t)
	input := scheduledBackupInput(t, f, "mysql_remote_schedule", true)
	input.RemoteTarget = &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision}
	schedule, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, "", input)
	if err != nil || schedule.Version != domain.HostingDatabaseMySQLBackupScheduleVersion || !reflect.DeepEqual(schedule.RemoteTarget, input.RemoteTarget) {
		t.Fatal("MySQL remote schedule selection", err)
	}
	for range 2 {
		if err = f.pg.ReconcileDatabaseBackupSchedules(f.ctx); err != nil {
			t.Fatal(err)
		}
	}
	occurrences := scheduledOccurrences(t, f, schedule.ID)
	if len(occurrences) != 1 || occurrences[0].OperationID == "" {
		t.Fatal("schedule did not admit exactly one operation")
	}
	backup := completeBackup(t, f, occurrences[0].OperationID)
	transfers, err := f.service.ListDatabaseBackupTransfers(f.ctx, backup.ID, false)
	if err != nil || len(transfers) != 1 || transfers[0].Version != domain.HostingDatabaseMySQLBackupTransferVersion || !reflect.DeepEqual(transfers[0].Blob, backup.Blob) {
		t.Fatal("scheduled completion did not queue exact MySQL upload", err)
	}
	settleRemoteTransfer(t, f, transfers[0].ID, "")
	input.RequestID, input.ExpectedVersion, input.Enabled, input.RemoteTarget = "disable_remote_schedule", 1, false, nil
	disabled, err := f.service.SaveDatabaseBackupSchedule(f.ctx, f.resource.ID, schedule.ID, input)
	if err != nil || !reflect.DeepEqual(disabled.RemoteTarget, schedule.RemoteTarget) {
		t.Fatal("disable discarded pinned remote target", err)
	}
}

func TestPostgresMySQLRemoteAuthority(t *testing.T) {
	for _, scenario := range []string{"schema", "capability", "target_revision", "credential"} {
		t.Run(scenario, func(t *testing.T) {
			f, target := mysqlRemoteFixture(t)
			input := domain.HostingDatabaseBackupInput{RequestID: "mysql_denied", ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: target.Revision}}
			var err error
			switch scenario {
			case "schema":
				_, err = f.db.Exec(f.ctx, `ALTER FUNCTION nerocd_mysql_remote_backup_schema_compatible() RENAME TO unavailable_mysql_remote_schema`)
			case "capability":
				_, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLBackupRemoteCapability)
			case "target_revision":
				input.RemoteTarget.Revision++
			case "credential":
				_, err = f.db.Exec(f.ctx, `UPDATE hosting_database_backup_remote_credentials SET status='revoked' WHERE id IN(SELECT access_id FROM hosting_database_backup_target_versions WHERE target_id=$1)`, target.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, input); err == nil {
				t.Fatal("unavailable MySQL remote authority admitted dump")
			}
			var count int
			if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM hosting_database_backups`).Scan(&count); err != nil || count != 0 {
				t.Fatal("rejected remote selection left local operation", err, count)
			}
		})
	}
}

func TestPostgresMySQLRemoteClaimAndCredentialCapability(t *testing.T) {
	f, target := mysqlRemoteFixture(t)
	backup, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "mysql_remote_claim", ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: 1}})
	if err != nil {
		t.Fatal(err)
	}
	completeBackup(t, f, backup.ID)
	upload := "db_upload_" + backup.ID
	if err = f.pg.PrepareDatabaseBackupTransfer(f.ctx, upload); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLBackupRemoteCapability); err != nil {
		t.Fatal(err)
	}
	if _, err = f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("runner with only shared validation capability claimed MySQL upload", err)
	}
	var leases int
	if err = f.db.QueryRow(f.ctx, `SELECT count(*) FROM run_leases WHERE run_id='db_transfer_'||$1`, upload).Scan(&leases); err != nil || leases != 0 {
		t.Fatal("denied claim retained lease", err, leases)
	}
	if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_append(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLBackupRemoteCapability); err != nil {
		t.Fatal(err)
	}
	a, plan := claimRemoteTransfer(t, f, upload)
	if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseMySQLBackupRemoteCapability); err != nil {
		t.Fatal(err)
	}
	request := domain.BackupRemoteCredentialRequest{Authority: a, DeliveryID: "mysql_revoked_delivery", PlanHash: plan.Hash, TargetID: target.ID, TargetRevision: 1, DaemonID: plan.DaemonID}
	grant, err := f.pg.AuthorizeBackupRemoteCredentials(f.ctx, f.runner, f.credential, request)
	if err == nil || grant.Records.Access.Metadata.SecretID != "" || grant.Records.Secret.Metadata.SecretID != "" {
		t.Fatal("revoked adapter released remote credentials", err)
	}
	if _, err = f.pg.DatabaseBackupTransferPlan(f.ctx, f.runner, a); err == nil {
		t.Fatal("revoked adapter retained live plan authority")
	}
}

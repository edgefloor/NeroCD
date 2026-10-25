package store_test

import (
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"nerocd/internal/app"
	"nerocd/internal/domain"
	"testing"
	"time"
)

func TestPostgresRemoteBackupDeniedTransferDoesNotStarveLocalClaim(t *testing.T) {
	for _, kind := range []string{"target_disabled", "keys_replaced", "database_error"} {
		t.Run(kind, func(t *testing.T) {
			f, target := remoteBackupFixture(t)
			backup, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "blocked_upload", ExpectedResourceGeneration: f.resource.Generation, RemoteTarget: &domain.BackupTargetSelection{TargetID: target.ID, Revision: 1}})
			if err != nil {
				t.Fatal(err)
			}
			completeBackup(t, f, backup.ID)
			upload := "db_upload_" + backup.ID
			if err = f.pg.PrepareDatabaseBackupTransfer(f.ctx, upload); err != nil {
				t.Fatal(err)
			}
			edit := domain.HostingDatabaseBackupTargetInput{RequestID: "target_change", ExpectedRevision: 1, Name: target.Name, Endpoint: target.Endpoint, Bucket: target.Bucket, Region: target.Region, Enabled: kind != "target_disabled"}
			var credentials *app.BackupTargetCredentials
			if kind == "keys_replaced" {
				credentials, err = app.NewBackupTargetCredentials([]byte("new-access"), []byte("new-secret"))
				if err != nil {
					t.Fatal(err)
				}
				defer credentials.Close()
			}
			if kind != "database_error" {
				if _, err = f.service.SaveDatabaseBackupTarget(f.ctx, target.ProjectID, target.ID, edit, credentials); err != nil {
					t.Fatal(err)
				}
			}
			local, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "eligible_local", ExpectedResourceGeneration: f.resource.Generation})
			if err != nil {
				t.Fatal(err)
			}
			if err = f.pg.PrepareDatabaseBackup(f.ctx, local.ID); err != nil {
				t.Fatal(err)
			}
			if kind == "database_error" {
				if _, err = f.db.Exec(f.ctx, `ALTER TABLE hosting_database_backup_remote_credentials RENAME TO unavailable_remote_credentials`); err != nil {
					t.Fatal(err)
				}
				_, claimErr := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
				if _, err = f.db.Exec(f.ctx, `ALTER TABLE unavailable_remote_credentials RENAME TO hosting_database_backup_remote_credentials`); err != nil {
					t.Fatal(err)
				}
				var databaseErr *pgconn.PgError
				if !errors.As(claimErr, &databaseErr) || databaseErr.Code != "42P01" {
					t.Fatal("genuine database failure was hidden", claimErr)
				}
				// Revocation now supplies an expected denial on the exact same queue item.
				if _, err = f.service.SaveDatabaseBackupTarget(f.ctx, target.ProjectID, target.ID, domain.HostingDatabaseBackupTargetInput{RequestID: "disable_after_error", ExpectedRevision: 1, Name: target.Name, Endpoint: target.Endpoint, Bucket: target.Bucket, Region: target.Region, Enabled: false}, nil); err != nil {
					t.Fatal(err)
				}
			}
			claim, err := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
			if err != nil || claim.Run.RunSpec.HostingOperationID != local.ID {
				t.Fatal("denied transfer starved eligible local work", err)
			}
			var leases, receipts, deliveries int
			if err = f.db.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM run_leases WHERE run_id='db_transfer_'||$1),(SELECT count(*) FROM hosting_database_backup_transfer_receipts WHERE operation_id=$1),(SELECT count(*) FROM hosting_database_backup_remote_deliveries WHERE operation_id=$1)`, upload).Scan(&leases, &receipts, &deliveries); err != nil || leases != 0 || receipts != 0 || deliveries != 0 {
				t.Fatal("denied transfer gained authority or effect evidence", err)
			}
			view, err := f.service.GetDatabaseBackupTransfer(f.ctx, upload)
			if err != nil || view.State != "dispatched" {
				t.Fatal("claim skip relabeled existing operation", err)
			}
		})
	}
}

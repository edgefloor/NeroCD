package store_test

import (
	"errors"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresDatabaseBackupDeniedClaimPreservesOwnershipAndQueueProgress(t *testing.T) {
	for _, mode := range []string{"backup", "restore", "database_error"} {
		t.Run(mode, func(t *testing.T) {
			f, target := remoteBackupFixture(t)
			backup, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "local_authority", ExpectedResourceGeneration: f.resource.Generation})
			if err != nil {
				t.Fatal(err)
			}
			denied := backup
			if mode == "restore" {
				backup = completeBackup(t, f, backup.ID)
				denied, err = f.service.RequestDatabaseBackupRestore(f.ctx, backup.ID, domain.HostingDatabaseBackupRestoreInput{RequestID: "held_restore", Name: "Held restore"})
				if err != nil {
					t.Fatal(err)
				}
				database, err := f.service.GetHostingDatabase(f.ctx, denied.TargetResourceID)
				if err != nil {
					t.Fatal(err)
				}
				prepareBackupRestoreChild(t, f, denied, database)
			}
			if err = f.pg.PrepareDatabaseBackup(f.ctx, denied.ID); err != nil {
				t.Fatal(err)
			}
			validation, err := f.service.ValidateDatabaseBackupTarget(f.ctx, target.ProjectID, target.ID, domain.HostingDatabaseBackupTargetValidationInput{RequestID: "later_eligible_validation", ExpectedRevision: 1, DestinationID: f.op.Snapshot.DestinationID})
			if err != nil {
				t.Fatal(err)
			}
			if err = f.pg.PrepareDatabaseBackupTransfer(f.ctx, validation.ID); err != nil {
				t.Fatal(err)
			}
			if mode == "database_error" {
				var definition string
				if err = f.db.QueryRow(f.ctx, `SELECT pg_get_functiondef('nerocd_hosting_database_schema_compatible()'::regprocedure)`).Scan(&definition); err != nil {
					t.Fatal(err)
				}
				if _, err = f.db.Exec(f.ctx, `CREATE OR REPLACE FUNCTION nerocd_hosting_database_schema_compatible() RETURNS boolean LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'owned fixture query failure' USING ERRCODE='58000'; END $$`); err != nil {
					t.Fatal(err)
				}
				_, claimErr := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
				if _, err = f.db.Exec(f.ctx, definition); err != nil {
					t.Fatal(err)
				}
				if !errors.Is(claimErr, store.ErrHostingDatabaseUnavailable) {
					t.Fatal("local gate hid database failure", claimErr)
				}
			}
			if _, err = f.db.Exec(f.ctx, `UPDATE runners SET capabilities=array_remove(capabilities,$2) WHERE id=$1`, f.runner, domain.HostingDatabaseBackupCapability); err != nil {
				t.Fatal(err)
			}
			claim, err := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
			if err != nil || claim.Run.RunSpec.HostingOperationID != validation.ID {
				t.Fatal("denied local operation starved independent eligible work", err)
			}
			var leases, receipts int
			var state string
			var held bool
			if err = f.db.QueryRow(f.ctx, `SELECT state,ownership_held,(SELECT count(*) FROM run_leases WHERE run_id='db_backup_'||$1),(SELECT count(*) FROM hosting_database_backup_receipts WHERE operation_id=$1) FROM hosting_database_backups WHERE id=$1`, denied.ID).Scan(&state, &held, &leases, &receipts); err != nil || state != "dispatched" || !held || leases != 0 || receipts != 0 {
				t.Fatal("claim skip altered unresolved ownership or effect history", err)
			}
		})
	}
}

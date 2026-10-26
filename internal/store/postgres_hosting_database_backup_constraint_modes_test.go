package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"nerocd/internal/domain"
	"nerocd/internal/store/sqlcgen"
)

func TestPostgresDatabaseBackupCopyConstraintModes(t *testing.T) {
	f := retentionFixture(t)
	backup := ownedRetentionBackup(t, f, 0)
	copyID := "dbc_local_" + backup.ID
	missingID := "missing_successor"
	for _, generated := range []bool{false, true} {
		name := "statement"
		if generated {
			name = "sqlc"
		}
		t.Run(name, func(t *testing.T) {
			tx, err := f.db.Begin(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
					t.Error(err)
				}
			})
			q := sqlcgen.New(tx)
			if err = q.FlushHostingDatabaseBindingConstraints(f.ctx); err != nil {
				t.Fatal(err)
			}
			if generated {
				err = q.DeferDatabaseBackupCopySupersession(f.ctx)
			} else {
				_, err = tx.Exec(f.ctx, `SET CONSTRAINTS hosting_database_backup_copies_superseded_by_copy_id_fkey, hosting_database_backup_copy_supersession_receipt DEFERRED`)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = q.SupersedeDatabaseBackupCopy(f.ctx, sqlcgen.SupersedeDatabaseBackupCopyParams{ID: copyID, SupersededByCopyID: &missingID}); err != nil {
				t.Fatal("selected constraints did not defer", err)
			}
			err = q.FlushHostingDatabaseBindingConstraints(f.ctx)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23503" && pgErr.Code != "23514" {
				t.Fatalf("immediate flush accepted missing successor: %v", err)
			}
			if err = tx.Rollback(f.ctx); err != nil {
				t.Fatal(err)
			}
			var successor *string
			if err = f.db.QueryRow(f.ctx, `SELECT superseded_by_copy_id FROM hosting_database_backup_copies WHERE id=$1`, copyID).Scan(&successor); err != nil || successor != nil {
				t.Fatalf("failed flush persisted supersession: successor=%v error=%v", successor, err)
			}
		})
	}
	// A fresh transaction must still enforce the immediate boundary. Deferring
	// the previous transaction cannot change another transaction's modes.
	tx, err := f.db.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			t.Error(err)
		}
	})
	q := sqlcgen.New(tx)
	if err = q.FlushHostingDatabaseBindingConstraints(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err = q.SupersedeDatabaseBackupCopy(f.ctx, sqlcgen.SupersedeDatabaseBackupCopyParams{ID: copyID, SupersededByCopyID: &missingID}); err == nil {
		t.Fatal("deferred mode escaped its transaction")
	}
	var securityDefiner bool
	if err = f.db.QueryRow(f.ctx, `SELECT prosecdef FROM pg_proc WHERE oid='nerocd_database_backup_defer_copy_supersession()'::regprocedure`).Scan(&securityDefiner); err != nil || securityDefiner {
		t.Fatalf("constraint helper must use invoker rights: security_definer=%t error=%v", securityDefiner, err)
	}
}

func TestPostgresDatabaseBackupAuthorityAttemptDoesNotNarrow(t *testing.T) {
	overflow := uint64(1) << 32
	t.Run("backup", func(t *testing.T) {
		f := backupFixture(t)
		v, err := f.service.RequestDatabaseBackup(f.ctx, f.resource.ID, domain.HostingDatabaseBackupInput{RequestID: "attempt_bounds", ExpectedResourceGeneration: f.resource.Generation})
		if err != nil {
			t.Fatal(err)
		}
		a, _ := claimBackup(t, f, v.ID)
		a.Attempt += int(overflow)
		_, err = f.pg.DatabaseBackupPlan(f.ctx, f.runner, a)
		assertBackupAttemptRangeError(t, err)
		a.Attempt -= int(overflow)
		if _, err = f.pg.DatabaseBackupPlan(f.ctx, f.runner, a); err != nil {
			t.Fatal("denied request changed valid backup authority", err)
		}
	})
	t.Run("transfer", func(t *testing.T) {
		f, target := remoteBackupFixture(t)
		v, err := f.service.ValidateDatabaseBackupTarget(f.ctx, f.resource.ProjectID, target.ID, domain.HostingDatabaseBackupTargetValidationInput{RequestID: "attempt_bounds", ExpectedRevision: target.Revision, DestinationID: f.op.Snapshot.DestinationID})
		if err != nil {
			t.Fatal(err)
		}
		a, _ := claimRemoteTransfer(t, f, v.ID)
		a.Attempt += int(overflow)
		_, err = f.pg.DatabaseBackupTransferPlan(f.ctx, f.runner, a)
		assertBackupAttemptRangeError(t, err)
		a.Attempt -= int(overflow)
		if _, err = f.pg.DatabaseBackupTransferPlan(f.ctx, f.runner, a); err != nil {
			t.Fatal("denied request changed valid transfer authority", err)
		}
	})
	t.Run("retention", func(t *testing.T) {
		f, v := admittedRetentionBatch(t)
		if err := f.pg.PrepareDatabaseBackupRetention(f.ctx, v.ID); err != nil {
			t.Fatal(err)
		}
		claim, err := f.pg.ClaimRun(f.ctx, f.runner, time.Now(), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		a := domain.HostingAuthority{OperationID: v.ID, RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Fence: claim.Lease.Fence}
		if _, err = f.pg.DatabaseBackupRetentionPlan(f.ctx, f.runner, a); err != nil {
			t.Fatal("valid attempt denied", err)
		}
		a.Attempt += int(overflow)
		_, err = f.pg.DatabaseBackupRetentionPlan(f.ctx, f.runner, a)
		assertBackupAttemptRangeError(t, err)
		a.Attempt -= int(overflow)
		if _, err = f.pg.DatabaseBackupRetentionPlan(f.ctx, f.runner, a); err != nil {
			t.Fatal("denied request changed valid retention authority", err)
		}
	})
}

func assertBackupAttemptRangeError(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "22003" {
		t.Fatalf("oversized attempt must fail integer range checking before plan publication: %v", err)
	}
}

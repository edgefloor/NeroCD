package hostingjobs

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// DatabaseBackupTransferKind identifies the database backup transfer River job kind.
const DatabaseBackupTransferKind = "hosting.database_backup_transfer.v1"

// DatabaseBackupTransferArgs identifies the operation handled by a database backup transfer job.
type DatabaseBackupTransferArgs struct {
	OperationID string `json:"operation_id"`
}

// Kind returns the River job kind.
func (DatabaseBackupTransferArgs) Kind() string { return DatabaseBackupTransferKind }

// InsertOpts returns the queue and uniqueness options for this job.
func (DatabaseBackupTransferArgs) InsertOpts() river.InsertOpts {
	return (PrepareDeploymentArgs{}).InsertOpts()
}

// DatabaseBackupTransferHandler handles database backup transfer jobs.
type DatabaseBackupTransferHandler interface {
	PrepareDatabaseBackupTransfer(context.Context, string) error
}
type databaseBackupTransferWorker struct {
	river.WorkerDefaults[DatabaseBackupTransferArgs]
	handler DatabaseBackupTransferHandler
}

func (w *databaseBackupTransferWorker) Work(ctx context.Context, job *river.Job[DatabaseBackupTransferArgs]) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("hosting_database_backup_transfer_panicked")
		}
	}()
	if !operationIDPattern.MatchString(job.Args.OperationID) {
		return river.JobCancel(ErrInvalidOperation)
	}
	if w.handler.PrepareDatabaseBackupTransfer(ctx, job.Args.OperationID) != nil {
		return errors.New("hosting_database_backup_transfer_failed")
	}
	return nil
}

// DatabaseBackupTransferEnabled reports whether the database backup transfer worker is
// configured.
func (c *Client) DatabaseBackupTransferEnabled() bool { return c != nil && c.hasDatabaseBackupTransfer }

// EnqueueDatabaseBackupTransferTx enqueues a database backup transfer job in the caller's
// transaction.
func (c *Client) EnqueueDatabaseBackupTransferTx(ctx context.Context, tx pgx.Tx, args DatabaseBackupTransferArgs) (int64, error) {
	if !c.DatabaseBackupTransferEnabled() {
		return 0, ErrUnavailable
	}
	if tx == nil || !operationIDPattern.MatchString(args.OperationID) {
		return 0, ErrInvalidOperation
	}
	return c.enqueueTx(ctx, tx, args)
}

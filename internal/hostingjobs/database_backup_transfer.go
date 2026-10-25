package hostingjobs

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

const DatabaseBackupTransferKind = "hosting.database_backup_transfer.v1"

type DatabaseBackupTransferArgs struct {
	OperationID string `json:"operation_id"`
}

func (DatabaseBackupTransferArgs) Kind() string { return DatabaseBackupTransferKind }
func (DatabaseBackupTransferArgs) InsertOpts() river.InsertOpts {
	return (PrepareDeploymentArgs{}).InsertOpts()
}

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
func (c *Client) DatabaseBackupTransferEnabled() bool { return c != nil && c.hasDatabaseBackupTransfer }
func (c *Client) EnqueueDatabaseBackupTransferTx(ctx context.Context, tx pgx.Tx, args DatabaseBackupTransferArgs) (int64, error) {
	if !c.DatabaseBackupTransferEnabled() {
		return 0, ErrUnavailable
	}
	if tx == nil || !operationIDPattern.MatchString(args.OperationID) {
		return 0, ErrInvalidOperation
	}
	return c.enqueueTx(ctx, tx, args)
}

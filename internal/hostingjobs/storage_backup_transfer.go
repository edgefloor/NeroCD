package hostingjobs

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

const StorageBackupTransferKind = "hosting.storage_backup_transfer.v1"

type StorageBackupTransferArgs struct {
	OperationID string `json:"operation_id"`
}

func (StorageBackupTransferArgs) Kind() string { return StorageBackupTransferKind }
func (StorageBackupTransferArgs) InsertOpts() river.InsertOpts {
	return (PrepareDeploymentArgs{}).InsertOpts()
}

type StorageBackupTransferHandler interface {
	PrepareStorageBackupTransfer(context.Context, string) error
}
type storageBackupTransferWorker struct {
	river.WorkerDefaults[StorageBackupTransferArgs]
	handler StorageBackupTransferHandler
}

func (w *storageBackupTransferWorker) Work(ctx context.Context, job *river.Job[StorageBackupTransferArgs]) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("hosting_storage_backup_transfer_panicked")
		}
	}()
	if !operationIDPattern.MatchString(job.Args.OperationID) {
		return river.JobCancel(ErrInvalidOperation)
	}
	if w.handler.PrepareStorageBackupTransfer(ctx, job.Args.OperationID) != nil {
		return errors.New("hosting_storage_backup_transfer_failed")
	}
	return nil
}
func (c *Client) StorageBackupTransferEnabled() bool { return c != nil && c.hasStorageBackupTransfer }
func (c *Client) EnqueueStorageBackupTransferTx(ctx context.Context, tx pgx.Tx, args StorageBackupTransferArgs) (int64, error) {
	if !c.StorageBackupTransferEnabled() {
		return 0, ErrUnavailable
	}
	if tx == nil || !operationIDPattern.MatchString(args.OperationID) {
		return 0, ErrInvalidOperation
	}
	return c.enqueueTx(ctx, tx, args)
}

package hostingjobs

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

const StorageBackupRetentionKind = "hosting.storage_backup_retention.v1"

type StorageBackupRetentionArgs struct {
	OperationID string `json:"operation_id"`
}

func (StorageBackupRetentionArgs) Kind() string { return StorageBackupRetentionKind }
func (StorageBackupRetentionArgs) InsertOpts() river.InsertOpts {
	return (PrepareDeploymentArgs{}).InsertOpts()
}

type StorageBackupRetentionHandler interface {
	PrepareStorageBackupRetention(context.Context, string) error
}
type storageBackupRetentionWorker struct {
	river.WorkerDefaults[StorageBackupRetentionArgs]
	handler StorageBackupRetentionHandler
}

func (w *storageBackupRetentionWorker) Work(ctx context.Context, job *river.Job[StorageBackupRetentionArgs]) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("hosting_storage_backup_retention_panicked")
		}
	}()
	if !operationIDPattern.MatchString(job.Args.OperationID) {
		return river.JobCancel(ErrInvalidOperation)
	}
	if w.handler.PrepareStorageBackupRetention(ctx, job.Args.OperationID) != nil {
		return errors.New("hosting_storage_backup_retention_failed")
	}
	return nil
}
func (c *Client) StorageBackupRetentionEnabled() bool {
	return c != nil && c.hasStorageBackupRetention
}
func (c *Client) EnqueueStorageBackupRetentionTx(ctx context.Context, tx pgx.Tx, args StorageBackupRetentionArgs) (int64, error) {
	if !c.StorageBackupRetentionEnabled() {
		return 0, ErrUnavailable
	}
	if tx == nil || !operationIDPattern.MatchString(args.OperationID) {
		return 0, ErrInvalidOperation
	}
	return c.enqueueTx(ctx, tx, args)
}

package hostingjobs

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

const DatabaseBackupRetentionKind = "hosting.database_backup_retention.v1"

type DatabaseBackupRetentionArgs struct {
	OperationID string `json:"operation_id"`
}

func (DatabaseBackupRetentionArgs) Kind() string { return DatabaseBackupRetentionKind }
func (DatabaseBackupRetentionArgs) InsertOpts() river.InsertOpts {
	return (PrepareDeploymentArgs{}).InsertOpts()
}

type DatabaseBackupRetentionHandler interface {
	PrepareDatabaseBackupRetention(context.Context, string) error
}
type databaseBackupRetentionWorker struct {
	river.WorkerDefaults[DatabaseBackupRetentionArgs]
	handler DatabaseBackupRetentionHandler
}

func (w *databaseBackupRetentionWorker) Work(ctx context.Context, job *river.Job[DatabaseBackupRetentionArgs]) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("hosting_database_backup_retention_panicked")
		}
	}()
	if !operationIDPattern.MatchString(job.Args.OperationID) {
		return river.JobCancel(ErrInvalidOperation)
	}
	if w.handler.PrepareDatabaseBackupRetention(ctx, job.Args.OperationID) != nil {
		return errors.New("hosting_database_backup_retention_failed")
	}
	return nil
}
func (c *Client) DatabaseBackupRetentionEnabled() bool {
	return c != nil && c.hasDatabaseBackupRetention
}
func (c *Client) EnqueueDatabaseBackupRetentionTx(ctx context.Context, tx pgx.Tx, args DatabaseBackupRetentionArgs) (int64, error) {
	if !c.DatabaseBackupRetentionEnabled() {
		return 0, ErrUnavailable
	}
	if tx == nil || !operationIDPattern.MatchString(args.OperationID) {
		return 0, ErrInvalidOperation
	}
	return c.enqueueTx(ctx, tx, args)
}

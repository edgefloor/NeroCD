package hostingjobs

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// DatabaseBackupRetentionKind identifies the database backup retention River job kind.
const DatabaseBackupRetentionKind = "hosting.database_backup_retention.v1"

// DatabaseBackupRetentionArgs identifies the operation handled by a database backup retention
// job.
type DatabaseBackupRetentionArgs struct {
	OperationID string `json:"operation_id"`
}

// Kind returns the River job kind.
func (DatabaseBackupRetentionArgs) Kind() string { return DatabaseBackupRetentionKind }

// InsertOpts returns the queue and uniqueness options for this job.
func (DatabaseBackupRetentionArgs) InsertOpts() river.InsertOpts {
	return (PrepareDeploymentArgs{}).InsertOpts()
}

// DatabaseBackupRetentionHandler handles database backup retention jobs.
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

// DatabaseBackupRetentionEnabled reports whether the database backup retention worker is
// configured.
func (c *Client) DatabaseBackupRetentionEnabled() bool {
	return c != nil && c.hasDatabaseBackupRetention
}

// EnqueueDatabaseBackupRetentionTx enqueues a database backup retention job in the caller's
// transaction.
func (c *Client) EnqueueDatabaseBackupRetentionTx(ctx context.Context, tx pgx.Tx, args DatabaseBackupRetentionArgs) (int64, error) {
	if !c.DatabaseBackupRetentionEnabled() {
		return 0, ErrUnavailable
	}
	if tx == nil || !operationIDPattern.MatchString(args.OperationID) {
		return 0, ErrInvalidOperation
	}
	return c.enqueueTx(ctx, tx, args)
}

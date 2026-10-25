package hostingjobs

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

const DatabaseBackupKind = "hosting.database_backup.v1"

var ErrDatabaseBackupWaiting = errors.New("hosting_database_backup_waiting")

type DatabaseBackupArgs struct {
	OperationID string `json:"operation_id"`
}

func (DatabaseBackupArgs) Kind() string { return DatabaseBackupKind }
func (DatabaseBackupArgs) InsertOpts() river.InsertOpts {
	return (PrepareDeploymentArgs{}).InsertOpts()
}

type DatabaseBackupHandler interface {
	PrepareDatabaseBackup(context.Context, string) error
}
type databaseBackupWorker struct {
	river.WorkerDefaults[DatabaseBackupArgs]
	handler DatabaseBackupHandler
}

func (w *databaseBackupWorker) Work(ctx context.Context, job *river.Job[DatabaseBackupArgs]) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("hosting_database_backup_panicked")
		}
	}()
	if !operationIDPattern.MatchString(job.Args.OperationID) {
		return river.JobCancel(ErrInvalidOperation)
	}
	err = w.handler.PrepareDatabaseBackup(ctx, job.Args.OperationID)
	if errors.Is(err, ErrDatabaseBackupWaiting) {
		return river.JobSnooze(time.Second)
	}
	if err != nil {
		return errors.New("hosting_database_backup_failed")
	}
	return nil
}
func (c *Client) DatabaseBackupEnabled() bool { return c != nil && c.hasDatabaseBackup }
func (c *Client) EnqueueDatabaseBackupTx(ctx context.Context, tx pgx.Tx, args DatabaseBackupArgs) (int64, error) {
	if !c.DatabaseBackupEnabled() {
		return 0, ErrUnavailable
	}
	if tx == nil || !operationIDPattern.MatchString(args.OperationID) {
		return 0, ErrInvalidOperation
	}
	return c.enqueueTx(ctx, tx, args)
}

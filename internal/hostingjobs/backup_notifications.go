package hostingjobs

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// BackupNotificationKind identifies the backup notification River job kind.
const BackupNotificationKind = "hosting.backup_notification.v1"

// BackupNotificationArgs contains no recipient, target, payload, or credentials.
type BackupNotificationArgs struct {
	DeliveryID string `json:"delivery_id"`
}

// Kind returns the River job kind.
func (BackupNotificationArgs) Kind() string { return BackupNotificationKind }

// InsertOpts returns the queue and uniqueness options for this job.
func (BackupNotificationArgs) InsertOpts() river.InsertOpts {
	v := (PrepareDeploymentArgs{}).InsertOpts()
	v.MaxAttempts = 5
	return v
}

// BackupNotificationHandler handles backup notification jobs.
type BackupNotificationHandler interface {
	SendBackupNotification(context.Context, string) error
}
type backupNotificationWorker struct {
	river.WorkerDefaults[BackupNotificationArgs]
	handler BackupNotificationHandler
}

func (w *backupNotificationWorker) Work(ctx context.Context, job *river.Job[BackupNotificationArgs]) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("notification_delivery_failed")
		}
	}()
	if !operationIDPattern.MatchString(job.Args.DeliveryID) {
		return river.JobCancel(ErrInvalidOperation)
	}
	if w.handler.SendBackupNotification(ctx, job.Args.DeliveryID) != nil {
		return errors.New("notification_delivery_failed")
	}
	return nil
}

// BackupNotificationEnabled reports whether the backup notification worker is configured.
func (c *Client) BackupNotificationEnabled() bool { return c != nil && c.hasBackupNotification }

// EnqueueBackupNotificationTx enqueues a backup notification job in the caller's transaction.
func (c *Client) EnqueueBackupNotificationTx(ctx context.Context, tx pgx.Tx, args BackupNotificationArgs) (int64, error) {
	if !c.BackupNotificationEnabled() {
		return 0, ErrUnavailable
	}
	if tx == nil || !operationIDPattern.MatchString(args.DeliveryID) {
		return 0, ErrInvalidOperation
	}
	return c.enqueueTx(ctx, tx, args)
}

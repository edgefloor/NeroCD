package hostingjobs

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// PrepareStorageArchiveKind dispatches only an opaque archive operation ID.
const PrepareStorageArchiveKind = "hosting.prepare_storage_archive.v1"

// PrepareStorageArchiveArgs never contains helper commands, paths or credentials.
type PrepareStorageArchiveArgs struct {
	OperationID string `json:"operation_id"`
}

// Kind returns the versioned archive preparation job contract.
func (PrepareStorageArchiveArgs) Kind() string { return PrepareStorageArchiveKind }

// InsertOpts preserves the bounded hosting queue retry and deduplication policy.
func (PrepareStorageArchiveArgs) InsertOpts() river.InsertOpts {
	return (PrepareDeploymentArgs{}).InsertOpts()
}

// StorageArchiveHandler dispatches fenced tasks without performing runtime I/O.
type StorageArchiveHandler interface {
	PrepareStorageArchive(context.Context, string) error
}
type storageArchiveWorker struct {
	river.WorkerDefaults[PrepareStorageArchiveArgs]
	handler StorageArchiveHandler
}

func (w *storageArchiveWorker) Work(ctx context.Context, job *river.Job[PrepareStorageArchiveArgs]) error {
	if !operationIDPattern.MatchString(job.Args.OperationID) {
		return river.JobCancel(ErrInvalidOperation)
	}
	return w.handler.PrepareStorageArchive(ctx, job.Args.OperationID)
}
func (*storageArchiveWorker) NextRetry(job *river.Job[PrepareStorageArchiveArgs]) time.Time {
	return time.Now().Add(time.Duration(job.Attempt) * time.Second)
}

// EnqueueStorageArchiveTx inserts only when a real archive dispatcher is registered.
func (c *Client) EnqueueStorageArchiveTx(ctx context.Context, tx pgx.Tx, args PrepareStorageArchiveArgs) (int64, error) {
	if !c.hasStorageArchive {
		return 0, ErrUnavailable
	}
	if tx == nil || !operationIDPattern.MatchString(args.OperationID) {
		return 0, ErrInvalidOperation
	}
	return c.enqueueTx(ctx, tx, args)
}

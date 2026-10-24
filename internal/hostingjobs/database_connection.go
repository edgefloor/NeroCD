package hostingjobs

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// DatabaseConnectionKind wakes durable connection state, never runtime effects.
const DatabaseConnectionKind = "hosting.database_connection.v1"

// ErrDatabaseConnectionWaiting asks River to wait without exhausting child retries.
var ErrDatabaseConnectionWaiting = errors.New("hosting_database_connection_waiting")

// DatabaseConnectionArgs contains only an immutable controller workflow identifier.
type DatabaseConnectionArgs struct {
	ConnectionID string `json:"connection_id"`
}

// Kind returns the bounded continuation job family.
func (DatabaseConnectionArgs) Kind() string { return DatabaseConnectionKind }

// InsertOpts reuses hosting queue limits and active-state deduplication.
func (DatabaseConnectionArgs) InsertOpts() river.InsertOpts {
	return (PrepareDeploymentArgs{}).InsertOpts()
}

// DatabaseConnectionHandler advances persisted children without Docker or secrets.
type DatabaseConnectionHandler interface {
	PrepareHostingDatabaseConnection(context.Context, string) error
}

type databaseConnectionWorker struct {
	river.WorkerDefaults[DatabaseConnectionArgs]
	handler DatabaseConnectionHandler
}

func (w *databaseConnectionWorker) Work(ctx context.Context, job *river.Job[DatabaseConnectionArgs]) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("hosting_database_connection_panicked")
		}
	}()
	if !operationIDPattern.MatchString(job.Args.ConnectionID) {
		return river.JobCancel(ErrInvalidOperation)
	}
	err = w.handler.PrepareHostingDatabaseConnection(ctx, job.Args.ConnectionID)
	if errors.Is(err, ErrDatabaseConnectionWaiting) {
		return river.JobSnooze(time.Second)
	}
	if errors.Is(err, ErrInvalidOperation) {
		return river.JobCancel(ErrInvalidOperation)
	}
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("hosting_database_connection_cancelled")
		}
		return errors.New("hosting_database_connection_failed")
	}
	return nil
}

// DatabaseConnectionEnabled requires a supplied real continuation handler.
func (c *Client) DatabaseConnectionEnabled() bool { return c != nil && c.hasDatabaseConnection }

// EnqueueDatabaseConnectionTx commits intent in the owning application transaction.
func (c *Client) EnqueueDatabaseConnectionTx(ctx context.Context, tx pgx.Tx, args DatabaseConnectionArgs) (int64, error) {
	if !c.DatabaseConnectionEnabled() {
		return 0, ErrUnavailable
	}
	if tx == nil || !operationIDPattern.MatchString(args.ConnectionID) {
		return 0, ErrInvalidOperation
	}
	return c.enqueueTx(ctx, tx, args)
}

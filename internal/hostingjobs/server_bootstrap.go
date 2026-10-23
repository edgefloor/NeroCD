package hostingjobs

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// ServerBootstrapArgs carries only an opaque persisted operation identity.
type ServerBootstrapArgs struct {
	OperationID string `json:"operation_id"`
}

// Kind returns the versioned opaque bootstrap job contract.
func (ServerBootstrapArgs) Kind() string { return "hosting.server_bootstrap.v1" }

// InsertOpts reuses bounded hosting retries and active-state deduplication.
func (ServerBootstrapArgs) InsertOpts() river.InsertOpts {
	return (PrepareDeploymentArgs{}).InsertOpts()
}

// ServerBootstrapHandler resolves authority and secrets at execution time.
type ServerBootstrapHandler interface {
	PrepareServerBootstrap(context.Context, string) error
}
type serverBootstrapWorker struct {
	river.WorkerDefaults[ServerBootstrapArgs]
	handler ServerBootstrapHandler
}

func (w *serverBootstrapWorker) Work(ctx context.Context, job *river.Job[ServerBootstrapArgs]) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("server_bootstrap_panicked")
		}
	}()
	if !operationIDPattern.MatchString(job.Args.OperationID) {
		return river.JobCancel(ErrInvalidOperation)
	}
	if err = w.handler.PrepareServerBootstrap(ctx, job.Args.OperationID); err != nil {
		return errors.New("server_bootstrap_failed")
	}
	return nil
}

// EnqueueServerBootstrapTx shares the caller's transaction and queue bounds.
func (c *Client) EnqueueServerBootstrapTx(ctx context.Context, tx pgx.Tx, args ServerBootstrapArgs) (int64, error) {
	if !c.hasServerBootstrap {
		return 0, ErrUnavailable
	}
	if tx == nil || !operationIDPattern.MatchString(args.OperationID) {
		return 0, ErrInvalidOperation
	}
	return c.enqueueTx(ctx, tx, args)
}

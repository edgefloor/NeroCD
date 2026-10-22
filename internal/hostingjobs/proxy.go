package hostingjobs

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// PrepareProxyKind is the immutable operation-ID-only proxy dispatch contract.
const PrepareProxyKind = "hosting.prepare_proxy.v1"

// PrepareProxyArgs contains an opaque identifier, never host configuration or credentials.
type PrepareProxyArgs struct {
	OperationID string `json:"operation_id"`
}

// Kind returns the versioned proxy preparation kind.
func (PrepareProxyArgs) Kind() string { return PrepareProxyKind }

// InsertOpts applies the bounded hosting queue's retry and deduplication policy.
func (PrepareProxyArgs) InsertOpts() river.InsertOpts { return (PrepareDeploymentArgs{}).InsertOpts() }

// ProxyHandler performs exact-target task dispatch without running the proxy engine.
type ProxyHandler interface {
	PrepareProxy(context.Context, string) error
}

type prepareProxyWorker struct {
	river.WorkerDefaults[PrepareProxyArgs]
	handler ProxyHandler
}

func (w *prepareProxyWorker) Work(ctx context.Context, job *river.Job[PrepareProxyArgs]) error {
	if !operationIDPattern.MatchString(job.Args.OperationID) {
		return river.JobCancel(ErrInvalidOperation)
	}
	return w.handler.PrepareProxy(ctx, job.Args.OperationID)
}
func (*prepareProxyWorker) NextRetry(job *river.Job[PrepareProxyArgs]) time.Time {
	return time.Now().Add(time.Duration(job.Attempt) * time.Second)
}

// EnqueueProxyTx commits proxy dispatch in the same application transaction.
// Clients without a real proxy handler refuse the job rather than queueing a stub.
func (c *Client) EnqueueProxyTx(ctx context.Context, tx pgx.Tx, args PrepareProxyArgs) (int64, error) {
	if !c.hasProxy {
		return 0, ErrUnavailable
	}
	if tx == nil || !operationIDPattern.MatchString(args.OperationID) {
		return 0, ErrInvalidOperation
	}
	return c.enqueueTx(ctx, tx, args)
}

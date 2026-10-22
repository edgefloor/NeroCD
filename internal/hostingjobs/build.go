package hostingjobs

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// PrepareBuildKind is the versioned BuildID-only hosting dispatch contract.
const PrepareBuildKind = "hosting.prepare_build.v1"

// PrepareBuildArgs contains only an opaque build ID, never source, credentials,
// plans, host configuration, or caller-selected execution authority.
type PrepareBuildArgs struct {
	BuildID string `json:"build_id"`
}

// Kind returns the versioned build preparation kind.
func (PrepareBuildArgs) Kind() string { return PrepareBuildKind }

// InsertOpts reuses the bounded hosting queue's retry and active-state deduplication policy.
func (PrepareBuildArgs) InsertOpts() river.InsertOpts { return (PrepareDeploymentArgs{}).InsertOpts() }

// BuildHandler performs DB-only dispatch after resolving persisted build state.
// It must honor cancellation and own durable business idempotency. It must not
// execute Docker or Git. Optional support requires the main Handler to implement it.
type BuildHandler interface {
	PrepareBuild(context.Context, string) error
}

type prepareBuildWorker struct {
	river.WorkerDefaults[PrepareBuildArgs]
	handler BuildHandler
}

func (w *prepareBuildWorker) Work(ctx context.Context, job *river.Job[PrepareBuildArgs]) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("hosting_build_prepare_panicked")
		}
	}()
	if !operationIDPattern.MatchString(job.Args.BuildID) {
		return river.JobCancel(ErrInvalidOperation)
	}
	if err = w.handler.PrepareBuild(ctx, job.Args.BuildID); err != nil {
		if errors.Is(err, ErrInvalidOperation) {
			return river.JobCancel(ErrInvalidOperation)
		}
		if ctx.Err() != nil {
			return errors.New("hosting_build_prepare_cancelled")
		}
		return errors.New("hosting_build_prepare_failed")
	}
	return nil
}
func (*prepareBuildWorker) NextRetry(job *river.Job[PrepareBuildArgs]) time.Time {
	return time.Now().Add(time.Duration(job.Attempt) * time.Second)
}

// EnqueueBuildTx inserts build dispatch in the caller's application transaction.
// A client without a supplied BuildHandler refuses admission without inserting.
// Endpoint, database, and schema must match the client; commit remains the caller's responsibility.
func (c *Client) EnqueueBuildTx(ctx context.Context, tx pgx.Tx, args PrepareBuildArgs) (int64, error) {
	if !c.hasBuild {
		return 0, ErrUnavailable
	}
	if tx == nil || !operationIDPattern.MatchString(args.BuildID) {
		return 0, ErrInvalidOperation
	}
	return c.enqueueTx(ctx, tx, args)
}

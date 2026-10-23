package hostingjobs

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// AutoDeployKind identifies an opaque wakeup; persisted demand owns authority.
const AutoDeployKind = "hosting.auto_deploy.v1"

// AutoDeployArgs never carries a plan, credentials, source or admission flags.
type AutoDeployArgs struct {
	ResourceID string `json:"resource_id"`
}

// Kind identifies the automatic deployment wakeup worker.
func (AutoDeployArgs) Kind() string { return AutoDeployKind }

// InsertOpts applies the existing bounded hosting queue and active deduplication.
func (AutoDeployArgs) InsertOpts() river.InsertOpts { return (PrepareDeploymentArgs{}).InsertOpts() }

// AutoDeployHandler advances the existing application admissions without host IO.
type AutoDeployHandler interface {
	PrepareHostingAutoDeploy(context.Context, string) error
}
type autoDeployWorker struct {
	river.WorkerDefaults[AutoDeployArgs]
	handler AutoDeployHandler
}

func (w *autoDeployWorker) Work(ctx context.Context, job *river.Job[AutoDeployArgs]) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("hosting_auto_deploy_panicked")
		}
	}()
	if !operationIDPattern.MatchString(job.Args.ResourceID) {
		return river.JobCancel(ErrInvalidOperation)
	}
	if err = w.handler.PrepareHostingAutoDeploy(ctx, job.Args.ResourceID); err != nil {
		if errors.Is(err, ErrInvalidOperation) {
			return river.JobCancel(ErrInvalidOperation)
		}
		if ctx.Err() != nil {
			return errors.New("hosting_auto_deploy_cancelled")
		}
		return errors.New("hosting_auto_deploy_failed")
	}
	return nil
}
func (*autoDeployWorker) NextRetry(job *river.Job[AutoDeployArgs]) time.Time {
	return time.Now().Add(time.Duration(job.Attempt) * time.Second)
}

// AutoDeployEnabled distinguishes a supplied real controller from build-only mode.
func (c *Client) AutoDeployEnabled() bool { return c != nil && c.hasAutoDeploy }

// EnqueueAutoDeployTx inserts only a bounded opaque wakeup in the caller transaction.
func (c *Client) EnqueueAutoDeployTx(ctx context.Context, tx pgx.Tx, args AutoDeployArgs) (int64, error) {
	if !c.AutoDeployEnabled() {
		return 0, ErrUnavailable
	}
	if tx == nil || !operationIDPattern.MatchString(args.ResourceID) {
		return 0, ErrInvalidOperation
	}
	return c.enqueueTx(ctx, tx, args)
}

package hostingjobs

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// ApplicationDatabaseRouteKind wakes durable connection state, never runtime effects.
const ApplicationDatabaseRouteKind = "hosting.application_database_route.v1"

// ErrApplicationDatabaseRouteWaiting asks River to wait without exhausting child retries.
var ErrApplicationDatabaseRouteWaiting = ErrDatabaseConnectionWaiting

// ApplicationDatabaseRouteArgs contains only an immutable controller workflow identifier.
type ApplicationDatabaseRouteArgs struct {
	OperationID string `json:"operation_id"`
}

// Kind returns the bounded continuation job family.
func (ApplicationDatabaseRouteArgs) Kind() string { return ApplicationDatabaseRouteKind }

// InsertOpts reuses hosting queue limits and active-state deduplication.
func (ApplicationDatabaseRouteArgs) InsertOpts() river.InsertOpts {
	return (PrepareDeploymentArgs{}).InsertOpts()
}

// ApplicationDatabaseRouteHandler advances persisted children without Docker or secrets.
type ApplicationDatabaseRouteHandler interface {
	PrepareApplicationDatabaseRoute(context.Context, string) error
}

type applicationDatabaseRouteWorker struct {
	river.WorkerDefaults[ApplicationDatabaseRouteArgs]
	handler ApplicationDatabaseRouteHandler
}

func (w *applicationDatabaseRouteWorker) Work(ctx context.Context, job *river.Job[ApplicationDatabaseRouteArgs]) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("hosting_application_database_route_panicked")
		}
	}()
	if !operationIDPattern.MatchString(job.Args.OperationID) {
		return river.JobCancel(ErrInvalidOperation)
	}
	err = w.handler.PrepareApplicationDatabaseRoute(ctx, job.Args.OperationID)
	if errors.Is(err, ErrApplicationDatabaseRouteWaiting) {
		return river.JobSnooze(time.Second)
	}
	if errors.Is(err, ErrInvalidOperation) {
		return river.JobCancel(ErrInvalidOperation)
	}
	if err != nil {
		if ctx.Err() != nil {
			return errors.New("hosting_application_database_route_cancelled")
		}
		return errors.New("hosting_application_database_route_failed")
	}
	return nil
}

// ApplicationDatabaseRouteEnabled requires a supplied real continuation handler.
func (c *Client) ApplicationDatabaseRouteEnabled() bool {
	return c != nil && c.hasApplicationDatabaseRoute
}

// EnqueueApplicationDatabaseRouteTx commits intent in the owning application transaction.
func (c *Client) EnqueueApplicationDatabaseRouteTx(ctx context.Context, tx pgx.Tx, args ApplicationDatabaseRouteArgs) (int64, error) {
	if !c.ApplicationDatabaseRouteEnabled() {
		return 0, ErrUnavailable
	}
	if tx == nil || !operationIDPattern.MatchString(args.OperationID) {
		return 0, ErrInvalidOperation
	}
	return c.enqueueTx(ctx, tx, args)
}

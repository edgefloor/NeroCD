// Package hostingjobs provides transactional River infrastructure for a supplied
// hosting preparation handler. It registers no runtime consumer by itself.
package hostingjobs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
)

const (
	// Queue is the bounded hosting control queue, separate from runner execution.
	Queue = "hosting_control"
	// PrepareDeploymentKind is the versioned preparation job contract.
	PrepareDeploymentKind = "hosting.prepare_deployment.v1"
	maxAttempts           = 3
	cancelTimeout         = 5 * time.Second
)

var (
	// ErrInvalidConfiguration reports an invalid dependency or bounded configuration.
	ErrInvalidConfiguration = errors.New("hosting_jobs_configuration_invalid")
	// ErrInvalidOperation reports an invalid opaque operation identifier.
	ErrInvalidOperation = errors.New("hosting_operation_invalid")
	// ErrUnavailable reports a safe database/client failure without upstream details.
	ErrUnavailable = errors.New("hosting_jobs_unavailable")
	// ErrStopTimeout reports that a handler did not finish before bounded shutdown returned.
	ErrStopTimeout = errors.New("hosting_jobs_stop_timeout")
)
var operationIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

// PrepareDeploymentArgs contains an opaque operation ID, never a deployment payload or credentials.
type PrepareDeploymentArgs struct {
	OperationID string `json:"operation_id"`
}

// Kind returns the frozen versioned job kind.
func (PrepareDeploymentArgs) Kind() string { return PrepareDeploymentKind }

// InsertOpts bounds retries and deduplicates only while the same args are queued or running.
// A later insert after completion is allowed. Business idempotency belongs to the operation handler.
func (PrepareDeploymentArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: Queue, MaxAttempts: maxAttempts, UniqueOpts: river.UniqueOpts{ByArgs: true, ByQueue: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled}}}
}

// Handler resolves the operation to immutable application state and performs real preparation.
// Implementations must honor context cancellation and own business idempotency. They must never execute Docker here.
type Handler interface {
	PrepareDeployment(context.Context, string) error
}

// HandlerFunc adapts a real preparation function to Handler.
type HandlerFunc func(context.Context, string) error

// PrepareDeployment calls the supplied handler function.
func (f HandlerFunc) PrepareDeployment(ctx context.Context, id string) error {
	if f == nil {
		return ErrInvalidConfiguration
	}
	return f(ctx, id)
}

// Config bounds worker concurrency and lifecycle. Schema must match the application's current schema.
type Config struct {
	Schema       string
	MaxWorkers   int
	JobTimeout   time.Duration
	DrainTimeout time.Duration
}

// Client owns a private River client. Construction neither migrates nor starts it.
type Client struct {
	hasServerBootstrap     bool
	hasProxy               bool
	hasBuild               bool
	hasGitHubPush          bool
	hasAutoDeploy          bool
	river                  *river.Client[pgx.Tx]
	pool                   *pgxpool.Pool
	schema, host, database string
	port                   uint16
	drainTimeout           time.Duration
}

// New requires a supplied handler and a live application pool. There is no default no-op worker.
func New(ctx context.Context, pool *pgxpool.Pool, handler Handler, cfg Config) (*Client, error) {
	if pool == nil || handler == nil {
		return nil, ErrInvalidConfiguration
	}
	value := reflect.ValueOf(handler)
	if (value.Kind() == reflect.Pointer || value.Kind() == reflect.Func) && value.IsNil() {
		return nil, ErrInvalidConfiguration
	}
	if cfg.MaxWorkers == 0 {
		cfg.MaxWorkers = 1
	}
	if cfg.JobTimeout == 0 {
		cfg.JobTimeout = time.Minute
	}
	if cfg.DrainTimeout == 0 {
		cfg.DrainTimeout = 10 * time.Second
	}
	if cfg.MaxWorkers < 1 || cfg.MaxWorkers > 8 || cfg.JobTimeout < time.Millisecond || cfg.JobTimeout > 5*time.Minute || cfg.DrainTimeout < time.Millisecond || cfg.DrainTimeout > time.Minute {
		return nil, ErrInvalidConfiguration
	}
	var schema string
	if err := pool.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		return nil, ErrUnavailable
	}
	if cfg.Schema != "" && cfg.Schema != schema {
		return nil, ErrInvalidConfiguration
	}
	cfg.Schema = schema
	workers := river.NewWorkers()
	river.AddWorker(workers, &prepareWorker{handler: handler})
	proxyHandler, hasProxy := handler.(ProxyHandler)
	if hasProxy {
		river.AddWorker(workers, &prepareProxyWorker{handler: proxyHandler})
	}
	buildHandler, hasBuild := handler.(BuildHandler)
	if hasBuild {
		river.AddWorker(workers, &prepareBuildWorker{handler: buildHandler})
	}
	hasGitHubPush := false
	bootstrapHandler, hasServerBootstrap := handler.(ServerBootstrapHandler)
	if hasServerBootstrap {
		river.AddWorker(workers, &serverBootstrapWorker{handler: bootstrapHandler})
	}
	if pushHandler, ok := handler.(GitHubPushHandler); ok {
		river.AddWorker(workers, &githubPushWorker{handler: pushHandler})
		hasGitHubPush = true
	}
	hasAutoDeploy := false
	if automatic, ok := handler.(AutoDeployHandler); ok {
		river.AddWorker(workers, &autoDeployWorker{handler: automatic})
		hasAutoDeploy = true
	}
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{Schema: schema, Workers: workers, Queues: map[string]river.QueueConfig{Queue: {MaxWorkers: cfg.MaxWorkers}}, JobTimeout: cfg.JobTimeout, SoftStopTimeout: cfg.DrainTimeout, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), FetchCooldown: 10 * time.Millisecond, FetchPollInterval: 100 * time.Millisecond})
	if err != nil {
		return nil, ErrInvalidConfiguration
	}
	connection := pool.Config().ConnConfig
	return &Client{river: client, pool: pool, schema: schema, host: connection.Host, port: connection.Port, database: connection.Database, drainTimeout: cfg.DrainTimeout, hasProxy: hasProxy, hasBuild: hasBuild, hasGitHubPush: hasGitHubPush, hasAutoDeploy: hasAutoDeploy, hasServerBootstrap: hasServerBootstrap}, nil
}

// EnqueueTx inserts in the caller's application transaction. Commit/rollback remain the caller's responsibility.
// The transaction must use the same endpoint, database, and current schema as the supplied pool.
func (c *Client) EnqueueTx(ctx context.Context, tx pgx.Tx, args PrepareDeploymentArgs) (int64, error) {
	if tx == nil || !operationIDPattern.MatchString(args.OperationID) {
		return 0, ErrInvalidOperation
	}
	return c.enqueueTx(ctx, tx, args)
}

func (c *Client) enqueueTx(ctx context.Context, tx pgx.Tx, args river.JobArgs) (int64, error) {
	connection := tx.Conn().Config()
	if connection.Host != c.host || connection.Port != c.port || connection.Database != c.database {
		return 0, ErrInvalidConfiguration
	}
	var schema string
	if err := tx.QueryRow(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		return 0, ErrUnavailable
	}
	if schema != c.schema {
		return 0, ErrInvalidConfiguration
	}
	result, err := c.river.InsertTx(ctx, tx, args, nil)
	if err != nil {
		return 0, ErrUnavailable
	}
	return result.Job.ID, nil
}

// Start validates the exact migrated bundle before starting the supplied worker.
func (c *Client) Start(ctx context.Context) error {
	if err := Validate(ctx, c.pool); err != nil {
		return err
	}
	if err := c.river.Start(ctx); err != nil {
		return ErrUnavailable
	}
	return nil
}

// Stop stops fetching, drains, then cancels work after DrainTimeout. Noncooperative handlers cause a bounded error.
func (c *Client) Stop(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.drainTimeout+cancelTimeout)
	defer cancel()
	if err := c.river.Stop(ctx); err != nil {
		return ErrStopTimeout
	}
	return nil
}

// Cancel cancels running handler contexts immediately and waits at most five seconds.
func (c *Client) Cancel(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, cancelTimeout)
	defer cancel()
	if err := c.river.StopAndCancel(ctx); err != nil {
		return ErrStopTimeout
	}
	return nil
}

// Stopped closes only when all worker services have stopped, including after a timed-out Stop.
func (c *Client) Stopped() <-chan struct{} { return c.river.Stopped() }

type prepareWorker struct {
	river.WorkerDefaults[PrepareDeploymentArgs]
	handler Handler
}

func (w *prepareWorker) Work(ctx context.Context, job *river.Job[PrepareDeploymentArgs]) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("hosting_prepare_panicked")
		}
	}()
	if !operationIDPattern.MatchString(job.Args.OperationID) {
		return river.JobCancel(ErrInvalidOperation)
	}
	if err = w.handler.PrepareDeployment(ctx, job.Args.OperationID); err != nil {
		if errors.Is(err, ErrInvalidOperation) {
			return river.JobCancel(ErrInvalidOperation)
		}
		if ctx.Err() != nil {
			return errors.New("hosting_prepare_cancelled")
		}
		return errors.New("hosting_prepare_failed")
	}
	return nil
}
func (*prepareWorker) NextRetry(job *river.Job[PrepareDeploymentArgs]) time.Time {
	return time.Now().Add(time.Duration(job.Attempt) * time.Second)
}

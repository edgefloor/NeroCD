package hostingjobs

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// GitHubPushKind is the versioned IntentID-only hosting dispatch contract.
const GitHubPushKind = "hosting.github_push.v1"

// GitHubPushArgs contains only an opaque intent ID, never source, credentials,
// plans, host configuration, or caller-selected execution authority.
type GitHubPushArgs struct {
	IntentID string `json:"intent_id"`
}

// Kind returns the versioned GitHub push preparation kind.
func (GitHubPushArgs) Kind() string { return GitHubPushKind }

// InsertOpts reuses the bounded hosting queue's retry and active-state deduplication policy.
func (GitHubPushArgs) InsertOpts() river.InsertOpts { return (PrepareDeploymentArgs{}).InsertOpts() }

// GitHubPushHandler resolves persisted push intent and verifies only fixed provider state.
// It must honor cancellation and own durable business idempotency. It must not
// execute Docker or Git; bounded provider HTTPS reads are permitted. Optional support requires the main Handler to implement it.
type GitHubPushHandler interface {
	PrepareGitHubPush(context.Context, string) error
}

type githubPushWorker struct {
	river.WorkerDefaults[GitHubPushArgs]
	handler GitHubPushHandler
}

func (w *githubPushWorker) Work(ctx context.Context, job *river.Job[GitHubPushArgs]) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("hosting_github_push_panicked")
		}
	}()
	if !operationIDPattern.MatchString(job.Args.IntentID) {
		return river.JobCancel(ErrInvalidOperation)
	}
	if err = w.handler.PrepareGitHubPush(ctx, job.Args.IntentID); err != nil {
		if errors.Is(err, ErrInvalidOperation) {
			return river.JobCancel(ErrInvalidOperation)
		}
		if ctx.Err() != nil {
			return errors.New("hosting_github_push_cancelled")
		}
		return errors.New("hosting_github_push_failed")
	}
	return nil
}
func (*githubPushWorker) NextRetry(job *river.Job[GitHubPushArgs]) time.Time {
	return time.Now().Add(time.Duration(job.Attempt) * time.Second)
}

// EnqueueGitHubPushTx inserts push verification dispatch in the caller's application transaction.
// A client without a supplied GitHubPushHandler refuses admission without inserting.
// Endpoint, database, and schema must match the client; commit remains the caller's responsibility.
func (c *Client) EnqueueGitHubPushTx(ctx context.Context, tx pgx.Tx, args GitHubPushArgs) (int64, error) {
	if !c.hasGitHubPush {
		return 0, ErrUnavailable
	}
	if tx == nil || !operationIDPattern.MatchString(args.IntentID) {
		return 0, ErrInvalidOperation
	}
	return c.enqueueTx(ctx, tx, args)
}

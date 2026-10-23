package store

import (
	"context"
	"errors"
	"nerocd/internal/domain"
	"nerocd/internal/githubpush"
	"time"
)

// ErrGitHubPushUnavailable reports a disabled executable controller without secret diagnostics.
var ErrGitHubPushUnavailable = errors.New("github_push_unavailable")

// ErrGitHubPushRunnerUnsupported requires authorized persisted runner capability setup.
var ErrGitHubPushRunnerUnsupported = errors.New("github_push_runner_unsupported")

// ErrGitHubPushCapacity rejects new demand without discarding pending receipts.
var ErrGitHubPushCapacity = errors.New("github_push_capacity")

// GitHubPushAuthorization is private current-authority evidence, never a public credential.
type GitHubPushAuthorization struct {
	Policy        domain.GitHubPushPolicy        `json:"-"`
	Configuration domain.GitHubPushConfiguration `json:"-"`
	Source        domain.HostingGitSource        `json:"-"`
	Token         string                         `json:"-"`
	Until         time.Time                      `json:"-"`
	IntentID      string                         `json:"-"`
	ActorID       string                         `json:"-"`
	RequestID     string                         `json:"-"`
	Replay        *domain.GitHubPushPolicy       `json:"-"`
}

// GitHubPushRepository keeps verified desired advance and existing build admission atomic.
type GitHubPushRepository interface {
	GetGitHubPushPolicy(context.Context, string, string) (domain.GitHubPushPolicy, error)
	ConfigureGitHubPushPolicy(context.Context, string, string, domain.GitHubPushConfigure, domain.AuditEvent) (domain.GitHubPushPolicy, error)
	BeginGitHubPushVerification(context.Context, string, string, domain.GitHubPushCommand) (GitHubPushAuthorization, error)
	FinishGitHubPushVerification(context.Context, GitHubPushAuthorization, githubpush.Evidence, string) (domain.GitHubPushPolicy, error)
	SetGitHubPushEnabled(context.Context, string, string, domain.GitHubPushEnable, domain.AuditEvent) (domain.GitHubPushPolicy, error)
	GitHubPushWebhookAuthority(context.Context, string) (GitHubPushAuthorization, error)
	AuthorizeGitHubPushSecret(context.Context, GitHubPushAuthorization, string) (ManagedSecretRecord, error)
	AcceptGitHubPushDelivery(context.Context, GitHubPushAuthorization, string, string, string, bool) (domain.GitHubPushReceipt, error)
	ListGitHubPushDeliveries(context.Context, string, string, string, int) (domain.GitHubPushDeliveryPage, error)
	RetryGitHubPushDelivery(context.Context, string, string, domain.GitHubPushRetry, domain.AuditEvent) (domain.GitHubPushDelivery, error)
	BeginGitHubPushDelivery(context.Context, string) (GitHubPushAuthorization, error)
	CommitGitHubPushDelivery(context.Context, GitHubPushAuthorization, githubpush.Evidence, string) error
}

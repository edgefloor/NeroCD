package domain

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Bounds keep ingress, provider proof, pending work and receipt retention finite.
const (
	GitHubPushPayloadLimit         = 1 << 20
	GitHubPushControlLimit         = 8 << 10
	GitHubPushPendingResourceLimit = 16
	GitHubPushPendingProjectLimit  = 64
	GitHubPushReceiptResourceLimit = 256
	GitHubPushReceiptProjectLimit  = 1024
	GitHubPushMaxAttempts          = 32
	GitHubPushComparisonLimit      = 100
)

var githubPushUUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
var githubPushSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
var githubPushName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

// GitHubPushSecretRef selects an immutable managed-secret version, never plaintext.
type GitHubPushSecretRef struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

// GitHubPushConfiguration records explicit independent secret purposes.
type GitHubPushConfiguration struct {
	AutomationActorID  string               `json:"automation_actor_id"`
	WebhookSecret      GitHubPushSecretRef  `json:"webhook_secret"`
	ProviderReadSecret *GitHubPushSecretRef `json:"provider_read_secret,omitempty"`
	SourceGrantID      string               `json:"source_grant_id,omitempty"`
}

// GitHubPushConfigure freezes current source intent and always saves disabled.
type GitHubPushConfigure struct {
	ExpectedPolicyGeneration   int64                   `json:"expected_policy_generation"`
	ExpectedResourceGeneration int64                   `json:"expected_resource_generation"`
	ExpectedSourceGeneration   int64                   `json:"expected_source_generation"`
	RequestID                  string                  `json:"request_id"`
	Configuration              GitHubPushConfiguration `json:"configuration"`
	ResetBranchBaseline        bool                    `json:"reset_branch_baseline"`
}

// GitHubPushCommand is a stable owner command against the current policy generation.
type GitHubPushCommand struct {
	ExpectedPolicyGeneration int64  `json:"expected_policy_generation"`
	RequestID                string `json:"request_id"`
}

// GitHubPushEnable changes only enabled state after current verified authorization.
type GitHubPushEnable struct {
	ExpectedPolicyGeneration int64  `json:"expected_policy_generation"`
	RequestID                string `json:"request_id"`
	Enabled                  bool   `json:"enabled"`
}

// GitHubPushRetry reopens one bounded delivery window without resetting build deduplication.
type GitHubPushRetry struct {
	ExpectedPolicyGeneration int64  `json:"expected_policy_generation"`
	RequestID                string `json:"request_id"`
	DeliveryID               string `json:"delivery_id"`
}

// GitHubPushPolicy is viewer-safe metadata; only current owners receive configuration selectors.
type GitHubPushPolicy struct {
	CanManage              bool                     `json:"can_manage"`
	ID                     string                   `json:"id"`
	ResourceID             string                   `json:"resource_id"`
	ProjectID              string                   `json:"project_id"`
	Generation             int64                    `json:"generation"`
	ResourceGeneration     int64                    `json:"resource_generation"`
	SourceGeneration       int64                    `json:"source_generation"`
	Enabled                bool                     `json:"enabled"`
	Status                 string                   `json:"status"`
	Reason                 string                   `json:"reason"`
	RepositoryID           int64                    `json:"repository_id"`
	Repository             string                   `json:"repository"`
	Branch                 string                   `json:"branch"`
	VerifiedAt             *time.Time               `json:"verified_at,omitempty"`
	DesiredCommit          string                   `json:"desired_commit"`
	LastBuildID            string                   `json:"last_build_id"`
	CallbackURL            string                   `json:"callback_url"`
	ProviderReadConfigured bool                     `json:"provider_read_configured"`
	OwnerConfiguration     *GitHubPushConfiguration `json:"owner_configuration,omitempty"`
}

// GitHubPushDelivery retains closed metadata, not signatures, payloads or credentials.
type GitHubPushDelivery struct {
	ID               string     `json:"id"`
	PolicyID         string     `json:"policy_id"`
	PolicyGeneration int64      `json:"policy_generation"`
	State            string     `json:"state"`
	Reason           string     `json:"reason"`
	AfterCommit      string     `json:"after_commit"`
	VerifiedCommit   string     `json:"verified_commit"`
	BuildID          string     `json:"build_id"`
	Attempts         int        `json:"attempts"`
	ReceivedAt       time.Time  `json:"received_at"`
	NextRetryAt      *time.Time `json:"next_retry_at,omitempty"`
	ExpiresAt        time.Time  `json:"expires_at"`
}

// GitHubPushReceipt is stable delivery admission metadata.
type GitHubPushReceipt struct {
	DeliveryID string `json:"delivery_id"`
	State      string `json:"state"`
}

// ValidGitHubPushRequestID accepts canonical UUIDs used for stable request identity.
func ValidGitHubPushRequestID(id string) bool { return githubPushUUID.MatchString(id) }

// ValidGitHubPushCommit accepts a full immutable GitHub SHA-1 commit identifier.
func ValidGitHubPushCommit(sha string) bool {
	return githubPushSHA.MatchString(sha) && sha != strings.Repeat("0", 40)
}

// GitHubPushRepositoryBranch derives the only allowed provider target from saved source intent.
func GitHubPushRepositoryBranch(source HostingGitSource) (string, string, string, error) {
	u, err := url.Parse(source.RepositoryURL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || (source.CredentialKind != "public" && source.CredentialKind != "https_basic") {
		return "", "", "", errors.New("github_push_source_unsupported")
	}
	path := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	parts := strings.Split(path, "/")
	branch := strings.TrimPrefix(source.RequestedRef, "refs/heads/")
	if len(parts) != 2 || !githubPushName.MatchString(parts[0]) || !githubPushName.MatchString(parts[1]) || branch == "" || len(branch) > 240 || strings.HasPrefix(branch, "refs/") || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.Contains(branch, "//") || strings.ContainsAny(branch, " ~^:?*[\\\x00\r\n\t") || ValidGitHubPushCommit(branch) {
		return "", "", "", errors.New("github_push_source_unsupported")
	}
	for _, part := range strings.Split(branch, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return "", "", "", errors.New("github_push_source_unsupported")
		}
	}
	return parts[0], parts[1], branch, nil
}

// Validate checks bounded explicit selectors without conferring authority.
func (in GitHubPushConfigure) Validate() error {
	// A webhook key must never also select provider authentication material,
	// even through a different version of the same managed secret.
	if provider := in.Configuration.ProviderReadSecret; provider != nil && provider.ID == in.Configuration.WebhookSecret.ID {
		return errors.New("invalid_github_push_configuration")
	}
	validSecret := func(r GitHubPushSecretRef) bool { return observationID(r.ID, false) && r.Version > 0 }
	if !in.ResetBranchBaseline || in.ExpectedPolicyGeneration < 0 || in.ExpectedResourceGeneration < 1 || in.ExpectedSourceGeneration < 1 || !ValidGitHubPushRequestID(in.RequestID) || !observationID(in.Configuration.AutomationActorID, false) || !validSecret(in.Configuration.WebhookSecret) || (in.Configuration.ProviderReadSecret != nil && !validSecret(*in.Configuration.ProviderReadSecret)) || !observationID(in.Configuration.SourceGrantID, true) {
		return errors.New("invalid_github_push_configuration")
	}
	return nil
}

// GitHubPushDeliveryPage is a bounded viewer history page.
type GitHubPushDeliveryPage struct {
	Items      []GitHubPushDelivery `json:"items"`
	NextCursor string               `json:"next_cursor"`
	HasMore    bool                 `json:"has_more"`
}

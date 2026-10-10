package runnerclient

import (
	"context"
	"fmt"
	"net/url"

	"nerocd/internal/domain"
)

// Authority carries the full lease tuple required for fenced operations.
type Authority struct {
	LeaseID string
	Attempt int
	Fence   string
}

// CompleteRequest supplies a fenced, idempotent run completion.
type CompleteRequest struct {
	LeaseID       string `json:"lease_id"`
	Status        string `json:"status"`
	Attempt       int    `json:"attempt"`
	Fence         string `json:"fence"`
	CompletionKey string `json:"completion_key"`
}

// EventRequest is one idempotent runner log event.
type EventRequest struct {
	EventKey string `json:"event_key"`
	Sequence int    `json:"sequence"`
	Stream   string `json:"stream"`
	Message  string `json:"message"`
}

// EventBatchRequest appends events under one lease authority tuple.
type EventBatchRequest struct {
	RunID   string         `json:"run_id"`
	LeaseID string         `json:"lease_id"`
	Attempt int            `json:"attempt"`
	Fence   string         `json:"fence"`
	Events  []EventRequest `json:"events"`
}

// EventBatchAck contains the persisted event records.
type EventBatchAck struct {
	Events []domain.RunLog `json:"events"`
}

// ArtifactRequest records one declared output under lease authority.
type ArtifactRequest struct {
	RunID    string `json:"run_id"`
	LeaseID  string `json:"lease_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Found    bool   `json:"found"`
	Required bool   `json:"required"`
	Size     int64  `json:"size"`
	Kind     string `json:"kind"`
	Attempt  int    `json:"attempt"`
	Fence    string `json:"fence"`
}

// OperationalTelemetry contains bounded aggregate runtime counters.
type OperationalTelemetry struct {
	JournalDepth  int `json:"journal_depth"`
	RetryCount    int `json:"retry_count"`
	RenewFailures int `json:"renew_failures"`
}

// SecretAccessRequest authorizes one logical secret binding for an attempt.
type SecretAccessRequest struct {
	AccessID string `json:"access_id"`
	RunID    string `json:"run_id"`
	LeaseID  string `json:"lease_id"`
	Attempt  int    `json:"attempt"`
	Fence    string `json:"fence"`
	Binding  string `json:"binding"`
	Provider string `json:"provider"`
	Version  string `json:"version"`
}

// DeploymentAuthority carries the deployment and lease identity tuple.
type DeploymentAuthority struct {
	DeploymentID string
	RunID        string
	LeaseID      string
	Attempt      int
	Fence        string
}

// ProvenanceRequest records resolved deployment content identity.
type ProvenanceRequest struct {
	DeploymentID    string   `json:"deployment_id"`
	RunID           string   `json:"run_id"`
	LeaseID         string   `json:"lease_id"`
	Attempt         int      `json:"attempt"`
	Fence           string   `json:"fence"`
	ResolutionID    string   `json:"resolution_id"`
	GitCommit       string   `json:"git_commit"`
	ComposeHash     string   `json:"compose_hash"`
	ImageDigests    []string `json:"image_digests"`
	ContentIdentity string   `json:"content_identity"`
}

// DeploymentTransitionRequest performs a fenced deployment state transition.
type DeploymentTransitionRequest struct {
	DeploymentID   string                  `json:"deployment_id"`
	RunID          string                  `json:"run_id"`
	LeaseID        string                  `json:"lease_id"`
	Attempt        int                     `json:"attempt"`
	Fence          string                  `json:"fence"`
	TransitionKey  string                  `json:"transition_key"`
	ExpectedStatus domain.DeploymentStatus `json:"expected_status"`
	TargetStatus   domain.DeploymentStatus `json:"target_status"`
	HealthPassed   *bool                   `json:"health_passed"`
	FailureCode    string                  `json:"failure_code"`
	Metadata       map[string]any          `json:"metadata"`
}

// DeploymentFailureRollbackRequest records failure and initiates fenced rollback.
type DeploymentFailureRollbackRequest struct {
	DeploymentID          string                  `json:"deployment_id"`
	RunID                 string                  `json:"run_id"`
	LeaseID               string                  `json:"lease_id"`
	Attempt               int                     `json:"attempt"`
	Fence                 string                  `json:"fence"`
	RequestID             string                  `json:"request_id"`
	ExpectedStatus        domain.DeploymentStatus `json:"expected_status"`
	CancellationRequestID string                  `json:"cancellation_request_id,omitempty"`
	FailureCode           string                  `json:"failure_code"`
	Metadata              map[string]any          `json:"metadata"`
}

// Heartbeat reports runner liveness and returns the current runner record.
func (c Client) Heartbeat(ctx context.Context) (domain.Runner, error) {
	var out domain.Runner
	err := c.post(ctx, "/api/v1/runners/heartbeat", struct{}{}, &out)
	return out, err
}

// ReportTelemetry publishes aggregate runtime health counters.
func (c Client) ReportTelemetry(ctx context.Context, input OperationalTelemetry) error {
	return c.post(ctx, "/api/v1/runners/telemetry", input, nil)
}

// Claim requests the next matching run and its lease authority.
func (c Client) Claim(ctx context.Context) (domain.ClaimedRun, error) {
	var out domain.ClaimedRun
	err := c.post(ctx, "/api/v1/runners/claim", struct{}{}, &out)
	return out, err
}

// Renew extends a lease using its full authority tuple.
func (c Client) Renew(ctx context.Context, authority Authority) (domain.RunLease, error) {
	var out domain.RunLease
	err := c.post(ctx, "/api/v1/runners/renew", struct {
		LeaseID string `json:"lease_id"`
		Attempt int    `json:"attempt"`
		Fence   string `json:"fence"`
	}{authority.LeaseID, authority.Attempt, authority.Fence}, &out)
	return out, err
}

// Lease reads current lease state using its full authority tuple.
func (c Client) Lease(ctx context.Context, authority Authority) (domain.RunLease, error) {
	var out domain.RunLease
	query := url.Values{"lease_id": {authority.LeaseID}, "attempt": {fmt.Sprint(authority.Attempt)}, "fence": {authority.Fence}}
	err := c.get(ctx, "/api/v1/runners/lease?"+query.Encode(), &out)
	return out, err
}

// Complete commits an idempotent terminal run status.
func (c Client) Complete(ctx context.Context, input CompleteRequest) (domain.RunLease, error) {
	var out domain.RunLease
	err := c.post(ctx, "/api/v1/runners/complete", input, &out)
	return out, err
}

// AppendEvents persists an idempotent batch of run output.
func (c Client) AppendEvents(ctx context.Context, input EventBatchRequest) (EventBatchAck, error) {
	var out EventBatchAck
	err := c.post(ctx, "/api/v1/runners/events/batch", input, &out)
	return out, err
}

// AppendArtifact records one declared attempt artifact.
func (c Client) AppendArtifact(ctx context.Context, input ArtifactRequest) (domain.ArtifactRecord, error) {
	var out domain.ArtifactRecord
	err := c.post(ctx, "/api/v1/runners/artifacts", input, &out)
	return out, err
}

// AuthorizeSecret authorizes one secret binding for a fenced attempt.
func (c Client) AuthorizeSecret(ctx context.Context, input SecretAccessRequest) (domain.SecretAccessGrant, error) {
	var out domain.SecretAccessGrant
	err := c.post(ctx, "/api/v1/runners/secrets/access", input, &out)
	return out, err
}
func deploymentQuery(input DeploymentAuthority) string {
	return url.Values{"deployment_id": {input.DeploymentID}, "run_id": {input.RunID}, "lease_id": {input.LeaseID}, "attempt": {fmt.Sprint(input.Attempt)}, "fence": {input.Fence}}.Encode()
}

// DeploymentPlan retrieves the immutable plan for a fenced deployment.
func (c Client) DeploymentPlan(ctx context.Context, input DeploymentAuthority) (domain.DeploymentPlan, error) {
	var out domain.DeploymentPlan
	err := c.get(ctx, "/api/v1/runners/deployments/plan?"+deploymentQuery(input), &out)
	return out, err
}

// DeploymentStatus retrieves current fenced deployment status.
func (c Client) DeploymentStatus(ctx context.Context, input DeploymentAuthority) (domain.DeploymentPlan, error) {
	var out domain.DeploymentPlan
	err := c.get(ctx, "/api/v1/runners/deployments/status?"+deploymentQuery(input), &out)
	return out, err
}

// ReportProvenance records the resolved revision and content identity.
func (c Client) ReportProvenance(ctx context.Context, input ProvenanceRequest) (domain.Revision, error) {
	var out domain.Revision
	err := c.post(ctx, "/api/v1/runners/deployments/provenance", input, &out)
	return out, err
}

// TransitionDeployment applies one fenced deployment state transition.
func (c Client) TransitionDeployment(ctx context.Context, input DeploymentTransitionRequest) (domain.Deployment, error) {
	var out domain.Deployment
	err := c.post(ctx, "/api/v1/runners/deployments/transition", input, &out)
	return out, err
}

// FailDeploymentAndRollback atomically records failure and requests rollback.
func (c Client) FailDeploymentAndRollback(ctx context.Context, input DeploymentFailureRollbackRequest) (domain.DeploymentFailureRollbackResult, error) {
	var out domain.DeploymentFailureRollbackResult
	err := c.post(ctx, "/api/v1/runners/deployments/fail-and-rollback", input, &out)
	return out, err
}

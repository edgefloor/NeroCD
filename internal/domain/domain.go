package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Project is a tenant-owned collection of automation resources.
type Project struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	CreatedAt   time.Time  `json:"created_at"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
}

// ProjectMember grants a user a role within a project.
type ProjectMember struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	UserID    string    `json:"user_id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ProjectRole describes a principal's effective project permissions.
type ProjectRole struct {
	ProjectID string `json:"project_id"`
	Role      string `json:"role"`
	CanView   bool   `json:"can_view"`
	CanRun    bool   `json:"can_run"`
	CanAdmin  bool   `json:"can_admin"`
}

// User is a locally managed control-plane identity.
type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	GlobalRole   string    `json:"global_role"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

// Session is a revocable browser or local-session credential record.
type Session struct {
	ID         string     `json:"id"`
	UserID     string     `json:"user_id"`
	ExpiresAt  time.Time  `json:"expires_at"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	SourceIP   string     `json:"source_ip,omitempty"`
	UserAgent  string     `json:"user_agent,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// APIToken is a hashed, role-scoped programmatic credential record.
type APIToken struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	TokenHash  string     `json:"-"`
	Roles      []string   `json:"roles"`
	Status     string     `json:"status"`
	CreatedBy  string     `json:"created_by"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
}

// TaskTemplate defines a reusable project automation request.
type TaskTemplate struct {
	ID          string   `json:"id"`
	ProjectID   string   `json:"project_id"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	RunSpec     RunSpec  `json:"run_spec"`
	Workflow    Workflow `json:"workflow"`
	RunnerTags  []string `json:"runner_tags"`
	RequiresAck bool     `json:"requires_ack"`
}

// RunSpec defines the runner primitives requested by a task.
type RunSpec struct {
	Type       string          `json:"type"`
	Inputs     map[string]any  `json:"inputs"`
	Repository *RepositoryRef  `json:"repository,omitempty"`
	Process    *ProcessSpec    `json:"process,omitempty"`
	Artifacts  []ArtifactSpec  `json:"artifacts,omitempty"`
	Secrets    []SecretBinding `json:"secrets,omitempty"`
	Workflow   *Workflow       `json:"workflow,omitempty"`
}

// RepositoryRef identifies source input for a task or deployment.
type RepositoryRef struct {
	ID       string `json:"id,omitempty"`
	URL      string `json:"url,omitempty"`
	Provider string `json:"provider,omitempty"`
	Ref      string `json:"ref,omitempty"`
	Path     string `json:"path,omitempty"`
}

// ProcessSpec describes a local process to execute.
type ProcessSpec struct {
	Command        []string          `json:"command"`
	WorkingDir     string            `json:"working_dir,omitempty"`
	Environment    map[string]string `json:"environment,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
}

// ArtifactSpec identifies an expected output relative to a workspace.
type ArtifactSpec struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Required  bool   `json:"required"`
	Retention string `json:"retention,omitempty"`
}

// SecretBinding maps an authorized secret reference to a runner target.
type SecretBinding struct {
	Name            string   `json:"name"`
	Provider        string   `json:"provider"`
	Reference       string   `json:"reference"`
	Target          string   `json:"target"`
	Required        bool     `json:"required"`
	Version         string   `json:"version,omitempty"`
	Fingerprint     string   `json:"fingerprint,omitempty"`
	RedactEncodings []string `json:"redact_encodings,omitempty"`
	Classification  string   `json:"classification,omitempty"`
}

// Workflow orders the steps that make up a multi-step task.
type Workflow struct {
	Steps []WorkflowStep `json:"steps"`
}

// WorkflowStep defines one dependency-aware task in a workflow.
type WorkflowStep struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	RunSpec     RunSpec  `json:"run_spec"`
	DependsOn   []string `json:"depends_on,omitempty"`
	RequiresAck bool     `json:"requires_ack"`
}

// WorkflowState records progress through a workflow.
type WorkflowState struct {
	CurrentStepID string              `json:"current_step_id,omitempty"`
	Steps         []WorkflowStepState `json:"steps"`
}

// WorkflowStepState records one workflow step's execution state.
type WorkflowStepState struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// TaskRun is a requested or executing instance of a task template.
type TaskRun struct {
	ID            string        `json:"id"`
	ProjectID     string        `json:"project_id"`
	TemplateID    *string       `json:"template_id,omitempty"`
	RunSpec       RunSpec       `json:"run_spec"`
	Workflow      Workflow      `json:"workflow"`
	WorkflowState WorkflowState `json:"workflow_state"`
	RunnerTags    []string      `json:"runner_tags"`
	Status        string        `json:"status"`
	RunnerID      *string       `json:"runner_id,omitempty"`
	RequestedBy   string        `json:"requested_by"`
	StartedAt     time.Time     `json:"started_at"`
	FinishedAt    *time.Time    `json:"finished_at,omitempty"`
}

// Runner is an enrolled worker that can execute tasks.
type Runner struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Tags            []string  `json:"tags"`
	Capabilities    []string  `json:"capabilities"`
	TokenHash       string    `json:"-"`
	Status          string    `json:"status"`
	RegisteredAt    time.Time `json:"registered_at"`
	LastHeartbeatAt time.Time `json:"last_heartbeat_at"`
}

// RunnerEnrollment contains only non-secret enrollment state. Hashes required
// by the persistence boundary are never serialized by API responses or audits.
type RunnerEnrollment struct {
	ID               string     `json:"id"`
	TokenHash        string     `json:"-"`
	RunnerID         string     `json:"runner_id"`
	RunnerName       string     `json:"runner_name"`
	Tags             []string   `json:"tags"`
	Capabilities     []string   `json:"capabilities"`
	CreatedBy        string     `json:"created_by"`
	CreatedAt        time.Time  `json:"created_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	UsedAt           *time.Time `json:"used_at,omitempty"`
	ConsumeRequestID *string    `json:"-"`
	CredentialHash   *string    `json:"-"`
}

// RunnerEnrollmentConsume carries hashes used to atomically consume enrollment.
type RunnerEnrollmentConsume struct {
	TokenHash      string
	RequestID      string
	CredentialHash string
}

// RunLease grants one runner a fenced, time-bounded attempt to execute a run.
type RunLease struct {
	ID          string     `json:"id"`
	RunID       string     `json:"run_id"`
	RunnerID    string     `json:"runner_id"`
	Status      string     `json:"status"`
	ExpiresAt   time.Time  `json:"expires_at"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	// Attempt is a monotonically increasing execution generation for a run.
	Attempt int `json:"attempt"`
	// Fence is an opaque, per-attempt capability. Every runner mutation carries it.
	Fence         string `json:"fence"`
	CompletionKey string `json:"-"`
}

// ClaimedRun combines a lease, run, and executable primitive plan.
type ClaimedRun struct {
	Lease         RunLease            `json:"lease"`
	Run           TaskRun             `json:"run"`
	PrimitivePlan RunnerPrimitivePlan `json:"primitive_plan"`
}

// RunLog is one ordered runner event retained for a task run.
type RunLog struct {
	ID                string    `json:"id"`
	RunID             string    `json:"run_id"`
	Sequence          int       `json:"sequence"`
	Stream            string    `json:"stream"`
	Message           string    `json:"message"`
	CreatedAt         time.Time `json:"created_at"`
	EventKey          string    `json:"event_key,omitempty"`
	LeaseID           string    `json:"lease_id,omitempty"`
	Attempt           int       `json:"attempt,omitempty"`
	RequestedSequence int       `json:"requested_sequence,omitempty"`
}

// ArtifactRecord records an artifact inspection result for a run.
type ArtifactRecord struct {
	ID        string    `json:"id"`
	RunID     string    `json:"run_id"`
	LeaseID   string    `json:"lease_id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Found     bool      `json:"found"`
	Required  bool      `json:"required"`
	Size      int64     `json:"size"`
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"created_at"`
}

// Repository is a project source repository and its admission policy.
type Repository struct {
	ID         string           `json:"id"`
	ProjectID  string           `json:"project_id"`
	Name       string           `json:"name"`
	URL        string           `json:"url"`
	Provider   string           `json:"provider"`
	DefaultRef string           `json:"default_ref"`
	Policy     RepositoryPolicy `json:"policy"`
	CreatedAt  time.Time        `json:"created_at"`
}

// AccessKey records a project-scoped external access-key reference.
type AccessKey struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
}

// Inventory is a named source of deployment target data.
type Inventory struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
}

// RunnerPrimitivePlan contains the runner primitives needed for a run.
type RunnerPrimitivePlan struct {
	RunID     string          `json:"run_id"`
	Checkout  *CheckoutPlan   `json:"checkout,omitempty"`
	Process   *ProcessSpec    `json:"process,omitempty"`
	Artifacts []ArtifactSpec  `json:"artifacts,omitempty"`
	Secrets   []SecretBinding `json:"secrets,omitempty"`
}

// CheckoutPlan identifies a repository checkout destination.
type CheckoutPlan struct {
	Repository RepositoryRef `json:"repository"`
	DestPath   string        `json:"dest_path"`
}

// Approval records the decision required to proceed with a run.
type Approval struct {
	ID          string     `json:"id"`
	RunID       string     `json:"run_id"`
	Status      string     `json:"status"`
	RequestedBy string     `json:"requested_by"`
	ApprovedBy  *string    `json:"approved_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ApprovedAt  *time.Time `json:"approved_at,omitempty"`
}

// AuditEvent records an actor's control-plane action.
type AuditEvent struct {
	ID        string         `json:"id"`
	ActorID   string         `json:"actor_id"`
	Action    string         `json:"action"`
	TargetID  string         `json:"target_id"`
	Metadata  map[string]any `json:"metadata"`
	CreatedAt time.Time      `json:"created_at"`
}

// Service is a durable deployment control-plane service record.
// Environment, Revision and Deployment are the durable deployment
// control plane. They intentionally carry metadata only: an execution adapter
// must obtain its own fenced attempt before mutating a runner.
type Service struct {
	ID           string    `json:"id"`
	ProjectID    string    `json:"project_id"`
	Name         string    `json:"name"`
	RepositoryID string    `json:"repository_id"`
	ComposePath  string    `json:"compose_path"`
	Profiles     []string  `json:"profiles"`
	OwnerID      string    `json:"owner_id"`
	CreatedAt    time.Time `json:"created_at"`
}

// HealthPolicy is server-owned deployment configuration. A runner converts it
// to a stricter one-request transport contract immediately before use.
type HealthPolicy struct {
	URL              string   `json:"url"`
	AllowedHosts     []string `json:"allowed_hosts"`
	AllowedCIDRs     []string `json:"allowed_cidrs,omitempty"`
	AllowedPorts     []int    `json:"allowed_ports,omitempty"`
	AllowHTTP        bool     `json:"allow_http,omitempty"`
	ExpectedRevision string   `json:"expected_revision,omitempty"`
	IntervalSeconds  float64  `json:"interval_seconds,omitempty"`
	TimeoutSeconds   float64  `json:"timeout_seconds,omitempty"`
	ExpectedStatus   int      `json:"expected_status,omitempty"`
}

// UnmarshalJSON rejects configuration keys the adapter does not understand.
// That makes the server, persisted plan, and runner agree on the same bounded
// policy surface rather than silently accepting an intended safeguard.
func (p *HealthPolicy) UnmarshalJSON(data []byte) error {
	type policy HealthPolicy
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded policy
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("invalid health policy")
	}
	*p = HealthPolicy(decoded)
	return nil
}

// Environment is a deployment target configuration for a service.
type Environment struct {
	ID                       string          `json:"id"`
	ServiceID                string          `json:"service_id"`
	Name                     string          `json:"name"`
	RunnerSelector           []string        `json:"runner_selector"`
	ComposeProject           string          `json:"compose_project"`
	HealthPolicy             HealthPolicy    `json:"health_policy"`
	ConfirmationRequired     bool            `json:"confirmation_required"`
	TimeoutSeconds           int             `json:"timeout_seconds"`
	SecretBindings           []SecretBinding `json:"secret_bindings"`
	RollbackSafe             bool            `json:"rollback_safe"`
	CurrentHealthyRevisionID *string         `json:"current_healthy_revision_id,omitempty"`
	CreatedAt                time.Time       `json:"created_at"`
}

// Revision records immutable deployment source evidence once resolved.
type Revision struct {
	ID              string    `json:"id"`
	ServiceID       string    `json:"service_id"`
	RequestedRef    string    `json:"requested_ref"`
	GitCommit       string    `json:"git_commit"`
	ComposeHash     string    `json:"compose_hash"`
	ImageDigests    []string  `json:"image_digests"`
	ContentIdentity string    `json:"content_identity"`
	CreatedBy       string    `json:"created_by"`
	CreatedAt       time.Time `json:"created_at"`
	// ProvenanceResolved is false until the runner has observed the requested
	// ref and normalized the compose input under its active deployment fence.
	// The evidence fields are intentionally omitted from ordinary JSON until
	// that point so a maintainer cannot claim immutable provenance on create.
	ProvenanceResolved bool       `json:"provenance_resolved"`
	ProvenanceState    string     `json:"provenance_state"`
	ResolvedAt         *time.Time `json:"resolved_at,omitempty"`
}

// DeploymentPlan is the deliberately small, runner-only authority needed to
// resolve provenance.  It contains opaque secret references, never values.
type DeploymentPlan struct {
	DeploymentID              string           `json:"deployment_id"`
	Status                    DeploymentStatus `json:"status"`
	RunID                     string           `json:"run_id"`
	LeaseID                   string           `json:"lease_id"`
	Attempt                   int              `json:"attempt"`
	Fence                     string           `json:"fence"`
	ProjectID                 string           `json:"project_id"`
	ServiceID                 string           `json:"service_id"`
	EnvironmentID             string           `json:"environment_id"`
	RepositoryID              string           `json:"repository_id"`
	RepositoryURL             string           `json:"repository_url"`
	RepositoryPolicy          RepositoryPolicy `json:"repository_policy"`
	RequestedRef              string           `json:"requested_ref"`
	ComposePath               string           `json:"compose_path"`
	Profiles                  []string         `json:"profiles"`
	ComposeProject            string           `json:"compose_project"`
	TimeoutSeconds            int              `json:"timeout_seconds"`
	HealthPolicy              HealthPolicy     `json:"health_policy"`
	SecretBindings            []SecretBinding  `json:"secret_bindings"`
	RollbackSafe              bool             `json:"rollback_safe"`
	PreviousHealthyRevisionID *string          `json:"previous_healthy_revision_id,omitempty"`
	RollbackOfID              *string          `json:"rollback_of_id,omitempty"`
	CancellationRequestID     *string          `json:"cancellation_request_id,omitempty"`
}

// RepositoryPolicy is runner-safe source admission configuration. Credential
// references are opaque IDs, never credential values.
type RepositoryPolicy struct {
	Version               int      `json:"version"`
	State                 string   `json:"state"`
	Mode                  string   `json:"mode,omitempty"`
	AllowedSchemes        []string `json:"allowed_schemes,omitempty"`
	AllowedHosts          []string `json:"allowed_hosts,omitempty"`
	AllowedCIDRs          []string `json:"allowed_cidrs,omitempty"`
	RedirectHosts         []string `json:"redirect_hosts,omitempty"`
	SSHHostFingerprints   []string `json:"ssh_host_fingerprints,omitempty"`
	CredentialReferenceID string   `json:"credential_reference_id,omitempty"`
	AllowInternal         bool     `json:"allow_internal"`
}

// Deployment coordinates applying a desired revision to an environment.
type Deployment struct {
	ID                        string           `json:"id"`
	EnvironmentID             string           `json:"environment_id"`
	DesiredRevisionID         string           `json:"desired_revision_id"`
	PreviousHealthyRevisionID *string          `json:"previous_healthy_revision_id,omitempty"`
	TaskRunID                 *string          `json:"task_run_id,omitempty"`
	IdempotencyKey            string           `json:"idempotency_key"`
	Status                    DeploymentStatus `json:"status"`
	RequestedBy               string           `json:"requested_by"`
	ConfirmedBy               *string          `json:"confirmed_by,omitempty"`
	CreatedAt                 time.Time        `json:"created_at"`
	UpdatedAt                 time.Time        `json:"updated_at"`
	FinishedAt                *time.Time       `json:"finished_at,omitempty"`
	HealthPassed              *bool            `json:"health_passed,omitempty"`
	RollbackOfID              *string          `json:"rollback_of_id,omitempty"`
	FailureCode               string           `json:"failure_code,omitempty"`
	FenceRequired             bool             `json:"fence_required"`
}

// DeploymentAttempt is the durable projection of a generic run lease into the
// deployment control plane. Fence is deliberately never exposed in normal
// deployment listings; runner endpoints receive it only in their lease.
type DeploymentAttempt struct {
	DeploymentID string     `json:"deployment_id"`
	RunID        string     `json:"run_id"`
	LeaseID      string     `json:"lease_id"`
	RunnerID     string     `json:"runner_id"`
	Attempt      int        `json:"attempt"`
	Fence        string     `json:"-"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

// DeploymentTransitionRequest carries a fenced runner status transition.
type DeploymentTransitionRequest struct {
	DeploymentID   string
	RunID          string
	LeaseID        string
	RunnerID       string
	Attempt        int
	Fence          string
	TransitionKey  string
	ExpectedStatus DeploymentStatus
	TargetStatus   DeploymentStatus
	HealthPassed   *bool
	FailureCode    string
	Metadata       map[string]any
}

// DeploymentFailureRollbackRequest is the one fenced operation permitted to
// turn a post-apply failure into a separately schedulable rollback.  The
// caller supplies stable IDs so a lost response can be replayed safely; the
// store owns the linkage, environment lock handoff, run and audit writes.
type DeploymentFailureRollbackRequest struct {
	DeploymentID          string
	RunID                 string
	LeaseID               string
	RunnerID              string
	Attempt               int
	Fence                 string
	RequestID             string
	ExpectedStatus        DeploymentStatus
	CancellationRequestID string
	FailureCode           string
	Metadata              map[string]any
}

// RollbackObjectIDs are derived solely from the fenced failure receipt. They
// are stable for a response-loss retry but cannot be selected by a runner.
func RollbackObjectIDs(deploymentID, requestID string) (string, string) {
	sum := sha256.Sum256([]byte(deploymentID + "\x00" + requestID))
	value := fmt.Sprintf("%x", sum[:16])
	return "dep_rollback_" + value, "run_rollback_" + value
}

// DeploymentFailureRollbackResult contains linked failed and rollback deployments.
type DeploymentFailureRollbackResult struct {
	Failed   Deployment `json:"failed"`
	Rollback Deployment `json:"rollback"`
}

// DeploymentCancelRequest is a maintainer-issued, idempotent control-plane
// receipt.  It is deliberately separate from generic run cancellation: a
// deployment owns an environment lock and may already have mutated a target.
// RequestID is stable across a lost HTTP response and is never runner-chosen.
type DeploymentCancelRequest struct {
	DeploymentID string
	RequestID    string
	ActorID      string
}

// SecretAccessRequest is the complete safe metadata used to authorize one
// runner-local secret read. Reference, target, value and raw fence are
// deliberately absent from the resulting audit record and response.
type SecretAccessRequest struct {
	AccessID    string
	RunnerID    string
	RunID       string
	LeaseID     string
	Attempt     int
	Fence       string
	Binding     string
	Provider    string
	Version     string
	RequestedAt time.Time
}

// SecretAccessGrant records authorization for a runner-local secret read.
type SecretAccessGrant struct {
	AccessID     string    `json:"access_id"`
	RunID        string    `json:"run_id"`
	LeaseID      string    `json:"lease_id"`
	Attempt      int       `json:"attempt"`
	Binding      string    `json:"binding"`
	Provider     string    `json:"provider"`
	Version      string    `json:"version,omitempty"`
	AuthorizedAt time.Time `json:"authorized_at"`
}

// Capability describes an advertised product or runner capability.
type Capability struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Description string `json:"description"`
}

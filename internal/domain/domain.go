package domain

import "time"

type Project struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	CreatedAt   time.Time  `json:"created_at"`
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
}

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

type ProjectRole struct {
	ProjectID string `json:"project_id"`
	Role      string `json:"role"`
	CanView   bool   `json:"can_view"`
	CanRun    bool   `json:"can_run"`
	CanAdmin  bool   `json:"can_admin"`
}

type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	Name         string    `json:"name"`
	Status       string    `json:"status"`
	GlobalRole   string    `json:"global_role"`
	PasswordHash string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
	RevokedAt time.Time `json:"-"`
}

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

type RunSpec struct {
	Type       string          `json:"type"`
	Inputs     map[string]any  `json:"inputs"`
	Repository *RepositoryRef  `json:"repository,omitempty"`
	Process    *ProcessSpec    `json:"process,omitempty"`
	Artifacts  []ArtifactSpec  `json:"artifacts,omitempty"`
	Secrets    []SecretBinding `json:"secrets,omitempty"`
	Workflow   *Workflow       `json:"workflow,omitempty"`
}

type RepositoryRef struct {
	ID       string `json:"id,omitempty"`
	URL      string `json:"url,omitempty"`
	Provider string `json:"provider,omitempty"`
	Ref      string `json:"ref,omitempty"`
	Path     string `json:"path,omitempty"`
}

type ProcessSpec struct {
	Command        []string          `json:"command"`
	WorkingDir     string            `json:"working_dir,omitempty"`
	Environment    map[string]string `json:"environment,omitempty"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty"`
}

type ArtifactSpec struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	Required  bool   `json:"required"`
	Retention string `json:"retention,omitempty"`
}

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

type Workflow struct {
	Steps []WorkflowStep `json:"steps"`
}

type WorkflowStep struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	RunSpec     RunSpec  `json:"run_spec"`
	DependsOn   []string `json:"depends_on,omitempty"`
	RequiresAck bool     `json:"requires_ack"`
}

type WorkflowState struct {
	CurrentStepID string              `json:"current_step_id,omitempty"`
	Steps         []WorkflowStepState `json:"steps"`
}

type WorkflowStepState struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

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

type RunnerEnrollmentConsume struct {
	TokenHash      string
	RequestID      string
	CredentialHash string
}

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

type ClaimedRun struct {
	Lease         RunLease            `json:"lease"`
	Run           TaskRun             `json:"run"`
	PrimitivePlan RunnerPrimitivePlan `json:"primitive_plan"`
}

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

type Repository struct {
	ID         string    `json:"id"`
	ProjectID  string    `json:"project_id"`
	Name       string    `json:"name"`
	URL        string    `json:"url"`
	Provider   string    `json:"provider"`
	DefaultRef string    `json:"default_ref"`
	CreatedAt  time.Time `json:"created_at"`
}

type AccessKey struct {
	ID          string    `json:"id"`
	ProjectID   string    `json:"project_id"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"created_at"`
}

type Inventory struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Source    string    `json:"source"`
	CreatedAt time.Time `json:"created_at"`
}

type RunnerPrimitivePlan struct {
	RunID     string          `json:"run_id"`
	Checkout  *CheckoutPlan   `json:"checkout,omitempty"`
	Process   *ProcessSpec    `json:"process,omitempty"`
	Artifacts []ArtifactSpec  `json:"artifacts,omitempty"`
	Secrets   []SecretBinding `json:"secrets,omitempty"`
}

type CheckoutPlan struct {
	Repository RepositoryRef `json:"repository"`
	DestPath   string        `json:"dest_path"`
}

type Approval struct {
	ID          string     `json:"id"`
	RunID       string     `json:"run_id"`
	Status      string     `json:"status"`
	RequestedBy string     `json:"requested_by"`
	ApprovedBy  *string    `json:"approved_by,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ApprovedAt  *time.Time `json:"approved_at,omitempty"`
}

type AuditEvent struct {
	ID        string         `json:"id"`
	ActorID   string         `json:"actor_id"`
	Action    string         `json:"action"`
	TargetID  string         `json:"target_id"`
	Metadata  map[string]any `json:"metadata"`
	CreatedAt time.Time      `json:"created_at"`
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

type Capability struct {
	Name        string `json:"name"`
	Status      string `json:"status"`
	Description string `json:"description"`
}

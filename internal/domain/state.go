package domain

type (
	// Role identifies a principal's authorization role.
	Role = string
	// PrincipalKind identifies the authentication mechanism for a principal.
	PrincipalKind = string
	// UserStatus identifies a local user's lifecycle state.
	UserStatus = string
	// TokenKind identifies the intended purpose of an API token.
	TokenKind = string
	// TokenStatus identifies an API token's lifecycle state.
	TokenStatus = string
	// RunStatus identifies a task run's lifecycle state.
	RunStatus = string
	// LeaseStatus identifies a run lease's lifecycle state.
	LeaseStatus = string
	// RunnerStatus identifies a runner's lifecycle state.
	RunnerStatus = string
	// ApprovalStatus identifies a run approval decision.
	ApprovalStatus = string
	// LogStream identifies the source of runner output.
	LogStream = string
	// ArtifactKind identifies the kind of captured artifact.
	ArtifactKind = string
	// AccessKeyKind identifies the kind of external access credential.
	AccessKeyKind = string
	// InventoryKind identifies the source form of an inventory.
	InventoryKind = string
	// Provider identifies the source of a repository or secret.
	Provider = string
	// RunType identifies the runner adapter for a task.
	RunType = string
	// DeploymentStatus identifies a deployment's lifecycle state.
	DeploymentStatus = string
)

// DeploymentQueued and the related constants describe deployment lifecycle states.
const (
	DeploymentQueued              = "queued"
	DeploymentWaitingConfirmation = "waiting_confirmation"
	DeploymentAssigned            = "assigned"
	DeploymentPreparing           = "preparing"
	DeploymentApplying            = "applying"
	DeploymentVerifying           = "verifying"
	DeploymentSucceeded           = "succeeded"
	DeploymentFailed              = "failed"
	DeploymentCanceled            = "canceled"
	DeploymentCancelRequested     = "cancel_requested"
	DeploymentRollingBack         = "rolling_back"
	DeploymentRolledBack          = "rolled_back"
	DeploymentRollbackFailed      = "rollback_failed"
	DeploymentManualIntervention  = "manual_intervention"
)

// IsTerminalDeploymentStatus reports whether status needs no further deployment work.
func IsTerminalDeploymentStatus(status string) bool {
	switch status {
	case DeploymentSucceeded, DeploymentFailed, DeploymentCanceled, DeploymentRolledBack, DeploymentRollbackFailed, DeploymentManualIntervention:
		return true
	default:
		return false
	}
}

// RootDeploymentTransitionAllowed is the single transition table shared by the
// runner-facing API and both persistence implementations. Before applying a
// revision, a deployment can stop directly. Once applying begins, a request to
// cancel records intent only and must be followed by rollback or explicit
// manual intervention; it can never silently become a direct cancellation.
func RootDeploymentTransitionAllowed(from, to DeploymentStatus) bool {
	allowed := map[DeploymentStatus][]DeploymentStatus{
		DeploymentQueued:              {DeploymentWaitingConfirmation, DeploymentAssigned, DeploymentFailed, DeploymentCanceled},
		DeploymentWaitingConfirmation: {DeploymentFailed, DeploymentCanceled},
		DeploymentAssigned:            {DeploymentPreparing, DeploymentFailed, DeploymentCanceled},
		DeploymentPreparing:           {DeploymentApplying, DeploymentFailed, DeploymentCanceled},
		DeploymentApplying:            {DeploymentVerifying, DeploymentCancelRequested},
		DeploymentVerifying:           {DeploymentSucceeded, DeploymentCancelRequested},
		DeploymentCancelRequested:     {DeploymentManualIntervention},
	}
	for _, candidate := range allowed[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

// RollbackChildTransitionAllowed deliberately has no generic terminal routes.
// A linked child only settles its root after it has verified rollback health or
// has failed loudly; it cannot succeed, cancel, or fail as an ordinary run.
func RollbackChildTransitionAllowed(from, to DeploymentStatus) bool {
	allowed := map[DeploymentStatus][]DeploymentStatus{
		DeploymentQueued:    {DeploymentAssigned},
		DeploymentAssigned:  {DeploymentPreparing},
		DeploymentPreparing: {DeploymentApplying},
		DeploymentApplying:  {DeploymentVerifying},
		DeploymentVerifying: {DeploymentRolledBack, DeploymentRollbackFailed},
	}
	for _, candidate := range allowed[from] {
		if candidate == to {
			return true
		}
	}
	return false
}

// DeploymentTransitionAllowed reports whether a root deployment may transition.
func DeploymentTransitionAllowed(from, to DeploymentStatus) bool {
	return RootDeploymentTransitionAllowed(from, to)
}

// DeploymentRoleTransitionAllowed applies the transition rules for the deployment role.
func DeploymentRoleTransitionAllowed(isRollbackChild bool, from, to DeploymentStatus) bool {
	if isRollbackChild {
		return RollbackChildTransitionAllowed(from, to)
	}
	return RootDeploymentTransitionAllowed(from, to)
}

// RoleSystemAdmin and the related constants identify authorization roles.
const (
	RoleUser        = "user"
	RoleSystemAdmin = "system_admin"
	RoleRunnerAdmin = "runner_admin"
	RoleOwner       = "owner"
	RoleMaintainer  = "maintainer"
	RoleViewer      = "viewer"
	RoleRunner      = "runner"
)

// PrincipalLocal and the related constants identify authenticated principal types.
const (
	PrincipalLocal    = "local"
	PrincipalAPIToken = "api_token"
	PrincipalRunner   = "runner"
)

// UserActive identifies the enabled user state.
const (
	UserActive = "active"
)

// TokenKindServiceAccount and TokenKindBootstrap identify API token purposes.
const (
	TokenKindServiceAccount = "service_account"
	TokenKindBootstrap      = "bootstrap"
)

// TokenActive and TokenRevoked identify API token states.
const (
	TokenActive  = "active"
	TokenRevoked = "revoked"
)

// RunQueued and the related constants identify task-run lifecycle states.
const (
	RunQueued          = "queued"
	RunRunning         = "running"
	RunSucceeded       = "succeeded"
	RunFailed          = "failed"
	RunCanceled        = "canceled"
	RunWaitingApproval = "waiting_approval"
)

// LeaseActive and LeaseExpired identify run-lease lifecycle states.
const (
	LeaseActive  = "active"
	LeaseExpired = "expired"
)

// RunnerActive and the related constants identify runner lifecycle states.
const (
	RunnerActive  = "active"
	RunnerStale   = "stale"
	RunnerRevoked = "revoked"
)

// ApprovalPending and the related constants identify approval decisions.
const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalRejected = "rejected"
)

// WorkflowPending and WorkflowRunning identify workflow-step states.
const (
	WorkflowPending = "pending"
	WorkflowRunning = "running"
)

// LogSystem and the related constants identify runner output streams.
const (
	LogSystem = "system"
	LogStdout = "stdout"
	LogStderr = "stderr"
)

// ArtifactFile and ArtifactDirectory identify captured artifact kinds.
const (
	ArtifactFile      = "file"
	ArtifactDirectory = "directory"
)

// AccessKeySSH and the related constants identify access-key kinds.
const (
	AccessKeySSH      = "ssh"
	AccessKeyPassword = "password"
	AccessKeyToken    = "token"
)

// InventoryStatic and InventoryDynamic identify inventory kinds.
const (
	InventoryStatic  = "static"
	InventoryDynamic = "dynamic"
)

// ProviderGit and the related constants identify supported source providers.
const (
	ProviderGit        = "git"
	ProviderEnv        = "env"
	ProviderRunnerFile = "runner_file"
)

// RunTypeShell and the related constants identify supported runner adapters.
const (
	RunTypeShell         = "shell"
	RunTypeAnsible       = "ansible"
	RunTypeOpenTofu      = "opentofu"
	RunTypeComposeDeploy = "compose-deploy"
)

// IsTerminalRunStatus reports whether status needs no further run processing.
func IsTerminalRunStatus(status string) bool {
	switch status {
	case RunSucceeded, RunFailed, RunCanceled:
		return true
	default:
		return false
	}
}

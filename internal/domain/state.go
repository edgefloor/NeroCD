package domain

type (
	Role             = string
	PrincipalKind    = string
	UserStatus       = string
	TokenKind        = string
	TokenStatus      = string
	RunStatus        = string
	LeaseStatus      = string
	RunnerStatus     = string
	ApprovalStatus   = string
	LogStream        = string
	ArtifactKind     = string
	AccessKeyKind    = string
	InventoryKind    = string
	Provider         = string
	RunType          = string
	DeploymentStatus = string
)

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

func IsTerminalDeploymentStatus(status string) bool {
	switch status {
	case DeploymentSucceeded, DeploymentFailed, DeploymentCanceled, DeploymentRolledBack, DeploymentRollbackFailed, DeploymentManualIntervention:
		return true
	default:
		return false
	}
}

// DeploymentTransitionAllowed is the single transition table shared by the
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

func DeploymentTransitionAllowed(from, to DeploymentStatus) bool {
	return RootDeploymentTransitionAllowed(from, to)
}

func DeploymentRoleTransitionAllowed(isRollbackChild bool, from, to DeploymentStatus) bool {
	if isRollbackChild {
		return RollbackChildTransitionAllowed(from, to)
	}
	return RootDeploymentTransitionAllowed(from, to)
}

const (
	RoleSystemAdmin = "system_admin"
	RoleRunnerAdmin = "runner_admin"
	RoleOwner       = "owner"
	RoleMaintainer  = "maintainer"
	RoleViewer      = "viewer"
	RoleRunner      = "runner"
)

const (
	PrincipalLocal    = "local"
	PrincipalAPIToken = "api_token"
	PrincipalRunner   = "runner"
)

const (
	UserActive = "active"
)

const (
	TokenKindServiceAccount = "service_account"
	TokenKindBootstrap      = "bootstrap"
)

const (
	TokenActive  = "active"
	TokenRevoked = "revoked"
)

const (
	RunQueued          = "queued"
	RunRunning         = "running"
	RunSucceeded       = "succeeded"
	RunFailed          = "failed"
	RunCanceled        = "canceled"
	RunWaitingApproval = "waiting_approval"
)

const (
	LeaseActive  = "active"
	LeaseExpired = "expired"
)

const (
	RunnerActive  = "active"
	RunnerStale   = "stale"
	RunnerRevoked = "revoked"
)

const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalRejected = "rejected"
)

const (
	WorkflowPending = "pending"
	WorkflowRunning = "running"
)

const (
	LogSystem = "system"
	LogStdout = "stdout"
	LogStderr = "stderr"
)

const (
	ArtifactFile      = "file"
	ArtifactDirectory = "directory"
)

const (
	AccessKeySSH      = "ssh"
	AccessKeyPassword = "password"
	AccessKeyToken    = "token"
)

const (
	InventoryStatic  = "static"
	InventoryDynamic = "dynamic"
)

const (
	ProviderGit        = "git"
	ProviderEnv        = "env"
	ProviderRunnerFile = "runner_file"
)

const (
	RunTypeShell         = "shell"
	RunTypeAnsible       = "ansible"
	RunTypeOpenTofu      = "opentofu"
	RunTypeComposeDeploy = "compose-deploy"
)

func IsTerminalRunStatus(status string) bool {
	switch status {
	case RunSucceeded, RunFailed, RunCanceled:
		return true
	default:
		return false
	}
}

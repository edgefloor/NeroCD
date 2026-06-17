package domain

type (
	Role           = string
	PrincipalKind  = string
	UserStatus     = string
	TokenKind      = string
	TokenStatus    = string
	RunStatus      = string
	LeaseStatus    = string
	RunnerStatus   = string
	ApprovalStatus = string
	LogStream      = string
	ArtifactKind   = string
	AccessKeyKind  = string
	InventoryKind  = string
	Provider       = string
	RunType        = string
)

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
	ProviderGit = "git"
	ProviderEnv = "env"
)

const (
	RunTypeShell    = "shell"
	RunTypeAnsible  = "ansible"
	RunTypeOpenTofu = "opentofu"
)

func IsTerminalRunStatus(status string) bool {
	switch status {
	case RunSucceeded, RunFailed, RunCanceled:
		return true
	default:
		return false
	}
}

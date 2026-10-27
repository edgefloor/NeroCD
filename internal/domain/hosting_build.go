package domain

import (
	"errors"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"nerocd/internal/source"
)

// HostingBuildTaskType identifies internal, exactly targeted source/build work.
const HostingBuildTaskType = "hosting_build_v1"

// HostingBuildTriggerRevisionCapability identifies first-checkout commit pinning support.
const HostingBuildTriggerRevisionCapability = "hosting_build_trigger_revision_v1"

// HostingPrivateGitCapability identifies runners that implement bounded HTTPS
// Basic credential acquisition and private checkout. Public build support alone
// does not advertise this feature.
const HostingPrivateGitCapability = "hosting_git_https_basic_v1"

// HostingBuildProtocol identifies the immutable local source/build persistence contract.
const HostingBuildProtocol = "local-build.v1"

// HostingBuildSettings mirrors the accepted local engine's non-secret settings.
// Static assets must already be prepared; no build arguments or credentials exist here.
type HostingBuildSettings struct {
	Strategy        string `json:"strategy"`
	ContextDir      string `json:"context_dir,omitempty"`
	DockerfilePath  string `json:"dockerfile_path,omitempty"`
	BuildTarget     string `json:"build_target,omitempty"`
	PublishDir      string `json:"publish_dir,omitempty"`
	SPAFallback     bool   `json:"spa_fallback,omitempty"`
	ComposeFile     string `json:"compose_file,omitempty"`
	PublicComponent string `json:"public_component,omitempty"`
	ContainerPort   int    `json:"container_port,omitempty"`
}

// HostingGitSource is an immutable trusted source version referenced by a Git resource.
type HostingGitSource struct {
	ID             string                  `json:"id"`
	ResourceID     string                  `json:"resource_id"`
	ProjectID      string                  `json:"project_id"`
	Generation     int64                   `json:"generation"`
	RepositoryURL  string                  `json:"repository_url"`
	RequestedRef   string                  `json:"requested_ref"`
	CredentialKind string                  `json:"credential_kind"`
	Policy         source.RepositoryPolicy `json:"policy"`
	Settings       HostingBuildSettings    `json:"settings"`
}

// HostingSourceGrant authorizes only Git transport for this exact source/policy.
// Version one is immutable; rotation needs a new owner-created grant ID.
type HostingSourceGrant struct {
	ID            string `json:"id"`
	Version       int64  `json:"version"`
	SourceID      string `json:"source_id"`
	ResourceID    string `json:"resource_id"`
	ProjectID     string `json:"project_id"`
	RepositoryURL string `json:"repository_url"`
	PolicyHash    string `json:"policy_hash"`
	SecretID      string `json:"secret_id"`
	SecretVersion int64  `json:"secret_version"`
	Kind          string `json:"kind"`
	CreatedBy     string `json:"created_by"`
	Revoked       bool   `json:"revoked"`
}

// HostingBuildLimits are explicit engine budgets; disk enforcement is monitored.
type HostingBuildLimits struct {
	CPUQuota      int64         `json:"cpu_quota"`
	MemoryBytes   int64         `json:"memory_bytes"`
	SwapBytes     int64         `json:"swap_bytes"`
	Deadline      time.Duration `json:"deadline"`
	ContextBytes  int64         `json:"context_bytes"`
	ContextFiles  int           `json:"context_files"`
	PlanBytes     int64         `json:"plan_bytes"`
	LogBytes      int64         `json:"log_bytes"`
	ExportBytes   int64         `json:"export_bytes"`
	UnpackedBytes int64         `json:"unpacked_bytes"`
	UsageBytes    int64         `json:"usage_bytes"`
	ReserveBytes  uint64        `json:"reserve_bytes"`
	PollInterval  time.Duration `json:"poll_interval"`
	DiskMode      string        `json:"disk_mode"`
}

// HostingSourceLimits freezes the accepted checkout adapter's acquisition budgets.
// Workspace/free-space monitoring is not a hard filesystem quota.
type HostingSourceLimits struct {
	MaxTreeBytes      int64         `json:"max_tree_bytes"`
	MaxWorkspaceBytes int64         `json:"max_workspace_bytes"`
	MinFreeBytes      int64         `json:"min_free_bytes"`
	MaxEntries        int           `json:"max_entries"`
	MaxDepth          int           `json:"max_depth"`
	PollInterval      time.Duration `json:"poll_interval"`
}

// HostingBuildPolicy is trusted in-process operator input, never public request data.
// ToolchainIdentity hashes the fixed, versioned operator toolchain configuration.
type HostingBuildPolicy struct {
	GitCompose        bool                `json:"git_compose,omitempty"`
	Identity          string              `json:"identity"`
	ToolchainIdentity string              `json:"toolchain_identity"`
	Protocol          string              `json:"protocol"`
	Platform          string              `json:"platform"`
	Limits            HostingBuildLimits  `json:"limits"`
	SourceLimits      HostingSourceLimits `json:"source_limits"`
}

// HostingBuildInput contains caller intent, not placement or artifact authority.
type HostingBuildInput struct {
	ExpectedResourceGeneration int64  `json:"expected_resource_generation"`
	ExpectedSourceGeneration   int64  `json:"expected_source_generation"`
	IdempotencyKey             string `json:"idempotency_key"`
	GrantID                    string `json:"grant_id,omitempty"`
}

// HostingBuildTriggerRevision freezes trusted trigger intent before source acquisition.
// It is not a field in the public build request.
type HostingBuildTriggerRevision struct {
	Version        int    `json:"version"`
	ExpectedCommit string `json:"expected_commit"`
}

// HostingBuildSnapshot is immutable desired input captured under transaction locks.
type HostingBuildSnapshot struct {
	BuildID            string                       `json:"build_id"`
	InputID            string                       `json:"input_id"`
	ResourceGeneration int64                        `json:"resource_generation"`
	Source             HostingGitSource             `json:"source"`
	BindingID          string                       `json:"binding_id"`
	DestinationID      string                       `json:"destination_id"`
	ServerID           string                       `json:"server_id"`
	RunnerID           string                       `json:"runner_id"`
	ExpectedDaemonID   string                       `json:"expected_daemon_id,omitempty"`
	Policy             HostingBuildPolicy           `json:"operator_policy"`
	Grant              *HostingSourceGrant          `json:"grant,omitempty"`
	TriggerRevision    *HostingBuildTriggerRevision `json:"trigger_revision,omitempty"`
}

// HostingBuildAuthority is the full fenced tuple, bound to an owned task target.
type HostingBuildAuthority struct {
	BuildID string `json:"build_id"`
	RunID   string `json:"run_id"`
	LeaseID string `json:"lease_id"`
	Attempt int    `json:"attempt"`
	Fence   string `json:"fence"`
}

// HostingBuildSourceReceipt freezes the resolved commit before build effects.
type HostingBuildSourceReceipt struct {
	BuildID    string `json:"build_id"`
	SourceID   string `json:"source_id"`
	InputID    string `json:"input_id"`
	Commit     string `json:"commit"`
	PolicyHash string `json:"policy_hash"`
	TreeDigest string `json:"tree_digest"`
}

// HostingBuildArtifact retains local identity, never a fabricated repository digest.
type HostingBuildArtifact struct {
	Origin            string `json:"origin"`
	BuildID           string `json:"build_id"`
	SourceID          string `json:"source_id"`
	InputID           string `json:"input_id"`
	SourceReceiptHash string `json:"source_receipt_hash"`
	ContextDigest     string `json:"context_digest"`
	RequestDigest     string `json:"request_digest"`
	DaemonID          string `json:"daemon_id"`
	ImageID           string `json:"image_id"`
	IndexDigest       string `json:"index_digest"`
	ManifestDigest    string `json:"manifest_digest"`
	ConfigDigest      string `json:"config_digest"`
	Platform          string `json:"platform"`
	ContextPolicy     string `json:"context_policy,omitempty"`
}

// HostingBuild is metadata only. Queued with an empty RunID is not dispatched.
type HostingBuild struct {
	CreatedAt     time.Time                  `json:"created_at"`
	UpdatedAt     time.Time                  `json:"updated_at"`
	ID            string                     `json:"id"`
	ProjectID     string                     `json:"project_id"`
	ResourceID    string                     `json:"resource_id"`
	State         string                     `json:"state"`
	RunID         string                     `json:"run_id,omitempty"`
	DaemonID      string                     `json:"daemon_id,omitempty"`
	Revision      int64                      `json:"revision"`
	CreatedBy     string                     `json:"created_by"`
	Snapshot      HostingBuildSnapshot       `json:"snapshot"`
	SourceReceipt *HostingBuildSourceReceipt `json:"source_receipt,omitempty"`
	Artifact      *HostingBuildArtifact      `json:"artifact,omitempty"`
	ComposeResult *HostingBuildComposeResult `json:"compose_result,omitempty"`
	FailureCode   string                     `json:"failure_code,omitempty"`
}

var buildSettingTarget = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
var buildDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var buildCommit = regexp.MustCompile(`^([0-9a-f]{40}|[0-9a-f]{64})$`)

// ValidateHostingBuildSettings checks canonical settings before any database mutation.
func ValidateHostingBuildSettings(s HostingBuildSettings) error {
	if s.Strategy != "dockerfile" && s.Strategy != "railpack" && s.Strategy != "static" && s.Strategy != "compose" {
		return errors.New("unsupported build strategy")
	}
	for _, p := range []string{s.ContextDir, s.DockerfilePath, s.PublishDir, s.ComposeFile} {
		if p == "" {
			continue
		}
		if len(p) > 512 || !fs.ValidPath(p) || p == "." || path.Clean(p) != p || strings.ContainsAny(p, "\\:") || len(strings.Split(p, "/")) > 32 {
			return errors.New("invalid build settings path")
		}
		for _, c := range p {
			if unicode.IsControl(c) {
				return errors.New("invalid build settings path")
			}
		}
		for _, part := range strings.Split(p, "/") {
			n := strings.ToLower(part)
			if n == ".git-credentials" || n == ".kube" || n == ".azure" || n == ".gnupg" || n == ".terraform" || n == "auth.json" || n == ".yarnrc" || n == ".yarnrc.yml" || n == ".pypirc" || n == "credentials" || n == "credentials.json" || n == "id_rsa" || n == "id_ed25519" || n == ".git" || n == ".env" || strings.HasPrefix(n, ".env.") || n == ".ssh" || n == ".aws" || n == ".docker" || n == ".config" || n == ".npmrc" || n == ".netrc" || strings.HasSuffix(n, ".key") || strings.HasSuffix(n, ".pem") {
				return errors.New("credential path denied")
			}
		}
	}
	if s.Strategy == "compose" {
		if s.ComposeFile == "" || !stackName.MatchString(s.PublicComponent) || s.ContainerPort < 1 || s.ContainerPort > 65535 {
			return errors.New("compose file, public component and container port are required")
		}
	} else if s.ComposeFile != "" || s.PublicComponent != "" || s.ContainerPort != 0 {
		return errors.New("compose settings require compose strategy")
	}
	if s.BuildTarget != "" && !buildSettingTarget.MatchString(s.BuildTarget) {
		return errors.New("invalid build target")
	}
	if s.Strategy != "dockerfile" && (s.BuildTarget != "" || s.DockerfilePath != "") {
		return errors.New("dockerfile settings require dockerfile strategy")
	}
	if s.Strategy != "static" && (s.PublishDir != "" || s.SPAFallback) {
		return errors.New("static settings require static strategy")
	}
	return nil
}

// ValidateHostingGitSource admits explicit policy-controlled Git source configuration.
func ValidateHostingGitSource(s HostingGitSource) error {
	if !buildID(s.ID) || !buildID(s.ResourceID) || !buildID(s.ProjectID) || s.Generation < 1 {
		return errors.New("invalid source identity")
	}
	u, err := s.Policy.ValidateURL(s.RepositoryURL, false)
	if err != nil || u.String() != s.RepositoryURL || len(s.RepositoryURL) > 8192 || s.Policy.CredentialReferenceID != "" {
		return errors.New("invalid repository policy or URL")
	}
	if s.CredentialKind != "public" && s.CredentialKind != "https_basic" && s.CredentialKind != "ssh_private_key" {
		return errors.New("invalid source credential kind")
	}
	if u.Scheme != "https" && u.Scheme != "ssh" || u.Scheme == "ssh" && s.CredentialKind != "ssh_private_key" || u.Scheme == "https" && s.CredentialKind == "ssh_private_key" {
		return errors.New("source authentication does not match transport")
	}
	if p := u.Port(); p != "" {
		if port, parseErr := strconv.Atoi(p); parseErr != nil || port < 1 || port > 65535 {
			return errors.New("invalid source port")
		}
	}
	r := s.RequestedRef
	if r == "" || r != strings.TrimSpace(r) || len(r) > 512 || strings.HasPrefix(r, "-") || strings.ContainsAny(r, ":*?[]\\~^ \t\r\n\x00") || strings.Contains(r, "..") || strings.Contains(r, "@{") || strings.ContainsFunc(r, unicode.IsControl) {
		return errors.New("invalid source ref")
	}
	return ValidateHostingBuildSettings(s.Settings)
}
func buildID(s string) bool {
	return s != "" && len(s) <= 128 && s == strings.TrimSpace(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

// ValidateHostingBuildPolicy checks trusted engine and source acquisition budgets.
func ValidateHostingBuildPolicy(p HostingBuildPolicy) error {
	if !buildDigest.MatchString(p.Identity) || !buildDigest.MatchString(p.ToolchainIdentity) || p.Protocol != HostingBuildProtocol || (p.Platform != "linux/amd64" && p.Platform != "linux/arm64") {
		return errors.New("invalid trusted build policy")
	}
	a := p.SourceLimits
	if a.MaxTreeBytes <= 0 || a.MaxTreeBytes > 1<<50 || a.MaxWorkspaceBytes < a.MaxTreeBytes || a.MaxWorkspaceBytes > 1<<50 || a.MinFreeBytes <= 0 || a.MinFreeBytes > 1<<50 || a.MaxEntries < 1 || a.MaxEntries > 1000000 || a.MaxDepth < 1 || a.MaxDepth > 256 || a.PollInterval < 10*time.Millisecond || a.PollInterval > time.Second {
		return errors.New("invalid source acquisition limits")
	}
	l := p.Limits
	if l.CPUQuota < 2000 || l.CPUQuota > 1000000000 || l.MemoryBytes < 64<<20 || l.SwapBytes < 0 || l.Deadline <= 0 || l.Deadline > 24*time.Hour || l.ContextFiles <= 0 || l.DiskMode != "monitored" || l.PollInterval < 10*time.Millisecond || l.PollInterval > time.Second || l.ReserveBytes == 0 || l.ReserveBytes > 1<<50 || l.LogBytes > 64<<20 || l.PlanBytes > 16<<20 {
		return errors.New("invalid build limits")
	}
	for _, n := range []int64{l.ContextBytes, l.PlanBytes, l.LogBytes, l.ExportBytes, l.UnpackedBytes, l.UsageBytes, l.MemoryBytes} {
		if n <= 0 || n > 1<<50 {
			return errors.New("invalid bounded build limits")
		}
	}
	if l.SwapBytes > 1<<50 {
		return errors.New("invalid swap budget")
	}
	return nil
}

// ValidateHostingBuildTriggerRevision checks optional trusted intent without
// changing historical nil-revision source semantics.
func ValidateHostingBuildTriggerRevision(s HostingBuildSnapshot) error {
	r := s.TriggerRevision
	if r == nil {
		return nil
	}
	if r.Version != 1 || !buildCommit.MatchString(r.ExpectedCommit) || strings.Trim(r.ExpectedCommit, "0") == "" {
		return errors.New("invalid build trigger revision")
	}
	configured := strings.ToLower(s.Source.RequestedRef)
	if buildCommit.MatchString(configured) && configured != r.ExpectedCommit {
		return errors.New("configured commit does not match trigger revision")
	}
	return nil
}

// ValidateHostingBuildSourceReceipt binds a full commit and tree to the frozen source input.
func ValidateHostingBuildSourceReceipt(r HostingBuildSourceReceipt, s HostingBuildSnapshot) error {
	if ValidateHostingBuildTriggerRevision(s) != nil || s.TriggerRevision != nil && r.Commit != s.TriggerRevision.ExpectedCommit || (buildCommit.MatchString(s.Source.RequestedRef) && r.Commit != s.Source.RequestedRef) || r.BuildID != s.BuildID || r.SourceID != s.Source.ID || r.InputID != s.InputID || !buildCommit.MatchString(r.Commit) || r.PolicyHash != HostingDocumentHash(s.Source.Policy) || !buildDigest.MatchString(r.TreeDigest) {
		return errors.New("source receipt identity mismatch")
	}
	return nil
}

// ValidateHostingBuildArtifact checks local artifact identity against its committed source and daemon.
func ValidateHostingBuildArtifact(a HostingBuildArtifact, b HostingBuild) error {
	if b.SourceReceipt == nil || b.Snapshot.TriggerRevision != nil && ValidateHostingBuildSourceReceipt(*b.SourceReceipt, b.Snapshot) != nil || a.Origin != "local_build" || a.BuildID != b.ID || a.SourceID != b.Snapshot.Source.ID || a.InputID != b.Snapshot.InputID || a.SourceReceiptHash != HostingDocumentHash(b.SourceReceipt) || a.DaemonID == "" || a.DaemonID != b.DaemonID || a.Platform != b.Snapshot.Policy.Platform {
		return errors.New("artifact binding mismatch")
	}
	for _, d := range []string{a.ContextDigest, a.RequestDigest, a.ImageID, a.IndexDigest, a.ManifestDigest, a.ConfigDigest} {
		if !buildDigest.MatchString(d) {
			return errors.New("invalid artifact identity")
		}
	}
	if a.ImageID != a.IndexDigest && a.ImageID != a.ManifestDigest && a.ImageID != a.ConfigDigest {
		return errors.New("unrelated daemon image identity")
	}
	if b.Snapshot.Source.Settings.Strategy == "static" {
		if a.ContextPolicy != "static-assets-v1" {
			return errors.New("static artifact policy required")
		}
	} else if a.ContextPolicy != "" {
		return errors.New("unexpected artifact policy")
	}
	return nil
}

// ValidHostingBuildFailure admits fixed nonsecret codes, never stderr.
func ValidHostingBuildFailure(code string) bool {
	switch code {
	case "cancel_requested", "source_failed", "build_failed", "deadline_exceeded", "limits_exceeded", "cleanup_unconfirmed", "authority_lost", "canceled":
		return true
	}
	return false
}

// ValidateHostingBuildTask rejects any generic execution material on an internal task.
// Persisted task targets and live lease authorization are separately mandatory.
func ValidateHostingBuildTask(run TaskRun) error {
	s := run.RunSpec
	if !validBuildTaskID(s.HostingBuildID) || run.ID != "build_run_"+s.HostingBuildID || s.Type != HostingBuildTaskType || s.Process != nil || s.Repository != nil || s.Workflow != nil || len(s.Inputs) != 0 || len(s.Secrets) != 0 || len(s.Artifacts) != 0 || len(run.Workflow.Steps) != 0 || s.HostingOperationID != "" || s.HostingMode != "" || s.ProxyOperationID != "" || s.ProxyMode != "" {
		return errors.New("invalid internal hosting build task")
	}
	return nil
}

func validBuildTaskID(id string) bool {
	if len(id) < 1 || len(id) > 63 {
		return false
	}
	for i, c := range id {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && (c != '-' || i == 0) {
			return false
		}
	}
	return true
}

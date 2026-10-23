package domain

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Bootstrap bounds cap review freshness, lease authority and durable history.
const (
	ServerBootstrapLeaseLifetime  = 30 * time.Second
	ServerBootstrapMaxLifetime    = time.Hour
	ServerBootstrapMaxAttempts    = 8
	ServerBootstrapMaxCommands    = 32
	ServerBootstrapMaxInspections = 8
	ServerBootstrapMaxDocument    = 16384
	ServerBootstrapPreset         = "hosting_image_v1"
)

// ErrServerBootstrapInput rejects malformed or unsupported onboarding facts.
var ErrServerBootstrapInput = errors.New("invalid server bootstrap input")
var bootstrapHex = regexp.MustCompile(`^[0-9a-f]{32}$`)
var bootstrapDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ServerBootstrapTarget pins one root SSH endpoint independently of its key.
type ServerBootstrapTarget struct {
	IP              string `json:"ip"`
	Port            uint16 `json:"port"`
	User            string `json:"user"`
	HostFingerprint string `json:"host_fingerprint"`
}

// ServerBootstrapArtifact is an exact reviewed deployment-only runner artifact.
type ServerBootstrapArtifact struct {
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
}

// ServerBootstrapInput contains only reviewable facts, never key or token bytes.
type ServerBootstrapInput struct {
	ProjectID        string                  `json:"project_id"`
	Name             string                  `json:"name"`
	Target           ServerBootstrapTarget   `json:"target"`
	ControllerURL    string                  `json:"controller_url"`
	Artifact         ServerBootstrapArtifact `json:"artifact"`
	SSHSecretID      string                  `json:"ssh_secret_id"`
	SSHSecretVersion int64                   `json:"ssh_secret_version"`
}

// Validate rejects aliases so the persisted endpoint is its serialization identity.
func (in ServerBootstrapInput) Validate() error {
	ip, err := netip.ParseAddr(in.Target.IP)
	if err != nil || ip.Zone() != "" || ip.Is4In6() || ip.String() != in.Target.IP || ip.IsUnspecified() || ip.IsMulticast() || ip == netip.AddrFrom4([4]byte{255, 255, 255, 255}) || in.Target.Port == 0 || in.Target.User != "root" || !validBootstrapFingerprint(in.Target.HostFingerprint) || !validBootstrapControllerURL(in.ControllerURL) || !bootstrapText(in.ProjectID, 256) || ValidateHostingName(in.Name) != nil || !bootstrapText(in.SSHSecretID, 256) || in.SSHSecretVersion <= 0 || in.Artifact.Size <= 0 || in.Artifact.Size > 256<<20 || !bootstrapDigest.MatchString(in.Artifact.SHA256) || !bootstrapText(in.Artifact.Version, 128) || in.Artifact.Architecture != "amd64" && in.Artifact.Architecture != "arm64" {
		return ErrServerBootstrapInput
	}
	return nil
}

func validBootstrapControllerURL(raw string) bool {
	if len(raw) > 2048 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Opaque != "" || u.RawPath != "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.String() != raw {
		return false
	}
	if port := u.Port(); port != "" {
		number, e := strconv.Atoi(port)
		if e != nil || number <= 0 || number > 65535 {
			return false
		}
	}
	return true
}

func bootstrapText(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && strings.TrimSpace(s) == s && !strings.ContainsAny(s, "\x00\r\n")
}
func validBootstrapFingerprint(s string) bool {
	if !strings.HasPrefix(s, "SHA256:") {
		return false
	}
	raw, err := base64.RawStdEncoding.DecodeString(s[7:])
	return err == nil && len(raw) == 32 && base64.RawStdEncoding.EncodeToString(raw) == s[7:]
}

// ServerBootstrapIDs are reserved once and survive every exact command replay.
type ServerBootstrapIDs struct {
	OperationID           string `json:"operation_id"`
	InstallationID        string `json:"installation_id"`
	InstallOperationID    string `json:"install_operation_id"`
	RunnerID              string `json:"runner_id"`
	EnrollmentID          string `json:"enrollment_id"`
	CredentialReferenceID string `json:"credential_reference_id"`
	ServerID              string `json:"server_id"`
	DestinationID         string `json:"destination_id"`
}

// ServerBootstrapOperation separates retained remote ownership from registration.
type ServerBootstrapOperation struct {
	Review          *ServerBootstrapReview `json:"review,omitempty"`
	ActiveCommandID string                 `json:"active_command_id,omitempty"`
	ActiveStage     *ServerBootstrapStage  `json:"active_stage,omitempty"`
	Commands        []ServerBootstrapStage `json:"commands,omitempty"`
	IDs             ServerBootstrapIDs     `json:"ids"`
	Input           ServerBootstrapInput   `json:"input"`
	CreatedBy       string                 `json:"created_by"`
	RequestHash     string                 `json:"request_hash"`
	Version         int64                  `json:"version"`
	State           string                 `json:"state"`
	RemoteHold      bool                   `json:"remote_hold"`
	RestoreHeld     bool                   `json:"restore_held"`
	CreatedAt       time.Time              `json:"created_at"`
}

// ServerBootstrapPrerequisites is a prepared-host profile, not health or enrollment.
// The exact Compose version is qualified by the separate pinned SSH fixture.
type ServerBootstrapPrerequisites struct {
	Profile            string `json:"profile"`
	OSState            string `json:"os_state"`
	OSID               string `json:"os_id"`
	OSVersion          string `json:"os_version"`
	Architecture       string `json:"architecture"`
	SystemdState       string `json:"systemd_state"`
	SystemdStatus      string `json:"systemd_status"`
	DockerState        string `json:"docker_state"`
	DockerAPIVersion   string `json:"docker_api_version"`
	ComposeState       string `json:"compose_state"`
	ComposeVersion     string `json:"compose_version"`
	DiskState          string `json:"disk_state"`
	DiskTotalBytes     uint64 `json:"disk_total_bytes"`
	DiskAvailableBytes uint64 `json:"disk_available_bytes"`
}

// Qualifies permits only the initial pinned Debian 12 deployment profile.
func (p ServerBootstrapPrerequisites) Qualifies(architecture string) bool {
	return p.Profile == "prepared_debian12_v1" && p.OSState == "available" && p.OSID == "debian" && p.OSVersion == "12" && p.Architecture == architecture && p.SystemdState == "available" && p.SystemdStatus == "running" && p.DockerState == "available" && p.DockerAPIVersion == "1.54" && p.ComposeState == "available" && p.ComposeVersion == "5.1.1" && p.DiskState == "available" && p.DiskTotalBytes >= p.DiskAvailableBytes && p.DiskAvailableBytes >= 1<<30
}

// ServerBootstrapReview stores exact caller-supplied transport bytes, not a second serializer.
type ServerBootstrapReview struct {
	Prerequisites   ServerBootstrapPrerequisites `json:"prerequisites"`
	Manifest        []byte                       `json:"manifest"`
	ManifestSHA256  string                       `json:"manifest_sha256"`
	ProbeObservedAt time.Time                    `json:"probe_observed_at"`
	ProbeExpiresAt  time.Time                    `json:"probe_expires_at"`
	TargetHash      string                       `json:"target_hash"`
}

// Validate binds a closed transport identity without changing its canonical bytes.
func (r ServerBootstrapReview) Validate(op ServerBootstrapOperation, now time.Time) error {
	if !r.Prerequisites.Qualifies(op.Input.Artifact.Architecture) || len(r.Manifest) == 0 || len(r.Manifest) > ServerBootstrapMaxDocument || !r.ProbeExpiresAt.After(now) || r.ProbeObservedAt.After(now) || !r.ProbeExpiresAt.After(r.ProbeObservedAt) || r.ProbeExpiresAt.Sub(r.ProbeObservedAt) > ServerBootstrapLeaseLifetime || r.TargetHash != HostingDocumentHash(op.Input.Target) {
		return ErrServerBootstrapInput
	}
	digest := sha256.Sum256(r.Manifest)
	if r.ManifestSHA256 != hex.EncodeToString(digest[:]) {
		return ErrServerBootstrapInput
	}
	var m struct {
		Version               int                     `json:"version"`
		InstallationID        string                  `json:"installation_id"`
		InstallOperationID    string                  `json:"install_operation_id"`
		RunnerID              string                  `json:"runner_id"`
		CredentialReferenceID string                  `json:"credential_reference_id"`
		ControllerURL         string                  `json:"controller_url"`
		Artifact              ServerBootstrapArtifact `json:"artifact"`
		CapabilityPreset      string                  `json:"capability_preset"`
	}
	if DecodeHostingRuntimeLogsJSON(r.Manifest, &m) != nil || m.Version != 1 || !bootstrapHex.MatchString(m.InstallationID) || !bootstrapHex.MatchString(m.InstallOperationID) || m.InstallationID != op.IDs.InstallationID || m.InstallOperationID != op.IDs.InstallOperationID || m.RunnerID != op.IDs.RunnerID || m.CredentialReferenceID != op.IDs.CredentialReferenceID || m.ControllerURL != op.Input.ControllerURL || m.Artifact != op.Input.Artifact || m.CapabilityPreset != ServerBootstrapPreset {
		return ErrServerBootstrapInput
	}
	return nil
}

// ServerBootstrapCommand is immutable actor-bound review and request evidence.
type ServerBootstrapCommand struct {
	ID              string                 `json:"id"`
	OperationID     string                 `json:"operation_id"`
	ActorID         string                 `json:"actor_id"`
	Key             string                 `json:"key"`
	RequestHash     string                 `json:"request_hash"`
	Action          string                 `json:"action"`
	ExpectedVersion int64                  `json:"expected_version"`
	Review          *ServerBootstrapReview `json:"review,omitempty"`
	Deadline        time.Time              `json:"deadline"`
	CreatedAt       time.Time              `json:"created_at"`
}

// ServerBootstrapStage is finite DB-clock authority, never proof a remote process stopped.
type ServerBootstrapStage struct {
	Receipt      *ServerBootstrapReceipt `json:"receipt,omitempty"`
	Command      ServerBootstrapCommand  `json:"command"`
	LeaseID      string                  `json:"lease_id"`
	Attempt      int32                   `json:"attempt"`
	Fence        int64                   `json:"fence"`
	ExpiresAt    time.Time               `json:"expires_at"`
	EffectIntent bool                    `json:"effect_intent"`
}

// ServerBootstrapReceipt excludes arbitrary remote output and credential material.
type ServerBootstrapReceipt struct {
	CommandID         string          `json:"command_id"`
	LeaseID           string          `json:"lease_id"`
	Attempt           int32           `json:"attempt"`
	Fence             int64           `json:"fence"`
	Outcome           string          `json:"outcome"`
	ManifestSHA256    string          `json:"manifest_sha256,omitempty"`
	RemoteOperationID string          `json:"remote_operation_id,omitempty"`
	Evidence          json.RawMessage `json:"evidence,omitempty"`
}

// ServerBootstrapActionAllowed is the complete foundation transition table.
// Remote restart/cleanup are unavailable until exact receipt integration exists.
func ServerBootstrapActionAllowed(action, state string, held bool) bool {
	switch action {
	case "probe":
		return state == "draft"
	case "review":
		return (state == "draft" || state == "reviewed") && !held
	case "install":
		return state == "reviewed" && !held
	case "inspect_owned":
		return state == "reviewed" || state == "running" || state == "installed_waiting_enrollment" || state == "registered" || state == "completed" || state == "uncertain"
	case "register_server":
		return state == "installed_waiting_enrollment"
	case "grant_destination":
		return state == "registered"
	case "cancel":
		return (state == "draft" || state == "reviewed") && !held
	default:
		return false
	}
}

// ServerBootstrapCapabilities returns the frozen deployment-only launcher identity.
// Every call owns its slice; changing this list requires a new launcher profile.
func ServerBootstrapCapabilities() []string {
	return []string{RunTypeComposeDeploy, RunTypeImageDeploy, HostingReleasePortsCapability, ServerReadinessCapability}
}

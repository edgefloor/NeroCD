package serverbootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
)

// Artifact describes an exact operator-provisioned Linux binary, never a download URL.
type Artifact struct {
	Size         int64  `json:"size"`
	SHA256       string `json:"sha256"`
	Version      string `json:"version"`
	Architecture string `json:"architecture"`
}

// InstallManifest is immutable installation ownership, not controller authorization.
type InstallManifest struct {
	Version               int      `json:"version"`
	InstallationID        string   `json:"installation_id"`
	InstallOperationID    string   `json:"install_operation_id"`
	RunnerID              string   `json:"runner_id"`
	CredentialReferenceID string   `json:"credential_reference_id"`
	ControllerURL         string   `json:"controller_url"`
	Artifact              Artifact `json:"artifact"`
	CapabilityPreset      string   `json:"capability_preset"`
}

// Ownership supplies the exact manifest required for every owned action.
type Ownership struct{ Manifest InstallManifest }

// Mutation identifies one immutable action. An uncertain result requires inspection.
type Mutation struct {
	OperationID string
	Ownership   Ownership
}

// ArtifactSource identifies a local regular file opened without following symlinks.
type ArtifactSource struct{ Path string }

// InstallRequest carries a secret separately from immutable public metadata.
type InstallRequest struct {
	OperationID     string
	Manifest        InstallManifest
	Artifact        ArtifactSource
	EnrollmentToken []byte
}

// OwnedState describes local owned state, never enrollment authority or server readiness.
type OwnedState string

// Installer states retain uncertainty rather than inventing a completed action.
const (
	OwnedAbsent    OwnedState = "absent"
	OwnedStaged    OwnedState = "staged"
	OwnedInstalled OwnedState = "installed"
	OwnedRemoved   OwnedState = "removed"
	OwnedUncertain OwnedState = "uncertain"
)

// OwnedInspection contains bounded nonsecret evidence about one exact installation.
type OwnedInspection struct {
	InstallationID       string     `json:"installation_id"`
	ManifestSHA256       string     `json:"manifest_sha256"`
	OperationID          string     `json:"operation_id"`
	State                OwnedState `json:"state"`
	ArtifactMatches      bool       `json:"artifact_matches"`
	ServiceActive        bool       `json:"service_active"`
	ServiceJobPending    bool       `json:"service_job_pending"`
	InvocationID         string     `json:"invocation_id"`
	EnrollmentFileExists bool       `json:"enrollment_file_exists"`
	CredentialFileExists bool       `json:"credential_file_exists"`
}

// Installer failures are safe codes without remote output or credential-derived facts.
const (
	InvalidArtifact   ErrorCode = "invalid_artifact"
	InvalidManifest   ErrorCode = "invalid_manifest"
	OwnershipConflict ErrorCode = "ownership_conflict"
	RemoteBusy        ErrorCode = "remote_busy"
	PartialTransfer   ErrorCode = "partial_transfer"
	RemoteFailure     ErrorCode = "remote_failure"
	Uncertain         ErrorCode = "uncertain"
)

const maxArtifactSize int64 = 256 << 20

var (
	operationIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
	digestPattern      = regexp.MustCompile(`^[a-f0-9]{64}$`)
	runnerIDPattern    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)
	referencePattern   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	enrollmentPattern  = regexp.MustCompile(`^nce_[a-f0-9]{64}$`)
)

// CanonicalManifest validates v1 public metadata and returns its exact JSON and
// lowercase SHA256 identity. It includes no enrollment or SSH credentials.
func CanonicalManifest(manifest InstallManifest) ([]byte, string, error) {
	return manifestBytes(manifest)
}

func manifestBytes(manifest InstallManifest) ([]byte, string, error) {
	artifact := manifest.Artifact
	controller, err := url.Parse(manifest.ControllerURL)
	validPort := true
	if err == nil && controller.Port() != "" {
		port, portErr := strconv.ParseUint(controller.Port(), 10, 16)
		validPort = portErr == nil && port != 0
	}
	if manifest.Version != 1 || !operationIDPattern.MatchString(manifest.InstallationID) ||
		!operationIDPattern.MatchString(manifest.InstallOperationID) || !runnerIDPattern.MatchString(manifest.RunnerID) ||
		!referencePattern.MatchString(manifest.CredentialReferenceID) || manifest.CapabilityPreset != "hosting_image_v1" ||
		err != nil || !validPort || controller.Scheme != "https" || controller.Hostname() == "" || controller.User != nil || controller.RawQuery != "" ||
		controller.Fragment != "" || controller.Opaque != "" || (controller.Path != "" && controller.Path != "/") ||
		len(manifest.ControllerURL) > 512 || artifact.Size <= 0 || artifact.Size > maxArtifactSize || !digestPattern.MatchString(artifact.SHA256) ||
		!versionToken.MatchString(artifact.Version) || (artifact.Architecture != "amd64" && artifact.Architecture != "arm64") {
		return nil, "", InvalidManifest
	}
	data, err := json.Marshal(manifest)
	if err != nil || len(data) > 4096 {
		return nil, "", InvalidManifest
	}
	digest := sha256.Sum256(data)
	return data, hex.EncodeToString(digest[:]), nil
}

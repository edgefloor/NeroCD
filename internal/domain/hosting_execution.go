package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/distribution/reference"
)

// Image hosting protocol and policy identifiers are versioned independently.
const (
	RunTypeImageDeploy     = "image_deploy"
	HostingCompilerVersion = "image-hosting.v1"
	HostingPolicyVersion   = "public-image.loopback.v1"
)

// HostingReadiness is an origin-relative HTTP probe on the reserved local port.
type HostingReadiness struct {
	Path           string `json:"path"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// HostingDeploymentInput is the complete public image deployment request.
type HostingDeploymentInput struct {
	ExpectedGeneration int64            `json:"expected_generation"`
	IdempotencyKey     string           `json:"idempotency_key"`
	ContainerPort      int              `json:"container_port"`
	HostPort           int              `json:"host_port"`
	Readiness          HostingReadiness `json:"readiness"`
}

// HostingSnapshot is captured under the resource and binding transaction locks.
// It is desired input, not evidence that an external workload exists.
type HostingSnapshot struct {
	ResourceID         string           `json:"resource_id"`
	ResourceGeneration int64            `json:"resource_generation"`
	ImageReference     string           `json:"image_reference"`
	BindingID          string           `json:"binding_id"`
	DestinationID      string           `json:"destination_id"`
	ServerID           string           `json:"server_id"`
	RunnerID           string           `json:"runner_id"`
	RuntimeNamespace   string           `json:"runtime_namespace"`
	ContainerPort      int              `json:"container_port"`
	HostPort           int              `json:"host_port"`
	Readiness          HostingReadiness `json:"readiness"`
	PolicyVersion      string           `json:"policy_version"`
}

// HostingRelease binds registry content and local image identity to one daemon.
type HostingRelease struct {
	CanonicalImage string `json:"canonical_image"`
	ImageID        string `json:"image_id"`
	OS             string `json:"os"`
	Architecture   string `json:"architecture"`
	Variant        string `json:"variant,omitempty"`
	DaemonID       string `json:"daemon_id"`
	Hash           string `json:"hash"`
}

// HostingHealthy records the last atomically settled healthy workload.
type HostingHealthy struct {
	Snapshot    HostingSnapshot `json:"snapshot"`
	OperationID string          `json:"operation_id"`
	ContainerID string          `json:"container_id"`
	Release     HostingRelease  `json:"release"`
	PlanHash    string          `json:"plan_hash"`
}

// HostingApplyPlan is compiled once from an immutable release and target.
type HostingApplyPlan struct {
	Snapshot        HostingSnapshot `json:"snapshot"`
	Release         HostingRelease  `json:"release"`
	PreviousHealthy *HostingHealthy `json:"previous_healthy,omitempty"`
	CompilerVersion string          `json:"compiler_version"`
	Hash            string          `json:"hash"`
}

// HostingReceipt acknowledges one stable, immutable runner document.
type HostingReceipt struct {
	ID        string    `json:"id"`
	BodyHash  string    `json:"body_hash"`
	CreatedAt time.Time `json:"created_at"`
}

// HostingResult is nonsecret durable evidence, never a command or arbitrary URL.
// Uncertain results retain the independent mutation hold.
type HostingResult struct {
	Outcome             string `json:"outcome"`
	ContainerID         string `json:"container_id,omitempty"`
	PreviousContainerID string `json:"previous_container_id,omitempty"`
	ImageID             string `json:"image_id,omitempty"`
	DaemonID            string `json:"daemon_id,omitempty"`
	PlanHash            string `json:"plan_hash,omitempty"`
	Ready               bool   `json:"ready"`
	Restored            bool   `json:"restored"`
	Uncertain           bool   `json:"uncertain"`
}

// HostingOperation exposes control state independently from task run terminal state.
type HostingOperation struct {
	ObservationLease      *RunLease         `json:"observation_lease,omitempty"`
	TaskMode              string            `json:"task_mode,omitempty"`
	ReconciliationRunID   string            `json:"reconciliation_run_id,omitempty"`
	ReconciliationHealthy *HostingHealthy   `json:"reconciliation_healthy,omitempty"`
	ID                    string            `json:"id"`
	ProjectID             string            `json:"project_id"`
	ResourceID            string            `json:"resource_id"`
	BindingID             string            `json:"binding_id"`
	RunID                 string            `json:"run_id,omitempty"`
	State                 string            `json:"state"`
	Snapshot              HostingSnapshot   `json:"snapshot"`
	Release               *HostingRelease   `json:"release,omitempty"`
	Plan                  *HostingApplyPlan `json:"plan,omitempty"`
	PreparedReceipt       *HostingReceipt   `json:"prepared_receipt,omitempty"`
	CompleteReceipt       *HostingReceipt   `json:"complete_receipt,omitempty"`
	Result                *HostingResult    `json:"result,omitempty"`
	RestoreOwnershipHeld  bool              `json:"restore_ownership_held"`
	MutationHold          bool              `json:"mutation_hold"`
	ReconcileRequested    bool              `json:"reconcile_requested"`
	CreatedBy             string            `json:"created_by"`
	CreatedAt             time.Time         `json:"created_at"`
	UpdatedAt             time.Time         `json:"updated_at"`
}

// HostingAuthority must match the authenticated runner and persisted task target.
type HostingAuthority struct {
	OperationID string `json:"operation_id"`
	RunID       string `json:"run_id"`
	LeaseID     string `json:"lease_id"`
	Attempt     int    `json:"attempt"`
	Fence       string `json:"fence"`
}

// HostingPreparedRequest binds a stable release receipt to exact lease authority.
type HostingPreparedRequest struct {
	HostingAuthority
	ReceiptID string         `json:"receipt_id"`
	Release   HostingRelease `json:"release"`
}

// HostingCompleteRequest binds a durable result receipt to exact lease authority.
type HostingCompleteRequest struct {
	HostingAuthority
	ReceiptID string        `json:"receipt_id"`
	Result    HostingResult `json:"result"`
}

// HostingObservation is read-only evidence compared against an already durable result.
// Observation alone never authorizes replaying a workload mutation.
type HostingObservation struct {
	NoOwnedContainers bool `json:"no_owned_containers"`
	HostingAuthority
	DaemonID    string `json:"daemon_id"`
	ContainerID string `json:"container_id"`
	ImageID     string `json:"image_id"`
	BindingID   string `json:"binding_id"`
	PlanHash    string `json:"plan_hash"`
	ReleaseHash string `json:"release_hash"`
	Ready       bool   `json:"ready"`
}

// ValidateHostingDeployment checks the bounded public deployment contract.
func ValidateHostingDeployment(in HostingDeploymentInput) error {
	if in.ExpectedGeneration < 1 || len(in.IdempotencyKey) < 1 || len(in.IdempotencyKey) > 128 || strings.ContainsFunc(in.IdempotencyKey, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return errors.New("expected_generation and a bounded idempotency_key are required")
	}
	if in.ContainerPort < 1 || in.ContainerPort > 65535 || in.HostPort < 1024 || in.HostPort > 65535 {
		return errors.New("invalid container or loopback host port")
	}
	p, err := url.ParseRequestURI(in.Readiness.Path)
	if err != nil || p.IsAbs() || p.Host != "" || !strings.HasPrefix(in.Readiness.Path, "/") || strings.HasPrefix(in.Readiness.Path, "//") || strings.ContainsAny(in.Readiness.Path, "#\\") || strings.ContainsFunc(in.Readiness.Path, unicode.IsControl) || len(in.Readiness.Path) > 1024 || in.Readiness.TimeoutSeconds < 1 || in.Readiness.TimeoutSeconds > 120 {
		return errors.New("readiness requires an origin-relative path and timeout of 1 to 120 seconds")
	}
	if strings.ContainsFunc(p.Path, unicode.IsControl) || strings.Contains(p.Path, "\\") {
		return errors.New("invalid readiness path")
	}
	return nil
}

// NormalizeHostingImage keeps the explicit-tag/digest contract while applying
// the maintained distribution grammar and Docker Hub repository normalization.
func NormalizeHostingImage(value string) (string, error) {
	if err := ValidateImageReference(value); err != nil {
		return "", err
	}
	named, err := reference.ParseNormalizedNamed(value)
	if err != nil {
		return "", errors.New("invalid container image reference")
	}
	return named.String(), nil
}

// ValidateHostingRelease checks canonical digest, platform and local daemon evidence.
func ValidateHostingRelease(release HostingRelease) error {
	named, err := reference.ParseNormalizedNamed(release.CanonicalImage)
	if err != nil {
		return errors.New("release requires canonical repository digest")
	}
	_, tagged := named.(reference.Tagged)
	digest, ok := named.(reference.Canonical)
	if !ok || tagged || digest.Digest().Algorithm().String() != "sha256" || named.String() != release.CanonicalImage || !imageSHA256.MatchString(release.ImageID) || release.OS != "linux" || release.Architecture == "" || len(release.Architecture) > 32 || len(release.Variant) > 32 || len(release.DaemonID) < 1 || len(release.DaemonID) > 128 || strings.ContainsFunc(release.DaemonID, unicode.IsSpace) {
		return errors.New("invalid immutable image release evidence")
	}
	expected := release
	expected.Hash = ""
	if release.Hash != HostingDocumentHash(expected) {
		return errors.New("release hash mismatch")
	}
	return nil
}

// HostingDocumentHash hashes the stable JSON representation of a typed document.
func HostingDocumentHash(value any) string {
	contents, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(contents)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// CompileHostingPlan hashes the immutable release, target and prior healthy pointer.
func CompileHostingPlan(snapshot HostingSnapshot, release HostingRelease, previous *HostingHealthy) (HostingApplyPlan, error) {
	if err := ValidateHostingRelease(release); err != nil {
		return HostingApplyPlan{}, err
	}
	if snapshot.PolicyVersion != HostingPolicyVersion {
		return HostingApplyPlan{}, errors.New("unsupported hosting policy")
	}
	plan := HostingApplyPlan{Snapshot: snapshot, Release: release, PreviousHealthy: previous, CompilerVersion: HostingCompilerVersion}
	plan.Hash = HostingDocumentHash(plan)
	return plan, nil
}

// HostingStatusRequest permits only the explicit live verification transition;
// an empty phase is a read of the exact persisted attempt, including after expiry.
type HostingStatusRequest struct {
	HostingAuthority
	Phase string `json:"phase,omitempty"`
}

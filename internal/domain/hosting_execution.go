package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
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
	// HostingReleasePortsCapability identifies agents that preserve retained releases.
	HostingReleasePortsCapability = "image_release_ports_v1"
	// HostingReleaseCompilerVersion identifies per-release port and retention plans.
	HostingReleaseCompilerVersion = "image-hosting.release-ports.v1"
	// HostingReleasePolicyVersion keeps per-release effects separate from legacy replacement.
	HostingReleasePolicyVersion = "public-image.release-ports.v1"
)

// ContainsHostingExecution inspects both accepted workflow representations for
// internal task types or authority fields. The iterative walk visits each nested
// workflow pointer once, without recursive traversal of untrusted graph depth.
func ContainsHostingExecution(spec RunSpec, workflow Workflow) bool {
	internal := func(spec RunSpec) bool {
		return strings.TrimSpace(spec.Type) == RunTypeDatabaseBackupRetention || strings.TrimSpace(spec.Type) == RunTypeDatabaseBackupTransfer || strings.TrimSpace(spec.Type) == RunTypeDatabaseBackup || strings.TrimSpace(spec.Type) == RunTypeDatabaseNetworkCapture || strings.TrimSpace(spec.Type) == RunTypeStorageArchive || strings.TrimSpace(spec.Type) == HostingBuildTaskType || spec.HostingBuildID != "" || strings.TrimSpace(spec.Type) == RunTypeLocalBuildDeploy || strings.TrimSpace(spec.Type) == RunTypeImageDeploy || strings.TrimSpace(spec.Type) == RunTypeDatabaseDeploy || strings.TrimSpace(spec.Type) == RunTypeProxyApply || spec.HostingOperationID != "" || spec.HostingMode != "" || spec.ProxyOperationID != "" || spec.ProxyMode != ""
	}
	if internal(spec) {
		return true
	}
	pending := []*Workflow{&workflow, spec.Workflow}
	seen := map[*Workflow]bool{}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == nil || seen[current] {
			continue
		}
		seen[current] = true
		for _, step := range current.Steps {
			if internal(step.RunSpec) {
				return true
			}
			pending = append(pending, step.RunSpec.Workflow)
		}
	}
	return false
}

// ValidateImageDeploymentTask checks canonical internal construction. Persisted
// target and lease authorization remain mandatory at claim and hosting endpoints.
func ValidateImageDeploymentTask(run TaskRun) error {
	spec := run.RunSpec
	if spec.Type != RunTypeImageDeploy || spec.HostingOperationID == "" || len(spec.HostingOperationID) > 128 || strings.TrimSpace(spec.HostingOperationID) != spec.HostingOperationID || spec.Process != nil || spec.Repository != nil || spec.Workflow != nil || len(spec.Inputs) != 0 || len(spec.Secrets) != 0 || len(spec.Artifacts) != 0 || len(run.Workflow.Steps) != 0 {
		return errors.New("invalid internal image deployment task")
	}
	prefix := "run_"
	if spec.HostingMode == "observe" {
		prefix = "observe_"
	} else if spec.HostingMode == "finalize" {
		prefix = "finalize_"
	} else if spec.HostingMode == "storage_recover" {
		prefix = "storage_recover_"
	} else if spec.HostingMode == HostingStackStopMode {
		prefix = "stack_stop_"
	} else if spec.HostingMode == "cleanup" {
		prefix = "cleanup_"
	} else if spec.HostingMode != "deploy" {
		return errors.New("invalid internal image deployment mode")
	}
	if run.ID != prefix+spec.HostingOperationID {
		return errors.New("image deployment task identity does not match operation")
	}
	return nil
}

// HostingReadiness is an origin-relative HTTP probe on the reserved local port.
type HostingReadiness struct {
	Path           string `json:"path"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// HostingDeploymentInput is the complete public image deployment request.
type HostingDeploymentInput struct {
	ManagedStack                           *HostingStackDeploymentInput                    `json:"managed_stack,omitempty"`
	ApplicationDatabaseStorage             *HostingApplicationDatabaseStorageInput         `json:"application_database_storage,omitempty"`
	ApplicationDatabaseRouteContinuity     *HostingApplicationDatabaseRouteContinuityInput `json:"application_database_route_continuity,omitempty"`
	StorageRouteContinuity                 *HostingStorageRouteContinuityInput             `json:"storage_route_continuity,omitempty"`
	ExpectedRuntimeConfigurationGeneration int64                                           `json:"expected_runtime_configuration_generation,omitempty"`
	ExpectedGeneration                     int64                                           `json:"expected_generation"`
	IdempotencyKey                         string                                          `json:"idempotency_key"`
	ContainerPort                          int                                             `json:"container_port"`
	HostPort                               int                                             `json:"host_port"`
	Readiness                              HostingReadiness                                `json:"readiness"`
}

// HostingSnapshot is captured under the resource and binding transaction locks.
// It is desired input, not evidence that an external workload exists.
type HostingSnapshot struct {
	StackStoppedOperationID string                           `json:"stack_stopped_operation_id,omitempty"`
	Stack                   HostingStackRef                  `json:"stack,omitzero"`
	Database                HostingDatabaseExecutionRef      `json:"database,omitzero"`
	StorageRouteContinuity  HostingStorageRouteContinuityRef `json:"storage_route_continuity,omitzero"`
	Storage                 HostingStorageRef                `json:"storage,omitzero"`
	ResourceID              string                           `json:"resource_id"`
	ResourceGeneration      int64                            `json:"resource_generation"`
	ImageReference          string                           `json:"image_reference"`
	BindingID               string                           `json:"binding_id"`
	DestinationID           string                           `json:"destination_id"`
	ServerID                string                           `json:"server_id"`
	RunnerID                string                           `json:"runner_id"`
	RuntimeNamespace        string                           `json:"runtime_namespace"`
	ContainerPort           int                              `json:"container_port"`
	HostPort                int                              `json:"host_port"`
	Readiness               HostingReadiness                 `json:"readiness"`
	PolicyVersion           string                           `json:"policy_version"`
	LocalBuild              HostingLocalBuildSource          `json:"local_build,omitzero"`
	RuntimeConfiguration    HostingRuntimeConfigurationRef   `json:"runtime_configuration,omitzero"`
	ApplicationDatabase     HostingApplicationDatabaseRef    `json:"application_database,omitzero"`
}

// HostingRelease binds registry content and local image identity to one daemon.
type HostingRelease struct {
	Stack          HostingStackReleaseRef `json:"stack,omitzero"`
	CanonicalImage string                 `json:"canonical_image"`
	ImageID        string                 `json:"image_id"`
	OS             string                 `json:"os"`
	Architecture   string                 `json:"architecture"`
	Variant        string                 `json:"variant,omitempty"`
	DaemonID       string                 `json:"daemon_id"`
	Hash           string                 `json:"hash"`
	LocalBuild     HostingBuildArtifact   `json:"local_build,omitzero"`
}

// HostingHealthy records the last atomically settled healthy workload.
type HostingHealthy struct {
	Stack       *HostingStackState     `json:"stack,omitempty"`
	Storage     *HostingHealthyStorage `json:"storage,omitempty"`
	Snapshot    HostingSnapshot        `json:"snapshot"`
	OperationID string                 `json:"operation_id"`
	ContainerID string                 `json:"container_id"`
	Release     HostingRelease         `json:"release"`
	PlanHash    string                 `json:"plan_hash"`
}

// HostingApplyPlan is compiled once from an immutable release and target.
type HostingApplyPlan struct {
	PreviousStopped     *HostingStackStopped            `json:"previous_stopped,omitempty"`
	Stack               *HostingStackPlan               `json:"stack,omitempty"`
	Database            *HostingDatabasePlan            `json:"database,omitempty"`
	Storage             *HostingStoragePlan             `json:"storage,omitempty"`
	RetainedReleases    []HostingHealthy                `json:"retained_releases,omitempty"`
	RetainPrevious      bool                            `json:"retain_previous,omitempty"`
	Snapshot            HostingSnapshot                 `json:"snapshot"`
	Release             HostingRelease                  `json:"release"`
	PreviousHealthy     *HostingHealthy                 `json:"previous_healthy,omitempty"`
	CompilerVersion     string                          `json:"compiler_version"`
	Hash                string                          `json:"hash"`
	ApplicationDatabase *HostingApplicationDatabasePlan `json:"application_database,omitempty"`
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
	StackStopped        bool               `json:"stack_stopped,omitempty"`
	Stack               *HostingStackState `json:"stack,omitempty"`
	Outcome             string             `json:"outcome"`
	ContainerID         string             `json:"container_id,omitempty"`
	PreviousContainerID string             `json:"previous_container_id,omitempty"`
	ImageID             string             `json:"image_id,omitempty"`
	DaemonID            string             `json:"daemon_id,omitempty"`
	PlanHash            string             `json:"plan_hash,omitempty"`
	Ready               bool               `json:"ready"`
	Restored            bool               `json:"restored"`
	Uncertain           bool               `json:"uncertain"`
}

// HostingOperation exposes control state independently from task run terminal state.
type HostingOperation struct {
	StackStopPlan                      *HostingStackStopPlan                      `json:"stack_stop_plan,omitempty"`
	StackSource                        *HostingStackDefinition                    `json:"stack_source,omitempty"`
	StackCreatePermitted               bool                                       `json:"stack_create_permitted,omitempty"`
	ApplicationDatabaseStorage         *HostingApplicationDatabaseStorage         `json:"application_database_storage,omitempty"`
	ApplicationDatabaseStorageState    *HostingApplicationDatabaseStorageState    `json:"application_database_storage_state,omitempty"`
	ApplicationDatabaseRouteContinuity *HostingApplicationDatabaseRouteContinuity `json:"application_database_route_continuity,omitempty"`
	ApplicationDatabaseYield           *HostingApplicationDatabaseYieldReceipt    `json:"application_database_yield,omitempty"`
	ApplicationDatabase                *HostingApplicationDatabaseExecutionProof  `json:"application_database,omitempty"`
	DatabaseMaintenance                *HostingDatabaseMaintenanceProof           `json:"database_maintenance,omitempty"`
	DatabaseUpgrade                    *HostingDatabaseUpgradeProof               `json:"database_upgrade,omitempty"`
	StorageArchive                     *HostingStorageArchive                     `json:"storage_archive,omitempty"`
	CleanupPlan                        *HostingCleanupPlan                        `json:"cleanup_plan,omitempty"`
	ObservationLease                   *RunLease                                  `json:"observation_lease,omitempty"`
	TaskMode                           string                                     `json:"task_mode,omitempty"`
	ReconciliationRunID                string                                     `json:"reconciliation_run_id,omitempty"`
	ReconciliationHealthy              *HostingHealthy                            `json:"reconciliation_healthy,omitempty"`
	ID                                 string                                     `json:"id"`
	ProjectID                          string                                     `json:"project_id"`
	ResourceID                         string                                     `json:"resource_id"`
	BindingID                          string                                     `json:"binding_id"`
	RunID                              string                                     `json:"run_id,omitempty"`
	State                              string                                     `json:"state"`
	Snapshot                           HostingSnapshot                            `json:"snapshot"`
	Release                            *HostingRelease                            `json:"release,omitempty"`
	Plan                               *HostingApplyPlan                          `json:"plan,omitempty"`
	PreparedReceipt                    *HostingReceipt                            `json:"prepared_receipt,omitempty"`
	CompleteReceipt                    *HostingReceipt                            `json:"complete_receipt,omitempty"`
	Result                             *HostingResult                             `json:"result,omitempty"`
	RestoreOwnershipHeld               bool                                       `json:"restore_ownership_held"`
	MutationHold                       bool                                       `json:"mutation_hold"`
	ReconcileRequested                 bool                                       `json:"reconcile_requested"`
	CreatedBy                          string                                     `json:"created_by"`
	CreatedAt                          time.Time                                  `json:"created_at"`
	UpdatedAt                          time.Time                                  `json:"updated_at"`
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
	StackImages []HostingStackImage `json:"stack_images,omitempty"`
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
	StackStopped      bool               `json:"stack_stopped,omitempty"`
	Stack             *HostingStackState `json:"stack,omitempty"`
	NoOwnedContainers bool               `json:"no_owned_containers"`
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
	if in.ManagedStack != nil && in.ManagedStack.ExpectedStoppedOperationID != "" && !hostingProxyID.MatchString(in.ManagedStack.ExpectedStoppedOperationID) {
		return errors.New("invalid stopped operation selection")
	}
	if in.ManagedStack != nil && (!in.ManagedStack.AcknowledgeInterruption || in.ApplicationDatabaseStorage != nil || in.ApplicationDatabaseRouteContinuity != nil) {
		return errors.New("managed stack requires explicit interrupting replacement acknowledgment")
	}

	if in.ApplicationDatabaseStorage != nil && (in.ApplicationDatabaseStorage.Validate() != nil || in.StorageRouteContinuity != nil) {
		return ErrInvalidHostingStorage
	}
	if in.ApplicationDatabaseRouteContinuity != nil {
		if _, err := CanonicalHostingApplicationDatabaseRouteContinuityInput(*in.ApplicationDatabaseRouteContinuity); err != nil || in.StorageRouteContinuity != nil {
			return errors.New("invalid application database route continuity request")
		}
	}
	if in.StorageRouteContinuity != nil {
		if _, err := CanonicalHostingStorageRouteContinuityInput(*in.StorageRouteContinuity); err != nil || in.HostPort < 1024 {
			return errors.New("invalid storage route continuity request")
		}
	}
	if in.ExpectedRuntimeConfigurationGeneration < 0 || in.ExpectedGeneration < 1 || len(in.IdempotencyKey) < 1 || len(in.IdempotencyKey) > 128 || strings.ContainsFunc(in.IdempotencyKey, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return errors.New("expected_generation and a bounded idempotency_key are required")
	}
	if in.ContainerPort < 1 || in.ContainerPort > 65535 || in.HostPort != 0 && in.HostPort < 1024 || in.HostPort > 65535 {
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
	if release.Stack != (HostingStackReleaseRef{}) {
		return validateStackRelease(release)
	}
	if release.LocalBuild != (HostingBuildArtifact{}) {
		return validateLocalBuildRelease(release)
	}
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
	if snapshot.Stack != (HostingStackRef{}) || release.Stack != (HostingStackReleaseRef{}) || previous != nil && previous.Stack != nil {
		return HostingApplyPlan{}, errors.New("stack requires versioned stack compiler")
	}
	if err := ValidateHostingStorageRouteContinuitySnapshot(snapshot); err != nil {
		return HostingApplyPlan{}, err
	}
	if err := ValidateHostingSnapshotRelease(snapshot, release); err != nil {
		return HostingApplyPlan{}, err
	}
	if snapshot.PolicyVersion != HostingPolicyVersion || snapshot.Storage != (HostingStorageRef{}) {
		return HostingApplyPlan{}, errors.New("unsupported hosting policy")
	}
	if previous != nil && (previous.Snapshot.ApplicationDatabase != (HostingApplicationDatabaseRef{}) || previous.Storage != nil || ValidateHostingHealthyStorage(*previous) != nil) {
		return HostingApplyPlan{}, ErrInvalidHostingStorage
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

// CompileRetainedHostingPlan binds all permitted running releases to one daemon.
// It does not authorize any of those containers for cleanup.
func CompileRetainedHostingPlan(snapshot HostingSnapshot, release HostingRelease, previous *HostingHealthy, retained []HostingHealthy) (HostingApplyPlan, error) {
	policy, compiler := HostingReleasePolicyVersion, HostingReleaseCompilerVersion
	if snapshot.RuntimeConfiguration != (HostingRuntimeConfigurationRef{}) {
		policy, compiler = HostingRuntimePolicyVersion, HostingRuntimeCompilerVersion
	}
	return compileRetainedHostingPlan(snapshot, release, previous, retained, policy, compiler)
}

func compileRetainedHostingPlan(snapshot HostingSnapshot, release HostingRelease, previous *HostingHealthy, retained []HostingHealthy, policy, compiler string) (HostingApplyPlan, error) {
	if snapshot.Stack != (HostingStackRef{}) || release.Stack != (HostingStackReleaseRef{}) || previous != nil && previous.Stack != nil {
		return HostingApplyPlan{}, errors.New("stack requires interrupting replacement")
	}
	if err := ValidateHostingStorageRouteContinuitySnapshot(snapshot); err != nil {
		return HostingApplyPlan{}, err
	}
	if ValidateHostingStorageSnapshot(snapshot) != nil || snapshot.Storage != (HostingStorageRef{}) && policy != HostingOwnedStoragePolicyVersion && policy != HostingOwnedStorageRuntimePolicyVersion && policy != HostingApplicationDatabaseStoragePolicyVersion {
		return HostingApplyPlan{}, ErrInvalidHostingStorage
	}
	if err := ValidateHostingSnapshotRelease(snapshot, release); err != nil {
		return HostingApplyPlan{}, err
	}
	if snapshot.PolicyVersion != policy || snapshot.HostPort < 1024 || snapshot.HostPort > 65535 || len(retained) > 32 {
		return HostingApplyPlan{}, errors.New("invalid retained release policy")
	}
	plan := HostingApplyPlan{Snapshot: snapshot, Release: release, PreviousHealthy: previous, CompilerVersion: compiler, RetainedReleases: append([]HostingHealthy(nil), retained...)}
	seen := map[string]bool{}
	for _, old := range plan.RetainedReleases {
		if policy != HostingApplicationDatabasePolicyVersion && policy != HostingApplicationDatabaseStoragePolicyVersion && old.Snapshot.ApplicationDatabase != (HostingApplicationDatabaseRef{}) || ValidateHostingHealthyStorage(old) != nil || policy != HostingOwnedStoragePolicyVersion && policy != HostingOwnedStorageRuntimePolicyVersion && policy != HostingApplicationDatabaseStoragePolicyVersion && old.Storage != nil {
			return HostingApplyPlan{}, ErrInvalidHostingStorage
		}
		if old.OperationID == "" || seen[old.OperationID] || old.Snapshot.HostPort < 1024 || old.Snapshot.HostPort > 65535 || old.Snapshot.BindingID != snapshot.BindingID || old.Snapshot.DestinationID != snapshot.DestinationID || old.Release.DaemonID != release.DaemonID || old.ContainerID == "" || ValidateHostingSnapshotRelease(old.Snapshot, old.Release) != nil {
			return HostingApplyPlan{}, errors.New("retained release identity mismatch")
		}
		seen[old.OperationID] = true
	}
	if previous != nil {
		if policy != HostingApplicationDatabasePolicyVersion && policy != HostingApplicationDatabaseStoragePolicyVersion && previous.Snapshot.ApplicationDatabase != (HostingApplicationDatabaseRef{}) || ValidateHostingHealthyStorage(*previous) != nil || policy != HostingOwnedStoragePolicyVersion && policy != HostingOwnedStorageRuntimePolicyVersion && policy != HostingApplicationDatabaseStoragePolicyVersion && previous.Storage != nil {
			return HostingApplyPlan{}, ErrInvalidHostingStorage
		}
		if ValidateHostingSnapshotRelease(previous.Snapshot, previous.Release) != nil || previous.OperationID == "" || previous.Snapshot.HostPort < 1024 || previous.Snapshot.HostPort > 65535 || previous.Snapshot.BindingID != snapshot.BindingID || previous.Snapshot.DestinationID != snapshot.DestinationID || previous.Release.DaemonID != release.DaemonID || previous.ContainerID == "" || seen[previous.OperationID] {
			return HostingApplyPlan{}, errors.New("previous release identity mismatch")
		}
		ownedPrevious := *previous
		if previous.Storage != nil {
			storage := *previous.Storage
			ownedPrevious.Storage = &storage
		}
		plan.PreviousHealthy = &ownedPrevious
		plan.RetainPrevious = previous.Snapshot.HostPort != snapshot.HostPort
	}
	for index := range plan.RetainedReleases {
		if plan.RetainedReleases[index].Storage != nil {
			storage := *plan.RetainedReleases[index].Storage
			plan.RetainedReleases[index].Storage = &storage
		}
	}
	sort.Slice(plan.RetainedReleases, func(i, j int) bool {
		return plan.RetainedReleases[i].OperationID < plan.RetainedReleases[j].OperationID
	})
	raw, err := json.Marshal(plan)
	if err != nil || len(raw) > 48<<10 {
		return HostingApplyPlan{}, errors.New("retained release plan exceeds journal limit")
	}
	plan.Hash = HostingDocumentHash(plan)
	return plan, nil
}

// HostingCleanupInput requests cleanup of a server-selected retained release.
// Container identities and ports are never supplied by the caller.
type HostingCleanupInput struct {
	ExpectedGeneration int64  `json:"expected_generation"`
	IdempotencyKey     string `json:"idempotency_key"`
}

// HostingRetainedRelease exposes one resource's retained release ownership.
type HostingRetainedRelease struct {
	Healthy            HostingHealthy `json:"healthy"`
	State              string         `json:"state"`
	RetainedRunning    bool           `json:"retained_running"`
	CleanupOperationID string         `json:"cleanup_operation_id,omitempty"`
}

// HostingCleanupPlan authorizes only one exact retained container and release.
type HostingCleanupPlan struct {
	Target                    HostingHealthy `json:"target"`
	CurrentHealthyOperationID string         `json:"current_healthy_operation_id"`
	CompilerVersion           string         `json:"compiler_version"`
	Hash                      string         `json:"hash"`
}

// CompileHostingCleanupPlan pins the cleanup target independently of current traffic.
func CompileHostingCleanupPlan(target HostingHealthy, current string) (HostingCleanupPlan, error) {
	if target.Snapshot.ApplicationDatabase != (HostingApplicationDatabaseRef{}) || target.OperationID == "" || target.OperationID == current || current == "" || target.ContainerID == "" || target.Snapshot.HostPort < 1024 || target.Snapshot.HostPort > 65535 || ValidateHostingSnapshotRelease(target.Snapshot, target.Release) != nil {
		return HostingCleanupPlan{}, errors.New("invalid retained cleanup target")
	}
	compiler := HostingReleaseCompilerVersion
	if target.Snapshot.LocalBuild != (HostingLocalBuildSource{}) {
		compiler = HostingLocalBuildCompilerVersion
	}
	plan := HostingCleanupPlan{Target: target, CurrentHealthyOperationID: current, CompilerVersion: compiler}
	plan.Hash = HostingDocumentHash(plan)
	return plan, nil
}

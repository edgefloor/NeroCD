package domain

import (
	"errors"
	"sort"
	"strings"
	"unicode"

	"github.com/distribution/reference"
)

// Managed stack protocol versions identify stack-wide authority and observations.
const (
	HostingStackCapability      = "hosting_managed_compose_v1"
	HostingStackPolicyVersion   = "managed-compose.interrupting.v1"
	HostingStackCompilerVersion = "managed-compose.compiler.v1"
)

// HostingStackDeploymentInput acknowledges the stack-wide interrupting replacement.
type HostingStackDeploymentInput struct {
	AcknowledgeInterruption    bool   `json:"acknowledge_interruption"`
	ExpectedStoppedOperationID string `json:"expected_stopped_operation_id,omitempty"`
}

// HostingStackRef selects one immutable source normalization.
type HostingStackRef struct {
	Generation int64  `json:"generation"`
	Hash       string `json:"hash"`
}

// HostingStackReleaseRef commits the complete sorted image set without making it a single image.
type HostingStackReleaseRef struct {
	Hash string `json:"hash"`
}

// HostingStackImage is the resolved immutable release of one named component.
type HostingStackImage struct {
	Component string         `json:"component"`
	Release   HostingRelease `json:"release"`
}

// HostingStackVolume preserves the original creation identity across source revisions.
type HostingStackVolume struct {
	Name                string `json:"name"`
	VolumeName          string `json:"volume_name"`
	CreationOperationID string `json:"creation_operation_id"`
	CreationNonce       string `json:"creation_nonce"`
	CreatedAt           string `json:"created_at,omitempty"`
}

// HostingStackPlan freezes all nonsecret inputs before any external mutation.
type HostingStackPlan struct {
	Definition  HostingStackDefinition `json:"definition"`
	Images      []HostingStackImage    `json:"images"`
	Volumes     []HostingStackVolume   `json:"volumes"`
	NetworkName string                 `json:"network_name"`
}

// HostingStackComponent records actual state, including unsuccessful and completed init containers.
type HostingStackComponent struct {
	Component   string `json:"component"`
	ContainerID string `json:"container_id"`
	ImageID     string `json:"image_id"`
	OperationID string `json:"operation_id"`
	Running     bool   `json:"running"`
	ExitCode    int    `json:"exit_code"`
	Health      string `json:"health"`
}

// HostingStackState is a whole-stack receipt, not a selected container success claim.
type HostingStackState struct {
	ObservationComplete bool                    `json:"observation_complete"`
	PublicComponent     string                  `json:"public_component"`
	NetworkID           string                  `json:"network_id"`
	Components          []HostingStackComponent `json:"components"`
	Volumes             []HostingStackVolume    `json:"volumes"`
}

// HostingStackVolumeRequest seals creation evidence before any stack writer starts.
type HostingStackVolumeRequest struct {
	HostingAuthority
	PlanHash string               `json:"plan_hash"`
	Volumes  []HostingStackVolume `json:"volumes"`
}

func validateStackRelease(release HostingRelease) error {
	if !imageSHA256.MatchString(release.Stack.Hash) || release.CanonicalImage != "" || release.ImageID != "" || release.OS != "" || release.Architecture != "" || release.Variant != "" || release.LocalBuild != (HostingBuildArtifact{}) || release.DaemonID == "" || len(release.DaemonID) > 128 || strings.ContainsFunc(release.DaemonID, unicode.IsSpace) {
		return errors.New("invalid stack release")
	}
	expected := release
	expected.Hash = ""
	if release.Hash != HostingDocumentHash(expected) {
		return errors.New("stack release hash mismatch")
	}
	return nil
}

// ValidateHostingStackImages checks complete ordering, source repositories and immutable digests.
func ValidateHostingStackImages(definition HostingStackDefinition, daemon string, images []HostingStackImage) error {
	if len(images) == 0 || len(images) > 16 || len(images) != len(definition.Services) {
		return errors.New("stack image set is incomplete")
	}
	for i, image := range images {
		if image.Component != definition.Services[i].Name || i > 0 && images[i-1].Component >= image.Component || image.Release.Stack != (HostingStackReleaseRef{}) || image.Release.LocalBuild != (HostingBuildArtifact{}) || image.Release.DaemonID != daemon || ValidateHostingRelease(image.Release) != nil {
			return errors.New("stack component release changed")
		}
		source, err := reference.ParseNormalizedNamed(definition.Services[i].Image)
		if err != nil {
			return errors.New("invalid stack image source")
		}
		resolved, err := reference.ParseNormalizedNamed(image.Release.CanonicalImage)
		if err != nil || reference.TrimNamed(source).String() != reference.TrimNamed(resolved).String() {
			return errors.New("stack image repository changed")
		}
		if requested, ok := source.(reference.Digested); ok {
			actual, ok := resolved.(reference.Digested)
			if !ok || requested.Digest() != actual.Digest() {
				return errors.New("stack image digest changed")
			}
		}
	}
	return nil
}

// CompileHostingStackPlan hashes the whole managed stack under its destination authority.
func CompileHostingStackPlan(snapshot HostingSnapshot, release HostingRelease, previous *HostingHealthy, stack HostingStackPlan, stopped ...*HostingStackStopped) (HostingApplyPlan, error) {
	var zero HostingApplyPlan
	if snapshot.PolicyVersion != HostingStackPolicyVersion || snapshot.Stack.Generation != snapshot.ResourceGeneration || snapshot.Stack.Generation < 1 || snapshot.Stack.Hash != HostingDocumentHash(stack.Definition) || snapshot.ImageReference != "" || snapshot.LocalBuild != (HostingLocalBuildSource{}) || snapshot.Database != (HostingDatabaseExecutionRef{}) || snapshot.Storage != (HostingStorageRef{}) || snapshot.ApplicationDatabase != (HostingApplicationDatabaseRef{}) || snapshot.ContainerPort != stack.Definition.ContainerPort || snapshot.HostPort < 1024 || snapshot.HostPort > 65535 || ValidateHostingRuntimeConfigurationRef(snapshot) != nil || ValidateHostingStorageRouteContinuitySnapshot(snapshot) != nil {
		return zero, errors.New("invalid stack snapshot")
	}
	if stack.Definition.Version != HostingStackVersion || !imageSHA256.MatchString(stack.Definition.OriginalHash) || release.Stack.Hash != HostingDocumentHash(stack.Images) || validateStackRelease(release) != nil || ValidateHostingStackImages(stack.Definition, release.DaemonID, stack.Images) != nil {
		return zero, errors.New("invalid stack image set")
	}
	if stack.NetworkName != HostingStackObjectID(snapshot.BindingID, "network", "private") || len(stack.Volumes) != len(stack.Definition.Volumes) || len(stack.Volumes) > 32 {
		return zero, errors.New("stack ownership changed")
	}
	for i, volume := range stack.Volumes {
		if volume.Name != stack.Definition.Volumes[i] || volume.VolumeName != HostingStackObjectID(snapshot.BindingID, "volume", volume.Name) || !hostingProxyID.MatchString(volume.CreationOperationID) || !hostingStorageRequest.MatchString(volume.CreationNonce) {
			return zero, errors.New("invalid stack volume")
		}
	}
	if previous != nil {
		if ValidateHostingStackHealthy(*previous) != nil || previous.Snapshot.BindingID != snapshot.BindingID || previous.Snapshot.DestinationID != snapshot.DestinationID || previous.Snapshot.HostPort != snapshot.HostPort || previous.Release.DaemonID != release.DaemonID {
			return zero, errors.New("stack predecessor changed")
		}
		for _, old := range previous.Stack.Volumes {
			for _, volume := range stack.Volumes {
				if old.Name == volume.Name && old != volume {
					return zero, errors.New("stack volume origin changed")
				}
			}
		}
	}
	plan := HostingApplyPlan{Snapshot: snapshot, Release: release, PreviousHealthy: previous, CompilerVersion: HostingStackCompilerVersion, Stack: &stack}
	if len(stopped) > 1 {
		return zero, errors.New("invalid stopped predecessor")
	}
	if len(stopped) == 1 && stopped[0] != nil {
		stop := stopped[0]
		if stop.Validate() != nil || previous == nil || snapshot.StackStoppedOperationID != stop.StopOperationID || HostingDocumentHash(stop.Target) != HostingDocumentHash(previous) {
			return zero, errors.New("stopped predecessor changed")
		}
		plan.PreviousStopped = stop
	} else if snapshot.StackStoppedOperationID != "" {
		return zero, errors.New("stopped predecessor proof missing")
	}

	plan.Hash = HostingDocumentHash(plan)
	return plan, nil
}

// ValidateHostingStackHealthy rejects mixed legacy and stack healthy identities.
func ValidateHostingStackHealthy(healthy HostingHealthy) error {
	if healthy.Stack == nil || !healthy.Stack.ObservationComplete || healthy.Snapshot.Stack.Generation < 1 || healthy.Snapshot.PolicyVersion != HostingStackPolicyVersion || healthy.ContainerID != "" || healthy.Storage != nil || healthy.OperationID == "" || !imageSHA256.MatchString(healthy.PlanHash) || validateStackRelease(healthy.Release) != nil || !stackName.MatchString(healthy.Stack.PublicComponent) || !imageSHA256.MatchString("sha256:"+healthy.Stack.NetworkID) || len(healthy.Stack.Components) == 0 || len(healthy.Stack.Components) > 16 || len(healthy.Stack.Volumes) > 32 {
		return errors.New("invalid healthy stack")
	}
	seen := map[string]bool{}
	public := false
	for _, component := range healthy.Stack.Components {
		if !stackName.MatchString(component.Component) || seen[component.Component] || component.OperationID != healthy.OperationID || !imageSHA256.MatchString("sha256:"+component.ContainerID) || !imageSHA256.MatchString(component.ImageID) {
			return errors.New("invalid healthy stack component")
		}
		seen[component.Component] = true
		if component.Component == healthy.Stack.PublicComponent {
			public = component.Running
		}
	}
	if !public {
		return errors.New("healthy stack lacks public component")
	}
	return nil
}

// ValidateHostingStackState requires every configured component and dependency outcome for success.
func ValidateHostingStackState(plan HostingApplyPlan, operation string, state HostingStackState, healthy bool) error {
	if state.NetworkID != "" && !imageSHA256.MatchString("sha256:"+state.NetworkID) || healthy && !state.ObservationComplete {
		return errors.New("stack observation is incomplete")
	}
	if plan.Stack == nil || state.PublicComponent != plan.Stack.Definition.PublicComponent || len(state.Components) > 32 || len(state.Volumes) != len(plan.Stack.Volumes) {
		return errors.New("stack observation shape changed")
	}
	for i, volume := range state.Volumes {
		expected := plan.Stack.Volumes[i]
		if expected.CreatedAt == "" {
			expected.CreatedAt = volume.CreatedAt
		}
		if volume != expected || healthy && volume.CreatedAt == "" {
			return errors.New("stack volume observation changed")
		}
	}
	seen := map[string]bool{}
	for _, component := range state.Components {
		if !stackName.MatchString(component.Component) || !imageSHA256.MatchString("sha256:"+component.ContainerID) || !imageSHA256.MatchString(component.ImageID) || component.OperationID != "" && !hostingProxyID.MatchString(component.OperationID) || component.Health != "" && component.Health != "starting" && component.Health != "healthy" && component.Health != "unhealthy" {
			return errors.New("invalid component observation")
		}
		if !healthy {
			continue
		}
		index := sort.Search(len(plan.Stack.Images), func(i int) bool { return plan.Stack.Images[i].Component >= component.Component })
		if index == len(plan.Stack.Images) || plan.Stack.Images[index].Component != component.Component || seen[component.Component] || component.OperationID != operation || component.ImageID != plan.Stack.Images[index].Release.ImageID {
			return errors.New("stack component changed")
		}
		seen[component.Component] = true
		service := plan.Stack.Definition.Services[index]
		init := false
		for _, consumer := range plan.Stack.Definition.Services {
			init = init || consumer.DependsOn[component.Component] == "service_completed_successfully"
		}
		if init {
			if component.Running || component.ExitCode != 0 {
				return errors.New("stack init job did not complete")
			}
		} else if !component.Running || service.Healthcheck != nil && component.Health != "healthy" {
			return errors.New("stack component is not healthy")
		}
	}
	if healthy && len(seen) != len(plan.Stack.Definition.Services) {
		return errors.New("stack observation is incomplete")
	}
	return nil
}

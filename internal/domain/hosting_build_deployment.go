package domain

import (
	"errors"
	"strings"
	"unicode"
)

const (
	// RunTypeLocalBuildDeploy requires agents that understand local artifact plans.
	RunTypeLocalBuildDeploy = "local_build_deploy_v1"
	// HostingLocalBuildPolicyVersion preserves local images and per-release ports.
	HostingLocalBuildPolicyVersion = "local-build.release-ports.v1"
	// HostingLocalBuildCompilerVersion identifies immutable local artifact plans.
	HostingLocalBuildCompilerVersion = "local-build-hosting.v1"
)

// HostingBuildDeploymentInput selects only a server-owned successful build.
type HostingBuildDeploymentInput struct {
	BuildID                  string           `json:"build_id"`
	ExpectedSourceGeneration int64            `json:"expected_source_generation"`
	ExpectedGeneration       int64            `json:"expected_generation"`
	IdempotencyKey           string           `json:"idempotency_key"`
	ContainerPort            int              `json:"container_port"`
	HostPort                 int              `json:"host_port"`
	Readiness                HostingReadiness `json:"readiness"`
}

// DeploymentInput returns the shared bounded port and readiness input.
func (in HostingBuildDeploymentInput) DeploymentInput() HostingDeploymentInput {
	return HostingDeploymentInput{ExpectedGeneration: in.ExpectedGeneration, IdempotencyKey: in.IdempotencyKey, ContainerPort: in.ContainerPort, HostPort: in.HostPort, Readiness: in.Readiness}
}

// ValidateHostingBuildDeployment rejects caller authority and incomplete selection.
func ValidateHostingBuildDeployment(in HostingBuildDeploymentInput) error {
	if in.BuildID == "" || len(in.BuildID) > 128 || strings.ContainsFunc(in.BuildID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) || in.ExpectedSourceGeneration < 1 {
		return errors.New("build_id and expected_source_generation are required")
	}
	return ValidateHostingDeployment(in.DeploymentInput())
}

// HostingLocalBuildSource binds the admitted artifact to its immutable source version.
// All fields are comparable so a runner cannot substitute any artifact identity.
type HostingLocalBuildSource struct {
	SourceGeneration int64                `json:"source_generation"`
	ArtifactHash     string               `json:"artifact_hash"`
	Artifact         HostingBuildArtifact `json:"artifact"`
}

func validateLocalArtifact(a HostingBuildArtifact) error {
	if a.Origin != "local_build" || a.BuildID == "" || len(a.BuildID) > 128 || a.SourceID == "" || a.InputID == "" || len(a.DaemonID) < 1 || len(a.DaemonID) > 128 || strings.ContainsFunc(a.DaemonID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) || (a.Platform != "linux/amd64" && a.Platform != "linux/arm64") || (a.ContextPolicy != "" && a.ContextPolicy != "static-assets-v1") {
		return errors.New("invalid local build artifact")
	}
	for _, d := range []string{a.SourceReceiptHash, a.ContextDigest, a.RequestDigest, a.ImageID, a.IndexDigest, a.ManifestDigest, a.ConfigDigest} {
		if !imageSHA256.MatchString(d) {
			return errors.New("invalid local build digest")
		}
	}
	if a.ImageID != a.IndexDigest && a.ImageID != a.ManifestDigest && a.ImageID != a.ConfigDigest {
		return errors.New("unrelated local image identity")
	}
	return nil
}

// NewHostingLocalBuildRelease derives immutable release evidence without a registry digest.
// The store additionally validates the artifact against its persisted successful build.
func NewHostingLocalBuildRelease(a HostingBuildArtifact) (HostingRelease, error) {
	if err := validateLocalArtifact(a); err != nil {
		return HostingRelease{}, err
	}
	platform := strings.Split(a.Platform, "/")
	release := HostingRelease{ImageID: a.ImageID, OS: platform[0], Architecture: platform[1], DaemonID: a.DaemonID, LocalBuild: a}
	release.Hash = HostingDocumentHash(release)
	return release, nil
}

func validateLocalBuildRelease(r HostingRelease) error {
	expected, err := NewHostingLocalBuildRelease(r.LocalBuild)
	if err != nil {
		return err
	}
	if r != expected {
		return errors.New("local release does not match admitted artifact")
	}
	return nil
}

// ValidateHostingSnapshotRelease enforces the exclusive registry/local union.
func ValidateHostingSnapshotRelease(s HostingSnapshot, r HostingRelease) error {
	if err := ValidateHostingRelease(r); err != nil {
		return err
	}
	if s.LocalBuild == (HostingLocalBuildSource{}) {
		if r.LocalBuild != (HostingBuildArtifact{}) {
			return errors.New("mixed hosting release origins")
		}
		return nil
	}
	if s.ImageReference != "" || s.PolicyVersion != HostingLocalBuildPolicyVersion || s.LocalBuild.SourceGeneration < 1 || s.LocalBuild.Artifact != r.LocalBuild || s.LocalBuild.ArtifactHash != HostingDocumentHash(r.LocalBuild) {
		return errors.New("local snapshot does not match admitted artifact")
	}
	return nil
}

// CompileLocalBuildHostingPlan retains the same bounded port and recovery semantics.
func CompileLocalBuildHostingPlan(s HostingSnapshot, r HostingRelease, previous *HostingHealthy, retained []HostingHealthy) (HostingApplyPlan, error) {
	if s.LocalBuild == (HostingLocalBuildSource{}) {
		return HostingApplyPlan{}, errors.New("local build source required")
	}
	return compileRetainedHostingPlan(s, r, previous, retained, HostingLocalBuildPolicyVersion, HostingLocalBuildCompilerVersion)
}

// ValidateLocalBuildDeploymentTask permits only canonical internal operation tasks.
func ValidateLocalBuildDeploymentTask(run TaskRun) error {
	if run.RunSpec.Type != RunTypeLocalBuildDeploy || run.RunSpec.HostingBuildID != "" || run.RunSpec.ProxyOperationID != "" || run.RunSpec.ProxyMode != "" {
		return errors.New("invalid internal local build deployment task")
	}
	// The image task validator checks the shared authority fields and excludes all
	// generic execution material; changing this local copy never changes the task.
	run.RunSpec.Type = RunTypeImageDeploy
	return ValidateImageDeploymentTask(run)
}

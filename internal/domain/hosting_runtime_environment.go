package domain

import "errors"

// HostingRuntimeEnvironmentCapability and the policy and compiler versions below
// identify deployment admission that pins saved runtime configuration.
const (
	HostingRuntimeEnvironmentCapability     = "hosting_runtime_env_v1"
	HostingRuntimePolicyVersion             = "public-image.runtime-env.release-ports.v1"
	HostingRuntimeCompilerVersion           = "image-hosting.runtime-env.v1"
	HostingLocalBuildRuntimePolicyVersion   = "local-build.runtime-env.release-ports.v1"
	HostingLocalBuildRuntimeCompilerVersion = "local-build-hosting.runtime-env.v1"
)

// HostingRuntimeConfigurationRef pins an immutable public document without its values.
type HostingRuntimeConfigurationRef struct {
	Generation int64  `json:"generation"`
	Hash       string `json:"hash"`
}

// ValidateHostingRuntimeConfigurationRef binds runtime references to their policy family.
func ValidateHostingRuntimeConfigurationRef(s HostingSnapshot) error {
	ref := s.RuntimeConfiguration
	runtimePolicy := s.PolicyVersion == HostingRuntimePolicyVersion || s.PolicyVersion == HostingLocalBuildRuntimePolicyVersion
	if ref == (HostingRuntimeConfigurationRef{}) {
		if runtimePolicy {
			return errors.New("runtime policy requires configuration reference")
		}
		return nil
	}
	if ref.Generation < 1 || !imageSHA256.MatchString(ref.Hash) || !runtimePolicy || (s.LocalBuild == (HostingLocalBuildSource{})) != (s.PolicyVersion == HostingRuntimePolicyVersion) {
		return errors.New("invalid runtime configuration reference policy")
	}
	return nil
}

// HostingPlanFamilyMatches rejects mixed policy/compiler families and substituted snapshots.
func HostingPlanFamilyMatches(s HostingSnapshot, p HostingApplyPlan) bool {
	if s.Database != (HostingDatabaseExecutionRef{}) || p.Database != nil || s.PolicyVersion == HostingDatabasePolicyVersion {
		return p.Snapshot == s && ValidateHostingDatabasePlan(p) == nil
	}
	if ValidateHostingRuntimeConfigurationRef(s) != nil || ValidateHostingStorageSnapshot(s) != nil || p.Snapshot != s {
		return false
	}
	if s.Storage == (HostingStorageRef{}) && p.Storage != nil {
		return false
	}
	if s.Storage != (HostingStorageRef{}) && (p.Storage == nil || p.Storage.Validate(s, p.Release) != nil) {
		return false
	}
	if p.PreviousHealthy != nil && (p.PreviousHealthy.Snapshot.Database != (HostingDatabaseExecutionRef{}) || ValidateHostingHealthyStorage(*p.PreviousHealthy) != nil || s.Storage == (HostingStorageRef{}) && p.PreviousHealthy.Storage != nil) {
		return false
	}
	for _, healthy := range p.RetainedReleases {
		if healthy.Snapshot.Database != (HostingDatabaseExecutionRef{}) || ValidateHostingHealthyStorage(healthy) != nil || s.Storage == (HostingStorageRef{}) && healthy.Storage != nil {
			return false
		}
	}
	if s.Storage != (HostingStorageRef{}) && p.RetainPrevious {
		return false
	}
	compiler := ""
	switch s.PolicyVersion {
	case HostingOwnedStoragePolicyVersion:
		compiler = HostingOwnedStorageCompilerVersion
	case HostingPolicyVersion:
		compiler = HostingCompilerVersion
	case HostingReleasePolicyVersion:
		compiler = HostingReleaseCompilerVersion
	case HostingLocalBuildPolicyVersion:
		compiler = HostingLocalBuildCompilerVersion
	case HostingRuntimePolicyVersion:
		compiler = HostingRuntimeCompilerVersion
	case HostingLocalBuildRuntimePolicyVersion:
		compiler = HostingLocalBuildRuntimeCompilerVersion
	}
	return compiler != "" && p.CompilerVersion == compiler
}

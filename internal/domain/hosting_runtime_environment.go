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
	if ValidateHostingRuntimeConfigurationRef(s) != nil || p.Snapshot != s {
		return false
	}
	compiler := ""
	switch s.PolicyVersion {
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

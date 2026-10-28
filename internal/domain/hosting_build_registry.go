package domain

// HostingBuildRegistryCapability identifies isolated build registry authentication.
const HostingBuildRegistryCapability = "hosting_build_registry_v1"

// HostingBuildRegistryCredentialRequest requests the immutable build-selected credential.
// It cannot choose a secret or registry independently of the saved source.
type HostingBuildRegistryCredentialRequest struct {
	Authority  HostingBuildAuthority `json:"authority"`
	DeliveryID string                `json:"delivery_id"`
}

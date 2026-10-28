package domain

import "errors"

// HostingBuildSecretsCapability identifies managed BuildKit secret delivery.
const HostingBuildSecretsCapability = "hosting_build_secrets_v1"

// HostingBuildSecretReference names an immutable managed secret for RUN environments.
// It contains no protected value and is safe in saved build metadata.
type HostingBuildSecretReference struct {
	Name   string                       `json:"name"`
	Secret HostingRegistryCredentialRef `json:"secret"`
}

// UnmarshalJSON rejects unknown fields, invalid names, and missing secret versions.
func (r *HostingBuildSecretReference) UnmarshalJSON(raw []byte) error {
	type wire HostingBuildSecretReference
	var value wire
	if DecodeHostingStorageProtocolJSON(raw, &value) != nil || !validHostingBuildArgName(value.Name) || value.Secret.Validate() != nil {
		return errors.New("invalid build secret reference")
	}
	*r = HostingBuildSecretReference(value)
	return nil
}

// HostingBuildSecretsRequest requests only snapshot-selected managed build secrets.
type HostingBuildSecretsRequest struct {
	Authority  HostingBuildAuthority `json:"authority"`
	DeliveryID string                `json:"delivery_id"`
}

func validateHostingBuildSecrets(s HostingBuildSettings) error {
	if len(s.BuildSecrets) > 64 || len(s.BuildSecrets) > 0 && s.Strategy != "dockerfile" && s.Strategy != "compose" {
		return errors.New("build secrets require Dockerfile or Compose strategy and at most 64 references")
	}
	names := map[string]bool{}
	for _, variable := range s.BuildVariables {
		names[variable.Name] = true
	}
	for _, ref := range s.BuildSecrets {
		if !validHostingBuildArgName(ref.Name) || ref.Secret.Validate() != nil || names[ref.Name] {
			return errors.New("invalid, duplicate or public variable colliding build secret")
		}
		names[ref.Name] = true
	}
	return nil
}

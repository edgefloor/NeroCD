package domain

import "slices"

// HostingCatalogRequirements describes known deployment needs without changing a saved template.
type HostingCatalogRequirements struct {
	SourceKind               string   `json:"source_kind"`
	RequiredCapabilities     []string `json:"required_capabilities"`
	ArchitectureRestriction  string   `json:"architecture_restriction"`
	ArchitectureVerification string   `json:"architecture_verification"`
	CPU                      string   `json:"cpu"`
	Memory                   string   `json:"memory"`
	Disk                     string   `json:"disk"`
	MountPaths               []string `json:"mount_paths"`
	PublicURLRequired        bool     `json:"public_url_required"`
	ManagedCredentialNames   []string `json:"managed_credential_names"`
}

// CatalogRequirements derives only declarations and existing runtime feature requirements.
func CatalogRequirements(t HostingCatalogTemplate) HostingCatalogRequirements {
	r := HostingCatalogRequirements{SourceKind: "image", RequiredCapabilities: []string{RunTypeImageDeploy, HostingOwnedStorageCapability, HostingOwnedStorageRuntimeCapability, HostingRuntimeEnvironmentCapability, HostingReleasePortsCapability}, ArchitectureRestriction: "undeclared", ArchitectureVerification: "unknown", CPU: "undeclared", Memory: "undeclared", Disk: "undeclared", MountPaths: []string{}, ManagedCredentialNames: append([]string{}, t.Definition.CredentialNames...)}
	if t.AMDOnly {
		r.ArchitectureRestriction = "amd64"
	}
	if t.ARMOnly {
		r.ArchitectureRestriction = "arm64"
	}
	if t.Definition.Compose != nil {
		r.SourceKind = "compose"
		r.PublicURLRequired = true
		r.RequiredCapabilities = []string{RunTypeImageDeploy, HostingStackCapability, HostingRuntimeEnvironmentCapability, HostingReleasePortsCapability}
		if stack, err := ParseHostingStack(*t.Definition.Compose); err == nil {
			for _, service := range stack.Services {
				for _, mount := range service.Mounts {
					if !slices.Contains(r.MountPaths, mount.Target) {
						r.MountPaths = append(r.MountPaths, mount.Target)
					}
				}
			}
		}
	} else if t.Definition.MountPath != "" {
		r.MountPaths = append(r.MountPaths, t.Definition.MountPath)
	}
	return r
}

// HostingCatalogDestinationOption exposes only grant-scoped placement information.
type HostingCatalogDestinationOption struct {
	Destination         Destination `json:"destination"`
	ServerName          string      `json:"server_name"`
	Available           bool        `json:"available"`
	ReasonCode          string      `json:"reason_code"`
	MissingCapabilities []string    `json:"missing_capabilities"`
}

// HostingCatalogDestinationOptions is a bounded projection, never execution authority.
type HostingCatalogDestinationOptions struct {
	TemplateID       string                            `json:"template_id"`
	ImmutableVersion string                            `json:"immutable_version"`
	Requirements     HostingCatalogRequirements        `json:"requirements"`
	Items            []HostingCatalogDestinationOption `json:"items"`
	Total            int                               `json:"total"`
	Limit            int                               `json:"limit"`
	Offset           int                               `json:"offset"`
}

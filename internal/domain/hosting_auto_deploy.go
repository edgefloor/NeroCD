package domain

import (
	"errors"
	"time"
)

// HostingAutoDeployMode delegates only unprotected deployment approval policy.
// It makes no claim about GitHub branch-protection configuration.
const HostingAutoDeployMode = "automatic_unprotected"

// HostingAutoDeployConfiguration explicitly reviews an existing healthy app and
// existing HTTPS routes. Host ports are always allocated by existing admission.
type HostingAutoDeployConfiguration struct {
	Mode                                   string                        `json:"mode"`
	ExpectedHealthyOperationID             string                        `json:"expected_healthy_operation_id"`
	ExpectedLoadedProxyOperationID         string                        `json:"expected_loaded_proxy_operation_id"`
	ExpectedLoadedGeneration               int64                         `json:"expected_loaded_generation"`
	ExpectedRuntimeConfigurationGeneration int64                         `json:"expected_runtime_configuration_generation"`
	ContainerPort                          int                           `json:"container_port"`
	Readiness                              HostingReadiness              `json:"readiness"`
	Routes                                 []HostingRouteSwitchSelection `json:"routes"`
}

// CanonicalHostingAutoDeployConfiguration validates and copies reviewed selectors.
func CanonicalHostingAutoDeployConfiguration(in HostingAutoDeployConfiguration) (HostingAutoDeployConfiguration, error) {
	if in.Mode != HostingAutoDeployMode || !hostingProxyID.MatchString(in.ExpectedHealthyOperationID) || !hostingProxyID.MatchString(in.ExpectedLoadedProxyOperationID) || in.ExpectedRuntimeConfigurationGeneration < 0 {
		return HostingAutoDeployConfiguration{}, errors.New("invalid automatic deployment configuration")
	}
	if err := ValidateHostingDeployment(HostingDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "automatic", ContainerPort: in.ContainerPort, HostPort: 0, Readiness: in.Readiness, ExpectedRuntimeConfigurationGeneration: in.ExpectedRuntimeConfigurationGeneration}); err != nil {
		return HostingAutoDeployConfiguration{}, err
	}
	route, err := CanonicalHostingRouteSwitchInput(HostingRouteSwitchInput{ExpectedLoadedGeneration: in.ExpectedLoadedGeneration, TargetReleaseOperationID: in.ExpectedHealthyOperationID, Routes: in.Routes})
	if err != nil {
		return HostingAutoDeployConfiguration{}, err
	}
	in.Routes = route.Routes
	return in, nil
}

// HostingAutoDeployStatus is viewer-safe current demand, not execution authority.
type HostingAutoDeployStatus struct {
	Mode                     string     `json:"mode"`
	State                    string     `json:"state"`
	Reason                   string     `json:"reason"`
	ManualEpoch              int64      `json:"manual_epoch"`
	DesiredCommit            string     `json:"desired_commit"`
	BuildID                  string     `json:"build_id"`
	DeploymentOperationID    string     `json:"deployment_operation_id"`
	SwitchOperationID        string     `json:"switch_operation_id"`
	Attempts                 int        `json:"attempts"`
	ExpiresAt                *time.Time `json:"expires_at,omitempty"`
	ObservedLoadedGeneration int64      `json:"observed_loaded_generation"`
}

// HostingAutoDeployOptions is fresh owner-only GET guidance, never admission.
// Baseline is absent when the current snapshot cannot qualify reviewed facts.
type HostingAutoDeployOptions struct {
	Available bool                       `json:"available"`
	Reason    string                     `json:"reason"`
	Baseline  *HostingAutoDeployBaseline `json:"baseline,omitempty"`
}

// HostingAutoDeployBaseline copies immutable manual bootstrap evidence. Port,
// readiness and runtime generation are reviewed facts, not editable defaults.
type HostingAutoDeployBaseline struct {
	HealthyOperationID             string                         `json:"healthy_operation_id"`
	LoadedProxyOperationID         string                         `json:"loaded_proxy_operation_id"`
	LoadedGeneration               int64                          `json:"loaded_generation"`
	RuntimeConfigurationGeneration int64                          `json:"runtime_configuration_generation"`
	ContainerPort                  int                            `json:"container_port"`
	Readiness                      HostingReadiness               `json:"readiness"`
	Routes                         []HostingAutoDeployRouteOption `json:"routes"`
	ManualEpoch                    int64                          `json:"manual_epoch"`
}

// HostingAutoDeployRouteOption is one currently eligible versioned owned route.
type HostingAutoDeployRouteOption struct {
	RouteID                    string `json:"route_id"`
	Hostname                   string `json:"hostname"`
	PathPrefix                 string `json:"path_prefix"`
	Version                    int64  `json:"version"`
	SelectedReleaseOperationID string `json:"selected_release_operation_id"`
}

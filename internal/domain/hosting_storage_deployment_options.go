package domain

// HostingStorageDeploymentOptions is a read-only choice snapshot. Submission
// must independently recheck the current resource, storage and route authority.
// Missing predecessor values remain null instead of supplying guessed defaults.
type HostingStorageDeploymentOptions struct {
	ApplicationDatabaseStorage *HostingApplicationDatabaseStorageReview `json:"application_database_storage"`
	ResourceID                 string                                   `json:"resource_id"`
	ResourceGeneration         int64                                    `json:"resource_generation"`
	StorageRef                 *HostingStorageRef                       `json:"storage_ref"`
	PreviousHealthyOperationID *string                                  `json:"previous_healthy_operation_id"`
	HostPort                   *int                                     `json:"host_port"`
	ContainerPort              *int                                     `json:"container_port"`
	Readiness                  *HostingReadiness                        `json:"readiness"`
	Available                  bool                                     `json:"available"`
	Reason                     string                                   `json:"reason"`
	Routes                     []HostingRouteSwitchRoute                `json:"routes"`
	StorageRouteContinuity     *HostingStorageRouteContinuityInput      `json:"storage_route_continuity"`
}

// HostingStorageRouteContinuityStatus separates a committed historical proxy
// receipt from current route health. LoadedGeneration never establishes current
// HTTPS readiness and is omitted until an applied completion receipt exists.
type HostingStorageRouteContinuityStatus struct {
	ResourceID                string `json:"resource_id"`
	OperationID               string `json:"operation_id"`
	State                     string `json:"state"`
	SuccessorProxyOperationID string `json:"successor_proxy_operation_id,omitempty"`
	LoadedGeneration          *int64 `json:"loaded_generation,omitempty"`
	Reason                    string `json:"reason"`
}

// StorageContinuityStatusReason returns only fixed public reason identifiers.
func StorageContinuityStatusReason(state string) string {
	switch state {
	case "reserved", "proxy_pending":
		return "pending"
	case "loaded":
		return "loaded_historical"
	case "released":
		return "released"
	default:
		return "reconciliation_required"
	}
}

package domain

import "errors"

// HostingStorageRouteContinuityInput opts into one same-port storage replacement.
// Every enabled route owned by the resource must be selected explicitly.
type HostingStorageRouteContinuityInput struct {
	ExpectedLoadedGeneration       int64                         `json:"expected_loaded_generation"`
	ExpectedLoadedProxyOperationID string                        `json:"expected_loaded_proxy_operation_id"`
	Routes                         []HostingRouteSwitchSelection `json:"routes"`
}

// HostingStorageRouteContinuityRef binds controller admission to the runner plan
// through the immutable snapshot without changing historical document hashes.
type HostingStorageRouteContinuityRef struct {
	Hash string `json:"hash"`
}

// HostingStorageRouteContinuity is a controller-derived immutable reservation.
// It pins the complete loaded scope, including routes owned by other resources.
type HostingStorageRouteContinuity struct {
	OperationID        string                             `json:"operation_id"`
	RequestHash        string                             `json:"request_hash"`
	Request            HostingStorageRouteContinuityInput `json:"request"`
	ProxyID            string                             `json:"proxy_id"`
	ProjectID          string                             `json:"project_id"`
	ResourceID         string                             `json:"resource_id"`
	BindingID          string                             `json:"binding_id"`
	DestinationID      string                             `json:"destination_id"`
	ServerID           string                             `json:"server_id"`
	RunnerID           string                             `json:"runner_id"`
	HostPort           int                                `json:"host_port"`
	DaemonID           string                             `json:"daemon_id"`
	PreviousHealthy    HostingHealthy                     `json:"previous_healthy"`
	LoadedSnapshotHash string                             `json:"loaded_snapshot_hash"`
}

// CanonicalHostingStorageRouteContinuityInput validates and copies route
// selections in route-ID order before the accepted request is hashed.
func CanonicalHostingStorageRouteContinuityInput(in HostingStorageRouteContinuityInput) (HostingStorageRouteContinuityInput, error) {
	if !hostingProxyID.MatchString(in.ExpectedLoadedProxyOperationID) {
		return HostingStorageRouteContinuityInput{}, errors.New("invalid loaded proxy operation")
	}
	canonical, err := CanonicalHostingRouteSwitchInput(HostingRouteSwitchInput{ExpectedLoadedGeneration: in.ExpectedLoadedGeneration, TargetReleaseOperationID: in.ExpectedLoadedProxyOperationID, Routes: in.Routes})
	if err != nil {
		return HostingStorageRouteContinuityInput{}, err
	}
	in.Routes = canonical.Routes
	return in, nil
}

// ValidateHostingStorageRouteContinuitySnapshot confines reservation references
// to the explicit owned-storage image policy family.
func ValidateHostingStorageRouteContinuitySnapshot(s HostingSnapshot) error {
	if s.StorageRouteContinuity == (HostingStorageRouteContinuityRef{}) {
		return nil
	}
	if !imageSHA256.MatchString(s.StorageRouteContinuity.Hash) || s.Storage == (HostingStorageRef{}) || s.PolicyVersion != HostingOwnedStoragePolicyVersion || s.LocalBuild != (HostingLocalBuildSource{}) || s.RuntimeConfiguration != (HostingRuntimeConfigurationRef{}) {
		return errors.New("invalid storage route continuity snapshot")
	}
	return nil
}

// Validate checks the descriptor against the exact hashed deployment snapshot.
func (c HostingStorageRouteContinuity) Validate(s HostingSnapshot) error {
	if ValidateHostingStorageRouteContinuitySnapshot(s) != nil || s.StorageRouteContinuity.Hash != HostingDocumentHash(c) || !hostingProxyID.MatchString(c.OperationID) || !imageSHA256.MatchString(c.RequestHash) || !imageSHA256.MatchString(c.LoadedSnapshotHash) || c.ProxyID == "" || c.DaemonID == "" || c.ProjectID == "" || c.ResourceID != s.ResourceID || c.BindingID != s.BindingID || c.DestinationID != s.DestinationID || c.ServerID != s.ServerID || c.RunnerID != s.RunnerID || c.HostPort != s.HostPort {
		return errors.New("storage route continuity target mismatch")
	}
	if _, err := CanonicalHostingStorageRouteContinuityInput(c.Request); err != nil {
		return err
	}
	p := c.PreviousHealthy
	if p.OperationID == "" || p.ContainerID == "" || p.Storage == nil || ValidateHostingHealthyStorage(p) != nil || p.Snapshot.Storage != s.Storage || p.Snapshot.BindingID != s.BindingID || p.Snapshot.DestinationID != s.DestinationID || p.Snapshot.ResourceID != s.ResourceID || p.Snapshot.ServerID != s.ServerID || p.Snapshot.RunnerID != s.RunnerID || p.Snapshot.HostPort != s.HostPort || p.Release.DaemonID != c.DaemonID || ValidateHostingSnapshotRelease(p.Snapshot, p.Release) != nil {
		return errors.New("storage route continuity predecessor mismatch")
	}
	for _, r := range c.Request.Routes {
		if r.ExpectedSelectedReleaseOperationID != p.OperationID {
			return errors.New("storage route continuity must replace the previous healthy release")
		}
	}
	return nil
}

package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"strconv"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingruntimeprotocol"
)

type imageMount struct {
	Type        string `json:"Type"`
	Name        string `json:"Name"`
	Driver      string `json:"Driver"`
	Destination string `json:"Destination"`
	RW          bool   `json:"RW"`
}

type ownedStorageExecution struct {
	request      hostingOwnedVolumeRequest
	materialized hostingOwnedVolumeEvidence
}

// ApplyWithOwnedStorage borrows controller-acknowledged materialization evidence.
// The caller must durably journal that acknowledgment and obtain ordinary apply
// admission first. This method never creates a volume or grants writer authority.
func (e *imageEngine) ApplyWithOwnedStorage(ctx context.Context, operationID string, plan domain.HostingApplyPlan, req hostingOwnedVolumeRequest, materialized hostingOwnedVolumeEvidence, verifying ...func() error) domain.HostingResult {
	storage := &ownedStorageExecution{request: req, materialized: materialized}
	if plan.Snapshot.RuntimeConfiguration != (domain.HostingRuntimeConfigurationRef{}) || storage.validate(operationID, plan) != nil {
		return domain.HostingResult{Outcome: "failed", DaemonID: plan.Release.DaemonID, PlanHash: plan.Hash, ImageID: plan.Release.ImageID}
	}
	return e.apply(ctx, operationID, plan, nil, time.Time{}, storage, verifying...)
}

func (s *ownedStorageExecution) validate(operationID string, plan domain.HostingApplyPlan) error {
	r := s.request
	if !hostingVolumeReference.MatchString(operationID) || r.CreatePermitted || r.validate() != nil || verifyImagePlan(plan) != nil || plan.Storage == nil || plan.RetainPrevious || plan.Snapshot.LocalBuild != (domain.HostingLocalBuildSource{}) || plan.Release.LocalBuild != (domain.HostingBuildArtifact{}) {
		return errors.New("owned storage requires an admitted public image plan with exact storage provenance")
	}
	p := plan.Storage
	if r.Storage.Reference() != p.Reference || r.Storage.BindingID != plan.Snapshot.BindingID || r.Storage.ResourceID != plan.Snapshot.ResourceID || r.Storage.DestinationID != plan.Snapshot.DestinationID || r.Storage.VolumeName != p.VolumeName || r.Storage.MountPath != p.MountPath || r.DaemonID != p.DaemonID || r.OriginOperationID != p.CreationOperationID || r.CreationNonce != p.CreationNonce || operationID == r.OriginOperationID && r.OriginPlanHash != plan.Hash {
		return errors.New("owned storage admission does not match immutable plan")
	}
	if !sameOwnedVolumeEvidence(s.materialized, hostingOwnedVolumeEvidence{
		Storage: r.Storage.Reference(), VolumeName: r.Storage.VolumeName, DaemonID: r.DaemonID,
		CreatedAt: s.materialized.CreatedAt, OriginOperationID: r.OriginOperationID, OriginPlanHash: r.OriginPlanHash,
		CreationNonce: r.CreationNonce, Driver: "local", Scope: "local", Labels: r.labels(),
	}) {
		return errors.New("owned storage materialization evidence mismatch")
	}
	created, err := time.Parse(time.RFC3339Nano, s.materialized.CreatedAt)
	if err != nil || created.IsZero() {
		return errors.New("owned storage materialization timestamp missing")
	}
	known := map[string]domain.HostingHealthy{}
	for _, healthy := range storagePlanUsers(plan) {
		if !dockerObjectID.MatchString(healthy.ContainerID) || healthy.OperationID == operationID || healthy.Snapshot.ResourceID != r.Storage.ResourceID || known[healthy.ContainerID].ContainerID != "" {
			return errors.New("owned storage predecessor identity mismatch")
		}
		if healthy.Storage != nil && (healthy.Storage.Plan != *p || healthy.Storage.CreationPlanHash != r.OriginPlanHash) {
			return errors.New("owned storage predecessor origin changed")
		}
		known[healthy.ContainerID] = healthy
	}
	for _, supplied := range r.KnownUsers {
		expected, ok := known[supplied.ContainerID]
		if !ok || domain.HostingDocumentHash(expected) != domain.HostingDocumentHash(supplied) {
			return errors.New("caller user list exceeds immutable plan authority")
		}
	}
	// Only the frozen plan authorizes users. A caller-supplied list cannot add a
	// same-binding container that has no durable retained or healthy pointer.
	s.request.KnownUsers = nil
	for _, healthy := range storagePlanUsers(plan) {
		if healthy.Storage != nil {
			s.request.KnownUsers = append(s.request.KnownUsers, healthy)
		}
	}
	return nil
}

func sameOwnedVolumeEvidence(a, b hostingOwnedVolumeEvidence) bool {
	return a.Storage == b.Storage && a.VolumeName == b.VolumeName && a.DaemonID == b.DaemonID && a.CreatedAt == b.CreatedAt && a.OriginOperationID == b.OriginOperationID && a.OriginPlanHash == b.OriginPlanHash && a.CreationNonce == b.CreationNonce && a.Driver == b.Driver && a.Scope == b.Scope && maps.Equal(a.Labels, b.Labels) && len(a.Options) == 0 && len(b.Options) == 0
}

func storagePlanUsers(plan domain.HostingApplyPlan) []domain.HostingHealthy {
	users := append([]domain.HostingHealthy(nil), plan.RetainedReleases...)
	if plan.PreviousHealthy != nil {
		users = append(users, *plan.PreviousHealthy)
	}
	return users
}

func (s *ownedStorageExecution) healthy(operationID, containerID string, plan domain.HostingApplyPlan) *domain.HostingHealthy {
	return &domain.HostingHealthy{OperationID: operationID, ContainerID: containerID, Snapshot: plan.Snapshot, Release: plan.Release, PlanHash: plan.Hash, Storage: &domain.HostingHealthyStorage{Plan: *plan.Storage, CreationPlanHash: s.request.OriginPlanHash}}
}

func storageContainerMatches(container imageContainer, expected domain.HostingHealthy) bool {
	if container.ID != expected.ContainerID || container.Image != expected.Release.ImageID || !ownedImageContainer(container, expected.Snapshot.BindingID, expected.OperationID, expected.Release.Hash, expected.PlanHash) {
		return false
	}
	ports := container.HostConfig.PortBindings[strconv.Itoa(expected.Snapshot.ContainerPort)+"/tcp"]
	if len(container.HostConfig.PortBindings) != 1 || len(ports) != 1 || ports[0].HostIP != "127.0.0.1" || ports[0].HostPort != strconv.Itoa(expected.Snapshot.HostPort) {
		return false
	}
	if expected.Storage == nil {
		return expected.Snapshot.Storage == (domain.HostingStorageRef{}) && len(container.Mounts) == 0
	}
	if domain.ValidateHostingHealthyStorage(expected) != nil || len(container.Mounts) != 1 {
		return false
	}
	mount := container.Mounts[0]
	p := expected.Storage.Plan
	return mount.Type == "volume" && mount.Name == p.VolumeName && mount.Driver == "local" && mount.Destination == p.MountPath && mount.RW
}

func (s *ownedStorageExecution) confirmStopped(ctx context.Context, e *imageEngine, expected domain.HostingHealthy) error {
	container, err := e.inspectContainer(ctx, expected.ContainerID)
	if err != nil || !storageContainerMatches(container, expected) || container.State.Running || ctx.Err() != nil {
		return errors.New("owned storage writer stop is unconfirmed")
	}
	return nil
}

func (s *ownedStorageExecution) verify(ctx context.Context, e *imageEngine, plan domain.HostingApplyPlan, candidate *domain.HostingHealthy, runningID string) error {
	req := s.request
	users := storagePlanUsers(plan)
	if candidate != nil {
		req.KnownUsers = append(append([]domain.HostingHealthy(nil), req.KnownUsers...), *candidate)
		users = append(users, *candidate)
	}
	got, err := e.ensureOwnedVolume(ctx, req)
	if err != nil {
		return err
	}
	if !sameOwnedVolumeEvidence(got, s.materialized) {
		return errors.New("materialized owned storage volume was replaced")
	}
	for _, expected := range users {
		container, err := e.inspectContainer(ctx, expected.ContainerID)
		if err != nil || !storageContainerMatches(container, expected) || container.State.Running && container.ID != runningID {
			return errors.New("owned storage workload identity or single writer mismatch")
		}
	}
	return ctx.Err()
}

// verifyHealthyStorageVolume reconstructs only hash-covered immutable metadata.
// Project identity from Docker must reproduce the controller-pinned document hash;
// it is never accepted merely because a volume supplied a matching hash label.
func (e *imageEngine) verifyHealthyStorageVolume(ctx context.Context, healthy domain.HostingHealthy) error {
	if domain.ValidateHostingHealthyStorage(healthy) != nil || healthy.Storage == nil {
		return errors.New("healthy owned storage descriptor missing")
	}
	p := healthy.Storage.Plan
	if err := e.verifyOwnedVolumeDaemon(ctx, p.DaemonID); err != nil {
		return err
	}
	raw, err := e.ownedVolumeDocker(ctx, 64<<10, "volume", "inspect", p.VolumeName, "--format", "{{json .}}")
	if err != nil {
		return err
	}
	var volume hostingInspectedVolume
	if json.Unmarshal(raw, &volume) != nil || volume.Name != p.VolumeName || volume.Driver != "local" || volume.Scope != "local" || len(volume.Options) != 0 {
		return errors.New("healthy owned storage volume policy changed")
	}
	created, err := time.Parse(time.RFC3339Nano, volume.CreatedAt)
	if err != nil || created.IsZero() {
		return errors.New("healthy owned storage volume timestamp missing")
	}
	metadata := domain.HostingStorage{ID: p.Reference.StorageID, ProjectID: volume.Labels[hostingStorageLabelPrefix+"project"], ResourceID: healthy.Snapshot.ResourceID, BindingID: healthy.Snapshot.BindingID, DestinationID: healthy.Snapshot.DestinationID, Kind: "platform_managed", Driver: "local", VolumeName: p.VolumeName, MountPath: p.MountPath, AccessMode: "read_write", Retention: "retain", Generation: p.Reference.Generation, Hash: p.Reference.Hash}
	req := hostingOwnedVolumeRequest{Storage: metadata, DaemonID: p.DaemonID, OriginOperationID: p.CreationOperationID, OriginPlanHash: healthy.Storage.CreationPlanHash, CreationNonce: p.CreationNonce}
	if metadata.DocumentHash() != p.Reference.Hash || !maps.Equal(volume.Labels, req.labels()) {
		return errors.New("healthy owned storage volume ownership changed")
	}
	return e.verifyOwnedVolumeDaemon(ctx, p.DaemonID)
}

func (e *imageEngine) verifyStorageObservation(ctx context.Context, expected domain.HostingHealthy, retained []domain.HostingHealthy) error {
	if err := e.verifyHealthyStorageVolume(ctx, expected); err != nil {
		return err
	}
	known := map[string]domain.HostingHealthy{expected.ContainerID: expected}
	for _, healthy := range retained {
		if healthy.ContainerID == expected.ContainerID {
			continue
		}
		if healthy.Storage != nil {
			if healthy.Snapshot.BindingID != expected.Snapshot.BindingID || healthy.Snapshot.ResourceID != expected.Snapshot.ResourceID || healthy.Storage.Plan != expected.Storage.Plan || healthy.Storage.CreationPlanHash != expected.Storage.CreationPlanHash {
				return errors.New("observed retained storage origin mismatch")
			}
			known[healthy.ContainerID] = healthy
		}
	}
	raw, err := e.ownedVolumeDocker(ctx, 16<<10, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "volume="+expected.Storage.Plan.VolumeName)
	if err != nil {
		return err
	}
	ids, err := hostingVolumeLines(raw, 128, dockerObjectID)
	if err != nil {
		return err
	}
	found := false
	for _, id := range ids {
		healthy, ok := known[id]
		if !ok {
			return errors.New("foreign user prevents storage observation")
		}
		container, err := e.inspectContainer(ctx, id)
		if err != nil || !storageContainerMatches(container, healthy) || id != expected.ContainerID && container.State.Running {
			return errors.New("storage observation identity or writer mismatch")
		}
		found = found || id == expected.ContainerID
	}
	if !found {
		return errors.New("healthy storage writer mount is missing")
	}
	return ctx.Err()
}

// ApplyWithOwnedStorageRuntimeEnvironment borrows protected values and admitted volume evidence.
func (e *imageEngine) ApplyWithOwnedStorageRuntimeEnvironment(ctx context.Context, operationID string, plan domain.HostingApplyPlan, req hostingOwnedVolumeRequest, materialized hostingOwnedVolumeEvidence, environment *hostingruntimeprotocol.Environment, deadline time.Time, verifying ...func() error) domain.HostingResult {
	storage := &ownedStorageExecution{request: req, materialized: materialized}
	if plan.CompilerVersion != domain.HostingOwnedStorageRuntimeCompilerVersion || storage.validate(operationID, plan) != nil {
		return domain.HostingResult{Outcome: "failed", DaemonID: plan.Release.DaemonID, PlanHash: plan.Hash, ImageID: plan.Release.ImageID}
	}
	return e.apply(ctx, operationID, plan, environment, deadline, storage, verifying...)
}

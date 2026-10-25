//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"time"

	"nerocd/internal/domain"
)

// Scoped storage is initialized from controller materialization evidence. It is
// never inferred from a Docker name or from a caller-provided mount.
func (e *applicationDatabaseEngine) bindStorage(op domain.HostingOperation, ack domain.HostingStorageCreateAck) error {
	if op.ApplicationDatabaseStorage == nil || op.Plan == nil || op.Plan.Storage == nil || op.ApplicationDatabaseStorage.Validate(op.Snapshot) != nil {
		return errApplicationDatabaseEngine
	}
	req, err := storageVolumeRequest(op, ack)
	if err != nil {
		return errApplicationDatabaseEngine
	}
	req.CreatePermitted = false
	e.storage = &req
	e.storagePredecessor = op.Plan.PreviousHealthy
	return nil
}
func applicationDatabaseMountedSafe(c applicationDatabaseContainer, p domain.HostingStoragePlan) bool {
	if len(c.Mounts) != 1 || len(c.HostConfig.Mounts) != 1 {
		return false
	}
	var observed struct {
		Type, Name, Driver, Destination, Mode, Propagation string
		RW                                                 bool
	}
	var configured struct {
		Type, Source, Target      string
		ReadOnly                  bool
		Consistency               string
		BindOptions, TmpfsOptions json.RawMessage
		VolumeOptions             *struct {
			NoCopy       bool
			Subpath      string
			Labels       map[string]string
			DriverConfig json.RawMessage
		}
	}
	if json.Unmarshal(c.Mounts[0], &observed) != nil || json.Unmarshal(c.HostConfig.Mounts[0], &configured) != nil || observed.Type != "volume" || observed.Name != p.VolumeName || observed.Driver != "local" || observed.Destination != p.MountPath || !observed.RW || observed.Propagation != "" || configured.Type != "volume" || configured.Source != p.VolumeName || configured.Target != p.MountPath || configured.ReadOnly || configured.Consistency != "" || len(configured.BindOptions) > 0 && string(configured.BindOptions) != "null" || len(configured.TmpfsOptions) > 0 && string(configured.TmpfsOptions) != "null" || configured.VolumeOptions == nil || !configured.VolumeOptions.NoCopy || configured.VolumeOptions.Subpath != "" || len(configured.VolumeOptions.Labels) != 0 || len(configured.VolumeOptions.DriverConfig) > 0 && string(configured.VolumeOptions.DriverConfig) != "null" {
		return false
	}
	c.Mounts = nil
	c.HostConfig.Mounts = nil
	return applicationDatabaseSafeContainer(c)
}
func (e *applicationDatabaseEngine) storageCandidateSafe(c applicationDatabaseContainer, in applicationDatabaseEngineInput) bool {
	if in.Plan.Storage == nil {
		return applicationDatabaseSafeContainer(c)
	}
	return e.storage != nil && e.storage.Storage.Reference() == in.Plan.Storage.Reference && applicationDatabaseMountedSafe(c, *in.Plan.Storage)
}
func (e *applicationDatabaseEngine) storageGuard(ctx context.Context, in applicationDatabaseEngineInput, candidate *applicationDatabaseCandidate, stopped bool) error {
	if in.Plan.Storage == nil {
		return nil
	}
	if e.storage == nil || e.storage.CreatePermitted || e.storage.Storage.Reference() != in.Plan.Storage.Reference || len(in.Proof.Members) != 1 {
		return errApplicationDatabaseEngine
	}
	req := *e.storage
	req.KnownUsers = append([]domain.HostingHealthy(nil), req.KnownUsers...)
	if candidate != nil {
		healthy := domain.HostingHealthy{OperationID: candidate.OperationID, ContainerID: candidate.ContainerID, PlanHash: candidate.PlanHash, Snapshot: in.Plan.Snapshot, Release: in.Plan.Release, Storage: &domain.HostingHealthyStorage{Plan: *in.Plan.Storage, CreationPlanHash: req.OriginPlanHash}}
		req.KnownUsers = append(req.KnownUsers, healthy)
	}
	if _, err := e.image.ensureOwnedVolume(ctx, req); err != nil {
		return errApplicationDatabaseEngine
	}
	if stopped {
		bridge, err := e.bridge(ctx)
		if err != nil || e.stoppedMember(ctx, in, in.Plan.ApplicationDatabase.PreviousMembers[0], bridge) != nil {
			return errApplicationDatabaseEngine
		}
	}
	return nil
}
func (e *applicationDatabaseEngine) StopStoragePredecessor(ctx context.Context, in applicationDatabaseEngineInput, before func(context.Context) error) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if in.Plan.Storage == nil || len(in.Proof.Members) != 1 || e.storageGuard(ctx, in, nil, false) != nil || e.network(ctx, in, nil, "") != nil {
		return "", errApplicationDatabaseEngine
	}
	m := in.Plan.ApplicationDatabase.PreviousMembers[0]
	old, err := e.member(ctx, m, in.Proof.Binding.ApplicationBindingID)
	if err != nil || !old.State.Running {
		return "", errApplicationDatabaseEngine
	}
	if e.effect(ctx, in, before, "container", "stop", "--time", "10", old.ID) != nil || e.storageGuard(ctx, in, nil, true) != nil {
		return "", errApplicationDatabaseEngine
	}
	return domain.HostingDocumentHash(struct{ Predecessor, Storage string }{domain.HostingDocumentHash(in.Plan.PreviousHealthy), domain.HostingDocumentHash(e.storage)}), nil
}
func (e *applicationDatabaseEngine) StopStorageCandidate(ctx context.Context, in applicationDatabaseEngineInput, candidate applicationDatabaseCandidate, before func(context.Context) error) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if in.Plan.Storage == nil || e.storageGuard(ctx, in, &candidate, true) != nil {
		return "", errApplicationDatabaseEngine
	}
	actual, err := e.container(ctx, candidate.ContainerID)
	if err != nil || !e.storageCandidateSafe(actual, in) || actual.Image != candidate.ImageID || actual.Config.Labels[imageOperationLabel] != candidate.OperationID || actual.Config.Labels[imagePlanLabel] != candidate.PlanHash || actual.Config.Labels[applicationDatabaseNonceLabel] != candidate.CreateNonce {
		return "", errApplicationDatabaseEngine
	}
	var attached *applicationDatabaseCandidate
	if actual.State.Running {
		attached = &candidate
	}
	if e.network(ctx, in, attached, "") != nil {
		return "", errApplicationDatabaseEngine
	}
	if actual.State.Running && e.effect(ctx, in, before, "container", "stop", "--time", "10", candidate.ContainerID) != nil {
		return "", errApplicationDatabaseEngine
	}
	actual, err = e.container(ctx, candidate.ContainerID)
	if err != nil || actual.State.Running || actual.State.Paused || actual.State.Restarting || actual.State.Dead || actual.State.Status != "exited" && actual.State.Status != "created" || e.storageGuard(ctx, in, &candidate, true) != nil {
		return "", errApplicationDatabaseEngine
	}
	return domain.HostingDocumentHash(struct{ Candidate, Storage string }{domain.HostingDocumentHash(candidate), domain.HostingDocumentHash(e.storage)}), nil
}
func (e *applicationDatabaseEngine) ResumeStoragePredecessor(ctx context.Context, in applicationDatabaseEngineInput, candidate applicationDatabaseCandidate, before func(context.Context) error) (string, *domain.HostingDatabaseBindingEndpointObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if in.Plan.Storage == nil || e.storageGuard(ctx, in, &candidate, true) != nil || e.network(ctx, in, nil, "") != nil {
		return "", nil, errApplicationDatabaseEngine
	}
	actual, err := e.container(ctx, candidate.ContainerID)
	if err != nil || actual.State.Running || e.inspectCandidateState(ctx, in, candidate, true, false, actual.State.Status == "exited") != nil {
		return "", nil, errApplicationDatabaseEngine
	}
	// The durable resume intent covers exact failed-candidate cleanup as well as
	// A's restart. Never delete the volume or retain B as a future known user.
	if e.effect(ctx, in, before, "container", "rm", candidate.ContainerID) != nil {
		return "", nil, errApplicationDatabaseEngine
	}
	absent, absenceErr := e.recoveryIDAbsent(ctx, candidate.ContainerID)
	if absenceErr != nil || !absent || e.storageGuard(ctx, in, nil, true) != nil || e.network(ctx, in, nil, "") != nil {
		return "", nil, errApplicationDatabaseEngine
	}
	old := in.Plan.ApplicationDatabase.PreviousMembers[0]
	if e.effect(ctx, in, before, "container", "start", old.Endpoint.Target.ContainerID) != nil {
		return "", nil, errApplicationDatabaseEngine
	}
	// Docker assigns a fresh endpoint after resume. Do not substitute that endpoint
	// into the immutable predecessor record; only readiness and writer quiescence
	// settle this explicit failed replacement.
	c, err := e.container(ctx, old.Endpoint.Target.ContainerID)
	bridge, bridgeErr := e.bridge(ctx)
	private := c.NetworkSettings.Networks[in.Proof.Binding.NetworkName]
	public := c.NetworkSettings.Networks["bridge"]
	if err != nil || bridgeErr != nil || !e.memberIdentity(c, old, in.Proof.Binding.ApplicationBindingID) || !c.State.Running || c.State.Status != "running" || len(c.NetworkSettings.Networks) != 2 || private.NetworkID != in.Proof.Origin.NetworkID || !dockerObjectID.MatchString(private.EndpointID) || public.NetworkID != bridge || !dockerObjectID.MatchString(public.EndpointID) || !applicationDatabaseEndpointConfig(private, c.Name, c.ID, true) || !applicationDatabaseEndpointConfig(public, c.Name, c.ID, false) {
		return "", nil, errApplicationDatabaseEngine
	}
	checkResumed := func(ctx context.Context) error {
		absent, absenceErr := e.recoveryIDAbsent(ctx, candidate.ContainerID)
		resumed, resumedErr := e.container(ctx, c.ID)
		if absenceErr != nil || !absent || resumedErr != nil || !e.memberIdentity(resumed, old, in.Proof.Binding.ApplicationBindingID) || !resumed.State.Running || resumed.NetworkSettings.Networks[in.Proof.Binding.NetworkName].EndpointID != private.EndpointID || e.storageGuard(ctx, in, nil, false) != nil {
			return errApplicationDatabaseEngine
		}
		// Use the observed resumed container only as the allowed network user.
		// The retained predecessor endpoint remains immutable in the input proof.
		return e.network(ctx, in, &applicationDatabaseCandidate{ContainerID: c.ID}, c.ID)
	}
	if err = e.image.readiness(ctx, domain.HostingApplyPlan{Snapshot: in.Plan.PreviousHealthy.Snapshot, Release: in.Plan.PreviousHealthy.Release, Hash: in.Plan.PreviousHealthy.PlanHash}, in.Plan.PreviousHealthy.OperationID, c.ID, checkResumed); err != nil {
		return "", nil, errApplicationDatabaseEngine
	}
	endpoint := old.Endpoint
	endpoint.EndpointID = c.NetworkSettings.Networks[in.Proof.Binding.NetworkName].EndpointID
	return domain.HostingDocumentHash(struct{ Predecessor, Candidate string }{domain.HostingDocumentHash(in.Plan.PreviousHealthy), domain.HostingDocumentHash(candidate)}), &domain.HostingDatabaseBindingEndpointObservation{Endpoint: endpoint, Present: true}, nil
}

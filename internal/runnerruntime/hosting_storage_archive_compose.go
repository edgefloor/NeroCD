//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"sort"

	"nerocd/internal/domain"
)

func (e *storageArchiveEngine) composeVolume(ctx context.Context, p domain.HostingStorageArchivePlan) (string, error) {
	c := p.ComposeSource
	if c == nil || e.image.verifyDaemon(ctx, p.Helper.DaemonID) != nil {
		return "", errStorageArchiveEngine
	}
	if _, err := e.image.inspectStackVolume(ctx, c.Plan, c.Volume); err != nil {
		return "", errStorageArchiveEngine
	}
	raw, err := e.image.ownedVolumeDocker(ctx, 64<<10, "volume", "inspect", c.Volume.VolumeName, "--format", "{{json .}}")
	var v struct {
		hostingInspectedVolume
		Mountpoint string
	}
	labels := map[string]string{imageBindingLabel: c.Plan.Snapshot.BindingID, stackVolumeLabel: c.Volume.Name, stackOriginLabel: c.Volume.CreationOperationID, stackNonceLabel: c.Volume.CreationNonce}
	expected := filepath.Join(e.dockerRoot, "volumes", c.Volume.VolumeName, "_data")
	if err != nil || json.Unmarshal(raw, &v) != nil || !maps.Equal(v.Labels, labels) || v.Mountpoint != expected {
		return "", errStorageArchiveEngine
	}
	canonical, err := filepath.EvalSymlinks(v.Mountpoint)
	if err != nil || canonical != expected {
		return "", errStorageArchiveEngine
	}
	return expected, nil
}
func (e *storageArchiveEngine) sourceVolume(ctx context.Context, p domain.HostingStorageArchivePlan) (string, error) {
	if p.ComposeSource != nil {
		return e.composeVolume(ctx, p)
	}
	return e.volume(ctx, archiveSourceRequest(p))
}

func (e *storageArchiveEngine) composeWriters(ctx context.Context, p domain.HostingStorageArchivePlan, helperID string) ([]domain.HostingStorageArchiveWriterState, error) {
	if _, err := e.sourceVolume(ctx, p); err != nil {
		return nil, errStorageArchiveEngine
	}
	raw, err := e.image.ownedVolumeDocker(ctx, 16<<10, "container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "volume="+p.SourceVolumeName())
	ids, parseErr := hostingVolumeLines(raw, 128, dockerObjectID)
	if err != nil || parseErr != nil {
		return nil, errStorageArchiveEngine
	}
	sort.Strings(ids)
	states := []domain.HostingStorageArchiveWriterState{}
	c := p.ComposeSource
	proofs := append([]domain.HostingStorageArchiveStackProof{{Healthy: c.Healthy, Plan: c.Plan}}, c.Retained...)
	eligible := p.WriterStates()
	for _, id := range ids {
		if id == helperID {
			continue
		}
		index := slices.IndexFunc(eligible, func(w domain.HostingStorageArchiveWriterState) bool { return w.ContainerID == id })
		if index < 0 {
			return nil, errStorageArchiveEngine
		}
		expected := eligible[index]
		container, err := e.image.inspectStackContainer(ctx, id)
		if err != nil || container.ID != id {
			return nil, errStorageArchiveEngine
		}
		found := false
		for _, proof := range proofs {
			if proof.Healthy.OperationID == expected.OperationID && stackContainerMatches(container, expected.OperationID, proof.Plan, proof.Healthy.Stack.NetworkID) == nil {
				found = true
				break
			}
		}
		if !found {
			return nil, errStorageArchiveEngine
		}
		expected.Running = container.State.Running
		states = append(states, expected)
	}
	if !p.MatchesWriterStates(states, true) {
		return nil, errStorageArchiveEngine
	}
	// Recovery must retain every original obligation even when a retained user has disappeared.
	if p.Mode == "recover" && len(p.RecoveryEvidence.Writers) > 0 {
		if len(states) != len(p.RecoveryEvidence.Writers) {
			return nil, errStorageArchiveEngine
		}
		for i, w := range states {
			if !sameArchiveWriter(w, p.RecoveryEvidence.Writers[i]) {
				return nil, errStorageArchiveEngine
			}
		}
	}
	return states, nil
}

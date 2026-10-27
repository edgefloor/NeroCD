package domain

import (
	"encoding/json"
	"slices"
	"sort"
	"time"
)

const (
	HostingComposeStorageArchiveVersion    = "managed-compose.local-volume-archive.v1"
	HostingComposeStorageArchiveCapability = "hosting_compose_storage_archive_v1"
)

// HostingStorageArchiveStackProof binds an observed stack to its immutable apply plan.
type HostingStorageArchiveStackProof struct {
	Healthy HostingHealthy   `json:"healthy"`
	Plan    HostingApplyPlan `json:"plan"`
}

// HostingStorageArchiveComposeSource selects one materialized named volume.
// Retained proofs account for owned users left by earlier deployments.
type HostingStorageArchiveComposeSource struct {
	Healthy  HostingHealthy                    `json:"healthy"`
	Plan     HostingApplyPlan                  `json:"plan"`
	Volume   HostingStackVolume                `json:"volume"`
	Retained []HostingStorageArchiveStackProof `json:"retained"`
}

// HostingStorageBackupVolumes reports settled controller evidence, not live health.
type HostingStorageBackupVolumes struct {
	ResourceID         string                       `json:"resource_id"`
	ResourceGeneration int64                        `json:"resource_generation"`
	OperationID        string                       `json:"operation_id,omitempty"`
	Available          bool                         `json:"available"`
	Reason             string                       `json:"reason,omitempty"`
	Volumes            []HostingStorageBackupVolume `json:"volumes"`
}
type HostingStorageBackupVolume struct {
	Volume   string                            `json:"volume"`
	Eligible bool                              `json:"eligible"`
	Reason   string                            `json:"reason,omitempty"`
	Mounts   []HostingStorageBackupVolumeMount `json:"mounts"`
}
type HostingStorageBackupVolumeMount struct {
	Component string `json:"component"`
	MountPath string `json:"mount_path"`
	ReadOnly  bool   `json:"read_only"`
}

func (p HostingStorageArchivePlan) SourceResourceID() string {
	if p.ComposeSource != nil {
		return p.ComposeSource.Healthy.Snapshot.ResourceID
	}
	return p.SourceStorage.ResourceID
}
func (p HostingStorageArchivePlan) SourceBindingID() string {
	if p.ComposeSource != nil {
		return p.ComposeSource.Healthy.Snapshot.BindingID
	}
	return p.SourceStorage.BindingID
}
func (p HostingStorageArchivePlan) SourceVolumeName() string {
	if p.ComposeSource != nil {
		return p.ComposeSource.Volume.VolumeName
	}
	return p.SourceStorage.VolumeName
}
func (p HostingStorageArchivePlan) SourceIdentityHash() string {
	if p.ComposeSource != nil {
		return HostingDocumentHash(p.ComposeSource)
	}
	return p.SourceStorage.Hash
}

// WriterStates returns every eligible user, including read-only and completed init containers.
// Running is observed again by the runner before it creates any resume obligation.
func (p HostingStorageArchivePlan) WriterStates() []HostingStorageArchiveWriterState {
	out := []HostingStorageArchiveWriterState{}
	if p.ComposeSource == nil {
		for _, h := range p.Writers {
			out = append(out, HostingStorageArchiveWriterState{ContainerID: h.ContainerID, OperationID: h.OperationID, ImageID: h.Release.ImageID, PlanHash: h.PlanHash, ReleaseHash: h.Release.Hash})
		}
		return out
	}
	c := p.ComposeSource
	proofs := append([]HostingStorageArchiveStackProof{{Healthy: c.Healthy, Plan: c.Plan}}, c.Retained...)
	for _, proof := range proofs {
		if proof.Healthy.Stack == nil || proof.Plan.Stack == nil {
			continue
		}
		for _, component := range proof.Healthy.Stack.Components {
			for _, service := range proof.Plan.Stack.Definition.Services {
				if service.Name != component.Component {
					continue
				}
				if slices.ContainsFunc(service.Mounts, func(m HostingStackMount) bool { return m.Volume == c.Volume.Name }) {
					out = append(out, HostingStorageArchiveWriterState{ContainerID: component.ContainerID, OperationID: component.OperationID, ImageID: component.ImageID, PlanHash: proof.Plan.Hash, ReleaseHash: proof.Healthy.Release.Hash})
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ContainerID < out[j].ContainerID })
	return out
}

func (p HostingStorageArchivePlan) validateCompose() error {
	c := p.ComposeSource
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > HostingStorageArchiveJSONLimit {
		return ErrInvalidStorageArchive
	}
	if c == nil || p.CompilerVersion != HostingComposeStorageArchiveVersion || p.PolicyVersion != HostingComposeStorageArchiveVersion || p.Helper.Validate() != nil || p.Limits.Validate() != nil || p.RemoteTarget != nil || p.RemoteSource != nil || p.Target != nil || p.Blob != nil || len(p.Writers) != 0 || HostingDocumentHash(p.SourceHealthy) != HostingDocumentHash(HostingHealthy{}) || p.SourceStorage != (HostingStorage{}) || HostingDocumentHash(p.SourceMaterialization) != HostingDocumentHash(HostingStorageMaterialization{}) {
		return ErrInvalidStorageArchive
	}
	for _, id := range []string{p.OperationID, p.ProjectID, p.ServerID, p.DestinationID, p.RunnerID, p.BackupID} {
		if !hostingProxyID.MatchString(id) {
			return ErrInvalidStorageArchive
		}
	}
	if !stackName.MatchString(c.Volume.Name) || c.Volume.CreatedAt == "" || len(c.Retained) >= 64 {
		return ErrInvalidStorageArchive
	}
	if t, err := time.Parse(time.RFC3339Nano, c.Volume.CreatedAt); err != nil || t.IsZero() {
		return ErrInvalidStorageArchive
	}
	proofs := append([]HostingStorageArchiveStackProof{{Healthy: c.Healthy, Plan: c.Plan}}, c.Retained...)
	seen := map[string]bool{}
	for _, proof := range proofs {
		h := proof.Healthy
		s := h.Snapshot
		if _, err := CompileHostingStackStopPlan(h, proof.Plan); err != nil {
			return ErrInvalidStorageArchive
		}
		if seen[h.OperationID] || s.ResourceID != p.SourceResourceID() || s.BindingID != p.SourceBindingID() || s.DestinationID != p.DestinationID || s.RunnerID != p.RunnerID || s.ServerID != p.ServerID || h.Release.DaemonID != p.Helper.DaemonID {
			return ErrInvalidStorageArchive
		}
		seen[h.OperationID] = true
		for _, v := range h.Stack.Volumes {
			if v.Name == c.Volume.Name && v != c.Volume {
				return ErrInvalidStorageArchive
			}
		}
	}
	if !slices.Contains(c.Healthy.Stack.Volumes, c.Volume) {
		return ErrInvalidStorageArchive
	}
	writers := p.WriterStates()
	if len(writers) < 1 || len(writers) > 1024 {
		return ErrInvalidStorageArchive
	}
	for i, w := range writers {
		if i > 0 && w.ContainerID <= writers[i-1].ContainerID {
			return ErrInvalidStorageArchive
		}
	}
	switch p.Mode {
	case "backup":
		if p.RecoveryOfOperationID != "" || p.RecoveryEvidence != nil {
			return ErrInvalidStorageArchive
		}
	case "recover":
		if !hostingProxyID.MatchString(p.RecoveryOfOperationID) || p.RecoveryOfOperationID == p.OperationID || p.RecoveryEvidence == nil || len(p.RecoveryEvidence.Writers) > 0 && !p.MatchesWriterStates(p.RecoveryEvidence.Writers, true) || p.RecoveryEvidence.ValidateStates(writers) != nil {
			return ErrInvalidStorageArchive
		}
	default:
		return ErrInvalidStorageArchive
	}
	expected := p
	expected.Hash = ""
	if p.Hash != HostingDocumentHash(expected) {
		return ErrInvalidStorageArchive
	}
	return nil
}

// ValidateStates preserves exact eligible identities and the initially running resume subset.
func (e HostingStorageArchiveRecoveryEvidence) ValidateStates(eligible []HostingStorageArchiveWriterState) error {
	if !imageSHA256.MatchString(e.OriginalPlanHash) || len(e.Writers) > 64 || len(e.ResumeRequiredContainerIDs) > len(e.Writers) {
		return ErrInvalidStorageArchive
	}
	if len(e.Writers) == 0 {
		if e.SnapshotReceiptID != "" || len(e.ResumeRequiredContainerIDs) != 0 {
			return ErrInvalidStorageArchive
		}
		return nil
	}
	if !validRuntimeReferenceID(e.SnapshotReceiptID) || len(e.Writers) > len(eligible) {
		return ErrInvalidStorageArchive
	}
	running := map[string]bool{}
	lastObserved := ""
	for _, w := range e.Writers {
		if w.ContainerID <= lastObserved {
			return ErrInvalidStorageArchive
		}
		lastObserved = w.ContainerID
		found := false
		for _, h := range eligible {
			h.Running = w.Running
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return ErrInvalidStorageArchive
		}
		running[w.ContainerID] = w.Running
	}
	last := ""
	for _, id := range e.ResumeRequiredContainerIDs {
		if id <= last || !running[id] {
			return ErrInvalidStorageArchive
		}
		last = id
	}
	return nil
}

// MatchesWriterStates accepts a canonical observed subset of retained users.
// Every component of the current deployment that mounts the selected volume is mandatory.
func (p HostingStorageArchivePlan) MatchesWriterStates(states []HostingStorageArchiveWriterState, ignoreRunning bool) bool {
	if len(states) > 64 {
		return false
	}
	eligible := p.WriterStates()
	if p.ComposeSource == nil && len(states) != len(eligible) {
		return false
	}
	seen := map[string]bool{}
	last := ""
	for _, w := range states {
		if w.ContainerID <= last {
			return false
		}
		last = w.ContainerID
		found := false
		for _, v := range eligible {
			if ignoreRunning {
				v.Running = w.Running
			}
			if v == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
		seen[w.ContainerID] = true
	}
	if p.ComposeSource != nil {
		for _, w := range eligible {
			if w.OperationID == p.ComposeSource.Healthy.OperationID && !seen[w.ContainerID] {
				return false
			}
		}
	}
	return true
}

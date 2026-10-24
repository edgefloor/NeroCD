package domain

import (
	"errors"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"
)

// Archive authority is a separate family. These identifiers do not qualify a runner.
const (
	RunTypeStorageArchive                 = "storage_archive"
	HostingStorageArchiveCapability       = "hosting_storage_archive_v1"
	HostingStorageArchiveVersion          = "owned-upload.local-archive.v1"
	HostingStorageArchiveFormat           = "nerocd-storage-v1"
	HostingStorageArchiveMaxBytes   int64 = 1 << 30
)

// ErrInvalidStorageArchive is safe to return without untrusted diagnostic data.
var ErrInvalidStorageArchive = errors.New("invalid storage archive document")

// HostingStorageBackupInput selects the exact saved resource generation.
type HostingStorageBackupInput struct {
	RequestID                  string `json:"request_id"`
	ExpectedResourceGeneration int64  `json:"expected_resource_generation"`
}

// HostingStorageRestoreInput creates a new held resource, never an in-place restore.
type HostingStorageRestoreInput struct {
	RequestID string `json:"request_id"`
	Name      string `json:"name"`
}

// HostingStorageArchiveRecoveryInput selects one immutable unresolved plan.
type HostingStorageArchiveRecoveryInput struct {
	RequestID        string `json:"request_id"`
	ExpectedPlanHash string `json:"expected_plan_hash"`
}

// Validate requires exact request identity and a positive resource generation.
func (in HostingStorageBackupInput) Validate() error {
	if !hostingStorageRequest.MatchString(in.RequestID) || in.ExpectedResourceGeneration < 1 {
		return ErrInvalidStorageArchive
	}
	return nil
}

// Validate permits only a request identity and a bounded new display name.
func (in HostingStorageRestoreInput) Validate() error {
	if !hostingStorageRequest.MatchString(in.RequestID) || in.Name == "" || len(in.Name) > 128 || strings.TrimSpace(in.Name) != in.Name || strings.ContainsFunc(in.Name, unicode.IsControl) {
		return ErrInvalidStorageArchive
	}
	return nil
}

// Validate requires an explicit immutable plan selected for recovery.
func (in HostingStorageArchiveRecoveryInput) Validate() error {
	if !hostingStorageRequest.MatchString(in.RequestID) || !imageSHA256.MatchString(in.ExpectedPlanHash) {
		return ErrInvalidStorageArchive
	}
	return nil
}

func decodeStorageArchiveInput(raw []byte, value any) error {
	if len(raw) > 4096 || !validRuntimeJSONUnicode(raw) || DecodeHostingStorageProtocolJSON(raw, value) != nil {
		return ErrInvalidStorageArchive
	}
	return nil
}

// UnmarshalJSON accepts only the closed backup request document.
func (in *HostingStorageBackupInput) UnmarshalJSON(raw []byte) error {
	type wire HostingStorageBackupInput
	var v wire
	if decodeStorageArchiveInput(raw, &v) != nil || HostingStorageBackupInput(v).Validate() != nil {
		return ErrInvalidStorageArchive
	}
	*in = HostingStorageBackupInput(v)
	return nil
}

// UnmarshalJSON accepts only the closed new-target restore request document.
func (in *HostingStorageRestoreInput) UnmarshalJSON(raw []byte) error {
	type wire HostingStorageRestoreInput
	var v wire
	if decodeStorageArchiveInput(raw, &v) != nil || HostingStorageRestoreInput(v).Validate() != nil {
		return ErrInvalidStorageArchive
	}
	*in = HostingStorageRestoreInput(v)
	return nil
}

// UnmarshalJSON accepts only the closed recovery request document.
func (in *HostingStorageArchiveRecoveryInput) UnmarshalJSON(raw []byte) error {
	type wire HostingStorageArchiveRecoveryInput
	var v wire
	if decodeStorageArchiveInput(raw, &v) != nil || HostingStorageArchiveRecoveryInput(v).Validate() != nil {
		return ErrInvalidStorageArchive
	}
	*in = HostingStorageArchiveRecoveryInput(v)
	return nil
}

// HostingStorageArchiveHelper is trusted immutable helper configuration. It is
// distinct from an application release and must be observed again at preparation.
type HostingStorageArchiveHelper struct {
	Mode             string `json:"mode"`
	RecipeVersion    string `json:"recipe_version"`
	ImageID          string `json:"image_id"`
	ExecutableSHA256 string `json:"executable_sha256"`
	SourceCommit     string `json:"source_commit"`
	OS               string `json:"os"`
	Architecture     string `json:"architecture"`
	Variant          string `json:"variant,omitempty"`
	DaemonID         string `json:"daemon_id"`
	Hash             string `json:"hash"`
}

// Validate accepts only the explicitly configured same-daemon native helper.
func (h HostingStorageArchiveHelper) Validate() error {
	if h.Mode != "local_image" || h.RecipeVersion != HostingStorageArchiveVersion || h.OS != "linux" || h.Architecture != "arm64" || h.Variant != "" || !validRuntimeReferenceID(h.DaemonID) || !imageSHA256.MatchString(h.ImageID) || !imageSHA256.MatchString(h.ExecutableSHA256) || len(h.SourceCommit) != 40 || strings.Trim(h.SourceCommit, "0123456789abcdef") != "" {
		return ErrInvalidStorageArchive
	}
	expected := h
	expected.Hash = ""
	if h.Hash != HostingDocumentHash(expected) {
		return ErrInvalidStorageArchive
	}
	return nil
}

// HostingStorageArchiveLimits carries every codec security limit explicitly.
// No field is optional and zero never means unbounded or a default.
type HostingStorageArchiveLimits struct {
	ArchiveBytes int64 `json:"archive_bytes"`
	DataBytes    int64 `json:"data_bytes"`
	FileBytes    int64 `json:"file_bytes"`
	Members      int   `json:"members"`
	Depth        int   `json:"depth"`
	PathBytes    int   `json:"path_bytes"`
}

// Validate bounds every helper codec limit without defaults.
func (l HostingStorageArchiveLimits) Validate() error {
	if l.ArchiveBytes < 1536 || l.ArchiveBytes > HostingStorageArchiveMaxBytes || l.DataBytes < 1 || l.DataBytes > l.ArchiveBytes || l.FileBytes < 1 || l.FileBytes > l.DataBytes || l.Members < 1 || l.Members > 1000000 || l.Depth < 1 || l.Depth > 128 || l.PathBytes < 1 || l.PathBytes > 255 {
		return ErrInvalidStorageArchive
	}
	return nil
}

// HostingStorageArchiveBlob is a committed private artifact identity, not a path,
// download URL, backup-location selector, or permission to read arbitrary bytes.
type HostingStorageArchiveBlob struct {
	BackupID      string `json:"backup_id"`
	DestinationID string `json:"destination_id"`
	DaemonID      string `json:"daemon_id"`
	SHA256        string `json:"sha256"`
	SizeBytes     int64  `json:"size_bytes"`
	Format        string `json:"format"`
}

// Validate rejects unbounded, nonlocal or unknown-format artifact metadata.
func (b HostingStorageArchiveBlob) Validate() error {
	if !hostingProxyID.MatchString(b.BackupID) || !hostingProxyID.MatchString(b.DestinationID) || !validRuntimeReferenceID(b.DaemonID) || !imageSHA256.MatchString(b.SHA256) || b.SizeBytes < 1 || b.SizeBytes > HostingStorageArchiveMaxBytes || b.Format != HostingStorageArchiveFormat {
		return ErrInvalidStorageArchive
	}
	return nil
}

// HostingStorageArchiveTarget contains actual immutable metadata created by the
// restore transaction. No routes, triggers, or plaintext configuration are cloned.
type HostingStorageArchiveTarget struct {
	Resource Resource           `json:"resource"`
	Storage  HostingStorage     `json:"storage"`
	Plan     HostingStoragePlan `json:"plan"`
}

// HostingStorageArchivePlan authorizes only this destination-local archive family.
// Writers are controller-derived eligible identities. Their actual initial states
// must be snapshotted durably before the first stop intent is acknowledged.
type HostingStorageArchivePlan struct {
	OperationID           string                                 `json:"operation_id"`
	ProjectID             string                                 `json:"project_id"`
	ServerID              string                                 `json:"server_id"`
	DestinationID         string                                 `json:"destination_id"`
	RunnerID              string                                 `json:"runner_id"`
	Mode                  string                                 `json:"mode"`
	BackupID              string                                 `json:"backup_id"`
	RecoveryOfOperationID string                                 `json:"recovery_of_operation_id,omitempty"`
	RecoveryEvidence      *HostingStorageArchiveRecoveryEvidence `json:"recovery_evidence,omitempty"`
	SourceHealthy         HostingHealthy                         `json:"source_healthy"`
	SourceStorage         HostingStorage                         `json:"source_storage"`
	SourceMaterialization HostingStorageMaterialization          `json:"source_materialization"`
	Writers               []HostingHealthy                       `json:"writers"`
	Target                *HostingStorageArchiveTarget           `json:"target,omitempty"`
	Helper                HostingStorageArchiveHelper            `json:"helper"`
	Limits                HostingStorageArchiveLimits            `json:"limits"`
	Blob                  *HostingStorageArchiveBlob             `json:"blob,omitempty"`
	CompilerVersion       string                                 `json:"compiler_version"`
	PolicyVersion         string                                 `json:"policy_version"`
	Hash                  string                                 `json:"hash"`
}

// HostingStorageArchiveWriterState reports only the exact allowed container and
// its state. Labels and mounts must additionally be inspected locally by the runner.
type HostingStorageArchiveWriterState struct {
	ContainerID string `json:"container_id"`
	OperationID string `json:"operation_id"`
	ImageID     string `json:"image_id"`
	PlanHash    string `json:"plan_hash"`
	ReleaseHash string `json:"release_hash"`
	Running     bool   `json:"running"`
}

// HostingStorageArchiveRecoveryEvidence is controller-derived immutable history,
// never old lease authority. Only durable stop intents create resume obligations.
type HostingStorageArchiveRecoveryEvidence struct {
	OriginalPlanHash           string                             `json:"original_plan_hash"`
	SnapshotReceiptID          string                             `json:"snapshot_receipt_id,omitempty"`
	Writers                    []HostingStorageArchiveWriterState `json:"writers"`
	ResumeRequiredContainerIDs []string                           `json:"resume_required_container_ids"`
}

// HostingStorageArchivePreparedRequest confirms the configured helper identity.
type HostingStorageArchivePreparedRequest struct {
	HostingAuthority
	ReceiptID string                      `json:"receipt_id"`
	PlanHash  string                      `json:"plan_hash"`
	Helper    HostingStorageArchiveHelper `json:"helper"`
}

// HostingStorageArchiveApplyBeginRequest requests durable family-only admission.
type HostingStorageArchiveApplyBeginRequest struct {
	HostingAuthority
	ReceiptID string `json:"receipt_id"`
	PlanHash  string `json:"plan_hash"`
}

// HostingStorageArchiveProgress is immutable fenced phase evidence. Each phase
// has one stable receipt and cannot overwrite an earlier writer snapshot.
type HostingStorageArchiveProgress struct {
	HostingAuthority
	ReceiptID         string                             `json:"receipt_id"`
	PlanHash          string                             `json:"plan_hash"`
	Phase             string                             `json:"phase"`
	Writers           []HostingStorageArchiveWriterState `json:"writers,omitempty"`
	HelperContainerID string                             `json:"helper_container_id,omitempty"`
	Blob              *HostingStorageArchiveBlob         `json:"blob,omitempty"`
	RestoreVerified   bool                               `json:"restore_verified"`
}

// HostingStorageArchiveProgressEvidence is safe operation history. Fenced runner
// requests are retained privately; lease IDs, attempts and fences are not exposed.
type HostingStorageArchiveProgressEvidence struct {
	Receipt           HostingReceipt                     `json:"receipt"`
	PlanHash          string                             `json:"plan_hash"`
	Phase             string                             `json:"phase"`
	Writers           []HostingStorageArchiveWriterState `json:"writers,omitempty"`
	HelperContainerID string                             `json:"helper_container_id,omitempty"`
	Blob              *HostingStorageArchiveBlob         `json:"blob,omitempty"`
	RestoreVerified   bool                               `json:"restore_verified"`
}

// HostingStorageArchiveResult never implies that an application is Ready.
type HostingStorageArchiveResult struct {
	ArchiveOutcome  string                     `json:"archive_outcome"`
	ResumeOutcome   string                     `json:"resume_outcome"`
	Blob            *HostingStorageArchiveBlob `json:"blob,omitempty"`
	RestoreVerified bool                       `json:"restore_verified"`
	Uncertain       bool                       `json:"uncertain"`
	FailureCode     string                     `json:"failure_code,omitempty"`
}

// HostingStorageArchiveCompleteRequest binds the independent result to its plan.
type HostingStorageArchiveCompleteRequest struct {
	HostingAuthority
	ReceiptID string                      `json:"receipt_id"`
	PlanHash  string                      `json:"plan_hash"`
	Result    HostingStorageArchiveResult `json:"result"`
}

// HostingStorageArchive is a typed sidecar. Generic image mutation endpoints
// reject its presence and never decode it as HostingApplyPlan or HostingResult.
type HostingStorageArchive struct {
	Mode                  string                                  `json:"mode"`
	BackupID              string                                  `json:"backup_id"`
	RestoreID             string                                  `json:"restore_id,omitempty"`
	RecoveryOfOperationID string                                  `json:"recovery_of_operation_id,omitempty"`
	Plan                  HostingStorageArchivePlan               `json:"plan"`
	PreparedReceipt       *HostingReceipt                         `json:"prepared_receipt,omitempty"`
	Progress              []HostingStorageArchiveProgressEvidence `json:"progress"`
	Result                *HostingStorageArchiveResult            `json:"result,omitempty"`
}

// HostingStorageBackup exposes metadata only. Bytes remain private to the daemon.
type HostingStorageBackup struct {
	ID          string                     `json:"id"`
	ProjectID   string                     `json:"project_id"`
	ResourceID  string                     `json:"resource_id"`
	OperationID string                     `json:"operation_id"`
	State       string                     `json:"state"`
	Blob        *HostingStorageArchiveBlob `json:"blob,omitempty"`
	CreatedBy   string                     `json:"created_by"`
	CreatedAt   time.Time                  `json:"created_at"`
}

// HostingStorageRestore returns the new held resource and its exact operation.
type HostingStorageRestore struct {
	Resource  Resource         `json:"resource"`
	Storage   HostingStorage   `json:"storage"`
	Operation HostingOperation `json:"operation"`
}

// ValidateStorageArchiveTask forbids generic execution and nested task payloads.
func ValidateStorageArchiveTask(run TaskRun) error {
	s := run.RunSpec
	if s.Type != RunTypeStorageArchive || !hostingProxyID.MatchString(s.HostingOperationID) || s.HostingBuildID != "" || s.ProxyOperationID != "" || s.ProxyMode != "" || s.Process != nil || s.Repository != nil || s.Workflow != nil || len(s.Inputs) != 0 || len(s.Secrets) != 0 || len(s.Artifacts) != 0 || len(run.Workflow.Steps) != 0 {
		return ErrInvalidStorageArchive
	}
	prefix := ""
	switch s.HostingMode {
	case "backup":
		prefix = "backup_"
	case "restore":
		prefix = "restore_"
	case "recover":
		prefix = "archive_recover_"
	default:
		return ErrInvalidStorageArchive
	}
	if run.ID != prefix+s.HostingOperationID {
		return ErrInvalidStorageArchive
	}
	return nil
}

// Validate enforces source, helper, target, and origin identity before hashing.
func (p HostingStorageArchivePlan) Validate() error {
	for _, id := range []string{p.OperationID, p.ProjectID, p.ServerID, p.DestinationID, p.RunnerID, p.BackupID} {
		if !hostingProxyID.MatchString(id) {
			return ErrInvalidStorageArchive
		}
	}
	if p.CompilerVersion != HostingStorageArchiveVersion || p.PolicyVersion != HostingStorageArchiveVersion || p.Helper.Validate() != nil || p.Limits.Validate() != nil || p.SourceStorage.Validate() != nil || p.SourceMaterialization.Validate(p.SourceStorage) != nil || p.SourceMaterialization.State != "materialized" || p.SourceMaterialization.MaterializedReceipt == nil {
		return ErrInvalidStorageArchive
	}
	h := p.SourceHealthy
	s := h.Snapshot
	if h.Storage != nil && (h.Storage.Plan.CreationOperationID != p.SourceMaterialization.CreationOperationID || h.Storage.Plan.CreationNonce != p.SourceMaterialization.CreationNonce || h.Storage.Plan.DaemonID != p.SourceMaterialization.DaemonID) {
		return ErrInvalidStorageArchive
	}
	if h.Storage != nil && (h.Storage.Plan.MountPath != p.SourceStorage.MountPath || h.Storage.Plan.VolumeName != p.SourceStorage.VolumeName) {
		return ErrInvalidStorageArchive
	}
	if ValidateHostingRelease(h.Release) != nil || ValidateHostingHealthyStorage(h) != nil || h.Storage == nil || s.PolicyVersion != HostingOwnedStoragePolicyVersion || s.Database != (HostingDatabaseExecutionRef{}) || s.LocalBuild != (HostingLocalBuildSource{}) || s.RuntimeConfiguration != (HostingRuntimeConfigurationRef{}) || p.SourceStorage.ProjectID != p.ProjectID || p.SourceStorage.ResourceID != s.ResourceID || p.SourceStorage.BindingID != s.BindingID || p.SourceStorage.DestinationID != p.DestinationID || s.ServerID != p.ServerID || s.RunnerID != p.RunnerID || s.DestinationID != p.DestinationID || p.Helper.DaemonID != h.Release.DaemonID || p.SourceMaterialization.DaemonID != p.Helper.DaemonID || p.SourceMaterialization.StorageRef != s.Storage || p.SourceStorage.Reference() != s.Storage || h.Storage.CreationPlanHash != p.SourceMaterialization.CreationPlanHash {
		return ErrInvalidStorageArchive
	}
	// A new restore target grants no authority to stop source writers. Recovery
	// preserves the original obligations, determined by whether a target exists.
	if len(p.Writers) > 64 || p.Target == nil && len(p.Writers) < 1 || p.Target != nil && len(p.Writers) != 0 {
		return ErrInvalidStorageArchive
	}
	last := ""
	found := false
	for _, w := range p.Writers {
		if w.ContainerID <= last || !hostingProxyID.MatchString(w.ContainerID) || !hostingProxyID.MatchString(w.OperationID) || !imageSHA256.MatchString(w.PlanHash) || ValidateHostingRelease(w.Release) != nil || ValidateHostingHealthyStorage(w) != nil || w.Storage == nil || w.Storage.Plan != h.Storage.Plan || w.Storage.CreationPlanHash != h.Storage.CreationPlanHash || w.Snapshot.ResourceID != s.ResourceID || w.Snapshot.BindingID != s.BindingID || w.Snapshot.DestinationID != s.DestinationID || w.Snapshot.ServerID != s.ServerID || w.Snapshot.RunnerID != s.RunnerID || w.Release.DaemonID != h.Release.DaemonID || w.Snapshot.Database != (HostingDatabaseExecutionRef{}) || w.Snapshot.LocalBuild != (HostingLocalBuildSource{}) {
			return ErrInvalidStorageArchive
		}
		last = w.ContainerID
		if w.ContainerID == h.ContainerID && HostingDocumentHash(w) == HostingDocumentHash(h) {
			found = true
		}
	}
	if p.Target == nil && !found {
		return ErrInvalidStorageArchive
	}
	switch p.Mode {
	case "backup":
		if p.Target != nil || p.Blob != nil || p.RecoveryOfOperationID != "" || p.RecoveryEvidence != nil {
			return ErrInvalidStorageArchive
		}
	case "restore":
		if p.Target == nil || p.Blob == nil || p.RecoveryOfOperationID != "" || p.RecoveryEvidence != nil {
			return ErrInvalidStorageArchive
		}
	case "recover":
		if !hostingProxyID.MatchString(p.RecoveryOfOperationID) || p.RecoveryOfOperationID == p.OperationID || p.RecoveryEvidence == nil || p.RecoveryEvidence.Validate(p.Writers) != nil {
			return ErrInvalidStorageArchive
		}
	default:
		return ErrInvalidStorageArchive
	}
	if p.Blob != nil && (p.Blob.Validate() != nil || p.Blob.BackupID != p.BackupID || p.Blob.DestinationID != p.DestinationID || p.Blob.DaemonID != p.Helper.DaemonID || p.Blob.SizeBytes > p.Limits.ArchiveBytes) {
		return ErrInvalidStorageArchive
	}
	if p.Target != nil {
		t := p.Target
		if t.Storage.BindingID == p.SourceStorage.BindingID || t.Storage.ID == p.SourceStorage.ID || t.Storage.VolumeName == p.SourceStorage.VolumeName {
			return ErrInvalidStorageArchive
		}
		origin := p.OperationID
		if p.Mode == "recover" {
			origin = p.RecoveryOfOperationID
		}
		if t.Plan.CreationOperationID != origin {
			return ErrInvalidStorageArchive
		}
		if t.Storage.Validate() != nil || t.Resource.ID == s.ResourceID || t.Resource.ProjectID != p.ProjectID || t.Resource.Kind != "application" || t.Resource.Source.Kind != "image" || t.Resource.Source.Image.Reference != h.Release.CanonicalImage || t.Storage.ResourceID != t.Resource.ID || t.Storage.ProjectID != p.ProjectID || t.Storage.DestinationID != p.DestinationID || t.Storage.MountPath != p.SourceStorage.MountPath || t.Plan.Reference != (HostingStorageRef{StorageID: t.Storage.ID, Generation: t.Storage.Generation, Hash: t.Storage.Hash}) || t.Plan.VolumeName != t.Storage.VolumeName || t.Plan.MountPath != t.Storage.MountPath || t.Plan.CreationOperationID == "" || t.Plan.DaemonID != p.Helper.DaemonID || t.Plan.CreationNonce != HostingStorageCreationNonce(t.Plan.Reference, t.Plan.CreationOperationID, t.Plan.DaemonID) {
			return ErrInvalidStorageArchive
		}
	}
	expected := p
	expected.Hash = ""
	if p.Hash != HostingDocumentHash(expected) {
		return ErrInvalidStorageArchive
	}
	return nil
}

// Validate requires canonical observed writer identities and a running-only
// resume subset. Proven stop intent and resumed evidence are controller checks.
func (e HostingStorageArchiveRecoveryEvidence) Validate(eligible []HostingHealthy) error {
	if !imageSHA256.MatchString(e.OriginalPlanHash) || len(e.Writers) > 64 || len(e.ResumeRequiredContainerIDs) > len(e.Writers) {
		return ErrInvalidStorageArchive
	}
	if len(e.Writers) == 0 {
		if e.SnapshotReceiptID != "" || len(e.ResumeRequiredContainerIDs) != 0 {
			return ErrInvalidStorageArchive
		}
		return nil
	}
	if !validRuntimeReferenceID(e.SnapshotReceiptID) || len(e.Writers) != len(eligible) {
		return ErrInvalidStorageArchive
	}
	running := map[string]bool{}
	for i, w := range e.Writers {
		h := eligible[i]
		if w.ContainerID != h.ContainerID || w.OperationID != h.OperationID || w.ImageID != h.Release.ImageID || w.PlanHash != h.PlanHash || w.ReleaseHash != h.Release.Hash {
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

func validStorageArchiveAuthority(a HostingAuthority, receipt, hash string) bool {
	return hostingProxyID.MatchString(a.OperationID) && hostingProxyID.MatchString(a.RunID) && validRuntimeReferenceID(a.LeaseID) && validRuntimeReferenceID(a.Fence) && a.Attempt > 0 && a.Attempt <= math.MaxInt32 && validRuntimeReferenceID(receipt) && imageSHA256.MatchString(hash)
}

// Validate checks document shape; the controller rechecks live authority.
func (in HostingStorageArchivePreparedRequest) Validate() error {
	if !validStorageArchiveAuthority(in.HostingAuthority, in.ReceiptID, in.PlanHash) || in.Helper.Validate() != nil {
		return ErrInvalidStorageArchive
	}
	return nil
}

// Validate checks the fenced, plan-bound admission receipt shape.
func (in HostingStorageArchiveApplyBeginRequest) Validate() error {
	if !validStorageArchiveAuthority(in.HostingAuthority, in.ReceiptID, in.PlanHash) {
		return ErrInvalidStorageArchive
	}
	return nil
}

// Validate accepts only closed phase and bounded nonsecret observation fields.
func (in HostingStorageArchiveProgress) Validate() error {
	if !validStorageArchiveAuthority(in.HostingAuthority, in.ReceiptID, in.PlanHash) || !slices.Contains([]string{"writer_snapshot", "stop_intent", "stopped", "helper_intent", "helper_identified", "archive_published", "restore_verified", "resume_intent", "resumed"}, in.Phase) || len(in.Writers) > 64 {
		return ErrInvalidStorageArchive
	}
	last := ""
	for _, w := range in.Writers {
		if w.ContainerID <= last || !hostingProxyID.MatchString(w.ContainerID) || !hostingProxyID.MatchString(w.OperationID) || !imageSHA256.MatchString(w.ImageID) || !imageSHA256.MatchString(w.PlanHash) || !imageSHA256.MatchString(w.ReleaseHash) {
			return ErrInvalidStorageArchive
		}
		last = w.ContainerID
	}
	if in.HelperContainerID != "" && !hostingProxyID.MatchString(in.HelperContainerID) {
		return ErrInvalidStorageArchive
	}
	if in.Blob != nil && in.Blob.Validate() != nil {
		return ErrInvalidStorageArchive
	}
	return nil
}

// Validate accepts only closed outcomes, failure codes and artifact metadata.
func (in HostingStorageArchiveCompleteRequest) Validate() error {
	if !validStorageArchiveAuthority(in.HostingAuthority, in.ReceiptID, in.PlanHash) || !slices.Contains([]string{"succeeded", "failed", "not_attempted"}, in.Result.ArchiveOutcome) || !slices.Contains([]string{"resumed", "not_needed", "uncertain"}, in.Result.ResumeOutcome) || !slices.Contains([]string{"", "authority_lost", "ownership_mismatch", "helper_failed", "archive_invalid", "limit_exceeded", "resume_failed", "canceled", "interrupted"}, in.Result.FailureCode) || in.Result.Blob != nil && in.Result.Blob.Validate() != nil {
		return ErrInvalidStorageArchive
	}
	return nil
}

// DecodeHostingStorageArchiveJSON accepts a bounded closed document, rejecting
// unknown fields, duplicate keys and case aliases before typed validation.
func DecodeHostingStorageArchiveJSON(raw []byte, out any) error {
	if !validRuntimeJSONUnicode(raw) || DecodeHostingStorageProtocolJSON(raw, out) != nil {
		return ErrInvalidStorageArchive
	}
	return nil
}

package domain

import (
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

// HostingStorageBackupRetentionVersion and related constants identify storage backup retention documents and capabilities.
const (
	HostingStorageBackupRetentionVersion    = "owned-upload.backup-retention.v1"
	HostingStorageBackupRetentionCapability = "hosting_storage_backup_retention_v1"
	RunTypeStorageBackupRetention           = "storage_backup_retention"
)

// ErrInvalidStorageBackupRetention reports invalid storage backup retention input.
var ErrInvalidStorageBackupRetention = errors.New("invalid storage backup retention request")

// HostingStorageBackupRetentionInput describes the requested storage backup retention.
type HostingStorageBackupRetentionInput struct {
	RequestID                  string                `json:"request_id"`
	ExpectedRevision           int64                 `json:"expected_revision"`
	ExpectedResourceGeneration int64                 `json:"expected_resource_generation"`
	Enabled                    bool                  `json:"enabled"`
	Automatic                  bool                  `json:"automatic"`
	Local                      BackupRetentionLimits `json:"local"`
	Remote                     BackupRetentionLimits `json:"remote"`
}

// Validate checks the storage backup retention input fields and invariants.
func (in HostingStorageBackupRetentionInput) Validate() error {
	if !validRuntimeReferenceID(in.RequestID) || in.ExpectedRevision < 0 || in.ExpectedResourceGeneration < 1 || in.Local.Validate() != nil || in.Remote.Validate() != nil {
		return ErrInvalidStorageBackupRetention
	}
	return nil
}

// HostingStorageBackupRetentionPolicy records the configured storage backup retention rules.
type HostingStorageBackupRetentionPolicy struct {
	Storage            HostingStorageRef     `json:"storage"`
	BindingID          string                `json:"binding_id"`
	DestinationID      string                `json:"destination_id"`
	Version            string                `json:"version"`
	ProjectID          string                `json:"project_id"`
	ResourceID         string                `json:"resource_id"`
	Revision           int64                 `json:"revision"`
	ResourceGeneration int64                 `json:"resource_generation"`
	Enabled            bool                  `json:"enabled"`
	Automatic          bool                  `json:"automatic"`
	Local              BackupRetentionLimits `json:"local"`
	Remote             BackupRetentionLimits `json:"remote"`
	CreatedAt          time.Time             `json:"created_at"`
	UpdatedAt          time.Time             `json:"updated_at"`
}

// HostingStorageBackupRetentionPreviewInput describes the requested storage backup retention
// preview.
type HostingStorageBackupRetentionPreviewInput struct {
	RequestID              string `json:"request_id"`
	ExpectedPolicyRevision int64  `json:"expected_policy_revision"`
}

// Validate checks the storage backup retention preview input fields and invariants.
func (in HostingStorageBackupRetentionPreviewInput) Validate() error {
	if !validRuntimeReferenceID(in.RequestID) || in.ExpectedPolicyRevision < 1 {
		return ErrInvalidStorageBackupRetention
	}
	return nil
}

// HostingStorageBackupRetentionPreview records a saved storage backup retention selection.
type HostingStorageBackupRetentionPreview struct {
	Origin             string                `json:"origin,omitempty"`
	BackupID           string                `json:"backup_id,omitempty"`
	ResourceGeneration int64                 `json:"resource_generation,omitempty"`
	FinalCopy          bool                  `json:"final_copy,omitempty"`
	ID                 string                `json:"id"`
	Version            string                `json:"version"`
	ProjectID          string                `json:"project_id"`
	ResourceID         string                `json:"resource_id"`
	PolicyRevision     int64                 `json:"policy_revision"`
	PlanHash           string                `json:"plan_hash"`
	EvaluatedAt        time.Time             `json:"evaluated_at"`
	LocalReferenceAt   *time.Time            `json:"local_reference_at"`
	RemoteReferenceAt  *time.Time            `json:"remote_reference_at"`
	SelectionTruncated bool                  `json:"selection_truncated"`
	Items              []BackupRetentionItem `json:"items"`
	CreatedAt          time.Time             `json:"created_at"`
}

// HostingStorageBackupRetentionExecutionInput describes the requested storage backup retention
// execution.
type HostingStorageBackupRetentionExecutionInput struct {
	RequestID              string `json:"request_id"`
	PreviewID              string `json:"preview_id"`
	ExpectedPolicyRevision int64  `json:"expected_policy_revision"`
	ExpectedPlanHash       string `json:"expected_plan_hash"`
}

// Validate checks the storage backup retention execution input fields and invariants.
func (in HostingStorageBackupRetentionExecutionInput) Validate() error {
	if !validRuntimeReferenceID(in.RequestID) || !validRuntimeReferenceID(in.PreviewID) || in.ExpectedPolicyRevision < 1 || !imageSHA256.MatchString(in.ExpectedPlanHash) {
		return ErrInvalidStorageBackupRetention
	}
	return nil
}

// HostingStorageBackupRetentionExecution records the state of a storage backup retention batch.
type HostingStorageBackupRetentionExecution struct {
	Origin             string                         `json:"origin,omitempty"`
	BackupID           string                         `json:"backup_id,omitempty"`
	ResourceGeneration int64                          `json:"resource_generation,omitempty"`
	FinalCopy          bool                           `json:"final_copy,omitempty"`
	ID                 string                         `json:"id"`
	Version            string                         `json:"version"`
	ProjectID          string                         `json:"project_id"`
	ResourceID         string                         `json:"resource_id"`
	PreviewID          string                         `json:"preview_id"`
	PolicyRevision     int64                          `json:"policy_revision"`
	PlanHash           string                         `json:"plan_hash"`
	State              string                         `json:"state"`
	ReservationHeld    bool                           `json:"reservation_held"`
	LocalState         string                         `json:"local_state"`
	RemoteState        string                         `json:"remote_state"`
	Items              []BackupRetentionExecutionItem `json:"items"`
	CreatedAt          time.Time                      `json:"created_at"`
	UpdatedAt          time.Time                      `json:"updated_at"`
}

// StorageBackupRetentionCopy combines a storage backup copy identity with its deletion evidence.
type StorageBackupRetentionCopy struct {
	Item          BackupRetentionItem                  `json:"item"`
	RunnerID      string                               `json:"runner_id"`
	DestinationID string                               `json:"destination_id"`
	ServerID      string                               `json:"server_id"`
	DaemonID      string                               `json:"daemon_id"`
	IdentityHash  string                               `json:"identity_hash"`
	Storage       HostingStorageRef                    `json:"storage"`
	Local         *BackupLocalCopyProof                `json:"local,omitempty"`
	Remote        *BackupRemoteCopyProof               `json:"remote,omitempty"`
	Target        *HostingDatabaseBackupTargetSnapshot `json:"target,omitempty"`
	ObjectKey     string                               `json:"object_key,omitempty"`
}

// HostingStorageBackupRetentionPlan binds the storage backup retention operation to its execution
// inputs.
type HostingStorageBackupRetentionPlan struct {
	Progress       []BackupRetentionExecutionItem         `json:"progress,omitempty"`
	Operation      HostingStorageBackupRetentionExecution `json:"operation"`
	ActorID        string                                 `json:"actor_id"`
	RunnerID       string                                 `json:"runner_id"`
	Copies         []StorageBackupRetentionCopy           `json:"copies"`
	TimeoutSeconds int                                    `json:"timeout_seconds"`
	Hash           string                                 `json:"hash"`
}

// Validate checks the storage backup retention plan fields and invariants.
func (p HostingStorageBackupRetentionPlan) Validate() error {
	if p.Progress != nil {
		if len(p.Progress) != len(p.Copies) {
			return ErrInvalidStorageBackupRetention
		}
		for i, v := range p.Progress {
			c := p.Copies[i].Item
			if v.CopyID != c.CopyID || v.BackupID != c.BackupID || v.Location != c.Location || !ValidBackupRetentionFailure(v.FailureCode) || !slices.Contains([]string{"pending", "intent", "deleted", "absent", "failed", "uncertain"}, v.State) {
				return ErrInvalidStorageBackupRetention
			}
			if (v.State == "deleted" || v.State == "absent" || v.State == "failed") && v.ReservationHeld {
				return ErrInvalidStorageBackupRetention
			}
		}
	}
	h := p.Hash
	p.Progress = nil
	p.Hash = ""
	if h != HostingDocumentHash(p) || !ValidStorageBackupDeletionOperation(p.Operation) || !validRuntimeReferenceID(p.Operation.ID) || !validRuntimeReferenceID(p.ActorID) || !validRuntimeReferenceID(p.RunnerID) || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 3600 || len(p.Copies) == 0 || len(p.Copies) > BackupRetentionBatchLimit || len(p.Copies) != len(p.Operation.Items) {
		return ErrInvalidStorageBackupRetention
	}
	seen := map[string]bool{}
	for i, c := range p.Copies {
		if p.Operation.Origin == StorageBackupManualSelection && c.Item.BackupID != p.Operation.BackupID {
			return ErrInvalidStorageBackupRetention
		}
		if !validRuntimeReferenceID(c.DestinationID) || !validRuntimeReferenceID(c.ServerID) || !validRuntimeReferenceID(c.DaemonID) || !c.Item.Selected || len(c.Item.ProtectedReasons) > 0 || !validRuntimeReferenceID(c.Storage.StorageID) || c.Storage.Generation < 1 || !imageSHA256.MatchString(c.Storage.Hash) || seen[c.Item.CopyID] || !validRuntimeReferenceID(c.Item.CopyID) || c.RunnerID != p.RunnerID || !imageSHA256.MatchString(c.IdentityHash) || !imageSHA256.MatchString(c.Item.Digest) || c.Item.Bytes < 5 || c.Item.Bytes > HostingStorageArchiveMaxBytes || c.Item.CopyID != p.Operation.Items[i].CopyID {
			return ErrInvalidStorageBackupRetention
		}
		seen[c.Item.CopyID] = true
		if c.Item.Location == "local" {
			if c.Local == nil || c.Local.Validate() != nil || c.Local.Bytes != c.Item.Bytes || !c.Local.Created || c.Remote != nil || c.Target != nil || c.ObjectKey != "" {
				return ErrInvalidStorageBackupRetention
			}
		} else if c.Item.Location == "remote" {
			if c.Remote == nil || c.Remote.Validate() != nil || !c.Remote.Created || c.Local != nil || c.Target == nil || c.Target.Validate() != nil || c.ObjectKey != StorageRemoteObjectKey(c.Item.BackupID, HostingStorageArchiveBlob{BackupID: c.Item.BackupID, DestinationID: c.DestinationID, DaemonID: c.DaemonID, SHA256: c.Item.Digest, SizeBytes: c.Item.Bytes, Format: HostingStorageArchiveFormat}) {
				return ErrInvalidStorageBackupRetention
			}
		} else {
			return ErrInvalidStorageBackupRetention
		}
	}
	return nil
}

// HostingStorageBackupRetentionMutation reports a fenced storage backup retention execution
// outcome.
type HostingStorageBackupRetentionMutation struct {
	ProbeOnly bool   `json:"probe_only,omitempty"`
	ProbeKey  string `json:"probe_key,omitempty"`
	HostingAuthority
	ReceiptID   string `json:"receipt_id"`
	PlanHash    string `json:"plan_hash"`
	CopyID      string `json:"copy_id"`
	Phase       string `json:"phase"`
	FailureCode string `json:"failure_code"`
}

// Validate checks the storage backup retention mutation fields and invariants.
func (in HostingStorageBackupRetentionMutation) Validate() error {
	if in.ProbeOnly {
		prefix := "nerocd/storage/retention-probes/" + in.OperationID + "/"
		if !strings.HasPrefix(in.ProbeKey, prefix) || len(in.ProbeKey) != len(prefix)+32 {
			return ErrInvalidStorageBackupRetention
		}
		for _, r := range strings.TrimPrefix(in.ProbeKey, prefix) {
			if !(r >= 'a' && r <= 'f' || r >= '0' && r <= '9') {
				return ErrInvalidStorageBackupRetention
			}
		}
	} else if in.ProbeKey != "" {
		return ErrInvalidStorageBackupRetention
	}
	if !validRuntimeReferenceID(in.ReceiptID) || !validRuntimeReferenceID(in.CopyID) || !imageSHA256.MatchString(in.PlanHash) || !ValidBackupRetentionFailure(in.FailureCode) || !slices.Contains([]string{"intent", "deleted", "absent", "failed", "uncertain"}, in.Phase) || !validRuntimeReferenceID(in.OperationID) || in.RunID != "storage_retention_"+in.OperationID || !validRuntimeReferenceID(in.LeaseID) || in.Attempt < 1 || in.Attempt > math.MaxInt32 || len(in.Fence) < 1 || len(in.Fence) > 256 {
		return ErrInvalidStorageBackupRetention
	}
	if (in.Phase == "failed" || in.Phase == "uncertain") != (in.FailureCode != "") {
		return ErrInvalidStorageBackupRetention
	}
	return nil
}

// ValidateStorageBackupRetentionTask checks the storage backup retention task contract.
func ValidateStorageBackupRetentionTask(r TaskRun) error {
	s := r.RunSpec
	if s.Type != RunTypeStorageBackupRetention || !validRuntimeReferenceID(s.HostingOperationID) || r.ID != "storage_retention_"+s.HostingOperationID || s.HostingMode != "retention" || s.HostingBuildID != "" || s.ProxyOperationID != "" || s.ProxyMode != "" || s.Process != nil || s.Repository != nil || s.Workflow != nil || len(s.Inputs) != 0 || len(s.Secrets) != 0 || len(s.Artifacts) != 0 || len(r.Workflow.Steps) != 0 {
		return ErrInvalidStorageBackupRetention
	}
	return nil
}

// HostingStorageBackupRetentionRetirementInput requests proof that an audited continuation superseded an
// old attempt. It grants no execution or credential authority to that attempt.
type HostingStorageBackupRetentionRetirementInput struct {
	HostingAuthority
	PlanHash string `json:"plan_hash"`
}

// Validate checks the storage backup retention retirement input fields and invariants.
func (in HostingStorageBackupRetentionRetirementInput) Validate() error {
	if !validRuntimeReferenceID(in.OperationID) || in.RunID != "storage_retention_"+in.OperationID || !validRuntimeReferenceID(in.LeaseID) || in.Attempt < 1 || in.Attempt > math.MaxInt32 || len(in.Fence) < 1 || len(in.Fence) > 256 || !imageSHA256.MatchString(in.PlanHash) {
		return ErrInvalidStorageBackupRetention
	}
	return nil
}

// HostingStorageBackupRetentionRetirement proves that an older storage retention attempt is
// restricted to receipt replay.
type HostingStorageBackupRetentionRetirement struct {
	HostingAuthority
	PlanHash    string `json:"plan_hash"`
	NextAttempt int    `json:"next_attempt"`
	ReplayOnly  bool   `json:"replay_only"`
}

// SelectStorageBackupRetention evaluates a complete successful-copy inventory at
// the persisted preview instant. Count, strict age and cumulative size form a
// union; only explicit ownership, active use and recovery protections override it.
func SelectStorageBackupRetention(p HostingStorageBackupRetentionPolicy, inventory []BackupRetentionItem, at time.Time) (items []BackupRetentionItem, local, remote *time.Time, truncated bool) {
	items = make([]BackupRetentionItem, len(inventory))
	for i, v := range inventory {
		items[i] = v
		items[i].Selected = false
		items[i].ProtectedReasons = slices.Clone(v.ProtectedReasons)
		if items[i].ProtectedReasons == nil {
			items[i].ProtectedReasons = []string{}
		}
	}
	for _, location := range []string{"local", "remote"} {
		indices := []int{}
		for i, v := range items {
			if v.Location == location && v.Availability != "deleted" {
				indices = append(indices, i)
			}
		}
		sort.Slice(indices, func(i, j int) bool {
			a, b := items[indices[i]], items[indices[j]]
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.After(b.CreatedAt)
			}
			return a.CopyID > b.CopyID
		})
		if len(indices) == 0 {
			continue
		}
		reference := items[indices[0]].CreatedAt
		if location == "local" {
			local = &reference
		} else {
			remote = &reference
		}
		limits := p.Local
		if location == "remote" {
			limits = p.Remote
		}
		var total int64
		exceeded := false
		for offset, i := range indices {
			if items[i].Bytes < 0 || total > 1<<62-items[i].Bytes {
				total = 1 << 62
			} else {
				total += items[i].Bytes
			}
			if offset > 0 && limits.MaxBytes > 0 && total > limits.MaxBytes {
				exceeded = true
			}
			selected := p.Enabled && (limits.KeepCount > 0 && offset >= limits.KeepCount || limits.KeepDays > 0 && items[i].CreatedAt.Before(at.AddDate(0, 0, -limits.KeepDays)) || exceeded)
			if selected && items[i].Availability != "available" {
				items[i].ProtectedReasons = appendReason(items[i].ProtectedReasons, "unavailable")
			}
			items[i].Selected = selected && len(items[i].ProtectedReasons) == 0
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if !items[i].CreatedAt.Equal(items[j].CreatedAt) {
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].CopyID < items[j].CopyID
	})
	selected := 0
	for i := range items {
		if items[i].Selected {
			selected++
			if selected > BackupRetentionBatchLimit {
				items[i].Selected = false
				items[i].ProtectedReasons = appendReason(items[i].ProtectedReasons, "batch_limit")
				truncated = true
			}
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Selected && !items[j].Selected })
	if len(items) > BackupRetentionInventoryLimit {
		items = items[:BackupRetentionInventoryLimit]
		truncated = true
	}
	return
}

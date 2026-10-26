package domain

import (
	"errors"
	"math"
	"slices"
	"sort"
	"strings"
	"time"
)

// HostingDatabaseBackupRetentionVersion and related constants identify database backup retention documents and capabilities.
const (
	HostingDatabaseBackupRetentionVersion    = "postgresql.backup-retention.v1"
	HostingDatabaseBackupRetentionCapability = "hosting_postgresql_backup_retention_v1"
	RunTypeDatabaseBackupRetention           = "database_backup_retention"
	BackupRetentionInventoryLimit            = 1000
	BackupRetentionBatchLimit                = 100
)

// ErrInvalidDatabaseBackupRetention reports invalid database backup retention input.
var ErrInvalidDatabaseBackupRetention = errors.New("invalid database backup retention request")

// BackupRetentionLimits sets the count and age limits for retained copies.
type BackupRetentionLimits struct {
	KeepCount int   `json:"keep_count"`
	KeepDays  int   `json:"keep_days"`
	MaxBytes  int64 `json:"max_bytes"`
}

// Validate checks the backup retention limits fields and invariants.
func (l BackupRetentionLimits) Validate() error {
	if l.KeepCount < 0 || l.KeepCount > 10000 || l.KeepDays < 0 || l.KeepDays > 36500 || l.MaxBytes < 0 || l.MaxBytes > 1<<50 {
		return ErrInvalidDatabaseBackupRetention
	}
	return nil
}

// HostingDatabaseBackupRetentionInput describes the requested database backup retention.
type HostingDatabaseBackupRetentionInput struct {
	RequestID                  string                `json:"request_id"`
	ExpectedRevision           int64                 `json:"expected_revision"`
	ExpectedResourceGeneration int64                 `json:"expected_resource_generation"`
	Enabled                    bool                  `json:"enabled"`
	Automatic                  bool                  `json:"automatic"`
	Local                      BackupRetentionLimits `json:"local"`
	Remote                     BackupRetentionLimits `json:"remote"`
}

// Validate checks the database backup retention input fields and invariants.
func (in HostingDatabaseBackupRetentionInput) Validate() error {
	if !validRuntimeReferenceID(in.RequestID) || in.ExpectedRevision < 0 || in.ExpectedResourceGeneration < 1 || in.Local.Validate() != nil || in.Remote.Validate() != nil {
		return ErrInvalidDatabaseBackupRetention
	}
	return nil
}

// HostingDatabaseBackupRetentionPolicy records the configured database backup retention rules.
type HostingDatabaseBackupRetentionPolicy struct {
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

// HostingDatabaseBackupRetentionPreviewInput describes the requested database backup retention
// preview.
type HostingDatabaseBackupRetentionPreviewInput struct {
	RequestID              string `json:"request_id"`
	ExpectedPolicyRevision int64  `json:"expected_policy_revision"`
}

// Validate checks the database backup retention preview input fields and invariants.
func (in HostingDatabaseBackupRetentionPreviewInput) Validate() error {
	if !validRuntimeReferenceID(in.RequestID) || in.ExpectedPolicyRevision < 1 {
		return ErrInvalidDatabaseBackupRetention
	}
	return nil
}

// BackupRetentionItem describes one copy considered by a retention preview.
type BackupRetentionItem struct {
	CopyID           string    `json:"copy_id"`
	BackupID         string    `json:"backup_id"`
	TransferID       string    `json:"transfer_id,omitempty"`
	Location         string    `json:"location"`
	Digest           string    `json:"digest"`
	Bytes            int64     `json:"bytes"`
	CreatedAt        time.Time `json:"created_at"`
	Availability     string    `json:"availability"`
	Selected         bool      `json:"selected"`
	ProtectedReasons []string  `json:"protected_reasons"`
}

// HostingDatabaseBackupRetentionPreview records a saved database backup retention selection.
type HostingDatabaseBackupRetentionPreview struct {
	ID                 string                `json:"id"`
	Version            string                `json:"version"`
	ProjectID          string                `json:"project_id"`
	ResourceID         string                `json:"resource_id"`
	PolicyRevision     int64                 `json:"policy_revision"`
	PlanHash           string                `json:"plan_hash"`
	LocalReferenceAt   *time.Time            `json:"local_reference_at"`
	RemoteReferenceAt  *time.Time            `json:"remote_reference_at"`
	SelectionTruncated bool                  `json:"selection_truncated"`
	Items              []BackupRetentionItem `json:"items"`
	CreatedAt          time.Time             `json:"created_at"`
}

// HostingDatabaseBackupRetentionExecutionInput describes the requested database backup retention
// execution.
type HostingDatabaseBackupRetentionExecutionInput struct {
	RequestID              string `json:"request_id"`
	PreviewID              string `json:"preview_id"`
	ExpectedPolicyRevision int64  `json:"expected_policy_revision"`
	ExpectedPlanHash       string `json:"expected_plan_hash"`
}

// Validate checks the database backup retention execution input fields and invariants.
func (in HostingDatabaseBackupRetentionExecutionInput) Validate() error {
	if !validRuntimeReferenceID(in.RequestID) || !validRuntimeReferenceID(in.PreviewID) || in.ExpectedPolicyRevision < 1 || !imageSHA256.MatchString(in.ExpectedPlanHash) {
		return ErrInvalidDatabaseBackupRetention
	}
	return nil
}

// BackupRetentionExecutionItem records progress and reservation state for a selected copy.
type BackupRetentionExecutionItem struct {
	CopyID          string `json:"copy_id"`
	BackupID        string `json:"backup_id"`
	Location        string `json:"location"`
	State           string `json:"state"`
	FailureCode     string `json:"failure_code"`
	ReservationHeld bool   `json:"reservation_held"`
}

// HostingDatabaseBackupRetentionExecution records the state of a database backup retention batch.
type HostingDatabaseBackupRetentionExecution struct {
	ID              string                         `json:"id"`
	Version         string                         `json:"version"`
	ProjectID       string                         `json:"project_id"`
	ResourceID      string                         `json:"resource_id"`
	PreviewID       string                         `json:"preview_id"`
	PolicyRevision  int64                          `json:"policy_revision"`
	PlanHash        string                         `json:"plan_hash"`
	State           string                         `json:"state"`
	ReservationHeld bool                           `json:"reservation_held"`
	LocalState      string                         `json:"local_state"`
	RemoteState     string                         `json:"remote_state"`
	Items           []BackupRetentionExecutionItem `json:"items"`
	CreatedAt       time.Time                      `json:"created_at"`
	UpdatedAt       time.Time                      `json:"updated_at"`
}

// ValidBackupRetentionFailure reports whether a retention failure code is recognized.
func ValidBackupRetentionFailure(v string) bool {
	return slices.Contains([]string{"", "authority_denied", "scope_changed", "object_changed", "conditional_unsupported", "interrupted", "storage_unavailable", "remote_unavailable"}, v)
}

// SelectBackupRetention consumes the COMPLETE relevant inventory, before applying
// output limits. Callers must never pass a paginated reference/availability view.
// Policy selection mirrors the upstream union of count, age and size selectors;
// safety protections are applied after that union. The oldest eligible copies
// enter the immutable batch first so a bounded response cannot starve old data.
func SelectBackupRetention(p HostingDatabaseBackupRetentionPolicy, inventory []BackupRetentionItem) (items []BackupRetentionItem, local, remote *time.Time, truncated bool) {
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
		var indices []int
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
		newest := items[indices[0]].CreatedAt
		if location == "local" {
			local = &newest
		} else {
			remote = &newest
		}
		limits := p.Local
		if location == "remote" {
			limits = p.Remote
		}
		matched := map[int]bool{}
		if p.Enabled {
			if limits.KeepCount > 0 {
				for _, i := range indices[min(limits.KeepCount, len(indices)):] {
					matched[i] = true
				}
			}
			if limits.KeepDays > 0 {
				cutoff := newest.AddDate(0, 0, -limits.KeepDays)
				for _, i := range indices {
					if items[i].CreatedAt.Before(cutoff) {
						matched[i] = true
					}
				}
			}
			if limits.MaxBytes > 0 {
				var total int64
				for offset, i := range indices[1:] {
					// Bounds on copy bytes and inventory prevent arithmetic overflow.
					if total > limits.MaxBytes-items[i].Bytes {
						total = limits.MaxBytes + 1
					} else {
						total += items[i].Bytes
					}
					if total > limits.MaxBytes {
						// Keep the threshold copy and the entire preceding prefix,
						// including equal timestamps ordered by descending copy ID.
						for _, j := range indices[offset+2:] {
							matched[j] = true
						}
						break
					}
				}
			}
		}
		// The newest known recovery copy is never a retention candidate.
		items[indices[0]].ProtectedReasons = appendReason(items[indices[0]].ProtectedReasons, "newest")
		for _, i := range indices {
			if items[i].Availability == "available" || items[i].Availability == "unproven" {
				items[i].ProtectedReasons = appendReason(items[i].ProtectedReasons, "newest")
				break
			}
		}
		for i := range matched {
			if items[i].Availability != "available" {
				items[i].ProtectedReasons = appendReason(items[i].ProtectedReasons, "unavailable")
			}
			items[i].Selected = len(items[i].ProtectedReasons) == 0
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
	// Selected items are always visible even when many older protected records
	// precede them. Presentation bounds never alter the globally chosen batch.
	sort.SliceStable(items, func(i, j int) bool { return items[i].Selected && !items[j].Selected })
	if len(items) > BackupRetentionInventoryLimit {
		items = items[:BackupRetentionInventoryLimit]
		truncated = true
	}
	return
}
func appendReason(v []string, s string) []string {
	if !slices.Contains(v, s) {
		v = append(v, s)
	}
	return v
}

// BackupLocalCopyProof is runner-only persisted evidence. It contains no local path or
// credential value. Created=false is adoption and cannot authorize deletion.
type BackupLocalCopyProof struct {
	PoolID  string `json:"pool_id"`
	Device  uint64 `json:"device"`
	Inode   uint64 `json:"inode"`
	Bytes   int64  `json:"bytes"`
	Created bool   `json:"created"`
}

// Validate checks the backup local copy proof fields and invariants.
func (p BackupLocalCopyProof) Validate() error {
	if !imageSHA256.MatchString(p.PoolID) || p.Inode == 0 || p.Bytes < 5 || p.Bytes > HostingDatabaseBackupMaxBytes {
		return ErrInvalidDatabaseBackupRetention
	}
	return nil
}

// BackupRemoteCopyProof records runner evidence for one remote backup object.
type BackupRemoteCopyProof struct {
	ETag      string `json:"etag"`
	VersionID string `json:"version_id"`
	Created   bool   `json:"created"`
}

// Validate checks the backup remote copy proof fields and invariants.
func (p BackupRemoteCopyProof) Validate() error {
	if len(p.ETag) < 3 || len(p.ETag) > 256 || len(p.VersionID) > 1024 {
		return ErrInvalidDatabaseBackupRetention
	}
	for _, s := range []string{p.ETag, p.VersionID} {
		for _, r := range s {
			if r < 32 || r == 127 {
				return ErrInvalidDatabaseBackupRetention
			}
		}
	}
	return nil
}

// BackupRetentionCopy combines a database backup copy identity with its deletion evidence.
type BackupRetentionCopy struct {
	Item          BackupRetentionItem                  `json:"item"`
	RunnerID      string                               `json:"runner_id"`
	DestinationID string                               `json:"destination_id"`
	ServerID      string                               `json:"server_id"`
	DaemonID      string                               `json:"daemon_id"`
	IdentityHash  string                               `json:"identity_hash"`
	Local         *BackupLocalCopyProof                `json:"local,omitempty"`
	Remote        *BackupRemoteCopyProof               `json:"remote,omitempty"`
	Target        *HostingDatabaseBackupTargetSnapshot `json:"target,omitempty"`
	ObjectKey     string                               `json:"object_key,omitempty"`
}

// HostingDatabaseBackupRetentionPlan binds the database backup retention operation to its
// execution inputs.
type HostingDatabaseBackupRetentionPlan struct {
	Progress       []BackupRetentionExecutionItem          `json:"progress,omitempty"`
	Operation      HostingDatabaseBackupRetentionExecution `json:"operation"`
	ActorID        string                                  `json:"actor_id"`
	RunnerID       string                                  `json:"runner_id"`
	Copies         []BackupRetentionCopy                   `json:"copies"`
	TimeoutSeconds int                                     `json:"timeout_seconds"`
	Hash           string                                  `json:"hash"`
}

// Validate checks the database backup retention plan fields and invariants.
func (p HostingDatabaseBackupRetentionPlan) Validate() error {
	if p.Progress != nil {
		if len(p.Progress) != len(p.Copies) {
			return ErrInvalidDatabaseBackupRetention
		}
		for i, v := range p.Progress {
			c := p.Copies[i].Item
			if v.CopyID != c.CopyID || v.BackupID != c.BackupID || v.Location != c.Location || !ValidBackupRetentionFailure(v.FailureCode) || !slices.Contains([]string{"pending", "intent", "deleted", "absent", "failed", "uncertain"}, v.State) {
				return ErrInvalidDatabaseBackupRetention
			}
			if (v.State == "deleted" || v.State == "absent" || v.State == "failed") && v.ReservationHeld {
				return ErrInvalidDatabaseBackupRetention
			}
		}
	}
	h := p.Hash
	p.Progress = nil
	p.Hash = ""
	if h != HostingDocumentHash(p) || p.Operation.Version != HostingDatabaseBackupRetentionVersion || !validRuntimeReferenceID(p.Operation.ID) || !validRuntimeReferenceID(p.ActorID) || !validRuntimeReferenceID(p.RunnerID) || p.TimeoutSeconds < 1 || p.TimeoutSeconds > 3600 || len(p.Copies) == 0 || len(p.Copies) > BackupRetentionBatchLimit || len(p.Copies) != len(p.Operation.Items) {
		return ErrInvalidDatabaseBackupRetention
	}
	seen := map[string]bool{}
	for i, c := range p.Copies {
		if seen[c.Item.CopyID] || !validRuntimeReferenceID(c.Item.CopyID) || c.RunnerID != p.RunnerID || !imageSHA256.MatchString(c.IdentityHash) || !imageSHA256.MatchString(c.Item.Digest) || c.Item.Bytes < 5 || c.Item.Bytes > HostingDatabaseBackupMaxBytes || c.Item.CopyID != p.Operation.Items[i].CopyID {
			return ErrInvalidDatabaseBackupRetention
		}
		seen[c.Item.CopyID] = true
		switch c.Item.Location {
		case "local":
			if c.Local == nil || c.Local.Validate() != nil || !c.Local.Created || c.Remote != nil || c.Target != nil || c.ObjectKey != "" {
				return ErrInvalidDatabaseBackupRetention
			}
		case "remote":
			if c.Remote == nil || c.Remote.Validate() != nil || !c.Remote.Created || c.Local != nil || c.Target == nil || c.Target.Validate() != nil || c.ObjectKey != BackupRemoteObjectKey(c.Item.BackupID, HostingDatabaseBackupBlob{Digest: c.Item.Digest, Bytes: c.Item.Bytes, Format: "postgresql_custom"}) {
				return ErrInvalidDatabaseBackupRetention
			}
		default:
			return ErrInvalidDatabaseBackupRetention
		}
	}
	return nil
}

// HostingDatabaseBackupRetentionMutation reports a fenced database backup retention execution
// outcome.
type HostingDatabaseBackupRetentionMutation struct {
	ProbeOnly bool   `json:"probe_only,omitempty"`
	ProbeKey  string `json:"probe_key,omitempty"`
	HostingAuthority
	ReceiptID   string `json:"receipt_id"`
	PlanHash    string `json:"plan_hash"`
	CopyID      string `json:"copy_id"`
	Phase       string `json:"phase"`
	FailureCode string `json:"failure_code"`
}

// Validate checks the database backup retention mutation fields and invariants.
func (in HostingDatabaseBackupRetentionMutation) Validate() error {
	if in.ProbeOnly {
		prefix := "nerocd/postgresql/retention-probes/" + in.OperationID + "/"
		if !strings.HasPrefix(in.ProbeKey, prefix) || len(in.ProbeKey) != len(prefix)+32 {
			return ErrInvalidDatabaseBackupRetention
		}
		for _, r := range strings.TrimPrefix(in.ProbeKey, prefix) {
			if (r < 'a' || r > 'f') && (r < '0' || r > '9') {
				return ErrInvalidDatabaseBackupRetention
			}
		}
	} else if in.ProbeKey != "" {
		return ErrInvalidDatabaseBackupRetention
	}
	if !validRuntimeReferenceID(in.ReceiptID) || !validRuntimeReferenceID(in.CopyID) || !imageSHA256.MatchString(in.PlanHash) || !ValidBackupRetentionFailure(in.FailureCode) || !slices.Contains([]string{"intent", "deleted", "absent", "failed", "uncertain"}, in.Phase) || !validRuntimeReferenceID(in.OperationID) || in.RunID != "db_retention_"+in.OperationID || !validRuntimeReferenceID(in.LeaseID) || in.Attempt < 1 || in.Attempt > math.MaxInt32 || len(in.Fence) < 1 || len(in.Fence) > 256 {
		return ErrInvalidDatabaseBackupRetention
	}
	if (in.Phase == "failed" || in.Phase == "uncertain") != (in.FailureCode != "") {
		return ErrInvalidDatabaseBackupRetention
	}
	return nil
}

// ValidateDatabaseBackupRetentionTask checks the database backup retention task contract.
func ValidateDatabaseBackupRetentionTask(r TaskRun) error {
	s := r.RunSpec
	if s.Type != RunTypeDatabaseBackupRetention || !validRuntimeReferenceID(s.HostingOperationID) || r.ID != "db_retention_"+s.HostingOperationID || s.HostingMode != "retention" || s.HostingBuildID != "" || s.ProxyOperationID != "" || s.ProxyMode != "" || s.Process != nil || s.Repository != nil || s.Workflow != nil || len(s.Inputs) != 0 || len(s.Secrets) != 0 || len(s.Artifacts) != 0 || len(r.Workflow.Steps) != 0 {
		return ErrInvalidDatabaseBackupRetention
	}
	return nil
}

// HostingDatabaseBackupRetentionRetirementInput requests proof that an audited continuation superseded an
// old attempt. It grants no execution or credential authority to that attempt.
type HostingDatabaseBackupRetentionRetirementInput struct {
	HostingAuthority
	PlanHash string `json:"plan_hash"`
}

// Validate checks the database backup retention retirement input fields and invariants.
func (in HostingDatabaseBackupRetentionRetirementInput) Validate() error {
	if !validRuntimeReferenceID(in.OperationID) || in.RunID != "db_retention_"+in.OperationID || !validRuntimeReferenceID(in.LeaseID) || in.Attempt < 1 || in.Attempt > math.MaxInt32 || len(in.Fence) < 1 || len(in.Fence) > 256 || !imageSHA256.MatchString(in.PlanHash) {
		return ErrInvalidDatabaseBackupRetention
	}
	return nil
}

// HostingDatabaseBackupRetentionRetirement proves that an older retention attempt is restricted
// to receipt replay.
type HostingDatabaseBackupRetentionRetirement struct {
	HostingAuthority
	PlanHash    string `json:"plan_hash"`
	NextAttempt int    `json:"next_attempt"`
	ReplayOnly  bool   `json:"replay_only"`
}

package runner

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"nerocd/internal/domain"
)

// JournalHosting is nonsecret immutable prepare, apply-intent, or result
// evidence. Apply intent is never discarded merely because its lease is stale.
type JournalHosting struct {
	DatabaseUpgrade      *domain.HostingDatabaseUpgradeProof       `json:"database_upgrade,omitempty"`
	Database             bool                                      `json:"database,omitempty"`
	DatabaseRef          domain.HostingDatabaseExecutionRef        `json:"database_ref,omitzero"`
	StorageRef           domain.HostingStorageRef                  `json:"storage_ref,omitzero"`
	StorageCreate        *domain.HostingStorageCreateRequest       `json:"storage_create,omitempty"`
	StorageMaterialized  *domain.HostingStorageMaterializedRequest `json:"storage_materialized,omitempty"`
	StorageMetadata      *domain.HostingStorage                    `json:"storage_metadata,omitempty"`
	RuntimeEnvironment   bool                                      `json:"runtime_environment,omitempty"`
	RuntimeConfiguration domain.HostingRuntimeConfigurationRef     `json:"runtime_configuration,omitzero"`
	DeliveryID           string                                    `json:"delivery_id,omitempty"`
	LocalBuild           bool                                      `json:"local_build,omitempty"`
	ID                   string                                    `json:"id"`
	Attempt              AttemptIdentity                           `json:"attempt_authority"`
	OperationID          string                                    `json:"operation_id"`
	Phase                string                                    `json:"phase"`
	Prepared             *domain.HostingPreparedRequest            `json:"prepared,omitempty"`
	CleanupPlan          *domain.HostingCleanupPlan                `json:"cleanup_plan,omitempty"`
	Plan                 *domain.HostingApplyPlan                  `json:"plan,omitempty"`
	Result               *domain.HostingCompleteRequest            `json:"result,omitempty"`
	CreatedAt            time.Time                                 `json:"created_at"`
}

// AppendHosting durably records nonsecret hosting evidence before external mutation or delivery.
func (j *AttemptJournal) AppendHosting(entry JournalHosting) (JournalHosting, error) {
	if hostingJournalHasLocalArtifact(entry) {
		entry.LocalBuild = true
	}
	if err := validateJournalHosting(entry); err != nil {
		return JournalHosting{}, err
	}
	entry = cloneJournalHosting(entry)
	j.mu.Lock()
	defer j.mu.Unlock()
	raw, err := json.Marshal(entry)
	if err != nil {
		return JournalHosting{}, err
	}
	for _, old := range j.state.Hosting {
		if old.ID == entry.ID {
			oldRaw, err := json.Marshal(old)
			if err != nil {
				return JournalHosting{}, err
			}
			if !bytes.Equal(raw, oldRaw) {
				return JournalHosting{}, ErrJournalConflict
			}
			return cloneJournalHosting(old), nil
		}
	}
	if j.entryCountLocked() >= journalMaxEntries {
		return JournalHosting{}, errors.New("runner journal entry limit reached")
	}
	if err := validateDatabaseUpgradeJournalChain(append(slices.Clone(j.state.Hosting), entry)); err != nil {
		return JournalHosting{}, err
	}
	oldVersion := j.state.Version
	if j.state.Version < 2 {
		j.state.Version = 2
	}
	if hostingJournalNeedsV3(entry) && j.state.Version < 3 {
		j.state.Version = 3
	} // Older binaries reject retained release and cleanup authority.
	if HostingJournalUsesLocalBuild(entry) && j.state.Version < 6 {
		j.state.Version = 6
	}
	if HostingJournalUsesRuntimeEnvironment(entry) && j.state.Version < 8 {
		j.state.Version = 8
	}
	if HostingJournalUsesStorage(entry) && j.state.Version < 10 {
		j.state.Version = 10
	}
	if HostingJournalUsesDatabase(entry) && j.state.Version < 11 {
		j.state.Version = 11
	}
	if entry.DatabaseUpgrade != nil && j.state.Version < 14 {
		j.state.Version = 14
	}
	j.state.Hosting = append(j.state.Hosting, entry)
	if err = j.persistLocked(); err != nil {
		j.state.Hosting = j.state.Hosting[:len(j.state.Hosting)-1]
		j.state.Version = oldVersion
		return JournalHosting{}, err
	}
	return cloneJournalHosting(entry), nil
}

// HostingJournalUsesLocalBuild detects local evidence at every retained level.
// The explicit marker protects result-only records; it grants no authority.
func HostingJournalUsesLocalBuild(entry JournalHosting) bool {
	return entry.LocalBuild || hostingJournalHasLocalArtifact(entry)
}

func hostingJournalHasLocalArtifact(entry JournalHosting) bool {
	healthy := func(h domain.HostingHealthy) bool {
		return h.Snapshot.LocalBuild != (domain.HostingLocalBuildSource{}) || h.Release.LocalBuild != (domain.HostingBuildArtifact{})
	}
	if entry.Prepared != nil && entry.Prepared.Release.LocalBuild != (domain.HostingBuildArtifact{}) {
		return true
	}
	if entry.CleanupPlan != nil && healthy(entry.CleanupPlan.Target) {
		return true
	}
	if entry.Plan != nil {
		p := entry.Plan
		if p.Snapshot.LocalBuild != (domain.HostingLocalBuildSource{}) || p.Release.LocalBuild != (domain.HostingBuildArtifact{}) || p.PreviousHealthy != nil && healthy(*p.PreviousHealthy) {
			return true
		}
		for _, retained := range p.RetainedReleases {
			if healthy(retained) {
				return true
			}
		}
	}
	return false
}

func hostingJournalNeedsV3(entry JournalHosting) bool {
	return entry.Phase == "cleanup_intent" || strings.HasPrefix(entry.Attempt.RunID, "cleanup_") || entry.Plan != nil && entry.Plan.CompilerVersion == domain.HostingReleaseCompilerVersion
}

// HostingJournalUsesRuntimeEnvironment detects nonsecret runtime authority at
// every retained level. It does not authorize fetching values during replay.
func HostingJournalUsesRuntimeEnvironment(entry JournalHosting) bool {
	if entry.RuntimeEnvironment || entry.RuntimeConfiguration != (domain.HostingRuntimeConfigurationRef{}) || entry.DeliveryID != "" && !HostingJournalUsesDatabase(entry) || hostingJournalHasRuntimeCompiler(entry) {
		return true
	}
	for _, s := range hostingJournalSnapshots(entry) {
		if s.RuntimeConfiguration != (domain.HostingRuntimeConfigurationRef{}) || s.PolicyVersion == domain.HostingRuntimePolicyVersion || s.PolicyVersion == domain.HostingLocalBuildRuntimePolicyVersion {
			return true
		}
	}
	return false
}

func hostingJournalHasRuntimeCompiler(entry JournalHosting) bool {
	return entry.Plan != nil && (entry.Plan.CompilerVersion == domain.HostingRuntimeCompilerVersion || entry.Plan.CompilerVersion == domain.HostingLocalBuildRuntimeCompilerVersion)
}

func hostingJournalSnapshots(entry JournalHosting) []domain.HostingSnapshot {
	var snapshots []domain.HostingSnapshot
	if entry.Plan != nil {
		snapshots = append(snapshots, entry.Plan.Snapshot)
		if entry.Plan.PreviousHealthy != nil {
			snapshots = append(snapshots, entry.Plan.PreviousHealthy.Snapshot)
		}
		for _, retained := range entry.Plan.RetainedReleases {
			snapshots = append(snapshots, retained.Snapshot)
		}
	}
	if entry.CleanupPlan != nil {
		snapshots = append(snapshots, entry.CleanupPlan.Target.Snapshot)
	}
	return snapshots
}

func validateHostingJournalRuntimeEnvironment(entry JournalHosting) error {
	if hostingJournalHasRuntimeCompiler(entry) && (entry.Plan.Snapshot.RuntimeConfiguration == (domain.HostingRuntimeConfigurationRef{}) || !domain.HostingPlanFamilyMatches(entry.Plan.Snapshot, *entry.Plan)) {
		return errors.New("journal runtime compiler requires matching current runtime snapshot")
	}
	snapshots := hostingJournalSnapshots(entry)
	hasReference := false
	for _, s := range snapshots {
		if err := domain.ValidateHostingRuntimeConfigurationRef(s); err != nil {
			return errors.New("invalid journal runtime snapshot reference")
		}
		hasReference = hasReference || s.RuntimeConfiguration != (domain.HostingRuntimeConfigurationRef{})
	}
	if !entry.RuntimeEnvironment {
		if hasReference || entry.RuntimeConfiguration != (domain.HostingRuntimeConfigurationRef{}) || entry.DeliveryID != "" && !HostingJournalUsesDatabase(entry) {
			return errors.New("journal runtime environment marker missing")
		}
		return nil
	}
	if len(snapshots) != 0 {
		if !hasReference || entry.RuntimeConfiguration != snapshots[0].RuntimeConfiguration {
			return errors.New("journal runtime environment reference conflicts with target")
		}
	} else {
		// Preparation and result receipts have no snapshot. Retain the exact
		// current public reference explicitly so older readers fail closed.
		s := domain.HostingSnapshot{RuntimeConfiguration: entry.RuntimeConfiguration, PolicyVersion: domain.HostingRuntimePolicyVersion}
		if entry.RuntimeConfiguration == (domain.HostingRuntimeConfigurationRef{}) || domain.ValidateHostingRuntimeConfigurationRef(s) != nil {
			return errors.New("journal runtime receipt requires configuration reference")
		}
	}
	currentEnvironment := entry.RuntimeConfiguration != (domain.HostingRuntimeConfigurationRef{})
	if entry.Phase == "apply_intent" && currentEnvironment {
		decoded, err := hex.DecodeString(entry.DeliveryID)
		if err != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != entry.DeliveryID {
			return errors.New("journal runtime apply intent requires canonical delivery id")
		}
		if entry.Plan == nil || !domain.HostingPlanFamilyMatches(entry.Plan.Snapshot, *entry.Plan) {
			return errors.New("journal runtime apply plan family mismatch")
		}
	} else if entry.DeliveryID != "" {
		return errors.New("journal delivery id requires current runtime apply intent")
	}
	return nil
}

// AckHosting removes only a server-acknowledged hosting document. Intent callers
// must first establish that its durable server mutation hold has been settled.
func (j *AttemptJournal) AckHosting(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	kept := make([]JournalHosting, 0, len(j.state.Hosting))
	for _, entry := range j.state.Hosting {
		if entry.ID != id {
			kept = append(kept, entry)
		}
	}
	original := j.state.Hosting
	j.state.Hosting = kept
	if err := j.persistLocked(); err != nil {
		j.state.Hosting = original
		return err
	}
	return nil
}

func cloneJournalHosting(entry JournalHosting) JournalHosting {
	// Validation bounds and marshals this exact typed document before storage.
	raw, err := json.Marshal(entry)
	if err != nil {
		return JournalHosting{}
	}
	var cloned JournalHosting
	if json.Unmarshal(raw, &cloned) != nil {
		return JournalHosting{}
	}
	return cloned
}
func cloneHostingEntries(entries []JournalHosting) []JournalHosting {
	copy := make([]JournalHosting, len(entries))
	for i, entry := range entries {
		copy[i] = cloneJournalHosting(entry)
	}
	return copy
}
func validateJournalHosting(entry JournalHosting) error {
	if err := validateHostingJournalDatabase(entry); err != nil {
		return err
	}
	if err := validateHostingJournalStorage(entry); err != nil {
		return err
	}
	if err := validateHostingJournalRuntimeEnvironment(entry); err != nil {
		return err
	}
	if entry.LocalBuild && entry.Phase != "result" && !hostingJournalHasLocalArtifact(entry) {
		return errors.New("local journal marker conflicts with structured origin")
	}
	if hostingJournalHasLocalArtifact(entry) {
		if p := entry.Plan; p != nil {
			if domain.ValidateHostingSnapshotRelease(p.Snapshot, p.Release) != nil {
				return errors.New("local journal plan origin mismatch")
			}
			if p.PreviousHealthy != nil && domain.ValidateHostingSnapshotRelease(p.PreviousHealthy.Snapshot, p.PreviousHealthy.Release) != nil {
				return errors.New("local journal previous origin mismatch")
			}
			for _, retained := range p.RetainedReleases {
				if domain.ValidateHostingSnapshotRelease(retained.Snapshot, retained.Release) != nil {
					return errors.New("local journal retained origin mismatch")
				}
			}
		}
	}
	if strings.TrimSpace(entry.ID) == "" || len(entry.ID) > 128 || entry.OperationID == "" || len(entry.OperationID) > 128 || entry.CreatedAt.IsZero() {
		return errors.New("hosting journal requires stable identity and timestamp")
	}
	if err := validateAttemptIdentity(entry.Attempt); err != nil {
		return err
	}
	a := domain.HostingAuthority{OperationID: entry.OperationID, RunID: entry.Attempt.RunID, LeaseID: entry.Attempt.LeaseID, Attempt: entry.Attempt.Attempt, Fence: entry.Attempt.Fence}
	if entry.CleanupPlan != nil && entry.Phase != "cleanup_intent" {
		return errors.New("cleanup journal document in wrong phase")
	}
	switch entry.Phase {
	case "storage_create_intent", "storage_materialized":
		// The storage validator checks the complete typed document and plan.
	case "cleanup_intent":
		if entry.CleanupPlan == nil || entry.Plan != nil || entry.Prepared != nil || entry.Result != nil {
			return errors.New("invalid cleanup intent")
		}
		expected, err := domain.CompileHostingCleanupPlan(entry.CleanupPlan.Target, entry.CleanupPlan.CurrentHealthyOperationID)
		if err != nil || expected.Hash != entry.CleanupPlan.Hash {
			return errors.New("cleanup journal plan hash mismatch")
		}
	case "prepared":
		if entry.Prepared == nil || entry.Plan != nil || entry.Result != nil || entry.Prepared.HostingAuthority != a || domain.ValidateHostingRelease(entry.Prepared.Release) != nil {
			return errors.New("invalid journal preparation receipt")
		}
	case "apply_intent":
		if entry.Plan == nil || entry.Prepared != nil || entry.Result != nil {
			return errors.New("invalid journal apply intent")
		}
		plan := *entry.Plan
		hash := plan.Hash
		plan.Hash = ""
		if hash != domain.HostingDocumentHash(plan) || domain.ValidateHostingRelease(plan.Release) != nil {
			return errors.New("invalid journal plan hash")
		}
	case "result":
		if entry.Result == nil || entry.Prepared != nil || entry.Plan != nil || entry.Result.HostingAuthority != a {
			return errors.New("invalid journal result")
		}
	default:
		return errors.New("unknown hosting journal phase")
	}
	raw, err := json.Marshal(entry)
	if err != nil || len(raw) > journalMaxProvenanceBytes {
		return errors.New("hosting journal document exceeds limit")
	}
	return nil
}

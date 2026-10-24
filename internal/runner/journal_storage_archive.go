package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"

	"nerocd/internal/domain"
)

var storageArchiveJournalID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var storageArchiveHelperID = regexp.MustCompile(`^[a-f0-9]{64}$`)

func decodeStorageArchiveJournal(raw []byte, state *journalState) error {
	return decodeDatabaseJournal(raw, state)
}

// JournalStorageArchive retains only typed, nonsecret archive evidence. In
// particular it cannot carry an ephemeral volume creation permission, helper
// output, a host path, or a generic application readiness result.
type JournalStorageArchive struct {
	ID                         string                                         `json:"id"`
	Attempt                    AttemptIdentity                                `json:"attempt_authority"`
	Plan                       domain.HostingStorageArchivePlan               `json:"plan"`
	Phase                      string                                         `json:"phase"`
	Prepared                   *domain.HostingStorageArchivePreparedRequest   `json:"prepared,omitempty"`
	ApplyBegin                 *domain.HostingStorageArchiveApplyBeginRequest `json:"apply_begin,omitempty"`
	Progress                   *domain.HostingStorageArchiveProgress          `json:"progress,omitempty"`
	Complete                   *domain.HostingStorageArchiveCompleteRequest   `json:"complete,omitempty"`
	StorageCreate              *domain.HostingStorageCreateRequest            `json:"storage_create,omitempty"`
	StorageMaterialized        *domain.HostingStorageMaterializedRequest      `json:"storage_materialized,omitempty"`
	StorageMetadata            *domain.HostingStorage                         `json:"storage_metadata,omitempty"`
	UncertainHelperContainerID string                                         `json:"uncertain_helper_container_id,omitempty"`
	RecoverySettlement         *domain.HostingReceipt                         `json:"recovery_settlement,omitempty"`
	RetirementReason           string                                         `json:"retirement_reason,omitempty"`
	CreatedAt                  time.Time                                      `json:"created_at"`
}

// Authority returns the original complete attempt tuple, never renewed authority.
func (e JournalStorageArchive) Authority() domain.HostingAuthority {
	return domain.HostingAuthority{OperationID: e.Plan.OperationID, RunID: e.Attempt.RunID, LeaseID: e.Attempt.LeaseID, Attempt: e.Attempt.Attempt, Fence: e.Attempt.Fence}
}

func validateJournalStorageArchive(e JournalStorageArchive) error {
	invalid := errors.New("invalid typed storage archive journal entry")
	if validateAttemptIdentity(e.Attempt) != nil || e.Plan.Validate() != nil || !storageArchiveJournalID.MatchString(e.ID) || e.CreatedAt.IsZero() {
		return invalid
	}
	run := domain.TaskRun{ID: e.Attempt.RunID, RunSpec: domain.RunSpec{Type: domain.RunTypeStorageArchive, HostingOperationID: e.Plan.OperationID, HostingMode: e.Plan.Mode}}
	if domain.ValidateStorageArchiveTask(run) != nil {
		return invalid
	}
	count := 0
	for _, present := range []bool{e.Prepared != nil, e.ApplyBegin != nil, e.Progress != nil, e.Complete != nil, e.StorageCreate != nil, e.StorageMaterialized != nil} {
		if present {
			count++
		}
	}
	if e.Phase == "recovery_settlement" {
		r := e.RecoverySettlement
		if count != 0 || e.Plan.Mode != "recover" || e.StorageMetadata != nil || e.UncertainHelperContainerID != "" || r == nil || !storageArchiveJournalID.MatchString(r.ID) || r.CreatedAt.IsZero() || r.BodyHash == "" || e.RetirementReason != "fresh_recovery_settled" {
			return invalid
		}
		return nil
	}
	if e.RecoverySettlement != nil || e.RetirementReason != "" {
		return invalid
	}
	if e.Phase == "plan" {
		if count != 0 || e.StorageMetadata != nil || e.UncertainHelperContainerID != "" {
			return invalid
		}
		return nil
	}
	if e.Phase == "helper_unverified" {
		if count != 0 || e.StorageMetadata != nil || !storageArchiveHelperID.MatchString(e.UncertainHelperContainerID) || e.Plan.Mode == "recover" {
			return invalid
		}
		return nil
	}
	if e.UncertainHelperContainerID != "" {
		return invalid
	}
	if count != 1 {
		return invalid
	}
	a := e.Authority()
	var authority domain.HostingAuthority
	var planHash, receipt string
	switch e.Phase {
	case "prepared":
		if e.Prepared == nil || e.Prepared.Validate() != nil || e.Prepared.Helper != e.Plan.Helper {
			return invalid
		}
		authority, planHash, receipt = e.Prepared.HostingAuthority, e.Prepared.PlanHash, e.Prepared.ReceiptID
	case "apply_intent":
		if e.ApplyBegin == nil || e.ApplyBegin.Validate() != nil {
			return invalid
		}
		authority, planHash, receipt = e.ApplyBegin.HostingAuthority, e.ApplyBegin.PlanHash, e.ApplyBegin.ReceiptID
	case "progress":
		if e.Progress == nil || e.Progress.Validate() != nil {
			return invalid
		}
		p := e.Progress
		noExtra := p.HelperContainerID == "" && p.Blob == nil && !p.RestoreVerified
		switch p.Phase {
		case "writer_snapshot":
			if e.Plan.Mode != "backup" || len(p.Writers) != len(e.Plan.Writers) || !noExtra {
				return invalid
			}
		case "stop_intent", "stopped", "resume_intent", "resumed":
			if e.Plan.Target != nil || len(p.Writers) != len(e.Plan.Writers) || !noExtra || e.Plan.Mode == "recover" && (p.Phase == "stop_intent" || p.Phase == "stopped") {
				return invalid
			}
		case "helper_intent":
			if e.Plan.Mode == "recover" || len(p.Writers) != 0 || !noExtra {
				return invalid
			}
		case "helper_identified":
			if e.Plan.Mode == "recover" || len(p.Writers) != 0 || !storageArchiveHelperID.MatchString(p.HelperContainerID) || p.Blob != nil || p.RestoreVerified {
				return invalid
			}
		case "archive_published":
			if e.Plan.Mode != "backup" || len(p.Writers) != 0 || p.HelperContainerID != "" || p.Blob == nil || p.RestoreVerified {
				return invalid
			}
		case "restore_verified":
			if e.Plan.Mode != "restore" || len(p.Writers) != 0 || p.HelperContainerID != "" || p.Blob == nil || e.Plan.Blob == nil || *p.Blob != *e.Plan.Blob || !p.RestoreVerified {
				return invalid
			}
		}
		if p.Phase == "stopped" {
			for _, w := range p.Writers {
				if w.Running {
					return invalid
				}
			}
		}
		if e.Plan.Mode == "recover" && (p.Phase == "resume_intent" || p.Phase == "resumed") && !slices.Equal(p.Writers, e.Plan.RecoveryEvidence.Writers) {
			return invalid
		}
		authority, planHash, receipt = e.Progress.HostingAuthority, e.Progress.PlanHash, e.Progress.ReceiptID
		for _, state := range e.Progress.Writers {
			found := false
			for _, writer := range e.Plan.Writers {
				found = found || state.ContainerID == writer.ContainerID && state.OperationID == writer.OperationID && state.ImageID == writer.Release.ImageID && state.PlanHash == writer.PlanHash && state.ReleaseHash == writer.Release.Hash
			}
			if !found {
				return invalid
			}
		}
		if e.Progress.Blob != nil && !archiveJournalBlobMatches(*e.Progress.Blob, e.Plan) {
			return invalid
		}
	case "complete":
		if e.Complete == nil || e.Complete.Validate() != nil {
			return invalid
		}
		authority, planHash, receipt = e.Complete.HostingAuthority, e.Complete.PlanHash, e.Complete.ReceiptID
		if e.Complete.Result.Blob != nil && !archiveJournalBlobMatches(*e.Complete.Result.Blob, e.Plan) {
			return invalid
		}
	case "storage_create_intent":
		if e.StorageCreate == nil || e.StorageCreate.Validate() != nil || e.Plan.Mode != "restore" || e.Plan.Target == nil || e.StorageCreate.StorageRef != e.Plan.Target.Storage.Reference() || e.StorageCreate.CreationNonce != e.Plan.Target.Plan.CreationNonce {
			return invalid
		}
		authority, planHash = e.StorageCreate.HostingAuthority, e.StorageCreate.PlanHash
	case "storage_materialized":
		if e.StorageMaterialized == nil || e.StorageMaterialized.Validate() != nil || e.StorageMetadata == nil || e.Plan.Target == nil || *e.StorageMetadata != e.Plan.Target.Storage || e.StorageMaterialized.StorageRef != e.Plan.Target.Storage.Reference() || e.StorageMaterialized.CreationOperationID != e.Plan.Target.Plan.CreationOperationID || e.StorageMaterialized.CreationNonce != e.Plan.Target.Plan.CreationNonce || e.StorageMaterialized.DaemonID != e.Plan.Helper.DaemonID {
			return invalid
		}
		authority, planHash, receipt = e.StorageMaterialized.HostingAuthority, e.StorageMaterialized.PlanHash, e.StorageMaterialized.ReceiptID
	default:
		return invalid
	}
	if authority != a || planHash != e.Plan.Hash || receipt != "" && receipt != e.ID || e.Phase != "storage_materialized" && e.StorageMetadata != nil {
		return invalid
	}
	return nil
}

func archiveJournalBlobMatches(blob domain.HostingStorageArchiveBlob, p domain.HostingStorageArchivePlan) bool {
	return blob.Validate() == nil && blob.BackupID == p.BackupID && blob.DestinationID == p.DestinationID && blob.DaemonID == p.Helper.DaemonID && blob.SizeBytes <= p.Limits.ArchiveBytes
}

// AppendStorageArchive persists an exact request before sending or mutating.
// IDs are immutable; response loss never permits a replacement request body.
func (j *AttemptJournal) AppendStorageArchive(entry JournalStorageArchive) (JournalStorageArchive, error) {
	if err := validateJournalStorageArchive(entry); err != nil {
		return JournalStorageArchive{}, err
	}
	entry = cloneStorageArchiveEntry(entry)
	j.mu.Lock()
	defer j.mu.Unlock()
	raw, err := json.Marshal(entry)
	if err != nil {
		return JournalStorageArchive{}, err
	}
	for _, prior := range j.state.StorageArchive {
		if prior.ID != entry.ID {
			continue
		}
		old, err := json.Marshal(prior)
		if err != nil {
			return JournalStorageArchive{}, err
		}
		if !bytes.Equal(raw, old) {
			return JournalStorageArchive{}, ErrJournalConflict
		}
		return cloneStorageArchiveEntry(prior), nil
	}
	if j.entryCountLocked() >= journalMaxEntries {
		return JournalStorageArchive{}, errors.New("runner journal entry limit reached")
	}
	priorVersion := j.state.Version
	j.state.Version = 12
	j.state.StorageArchive = append(j.state.StorageArchive, entry)
	err = validateJournalState(j.state)
	if err == nil {
		err = j.persistLocked()
	}
	if err != nil {
		j.state.StorageArchive = j.state.StorageArchive[:len(j.state.StorageArchive)-1]
		j.state.Version = priorVersion
		return JournalStorageArchive{}, err
	}
	return cloneStorageArchiveEntry(entry), nil
}

func validateJournalStorageArchives(state journalState) error {
	for i, entry := range state.StorageArchive {
		if entry.RecoverySettlement != nil {
			matched := false
			for _, proof := range state.StorageArchive {
				if proof.Authority() == entry.Authority() && proof.Complete != nil && proof.Complete.ReceiptID == entry.RecoverySettlement.ID && domain.HostingDocumentHash(*proof.Complete) == entry.RecoverySettlement.BodyHash && !proof.Complete.Result.Uncertain && proof.Complete.Result.ResumeOutcome != "uncertain" {
					matched = true
				}
			}
			if !matched {
				return errors.New("archive recovery settlement requires exact completion")
			}
		}
		for _, prior := range state.StorageArchive[:i] {
			if sameAttemptAuthority(prior.Attempt, entry.Attempt) && (prior.Plan.OperationID != entry.Plan.OperationID || prior.Plan.Hash != entry.Plan.Hash) {
				return errors.New("archive journal attempt changed immutable plan")
			}
			if prior.Authority() == entry.Authority() && prior.Progress != nil && entry.Progress != nil && prior.Progress.Phase == entry.Progress.Phase {
				return errors.New("archive journal phase has more than one immutable request")
			}
		}
		for _, other := range state.Hosting {
			if sameAttemptAuthority(entry.Attempt, other.Attempt) {
				return errors.New("archive journal mixes hosting families")
			}
		}
		for _, other := range state.Proxy {
			if sameAttemptAuthority(entry.Attempt, other.Attempt) {
				return errors.New("archive journal mixes proxy families")
			}
		}
		for _, other := range state.Build {
			if sameAttemptAuthority(entry.Attempt, other.Attempt) {
				return errors.New("archive journal mixes build families")
			}
		}
		for _, other := range state.Completions {
			if sameAttemptAuthority(entry.Attempt, other.Attempt) {
				return errors.New("archive journal mixes generic completion")
			}
		}
		for _, other := range state.Provenance {
			if sameAttemptAuthority(entry.Attempt, other.Attempt) {
				return errors.New("archive journal mixes generic provenance")
			}
		}
	}
	return nil
}

// AckStorageArchive removes only explicitly settled archive evidence.
func (j *AttemptJournal) AckStorageArchive(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	kept := make([]JournalStorageArchive, 0, len(j.state.StorageArchive))
	for _, entry := range j.state.StorageArchive {
		if entry.ID != id {
			kept = append(kept, entry)
		}
	}
	if len(kept) == len(j.state.StorageArchive) {
		return nil
	}
	prior := j.state.StorageArchive
	j.state.StorageArchive = kept
	if err := j.persistLocked(); err != nil {
		j.state.StorageArchive = prior
		return err
	}
	return nil
}

func cloneStorageArchiveEntry(entry JournalStorageArchive) JournalStorageArchive {
	raw, err := json.Marshal(entry)
	if err != nil {
		panic("validated archive journal cannot be encoded")
	}
	var clone JournalStorageArchive
	if json.Unmarshal(raw, &clone) != nil {
		panic("validated archive journal cannot be cloned")
	}
	return clone
}
func cloneStorageArchiveEntries(entries []JournalStorageArchive) []JournalStorageArchive {
	if entries == nil {
		return nil
	}
	clones := make([]JournalStorageArchive, len(entries))
	for i, entry := range entries {
		clones[i] = cloneStorageArchiveEntry(entry)
	}
	return clones
}

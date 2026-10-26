package runner

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"nerocd/internal/domain"
)

// JournalStorageBackupTransfer retains exact intent and result requests independently
// of task completion. An unpaired import intent is never replay permission.
type JournalStorageBackupTransfer struct {
	ID        string                                      `json:"id"`
	Attempt   AttemptIdentity                             `json:"attempt_authority"`
	Plan      domain.HostingStorageBackupTransferPlan     `json:"plan"`
	Request   domain.HostingStorageBackupTransferMutation `json:"request"`
	CreatedAt time.Time                                   `json:"created_at"`
}

func validateJournalStorageBackupTransfer(e JournalStorageBackupTransfer) error {
	if e.Plan.DaemonID != "" && e.Request.DaemonID != e.Plan.DaemonID {
		return errors.New("storage transfer daemon mismatch")
	}
	a := e.Request.HostingAuthority
	if domain.ValidateStorageTransferCopyProof(e.Plan, e.Request) != nil || e.ID != e.Request.ReceiptID || validateAttemptIdentity(e.Attempt) != nil || e.CreatedAt.IsZero() || e.Plan.Validate() != nil || e.Request.Validate() != nil || e.Request.PlanHash != e.Plan.Hash || a.OperationID != e.Plan.Operation.ID || a.RunID != e.Attempt.RunID || a.LeaseID != e.Attempt.LeaseID || a.Attempt != e.Attempt.Attempt || a.Fence != e.Attempt.Fence {
		return errors.New("invalid remote backup transfer journal")
	}
	return nil
}
func cloneStorageBackupTransfers(v []JournalStorageBackupTransfer) []JournalStorageBackupTransfer {
	if v == nil {
		return nil
	}
	raw, _ := json.Marshal(v)
	var out []JournalStorageBackupTransfer
	_ = json.Unmarshal(raw, &out)
	return out
}

// AppendStorageBackupTransfer durably retains a validated storage transfer receipt, rejecting conflicting IDs.
func (j *AttemptJournal) AppendStorageBackupTransfer(e JournalStorageBackupTransfer) (JournalStorageBackupTransfer, error) {
	if err := validateJournalStorageBackupTransfer(e); err != nil {
		return JournalStorageBackupTransfer{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, old := range j.state.StorageBackupTransfers {
		if old.ID == e.ID {
			if domain.HostingDocumentHash(old) != domain.HostingDocumentHash(e) {
				return JournalStorageBackupTransfer{}, ErrJournalConflict
			}
			return cloneStorageBackupTransfers([]JournalStorageBackupTransfer{old})[0], nil
		}
	}
	if j.entryCountLocked() >= journalMaxEntries {
		return JournalStorageBackupTransfer{}, errors.New("runner journal entry limit reached")
	}
	prior := j.state.StorageBackupTransfers
	version := j.state.Version
	j.state.StorageBackupTransfers = append(cloneStorageBackupTransfers(prior), e)
	j.state.Version = 19
	if err := j.persistLocked(); err != nil {
		j.state.StorageBackupTransfers = prior
		j.state.Version = version
		return JournalStorageBackupTransfer{}, err
	}
	return e, nil
}

// AckStorageBackupTransfer removes only this exact entry after a controller receipt.
func (j *AttemptJournal) AckStorageBackupTransfer(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	prior := j.state.StorageBackupTransfers
	j.state.StorageBackupTransfers = slices.DeleteFunc(slices.Clone(prior), func(e JournalStorageBackupTransfer) bool { return e.ID == id })
	if err := j.persistLocked(); err != nil {
		j.state.StorageBackupTransfers = prior
		return err
	}
	return nil
}

func validateJournalStorageBackupTransfers(state journalState) error {
	seenStorageBackupTransfers := map[string]bool{}
	for _, entry := range state.StorageBackupTransfers {
		if seenStorageBackupTransfers[entry.ID] {
			return errors.New("duplicate remote backup transfer journal ID")
		}
		seenStorageBackupTransfers[entry.ID] = true
		for _, completion := range state.Completions {
			if sameAttemptAuthority(entry.Attempt, completion.Attempt) {
				return errors.New("remote backup transfer cannot use generic completion")
			}
		}
	}
	for _, entry := range state.StorageBackupTransfers {
		if err := validateJournalStorageBackupTransfer(entry); err != nil {
			return err
		}
	}
	return nil
}

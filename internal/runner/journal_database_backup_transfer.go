package runner

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"nerocd/internal/domain"
)

// JournalDatabaseBackupTransfer retains exact intent and result requests independently
// of task completion. An unpaired import intent is never replay permission.
type JournalDatabaseBackupTransfer struct {
	ID        string                                       `json:"id"`
	Attempt   AttemptIdentity                              `json:"attempt_authority"`
	Plan      domain.HostingDatabaseBackupTransferPlan     `json:"plan"`
	Request   domain.HostingDatabaseBackupTransferMutation `json:"request"`
	CreatedAt time.Time                                    `json:"created_at"`
}

func validateJournalDatabaseBackupTransfer(e JournalDatabaseBackupTransfer) error {
	if e.Request.LocalCopy != nil && e.Plan.Operation.Mode != "download" || e.Request.RemoteCopy != nil && e.Plan.Operation.Mode != "upload" {
		return errors.New("remote backup copy proof purpose mismatch")
	}
	a := e.Request.HostingAuthority
	if e.ID != e.Request.ReceiptID || validateAttemptIdentity(e.Attempt) != nil || e.CreatedAt.IsZero() || e.Plan.Validate() != nil || e.Request.Validate() != nil || e.Request.PlanHash != e.Plan.Hash || a.OperationID != e.Plan.Operation.ID || a.RunID != e.Attempt.RunID || a.LeaseID != e.Attempt.LeaseID || a.Attempt != e.Attempt.Attempt || a.Fence != e.Attempt.Fence {
		return errors.New("invalid remote backup transfer journal")
	}
	return nil
}
func cloneDatabaseBackupTransfers(v []JournalDatabaseBackupTransfer) []JournalDatabaseBackupTransfer {
	if v == nil {
		return nil
	}
	raw, _ := json.Marshal(v)
	var out []JournalDatabaseBackupTransfer
	_ = json.Unmarshal(raw, &out)
	return out
}
// AppendDatabaseBackupTransfer durably retains a validated transfer receipt, rejecting conflicting IDs.
func (j *AttemptJournal) AppendDatabaseBackupTransfer(e JournalDatabaseBackupTransfer) (JournalDatabaseBackupTransfer, error) {
	if err := validateJournalDatabaseBackupTransfer(e); err != nil {
		return JournalDatabaseBackupTransfer{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, old := range j.state.DatabaseBackupTransfers {
		if old.ID == e.ID {
			if domain.HostingDocumentHash(old) != domain.HostingDocumentHash(e) {
				return JournalDatabaseBackupTransfer{}, ErrJournalConflict
			}
			return cloneDatabaseBackupTransfers([]JournalDatabaseBackupTransfer{old})[0], nil
		}
	}
	if j.entryCountLocked() >= journalMaxEntries {
		return JournalDatabaseBackupTransfer{}, errors.New("runner journal entry limit reached")
	}
	prior := j.state.DatabaseBackupTransfers
	version := j.state.Version
	j.state.DatabaseBackupTransfers = append(cloneDatabaseBackupTransfers(prior), e)
	j.state.Version = 19
	if err := j.persistLocked(); err != nil {
		j.state.DatabaseBackupTransfers = prior
		j.state.Version = version
		return JournalDatabaseBackupTransfer{}, err
	}
	return e, nil
}

// AckDatabaseBackupTransfer removes only this exact entry after a controller receipt.
func (j *AttemptJournal) AckDatabaseBackupTransfer(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	prior := j.state.DatabaseBackupTransfers
	j.state.DatabaseBackupTransfers = slices.DeleteFunc(slices.Clone(prior), func(e JournalDatabaseBackupTransfer) bool { return e.ID == id })
	if err := j.persistLocked(); err != nil {
		j.state.DatabaseBackupTransfers = prior
		return err
	}
	return nil
}

func validateJournalDatabaseBackupTransfers(state journalState) error {
	seenDatabaseBackupTransfers := map[string]bool{}
	for _, entry := range state.DatabaseBackupTransfers {
		if seenDatabaseBackupTransfers[entry.ID] {
			return errors.New("duplicate remote backup transfer journal ID")
		}
		seenDatabaseBackupTransfers[entry.ID] = true
		for _, completion := range state.Completions {
			if sameAttemptAuthority(entry.Attempt, completion.Attempt) {
				return errors.New("remote backup transfer cannot use generic completion")
			}
		}
	}
	for _, entry := range state.DatabaseBackupTransfers {
		if err := validateJournalDatabaseBackupTransfer(entry); err != nil {
			return err
		}
	}
	return nil
}

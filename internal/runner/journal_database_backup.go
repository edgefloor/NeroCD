package runner

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"nerocd/internal/domain"
)

// JournalDatabaseBackup retains exact intent and result requests independently
// of task completion. An unpaired import intent is never replay permission.
type JournalDatabaseBackup struct {
	ID        string                               `json:"id"`
	Attempt   AttemptIdentity                      `json:"attempt_authority"`
	Plan      domain.HostingDatabaseBackupPlan     `json:"plan"`
	Request   domain.HostingDatabaseBackupMutation `json:"request"`
	CreatedAt time.Time                            `json:"created_at"`
}

func validateJournalDatabaseBackup(e JournalDatabaseBackup) error {
	a := e.Request.HostingAuthority
	if e.ID != e.Request.ReceiptID || validateAttemptIdentity(e.Attempt) != nil || e.CreatedAt.IsZero() || e.Plan.Validate() != nil || e.Request.Validate() != nil || e.Request.PlanHash != e.Plan.Hash || a.OperationID != e.Plan.Operation.ID || a.RunID != e.Attempt.RunID || a.LeaseID != e.Attempt.LeaseID || a.Attempt != e.Attempt.Attempt || a.Fence != e.Attempt.Fence {
		return errors.New("invalid database backup journal")
	}
	return nil
}
func cloneDatabaseBackups(v []JournalDatabaseBackup) []JournalDatabaseBackup {
	if v == nil {
		return nil
	}
	raw, _ := json.Marshal(v)
	var out []JournalDatabaseBackup
	_ = json.Unmarshal(raw, &out)
	return out
}
// AppendDatabaseBackup durably retains a validated backup receipt, rejecting conflicting IDs.
func (j *AttemptJournal) AppendDatabaseBackup(e JournalDatabaseBackup) (JournalDatabaseBackup, error) {
	if err := validateJournalDatabaseBackup(e); err != nil {
		return JournalDatabaseBackup{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, old := range j.state.DatabaseBackup {
		if old.ID == e.ID {
			if domain.HostingDocumentHash(old) != domain.HostingDocumentHash(e) {
				return JournalDatabaseBackup{}, ErrJournalConflict
			}
			return cloneDatabaseBackups([]JournalDatabaseBackup{old})[0], nil
		}
	}
	if j.entryCountLocked() >= journalMaxEntries {
		return JournalDatabaseBackup{}, errors.New("runner journal entry limit reached")
	}
	prior := j.state.DatabaseBackup
	version := j.state.Version
	j.state.DatabaseBackup = append(cloneDatabaseBackups(prior), e)
	j.state.Version = 18
	if err := j.persistLocked(); err != nil {
		j.state.DatabaseBackup = prior
		j.state.Version = version
		return JournalDatabaseBackup{}, err
	}
	return e, nil
}

// AckDatabaseBackup removes only this exact entry after a controller receipt.
func (j *AttemptJournal) AckDatabaseBackup(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	prior := j.state.DatabaseBackup
	j.state.DatabaseBackup = slices.DeleteFunc(slices.Clone(prior), func(e JournalDatabaseBackup) bool { return e.ID == id })
	if err := j.persistLocked(); err != nil {
		j.state.DatabaseBackup = prior
		return err
	}
	return nil
}

func validateJournalDatabaseBackups(state journalState) error {
	seenDatabaseBackups := map[string]bool{}
	for _, entry := range state.DatabaseBackup {
		if seenDatabaseBackups[entry.ID] {
			return errors.New("duplicate database backup journal ID")
		}
		seenDatabaseBackups[entry.ID] = true
		for _, completion := range state.Completions {
			if sameAttemptAuthority(entry.Attempt, completion.Attempt) {
				return errors.New("database backup cannot use generic completion")
			}
		}
	}
	for _, entry := range state.DatabaseBackup {
		if err := validateJournalDatabaseBackup(entry); err != nil {
			return err
		}
	}
	return nil
}

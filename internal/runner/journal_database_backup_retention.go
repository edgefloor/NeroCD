package runner

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"nerocd/internal/domain"
)

// JournalDatabaseBackupRetention retains exact intent and result requests independently
// of task completion. An unpaired deletion intent is never replay permission.
type JournalDatabaseBackupRetention struct {
	ProbeKey  string                                        `json:"probe_key,omitempty"`
	ID        string                                        `json:"id"`
	Attempt   AttemptIdentity                               `json:"attempt_authority"`
	Plan      domain.HostingDatabaseBackupRetentionPlan     `json:"plan"`
	Request   domain.HostingDatabaseBackupRetentionMutation `json:"request"`
	CreatedAt time.Time                                     `json:"created_at"`
}

func validateJournalDatabaseBackupRetention(e JournalDatabaseBackupRetention) error {
	found := false
	for _, copy := range e.Plan.Copies {
		if copy.Item.CopyID == e.Request.CopyID {
			found = true
			if e.Request.ProbeOnly {
				prefix := "nerocd/postgresql/retention-probes/" + e.Plan.Operation.ID + "/"
				if !strings.HasPrefix(e.ProbeKey, prefix) || len(e.ProbeKey) != len(prefix)+32 {
					return errors.New("invalid retention probe identity")
				}
				for _, r := range strings.TrimPrefix(e.ProbeKey, prefix) {
					if (r < 'a' || r > 'f') && (r < '0' || r > '9') {
						return errors.New("invalid retention probe identity")
					}
				}
			} else if e.ProbeKey != "" {
				return errors.New("unexpected retention probe")
			}
		}
	}
	if e.Request.ProbeOnly && e.Request.ProbeKey != e.ProbeKey {
		return errors.New("retention probe request identity mismatch")
	}
	if !found {
		return errors.New("retention copy absent from plan")
	}
	a := e.Request.HostingAuthority
	if e.ID != e.Request.ReceiptID || validateAttemptIdentity(e.Attempt) != nil || e.CreatedAt.IsZero() || e.Plan.Validate() != nil || e.Request.Validate() != nil || e.Request.PlanHash != e.Plan.Hash || a.OperationID != e.Plan.Operation.ID || a.RunID != e.Attempt.RunID || a.LeaseID != e.Attempt.LeaseID || a.Attempt != e.Attempt.Attempt || a.Fence != e.Attempt.Fence {
		return errors.New("invalid backup retention journal")
	}
	return nil
}
func cloneDatabaseBackupRetentions(v []JournalDatabaseBackupRetention) []JournalDatabaseBackupRetention {
	if v == nil {
		return nil
	}
	raw, _ := json.Marshal(v)
	var out []JournalDatabaseBackupRetention
	_ = json.Unmarshal(raw, &out)
	return out
}

// AppendDatabaseBackupRetention durably retains a validated retention receipt, rejecting conflicting IDs.
func (j *AttemptJournal) AppendDatabaseBackupRetention(e JournalDatabaseBackupRetention) (JournalDatabaseBackupRetention, error) {
	if err := validateJournalDatabaseBackupRetention(e); err != nil {
		return JournalDatabaseBackupRetention{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, old := range j.state.DatabaseBackupRetentions {
		if old.ID == e.ID {
			if domain.HostingDocumentHash(old) != domain.HostingDocumentHash(e) {
				return JournalDatabaseBackupRetention{}, ErrJournalConflict
			}
			return cloneDatabaseBackupRetentions([]JournalDatabaseBackupRetention{old})[0], nil
		}
	}
	if j.entryCountLocked() >= journalMaxEntries {
		return JournalDatabaseBackupRetention{}, errors.New("runner journal entry limit reached")
	}
	prior := j.state.DatabaseBackupRetentions
	version := j.state.Version
	j.state.DatabaseBackupRetentions = append(cloneDatabaseBackupRetentions(prior), e)
	j.state.Version = 20
	if err := j.persistLocked(); err != nil {
		j.state.DatabaseBackupRetentions = prior
		j.state.Version = version
		return JournalDatabaseBackupRetention{}, err
	}
	return e, nil
}

// AckDatabaseBackupRetention removes only this exact entry after a controller receipt.
func (j *AttemptJournal) AckDatabaseBackupRetention(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	prior := j.state.DatabaseBackupRetentions
	j.state.DatabaseBackupRetentions = slices.DeleteFunc(slices.Clone(prior), func(e JournalDatabaseBackupRetention) bool { return e.ID == id })
	if err := j.persistLocked(); err != nil {
		j.state.DatabaseBackupRetentions = prior
		return err
	}
	return nil
}

func validateJournalDatabaseBackupRetentions(state journalState) error {
	seenDatabaseBackupRetentions := map[string]bool{}
	for _, entry := range state.DatabaseBackupRetentions {
		if seenDatabaseBackupRetentions[entry.ID] {
			return errors.New("duplicate backup retention journal ID")
		}
		seenDatabaseBackupRetentions[entry.ID] = true
		for _, completion := range state.Completions {
			if sameAttemptAuthority(entry.Attempt, completion.Attempt) {
				return errors.New("backup retention cannot use generic completion")
			}
		}
	}
	for _, entry := range state.DatabaseBackupRetentions {
		if err := validateJournalDatabaseBackupRetention(entry); err != nil {
			return err
		}
	}
	return nil
}

package runner

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"nerocd/internal/domain"
)

// JournalStorageBackupRetention retains exact intent and result requests independently
// of task completion. An unpaired deletion intent is never replay permission.
type JournalStorageBackupRetention struct {
	ProbeKey  string                                       `json:"probe_key,omitempty"`
	ID        string                                       `json:"id"`
	Attempt   AttemptIdentity                              `json:"attempt_authority"`
	Plan      domain.HostingStorageBackupRetentionPlan     `json:"plan"`
	Request   domain.HostingStorageBackupRetentionMutation `json:"request"`
	CreatedAt time.Time                                    `json:"created_at"`
}

func validateJournalStorageBackupRetention(e JournalStorageBackupRetention) error {
	found := false
	for _, copy := range e.Plan.Copies {
		if copy.Item.CopyID == e.Request.CopyID {
			found = true
			if e.Request.ProbeOnly {
				prefix := "nerocd/storage/retention-probes/" + e.Plan.Operation.ID + "/"
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
func cloneStorageBackupRetentions(v []JournalStorageBackupRetention) []JournalStorageBackupRetention {
	if v == nil {
		return nil
	}
	raw, _ := json.Marshal(v)
	var out []JournalStorageBackupRetention
	_ = json.Unmarshal(raw, &out)
	return out
}

// AppendStorageBackupRetention durably retains a validated storage retention receipt, rejecting conflicting IDs.
func (j *AttemptJournal) AppendStorageBackupRetention(e JournalStorageBackupRetention) (JournalStorageBackupRetention, error) {
	if err := validateJournalStorageBackupRetention(e); err != nil {
		return JournalStorageBackupRetention{}, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, old := range j.state.StorageBackupRetentions {
		if old.ID == e.ID {
			if domain.HostingDocumentHash(old) != domain.HostingDocumentHash(e) {
				return JournalStorageBackupRetention{}, ErrJournalConflict
			}
			return cloneStorageBackupRetentions([]JournalStorageBackupRetention{old})[0], nil
		}
	}
	if j.entryCountLocked() >= journalMaxEntries {
		return JournalStorageBackupRetention{}, errors.New("runner journal entry limit reached")
	}
	prior := j.state.StorageBackupRetentions
	version := j.state.Version
	j.state.StorageBackupRetentions = append(cloneStorageBackupRetentions(prior), e)
	j.state.Version = 22
	if err := j.persistLocked(); err != nil {
		j.state.StorageBackupRetentions = prior
		j.state.Version = version
		return JournalStorageBackupRetention{}, err
	}
	return e, nil
}

// AckStorageBackupRetention removes only this exact entry after a controller receipt.
func (j *AttemptJournal) AckStorageBackupRetention(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	prior := j.state.StorageBackupRetentions
	j.state.StorageBackupRetentions = slices.DeleteFunc(slices.Clone(prior), func(e JournalStorageBackupRetention) bool { return e.ID == id })
	if err := j.persistLocked(); err != nil {
		j.state.StorageBackupRetentions = prior
		return err
	}
	return nil
}

func validateJournalStorageBackupRetentions(state journalState) error {
	seenStorageBackupRetentions := map[string]bool{}
	for _, entry := range state.StorageBackupRetentions {
		if seenStorageBackupRetentions[entry.ID] {
			return errors.New("duplicate backup retention journal ID")
		}
		seenStorageBackupRetentions[entry.ID] = true
		for _, completion := range state.Completions {
			if sameAttemptAuthority(entry.Attempt, completion.Attempt) {
				return errors.New("backup retention cannot use generic completion")
			}
		}
	}
	for _, entry := range state.StorageBackupRetentions {
		if err := validateJournalStorageBackupRetention(entry); err != nil {
			return err
		}
	}
	return nil
}

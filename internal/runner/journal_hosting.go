package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"nerocd/internal/domain"
)

// JournalHosting is nonsecret immutable prepare, apply-intent, or result
// evidence. Apply intent is never discarded merely because its lease is stale.
type JournalHosting struct {
	ID          string                         `json:"id"`
	Attempt     AttemptIdentity                `json:"attempt_authority"`
	OperationID string                         `json:"operation_id"`
	Phase       string                         `json:"phase"`
	Prepared    *domain.HostingPreparedRequest `json:"prepared,omitempty"`
	Plan        *domain.HostingApplyPlan       `json:"plan,omitempty"`
	Result      *domain.HostingCompleteRequest `json:"result,omitempty"`
	CreatedAt   time.Time                      `json:"created_at"`
}

func (j *AttemptJournal) AppendHosting(entry JournalHosting) (JournalHosting, error) {
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
	oldVersion := j.state.Version
	j.state.Version = 2 // Older binaries reject v2 before interpreting any records.
	j.state.Hosting = append(j.state.Hosting, entry)
	if err = j.persistLocked(); err != nil {
		j.state.Hosting = j.state.Hosting[:len(j.state.Hosting)-1]
		j.state.Version = oldVersion
		return JournalHosting{}, err
	}
	return cloneJournalHosting(entry), nil
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
	if strings.TrimSpace(entry.ID) == "" || len(entry.ID) > 128 || entry.OperationID == "" || len(entry.OperationID) > 128 || entry.CreatedAt.IsZero() {
		return errors.New("hosting journal requires stable identity and timestamp")
	}
	if err := validateAttemptIdentity(entry.Attempt); err != nil {
		return err
	}
	a := domain.HostingAuthority{OperationID: entry.OperationID, RunID: entry.Attempt.RunID, LeaseID: entry.Attempt.LeaseID, Attempt: entry.Attempt.Attempt, Fence: entry.Attempt.Fence}
	switch entry.Phase {
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

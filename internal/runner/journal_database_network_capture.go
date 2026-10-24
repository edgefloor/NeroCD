package runner

import (
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"nerocd/internal/domain"
)

// JournalDatabaseNetworkCapture retains an immutable read-only exchange. The
// historical fence permits exact receipt replay, never another host observation.
// Exactly one typed payload is present, selected by Phase.
// An unresolved chain blocks a replacement attempt even after expiry. Renewed
// attempts require a future explicit reconciliation contract; expiry alone is
// never evidence that the original origin request did not commit.
type JournalDatabaseNetworkCapture struct {
	ID        string                                      `json:"id"`
	Attempt   AttemptIdentity                             `json:"attempt_authority"`
	Phase     string                                      `json:"phase"`
	Claim     *domain.HostingDatabaseNetworkCaptureClaim  `json:"claim,omitempty"`
	Plan      *domain.HostingDatabaseNetworkCapture       `json:"plan,omitempty"`
	Origin    *domain.HostingDatabaseNetworkOriginRequest `json:"origin,omitempty"`
	Ack       *domain.HostingDatabaseNetworkOriginReceipt `json:"ack,omitempty"`
	CreatedAt time.Time                                   `json:"created_at"`
}

var errJournalDatabaseNetworkCapture = errors.New("invalid database network capture journal")

func validateCaptureJournalTarget(c domain.HostingDatabaseNetworkCapture) error {
	if c.Plan.Validate() != nil || c.ID != c.Plan.OperationID || c.RunID != "capture_"+c.ID || c.State != "running" || c.Receipt != nil || !storageArchiveJournalID.MatchString(c.CreatedBy) || c.CreatedAt.IsZero() || c.UpdatedAt.Before(c.CreatedAt) {
		return errJournalDatabaseNetworkCapture
	}
	return nil
}

func validateJournalDatabaseNetworkCapture(e JournalDatabaseNetworkCapture) error {
	if !storageArchiveJournalID.MatchString(e.ID) || validateAttemptIdentity(e.Attempt) != nil || e.CreatedAt.IsZero() {
		return errJournalDatabaseNetworkCapture
	}
	n := 0
	for _, present := range []bool{e.Claim != nil, e.Plan != nil, e.Origin != nil, e.Ack != nil} {
		if present {
			n++
		}
	}
	if n != 1 {
		return errJournalDatabaseNetworkCapture
	}
	switch e.Phase {
	case "claim":
		if e.Claim == nil {
			return errJournalDatabaseNetworkCapture
		}
		c, l := e.Claim.Capture, e.Claim.Lease
		if validateCaptureJournalTarget(c) != nil || l.RunID != c.RunID || l.RunnerID != c.Plan.RunnerID || l.Status != domain.LeaseActive || l.CompletedAt != nil || l.CompletionKey != "" || l.Attempt > 2147483647 || !storageArchiveJournalID.MatchString(l.ID) || !storageArchiveJournalID.MatchString(l.Fence) || l.ExpiresAt.Sub(l.CreatedAt) > 10*time.Minute || !reflect.DeepEqual(e.Attempt, AttemptIdentity{RunID: l.RunID, LeaseID: l.ID, Attempt: l.Attempt, Fence: l.Fence, CreatedAt: l.CreatedAt, ExpiresAt: l.ExpiresAt}) {
			return errJournalDatabaseNetworkCapture
		}
	case "plan":
		if e.Plan == nil || validateCaptureJournalTarget(*e.Plan) != nil || e.Plan.RunID != e.Attempt.RunID {
			return errJournalDatabaseNetworkCapture
		}
	case "origin":
		if e.Origin == nil || e.Origin.Validate() != nil || e.Origin.RunID != e.Attempt.RunID || e.Origin.LeaseID != e.Attempt.LeaseID || e.Origin.Attempt != e.Attempt.Attempt || e.Origin.Fence != e.Attempt.Fence {
			return errJournalDatabaseNetworkCapture
		}
		// The controller accepts sampling from lease creation minus 30 seconds
		// through acceptance plus 30 seconds. Acceptance needs a live lease.
		if e.Origin.ObservedAt.Before(e.Attempt.CreatedAt.Add(-30*time.Second)) || e.Origin.ObservedAt.After(e.Attempt.ExpiresAt.Add(30*time.Second)) {
			return errJournalDatabaseNetworkCapture
		}
	case "ack":
		if e.Ack == nil || e.Ack.CreatedAt.IsZero() {
			return errJournalDatabaseNetworkCapture
		}
	default:
		return errJournalDatabaseNetworkCapture
	}
	return nil
}

func validateJournalDatabaseNetworkCaptures(state journalState) error {
	chains := make(map[string][]JournalDatabaseNetworkCapture)
	ids := make(map[string]bool)
	for _, e := range state.DatabaseNetworkCapture {
		if validateJournalDatabaseNetworkCapture(e) != nil {
			return errJournalDatabaseNetworkCapture
		}
		if ids[e.ID] {
			return ErrJournalConflict
		}
		ids[e.ID] = true
		chain := chains[e.Attempt.RunID]
		phases := [...]string{"claim", "plan", "origin", "ack"}
		if len(chain) >= len(phases) || e.Phase != phases[len(chain)] {
			return errJournalDatabaseNetworkCapture
		}
		if len(chain) > 0 {
			first := chain[0]
			if !reflect.DeepEqual(e.Attempt, first.Attempt) {
				return errJournalDatabaseNetworkCapture
			}
			p := first.Claim.Capture.Plan
			switch e.Phase {
			case "plan":
				if !reflect.DeepEqual(*e.Plan, first.Claim.Capture) {
					return errJournalDatabaseNetworkCapture
				}
			case "origin":
				if e.Origin.OperationID != p.OperationID || e.Origin.PlanHash != p.Hash || p.ValidateOrigin(e.Origin.Origin) != nil {
					return errJournalDatabaseNetworkCapture
				}
			case "ack":
				in, ack := chain[2].Origin, e.Ack
				if ack.ID != in.ReceiptID || ack.BodyHash != domain.HostingDocumentHash(*in) || ack.OperationID != p.OperationID || ack.SourceOperationID != p.SourceOperationID || ack.PlanHash != p.Hash || ack.OriginHash != in.Origin.DocumentHash() || ack.Origin != in.Origin || !ack.ObservedAt.Equal(in.ObservedAt) || ack.RunnerID != p.RunnerID || ack.ObservedAt.After(ack.CreatedAt.Add(30*time.Second)) {
					return errJournalDatabaseNetworkCapture
				}
			}
		}
		chains[e.Attempt.RunID] = append(chain, e)
	}
	// Capture owns a separate task and cannot borrow an executable or generic
	// completion channel, even under a different fence for that same run.
	for run := range chains {
		for _, e := range state.Events {
			if ids[e.ID] {
				return ErrJournalConflict
			}
			if e.Attempt.RunID == run {
				return errJournalDatabaseNetworkCapture
			}
		}
		for _, e := range state.Completions {
			if ids[e.ID] {
				return ErrJournalConflict
			}
			if e.Attempt.RunID == run {
				return errJournalDatabaseNetworkCapture
			}
		}
		for _, e := range state.Provenance {
			if ids[e.ID] {
				return ErrJournalConflict
			}
			if e.Attempt.RunID == run {
				return errJournalDatabaseNetworkCapture
			}
		}
		for _, e := range state.Hosting {
			if ids[e.ID] {
				return ErrJournalConflict
			}
			if e.Attempt.RunID == run {
				return errJournalDatabaseNetworkCapture
			}
		}
		for _, e := range state.Proxy {
			if ids[e.ID] {
				return ErrJournalConflict
			}
			if e.Attempt.RunID == run {
				return errJournalDatabaseNetworkCapture
			}
		}
		for _, e := range state.Build {
			if ids[e.ID] {
				return ErrJournalConflict
			}
			if e.Attempt.RunID == run {
				return errJournalDatabaseNetworkCapture
			}
		}
		for _, e := range state.BuildLogs {
			if ids[e.ID] {
				return ErrJournalConflict
			}
			if e.Attempt.RunID == run {
				return errJournalDatabaseNetworkCapture
			}
		}
		for _, e := range state.BuildLogCursors {
			if e.Authority.RunID == run {
				return errJournalDatabaseNetworkCapture
			}
		}
		for _, e := range state.StorageArchive {
			if ids[e.ID] {
				return ErrJournalConflict
			}
			if e.Attempt.RunID == run {
				return errJournalDatabaseNetworkCapture
			}
		}
	}
	return nil
}

func cloneDatabaseNetworkCaptures(entries []JournalDatabaseNetworkCapture) []JournalDatabaseNetworkCapture {
	if entries == nil {
		return nil
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		panic("validated capture journal cannot marshal")
	}
	var out []JournalDatabaseNetworkCapture
	if json.Unmarshal(raw, &out) != nil {
		panic("validated capture journal cannot clone")
	}
	return out
}

// AppendDatabaseNetworkCapture durably records one phase before sending its
// exchange. An origin entry is the original request, not a recipe to sample again.
func (j *AttemptJournal) AppendDatabaseNetworkCapture(e JournalDatabaseNetworkCapture) (JournalDatabaseNetworkCapture, error) {
	if validateJournalDatabaseNetworkCapture(e) != nil {
		return JournalDatabaseNetworkCapture{}, errJournalDatabaseNetworkCapture
	}
	// Shape validation alone does not guarantee that times can be encoded or
	// nested documents fit their strict transport budgets. Reject those inputs
	// before using the infallible clone reserved for already accepted state.
	raw, err := json.Marshal(e)
	if err != nil {
		return JournalDatabaseNetworkCapture{}, errJournalDatabaseNetworkCapture
	}
	var copied JournalDatabaseNetworkCapture
	if json.Unmarshal(raw, &copied) != nil {
		return JournalDatabaseNetworkCapture{}, errJournalDatabaseNetworkCapture
	}
	e = copied
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, old := range j.state.DatabaseNetworkCapture {
		if old.ID == e.ID {
			if !reflect.DeepEqual(old, e) {
				return JournalDatabaseNetworkCapture{}, ErrJournalConflict
			}
			return cloneDatabaseNetworkCaptures([]JournalDatabaseNetworkCapture{old})[0], nil
		}
	}
	if j.entryCountLocked() >= journalMaxEntries {
		return JournalDatabaseNetworkCapture{}, errors.New("runner journal entry limit reached")
	}
	version := j.state.Version
	previous := j.state.DatabaseNetworkCapture
	j.state.Version = 13
	j.state.DatabaseNetworkCapture = append(j.state.DatabaseNetworkCapture, e)
	err = validateJournalState(j.state)
	if err == nil {
		err = j.persistLocked()
	}
	if err != nil {
		j.state.DatabaseNetworkCapture = previous
		j.state.Version = version
		return JournalDatabaseNetworkCapture{}, err
	}
	return cloneDatabaseNetworkCaptures([]JournalDatabaseNetworkCapture{e})[0], nil
}

// RetireDatabaseNetworkCapture removes only an entire acknowledged exchange for
// the exact original lease, including its retained sampling request.
func (j *AttemptJournal) RetireDatabaseNetworkCapture(at AttemptIdentity) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if validateAttemptIdentity(at) != nil || validateJournalState(j.state) != nil {
		return errJournalDatabaseNetworkCapture
	}
	kept := make([]JournalDatabaseNetworkCapture, 0, len(j.state.DatabaseNetworkCapture))
	count := 0
	for _, e := range j.state.DatabaseNetworkCapture {
		if reflect.DeepEqual(e.Attempt, at) {
			count++
		} else {
			kept = append(kept, e)
		}
	}
	if count == 0 {
		return nil
	}
	if count != 4 {
		return errJournalDatabaseNetworkCapture
	}
	previous := j.state.DatabaseNetworkCapture
	j.state.DatabaseNetworkCapture = kept
	if err := j.persistLocked(); err != nil {
		j.state.DatabaseNetworkCapture = previous
		return err
	}
	return nil
}

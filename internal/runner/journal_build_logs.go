package runner

import (
	"errors"
	"math"
	"reflect"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
)

// JournalBuildLog is one canonical event allocated and persisted with its cursor.
// Batch always contains exactly one event; transport may combine retained events.
type JournalBuildLog struct {
	ID        string                      `json:"id"`
	Attempt   AttemptIdentity             `json:"attempt_authority"`
	Batch     domain.HostingBuildLogBatch `json:"batch"`
	CreatedAt time.Time                   `json:"created_at"`
}

// JournalBuildLogCursor survives log acknowledgments until exact completion.
type JournalBuildLogCursor struct {
	Authority    domain.HostingBuildAuthority `json:"authority"`
	LastSequence int                          `json:"last_sequence"`
}

func cloneBuildLogs(entries []JournalBuildLog) []JournalBuildLog {
	if entries == nil {
		return nil
	}
	out := make([]JournalBuildLog, len(entries))
	for i, e := range entries {
		out[i] = e
		out[i].Batch.Events = append([]domain.HostingBuildLogEvent(nil), e.Batch.Events...)
		for n, event := range out[i].Batch.Events {
			if event.SourceTimestamp != nil {
				v := *event.SourceTimestamp
				out[i].Batch.Events[n].SourceTimestamp = &v
			}
		}
	}
	return out
}

// AppendBuildLog atomically allocates identity and sequence, then appends before
// send. Failed persistence restores both the cursor and the pending event.
func (j *AttemptJournal) AppendBuildLog(at AttemptIdentity, buildID, component, stream, message string) (JournalBuildLog, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	a := domain.HostingBuildAuthority{BuildID: buildID, RunID: at.RunID, LeaseID: at.LeaseID, Attempt: at.Attempt, Fence: at.Fence}
	index := -1
	sequence := 1
	for i, c := range j.state.BuildLogCursors {
		if c.Authority == a {
			index = i
			sequence = c.LastSequence + 1
		}
	}
	if sequence > math.MaxInt32 {
		return JournalBuildLog{}, errors.New("build log sequence exhausted")
	}
	id, err := NewJournalID("build_log")
	if err != nil {
		return JournalBuildLog{}, err
	}
	b, err := domain.CanonicalHostingBuildLogs(domain.HostingBuildLogBatch{Authority: a, Events: []domain.HostingBuildLogEvent{{EventKey: id, Sequence: sequence, Component: component, Stream: stream, Message: message}}})
	if err != nil {
		return JournalBuildLog{}, err
	}
	entry := JournalBuildLog{ID: id, Attempt: at, Batch: b, CreatedAt: time.Now().UTC()}
	previous := j.state
	j.state.BuildLogs = append(append([]JournalBuildLog(nil), previous.BuildLogs...), entry)
	j.state.BuildLogCursors = append([]JournalBuildLogCursor(nil), previous.BuildLogCursors...)
	if index < 0 {
		j.state.BuildLogCursors = append(j.state.BuildLogCursors, JournalBuildLogCursor{Authority: a, LastSequence: sequence})
	} else {
		j.state.BuildLogCursors[index].LastSequence = sequence
	}
	j.state.Version = 7
	if err = validateJournalState(j.state); err == nil {
		err = j.persistLocked()
	}
	if err != nil {
		j.state = previous
		return JournalBuildLog{}, err
	}
	return cloneBuildLogs([]JournalBuildLog{entry})[0], nil
}

// AckBuildLogs validates the ordered exact retained batch itself before removal.
// Cursors remain durable even when every pending event has been acknowledged.
func (j *AttemptJournal) AckBuildLogs(batch domain.HostingBuildLogBatch, ack domain.HostingBuildLogAck) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := hostingbuildprotocol.ValidateLogAcknowledgment(batch, ack); err != nil {
		return err
	}
	wanted := map[string]domain.HostingBuildLogEvent{}
	for _, e := range batch.Events {
		wanted[e.EventKey] = e
	}
	kept := make([]JournalBuildLog, 0, len(j.state.BuildLogs))
	matched := 0
	for _, e := range j.state.BuildLogs {
		event, ok := wanted[e.ID]
		if !ok {
			kept = append(kept, e)
			continue
		}
		if e.Batch.Authority != batch.Authority || !reflect.DeepEqual(e.Batch.Events[0], event) {
			return ErrJournalConflict
		}
		matched++
	}
	if matched != len(batch.Events) {
		return ErrJournalConflict
	}
	previous := j.state.BuildLogs
	j.state.BuildLogs = kept
	if err := j.persistLocked(); err != nil {
		j.state.BuildLogs = previous
		return err
	}
	return nil
}

// AckBuildCompletion atomically settles only the exact retained result authority
// and its intent and cursor, after all its log evidence has been acknowledged.
func (j *AttemptJournal) AckBuildCompletion(id string, ack hostingbuildprotocol.ReceiptAcknowledgment) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	var result *JournalBuild
	for i := range j.state.Build {
		if j.state.Build[i].ID == id {
			result = &j.state.Build[i]
			break
		}
	}
	if result == nil || result.Result == nil {
		return ErrJournalConflict
	}
	r := result.Result
	phase, err := hostingbuildprotocol.CompletionPhase(r.Completion)
	if err != nil {
		return err
	}
	if err = hostingbuildprotocol.ValidateAcknowledgment(ack, r.Authority, phase, r.ReceiptID, hostingbuildprotocol.CompletionHash(r.Completion)); err != nil {
		return err
	}
	for _, entry := range j.state.BuildLogs {
		if entry.Batch.Authority == r.Authority {
			return errors.New("build completion has pending logs")
		}
	}
	previous := j.state
	j.state.Build = nil
	for _, entry := range previous.Build {
		if entry.ID != id && (entry.Phase != "attempt_intent" || buildJournalAuthority(entry) != r.Authority) {
			j.state.Build = append(j.state.Build, entry)
		}
	}
	j.state.BuildLogCursors = nil
	for _, c := range previous.BuildLogCursors {
		if c.Authority != r.Authority {
			j.state.BuildLogCursors = append(j.state.BuildLogCursors, c)
		}
	}
	if err = j.persistLocked(); err != nil {
		j.state = previous
		return err
	}
	return nil
}

func validateJournalBuildLogs(s journalState) error {
	if len(s.BuildLogs)+len(s.BuildLogCursors) == 0 {
		return nil
	}
	if s.Version < 7 {
		return errors.New("build log journal requires version 7")
	}
	ids := map[string]bool{}
	add := func(id string) error {
		if ids[id] {
			return ErrJournalConflict
		}
		ids[id] = true
		return nil
	}
	for _, e := range s.Events {
		if err := add(e.ID); err != nil {
			return err
		}
	}
	for _, e := range s.Completions {
		if err := add(e.ID); err != nil {
			return err
		}
	}
	for _, e := range s.Provenance {
		if err := add(e.ID); err != nil {
			return err
		}
	}
	for _, e := range s.Hosting {
		if err := add(e.ID); err != nil {
			return err
		}
	}
	for _, e := range s.Proxy {
		if err := add(e.ID); err != nil {
			return err
		}
	}
	for _, e := range s.Build {
		if err := add(e.ID); err != nil {
			return err
		}
	}
	cursors := map[domain.HostingBuildAuthority]int{}
	for _, c := range s.BuildLogCursors {
		if hostingbuildprotocol.ValidateAuthority(c.Authority) != nil || c.LastSequence < 1 || c.LastSequence > math.MaxInt32 || cursors[c.Authority] != 0 {
			return errors.New("invalid build log cursor")
		}
		intent := false
		for _, e := range s.Build {
			if e.Phase == "attempt_intent" && buildJournalAuthority(e) == c.Authority {
				intent = true
			}
		}
		if !intent {
			return errors.New("build log cursor has no immutable intent")
		}
		for _, e := range s.Events {
			if e.Attempt.RunID == c.Authority.RunID {
				return errors.New("build logs cannot use generic events")
			}
		}
		for _, e := range s.Completions {
			if e.Attempt.RunID == c.Authority.RunID {
				return errors.New("build logs cannot use generic completion")
			}
		}
		cursors[c.Authority] = c.LastSequence
	}
	last := map[domain.HostingBuildAuthority]int{}
	for _, e := range s.BuildLogs {
		if err := add(e.ID); err != nil {
			return err
		}
		b, err := domain.CanonicalHostingBuildLogs(e.Batch)
		if err != nil || len(b.Events) != 1 || validateAttemptIdentity(e.Attempt) != nil || e.CreatedAt.IsZero() || !reflect.DeepEqual(b, e.Batch) {
			return errors.New("invalid build log journal event")
		}
		a := b.Authority
		if a.RunID != e.Attempt.RunID || a.LeaseID != e.Attempt.LeaseID || a.Attempt != e.Attempt.Attempt || a.Fence != e.Attempt.Fence || e.ID != b.Events[0].EventKey || b.Events[0].Sequence <= last[a] || b.Events[0].Sequence > cursors[a] {
			return ErrJournalConflict
		}
		last[a] = b.Events[0].Sequence
	}
	return nil
}

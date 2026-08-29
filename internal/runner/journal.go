package runner

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	journalFormatVersion      = 1
	journalMaxEntries         = 8192
	journalMaxBytes           = 8 << 20
	journalMaxEventBytes      = 256 << 10
	journalMaxProvenanceBytes = 64 << 10
)

// ErrJournalConflict indicates reuse of a journal ID with different content.
var ErrJournalConflict = errors.New("runner journal id reused with different content")

// AttemptIdentity is the complete fenced authority required to replay an
// attempt mutation. Bearer credentials deliberately do not belong here: they
// remain in the separately permissioned credential file.
type AttemptIdentity struct {
	RunID     string    `json:"run_id"`
	LeaseID   string    `json:"lease_id"`
	Attempt   int       `json:"attempt"`
	Fence     string    `json:"fence"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// JournalEvent is a durable runner output event.
type JournalEvent struct {
	ID        string          `json:"id"`
	Attempt   AttemptIdentity `json:"attempt_authority"`
	Sequence  int             `json:"sequence"`
	Stream    string          `json:"stream"`
	Message   string          `json:"message"`
	CreatedAt time.Time       `json:"created_at"`
}

// JournalCompletion is a durable terminal attempt outcome.
type JournalCompletion struct {
	ID        string          `json:"id"`
	Attempt   AttemptIdentity `json:"attempt_authority"`
	Status    string          `json:"status"`
	CreatedAt time.Time       `json:"created_at"`
}

// JournalProvenance is the complete, non-secret resolution request that must
// survive a response loss. It deliberately contains evidence and authority,
// never repository credentials, local workspace paths, or transport headers.
type JournalProvenance struct {
	ID              string          `json:"id"`
	Attempt         AttemptIdentity `json:"attempt_authority"`
	DeploymentID    string          `json:"deployment_id"`
	GitCommit       string          `json:"git_commit"`
	ComposeHash     string          `json:"compose_hash"`
	ImageDigests    []string        `json:"image_digests"`
	ContentIdentity string          `json:"content_identity"`
	CreatedAt       time.Time       `json:"created_at"`
}

// JournalSnapshot is a copy of pending durable attempt mutations.
type JournalSnapshot struct {
	Events      []JournalEvent      `json:"events"`
	Completions []JournalCompletion `json:"completions"`
	Provenance  []JournalProvenance `json:"provenance"`
}

type journalState struct {
	Version     int                 `json:"version"`
	Events      []JournalEvent      `json:"events"`
	Completions []JournalCompletion `json:"completions"`
	Provenance  []JournalProvenance `json:"provenance"`
}

// AttemptJournal is an append-before-send, atomic-rewrite journal. The secure
// directory handle is held for the journal lifetime so later path replacement
// cannot redirect reads or writes through a symlink.
type AttemptJournal struct {
	mu    sync.Mutex
	store *secureJournalStore
	state journalState
}

// OpenAttemptJournal opens or creates the secure journal at path.
func OpenAttemptJournal(path string) (*AttemptJournal, error) {
	store, contents, err := openSecureJournalStore(strings.TrimSpace(path), journalMaxBytes)
	if err != nil {
		return nil, err
	}
	j := &AttemptJournal{store: store, state: journalState{Version: journalFormatVersion}}
	if len(contents) != 0 {
		if err := json.Unmarshal(contents, &j.state); err != nil {
			_ = store.Close()
			return nil, fmt.Errorf("decode runner journal: %w", err)
		}
		if err := validateJournalState(j.state); err != nil {
			_ = store.Close()
			return nil, err
		}
	}
	return j, nil
}

// Close releases the journal's secure resources.
func (j *AttemptJournal) Close() error {
	if j == nil || j.store == nil {
		return nil
	}
	return j.store.Close()
}

// AppendEvent durably appends event unless its ID is already recorded.
func (j *AttemptJournal) AppendEvent(event JournalEvent) (JournalEvent, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := validateJournalEvent(event); err != nil {
		return JournalEvent{}, err
	}
	for _, existing := range j.state.Events {
		if existing.ID != event.ID {
			continue
		}
		if journalEventsEqual(existing, event) {
			return existing, nil
		}
		return JournalEvent{}, ErrJournalConflict
	}
	if j.entryCountLocked() >= journalMaxEntries {
		return JournalEvent{}, errors.New("runner journal entry limit reached")
	}
	j.state.Events = append(j.state.Events, event)
	if err := j.persistLocked(); err != nil {
		j.state.Events = j.state.Events[:len(j.state.Events)-1]
		return JournalEvent{}, err
	}
	return event, nil
}

// AppendCompletion durably appends completion unless its ID is already recorded.
func (j *AttemptJournal) AppendCompletion(completion JournalCompletion) (JournalCompletion, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := validateJournalCompletion(completion); err != nil {
		return JournalCompletion{}, err
	}
	for _, existing := range j.state.Completions {
		if existing.ID != completion.ID {
			continue
		}
		if journalCompletionsEqual(existing, completion) {
			return existing, nil
		}
		return JournalCompletion{}, ErrJournalConflict
	}
	if j.entryCountLocked() >= journalMaxEntries {
		return JournalCompletion{}, errors.New("runner journal entry limit reached")
	}
	j.state.Completions = append(j.state.Completions, completion)
	if err := j.persistLocked(); err != nil {
		j.state.Completions = j.state.Completions[:len(j.state.Completions)-1]
		return JournalCompletion{}, err
	}
	return completion, nil
}

// AppendProvenance durably appends provenance unless its ID is already recorded.
func (j *AttemptJournal) AppendProvenance(provenance JournalProvenance) (JournalProvenance, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := validateJournalProvenance(provenance); err != nil {
		return JournalProvenance{}, err
	}
	for _, existing := range j.state.Provenance {
		if existing.ID != provenance.ID {
			continue
		}
		if journalProvenanceEqual(existing, provenance) {
			return existing, nil
		}
		return JournalProvenance{}, ErrJournalConflict
	}
	if j.entryCountLocked() >= journalMaxEntries {
		return JournalProvenance{}, errors.New("runner journal entry limit reached")
	}
	j.state.Provenance = append(j.state.Provenance, provenance)
	if err := j.persistLocked(); err != nil {
		j.state.Provenance = j.state.Provenance[:len(j.state.Provenance)-1]
		return JournalProvenance{}, err
	}
	return provenance, nil
}

// AckEvents removes acknowledged event IDs from the journal.
func (j *AttemptJournal) AckEvents(ids []string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	wanted := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	kept := make([]JournalEvent, 0, len(j.state.Events))
	for _, event := range j.state.Events {
		if _, ok := wanted[event.ID]; !ok {
			kept = append(kept, event)
		}
	}
	if len(kept) == len(j.state.Events) {
		return nil
	}
	original := j.state.Events
	j.state.Events = kept
	if err := j.persistLocked(); err != nil {
		j.state.Events = original
		return err
	}
	return nil
}

// AckCompletion removes an acknowledged completion from the journal.
func (j *AttemptJournal) AckCompletion(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	kept := make([]JournalCompletion, 0, len(j.state.Completions))
	for _, completion := range j.state.Completions {
		if completion.ID != id {
			kept = append(kept, completion)
		}
	}
	if len(kept) == len(j.state.Completions) {
		return nil
	}
	original := j.state.Completions
	j.state.Completions = kept
	if err := j.persistLocked(); err != nil {
		j.state.Completions = original
		return err
	}
	return nil
}

// AckProvenance removes acknowledged provenance from the journal.
func (j *AttemptJournal) AckProvenance(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	kept := make([]JournalProvenance, 0, len(j.state.Provenance))
	for _, pending := range j.state.Provenance {
		if pending.ID != id {
			kept = append(kept, pending)
		}
	}
	if len(kept) == len(j.state.Provenance) {
		return nil
	}
	original := j.state.Provenance
	j.state.Provenance = kept
	if err := j.persistLocked(); err != nil {
		j.state.Provenance = original
		return err
	}
	return nil
}

// DiscardAttempt removes all pending mutations for one fenced attempt.
func (j *AttemptJournal) DiscardAttempt(leaseID string, attempt int) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	events := make([]JournalEvent, 0, len(j.state.Events))
	for _, event := range j.state.Events {
		if event.Attempt.LeaseID != leaseID || event.Attempt.Attempt != attempt {
			events = append(events, event)
		}
	}
	completions := make([]JournalCompletion, 0, len(j.state.Completions))
	for _, completion := range j.state.Completions {
		if completion.Attempt.LeaseID != leaseID || completion.Attempt.Attempt != attempt {
			completions = append(completions, completion)
		}
	}
	provenance := make([]JournalProvenance, 0, len(j.state.Provenance))
	for _, pending := range j.state.Provenance {
		if pending.Attempt.LeaseID != leaseID || pending.Attempt.Attempt != attempt {
			provenance = append(provenance, pending)
		}
	}
	if len(events) == len(j.state.Events) && len(completions) == len(j.state.Completions) && len(provenance) == len(j.state.Provenance) {
		return nil
	}
	originalEvents, originalCompletions, originalProvenance := j.state.Events, j.state.Completions, j.state.Provenance
	j.state.Events, j.state.Completions, j.state.Provenance = events, completions, provenance
	if err := j.persistLocked(); err != nil {
		j.state.Events, j.state.Completions, j.state.Provenance = originalEvents, originalCompletions, originalProvenance
		return err
	}
	return nil
}

// Snapshot returns a copy of pending journal state.
func (j *AttemptJournal) Snapshot() JournalSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	return JournalSnapshot{
		Events:      append([]JournalEvent(nil), j.state.Events...),
		Completions: append([]JournalCompletion(nil), j.state.Completions...),
		Provenance:  append([]JournalProvenance(nil), j.state.Provenance...),
	}
}

// Depth returns the number of pending journal entries.
func (j *AttemptJournal) Depth() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.entryCountLocked()
}

// NewJournalID returns a cryptographically random journal identifier.
func NewJournalID(prefix string) (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(entropy[:]), nil
}

func (j *AttemptJournal) entryCountLocked() int {
	return len(j.state.Events) + len(j.state.Completions) + len(j.state.Provenance)
}

func (j *AttemptJournal) persistLocked() error {
	contents, err := json.Marshal(j.state)
	if err != nil {
		return err
	}
	if len(contents) > journalMaxBytes {
		return errors.New("runner journal byte limit reached")
	}
	return j.store.Write(contents)
}

func validateJournalState(state journalState) error {
	if state.Version != journalFormatVersion {
		return fmt.Errorf("unsupported runner journal version %d", state.Version)
	}
	if len(state.Events)+len(state.Completions)+len(state.Provenance) > journalMaxEntries {
		return errors.New("runner journal entry limit exceeded")
	}
	ids := make(map[string]struct{}, len(state.Events)+len(state.Completions)+len(state.Provenance))
	for _, event := range state.Events {
		if err := validateJournalEvent(event); err != nil {
			return err
		}
		if _, ok := ids[event.ID]; ok {
			return errors.New("runner journal contains duplicate ids")
		}
		ids[event.ID] = struct{}{}
	}
	for _, completion := range state.Completions {
		if err := validateJournalCompletion(completion); err != nil {
			return err
		}
		if _, ok := ids[completion.ID]; ok {
			return errors.New("runner journal contains duplicate ids")
		}
		ids[completion.ID] = struct{}{}
	}
	for _, pending := range state.Provenance {
		if err := validateJournalProvenance(pending); err != nil {
			return err
		}
		if _, ok := ids[pending.ID]; ok {
			return errors.New("runner journal contains duplicate ids")
		}
		ids[pending.ID] = struct{}{}
	}
	return nil
}

func validateJournalProvenance(p JournalProvenance) error {
	if strings.TrimSpace(p.ID) == "" || strings.TrimSpace(p.DeploymentID) == "" || strings.TrimSpace(p.GitCommit) == "" || strings.TrimSpace(p.ComposeHash) == "" || strings.TrimSpace(p.ContentIdentity) != strings.TrimSpace(p.GitCommit)+":"+strings.TrimSpace(p.ComposeHash) || p.CreatedAt.IsZero() {
		return errors.New("journal provenance requires exact authority and immutable evidence")
	}
	if err := validateAttemptIdentity(p.Attempt); err != nil {
		return err
	}
	if len(p.ImageDigests) == 0 {
		return errors.New("journal provenance requires image digests")
	}
	size := len(p.ID) + len(p.DeploymentID) + len(p.GitCommit) + len(p.ComposeHash) + len(p.ContentIdentity)
	for i, digest := range p.ImageDigests {
		if strings.TrimSpace(digest) == "" || (i > 0 && p.ImageDigests[i-1] >= digest) {
			return errors.New("journal provenance image digests are invalid")
		}
		size += len(digest)
	}
	if size > journalMaxProvenanceBytes {
		return errors.New("journal provenance exceeds transport byte limit")
	}
	return nil
}

func validateJournalEvent(event JournalEvent) error {
	if strings.TrimSpace(event.ID) == "" || event.Sequence <= 0 || strings.TrimSpace(event.Stream) == "" {
		return errors.New("journal event requires id, positive sequence, and stream")
	}
	if len(event.ID)+len(event.Stream)+len(event.Message) > journalMaxEventBytes {
		return errors.New("journal event exceeds transport byte limit")
	}
	return validateAttemptIdentity(event.Attempt)
}

func validateJournalCompletion(completion JournalCompletion) error {
	if strings.TrimSpace(completion.ID) == "" || strings.TrimSpace(completion.Status) == "" {
		return errors.New("journal completion requires id and status")
	}
	return validateAttemptIdentity(completion.Attempt)
}

func validateAttemptIdentity(attempt AttemptIdentity) error {
	if strings.TrimSpace(attempt.RunID) == "" || strings.TrimSpace(attempt.LeaseID) == "" || attempt.Attempt <= 0 || strings.TrimSpace(attempt.Fence) == "" {
		return errors.New("journal attempt requires run_id, lease_id, positive attempt, and fence")
	}
	if attempt.CreatedAt.IsZero() || !attempt.ExpiresAt.After(attempt.CreatedAt) {
		return errors.New("journal attempt lease timestamps are invalid")
	}
	return nil
}

func journalEventsEqual(left, right JournalEvent) bool {
	return left.ID == right.ID && left.Attempt == right.Attempt && left.Sequence == right.Sequence && left.Stream == right.Stream && left.Message == right.Message && left.CreatedAt.Equal(right.CreatedAt)
}

func journalCompletionsEqual(left, right JournalCompletion) bool {
	return left.ID == right.ID && left.Attempt == right.Attempt && left.Status == right.Status && left.CreatedAt.Equal(right.CreatedAt)
}

func journalProvenanceEqual(left, right JournalProvenance) bool {
	if left.ID != right.ID || left.Attempt != right.Attempt || left.DeploymentID != right.DeploymentID || left.GitCommit != right.GitCommit || left.ComposeHash != right.ComposeHash || left.ContentIdentity != right.ContentIdentity || !left.CreatedAt.Equal(right.CreatedAt) || len(left.ImageDigests) != len(right.ImageDigests) {
		return false
	}
	for i := range left.ImageDigests {
		if left.ImageDigests[i] != right.ImageDigests[i] {
			return false
		}
	}
	return true
}

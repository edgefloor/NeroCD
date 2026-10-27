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
	StorageBackupRetentions  []JournalStorageBackupRetention  `json:"storage_backup_retentions,omitempty"`
	DatabaseBackupRetentions []JournalDatabaseBackupRetention `json:"database_backup_retentions,omitempty"`
	StorageBackupTransfers   []JournalStorageBackupTransfer   `json:"storage_backup_transfers,omitempty"`
	DatabaseBackupTransfers  []JournalDatabaseBackupTransfer  `json:"database_backup_transfers,omitempty"`
	DatabaseBackup           []JournalDatabaseBackup          `json:"database_backup,omitempty"`
	DatabaseNetworkCapture   []JournalDatabaseNetworkCapture  `json:"database_network_capture,omitempty"`
	StorageArchive           []JournalStorageArchive          `json:"storage_archive,omitempty"`
	BuildLogs                []JournalBuildLog                `json:"build_logs,omitempty"`
	BuildLogCursors          []JournalBuildLogCursor          `json:"build_log_cursors,omitempty"`
	Build                    []JournalBuild                   `json:"build,omitempty"`
	Proxy                    []JournalProxy                   `json:"proxy,omitempty"`
	Hosting                  []JournalHosting                 `json:"hosting,omitempty"`
	Events                   []JournalEvent                   `json:"events"`
	Completions              []JournalCompletion              `json:"completions"`
	Provenance               []JournalProvenance              `json:"provenance"`
}

type journalState struct {
	StackBaseVersion         int                              `json:"stack_base_version,omitempty"`
	StorageBackupRetentions  []JournalStorageBackupRetention  `json:"storage_backup_retentions,omitempty"`
	DatabaseBackupRetentions []JournalDatabaseBackupRetention `json:"database_backup_retentions,omitempty"`
	StorageBackupTransfers   []JournalStorageBackupTransfer   `json:"storage_backup_transfers,omitempty"`
	DatabaseBackupTransfers  []JournalDatabaseBackupTransfer  `json:"database_backup_transfers,omitempty"`
	DatabaseBackup           []JournalDatabaseBackup          `json:"database_backup,omitempty"`
	DatabaseNetworkCapture   []JournalDatabaseNetworkCapture  `json:"database_network_capture,omitempty"`
	StorageArchive           []JournalStorageArchive          `json:"storage_archive,omitempty"`
	BuildLogs                []JournalBuildLog                `json:"build_logs,omitempty"`
	BuildLogCursors          []JournalBuildLogCursor          `json:"build_log_cursors,omitempty"`
	Build                    []JournalBuild                   `json:"build,omitempty"`
	Proxy                    []JournalProxy                   `json:"proxy,omitempty"`
	Hosting                  []JournalHosting                 `json:"hosting,omitempty"`
	Version                  int                              `json:"version"`
	Events                   []JournalEvent                   `json:"events"`
	Completions              []JournalCompletion              `json:"completions"`
	Provenance               []JournalProvenance              `json:"provenance"`
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
		if j.state.Version == 23 {
			contents, err = decodeStackJournal(contents, &j.state)
			if err != nil {
				_ = store.Close()
				return nil, err
			}
		} else if j.state.StackBaseVersion != 0 || journalStateUsesStack(j.state) {
			_ = store.Close()
			return nil, errors.New("managed stack journal requires version 23")
		}
		if journalStateUsesComposeStorageArchive(j.state) {
			if err := decodeStorageArchiveJournal(contents, &j.state); err != nil {
				_ = store.Close()
				return nil, err
			}
		} else {
			switch j.state.Version {
			case 12, 13:
				if err := decodeStorageArchiveJournal(contents, &j.state); err != nil {
					_ = store.Close()
					return nil, err
				}
			case 18, 19, 20, 22:
				if err := decodeDatabaseJournal(contents, &j.state); err != nil {
					_ = store.Close()
					return nil, err
				}
			case 10, 11, 14, 15, 16, 17:
				if err := decodeStorageJournal(contents, &j.state); err != nil {
					_ = store.Close()
					return nil, err
				}
			}
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
func (j *AttemptJournal) DiscardAttempt(authority AttemptIdentity) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, entry := range j.state.StorageBackupRetentions {
		if sameAttemptAuthority(entry.Attempt, authority) {
			return errors.New("storage retention requires explicit acknowledgment")
		}
	}
	for _, entry := range j.state.DatabaseBackupRetentions {
		if sameAttemptAuthority(entry.Attempt, authority) {
			return errors.New("retention evidence requires explicit acknowledgment")
		}
	}
	for _, entry := range j.state.StorageBackupTransfers {
		if sameAttemptAuthority(entry.Attempt, authority) {
			return errors.New("storage transfer evidence requires explicit acknowledgment")
		}
	}
	for _, entry := range j.state.DatabaseBackupTransfers {
		if sameAttemptAuthority(entry.Attempt, authority) {
			return errors.New("remote transfer evidence requires explicit acknowledgment")
		}
	}
	for _, entry := range j.state.DatabaseBackup {
		if sameAttemptAuthority(entry.Attempt, authority) {
			return errors.New("database backup evidence requires explicit receipt acknowledgment")
		}
	}
	for _, entry := range j.state.DatabaseNetworkCapture {
		if sameAttemptAuthority(entry.Attempt, authority) {
			return errors.New("capture evidence requires acknowledged retirement")
		}
	}
	for _, entry := range j.state.StorageArchive {
		if sameAttemptAuthority(entry.Attempt, authority) {
			return errors.New("archive mutation evidence requires explicit reconciliation")
		}
	}
	for _, cursor := range j.state.BuildLogCursors {
		if cursor.Authority.RunID == authority.RunID && cursor.Authority.LeaseID == authority.LeaseID && cursor.Authority.Attempt == authority.Attempt && cursor.Authority.Fence == authority.Fence {
			return errors.New("build log evidence requires explicit reconciliation")
		}
	}
	for _, entry := range j.state.Build {
		if sameAttemptAuthority(entry.Attempt, authority) {
			return errors.New("build mutation evidence requires explicit reconciliation")
		}
	}
	for _, entry := range j.state.Proxy {
		if sameAttemptAuthority(entry.Attempt, authority) {
			return errors.New("proxy mutation evidence requires explicit reconciliation")
		}
	}
	for _, entry := range j.state.Hosting {
		if sameAttemptAuthority(entry.Attempt, authority) {
			return errors.New("hosting mutation evidence requires explicit reconciliation")
		}
	}

	events := make([]JournalEvent, 0, len(j.state.Events))
	for _, event := range j.state.Events {
		if !sameAttemptAuthority(event.Attempt, authority) {
			events = append(events, event)
		}
	}
	completions := make([]JournalCompletion, 0, len(j.state.Completions))
	for _, completion := range j.state.Completions {
		if !sameAttemptAuthority(completion.Attempt, authority) {
			completions = append(completions, completion)
		}
	}
	provenance := make([]JournalProvenance, 0, len(j.state.Provenance))
	for _, pending := range j.state.Provenance {
		if !sameAttemptAuthority(pending.Attempt, authority) {
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

func sameAttemptAuthority(left, right AttemptIdentity) bool {
	return left.RunID == right.RunID && left.LeaseID == right.LeaseID && left.Attempt == right.Attempt && left.Fence == right.Fence
}

// Snapshot returns a copy of pending journal state.
func (j *AttemptJournal) Snapshot() JournalSnapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	return JournalSnapshot{StorageBackupRetentions: cloneStorageBackupRetentions(j.state.StorageBackupRetentions), StorageBackupTransfers: cloneStorageBackupTransfers(j.state.StorageBackupTransfers), DatabaseBackupRetentions: cloneDatabaseBackupRetentions(j.state.DatabaseBackupRetentions), DatabaseBackupTransfers: cloneDatabaseBackupTransfers(j.state.DatabaseBackupTransfers), DatabaseBackup: cloneDatabaseBackups(j.state.DatabaseBackup),
		DatabaseNetworkCapture: cloneDatabaseNetworkCaptures(j.state.DatabaseNetworkCapture),
		StorageArchive:         cloneStorageArchiveEntries(j.state.StorageArchive),
		BuildLogs:              cloneBuildLogs(j.state.BuildLogs),
		BuildLogCursors:        append([]JournalBuildLogCursor(nil), j.state.BuildLogCursors...),
		Build:                  cloneBuildEntries(j.state.Build),
		Proxy:                  cloneProxyEntries(j.state.Proxy),
		Hosting:                cloneHostingEntries(j.state.Hosting),
		Events:                 append([]JournalEvent(nil), j.state.Events...),
		Completions:            append([]JournalCompletion(nil), j.state.Completions...),
		Provenance:             append([]JournalProvenance(nil), j.state.Provenance...),
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
	return len(j.state.StorageBackupRetentions) + len(j.state.StorageBackupTransfers) + len(j.state.DatabaseBackupRetentions) + len(j.state.DatabaseBackupTransfers) + len(j.state.DatabaseBackup) + len(j.state.DatabaseNetworkCapture) + len(j.state.StorageArchive) + len(j.state.Events) + len(j.state.Completions) + len(j.state.Provenance) + len(j.state.Hosting) + len(j.state.Proxy) + len(j.state.Build) + len(j.state.BuildLogs) + len(j.state.BuildLogCursors)
}

func (j *AttemptJournal) persistLocked() error {
	if err := validateJournalStorageBackupTransfers(j.state); err != nil {
		return err
	}
	if err := validateJournalDatabaseBackupTransfers(j.state); err != nil {
		return err
	}
	if len(j.state.DatabaseBackupTransfers) > 0 {
		j.state.Version = 19
	}
	if err := validateJournalDatabaseBackups(j.state); err != nil {
		return err
	}
	if err := validateApplicationDatabaseJournalChain(j.state.Hosting); err != nil {
		return err
	}
	if err := validateJournalDatabaseNetworkCaptures(j.state); err != nil {
		return err
	}
	if err := validateJournalStorageArchives(j.state); err != nil {
		return err
	}
	if err := validateJournalBuildLogs(j.state); err != nil {
		return err
	}
	if err := validateJournalBuildEntries(j.state); err != nil {
		return err
	}
	if err := validateJournalDatabaseBackupRetentions(j.state); err != nil {
		return err
	}
	if journalUsesRetention(j.state) {
		j.state.Version = 20
	}
	state := j.state
	if state.Version == 22 {
		state.Version = 21
	}
	if err := validateJournalStorageBackupRetentions(state); err != nil {
		return err
	}
	if len(state.StorageBackupTransfers) > 0 {
		state.Version = 21
	}
	if state.Version == 21 && len(state.StorageBackupTransfers) == 0 {
		state.Version = 20
	}
	if state.Version == 20 && !journalUsesRetention(state) {
		state.Version = 19
	}
	if state.Version == 19 && len(state.DatabaseBackupTransfers) == 0 {
		state.Version = 18
	}
	if state.Version == 18 && len(state.DatabaseBackup) == 0 {
		state.Version = 17
	}
	if state.Version == 17 && !journalStateUsesApplicationStorage(state) {
		state.Version = 16
	}
	if state.Version == 16 && !journalStateUsesRouteContinuity(state) {
		state.Version = 15
	}
	if state.Version == 15 && !journalStateUsesConnection(state) {
		state.Version = 14
	}
	if state.Version == 14 && !journalStateUsesDatabaseUpgrade(state) {
		state.Version = 13
	}
	if state.Version == 13 && len(state.DatabaseNetworkCapture) == 0 {
		state.Version = 12
	}
	if state.Version == 12 && len(state.StorageArchive) == 0 {
		state.Version = 11
	}
	if state.Version == 11 && !journalStateUsesDatabase(state) {
		state.Version = 10
	}
	if state.Version == 10 && !journalStateUsesStorage(state) {
		state.Version = 9
	}
	if state.Version == 9 {
		strict := false
		for _, entry := range state.Proxy {
			strict = strict || entry.Plan.Snapshot.VerifiedSwitch != nil
		}
		if !strict {
			state.Version = 8
		}
	}
	if len(state.StorageBackupRetentions) > 0 {
		state.Version = 22
	}
	wire := state
	if journalStateUsesStack(state) {
		wire.StackBaseVersion = state.Version
		wire.Version = 23
	}
	contents, err := json.Marshal(wire)
	if err != nil {
		return err
	}
	if len(contents) > journalMaxBytes {
		return errors.New("runner journal byte limit reached")
	}
	if err = j.store.Write(contents); err != nil {
		return err
	}
	j.state.Version = state.Version
	return nil
}

func validateJournalState(state journalState) error {
	if len(state.StorageBackupRetentions) > 0 {
		if state.Version != 22 {
			return errors.New("storage retention journal version mismatch")
		}
		if err := validateJournalStorageBackupRetentions(state); err != nil {
			return err
		}
		if len(state.StorageBackupRetentions)+len(state.StorageBackupTransfers)+len(state.DatabaseBackupRetentions)+len(state.DatabaseBackupTransfers)+len(state.DatabaseBackup)+len(state.DatabaseNetworkCapture)+len(state.StorageArchive)+len(state.Events)+len(state.Completions)+len(state.Provenance)+len(state.Hosting)+len(state.Proxy)+len(state.Build)+len(state.BuildLogs)+len(state.BuildLogCursors) > journalMaxEntries {
			return errors.New("runner journal entry limit exceeded")
		}
		state.StorageBackupRetentions = nil
		state.Version = 21
		if len(state.StorageBackupTransfers) > 0 {
			state.Version = 21
		}
		if state.Version == 21 && len(state.StorageBackupTransfers) == 0 {
			state.Version = 20
		}
		if state.Version == 20 && !journalUsesRetention(state) {
			state.Version = 19
		}
		if state.Version == 19 && len(state.DatabaseBackupTransfers) == 0 {
			state.Version = 18
		}
		if state.Version == 18 && len(state.DatabaseBackup) == 0 {
			state.Version = 17
		}
		if state.Version == 17 && !journalStateUsesApplicationStorage(state) {
			state.Version = 16
		}
		if state.Version == 16 && !journalStateUsesRouteContinuity(state) {
			state.Version = 15
		}
		if state.Version == 15 && !journalStateUsesConnection(state) {
			state.Version = 14
		}
		if state.Version == 14 && !journalStateUsesDatabaseUpgrade(state) {
			state.Version = 13
		}
		if state.Version == 13 && len(state.DatabaseNetworkCapture) == 0 {
			state.Version = 12
		}
		if state.Version == 12 && len(state.StorageArchive) == 0 {
			state.Version = 11
		}
		if state.Version == 11 && !journalStateUsesDatabase(state) {
			state.Version = 10
		}
		if state.Version == 10 && !journalStateUsesStorage(state) {
			state.Version = 9
		}
		if state.Version == 9 {
			strict := false
			for _, entry := range state.Proxy {
				strict = strict || entry.Plan.Snapshot.VerifiedSwitch != nil
			}
			if !strict {
				state.Version = 8
			}
		}

	}

	if len(state.StorageBackupTransfers) > 0 {
		if state.Version != 21 {
			return errors.New("storage transfer journal version mismatch")
		}
		if err := validateJournalStorageBackupTransfers(state); err != nil {
			return err
		}
		state.Version = 20
		if state.Version == 20 && !journalUsesRetention(state) {
			state.Version = 19
		}
		if state.Version == 19 && len(state.DatabaseBackupTransfers) == 0 {
			state.Version = 18
		}
		if state.Version == 18 && len(state.DatabaseBackup) == 0 {
			state.Version = 17
		}
		if state.Version == 17 && !journalStateUsesApplicationStorage(state) {
			state.Version = 16
		}
		if state.Version == 16 && !journalStateUsesRouteContinuity(state) {
			state.Version = 15
		}
		if state.Version == 15 && !journalStateUsesConnection(state) {
			state.Version = 14
		}
		if state.Version == 14 && !journalStateUsesDatabaseUpgrade(state) {
			state.Version = 13
		}
		if state.Version == 13 && len(state.DatabaseNetworkCapture) == 0 {
			state.Version = 12
		}
		if state.Version == 12 && len(state.StorageArchive) == 0 {
			state.Version = 11
		}
		if state.Version == 11 && !journalStateUsesDatabase(state) {
			state.Version = 10
		}
		if state.Version == 10 && !journalStateUsesStorage(state) {
			state.Version = 9
		}
		if state.Version == 9 {
			strict := false
			for _, entry := range state.Proxy {
				strict = strict || entry.Plan.Snapshot.VerifiedSwitch != nil
			}
			if !strict {
				state.Version = 8
			}
		}

	}

	if journalUsesRetention(state) && state.Version != 20 || state.Version == 20 && !journalUsesRetention(state) {
		return errors.New("backup retention journal version mismatch")
	}
	if err := validateJournalDatabaseBackupRetentions(state); err != nil {
		return err
	}
	if len(state.DatabaseBackupTransfers) > 0 && state.Version < 19 || state.Version == 19 && len(state.DatabaseBackupTransfers) == 0 {
		return errors.New("remote backup transfer journal version mismatch")
	}
	if err := validateJournalDatabaseBackupTransfers(state); err != nil {
		return err
	}
	if len(state.DatabaseBackup) > 0 && state.Version < 18 || state.Version == 18 && len(state.DatabaseBackup) == 0 {
		return errors.New("database backup journal version mismatch")
	}
	if err := validateJournalDatabaseBackups(state); err != nil {
		return err
	}
	if err := validateApplicationDatabaseJournalChain(state.Hosting); err != nil {
		return err
	}
	if err := validateDatabaseUpgradeJournalChain(state.Hosting); err != nil {
		return err
	}
	if err := validateJournalDatabaseNetworkCaptures(state); err != nil {
		return err
	}
	if err := validateJournalStorageArchives(state); err != nil {
		return err
	}
	if state.Version < journalFormatVersion || state.Version > 20 {
		return fmt.Errorf("unsupported runner journal version %d", state.Version)
	}
	if len(state.StorageBackupTransfers)+len(state.DatabaseBackupRetentions)+len(state.DatabaseBackupTransfers)+len(state.DatabaseBackup)+len(state.DatabaseNetworkCapture)+len(state.StorageArchive)+len(state.Events)+len(state.Completions)+len(state.Provenance)+len(state.Hosting)+len(state.Proxy)+len(state.Build)+len(state.BuildLogs)+len(state.BuildLogCursors) > journalMaxEntries {
		return errors.New("runner journal entry limit exceeded")
	}
	ids := make(map[string]struct{}, len(state.Events)+len(state.Completions)+len(state.Provenance))
	if len(state.DatabaseNetworkCapture) > 0 && state.Version < 13 || state.Version == 13 && len(state.DatabaseNetworkCapture) == 0 {
		return errors.New("capture journal requires marked version 13 evidence")
	}
	for _, entry := range state.DatabaseNetworkCapture {
		if _, exists := ids[entry.ID]; exists {
			return errors.New("runner journal contains duplicate ids")
		}
		ids[entry.ID] = struct{}{}
	}
	if len(state.StorageArchive) > 0 && state.Version < 12 || state.Version == 12 && len(state.StorageArchive) == 0 {
		return errors.New("archive journal requires marked version 12 evidence")
	}
	for _, entry := range state.StorageArchive {
		if err := validateJournalStorageArchive(entry); err != nil {
			return err
		}
		if _, exists := ids[entry.ID]; exists {
			return errors.New("runner journal contains duplicate ids")
		}
		ids[entry.ID] = struct{}{}
	}
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
	if len(state.Hosting) > 0 && state.Version < 2 {
		return errors.New("hosting journal requires version2")
	}
	if state.Version == 14 && !journalStateUsesDatabaseUpgrade(state) {
		return errors.New("journal version 14 requires database upgrade evidence")
	}
	if state.Version == 15 && !journalStateUsesConnection(state) {
		return errors.New("journal version 15 requires connection evidence")
	}
	if state.Version == 16 && !journalStateUsesRouteContinuity(state) {
		return errors.New("journal version 16 requires route continuation evidence")
	}
	if state.Version == 17 && !journalStateUsesApplicationStorage(state) {
		return errJournalConnection
	}
	for _, entry := range state.Hosting {
		if entry.ApplicationStorage != nil && state.Version < 17 {
			return errJournalConnection
		}
		if hostingJournalUsesRouteContinuity(entry) && state.Version < 16 {
			return errors.New("route continuation requires journal version 16")
		}
		if hostingJournalUsesConnection(entry) && state.Version < 15 {
			return errors.New("connection journal requires version 15")
		}
		if entry.DatabaseUpgrade != nil && state.Version < 14 {
			return errors.New("database upgrade journal requires version 14")
		}
		if state.Version < 11 && HostingJournalUsesDatabase(entry) {
			return errors.New("database journal requires version 11")
		}
		if state.Version < 10 && HostingJournalUsesStorage(entry) {
			return errors.New("owned storage journal requires version 10")
		}
		if state.Version < 8 && HostingJournalUsesRuntimeEnvironment(entry) {
			return errors.New("runtime environment journal requires version 8")
		}
		if state.Version < 6 && HostingJournalUsesLocalBuild(entry) {
			return errors.New("local artifact journal requires version 6")
		}
		if state.Version < 3 && hostingJournalNeedsV3(entry) {
			return errors.New("retained release journal requires version 3")
		}
		if err := validateJournalHosting(entry); err != nil {
			return err
		}
		if _, exists := ids[entry.ID]; exists {
			return errors.New("runner journal contains duplicate ids")
		}
		ids[entry.ID] = struct{}{}
	}
	if len(state.Proxy) > 0 && state.Version < 4 {
		return errors.New("proxy journal requires version 4")
	}
	strictProxy := false
	for _, entry := range state.Proxy {
		strictProxy = strictProxy || entry.Plan.Snapshot.VerifiedSwitch != nil
		if entry.Plan.Snapshot.VerifiedSwitch != nil && state.Version < 9 {
			return errors.New("verified switch journal requires version 9")
		}
		if err := validateJournalProxy(entry); err != nil {
			return err
		}
		if _, exists := ids[entry.ID]; exists {
			return errors.New("runner journal contains duplicate ids")
		}
		ids[entry.ID] = struct{}{}
	}
	if state.Version == 9 && !strictProxy {
		return errors.New("journal version 9 requires verified switch intent")
	}
	if state.Version == 11 && !journalStateUsesDatabase(state) {
		return errors.New("journal version 11 requires database evidence")
	}
	if state.Version == 10 && !journalStateUsesStorage(state) {
		return errors.New("journal version 10 requires owned storage evidence")
	}

	if err := validateJournalBuildEntries(state); err != nil {
		return err
	}
	return validateJournalBuildLogs(state)
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

func journalUsesRetention(state journalState) bool {
	if len(state.DatabaseBackupRetentions) > 0 {
		return true
	}
	for _, e := range state.DatabaseBackup {
		if e.Request.LocalCopy != nil {
			return true
		}
	}
	for _, e := range state.DatabaseBackupTransfers {
		if e.Request.RemoteCopy != nil || e.Request.LocalCopy != nil {
			return true
		}
	}
	return false
}

package runner

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuildprotocol"
)

// JournalBuild retains only nonsecret immutable intent or exact phase requests.
// InputHash binds intent to a snapshot without storing URLs, paths or credentials.
type JournalBuild struct {
	ID        string                                    `json:"id"`
	Attempt   AttemptIdentity                           `json:"attempt_authority"`
	BuildID   string                                    `json:"build_id"`
	Phase     string                                    `json:"phase"`
	InputHash string                                    `json:"input_hash,omitempty"`
	Source    *hostingbuildprotocol.SourceCommitRequest `json:"source,omitempty"`
	Result    *hostingbuildprotocol.CompleteRequest     `json:"result,omitempty"`
	CreatedAt time.Time                                 `json:"created_at"`
}

// AppendBuild durably records build intent or a request before effects or send.
// The same ID is idempotent only for the exact previously retained document.
func (j *AttemptJournal) AppendBuild(entry JournalBuild) (JournalBuild, error) {
	if err := validateJournalBuild(entry); err != nil {
		return JournalBuild{}, err
	}
	entry = cloneJournalBuild(entry)
	j.mu.Lock()
	defer j.mu.Unlock()
	raw, err := json.Marshal(entry)
	if err != nil {
		return JournalBuild{}, err
	}
	for _, old := range j.state.Build {
		if old.ID == entry.ID {
			previous, err := json.Marshal(old)
			if err != nil {
				return JournalBuild{}, err
			}
			if !bytes.Equal(raw, previous) {
				return JournalBuild{}, ErrJournalConflict
			}
			return cloneJournalBuild(old), nil
		}
	}
	if j.entryCountLocked() >= journalMaxEntries {
		return JournalBuild{}, errors.New("runner journal entry limit reached")
	}
	version := j.state.Version
	if j.state.Version < 5 {
		j.state.Version = 5
	}
	j.state.Build = append(j.state.Build, entry)
	if err = validateJournalState(j.state); err == nil {
		err = j.persistLocked()
	}
	if err != nil {
		j.state.Build = j.state.Build[:len(j.state.Build)-1]
		j.state.Version = version
		return JournalBuild{}, err
	}
	return cloneJournalBuild(entry), nil
}

// AckBuild explicitly removes one caller-settled record. The caller must first
// verify the exact committed receipt, or establish settled intent and cleanup.
// Lease expiry alone is never settlement and never invokes this method.
func (j *AttemptJournal) AckBuild(id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	previous := j.state.Build
	kept := make([]JournalBuild, 0, len(previous))
	for _, entry := range previous {
		if entry.ID != id {
			kept = append(kept, entry)
		}
	}
	if len(kept) == len(previous) {
		return nil
	}
	j.state.Build = kept
	if err := j.persistLocked(); err != nil {
		j.state.Build = previous
		return err
	}
	return nil
}
func cloneJournalBuild(entry JournalBuild) JournalBuild {
	if entry.Source != nil {
		source := *entry.Source
		entry.Source = &source
	}
	if entry.Result != nil {
		result := *entry.Result
		if result.Completion.Artifact != nil {
			artifact := *result.Completion.Artifact
			result.Completion.Artifact = &artifact
		}
		entry.Result = &result
	}
	return entry
}
func cloneBuildEntries(entries []JournalBuild) []JournalBuild {
	copy := make([]JournalBuild, len(entries))
	for i, entry := range entries {
		copy[i] = cloneJournalBuild(entry)
	}
	return copy
}
func buildJournalID(value string) bool {
	return value != "" && len(value) <= 128 && strings.TrimSpace(value) == value && !strings.ContainsFunc(value, unicode.IsControl)
}
func buildJournalDigest(value string) bool {
	return len(value) == 71 && strings.HasPrefix(value, "sha256:") && proxyJournalDigest(value[7:])
}
func buildJournalAuthority(entry JournalBuild) domain.HostingBuildAuthority {
	return domain.HostingBuildAuthority{BuildID: entry.BuildID, RunID: entry.Attempt.RunID, LeaseID: entry.Attempt.LeaseID, Attempt: entry.Attempt.Attempt, Fence: entry.Attempt.Fence}
}
func validateJournalBuild(entry JournalBuild) error {
	invalid := errors.New("invalid nonsecret build journal document")
	if !buildJournalID(entry.ID) || entry.CreatedAt.IsZero() || validateAttemptIdentity(entry.Attempt) != nil {
		return invalid
	}
	authority := buildJournalAuthority(entry)
	if hostingbuildprotocol.ValidateAuthority(authority) != nil {
		return invalid
	}
	switch entry.Phase {
	case "attempt_intent":
		if entry.Source != nil || entry.Result != nil || !buildJournalDigest(entry.InputHash) {
			return invalid
		}
	case "source":
		if entry.InputHash != "" || entry.Source == nil || entry.Result != nil || entry.Source.Validate() != nil || entry.Source.Authority != authority || entry.Source.ReceiptID != entry.ID {
			return invalid
		}
		receipt := entry.Source.Receipt
		if !buildJournalID(receipt.SourceID) || !buildJournalDigest(receipt.InputID) || !buildJournalDigest(receipt.PolicyHash) || !buildJournalDigest(receipt.TreeDigest) || (len(receipt.Commit) != 40 && len(receipt.Commit) != 64) {
			return invalid
		}
		for _, c := range receipt.Commit {
			if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
				return invalid
			}
		}
	case "result":
		if entry.InputHash != "" || entry.Source != nil || entry.Result == nil || entry.Result.Validate() != nil || entry.Result.Authority != authority || entry.Result.ReceiptID != entry.ID {
			return invalid
		}
		if artifact := entry.Result.Completion.Artifact; artifact != nil {
			if artifact.Origin != "local_build" || !buildJournalID(artifact.SourceID) || !buildJournalDigest(artifact.InputID) || !buildJournalDigest(artifact.SourceReceiptHash) || artifact.DaemonID == "" || len(artifact.DaemonID) > 256 || strings.TrimSpace(artifact.DaemonID) != artifact.DaemonID || strings.ContainsFunc(artifact.DaemonID, unicode.IsControl) || (artifact.Platform != "linux/amd64" && artifact.Platform != "linux/arm64") || (artifact.ContextPolicy != "" && artifact.ContextPolicy != "static-assets-v1") {
				return invalid
			}
			for _, digest := range []string{artifact.ContextDigest, artifact.RequestDigest, artifact.ImageID, artifact.IndexDigest, artifact.ManifestDigest, artifact.ConfigDigest} {
				if !buildJournalDigest(digest) {
					return invalid
				}
			}
			if artifact.ImageID != artifact.IndexDigest && artifact.ImageID != artifact.ManifestDigest && artifact.ImageID != artifact.ConfigDigest {
				return invalid
			}
		}
	default:
		return invalid
	}
	raw, err := json.Marshal(entry)
	if err != nil || len(raw) > journalMaxProvenanceBytes {
		return invalid
	}
	return nil
}

// Validate only the additive build surface during writes by older entry APIs.
// Old-only journals retain their existing behavior; mixed journals cannot gain
// a cross-family duplicate ID or contradict retained source/artifact bindings.
func validateJournalBuildEntries(state journalState) error {
	if len(state.Build) == 0 {
		return nil
	}
	if state.Version < 5 {
		return errors.New("build journal requires version 5")
	}
	ids := make(map[string]struct{}, len(state.Build)+len(state.Events)+len(state.Completions)+len(state.Provenance)+len(state.Hosting)+len(state.Proxy))
	addID := func(id string) error {
		if _, exists := ids[id]; exists {
			return ErrJournalConflict
		}
		ids[id] = struct{}{}
		return nil
	}
	for _, entry := range state.Events {
		if err := addID(entry.ID); err != nil {
			return err
		}
	}
	for _, entry := range state.Completions {
		if err := addID(entry.ID); err != nil {
			return err
		}
	}
	for _, entry := range state.Provenance {
		if err := addID(entry.ID); err != nil {
			return err
		}
	}
	for _, entry := range state.Hosting {
		if err := addID(entry.ID); err != nil {
			return err
		}
	}
	for _, entry := range state.Proxy {
		if err := addID(entry.ID); err != nil {
			return err
		}
	}
	type evidence struct {
		input     string
		source    *domain.HostingBuildSourceReceipt
		artifacts []*domain.HostingBuildArtifact
	}
	attempts := make(map[domain.HostingBuildAuthority]*evidence)
	for _, entry := range state.Build {
		if err := validateJournalBuild(entry); err != nil {
			return err
		}
		if _, exists := ids[entry.ID]; exists {
			return ErrJournalConflict
		}
		ids[entry.ID] = struct{}{}
		authority := buildJournalAuthority(entry)
		group := attempts[authority]
		if group == nil {
			group = &evidence{}
			attempts[authority] = group
		}
		if entry.Phase == "attempt_intent" {
			if group.input != "" && group.input != entry.InputHash {
				return ErrJournalConflict
			}
			group.input = entry.InputHash
		}
		if entry.Source != nil {
			receipt := entry.Source.Receipt
			if group.source != nil && hostingbuildprotocol.SourceReceiptHash(*group.source) != hostingbuildprotocol.SourceReceiptHash(receipt) {
				return ErrJournalConflict
			}
			group.source = &receipt
		}
		if entry.Result != nil && entry.Result.Completion.Artifact != nil {
			group.artifacts = append(group.artifacts, entry.Result.Completion.Artifact)
		}
	}
	for _, group := range attempts {
		for _, artifact := range group.artifacts {
			if group.source != nil && (artifact.SourceID != group.source.SourceID || artifact.InputID != group.source.InputID || artifact.SourceReceiptHash != hostingbuildprotocol.SourceReceiptHash(*group.source)) {
				return ErrJournalConflict
			}
		}
	}
	return nil
}

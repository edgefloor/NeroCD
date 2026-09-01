package store

import "errors"

// ProvenanceConflictClass is a fixed, non-sensitive reason for a provenance
// conflict. It is suitable for operator diagnostics, not API responses.
type ProvenanceConflictClass string

const (
	provenanceConflictCommitMismatch      ProvenanceConflictClass = "commit_mismatch"
	provenanceConflictComposeHashMismatch ProvenanceConflictClass = "compose_hash_mismatch"
	provenanceConflictImageMismatch       ProvenanceConflictClass = "image_mismatch"
	provenanceConflictReplayKey           ProvenanceConflictClass = "replay_key"
	provenanceConflictUnique              ProvenanceConflictClass = "unique"
)

type provenanceConflictError struct{ class ProvenanceConflictClass }

func (e *provenanceConflictError) Error() string { return ErrConflict.Error() }
func (e *provenanceConflictError) Unwrap() error { return ErrConflict }

func provenanceConflict(class ProvenanceConflictClass) error {
	return &provenanceConflictError{class: class}
}

func provenanceContentConflict(existingCommit, existingHash, commit, hash string, imagesEqual bool) error {
	switch {
	case existingCommit != commit:
		return provenanceConflict(provenanceConflictCommitMismatch)
	case existingHash != hash:
		return provenanceConflict(provenanceConflictComposeHashMismatch)
	case !imagesEqual:
		return provenanceConflict(provenanceConflictImageMismatch)
	default:
		return ErrConflict
	}
}

// ProvenanceConflictReason returns a fixed diagnostic reason only for typed
// provenance conflicts. Generic conflicts intentionally remain unclassified.
func ProvenanceConflictReason(err error) string {
	var conflict *provenanceConflictError
	if !errors.As(err, &conflict) {
		return ""
	}
	switch conflict.class {
	case provenanceConflictCommitMismatch, provenanceConflictComposeHashMismatch, provenanceConflictImageMismatch, provenanceConflictReplayKey, provenanceConflictUnique:
		return string(conflict.class)
	default:
		return ""
	}
}

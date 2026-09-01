package store

import (
	"errors"
	"fmt"
	"testing"
)

func TestProvenanceConflictReasonsRemainConflictCompatible(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{name: "commit", err: provenanceContentConflict("commit-a", "hash", "commit-b", "hash", true), want: "commit_mismatch"},
		{name: "compose hash", err: provenanceContentConflict("commit", "hash-a", "commit", "hash-b", true), want: "compose_hash_mismatch"},
		{name: "images", err: provenanceContentConflict("commit", "hash", "commit", "hash", false), want: "image_mismatch"},
		{name: "priority", err: provenanceContentConflict("commit-a", "hash-a", "commit-b", "hash-b", false), want: "commit_mismatch"},
		{name: "replay", err: provenanceConflict(provenanceConflictReplayKey), want: "replay_key"},
		{name: "unique", err: provenanceConflict(provenanceConflictUnique), want: "unique"},
		{name: "generic", err: ErrConflict, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("store operation: %w", tc.err)
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("errors.Is(%v, ErrConflict) = false, want true", err)
			}
			if got := ProvenanceConflictReason(err); got != tc.want {
				t.Errorf("ProvenanceConflictReason(%v) = %q, want %q", tc.name, got, tc.want)
			}
		})
	}
}

package runner

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestAttemptJournalPersistsAndRecoversAtomically(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secure journal is supported on Unix")
	}
	root := filepath.Join(t.TempDir(), "journal")
	j, err := OpenAttemptJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	authority := testJournalAuthority()
	event := JournalEvent{ID: "event_one", Attempt: authority, Sequence: 4, Stream: "stdout", Message: "hello", CreatedAt: time.Now().UTC()}
	completion := JournalCompletion{ID: "completion_one", Attempt: authority, Status: "succeeded", CreatedAt: time.Now().UTC()}
	if _, err := j.AppendEvent(event); err != nil {
		t.Fatal(err)
	}
	if _, err := j.AppendCompletion(completion); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}

	// A crash may leave an unrenamed temporary file. Recovery reads only the
	// atomically renamed state and cannot confuse the partial file with it.
	if err := os.WriteFile(filepath.Join(root, ".journal.json.interrupted"), []byte(`{"partial":`), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenAttemptJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	snapshot := reopened.Snapshot()
	if len(snapshot.Events) != 1 || snapshot.Events[0] != event || len(snapshot.Completions) != 1 || snapshot.Completions[0] != completion {
		t.Fatalf("recovered snapshot = %#v", snapshot)
	}
	if err := reopened.AckEvents([]string{event.ID}); err != nil {
		t.Fatal(err)
	}
	if err := reopened.AckCompletion(completion.ID); err != nil {
		t.Fatal(err)
	}
	if reopened.Depth() != 0 {
		t.Fatalf("journal depth = %d, want 0", reopened.Depth())
	}
}

func FuzzAttemptJournalRecovery(f *testing.F) {
	if runtime.GOOS == "windows" {
		f.Skip("secure journal is supported on Unix")
	}
	validContents, expected := fuzzAttemptJournalValidState(f)
	invalidTimestamp := bytes.Replace(validContents, []byte(`"expires_at":"2026-08-16T08:01:00Z"`), []byte(`"expires_at":"2026-08-16T08:00:00Z"`), 1)
	if bytes.Equal(invalidTimestamp, validContents) {
		f.Fatal("valid journal state did not contain expected expiry timestamp")
	}
	duplicateID := bytes.Replace(validContents, []byte(`"id":"completion_recovery_one"`), []byte(`"id":"event_recovery_one"`), 1)
	if bytes.Equal(duplicateID, validContents) {
		f.Fatal("valid journal state did not contain expected completion id")
	}
	invalidSeeds := [][]byte{
		[]byte(`{"version":`),
		[]byte(`{"version":4}`),
		invalidTimestamp,
		duplicateID,
	}
	for _, seed := range append([][]byte{validContents}, invalidSeeds...) {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, contents []byte) {
		if len(contents) > 64<<10 {
			t.Skip("journal input exceeds fuzz size limit")
		}
		root := filepath.Join(t.TempDir(), "journal")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		statePath := filepath.Join(root, journalStateFilename)
		if err := os.WriteFile(statePath, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		knownInvalid := false
		for _, seed := range invalidSeeds {
			knownInvalid = knownInvalid || bytes.Equal(contents, seed)
		}

		journal, err := OpenAttemptJournal(root)
		if err != nil {
			if err := os.WriteFile(statePath, validContents, 0o600); err != nil {
				t.Fatal(err)
			}
			recovered, err := OpenAttemptJournal(root)
			if err != nil {
				t.Fatalf("open valid journal after recovery error: %v", err)
			}
			t.Cleanup(func() { _ = recovered.Close() })
			if got := recovered.Snapshot(); !reflect.DeepEqual(got, expected) {
				t.Fatalf("recovered valid journal snapshot = %#v, want %#v", got, expected)
			}
			if err := recovered.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenAttemptJournal(root)
			if err != nil {
				t.Fatalf("open valid journal after close: %v", err)
			}
			t.Cleanup(func() { _ = reopened.Close() })
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
			return
		}
		t.Cleanup(func() { _ = journal.Close() })
		if knownInvalid {
			_ = journal.Close()
			t.Fatal("OpenAttemptJournal accepted a known-invalid persisted state")
		}

		snapshot := journal.Snapshot()
		if bytes.Equal(contents, validContents) && !reflect.DeepEqual(snapshot, expected) {
			_ = journal.Close()
			t.Fatalf("valid journal snapshot = %#v, want %#v", snapshot, expected)
		}
		if err := journal.Close(); err != nil {
			t.Fatal(err)
		}

		recovered, err := OpenAttemptJournal(root)
		if err != nil {
			t.Fatalf("reopen recovered journal: %v", err)
		}
		t.Cleanup(func() { _ = recovered.Close() })
		if got := recovered.Snapshot(); !reflect.DeepEqual(got, snapshot) {
			_ = recovered.Close()
			t.Fatalf("reopened snapshot = %#v, want %#v", got, snapshot)
		}
		remaining := snapshot
		if len(snapshot.Events) > 0 {
			if err := recovered.AckEvents([]string{snapshot.Events[0].ID}); err != nil {
				t.Fatal(err)
			}
			remaining.Events = append([]JournalEvent(nil), snapshot.Events[1:]...)
		}
		if len(snapshot.Completions) > 0 {
			if err := recovered.AckCompletion(snapshot.Completions[0].ID); err != nil {
				t.Fatal(err)
			}
			remaining.Completions = append([]JournalCompletion(nil), snapshot.Completions[1:]...)
		}
		if len(snapshot.Provenance) > 0 {
			if err := recovered.AckProvenance(snapshot.Provenance[0].ID); err != nil {
				t.Fatal(err)
			}
			remaining.Provenance = append([]JournalProvenance(nil), snapshot.Provenance[1:]...)
		}
		if err := recovered.Close(); err != nil {
			t.Fatal(err)
		}

		final, err := OpenAttemptJournal(root)
		if err != nil {
			t.Fatalf("open after recovered acknowledgements: %v", err)
		}
		t.Cleanup(func() { _ = final.Close() })
		if got := final.Snapshot(); !reflect.DeepEqual(got, remaining) {
			_ = final.Close()
			t.Fatalf("snapshot after recovered acknowledgements = %#v, want %#v", got, remaining)
		}
		if err := final.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func fuzzAttemptJournalValidState(f *testing.F) ([]byte, JournalSnapshot) {
	f.Helper()
	root := filepath.Join(f.TempDir(), "journal")
	journal, err := OpenAttemptJournal(root)
	if err != nil {
		f.Fatal(err)
	}
	f.Cleanup(func() { _ = journal.Close() })
	authority := testJournalAuthority()
	for _, event := range []JournalEvent{
		{ID: "event_recovery_one", Attempt: authority, Sequence: 1, Stream: "stdout", Message: "first", CreatedAt: authority.CreatedAt.Add(time.Second)},
		{ID: "event_recovery_two", Attempt: authority, Sequence: 2, Stream: "stderr", Message: "second", CreatedAt: authority.CreatedAt.Add(2 * time.Second)},
	} {
		if _, err := journal.AppendEvent(event); err != nil {
			f.Fatal(err)
		}
	}
	for _, completion := range []JournalCompletion{
		{ID: "completion_recovery_one", Attempt: authority, Status: "failed", CreatedAt: authority.CreatedAt.Add(3 * time.Second)},
		{ID: "completion_recovery_two", Attempt: authority, Status: "succeeded", CreatedAt: authority.CreatedAt.Add(4 * time.Second)},
	} {
		if _, err := journal.AppendCompletion(completion); err != nil {
			f.Fatal(err)
		}
	}
	commit := strings.Repeat("a", 40)
	composeHash := "sha256:" + strings.Repeat("b", 64)
	for _, provenance := range []JournalProvenance{
		{ID: "provenance_recovery_one", Attempt: authority, DeploymentID: "deployment_one", GitCommit: commit, ComposeHash: composeHash, ImageDigests: []string{"sha256:" + strings.Repeat("c", 64)}, ContentIdentity: commit + ":" + composeHash, CreatedAt: authority.CreatedAt.Add(5 * time.Second)},
		{ID: "provenance_recovery_two", Attempt: authority, DeploymentID: "deployment_two", GitCommit: commit, ComposeHash: composeHash, ImageDigests: []string{"sha256:" + strings.Repeat("d", 64)}, ContentIdentity: commit + ":" + composeHash, CreatedAt: authority.CreatedAt.Add(6 * time.Second)},
	} {
		if _, err := journal.AppendProvenance(provenance); err != nil {
			f.Fatal(err)
		}
	}
	expected := journal.Snapshot()
	if err := journal.Close(); err != nil {
		f.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(root, journalStateFilename))
	if err != nil {
		f.Fatal(err)
	}
	return contents, expected
}

func TestAttemptJournalRejectsInsecurePaths(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secure journal is supported on Unix")
	}
	parent := t.TempDir()
	t.Run("directory_permissions", func(t *testing.T) {
		root := filepath.Join(parent, "wide")
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenAttemptJournal(root); err == nil {
			t.Fatal("OpenAttemptJournal accepted 0755 directory")
		}
	})
	t.Run("directory_symlink", func(t *testing.T) {
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(parent, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenAttemptJournal(link); err == nil {
			t.Fatal("OpenAttemptJournal followed directory symlink")
		}
	})
	t.Run("state_symlink", func(t *testing.T) {
		root := filepath.Join(parent, "state-link")
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(parent, "external")
		if err := os.WriteFile(target, []byte(`{"version":1,"events":[],"completions":[]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(root, journalStateFilename)); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenAttemptJournal(root); err == nil {
			t.Fatal("OpenAttemptJournal followed state symlink")
		}
	})
}

func TestAttemptJournalDuplicateConflictAndLimits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secure journal is supported on Unix")
	}
	j, err := OpenAttemptJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	event := JournalEvent{ID: "stable", Attempt: testJournalAuthority(), Sequence: 4, Stream: "stdout", Message: "same", CreatedAt: time.Now().UTC()}
	if _, err := j.AppendEvent(event); err != nil {
		t.Fatal(err)
	}
	if _, err := j.AppendEvent(event); err != nil {
		t.Fatalf("exact duplicate: %v", err)
	}
	conflict := event
	conflict.Message = "different"
	if _, err := j.AppendEvent(conflict); !errors.Is(err, ErrJournalConflict) {
		t.Fatalf("conflicting duplicate error = %v", err)
	}
	tooLarge := event
	tooLarge.ID = "too_large"
	tooLarge.Message = strings.Repeat("x", journalMaxBytes)
	if _, err := j.AppendEvent(tooLarge); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("oversize error = %v", err)
	}
	if j.Depth() != 1 {
		t.Fatalf("failed append changed depth to %d", j.Depth())
	}
}

func TestAttemptJournalSerializedStateContainsNoBearerCredential(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secure journal is supported on Unix")
	}
	root := filepath.Join(t.TempDir(), "journal")
	j, err := OpenAttemptJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	event := JournalEvent{ID: "event", Attempt: testJournalAuthority(), Sequence: 4, Stream: "system", Message: "safe", CreatedAt: time.Now().UTC()}
	if _, err := j.AppendEvent(event); err != nil {
		t.Fatal(err)
	}
	pending := JournalProvenance{ID: "provenance_stable", Attempt: testJournalAuthority(), DeploymentID: "deployment_1", GitCommit: strings.Repeat("a", 40), ComposeHash: "sha256:" + strings.Repeat("b", 64), ImageDigests: []string{"sha256:" + strings.Repeat("c", 64)}, ContentIdentity: strings.Repeat("a", 40) + ":sha256:" + strings.Repeat("b", 64), CreatedAt: time.Now().UTC()}
	if _, err := j.AppendProvenance(pending); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(root, journalStateFilename))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"ncr_example_bearer", `"token"`, `"credential"`, `"workspace"`, `"path"`, "private-key"} {
		if strings.Contains(string(contents), forbidden) {
			t.Fatalf("journal contains forbidden bearer material %q", forbidden)
		}
	}
}

func TestAttemptJournalProvenanceIsStableAndAcknowledgedAtomically(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secure journal is supported on Unix")
	}
	j, err := OpenAttemptJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	commit := strings.Repeat("a", 40)
	pending := JournalProvenance{ID: "provenance_stable", Attempt: testJournalAuthority(), DeploymentID: "deployment_1", GitCommit: commit, ComposeHash: "sha256:" + strings.Repeat("b", 64), ImageDigests: []string{"sha256:" + strings.Repeat("c", 64)}, ContentIdentity: commit + ":sha256:" + strings.Repeat("b", 64), CreatedAt: time.Now().UTC()}
	if _, err := j.AppendProvenance(pending); err != nil {
		t.Fatal(err)
	}
	if _, err := j.AppendProvenance(pending); err != nil {
		t.Fatalf("same request was not idempotent: %v", err)
	}
	changed := pending
	changed.GitCommit = strings.Repeat("d", 40)
	changed.ContentIdentity = changed.GitCommit + ":" + changed.ComposeHash
	if _, err := j.AppendProvenance(changed); !errors.Is(err, ErrJournalConflict) {
		t.Fatalf("changed durable request error = %v", err)
	}
	if got := j.Snapshot().Provenance; len(got) != 1 || !journalProvenanceEqual(got[0], pending) {
		t.Fatalf("stored provenance = %#v", got)
	}
	if err := j.AckProvenance(pending.ID); err != nil {
		t.Fatal(err)
	}
	if len(j.Snapshot().Provenance) != 0 {
		t.Fatal("provenance remained after valid acknowledgement")
	}
}

func TestAttemptJournalExclusiveLockRejectsSecondOpenerAndReleasesOnClose(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secure journal is supported on Unix")
	}
	root := filepath.Join(t.TempDir(), "journal")
	first, err := OpenAttemptJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAttemptJournal(root); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("second opener error = %v", err)
	}
	if _, err := first.AppendEvent(JournalEvent{ID: "held_event", Attempt: testJournalAuthority(), Sequence: 1, Stream: "system", Message: "durable", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := OpenAttemptJournal(root)
	if err != nil {
		t.Fatalf("open after close: %v", err)
	}
	defer func() { _ = next.Close() }()
	if len(next.Snapshot().Events) != 1 {
		t.Fatalf("state lost after lock handoff: %#v", next.Snapshot())
	}
}

func TestAttemptJournalRejectsUnsafeLockFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secure journal is supported on Unix")
	}
	for _, tc := range []struct {
		name string
		make func(root string) error
	}{
		{name: "symlink", make: func(root string) error { return os.Symlink("outside", filepath.Join(root, journalLockFilename)) }},
		{name: "hardlink", make: func(root string) error {
			target := filepath.Join(root, "other-lock")
			if err := os.WriteFile(target, []byte{}, 0o600); err != nil {
				return err
			}
			return os.Link(target, filepath.Join(root, journalLockFilename))
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "journal")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := tc.make(root); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenAttemptJournal(root); err == nil {
				t.Fatal("unsafe lock file was accepted")
			}
		})
	}
}

func TestAttemptJournalLockReleasedWhenHolderProcessExits(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secure journal is supported on Unix")
	}
	root := filepath.Join(t.TempDir(), "journal")
	seed, err := OpenAttemptJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seed.AppendEvent(JournalEvent{ID: "seed_event", Attempt: testJournalAuthority(), Sequence: 1, Stream: "system", Message: "survives", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := seed.Close(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestAttemptJournalLockHelper$")
	cmd.Env = append(os.Environ(), "NEROCD_JOURNAL_LOCK_HELPER=1", "NEROCD_JOURNAL_LOCK_ROOT="+root)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(ready) != "locked" {
		t.Fatalf("helper readiness=%q err=%v", ready, err)
	}
	if _, err := OpenAttemptJournal(root); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("opener while child holds lock error = %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenAttemptJournal(root)
	if err != nil {
		t.Fatalf("open after holder exit: %v", err)
	}
	defer func() { _ = opened.Close() }()
	if got := opened.Snapshot(); len(got.Events) != 1 || got.Events[0].ID != "seed_event" {
		t.Fatalf("state after subprocess holder = %#v", got)
	}
}

func TestAttemptJournalLockHelper(t *testing.T) {
	if os.Getenv("NEROCD_JOURNAL_LOCK_HELPER") != "1" {
		return
	}
	j, err := OpenAttemptJournal(os.Getenv("NEROCD_JOURNAL_LOCK_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	if _, err := os.Stdout.WriteString("locked\n"); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	_, _ = os.Stdin.Read(one[:])
}

func TestAttemptJournalFilteringRollbackPreservesInMemoryState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secure journal is supported on Unix")
	}
	tests := []struct {
		name   string
		mutate func(*AttemptJournal) error
	}{
		{name: "ack_events", mutate: func(j *AttemptJournal) error { return j.AckEvents([]string{"event_one"}) }},
		{name: "ack_completion", mutate: func(j *AttemptJournal) error { return j.AckCompletion("completion_one") }},
		{name: "discard_attempt", mutate: func(j *AttemptJournal) error { return j.DiscardAttempt(testJournalAuthority()) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j, err := OpenAttemptJournal(filepath.Join(t.TempDir(), "journal"))
			if err != nil {
				t.Fatal(err)
			}
			first := testJournalAuthority()
			second := first
			second.RunID = "run_other"
			second.LeaseID = "lease_other"
			second.Attempt = 2
			second.Fence = "opaque_fence_other"
			for _, event := range []JournalEvent{
				{ID: "event_one", Attempt: first, Sequence: 1, Stream: "stdout", Message: "first", CreatedAt: time.Now().UTC()},
				{ID: "event_two", Attempt: second, Sequence: 2, Stream: "stderr", Message: "second", CreatedAt: time.Now().UTC()},
			} {
				if _, err := j.AppendEvent(event); err != nil {
					t.Fatal(err)
				}
			}
			for _, completion := range []JournalCompletion{
				{ID: "completion_one", Attempt: first, Status: "failed", CreatedAt: time.Now().UTC()},
				{ID: "completion_two", Attempt: second, Status: "succeeded", CreatedAt: time.Now().UTC()},
			} {
				if _, err := j.AppendCompletion(completion); err != nil {
					t.Fatal(err)
				}
			}
			before := j.Snapshot()
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			if err := tt.mutate(j); err == nil {
				t.Fatal("filtering mutation unexpectedly persisted through a closed journal store")
			}
			if after := j.Snapshot(); !reflect.DeepEqual(after, before) {
				t.Fatalf("failed filtering mutation changed journal state\nbefore: %#v\nafter:  %#v", before, after)
			}
		})
	}
}

func testJournalAuthority() AttemptIdentity {
	created := time.Date(2026, 8, 16, 8, 0, 0, 0, time.UTC)
	return AttemptIdentity{RunID: "run_journal", LeaseID: "lease_journal", Attempt: 1, Fence: "opaque_fence", CreatedAt: created, ExpiresAt: created.Add(time.Minute)}
}

func TestAttemptJournalDiscardAttemptMatchesOnlyAuthorityTuple(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secure journal is supported on Unix")
	}
	root := filepath.Join(t.TempDir(), "journal")
	journal, err := OpenAttemptJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	journalClosed := false
	t.Cleanup(func() {
		if !journalClosed {
			_ = journal.Close()
		}
	})
	target := testJournalAuthority()
	identities := []AttemptIdentity{
		target,
		{RunID: "run_other", LeaseID: target.LeaseID, Attempt: target.Attempt, Fence: target.Fence, CreatedAt: target.CreatedAt.Add(time.Hour), ExpiresAt: target.ExpiresAt.Add(time.Hour)},
		{RunID: target.RunID, LeaseID: "lease_other", Attempt: target.Attempt, Fence: target.Fence, CreatedAt: target.CreatedAt.Add(2 * time.Hour), ExpiresAt: target.ExpiresAt.Add(2 * time.Hour)},
		{RunID: target.RunID, LeaseID: target.LeaseID, Attempt: 2, Fence: target.Fence, CreatedAt: target.CreatedAt.Add(3 * time.Hour), ExpiresAt: target.ExpiresAt.Add(3 * time.Hour)},
		{RunID: target.RunID, LeaseID: target.LeaseID, Attempt: target.Attempt, Fence: "opaque_fence_other", CreatedAt: target.CreatedAt.Add(4 * time.Hour), ExpiresAt: target.ExpiresAt.Add(4 * time.Hour)},
	}
	for index, authority := range identities {
		if _, err := journal.AppendEvent(JournalEvent{ID: fmt.Sprintf("event_%d", index), Attempt: authority, Sequence: index + 1, Stream: "stdout", Message: "pending", CreatedAt: authority.CreatedAt}); err != nil {
			t.Fatal(err)
		}
		if _, err := journal.AppendCompletion(JournalCompletion{ID: fmt.Sprintf("completion_%d", index), Attempt: authority, Status: "succeeded", CreatedAt: authority.CreatedAt}); err != nil {
			t.Fatal(err)
		}
		if _, err := journal.AppendProvenance(JournalProvenance{ID: fmt.Sprintf("provenance_%d", index), Attempt: authority, DeploymentID: fmt.Sprintf("deployment_%d", index), GitCommit: strings.Repeat("a", 40), ComposeHash: "sha256:" + strings.Repeat("b", 64), ImageDigests: []string{"sha256:" + strings.Repeat("c", 64)}, ContentIdentity: strings.Repeat("a", 40) + ":sha256:" + strings.Repeat("b", 64), CreatedAt: authority.CreatedAt}); err != nil {
			t.Fatal(err)
		}
	}

	metadataOnly := target
	metadataOnly.CreatedAt = target.CreatedAt.Add(24 * time.Hour)
	metadataOnly.ExpiresAt = target.ExpiresAt.Add(24 * time.Hour)
	if err := journal.DiscardAttempt(metadataOnly); err != nil {
		t.Fatal(err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	journalClosed = true
	reopened, err := OpenAttemptJournal(root)
	if err != nil {
		t.Fatal(err)
	}
	reopenedClosed := false
	t.Cleanup(func() {
		if !reopenedClosed {
			_ = reopened.Close()
		}
	})
	snapshot := reopened.Snapshot()
	if len(snapshot.Events) != 4 || len(snapshot.Completions) != 4 || len(snapshot.Provenance) != 4 {
		t.Fatalf("discarded snapshot = %#v", snapshot)
	}
	for index, authority := range identities[1:] {
		id := index + 1
		if snapshot.Events[index].ID != fmt.Sprintf("event_%d", id) || snapshot.Events[index].Attempt != authority {
			t.Fatalf("event %d = %#v, want authority %#v", id, snapshot.Events[index], authority)
		}
		if snapshot.Completions[index].ID != fmt.Sprintf("completion_%d", id) || snapshot.Completions[index].Attempt != authority {
			t.Fatalf("completion %d = %#v, want authority %#v", id, snapshot.Completions[index], authority)
		}
		if snapshot.Provenance[index].ID != fmt.Sprintf("provenance_%d", id) || snapshot.Provenance[index].Attempt != authority {
			t.Fatalf("provenance %d = %#v, want authority %#v", id, snapshot.Provenance[index], authority)
		}
	}
}

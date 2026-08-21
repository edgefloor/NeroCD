package runner

import (
	"bufio"
	"errors"
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
	defer reopened.Close()
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
	defer j.Close()
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
	defer j.Close()
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
	defer next.Close()
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
	defer opened.Close()
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
	defer j.Close()
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
		{name: "discard_attempt", mutate: func(j *AttemptJournal) error { return j.DiscardAttempt("lease_journal", 1) }},
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

package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func TestHostingJournalMigratesV1AndRetainsStaleApplyIntent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	journal, err := OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	authority := AttemptIdentity{RunID: "run_image", LeaseID: "lease_image", Attempt: 1, Fence: "fence_image", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	event := JournalEvent{ID: "event_legacy", Attempt: authority, Sequence: 1, Stream: "system", Message: "legacy output", CreatedAt: now}
	if _, err = journal.AppendEvent(event); err != nil {
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	release := domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "amd64", DaemonID: "daemon-journal"}
	release.Hash = domain.HostingDocumentHash(release)
	plan, err := domain.CompileHostingPlan(domain.HostingSnapshot{BindingID: "binding_image", PolicyVersion: domain.HostingPolicyVersion}, release, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry := JournalHosting{ID: "intent_image", Attempt: authority, OperationID: "hop_image", Phase: "apply_intent", Plan: &plan, CreatedAt: now}
	if _, err = journal.AppendHosting(entry); err != nil {
		t.Fatal(err)
	}
	plan.Release.DaemonID = "mutated-caller-value"
	if journal.Snapshot().Hosting[0].Plan.Release.DaemonID != "daemon-journal" {
		t.Fatal("caller mutated durable intent")
	}
	if err = journal.DiscardAttempt(authority); err == nil {
		t.Fatal("stale attempt discarded unacknowledged intent")
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	if len(journal.Snapshot().Hosting) != 1 || len(journal.Snapshot().Events) != 1 {
		t.Fatal("journal migration lost records")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	var header struct {
		Version int `json:"version"`
	}
	if err = json.Unmarshal(raw, &header); err != nil || header.Version != 2 {
		t.Fatalf("old binary would not fail closed: version%d err=%v", header.Version, err)
	}
	if err = journal.AckHosting(entry.ID); err != nil {
		t.Fatal(err)
	}
	if err = journal.DiscardAttempt(authority); err != nil {
		t.Fatal(err)
	}
}

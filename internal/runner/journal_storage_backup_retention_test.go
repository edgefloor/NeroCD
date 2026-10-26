package runner

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func storageRetentionJournalEntry() JournalStorageBackupRetention {
	now := time.Now().UTC()
	item := domain.BackupRetentionItem{CopyID: "copy", BackupID: "backup", Location: "local", Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 65537, CreatedAt: now, Availability: "available", Selected: true, ProtectedReasons: []string{}}
	operation := domain.HostingStorageBackupRetentionExecution{ID: "retention", Version: domain.HostingStorageBackupRetentionVersion, ProjectID: "project", ResourceID: "resource", PreviewID: "preview", PolicyRevision: 1, PlanHash: "sha256:" + strings.Repeat("b", 64), State: "queued", ReservationHeld: true, LocalState: "pending", RemoteState: "not_selected", Items: []domain.BackupRetentionExecutionItem{{CopyID: "copy", BackupID: "backup", Location: "local", State: "pending", ReservationHeld: true}}, CreatedAt: now, UpdatedAt: now}
	copy := domain.StorageBackupRetentionCopy{Storage: domain.HostingStorageRef{StorageID: "storage", Generation: 1, Hash: "sha256:" + strings.Repeat("e", 64)}, Item: item, RunnerID: "runner", DestinationID: "destination", ServerID: "server", DaemonID: "daemon", IdentityHash: "sha256:" + strings.Repeat("c", 64), Local: &domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("d", 64), Device: 1, Inode: 2, Bytes: item.Bytes, Created: true}}
	p := domain.HostingStorageBackupRetentionPlan{Operation: operation, ActorID: "owner", RunnerID: "runner", Copies: []domain.StorageBackupRetentionCopy{copy}, TimeoutSeconds: 60}
	p.Hash = domain.HostingDocumentHash(p)
	a := domain.HostingAuthority{OperationID: "retention", RunID: "storage_retention_retention", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	return JournalStorageBackupRetention{ID: "intent", Attempt: AttemptIdentity{RunID: a.RunID, LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, Plan: p, Request: domain.HostingStorageBackupRetentionMutation{HostingAuthority: a, ReceiptID: "intent", PlanHash: p.Hash, CopyID: "copy", Phase: "intent"}, CreatedAt: now}
}
func TestStorageBackupRetentionJournal20RestartAndExplicitRetirement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	j, err := OpenAttemptJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := storageRetentionJournalEntry()
	if _, err = j.AppendStorageBackupRetention(entry); err != nil {
		t.Fatal(err)
	}
	if j.state.Version != 22 {
		t.Fatal("retention not journal20")
	}
	if err = j.DiscardAttempt(entry.Attempt); err == nil {
		t.Fatal("unacknowledged retention discarded")
	}
	if _, err = j.AppendCompletion(JournalCompletion{ID: "generic", Attempt: entry.Attempt, Status: "succeeded", CreatedAt: time.Now()}); err == nil {
		t.Fatal("generic completion accepted")
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = OpenAttemptJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if got := j.Snapshot().StorageBackupRetentions; len(got) != 1 || !reflect.DeepEqual(got[0], entry) {
		t.Fatal("journal restart changed immutable batch")
	}
	bad := entry
	bad.Request.Fence = "foreign"
	if _, err = j.AppendStorageBackupRetention(bad); err == nil {
		t.Fatal("same ID changed tuple")
	}
	if err = j.AckStorageBackupRetention(entry.ID); err != nil || j.Depth() != 0 {
		t.Fatal("explicit receipt retirement", err)
	}
}
func TestStorageBackupRetentionRegistryOnlyIDAndProgressDoesNotAlterPlan(t *testing.T) {
	entry := storageRetentionJournalEntry()
	run := domain.TaskRun{ID: entry.Request.RunID, RunSpec: domain.RunSpec{Type: domain.RunTypeStorageBackupRetention, HostingOperationID: entry.Request.OperationID, HostingMode: "retention", Inputs: map[string]any{}}}
	p, err := NewRegistry().BuildPlan(run)
	if err != nil || !reflect.DeepEqual(p, domain.RunnerPrimitivePlan{RunID: run.ID}) {
		t.Fatal("retention primitive authority", err)
	}
	run.RunSpec.Process = &domain.ProcessSpec{}
	if _, err = NewRegistry().BuildPlan(run); err == nil {
		t.Fatal("retention acquired command")
	}
	plan := entry.Plan
	plan.Progress = append([]domain.BackupRetentionExecutionItem(nil), plan.Operation.Items...)
	if plan.Validate() != nil {
		t.Fatal("safe progress sidecar rejected")
	}
	plan.Progress[0].CopyID = "foreign"
	if plan.Validate() == nil {
		t.Fatal("foreign progress accepted")
	}
}

func TestStorageBackupDeletionJournalRestartPreservesManualOrigin(t *testing.T) {
	entry := storageRetentionJournalEntry()
	entry.Plan.Operation.Version = domain.HostingStorageBackupDeletionVersion
	entry.Plan.Operation.Origin = domain.StorageBackupManualSelection
	entry.Plan.Operation.BackupID = "backup"
	entry.Plan.Operation.ResourceGeneration = 3
	entry.Plan.Operation.PolicyRevision = 0
	entry.Plan.Hash = ""
	entry.Plan.Hash = domain.HostingDocumentHash(entry.Plan)
	entry.Request.PlanHash = entry.Plan.Hash
	path := filepath.Join(t.TempDir(), "journal")
	j, e := OpenAttemptJournal(path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = j.AppendStorageBackupRetention(entry); e != nil {
		t.Fatal(e)
	}
	if e = j.Close(); e != nil {
		t.Fatal(e)
	}
	j, e = OpenAttemptJournal(path)
	if e != nil {
		t.Fatal(e)
	}
	defer j.Close()
	got := j.Snapshot().StorageBackupRetentions
	if len(got) != 1 || !reflect.DeepEqual(got[0], entry) {
		t.Fatal("manual selection changed across restart")
	}
}

package runner

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func retentionJournalEntry() JournalDatabaseBackupRetention {
	now := time.Now().UTC()
	item := domain.BackupRetentionItem{CopyID: "copy", BackupID: "backup", Location: "local", Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 65537, CreatedAt: now, Availability: "available", Selected: true, ProtectedReasons: []string{}}
	operation := domain.HostingDatabaseBackupRetentionExecution{ID: "retention", Version: domain.HostingDatabaseBackupRetentionVersion, ProjectID: "project", ResourceID: "resource", PreviewID: "preview", PolicyRevision: 1, PlanHash: "sha256:" + strings.Repeat("b", 64), State: "queued", ReservationHeld: true, LocalState: "pending", RemoteState: "not_selected", Items: []domain.BackupRetentionExecutionItem{{CopyID: "copy", BackupID: "backup", Location: "local", State: "pending", ReservationHeld: true}}, CreatedAt: now, UpdatedAt: now}
	copy := domain.BackupRetentionCopy{Item: item, RunnerID: "runner", DestinationID: "destination", ServerID: "server", DaemonID: "daemon", IdentityHash: "sha256:" + strings.Repeat("c", 64), Local: &domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("d", 64), Device: 1, Inode: 2, Bytes: item.Bytes, Created: true}}
	p := domain.HostingDatabaseBackupRetentionPlan{Operation: operation, ActorID: "owner", RunnerID: "runner", Copies: []domain.BackupRetentionCopy{copy}, TimeoutSeconds: 60}
	p.Hash = domain.HostingDocumentHash(p)
	a := domain.HostingAuthority{OperationID: "retention", RunID: "db_retention_retention", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	return JournalDatabaseBackupRetention{ID: "intent", Attempt: AttemptIdentity{RunID: a.RunID, LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, Plan: p, Request: domain.HostingDatabaseBackupRetentionMutation{HostingAuthority: a, ReceiptID: "intent", PlanHash: p.Hash, CopyID: "copy", Phase: "intent"}, CreatedAt: now}
}
func TestBackupRetentionJournal20RestartAndExplicitRetirement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	j, err := OpenAttemptJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := retentionJournalEntry()
	if _, err = j.AppendDatabaseBackupRetention(entry); err != nil {
		t.Fatal(err)
	}
	if j.state.Version != 20 {
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
	defer func() {
		if err := j.Close(); err != nil {
			t.Error("close fixture:", err)
		}
	}()
	if got := j.Snapshot().DatabaseBackupRetentions; len(got) != 1 || !reflect.DeepEqual(got[0], entry) {
		t.Fatal("journal restart changed immutable batch")
	}
	bad := entry
	bad.Request.Fence = "foreign"
	if _, err = j.AppendDatabaseBackupRetention(bad); err == nil {
		t.Fatal("same ID changed tuple")
	}
	if err = j.AckDatabaseBackupRetention(entry.ID); err != nil || j.Depth() != 0 {
		t.Fatal("explicit receipt retirement", err)
	}
}
func TestBackupRetentionRegistryOnlyIDAndProgressDoesNotAlterPlan(t *testing.T) {
	entry := retentionJournalEntry()
	run := domain.TaskRun{ID: entry.Request.RunID, RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseBackupRetention, HostingOperationID: entry.Request.OperationID, HostingMode: "retention", Inputs: map[string]any{}}}
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

func TestMySQLBackupRetentionJournalExactProbeNamespace(t *testing.T) {
	entry := retentionJournalEntry()
	entry.Plan.Operation.Version = domain.HostingDatabaseMySQLBackupRetentionVersion
	entry.Plan.Hash = ""
	entry.Plan.Hash = domain.HostingDocumentHash(entry.Plan)
	entry.Request.PlanHash = entry.Plan.Hash
	entry.ProbeKey = domain.HostingDatabaseBackupRetentionProbeNamespace(entry.Plan.Operation.Version) + entry.Plan.Operation.ID + "/" + strings.Repeat("a", 32)
	entry.Request.ProbeKey = entry.ProbeKey
	entry.Request.ProbeOnly = true
	if validateJournalDatabaseBackupRetention(entry) != nil {
		t.Fatal("exact MySQL probe rejected")
	}
	entry.ProbeKey = strings.Replace(entry.ProbeKey, "mysql/", "postgresql/", 1)
	entry.Request.ProbeKey = entry.ProbeKey
	if validateJournalDatabaseBackupRetention(entry) == nil {
		t.Fatal("foreign probe namespace acquired journal authority")
	}
}

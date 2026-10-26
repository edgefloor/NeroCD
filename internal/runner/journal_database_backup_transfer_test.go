package runner

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func transferJournalEntry() JournalDatabaseBackupTransfer {
	now := time.Now().UTC()
	target := domain.HostingDatabaseBackupTarget{ID: "target", ProjectID: "project", Version: domain.HostingDatabaseBackupTargetVersion, Revision: 1, Name: "fixture", Endpoint: "https://backup.example", Bucket: "backup-fixture", Region: "us-east-1", Enabled: true, CreatedAt: now, UpdatedAt: now}
	p := domain.HostingDatabaseBackupTransferPlan{Operation: domain.HostingDatabaseBackupTransfer{ID: "transfer", Version: domain.HostingDatabaseBackupTransferVersion, ProjectID: "project", TargetID: "target", TargetRevision: 1, DestinationID: "destination", Mode: "upload", BackupID: "backup", State: "queued", Blob: &domain.HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 65537, Format: "postgresql_custom"}, CreatedAt: now, UpdatedAt: now}, Target: domain.HostingDatabaseBackupTargetSnapshot{Target: target, Access: domain.BackupRemoteCredentialRef{SecretID: "access", Version: 1}, Secret: domain.BackupRemoteCredentialRef{SecretID: "secret", Version: 1}}, ActorID: "actor", RunnerID: "runner", ServerID: "server", DaemonID: "daemon", LocationID: "backup", MaxBytes: 1 << 20, TimeoutSeconds: 60}
	p.ObjectKey = domain.BackupRemoteObjectKey("backup", *p.Operation.Blob)
	p.Hash = domain.HostingDocumentHash(p)
	a := domain.HostingAuthority{OperationID: "transfer", RunID: "db_transfer_transfer", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	return JournalDatabaseBackupTransfer{ID: "intent", Attempt: AttemptIdentity{RunID: a.RunID, LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, Plan: p, Request: domain.HostingDatabaseBackupTransferMutation{HostingAuthority: a, ReceiptID: "intent", PlanHash: p.Hash, Phase: "intent", DaemonID: "daemon"}, CreatedAt: now}
}
func TestBackupRemoteJournalVersion19RestartAndExplicitRetirement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	j, err := OpenAttemptJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := transferJournalEntry()
	if _, err = j.AppendDatabaseBackupTransfer(entry); err != nil {
		t.Fatal(err)
	}
	if _, err = j.AppendCompletion(JournalCompletion{ID: "generic", Attempt: entry.Attempt, Status: "succeeded", CreatedAt: time.Now().UTC()}); err == nil {
		t.Fatal("generic completion mixed with remote transfer")
	}
	if err = j.DiscardAttempt(entry.Attempt); err == nil {
		t.Fatal("unacknowledged intent discarded")
	}
	bad := entry
	bad.Request.DaemonID = "foreign"
	if _, err = j.AppendDatabaseBackupTransfer(bad); err == nil {
		t.Fatal("changed same-ID receipt accepted")
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
	if entries := j.Snapshot().DatabaseBackupTransfers; len(entries) != 1 || !reflect.DeepEqual(entries[0], entry) {
		t.Fatal("journal19 changed authority")
	}
	if err = j.AckDatabaseBackupTransfer(entry.ID); err != nil || j.Depth() != 0 {
		t.Fatal("acknowledged receipt not retired", err)
	}
}
func TestBackupRemoteRegistryCannotAcquireGenericPrimitive(t *testing.T) {
	for _, mode := range []string{"validate", "upload", "download"} {
		run := domain.TaskRun{ID: "db_transfer_transfer", RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseBackupTransfer, HostingOperationID: "transfer", HostingMode: mode, Inputs: map[string]any{}}}
		plan, err := NewRegistry().BuildPlan(run)
		if err != nil || !reflect.DeepEqual(plan, domain.RunnerPrimitivePlan{RunID: run.ID}) {
			t.Fatal("typed transfer acquired generic execution", err)
		}
		run.RunSpec.Process = &domain.ProcessSpec{}
		if _, err = NewRegistry().BuildPlan(run); err == nil {
			t.Fatal("process-bearing transfer accepted")
		}
	}
}

func TestBackupRetentionDownloadProofJournal20(t *testing.T) {
	entry := transferJournalEntry()
	entry.Plan.Operation.Mode = "download"
	entry.Plan.Operation.RestoreOperationID = "restore"
	entry.Plan.Hash = ""
	entry.Plan.Hash = domain.HostingDocumentHash(entry.Plan)
	entry.Request.PlanHash = entry.Plan.Hash
	entry.ID = "download_complete"
	entry.Request.ReceiptID = entry.ID
	entry.Request.Phase = "complete"
	entry.Request.LocalCopy = &domain.BackupLocalCopyProof{PoolID: "sha256:" + strings.Repeat("b", 64), Device: 1, Inode: 2, Bytes: 65537, Created: true}
	path := filepath.Join(t.TempDir(), "journal")
	j, e := OpenAttemptJournal(path)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = j.AppendDatabaseBackupTransfer(entry); e != nil {
		t.Fatal(e)
	}
	if j.state.Version != 20 {
		t.Fatal("download proof not journal20")
	}
	if e = j.Close(); e != nil {
		t.Fatal(e)
	}
	j, e = OpenAttemptJournal(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if err := j.Close(); err != nil {
			t.Error("close fixture:", err)
		}
	}()
	if got := j.Snapshot().DatabaseBackupTransfers; len(got) != 1 || !reflect.DeepEqual(got[0], entry) {
		t.Fatal("download proof lost at restart")
	}
	bad := entry
	bad.Plan.Operation.Mode = "upload"
	bad.Plan.Operation.RestoreOperationID = ""
	bad.Plan.Hash = ""
	bad.Plan.Hash = domain.HostingDocumentHash(bad.Plan)
	bad.Request.PlanHash = bad.Plan.Hash
	if _, e = j.AppendDatabaseBackupTransfer(bad); e == nil {
		t.Fatal("download proof accepted for upload")
	}
}

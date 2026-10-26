package runner

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
)

func storageTransferJournalEntry() JournalStorageBackupTransfer {
	now := time.Now().UTC()
	target := domain.HostingDatabaseBackupTarget{ID: "target", ProjectID: "project", Version: domain.HostingDatabaseBackupTargetVersion, Revision: 1, Name: "fixture", Endpoint: "https://backup.example", Bucket: "backup-fixture", Region: "us-east-1", Enabled: true, CreatedAt: now, UpdatedAt: now}
	p := domain.HostingStorageBackupTransferPlan{Operation: domain.HostingStorageBackupTransfer{ID: "transfer", Version: domain.HostingStorageBackupTransferVersion, ProjectID: "project", TargetID: "target", TargetRevision: 1, DestinationID: "destination", Mode: "upload", BackupID: "backup", State: "queued", Blob: &domain.HostingStorageArchiveBlob{BackupID: "backup", DestinationID: "destination", DaemonID: "daemon", SHA256: "sha256:" + strings.Repeat("a", 64), SizeBytes: 65537, Format: domain.HostingStorageArchiveFormat}, CreatedAt: now, UpdatedAt: now}, Target: domain.HostingDatabaseBackupTargetSnapshot{Target: target, Access: domain.BackupRemoteCredentialRef{SecretID: "access", Version: 1}, Secret: domain.BackupRemoteCredentialRef{SecretID: "secret", Version: 1}}, ActorID: "actor", RunnerID: "runner", ServerID: "server", DaemonID: "daemon", LocationID: "backup", MaxBytes: 1 << 20, TimeoutSeconds: 60, Limits: domain.HostingStorageArchiveLimits{ArchiveBytes: 1 << 20, DataBytes: 1 << 19, FileBytes: 1 << 18, Members: 100, Depth: 16, PathBytes: 255}}
	p.ObjectKey = domain.StorageRemoteObjectKey("backup", *p.Operation.Blob)
	p.Hash = domain.HostingDocumentHash(p)
	a := domain.HostingAuthority{OperationID: "transfer", RunID: "storage_transfer_transfer", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	return JournalStorageBackupTransfer{ID: "intent", Attempt: AttemptIdentity{RunID: a.RunID, LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, Plan: p, Request: domain.HostingStorageBackupTransferMutation{HostingAuthority: a, ReceiptID: "intent", PlanHash: p.Hash, Phase: "intent", DaemonID: "daemon"}, CreatedAt: now}
}
func TestStorageRemoteJournalVersion21RestartAndExplicitRetirement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal")
	j, err := OpenAttemptJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := storageTransferJournalEntry()
	if _, err = j.AppendStorageBackupTransfer(entry); err != nil {
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
	if _, err = j.AppendStorageBackupTransfer(bad); err == nil {
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
	if entries := j.Snapshot().StorageBackupTransfers; len(entries) != 1 || !reflect.DeepEqual(entries[0], entry) {
		t.Fatal("journal21 changed authority")
	}
	if err = j.AckStorageBackupTransfer(entry.ID); err != nil || j.Depth() != 0 {
		t.Fatal("acknowledged receipt not retired", err)
	}
}

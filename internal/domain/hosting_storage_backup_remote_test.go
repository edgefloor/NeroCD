package domain

import (
	"strings"
	"testing"
	"time"
)

func TestStorageRemoteCredentialPurposeAndFenceFamily(t *testing.T) {
	a := HostingAuthority{OperationID: "transfer", RunID: "storage_transfer_transfer", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	r := BackupRemoteCredentialRequest{Authority: a, DeliveryID: "delivery", PlanHash: "sha256:" + strings.Repeat("a", 64), TargetID: "target", TargetRevision: 1, DaemonID: "daemon"}
	if r.Validate() != nil {
		t.Fatal("storage credential request rejected")
	}
	m := BackupRemoteCredentialMetadata{Purpose: HostingStorageBackupRemoteCredentialPurpose, Request: r, RunnerID: "runner", UseUntil: time.Now().Add(time.Minute)}
	m.RequestHash = HostingDocumentHash(struct {
		Runner  string
		Request BackupRemoteCredentialRequest
	}{m.RunnerID, r})
	if m.Validate() != nil {
		t.Fatal("storage purpose rejected")
	}
	m.Purpose = HostingDatabaseBackupRemoteCredentialPurpose
	if m.Validate() == nil {
		t.Fatal("PostgreSQL credential purpose authorized storage")
	}
	storage := HostingStorageBackupTransferMutation{HostingAuthority: a, ReceiptID: "receipt", PlanHash: r.PlanHash, Phase: "intent", DaemonID: "daemon"}
	if storage.Validate() != nil {
		t.Fatal("storage authority rejected")
	}
	storage.RunID = "db_transfer_transfer"
	if storage.Validate() == nil {
		t.Fatal("PostgreSQL run authorized storage mutation")
	}
}

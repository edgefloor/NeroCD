package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func remoteTransferPlanFixture() HostingDatabaseBackupTransferPlan {
	now := time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)
	target := HostingDatabaseBackupTarget{ID: "target", ProjectID: "project", Version: HostingDatabaseBackupTargetVersion, Revision: 1, Name: "Fixture", Endpoint: "https://backup.example", Bucket: "backup-fixture", Region: "us-east-1", Enabled: true, CreatedAt: now, UpdatedAt: now}
	p := HostingDatabaseBackupTransferPlan{Operation: HostingDatabaseBackupTransfer{ID: "transfer", Version: HostingDatabaseBackupTransferVersion, ProjectID: "project", TargetID: "target", TargetRevision: 1, DestinationID: "destination", Mode: "upload", BackupID: "backup", State: "queued", Blob: &HostingDatabaseBackupBlob{Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 100, Format: "postgresql_custom"}, CreatedAt: now, UpdatedAt: now}, Target: HostingDatabaseBackupTargetSnapshot{Target: target, Access: BackupRemoteCredentialRef{SecretID: "access", Version: 1}, Secret: BackupRemoteCredentialRef{SecretID: "secret", Version: 1}}, ActorID: "actor", RunnerID: "runner", ServerID: "server", DaemonID: "daemon", LocationID: "backup", MaxBytes: 1024, TimeoutSeconds: 60}
	p.ObjectKey = BackupRemoteObjectKey(p.Operation.BackupID, *p.Operation.Blob)
	p.Hash = HostingDocumentHash(p)
	return p
}
func TestBackupRemoteTargetAndTransferClosedIdentity(t *testing.T) {
	p := remoteTransferPlanFixture()
	if p.Validate() != nil {
		t.Fatal("valid remote plan rejected")
	}
	for name, edit := range map[string]func(*HostingDatabaseBackupTransferPlan){
		"foreignproject": func(p *HostingDatabaseBackupTransferPlan) { p.Target.Target.ProjectID = "foreign" },
		"revision":       func(p *HostingDatabaseBackupTransferPlan) { p.Target.Target.Revision++ },
		"key":            func(p *HostingDatabaseBackupTransferPlan) { p.ObjectKey = "arbitrary/object" },
		"location":       func(p *HostingDatabaseBackupTransferPlan) { p.LocationID = "foreign" },
		"limit":          func(p *HostingDatabaseBackupTransferPlan) { p.MaxBytes = 50 },
		"missingblob":    func(p *HostingDatabaseBackupTransferPlan) { p.Operation.Blob = nil },
		"unknownstate":   func(p *HostingDatabaseBackupTransferPlan) { p.Operation.State = "complete" },
		"unknownfailure": func(p *HostingDatabaseBackupTransferPlan) { p.Operation.FailureCode = "arbitrary canary" },
		"wrongmode":      func(p *HostingDatabaseBackupTransferPlan) { p.Operation.Mode = "restore" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := p
			edit(&bad)
			bad.Hash = ""
			bad.Hash = HostingDocumentHash(bad)
			if bad.Validate() == nil {
				t.Fatal("changed identity accepted with recomputed hash")
			}
		})
	}
	for _, endpoint := range []string{"https://user:secret@backup.example", "https://backup.example/private", "https://backup.example?token=value", "https://backup.example/#fragment", "file:///tmp/archive", "https://backup.example./", " https://backup.example"} {
		if ValidateBackupRemoteEndpoint(endpoint) == nil {
			t.Fatal("invalid endpoint accepted")
		}
	}
	for _, endpoint := range []string{"https://backup.example", "https://backup.example/", "http://127.0.0.1:19000"} {
		if ValidateBackupRemoteEndpoint(endpoint) != nil {
			t.Fatal("syntactic endpoint rejected")
		}
	}
}

func TestBackupRemoteCredentialPurposeCodecAndRedaction(t *testing.T) {
	request := BackupRemoteCredentialRequest{Authority: HostingAuthority{OperationID: "transfer", RunID: "db_transfer_transfer", LeaseID: "lease", Attempt: 1, Fence: "opaque-fence"}, DeliveryID: "delivery", PlanHash: "sha256:" + strings.Repeat("a", 64), TargetID: "target", TargetRevision: 1, DaemonID: "daemon"}
	m := BackupRemoteCredentialMetadata{Purpose: HostingDatabaseBackupRemoteCredentialPurpose, Request: request, RunnerID: "runner", UseUntil: time.Now().UTC().Add(time.Minute)}
	m.RequestHash = HostingDocumentHash(struct {
		Runner  string
		Request BackupRemoteCredentialRequest
	}{m.RunnerID, request})
	access, secret := []byte("access-canary"), []byte("secret-canary")
	c, err := NewBackupRemoteCredentials(m, access, secret)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	clear(access)
	clear(secret)
	ordinary, _ := json.Marshal(c)
	if string(ordinary) != "{}" || strings.Contains(fmt.Sprintf("%+v", c), "canary") {
		t.Fatal("ordinary encoding revealed credential")
	}
	var wire bytes.Buffer
	if WriteBackupRemoteCredentials(&wire, c) != nil {
		t.Fatal("protected encoding failed")
	}
	decoded, err := ReadBackupRemoteCredentials(bytes.NewReader(wire.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Metadata() != m {
		t.Fatal("purpose authority changed")
	}
	if decoded.Use(func(a, b []byte) error {
		if string(a) != "access-canary" || string(b) != "secret-canary" {
			t.Fatal("credential ownership aliased input")
		}
		return nil
	}) != nil {
		t.Fatal("borrow failed")
	}
	decoded.Close()
	if decoded.Use(func([]byte, []byte) error { return nil }) == nil {
		t.Fatal("closed credential reused")
	}
	bad := bytes.Replace(wire.Bytes(), []byte(HostingDatabaseBackupRemoteCredentialPurpose), []byte("database-role"), 1)
	if _, err = ReadBackupRemoteCredentials(bytes.NewReader(bad)); err == nil {
		t.Fatal("foreign credential purpose accepted")
	}
	bad = bytes.Replace(wire.Bytes(), []byte(`"target_revision":1`), []byte(`"target_revision":1,"target_revision":2`), 1)
	if _, err = ReadBackupRemoteCredentials(bytes.NewReader(bad)); err == nil {
		t.Fatal("duplicate metadata accepted")
	}
}

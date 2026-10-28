package domain

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestMySQLRemoteTransferKeepsEngineAndArtifactIdentity(t *testing.T) {
	p := remoteTransferPlanFixture()
	pgKey := p.ObjectKey
	p.Operation.Version = HostingDatabaseMySQLBackupTransferVersion
	p.Operation.Blob.Format = "mysql_sql"
	p.ObjectKey = BackupRemoteObjectKey(p.Operation.BackupID, *p.Operation.Blob)
	p.Hash = ""
	p.Hash = HostingDocumentHash(p)
	if p.Validate() != nil || p.ObjectKey != "nerocd/mysql/backup/"+strings.Repeat("a", 64)+".sql" || pgKey != "nerocd/postgresql/backup/"+strings.Repeat("a", 64)+".dump" {
		t.Fatal("engine object namespaces changed")
	}
	if HostingDatabaseBackupTransferVersionFor(HostingDatabaseMySQLBackupVersion) != p.Operation.Version || HostingDatabaseBackupRemoteCapabilityFor(p.Operation.Version, p.Operation.Mode) != HostingDatabaseMySQLBackupRemoteCapability || HostingDatabaseBackupRemoteCredentialPurposeFor(p.Operation.Version, p.Operation.Mode) != HostingDatabaseMySQLBackupRemoteCredentialPurpose {
		t.Fatal("MySQL upload adapter identity")
	}
	if HostingDatabaseBackupTransferVersionFor(HostingDatabaseBackupComponentVersion) != HostingDatabaseBackupTransferVersion {
		t.Fatal("existing Compose PostgreSQL transfer identity changed")
	}
	if HostingDatabaseBackupTransferVersionFor("mysql.compose.logical-local.v1") != "" {
		t.Fatal("unsupported Compose MySQL gained remote uploads")
	}
	for _, change := range []func(*HostingDatabaseBackupTransferPlan){
		func(v *HostingDatabaseBackupTransferPlan) { v.Operation.Version = HostingDatabaseBackupTransferVersion },
		func(v *HostingDatabaseBackupTransferPlan) { v.Operation.Blob.Format = "postgresql_custom" },
		func(v *HostingDatabaseBackupTransferPlan) { v.ObjectKey = pgKey },
		func(v *HostingDatabaseBackupTransferPlan) {
			v.Operation.Mode = "validate"
			v.Operation.BackupID = ""
			v.Operation.Blob = nil
		},
	} {
		bad := p
		blob := *p.Operation.Blob
		bad.Operation.Blob = &blob
		change(&bad)
		bad.Hash = ""
		bad.Hash = HostingDocumentHash(bad)
		if bad.Validate() == nil {
			t.Fatal("cross-engine or shared validation identity accepted")
		}
	}
}

func TestMySQLRemoteDownloadBindsModeAuthority(t *testing.T) {
	p := remoteTransferPlanFixture()
	p.Operation.Version = HostingDatabaseMySQLBackupTransferVersion
	p.Operation.Mode, p.Operation.RestoreOperationID = "download", "restore"
	p.Operation.Blob.Format = "mysql_sql"
	p.ObjectKey = BackupRemoteObjectKey(p.Operation.BackupID, *p.Operation.Blob)
	p.Hash = ""
	p.Hash = HostingDocumentHash(p)
	if p.Validate() != nil || HostingDatabaseBackupRemoteCapabilityFor(p.Operation.Version, p.Operation.Mode) != HostingDatabaseMySQLRemoteRestoreCapability || HostingDatabaseBackupRemoteCredentialPurposeFor(p.Operation.Version, p.Operation.Mode) != HostingDatabaseMySQLRemoteRestoreCredentialPurpose {
		t.Fatal("MySQL download lost its exact mode authority")
	}
	p.Operation.RestoreOperationID = ""
	if p.Operation.Validate() == nil {
		t.Fatal("download without exact restore accepted")
	}
	for _, version := range []string{HostingDatabaseBackupVersion, "mysql.compose.logical-local.v1", "unknown"} {
		if HostingDatabaseBackupRemoteCapabilityFor(version, "download") != "" || HostingDatabaseBackupRemoteCredentialPurposeFor(version, "download") != "" {
			t.Fatal("unknown transfer selected an adapter")
		}
	}
}

func TestMySQLRemoteRestoreCredentialPurposeCodec(t *testing.T) {
	request := BackupRemoteCredentialRequest{Authority: HostingAuthority{OperationID: "download", RunID: "db_transfer_download", LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: "delivery", PlanHash: "sha256:" + strings.Repeat("a", 64), TargetID: "target", TargetRevision: 1, DaemonID: "daemon"}
	m := BackupRemoteCredentialMetadata{Purpose: HostingDatabaseMySQLRemoteRestoreCredentialPurpose, Request: request, RunnerID: "runner", UseUntil: time.Now().UTC().Add(time.Minute)}
	m.RequestHash = HostingDocumentHash(struct {
		Runner  string
		Request BackupRemoteCredentialRequest
	}{m.RunnerID, m.Request})
	credentials, err := NewBackupRemoteCredentials(m, []byte("access-canary"), []byte("secret-canary"))
	if err != nil {
		t.Fatal(err)
	}
	defer credentials.Close()
	var wire bytes.Buffer
	if err = WriteBackupRemoteCredentials(&wire, credentials); err != nil {
		t.Fatal(err)
	}
	decoded, err := ReadBackupRemoteCredentials(&wire)
	if err != nil {
		t.Fatal(err)
	}
	defer decoded.Close()
	if decoded.Metadata() != m {
		t.Fatal("download purpose or request changed in protected transport")
	}
	for _, family := range []string{"storage_transfer_download", "db_retention_download", "storage_retention_download"} {
		wrong := m
		wrong.Request.Authority.RunID = family
		if strings.Contains(family, "retention") {
			wrong.Request.RetentionCopyID = "copy"
		}
		wrong.RequestHash = HostingDocumentHash(struct {
			Runner  string
			Request BackupRemoteCredentialRequest
		}{wrong.RunnerID, wrong.Request})
		if wrong.Validate() == nil {
			t.Fatal("download credentials crossed protocol families")
		}
	}
}

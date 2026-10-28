package domain

import (
	"strings"
	"testing"
	"time"
)

func TestMySQLBackupRetentionClosedEngineContracts(t *testing.T) {
	for _, tc := range []struct{ version, format, capability, purpose, prefix string }{
		{HostingDatabaseBackupRetentionVersion, "postgresql_custom", HostingDatabaseBackupRetentionCapability, HostingDatabaseBackupRetentionCredentialPurpose, "nerocd/postgresql/retention-probes/"},
		{HostingDatabaseMySQLBackupRetentionVersion, "mysql_sql", HostingDatabaseMySQLBackupRetentionCapability, HostingDatabaseMySQLBackupRetentionCredentialPurpose, "nerocd/mysql/retention-probes/"},
		{"mysql.component.backup-retention.v1", "", "", "", ""},
		{"unknown", "", "", "", ""},
	} {
		if HostingDatabaseBackupRetentionFormat(tc.version) != tc.format || HostingDatabaseBackupRetentionCapabilityFor(tc.version) != tc.capability || HostingDatabaseBackupRetentionPurposeFor(tc.version) != tc.purpose || HostingDatabaseBackupRetentionProbeNamespace(tc.version) != tc.prefix {
			t.Fatal("engine contract mismatch", tc.version)
		}
	}
	if HostingDatabaseBackupRetentionVersionForSchedule(HostingDatabaseMySQLBackupScheduleVersion) != HostingDatabaseMySQLBackupRetentionVersion || HostingDatabaseBackupRetentionVersionForBackup(HostingDatabaseMySQLBackupVersion) != HostingDatabaseMySQLBackupRetentionVersion || HostingDatabaseBackupRetentionVersionForBackup(HostingDatabaseBackupComponentVersion) != HostingDatabaseBackupRetentionVersion || HostingDatabaseBackupRetentionVersionForSchedule("mysql.component.schedule.v1") != "" {
		t.Fatal("unsupported source identity")
	}
}

func TestMySQLBackupRetentionRemotePlanEngineBinding(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	target := HostingDatabaseBackupTarget{ID: "target", ProjectID: "project", Version: HostingDatabaseBackupTargetVersion, Revision: 1, Name: "Fixture", Endpoint: "https://backup.example", Bucket: "backup-fixture", Region: "us-east-1", Enabled: true, CreatedAt: now, UpdatedAt: now}
	copy := BackupRetentionCopy{Item: BackupRetentionItem{CopyID: "copy", BackupID: "backup", Location: "remote", Digest: "sha256:" + strings.Repeat("a", 64), Bytes: 100}, RunnerID: "runner", IdentityHash: "sha256:" + strings.Repeat("b", 64), Remote: &BackupRemoteCopyProof{ETag: `"opaque"`, Created: true}, Target: &HostingDatabaseBackupTargetSnapshot{Target: target, Access: BackupRemoteCredentialRef{SecretID: "access", Version: 1}, Secret: BackupRemoteCredentialRef{SecretID: "secret", Version: 1}}}
	copy.ObjectKey = BackupRemoteObjectKey("backup", HostingDatabaseBackupBlob{Digest: copy.Item.Digest, Bytes: 100, Format: "mysql_sql"})
	plan := HostingDatabaseBackupRetentionPlan{Operation: HostingDatabaseBackupRetentionExecution{ID: "retention", Version: HostingDatabaseMySQLBackupRetentionVersion, Items: []BackupRetentionExecutionItem{{CopyID: "copy"}}}, ActorID: "actor", RunnerID: "runner", Copies: []BackupRetentionCopy{copy}, TimeoutSeconds: 60}
	for _, tc := range []struct {
		name, version, key string
		valid              bool
	}{
		{"native", HostingDatabaseMySQLBackupRetentionVersion, copy.ObjectKey, true},
		{"postgres_namespace", HostingDatabaseMySQLBackupRetentionVersion, strings.Replace(copy.ObjectKey, "mysql/", "postgresql/", 1), false},
		{"foreign_engine", HostingDatabaseBackupRetentionVersion, copy.ObjectKey, false},
		{"unsupported", "mysql.component.retention.v1", copy.ObjectKey, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := plan
			p.Copies = append([]BackupRetentionCopy(nil), plan.Copies...)
			p.Operation.Version = tc.version
			p.Copies[0].ObjectKey = tc.key
			p.Hash = ""
			p.Hash = HostingDocumentHash(p)
			if (p.Validate() == nil) != tc.valid {
				t.Fatal("invalid engine/key accepted or native rejected")
			}
		})
	}
}

func TestMySQLBackupRetentionCredentialFamilySeparation(t *testing.T) {
	request := BackupRemoteCredentialRequest{Authority: HostingAuthority{OperationID: "retention", RunID: "db_retention_retention", LeaseID: "lease", Attempt: 1, Fence: "fence"}, DeliveryID: "delivery", PlanHash: "sha256:" + strings.Repeat("a", 64), TargetID: "target", TargetRevision: 1, DaemonID: "daemon", RetentionCopyID: "copy"}
	metadata := BackupRemoteCredentialMetadata{Purpose: HostingDatabaseMySQLBackupRetentionCredentialPurpose, Request: request, RunnerID: "runner", UseUntil: time.Date(2026, 10, 28, 12, 1, 0, 0, time.UTC)}
	hash := func() {
		metadata.RequestHash = HostingDocumentHash(struct {
			Runner  string
			Request BackupRemoteCredentialRequest
		}{metadata.RunnerID, metadata.Request})
	}
	hash()
	if metadata.Validate() != nil {
		t.Fatal("native retention metadata rejected")
	}
	for _, run := range []string{"db_transfer_retention", "storage_retention_retention"} {
		metadata.Request.Authority.RunID = run
		hash()
		if metadata.Validate() == nil {
			t.Fatal("retention purpose crossed operation family")
		}
	}
}

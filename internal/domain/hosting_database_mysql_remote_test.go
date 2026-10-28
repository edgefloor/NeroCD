package domain

import (
	"strings"
	"testing"
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
	if HostingDatabaseBackupTransferVersionFor(HostingDatabaseMySQLBackupVersion) != p.Operation.Version || HostingDatabaseBackupRemoteCapabilityFor(p.Operation.Version) != HostingDatabaseMySQLBackupRemoteCapability || HostingDatabaseBackupRemoteCredentialPurposeFor(p.Operation.Version) != HostingDatabaseMySQLBackupRemoteCredentialPurpose {
		t.Fatal("MySQL upload adapter identity")
	}
	if HostingDatabaseBackupTransferVersionFor(HostingDatabaseBackupComponentVersion) != "" {
		t.Fatal("Compose database gained remote uploads")
	}
	for _, change := range []func(*HostingDatabaseBackupTransferPlan){
		func(v *HostingDatabaseBackupTransferPlan) { v.Operation.Version = HostingDatabaseBackupTransferVersion },
		func(v *HostingDatabaseBackupTransferPlan) { v.Operation.Blob.Format = "postgresql_custom" },
		func(v *HostingDatabaseBackupTransferPlan) { v.ObjectKey = pgKey },
		func(v *HostingDatabaseBackupTransferPlan) {
			v.Operation.Mode = "download"
			v.Operation.RestoreOperationID = "restore"
		},
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
			t.Fatal("cross-engine, download or shared validation identity accepted")
		}
	}
}

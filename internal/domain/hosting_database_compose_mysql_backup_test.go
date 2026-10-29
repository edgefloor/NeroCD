package domain

import "testing"

func TestMySQLComponentBackupRemoteUploadKeepsRetentionClosed(t *testing.T) {
	version := HostingDatabaseMySQLBackupComponentVersion
	if HostingDatabaseBackupFormatFor(version) != "mysql_sql" || HostingDatabaseBackupCapabilityFor(version) != HostingDatabaseMySQLBackupCapability || HostingDatabaseBackupTransferVersionFor(version) != HostingDatabaseMySQLBackupTransferVersion || HostingDatabaseBackupRetentionVersionForBackup(version) != "" {
		t.Fatal("MySQL component widened another adapter")
	}
	operation := HostingDatabaseBackupOperation{Version: version, SourceComponent: "mysql", SourceDeployedOperationID: "hop_source", Mode: "backup"}
	if operation.validateComponentIdentity() != nil {
		t.Fatal("closed component identity rejected")
	}
	operation.RemoteTarget = &BackupTargetSelection{TargetID: "target", Revision: 1}
	if operation.validateComponentIdentity() != nil {
		t.Fatal("MySQL component remote target rejected")
	}
}

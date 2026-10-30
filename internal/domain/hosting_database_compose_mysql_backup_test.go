package domain

import "testing"

func TestMySQLComponentBackupUsesMySQLEngineAdapters(t *testing.T) {
	version := HostingDatabaseMySQLBackupComponentVersion
	if HostingDatabaseBackupFormatFor(version) != "mysql_sql" || HostingDatabaseBackupCapabilityFor(version) != HostingDatabaseMySQLBackupCapability || HostingDatabaseBackupTransferVersionFor(version) != HostingDatabaseMySQLBackupTransferVersion || HostingDatabaseBackupRetentionVersionForBackup(version) != HostingDatabaseMySQLBackupRetentionVersion {
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

func TestMySQLComponentRetentionSourceScopeIsClosed(t *testing.T) {
	valid := HostingDatabaseBackupRetentionSourceScope{BackupVersion: HostingDatabaseMySQLBackupComponentVersion, Component: "mysql"}
	if valid.Validate() != nil || HostingDatabaseBackupRetentionVersionForSchedule(HostingDatabaseMySQLBackupComponentScheduleVersion) != HostingDatabaseMySQLBackupRetentionVersion {
		t.Fatal("qualified component retention scope rejected")
	}
	for _, invalid := range []HostingDatabaseBackupRetentionSourceScope{{BackupVersion: HostingDatabaseBackupComponentVersion, Component: "mysql"}, {BackupVersion: valid.BackupVersion, Component: "postgresql"}, {}} {
		if invalid.Validate() == nil {
			t.Fatal("foreign component retention scope accepted")
		}
	}
}

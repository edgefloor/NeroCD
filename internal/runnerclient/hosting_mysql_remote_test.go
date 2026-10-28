package runnerclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"nerocd/internal/domain"
)

func TestMySQLRemoteIdentityRequiresExplicitAdapter(t *testing.T) {
	for _, scenario := range []string{"mysql", "postgresql", "remote_flag_absent", "logical_flag_absent", "database_flag_absent", "missing_task", "shell"} {
		t.Run(scenario, func(t *testing.T) {
			caps := []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.RunTypeDatabaseBackup, domain.HostingDatabaseMySQLCapability, domain.HostingDatabaseMySQLBackupCapability, domain.RunTypeDatabaseBackupTransfer, domain.HostingDatabaseBackupRemoteCapability, domain.HostingDatabaseMySQLBackupRemoteCapability}
			if scenario == "postgresql" {
				caps = []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability, domain.RunTypeDatabaseBackupTransfer, domain.HostingDatabaseBackupRemoteCapability}
			}
			if scenario == "missing_task" {
				caps = append(caps[:6], caps[7:]...)
			}
			if scenario == "shell" {
				caps = append(caps, "shell")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/runners/identity" {
					t.Error("identity validation reached an effect")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "mysql_remote", "capabilities": caps})
			}))
			defer server.Close()
			options := RuntimeExecutionOptions{Database: scenario != "database_flag_absent", LogicalDatabaseBackups: scenario != "logical_flag_absent", RemoteDatabaseBackups: scenario != "remote_flag_absent", DatabaseTrustedLoopbackHTTP: true}
			_, err := New(server.URL, "fixture").RuntimeExecutionIdentityWithOptions(t.Context(), options)
			if (err == nil) != (scenario == "mysql" || scenario == "postgresql") {
				t.Fatal("remote adapter startup profile", err)
			}
		})
	}
}

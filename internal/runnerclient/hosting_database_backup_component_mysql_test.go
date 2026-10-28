package runnerclient

import (
	"encoding/json"
	"nerocd/internal/domain"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestMySQLComponentBackupRuntimeIdentity(t *testing.T) {
	base := []string{domain.RunTypeImageDeploy, domain.HostingReleasePortsCapability, domain.HostingRuntimeEnvironmentCapability, domain.HostingStackCapability, domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.RunTypeDatabaseBackup, domain.HostingDatabaseMySQLBackupCapability, domain.HostingDatabaseMySQLBackupComponentCapability}
	for _, mode := range []string{"enabled", "stack", "mysql_backup", "runtime_flag", "database_flag", "shell"} {
		t.Run(mode, func(t *testing.T) {
			caps := slices.Clone(base)
			options := RuntimeExecutionOptions{Database: true, LogicalDatabaseBackups: true, RuntimeEnvironment: true, DatabaseTrustedLoopbackHTTP: true}
			switch mode {
			case "stack":
				caps = slices.DeleteFunc(caps, func(s string) bool { return s == domain.HostingStackCapability })
			case "mysql_backup":
				caps = slices.DeleteFunc(caps, func(s string) bool { return s == domain.HostingDatabaseMySQLBackupCapability })
			case "runtime_flag":
				options.RuntimeEnvironment = false
			case "database_flag":
				options.Database = false
			case "shell":
				caps = append(caps, "shell")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if e := json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": caps}); e != nil {
					t.Error(e)
				}
			}))
			defer server.Close()
			if _, err := New(server.URL, "synthetic").RuntimeExecutionIdentityWithOptions(t.Context(), options); (err == nil) != (mode == "enabled") {
				t.Fatal("MySQL component profile", err)
			}
		})
	}
}

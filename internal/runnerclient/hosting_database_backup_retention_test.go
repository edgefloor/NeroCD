package runnerclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"nerocd/internal/domain"
)

func TestBackupRetentionIdentityRequiresCompleteExplicitProfile(t *testing.T) {
	base := []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.RunTypeDatabaseBackup, domain.HostingDatabaseBackupCapability, domain.RunTypeDatabaseBackupTransfer, domain.HostingDatabaseBackupRemoteCapability}
	for _, mode := range []string{"enabled", "disabled", "missing_task", "missing_capability", "flag_absent", "dependency_absent", "shell"} {
		t.Run(mode, func(t *testing.T) {
			caps := append([]string(nil), base...)
			flag := true
			if mode != "disabled" && mode != "missing_task" {
				caps = append(caps, domain.RunTypeDatabaseBackupRetention)
			}
			if mode != "disabled" && mode != "missing_capability" {
				caps = append(caps, domain.HostingDatabaseBackupRetentionCapability)
			}
			if mode == "disabled" || mode == "flag_absent" {
				flag = false
			}
			if mode == "shell" {
				caps = append(caps, "shell")
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": caps}); err != nil {
					t.Error("encode identity response:", err)
				}
			}))
			defer server.Close()
			options := RuntimeExecutionOptions{Database: true, LogicalDatabaseBackups: true, RemoteDatabaseBackups: true, DatabaseBackupRetention: flag, DatabaseTrustedLoopbackHTTP: true}
			if mode == "dependency_absent" {
				options.RemoteDatabaseBackups = false
			}
			_, err := New(server.URL, "synthetic").RuntimeExecutionIdentityWithOptions(t.Context(), options)
			valid := mode == "enabled" || mode == "disabled"
			if (err == nil) != valid {
				t.Fatalf("identity profile%s err=%v", mode, err)
			}
		})
	}
}

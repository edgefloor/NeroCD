package runnerclient

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestMySQLBackupRetentionIdentityRequiresCompleteExplicitProfile(t *testing.T) {
	base := []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.HostingDatabaseMySQLCapability, domain.RunTypeDatabaseBackup, domain.HostingDatabaseMySQLBackupCapability, domain.RunTypeDatabaseBackupTransfer, domain.HostingDatabaseBackupRemoteCapability, domain.HostingDatabaseMySQLBackupRetentionCapability, domain.RunTypeDatabaseBackupRetention}
	for _, mode := range []string{"enabled", "flag", "remote_flag", "missing_native", "missing_backup", "missing_task", "shell"} {
		t.Run(mode, func(t *testing.T) {
			caps := append([]string(nil), base...)
			omit := ""
			switch mode {
			case "missing_native":
				omit = domain.HostingDatabaseMySQLCapability
			case "missing_backup":
				omit = domain.HostingDatabaseMySQLBackupCapability
			case "missing_task":
				omit = domain.RunTypeDatabaseBackupRetention
			case "shell":
				caps = append(caps, "shell")
			}
			filtered := []string{}
			for _, cap := range caps {
				if cap != omit {
					filtered = append(filtered, cap)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": filtered}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			options := RuntimeExecutionOptions{Database: true, LogicalDatabaseBackups: true, RemoteDatabaseBackups: mode != "remote_flag", DatabaseBackupRetention: mode != "flag", DatabaseTrustedLoopbackHTTP: true}
			_, err := New(server.URL, "synthetic").RuntimeExecutionIdentityWithOptions(t.Context(), options)
			if (err == nil) != (mode == "enabled") {
				t.Fatal("MySQL profile closure", err)
			}
		})
	}
}

func TestMySQLBackupRetentionReceiptResponseIdentity(t *testing.T) {
	input := domain.HostingDatabaseBackupRetentionMutation{HostingAuthority: domain.HostingAuthority{OperationID: "execution", RunID: "db_retention_execution", LeaseID: "lease", Attempt: 1, Fence: "fence"}, ReceiptID: "receipt", PlanHash: "sha256:" + strings.Repeat("a", 64), CopyID: "copy", Phase: "deleted"}
	for _, mode := range []string{"mysql", "postgresql", "unknown", "foreign_operation", "foreign_copy", "foreign_phase"} {
		t.Run(mode, func(t *testing.T) {
			out := domain.HostingDatabaseBackupRetentionExecution{ID: "execution", Version: domain.HostingDatabaseMySQLBackupRetentionVersion, Items: []domain.BackupRetentionExecutionItem{{CopyID: "copy", State: "deleted"}}}
			switch mode {
			case "postgresql":
				out.Version = domain.HostingDatabaseBackupRetentionVersion
			case "unknown":
				out.Version = "mysql.component.retention.v1"
			case "foreign_operation":
				out.ID = "other"
			case "foreign_copy":
				out.Items[0].CopyID = "other"
			case "foreign_phase":
				out.Items[0].State = "intent"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewEncoder(w).Encode(out); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			ctx := WithDatabaseTransport(t.Context(), DatabaseTransport{TrustedLoopbackHTTP: true})
			_, err := New(server.URL, "synthetic").HostingDatabaseBackupRetentionMutation(ctx, input)
			if (err == nil) != (mode == "mysql" || mode == "postgresql") {
				t.Fatal("receipt identity accepted mismatch", err)
			}
		})
	}
}

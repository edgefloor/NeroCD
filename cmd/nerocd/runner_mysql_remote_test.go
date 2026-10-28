//go:build linux && arm64

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"nerocd/internal/domain"
)

func TestRunnerMySQLRemoteCommandRegistrationAndEnrollment(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root-owned persistent database directories")
	}
	for _, mode := range []string{"register", "enroll"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("NEROCD_MODE", "development")
			t.Setenv("NEROCD_TOKEN", "")
			t.Setenv("NEROCD_RUNNER_CREDENTIAL_FILE", "")
			t.Setenv("NEROCD_RUNNER_JOURNAL_DIR", "")
			t.Setenv("NEROCD_RUNNER_SECRET_ROOT", "")
			root, err := os.MkdirTemp("/var/lib", "nerocd-mysql-remote-cli-")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.RemoveAll(root); err != nil {
					t.Error(err)
				}
			})
			for _, name := range []string{"state", "secrets"} {
				if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			credential, enrollment := filepath.Join(root, "credential"), filepath.Join(root, "enrollment")
			caps := []string{domain.RunTypeDatabaseDeploy, domain.HostingDatabaseCapability, domain.HostingOwnedStorageCapability, domain.HostingDatabaseMySQLCapability, domain.RunTypeDatabaseBackup, domain.HostingDatabaseMySQLBackupCapability, domain.RunTypeDatabaseBackupTransfer, domain.HostingDatabaseBackupRemoteCapability, domain.HostingDatabaseMySQLBackupRemoteCapability}
			var mu sync.Mutex
			registered, consumed, claimed := false, false, false
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.URL.Path {
				case "/api/v1/runners/register":
					var in runnerRegisterRequest
					if json.NewDecoder(r.Body).Decode(&in) != nil || !slices.Contains(in.Capabilities, domain.HostingDatabaseMySQLBackupRemoteCapability) || !slices.Contains(in.Capabilities, domain.HostingDatabaseBackupRemoteCapability) || slices.Contains(in.Capabilities, "shell") {
						t.Error("CLI did not advertise both configured transport adapters")
					}
					registered = true
					_ = json.NewEncoder(w).Encode(map[string]any{"token": "runner-fixture"})
				case "/api/v1/runner-enrollments/consume":
					consumed = true
					_ = json.NewEncoder(w).Encode(map[string]any{"runner": map[string]any{"id": "mysql_remote"}})
				case "/api/v1/runners/identity":
					// No PostgreSQL local-backup engine capability is needed.
					_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "mysql_remote", "capabilities": caps})
				case "/api/v1/runners/heartbeat":
					_ = json.NewEncoder(w).Encode(domain.Runner{ID: "mysql_remote", Status: domain.RunnerActive, Capabilities: caps})
				case "/api/v1/runners/hosting/observation-targets":
					w.WriteHeader(http.StatusNotFound)
				case "/api/v1/runners/claim":
					claimed = true
					w.WriteHeader(http.StatusUnauthorized)
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer server.Close()
			previous := http.DefaultTransport
			transport := server.Client().Transport.(*http.Transport).Clone()
			http.DefaultTransport = transport
			defer func() { http.DefaultTransport = previous; transport.CloseIdleConnections() }()
			args := []string{"--server", server.URL, "--id", "mysql_remote", "--execute", "--once", "--hosting-database", "--database-state-root", filepath.Join(root, "state"), "--database-secret-root", filepath.Join(root, "secrets"), "--hosting-database-backups", "--hosting-database-remote-backups", "--work-dir", filepath.Join(root, "work"), "--journal-dir", filepath.Join(root, "journal")}
			if mode == "register" {
				args = append(args, "--token", "registration-fixture")
			} else {
				if err := os.WriteFile(enrollment, []byte("nce_"+strings.Repeat("a", 64)), 0600); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--enrollment-file", enrollment, "--credential-file", credential)
			}
			err = runRunner(args)
			mu.Lock()
			defer mu.Unlock()
			if err == nil || !claimed || mode == "register" && !registered || mode == "enroll" && !consumed {
				t.Fatal("native MySQL command did not reach intentional claim rejection", err)
			}
			if !strings.Contains(err.Error(), "401") {
				t.Fatal("unexpected command termination", err)
			}
		})
	}
}

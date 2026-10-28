//go:build unix

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/domain"
)

func TestRunnerEnrollmentKeepsFileAcrossConsumeRequest(t *testing.T) {
	for _, replacement := range []string{"directory", "symlink"} {
		t.Run(replacement, func(t *testing.T) {
			t.Setenv("NEROCD_MODE", "development")
			t.Setenv("NEROCD_TOKEN", "")
			t.Setenv("NEROCD_RUNNER_CREDENTIAL_FILE", "")
			t.Setenv("NEROCD_RUNNER_JOURNAL_DIR", "")
			root := privateBackupTestDir(t)
			parent := filepath.Join(root, "parent")
			if err := os.Mkdir(parent, 0700); err != nil {
				t.Fatal(err)
			}
			enrollment := filepath.Join(parent, "enrollment")
			token := []byte("original-consumed-enrollment")
			if err := os.WriteFile(enrollment, token, 0600); err != nil {
				t.Fatal(err)
			}
			original, err := os.Open(enrollment)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = original.Close() })
			moved := parent + "-original"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/runner-enrollments/consume":
					if r.Header.Get("Authorization") != "Bearer "+string(token) {
						t.Error("consume did not use original enrollment")
					}
					// Replace the path while the actual command awaits the consume reply.
					// The runner must retain the admitted file through this exchange.
					replace := func() error {
						if err := os.Rename(parent, moved); err != nil {
							return err
						}
						target := parent
						if replacement == "symlink" {
							target += "-replacement"
						}
						if err := os.Mkdir(target, 0700); err != nil {
							return err
						}
						if replacement == "symlink" {
							if err := os.Symlink(target, parent); err != nil {
								return err
							}
						}
						return os.WriteFile(enrollment, []byte("unrelated private file"), 0600)
					}
					if err := replace(); err != nil {
						t.Error(err)
						http.Error(w, "fixture replacement failed", http.StatusBadRequest)
						return
					}
					_ = json.NewEncoder(w).Encode(app.ConsumedRunnerEnrollment{Runner: domain.Runner{ID: "runner_enrollment"}})
				case "/api/v1/runners/identity":
					_ = json.NewEncoder(w).Encode(app.RunnerIdentity{RunnerID: "runner_enrollment"})
				case "/api/v1/runners/heartbeat":
					_ = json.NewEncoder(w).Encode(domain.Runner{ID: "runner_enrollment", Status: domain.RunnerActive})
				case "/api/v1/runners/telemetry":
					w.WriteHeader(http.StatusNoContent)
				case "/api/v1/runners/claim":
					w.WriteHeader(http.StatusNotFound)
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(server.Close)
			err = runRunner([]string{"--server", server.URL, "--enrollment-file", enrollment, "--credential-file", filepath.Join(root, "credential"), "--work-dir", t.TempDir(), "--once"})
			if err != nil {
				t.Fatal(err)
			}
			wiped, err := io.ReadAll(original)
			if err != nil || !bytes.Equal(wiped, make([]byte, len(token))) {
				t.Fatalf("consumed file was not wiped: %q, %v", wiped, err)
			}
			if _, err := os.Lstat(filepath.Join(moved, "enrollment")); !os.IsNotExist(err) {
				t.Fatalf("consumed entry remains: %v", err)
			}
			untouched, err := os.ReadFile(enrollment)
			if err != nil || string(untouched) != "unrelated private file" {
				t.Fatalf("replacement was changed: %q, %v", untouched, err)
			}
		})
	}
}

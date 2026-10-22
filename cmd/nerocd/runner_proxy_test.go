package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func TestRunnerProxyConfigurationFailsBeforeRegistrationOrCredentialUse(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	root := filepath.Join(t.TempDir(), "must-not-create")
	for _, args := range [][]string{
		{"--capabilities", "proxy_apply"},
		{"--proxy-state-root", root},
		{"--proxy-state-root", root, "--proxy-protocol", "old.v0"},
		{"--proxy-state-root", root, "--proxy-protocol", "managed-traefik.file.v1", "--proxy-docker-binary", "relative", "--proxy-docker-socket", "/var/run/docker.sock"},
	} {
		command := append([]string{"--server", server.URL, "--token", "fixture-registration-token"}, args...)
		if err := runRunner(command); err == nil {
			t.Fatalf("invalid proxy configuration accepted: %v", args)
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid local proxy configuration performed %d authentication or registration requests", requests.Load())
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("invalid local configuration created its state root: %v", err)
	}
}

func TestRunnerOrdinaryLegacyIdentityStartsAndRestartsWithoutProxy(t *testing.T) {
	t.Setenv("NEROCD_MODE", "development")
	t.Setenv("NEROCD_TOKEN", "")
	t.Setenv("NEROCD_RUNNER_CREDENTIAL_FILE", "")
	t.Setenv("NEROCD_RUNNER_JOURNAL_DIR", "")
	for _, test := range []struct {
		id           string
		capabilities []string
		flags        []string
	}{
		{"runner.legacy", []string{"shell"}, nil},
		{"runner_CUSTOM", []string{"shell", "CUSTOM", "shell"}, []string{"--capabilities", "shell,CUSTOM,shell"}},
	} {
		t.Run(test.id, func(t *testing.T) {
			work := t.TempDir()
			journalPath := filepath.Join(work, "journal")
			var mu sync.Mutex
			var requests []string
			firstIdentity := true
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				requests = append(requests, r.URL.Path)
				credential := "Bearer dedicated-legacy-fixture"
				if r.URL.Path == "/api/v1/runners/register" {
					credential = "Bearer registration-fixture"
				}
				if r.Header.Get("Authorization") != credential {
					t.Error("ordinary startup used the wrong credential")
				}
				var response any
				switch r.URL.Path {
				case "/api/v1/runners/register":
					var input runnerRegisterRequest
					if json.NewDecoder(r.Body).Decode(&input) != nil || input.ID != test.id || !reflect.DeepEqual(input.Capabilities, test.capabilities) {
						t.Errorf("registration changed ordinary identity/capabilities: %+v", input)
					}
					response = runnerRegisterResponse{Runner: domain.Runner{ID: test.id, Capabilities: test.capabilities}, Token: "dedicated-legacy-fixture"}
				case "/api/v1/runners/identity":
					if firstIdentity {
						if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
							t.Errorf("identity ran after opening journal: %v", err)
						}
						firstIdentity = false
					}
					response = map[string]any{"runner_id": test.id, "capabilities": test.capabilities}
				case "/api/v1/runners/heartbeat":
					if info, err := os.Stat(journalPath); err != nil || !info.IsDir() {
						t.Errorf("ordinary identity did not reach journal startup: %v", err)
					}
					response = domain.Runner{ID: test.id, Capabilities: test.capabilities, Status: domain.RunnerActive}
				case "/api/v1/runners/telemetry":
					w.WriteHeader(http.StatusNoContent)
					return
				case "/api/v1/runners/claim":
					w.WriteHeader(http.StatusNotFound)
					return
				default:
					t.Errorf("unexpected ordinary startup request %s", r.URL.Path)
					w.WriteHeader(404)
					return
				}
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			args := []string{"--server", server.URL, "--token", "registration-fixture", "--id", test.id, "--work-dir", work, "--journal-dir", journalPath, "--once"}
			if err := runRunner(append(args, test.flags...)); err != nil {
				t.Fatal(err)
			}
			credentialPath := filepath.Join(work, "credential")
			if err := os.WriteFile(credentialPath, []byte("dedicated-legacy-fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := runRunner([]string{"--server", server.URL, "--credential-file", credentialPath, "--id", test.id, "--work-dir", work, "--journal-dir", journalPath, "--once"}); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			got := append([]string(nil), requests...)
			mu.Unlock()
			want := []string{"/api/v1/runners/register", "/api/v1/runners/identity", "/api/v1/runners/heartbeat", "/api/v1/runners/telemetry", "/api/v1/runners/claim", "/api/v1/runners/identity", "/api/v1/runners/heartbeat", "/api/v1/runners/telemetry", "/api/v1/runners/claim"}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("startup/restart order changed: %v", got)
			}
			journal, err := runner.OpenAttemptJournal(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := journal.Close(); err != nil {
					t.Error(err)
				}
			}()
			if journal.Depth() != 0 {
				t.Fatal("ordinary startup created proxy journal evidence")
			}
		})
	}
}

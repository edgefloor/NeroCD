//go:build unix

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func TestRunnerEnrolledIdentitySelectsSameDefaultJournalOnCredentialRestart(t *testing.T) {
	t.Setenv("NEROCD_MODE", "development")
	t.Setenv("NEROCD_TOKEN", "")
	t.Setenv("NEROCD_RUNNER_CREDENTIAL_FILE", "")
	t.Setenv("NEROCD_RUNNER_JOURNAL_DIR", "")
	identityDir := t.TempDir()
	if err := os.Chmod(identityDir, 0700); err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	credentialPath := filepath.Join(identityDir, "credential")
	enrollmentPath := filepath.Join(identityDir, "enrollment")
	if err := os.WriteFile(enrollmentPath, []byte("nce_"+strings.Repeat("a", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	const runnerID = "runner_enrolled_nondefault"
	now := time.Now().UTC()
	authority := runner.AttemptIdentity{RunID: "run_identity", LeaseID: "lease_identity", Attempt: 1, Fence: "fence_identity", CreatedAt: now, ExpiresAt: now.Add(2 * time.Minute)}
	lease := domain.RunLease{ID: authority.LeaseID, RunID: authority.RunID, RunnerID: runnerID, Attempt: 1, Fence: authority.Fence, Status: domain.LeaseActive, CreatedAt: now, ExpiresAt: authority.ExpiresAt}
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.URL.Path)
		mu.Unlock()
		if r.URL.Path != "/api/v1/runner-enrollments/consume" {
			credential, err := readRunnerCredentialFile(credentialPath)
			if err != nil || r.Header.Get("Authorization") != "Bearer "+credential {
				t.Errorf("dedicated credential was not used: %v", err)
			}
		}
		switch r.URL.Path {
		case "/api/v1/runner-enrollments/consume":
			_ = json.NewEncoder(w).Encode(app.ConsumedRunnerEnrollment{Runner: domain.Runner{ID: runnerID}})
		case "/api/v1/runners/identity":
			_ = json.NewEncoder(w).Encode(app.RunnerIdentity{RunnerID: runnerID})
		case "/api/v1/runners/lease":
			_ = json.NewEncoder(w).Encode(lease)
		case "/api/v1/runners/events/batch":
			var input runnerclient.EventBatchRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if input.RunID != authority.RunID || input.LeaseID != authority.LeaseID || input.Attempt != 1 || input.Fence != authority.Fence {
				t.Errorf("wrong replay authority: %+v", input)
			}
			var events []domain.RunLog
			for _, event := range input.Events {
				events = append(events, domain.RunLog{EventKey: event.EventKey, RunID: input.RunID, LeaseID: input.LeaseID, Attempt: input.Attempt, RequestedSequence: event.Sequence, Stream: event.Stream, Message: event.Message})
			}
			_ = json.NewEncoder(w).Encode(runnerclient.EventBatchAck{Events: events})
		case "/api/v1/runners/complete":
			var input runnerclient.CompleteRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if input.CompletionKey != "completion_identity" {
				t.Errorf("completion key changed: %+v", input)
			}
			completed := lease
			completed.Status = domain.RunSucceeded
			_ = json.NewEncoder(w).Encode(completed)
		case "/api/v1/runners/heartbeat":
			_ = json.NewEncoder(w).Encode(domain.Runner{ID: runnerID, Status: domain.RunnerActive})
		case "/api/v1/runners/telemetry":
			w.WriteHeader(204)
		case "/api/v1/runners/claim":
			w.WriteHeader(404)
		default:
			t.Errorf("unexpected runner request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	args := []string{"--server", server.URL, "--credential-file", credentialPath, "--work-dir", workDir, "--once"}
	if err := runRunner(append(append([]string{}, args...), "--enrollment-file", enrollmentPath)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(enrollmentPath); !os.IsNotExist(err) {
		t.Fatalf("consumed enrollment remains: %v", err)
	}
	entries, err := os.ReadDir(workDir)
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), ".nerocd-journal-") {
		t.Fatalf("default journals=%v err=%v", entries, err)
	}
	journalPath := filepath.Join(workDir, entries[0].Name())
	journal, err := runner.OpenAttemptJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Error(err)
		}
	})
	if _, err = journal.AppendEvent(runner.JournalEvent{ID: "event_identity", Attempt: authority, Sequence: 4, Stream: domain.LogStdout, Message: "durable output", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.AppendCompletion(runner.JournalCompletion{ID: "completion_identity", Attempt: authority, Status: domain.RunSucceeded, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	requests = nil
	mu.Unlock()
	if err = runRunner(args); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got := append([]string(nil), requests...)
	mu.Unlock()
	want := []string{"/api/v1/runners/identity", "/api/v1/runners/lease", "/api/v1/runners/events/batch", "/api/v1/runners/complete", "/api/v1/runners/heartbeat", "/api/v1/runners/telemetry", "/api/v1/runners/claim"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("restart ordering=%v want=%v", got, want)
	}
	after, err := os.ReadDir(workDir)
	if err != nil || len(after) != 1 || after[0].Name() != entries[0].Name() {
		t.Fatalf("restart selected another journal: %v %v", after, err)
	}
	replayed, err := runner.OpenAttemptJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := replayed.Close(); err != nil {
			t.Error(err)
		}
	}()
	if replayed.Depth() != 0 {
		t.Fatalf("journal not drained: %+v", replayed.Snapshot())
	}
}

func TestRunnerIdentityFailurePreventsJournalAndRuntimeTraffic(t *testing.T) {
	t.Setenv("NEROCD_MODE", "development")
	t.Setenv("NEROCD_TOKEN", "")
	t.Setenv("NEROCD_RUNNER_CREDENTIAL_FILE", "")
	t.Setenv("NEROCD_RUNNER_JOURNAL_DIR", "")
	for _, tc := range []struct {
		name                  string
		status                int
		body                  string
		explicitID, transport bool
	}{{"explicit mismatch", 200, `{"runner_id":"runner_actual"}`, true, false}, {"explicit default mismatch", 200, `{"runner_id":"runner_actual"}`, true, false}, {"unauthorized", 401, `{}`, false, false}, {"unsupported server", 404, `{}`, false, false}, {"server failure", 500, `{}`, false, false}, {"empty identity", 200, `{}`, false, false}, {"transport", 0, "", false, true}} {
		for _, existing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing=%t", tc.name, existing), func(t *testing.T) {
				identityDir := t.TempDir()
				if err := os.Chmod(identityDir, 0700); err != nil {
					t.Fatal(err)
				}
				credential := filepath.Join(identityDir, "credential")
				if err := os.WriteFile(credential, []byte("ncr_"+strings.Repeat("b", 64)), 0600); err != nil {
					t.Fatal(err)
				}
				workDir := t.TempDir()
				journalPath := filepath.Join(workDir, "journal")
				if existing {
					if err := os.Mkdir(journalPath, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(journalPath, "journal.json"), []byte("untouched journal sentinel"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				var mu sync.Mutex
				var paths []string
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					paths = append(paths, r.URL.Path)
					mu.Unlock()
					if r.URL.Path != "/api/v1/runners/identity" {
						t.Errorf("runtime started before identity: %s", r.URL.Path)
						w.WriteHeader(500)
						return
					}
					if tc.transport {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
						return
					}
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(tc.body))
				}))
				defer server.Close()
				args := []string{"--server", server.URL, "--credential-file", credential, "--work-dir", workDir, "--journal-dir", journalPath, "--once"}
				if tc.explicitID {
					id := "runner_other"
					if tc.name == "explicit default mismatch" {
						id = "runner_local"
					}
					args = append(args, "--id", id)
				}
				err := runRunner(args)
				if err == nil {
					t.Fatal("identity failure accepted")
				}
				if tc.explicitID && !strings.Contains(err.Error(), "--id does not match") {
					t.Fatalf("mismatch error: %v", err)
				}
				mu.Lock()
				got := append([]string(nil), paths...)
				mu.Unlock()
				if !reflect.DeepEqual(got, []string{"/api/v1/runners/identity"}) {
					t.Fatalf("requests=%v", got)
				}
				if existing {
					contents, err := os.ReadFile(filepath.Join(journalPath, "journal.json"))
					if err != nil || string(contents) != "untouched journal sentinel" {
						t.Fatalf("journal mutated: %q %v", contents, err)
					}
					files, err := os.ReadDir(journalPath)
					if err != nil || len(files) != 1 {
						t.Fatalf("journal opened/mutated: %v %v", files, err)
					}
				} else if _, err := os.Lstat(journalPath); !os.IsNotExist(err) {
					t.Fatalf("journal created before identity: %v", err)
				}
			})
		}
	}
}

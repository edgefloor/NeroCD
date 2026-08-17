package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func TestExecuteClaimRunnerFileSecretsAreAuthorizedAndRedactedBeforeReporting(t *testing.T) {
	secret := "runtime-secret-value-42"
	root := secretTestRoot(t)
	if err := os.WriteFile(filepath.Join(root, "database-password"), []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	claim := secretTestClaim(secret)
	var mu sync.Mutex
	paths := []string{}
	messages := []string{}
	accesses := 0
	completions := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mu.Lock()
		paths = append(paths, request.URL.Path)
		mu.Unlock()
		switch request.URL.Path {
		case "/api/v1/runners/renew", "/api/v1/runners/lease":
			lease := claim.Lease
			lease.ExpiresAt = time.Now().Add(30 * time.Second)
			_ = json.NewEncoder(w).Encode(lease)
		case "/api/v1/runners/heartbeat":
			_ = json.NewEncoder(w).Encode(domain.Runner{ID: claim.Lease.RunnerID})
		case "/api/v1/runners/secrets/access":
			var input struct {
				AccessID string `json:"access_id"`
				RunID    string `json:"run_id"`
				LeaseID  string `json:"lease_id"`
				Attempt  int    `json:"attempt"`
				Fence    string `json:"fence"`
				Binding  string `json:"binding"`
				Provider string `json:"provider"`
				Version  string `json:"version"`
			}
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.RunID != claim.Run.ID || input.LeaseID != claim.Lease.ID || input.Attempt != claim.Lease.Attempt || input.Fence != claim.Lease.Fence || input.Binding != "database-password" || input.Provider != domain.ProviderRunnerFile || input.Version != "v1" {
				t.Errorf("secret access input=%#v", input)
			}
			mu.Lock()
			accesses++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(domain.SecretAccessGrant{AccessID: input.AccessID, RunID: input.RunID, LeaseID: input.LeaseID, Attempt: input.Attempt, Binding: input.Binding, Provider: input.Provider, Version: input.Version, AuthorizedAt: time.Now().UTC()})
		case "/api/v1/runners/events/batch":
			batch := decodeRunnerEventBatch(t, request)
			mu.Lock()
			for _, event := range batch.Events {
				messages = append(messages, event.Message)
			}
			mu.Unlock()
			writeRunnerEventBatchAck(t, w, batch)
		case "/api/v1/runners/complete":
			mu.Lock()
			completions++
			mu.Unlock()
			lease := claim.Lease
			lease.Status = domain.RunSucceeded
			_ = json.NewEncoder(w).Encode(lease)
		default:
			t.Errorf("unexpected request %s", request.URL.Path)
		}
	}))
	defer server.Close()
	journal := openReplayTestJournal(t)
	defer journal.Close()
	if err := executeClaimWithJournalAndSecretRoot(server.URL, "credential", claim, secretTestPhysicalTemp(t), 20*time.Millisecond, journal, root); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(messages, "\n")
	for _, forbidden := range []string{
		secret,
		base64.StdEncoding.EncodeToString([]byte(secret)),
		base64.RawURLEncoding.EncodeToString([]byte(secret)),
		hex.EncodeToString([]byte(secret)),
		strings.ToUpper(hex.EncodeToString([]byte(secret))),
	} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("reported messages leaked sensitive form %q: %s", forbidden, joined)
		}
	}
	for _, expected := range []string{"safe-before", "safe-after", runner.RedactionMarker} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("reported messages missing %q: %s", expected, joined)
		}
	}
	if accesses != 1 || completions != 1 || journal.Depth() != 0 {
		t.Fatalf("accesses=%d completions=%d journal_depth=%d paths=%v", accesses, completions, journal.Depth(), paths)
	}
	accessIndex, processLogIndex := -1, -1
	for index, path := range paths {
		if path == "/api/v1/runners/secrets/access" && accessIndex < 0 {
			accessIndex = index
		}
		if path == "/api/v1/runners/events/batch" && processLogIndex < 0 {
			processLogIndex = index
		}
	}
	if accessIndex < 0 || processLogIndex < 0 || accessIndex >= processLogIndex {
		t.Fatalf("secret authorization did not precede reporting: %v", paths)
	}
}

func TestExecuteClaimMissingOrStaleRunnerFileSecretNeverSpawnsProcess(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "stale"}[stale], func(t *testing.T) {
			root := secretTestRoot(t)
			claim := secretTestClaim("unused")
			marker := filepath.Join(secretTestPhysicalTemp(t), "process-started")
			claim.PrimitivePlan.Process.Command = []string{"/usr/bin/touch", marker}
			var mu sync.Mutex
			events, completions, accesses := 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				switch request.URL.Path {
				case "/api/v1/runners/renew", "/api/v1/runners/lease":
					lease := claim.Lease
					lease.ExpiresAt = time.Now().Add(30 * time.Second)
					_ = json.NewEncoder(w).Encode(lease)
				case "/api/v1/runners/heartbeat":
					_ = json.NewEncoder(w).Encode(domain.Runner{ID: claim.Lease.RunnerID})
				case "/api/v1/runners/secrets/access":
					mu.Lock()
					accesses++
					mu.Unlock()
					if stale {
						w.WriteHeader(http.StatusNotFound)
						_, _ = w.Write([]byte(`{"code":"not_found","error":"not found"}`))
						return
					}
					var input map[string]any
					_ = json.NewDecoder(request.Body).Decode(&input)
					_ = json.NewEncoder(w).Encode(domain.SecretAccessGrant{AccessID: input["access_id"].(string), RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: claim.Lease.Attempt, Binding: "database-password", Provider: domain.ProviderRunnerFile, Version: "v1", AuthorizedAt: time.Now().UTC()})
				case "/api/v1/runners/events/batch":
					mu.Lock()
					events++
					mu.Unlock()
					writeRunnerEventAck(t, w, request)
				case "/api/v1/runners/complete":
					mu.Lock()
					completions++
					mu.Unlock()
					lease := claim.Lease
					lease.Status = domain.RunFailed
					_ = json.NewEncoder(w).Encode(lease)
				}
			}))
			defer server.Close()
			journal := openReplayTestJournal(t)
			defer journal.Close()
			err := executeClaimWithJournalAndSecretRoot(server.URL, "credential", claim, secretTestPhysicalTemp(t), 20*time.Millisecond, journal, root)
			if err == nil {
				t.Fatal("missing/stale secret unexpectedly succeeded")
			}
			if stale && !errors.Is(err, errLeaseAuthorityLost) {
				t.Fatalf("stale error=%v", err)
			}
			if _, statErr := os.Stat(marker); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("process marker exists or stat failed: %v", statErr)
			}
			mu.Lock()
			defer mu.Unlock()
			if accesses != 1 {
				t.Fatalf("accesses=%d", accesses)
			}
			if stale && (events != 0 || completions != 0) {
				t.Fatalf("stale attempt issued terminal traffic events=%d completions=%d", events, completions)
			}
			if !stale && completions != 1 {
				t.Fatalf("missing secret completion count=%d", completions)
			}
		})
	}
}

func secretTestClaim(_ string) domain.ClaimedRun {
	now := time.Now().UTC()
	lease := domain.RunLease{ID: "lease_secret", RunID: "run_secret", RunnerID: "runner_secret", Attempt: 1, Fence: "opaque-fence", Status: domain.LeaseActive, CreatedAt: now, ExpiresAt: now.Add(30 * time.Second)}
	script := `printf 'safe-before\n'; printf '%s' "$TOKEN"; printf '\n'; printf '%s' "$TOKEN" | base64; printf '%s' "$TOKEN" | xxd -p; printf 'safe-after\n' >&2`
	return domain.ClaimedRun{
		Run: domain.TaskRun{ID: lease.RunID, Status: domain.RunRunning}, Lease: lease,
		PrimitivePlan: domain.RunnerPrimitivePlan{RunID: lease.RunID, Process: &domain.ProcessSpec{Command: []string{"/bin/sh", "-c", script}}, Secrets: []domain.SecretBinding{{
			Name: "database-password", Provider: domain.ProviderRunnerFile, Reference: "database-password", Target: "env:TOKEN", Required: true, Version: "v1", RedactEncodings: []string{"base64", "base64url", "hex"},
		}}},
	}
}

func secretTestRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(secretTestPhysicalTemp(t), "secrets")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func secretTestPhysicalTemp(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

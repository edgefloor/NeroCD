package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/api"
	"nerocd/internal/app"
	"nerocd/internal/domain"
)

func TestReadRunnerCredentialFileSecurity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("supported Unix credential checks")
	}
	tests := []struct {
		name    string
		prepare func(t *testing.T, root string) string
		want    string
	}{
		{name: "valid", want: "runner-secret", prepare: func(t *testing.T, root string) string {
			filename := filepath.Join(root, "valid")
			if err := os.WriteFile(filename, []byte("runner-secret\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return filename
		}},
		{name: "wrong_permissions", prepare: func(t *testing.T, root string) string {
			filename := filepath.Join(root, "permissive")
			if err := os.WriteFile(filename, []byte("secret"), 0o644); err != nil {
				t.Fatal(err)
			}
			return filename
		}},
		{name: "empty", prepare: func(t *testing.T, root string) string {
			filename := filepath.Join(root, "empty")
			if err := os.WriteFile(filename, []byte(" \n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return filename
		}},
		{name: "symlink", prepare: func(t *testing.T, root string) string {
			target := filepath.Join(root, "target")
			if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, "link")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			return link
		}},
		{name: "not_regular", prepare: func(t *testing.T, root string) string { return root }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			credential, err := readRunnerCredentialFile(tc.prepare(t, t.TempDir()))
			if tc.want != "" {
				if err != nil || credential != tc.want {
					t.Fatalf("credential=%q err=%v, want %q", credential, err, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("credential=%q unexpectedly accepted", credential)
			}
		})
	}
}

func TestRunnerCredentialFileSkipsRegistration(t *testing.T) {
	t.Setenv("NEROCD_MODE", "development")
	t.Setenv("NEROCD_TOKEN", "")
	t.Setenv("NEROCD_RUNNER_CREDENTIAL_FILE", "")
	credential := filepath.Join(t.TempDir(), "runner-token")
	const token = "dedicated-runner-token"
	if err := os.WriteFile(credential, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	registerRequests := 0
	heartbeatRequests := 0
	telemetry := app.RunnerOperationalTelemetry{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization header = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/api/v1/runners/register":
			mu.Lock()
			registerRequests++
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
		case "/api/v1/runners/heartbeat":
			mu.Lock()
			heartbeatRequests++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(domain.Runner{ID: "runner-dedicated", Status: domain.RunnerActive})
		case "/api/v1/runners/telemetry":
			if err := json.NewDecoder(r.Body).Decode(&telemetry); err != nil {
				t.Error(err)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/v1/runners/claim":
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	if err := runRunner([]string{"--server", server.URL, "--credential-file", credential, "--once"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if registerRequests != 0 || heartbeatRequests != 1 || telemetry.JournalDepth != 0 || telemetry.RetryCount != 0 || telemetry.RenewFailures != 0 {
		t.Fatalf("register=%d heartbeat=%d telemetry=%#v, want 0/1/zero", registerRequests, heartbeatRequests, telemetry)
	}
}

func TestRunnerOperationalCountersSaturateUnderConcurrency(t *testing.T) {
	counters := &runnerOperationalCounters{}
	counters.retries.Store(maxRunnerOperationalCounter - 1)
	counters.renewFailures.Store(maxRunnerOperationalCounter - 1)
	var workers sync.WaitGroup
	for range 32 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 64 {
				counters.Retry()
				counters.RenewFailure()
			}
		}()
	}
	workers.Wait()
	retries, renewals := counters.Snapshot()
	if retries != int(maxRunnerOperationalCounter) || renewals != int(maxRunnerOperationalCounter) {
		t.Fatalf("saturated counters retries=%d renewals=%d", retries, renewals)
	}
}

func TestRunnerCredentialAndRegistrationModesAreExclusive(t *testing.T) {
	t.Setenv("NEROCD_MODE", "development")
	t.Setenv("NEROCD_TOKEN", "")
	t.Setenv("NEROCD_RUNNER_CREDENTIAL_FILE", "")
	credential := filepath.Join(t.TempDir(), "runner-token")
	if err := os.WriteFile(credential, []byte("dedicated"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runRunner([]string{"--token", "privileged", "--credential-file", credential})
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("err=%v", err)
	}
}

func TestOpenAPIContractLoadsAndMatchesImplementedRoutes(t *testing.T) {
	document, err := loadOpenAPIContract("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	documented, err := readOpenAPIOperations(document, "../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}

	for _, route := range api.PublicRoutes() {
		key := route.Method + " " + route.Path
		if _, ok := documented[key]; !ok {
			t.Fatalf("%s is implemented but missing from OpenAPI contract", key)
		}
	}
}

func TestSupervisorCancellationPreventsTerminalRequest(t *testing.T) {
	s := newAttemptSupervisor(domain.RunLease{CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)})
	s.cancel()
	defer s.Close()
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	err := supervisedComplete(s, server.URL, "token", domain.RunLease{ID: "lease", Attempt: 1, Fence: "fence"}, "failed", nil)
	if err == nil || !errors.Is(err, errLeaseAuthorityLost) || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestLeaseWatcherStopDuringBlockedGETDoesNotCancelSupervisor(t *testing.T) {
	s := newAttemptSupervisor(domain.RunLease{CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)})
	defer s.Close()
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer server.Close()
	w := startLeaseWatcher(s.Context(), server.URL, "token", domain.RunLease{ID: "lease", Attempt: 1, Fence: "fence"}, time.Nanosecond, s.cancel)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("GET did not start")
	}
	done := make(chan struct{})
	go func() { w.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked")
	}
	if s.Context().Err() != nil {
		t.Fatal("intentional stop canceled supervisor")
	}
}

func TestLeaseWatcherAuthorityFailureCancelsSupervisor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"non_active", func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(domain.RunLease{Status: "expired"})
		}},
		{"authority_failure", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusForbidden) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAttemptSupervisor(domain.RunLease{CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)})
			defer s.Close()
			server := httptest.NewServer(tc.handler)
			defer server.Close()
			w := startLeaseWatcher(s.Context(), server.URL, "token", domain.RunLease{ID: "lease", Attempt: 1, Fence: "fence"}, time.Nanosecond, s.cancel)
			select {
			case <-s.Context().Done():
			case <-time.After(time.Second):
				t.Fatal("supervisor not canceled")
			}
			w.Stop()
			w.Stop()
		})
	}
}

func TestCompleteAttempt(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			s := newAttemptSupervisor(domain.RunLease{CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)})
			defer s.Close()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req runnerCompleteRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				if req.LeaseID != "lease" || req.Attempt != 1 || req.Fence != "fence" || req.Status != "succeeded" {
					t.Errorf("bad request %#v", req)
				}
				w.WriteHeader(status)
				if status == http.StatusOK {
					_ = json.NewEncoder(w).Encode(domain.RunLease{ID: "lease", Status: "succeeded"})
				}
			}))
			defer server.Close()
			w := startLeaseWatcher(s.Context(), server.URL, "token", domain.RunLease{ID: "lease", Attempt: 1, Fence: "fence"}, time.Hour, s.cancel)
			r := startLeaseRenewer(s.Context(), s, server.URL, "token", domain.RunLease{ID: "lease", Attempt: 1, Fence: "fence", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}, time.Hour)
			err := completeAttempt(s, w, r, server.URL, "token", domain.RunLease{ID: "lease", Attempt: 1, Fence: "fence"}, "succeeded", nil)
			if status == http.StatusOK {
				if err != nil || s.Context().Err() != nil {
					t.Fatalf("healthy completion err=%v ctx=%v", err, s.Context().Err())
				}
			} else if err == nil || !errors.Is(err, errLeaseAuthorityLost) || s.Context().Err() == nil {
				t.Fatalf("failure err=%v ctx=%v", err, s.Context().Err())
			}
			w.Stop()
		})
	}
}

func TestLeaseRenewerStopDuringBlockedRenewDoesNotCancelSupervisor(t *testing.T) {
	s := newAttemptSupervisor(domain.RunLease{CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)})
	defer s.Close()
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/renew") {
			close(started)
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}
	}))
	defer server.Close()
	r := startLeaseRenewer(s.Context(), s, server.URL, "token", domain.RunLease{ID: "lease", Attempt: 1, Fence: "fence", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}, time.Nanosecond)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("renew did not start")
	}
	done := make(chan struct{})
	go func() { r.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop blocked")
	}
	close(release)
	if s.Context().Err() != nil {
		t.Fatal("intentional renew stop canceled parent")
	}
}

func TestLeaseRenewerStopDuringBlockedHeartbeatDoesNotCancelSupervisor(t *testing.T) {
	s := newAttemptSupervisor(domain.RunLease{CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)})
	defer s.Close()
	beat := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/renew") {
			_ = json.NewEncoder(w).Encode(domain.RunLease{ExpiresAt: time.Now().Add(time.Minute)})
			return
		}
		close(beat)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	r := startLeaseRenewer(s.Context(), s, server.URL, "token", domain.RunLease{ID: "lease", Attempt: 1, Fence: "fence", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}, time.Nanosecond)
	select {
	case <-beat:
	case <-time.After(time.Second):
		t.Fatal("heartbeat did not start")
	}
	done := make(chan struct{}, 2)
	go func() { r.Stop(); done <- struct{}{} }()
	go func() { r.Stop(); done <- struct{}{} }()
	for range 2 {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("Stop blocked")
		}
	}
	close(release)
	if s.Context().Err() != nil {
		t.Fatal("intentional heartbeat stop canceled parent")
	}
}

func TestLeaseRenewerAuthorityFailureCancelsSupervisor(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail string
	}{{"renew", "renew"}, {"heartbeat", "heartbeat"}} {
		t.Run(tc.name, func(t *testing.T) {
			s := newAttemptSupervisor(domain.RunLease{CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)})
			defer s.Close()
			var callsMu sync.Mutex
			var calls []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				path := strings.TrimPrefix(r.URL.Path, "/api/v1/runners/")
				callsMu.Lock()
				calls = append(calls, path)
				callsMu.Unlock()
				if path == tc.fail {
					w.WriteHeader(http.StatusForbidden)
					return
				}
				if path == "renew" {
					_ = json.NewEncoder(w).Encode(domain.RunLease{ExpiresAt: time.Now().Add(time.Minute)})
				} else {
					_ = json.NewEncoder(w).Encode(domain.Runner{ID: "runner"})
				}
			}))
			defer server.Close()
			r := startLeaseRenewer(s.Context(), s, server.URL, "token", domain.RunLease{ID: "lease", Attempt: 1, Fence: "fence", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}, time.Nanosecond)
			select {
			case <-s.Context().Done():
			case <-time.After(time.Second):
				t.Fatal("parent not canceled")
			}
			r.Stop()
			seenFailure := false
			callsMu.Lock()
			for _, call := range calls {
				if call == tc.fail {
					seenFailure = true
				}
			}
			callsMu.Unlock()
			if !seenFailure {
				t.Fatalf("failing endpoint %q was not called", tc.fail)
			}
			retries, renewFailures := s.metrics.Snapshot()
			wantRenewFailures := 0
			if tc.fail == "renew" {
				wantRenewFailures = 1
			}
			if retries != 0 || renewFailures != wantRenewFailures {
				t.Fatalf("authority counters retries=%d renew_failures=%d want=0/%d", retries, renewFailures, wantRenewFailures)
			}
		})
	}
}

func TestAttemptSupervisorUpdateExtendsWatchdog(t *testing.T) {
	now := time.Now()
	oldExpiry := now.Add(2 * time.Second)
	extendedExpiry := now.Add(5 * time.Second)
	oldGuard := oldExpiry.Add(-time.Second)
	extendedGuard := extendedExpiry.Add(-time.Second)
	s := newAttemptSupervisor(domain.RunLease{CreatedAt: now, ExpiresAt: oldExpiry})
	defer s.Close()
	s.Update(domain.RunLease{ExpiresAt: extendedExpiry})
	checkpoint := extendedGuard.Add(-400 * time.Millisecond)
	select {
	case <-time.After(time.Until(checkpoint)):
	case <-s.Context().Done():
		t.Fatalf("canceled before extended checkpoint; old=%v extended=%v", oldGuard, extendedGuard)
	}
	ctx, cancel, err := s.RequestContext()
	if err != nil || ctx.Err() != nil {
		t.Fatalf("request context err=%v", err)
	}
	cancel()
	select {
	case <-s.Context().Done():
		if time.Now().Before(extendedGuard.Add(-150 * time.Millisecond)) {
			t.Fatal("canceled too early")
		}
	case <-time.After(time.Until(extendedGuard.Add(time.Second))):
		t.Fatal("did not cancel near extended guard")
	}
}

func TestExecuteClaimPreflightRenewalExtendsAuthorityThroughCompletion(t *testing.T) {
	now := time.Now()
	lease := domain.RunLease{ID: "lease", RunID: "run", RunnerID: "runner", Attempt: 1, Fence: "fence", CreatedAt: now.Add(-3 * time.Second), ExpiresAt: now.Add(2 * time.Second)}
	var mu sync.Mutex
	order := []string{}
	completes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		order = append(order, strings.TrimPrefix(r.URL.Path, "/api/v1/runners/"))
		mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/runners/renew":
			_ = json.NewEncoder(w).Encode(domain.RunLease{ID: lease.ID, RunID: lease.RunID, RunnerID: lease.RunnerID, Attempt: lease.Attempt, Fence: lease.Fence, CreatedAt: lease.CreatedAt, ExpiresAt: time.Now().Add(5 * time.Second), Status: "active"})
		case "/api/v1/runners/lease":
			active := lease
			active.Status = "active"
			_ = json.NewEncoder(w).Encode(active)
		case "/api/v1/runners/events/batch":
			writeRunnerEventAck(t, w, r)
		case "/api/v1/runners/heartbeat":
			_ = json.NewEncoder(w).Encode(domain.Runner{ID: "runner"})
		case "/api/v1/runners/complete":
			var req runnerCompleteRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.LeaseID != lease.ID || req.Attempt != lease.Attempt || req.Fence != lease.Fence || req.Status != "succeeded" {
				t.Errorf("bad completion %#v", req)
			}
			mu.Lock()
			completes++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(domain.RunLease{ID: lease.ID, Status: "succeeded"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	claim := domain.ClaimedRun{Lease: lease, Run: domain.TaskRun{ID: "run"}, PrimitivePlan: domain.RunnerPrimitivePlan{Process: &domain.ProcessSpec{Command: []string{"sh", "-c", "sleep 1.3"}}}}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_ = ctx
	if err := executeClaim(server.URL, "token", claim, t.TempDir(), 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 1200*time.Millisecond {
		t.Fatal("process did not cross original guard")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) == 0 || order[0] != "renew" || completes != 1 {
		t.Fatalf("order=%v completes=%d", order, completes)
	}
}

func TestExecuteClaimRenewalFailureCancelsProcessWithoutTerminalWrites(t *testing.T) {
	now := time.Now()
	lease := domain.RunLease{ID: "lease", RunID: "run", RunnerID: "runner", Attempt: 1, Fence: "fence", CreatedAt: now, ExpiresAt: now.Add(5 * time.Second)}
	marker := t.TempDir() + "/process-started"
	descendantPIDFile := t.TempDir() + "/descendant.pid"
	second := make(chan struct{})
	var mu sync.Mutex
	renews := 0
	logs := 0
	artifacts := 0
	completes := 0
	watcher := 0
	loss := false
	var lossAt time.Time
	var preflightGuard time.Time
	descendantPID := 0
	trace := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/v1/runners/renew":
			renews++
			if renews == 1 {
				trace = append(trace, "renew-preflight")
				expiresAt := time.Now().Add(5 * time.Second)
				preflightGuard = expiresAt.Add(-time.Second)
				_ = json.NewEncoder(w).Encode(domain.RunLease{ID: lease.ID, RunID: lease.RunID, RunnerID: lease.RunnerID, Attempt: 1, Fence: lease.Fence, CreatedAt: lease.CreatedAt, ExpiresAt: expiresAt, Status: "active"})
			} else {
				if _, err := os.Stat(marker); err != nil {
					t.Errorf("renewal loss occurred before process start marker: %v", err)
				} else {
					trace = append(trace, "process-started")
				}
				if watcher == 0 {
					t.Error("renewal loss occurred before any lease-status observation")
				} else {
					trace = append(trace, "watcher-observed")
				}
				contents, err := os.ReadFile(descendantPIDFile)
				if err != nil {
					t.Errorf("renewal loss occurred before descendant PID was recorded: %v", err)
				} else if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(contents))); parseErr != nil || pid <= 0 {
					t.Errorf("invalid descendant PID %q: %v", contents, parseErr)
				} else {
					descendantPID = pid
					trace = append(trace, "descendant-started")
				}
				trace = append(trace, "renew-loss")
				loss = true
				lossAt = time.Now()
				close(second)
				w.WriteHeader(http.StatusForbidden)
			}
		case "/api/v1/runners/lease":
			watcher++
			trace = append(trace, "lease")
			active := lease
			active.Status = "active"
			_ = json.NewEncoder(w).Encode(active)
		case "/api/v1/runners/events/batch":
			if loss {
				t.Error("log after loss")
			}
			var req runnerEventBatchRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Error(err)
			}
			logs += len(req.Events)
			trace = append(trace, "log")
			ack := runnerEventBatchAck{Events: make([]domain.RunLog, 0, len(req.Events))}
			for i, event := range req.Events {
				ack.Events = append(ack.Events, domain.RunLog{ID: fmt.Sprintf("log-%d", i), RunID: req.RunID, LeaseID: req.LeaseID, Attempt: req.Attempt, EventKey: event.EventKey, Sequence: event.Sequence, RequestedSequence: event.Sequence, Stream: event.Stream, Message: event.Message})
			}
			_ = json.NewEncoder(w).Encode(ack)
		case "/api/v1/runners/artifacts":
			if loss {
				t.Error("artifact after loss")
			}
			artifacts++
			trace = append(trace, "artifact")
			_ = json.NewEncoder(w).Encode(domain.ArtifactRecord{ID: "art"})
		case "/api/v1/runners/complete":
			if loss {
				t.Error("completion after loss")
			}
			completes++
			trace = append(trace, "complete")
			_ = json.NewEncoder(w).Encode(domain.RunLease{ID: lease.ID})
		case "/api/v1/runners/heartbeat":
			_ = json.NewEncoder(w).Encode(domain.Runner{ID: "runner"})
		}
	}))
	defer server.Close()
	processCommand := "touch \"$1\"; /bin/sh -c 'echo $$ > \"$2\"; exec /bin/sleep 30' sh \"$1\" \"$2\" & wait"
	claim := domain.ClaimedRun{Lease: lease, Run: domain.TaskRun{ID: "run"}, PrimitivePlan: domain.RunnerPrimitivePlan{Process: &domain.ProcessSpec{Command: []string{"/bin/sh", "-c", processCommand, "sh", marker, descendantPIDFile}}}}
	start := time.Now()
	err := executeClaim(server.URL, "token", claim, t.TempDir(), 20*time.Millisecond)
	select {
	case <-second:
	case <-time.After(3 * time.Second):
		t.Fatal("second renew not observed")
	}
	if !errors.Is(err, errLeaseAuthorityLost) {
		t.Fatalf("err=%v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("process cancellation exceeded transport bound")
	}
	mu.Lock()
	defer mu.Unlock()
	if time.Since(lossAt) > 750*time.Millisecond {
		t.Fatalf("loss-to-return=%v trace=%v", time.Since(lossAt), trace)
	}
	if preflightGuard.IsZero() {
		t.Fatal("preflight renewal did not record an authority guard")
	}
	if !lossAt.Before(preflightGuard.Add(-1500*time.Millisecond)) || !time.Now().Before(preflightGuard.Add(-1500*time.Millisecond)) {
		t.Fatalf("authority loss/return too close to preflight guard: loss=%s return=%s guard=%s trace=%v", lossAt, time.Now(), preflightGuard, trace)
	}
	if !strings.Contains(strings.Join(trace, ","), "process-started,watcher-observed,descendant-started,renew-loss") {
		t.Fatalf("renewal loss lacked required causal events: trace=%v", trace)
	}
	if renews != 2 || watcher == 0 || logs == 0 || artifacts != 0 || completes != 0 || descendantPID == 0 {
		t.Fatalf("renews=%d watcher=%d logs=%d artifacts=%d completes=%d descendant=%d trace=%v", renews, watcher, logs, artifacts, completes, descendantPID, trace)
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		waitForTestProcessGone(t, descendantPID)
	}
}

func writeRunnerEventAck(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	var req runnerEventBatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Error(err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	ack := runnerEventBatchAck{Events: make([]domain.RunLog, 0, len(req.Events))}
	for i, event := range req.Events {
		ack.Events = append(ack.Events, domain.RunLog{ID: fmt.Sprintf("log-%d", i), RunID: req.RunID, LeaseID: req.LeaseID, Attempt: req.Attempt, EventKey: event.EventKey, Sequence: event.Sequence, RequestedSequence: event.Sequence, Stream: event.Stream, Message: event.Message})
	}
	if err := json.NewEncoder(w).Encode(ack); err != nil {
		t.Error(err)
	}
}

func waitForTestProcessGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		output, err := exec.Command("/bin/ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
		if err != nil || strings.HasPrefix(strings.TrimSpace(string(output)), "Z") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("descendant PID %d is still alive", pid)
}

func TestRuntimeConfigValidation(t *testing.T) {
	t.Setenv("NEROCD_DATABASE_URL", "mysql://example.local/db")
	if _, err := loadRuntimeConfig(":8080"); err == nil || !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("loadRuntimeConfig accepted invalid database URL: %v", err)
	}

	t.Setenv("NEROCD_DATABASE_URL", "")
	t.Setenv("NEROCD_REQUIRE_DATABASE", "true")
	if _, err := loadRuntimeConfig(":8080"); err == nil || !strings.Contains(err.Error(), "requires NEROCD_DATABASE_URL") {
		t.Fatalf("loadRuntimeConfig accepted missing required database: %v", err)
	}

	t.Setenv("NEROCD_REQUIRE_DATABASE", "")
	t.Setenv("NEROCD_DEV_MEMORY", "true")
	bootstrap := filepath.Join(t.TempDir(), "bootstrap-password")
	if err := os.WriteFile(bootstrap, []byte("test-password\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEROCD_DEV_BOOTSTRAP_EMAIL", "test@example.invalid")
	t.Setenv("NEROCD_DEV_BOOTSTRAP_PASSWORD_FILE", bootstrap)
	cfg, err := loadRuntimeConfig("127.0.0.1:18080")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.addr != "127.0.0.1:18080" || cfg.databaseURL != "" || !cfg.cookieSecure {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	t.Setenv("NEROCD_COOKIE_SECURE", "false")
	cfg, err = loadRuntimeConfig("127.0.0.1:18080")
	if err != nil || cfg.cookieSecure {
		t.Fatalf("local cookie override config=%#v err=%v", cfg, err)
	}
	t.Setenv("NEROCD_COOKIE_SECURE", "invalid")
	if _, err := loadRuntimeConfig("127.0.0.1:18080"); err == nil || !strings.Contains(err.Error(), "NEROCD_COOKIE_SECURE") {
		t.Fatalf("loadRuntimeConfig accepted invalid cookie security override: %v", err)
	}
}

func TestMigrationFilesAreOrderedAndChecksummed(t *testing.T) {
	files, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 || files[0] != "migrations/0001_backend_primitives.sql" {
		t.Fatalf("unexpected migration files: %#v", files)
	}
	if checksum := sqlChecksum([]byte("select 1;")); !strings.HasPrefix(checksum, "sha256:") {
		t.Fatalf("unexpected checksum: %s", checksum)
	}
}

func TestDocumentedOperationMetadataComesFromOpenAPIModel(t *testing.T) {
	document, err := loadOpenAPIContract("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	documented, err := readOpenAPIOperations(document, "../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}

	session := documented[http.MethodPost+" /api/v1/sessions"]
	if !session.OperationID {
		t.Fatal("POST /api/v1/sessions missing operationId metadata")
	}
	if !session.SecurityEmpty {
		t.Fatal("POST /api/v1/sessions should document security: []")
	}
	if !session.RequestBody || !session.JSONRequestBody {
		t.Fatal("POST /api/v1/sessions should document a JSON request body")
	}
	if !session.JSONResponseCodes["201"] {
		t.Fatal("POST /api/v1/sessions should document a JSON 201 response")
	}

	projects := documented[http.MethodGet+" /api/v1/projects"]
	if projects.SecurityEmpty {
		t.Fatal("GET /api/v1/projects should inherit bearer security")
	}
	if !projects.Responses["401"] {
		t.Fatal("GET /api/v1/projects should document 401")
	}
	for _, key := range []string{
		http.MethodDelete + " /api/v1/sessions",
		http.MethodPost + " /api/v1/runner-enrollments/consume",
	} {
		op := documented[key]
		if !op.SecuritySchemes["bearerAuth"] || op.SecuritySchemes["browserSession"] {
			t.Fatalf("%s must explicitly document bearer-only security: %#v", key, op.SecuritySchemes)
		}
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func TestRunnerFailureClassification(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want runnerFailureClass
	}{
		{name: "timeout", err: context.DeadlineExceeded, want: runnerFailureTransient},
		{name: "request timeout", err: &runnerAPIError{StatusCode: http.StatusRequestTimeout}, want: runnerFailureTransient},
		{name: "rate limit", err: &runnerAPIError{StatusCode: http.StatusTooManyRequests}, want: runnerFailureTransient},
		{name: "server", err: &runnerAPIError{StatusCode: http.StatusBadGateway}, want: runnerFailureTransient},
		{name: "unauthorized", err: &runnerAPIError{StatusCode: http.StatusUnauthorized}, want: runnerFailureAuthority},
		{name: "fenced", err: &runnerAPIError{StatusCode: http.StatusNotFound}, want: runnerFailureAuthority},
		{name: "conflict", err: &runnerAPIError{StatusCode: http.StatusConflict}, want: runnerFailureAuthority},
		{name: "bad request", err: &runnerAPIError{StatusCode: http.StatusBadRequest}, want: runnerFailurePermanent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyRunnerFailure(tc.err); got != tc.want {
				t.Fatalf("class = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRunnerRequestLabelRemovesCapabilityQuery(t *testing.T) {
	fence := "fence_must_not_leak"
	label := runnerRequestLabel("https://runner.example/api/v1/runners/lease?lease_id=lease_1&attempt=1&fence=" + fence + "#fragment")
	if label != "https://runner.example/api/v1/runners/lease" || bytes.Contains([]byte(label), []byte(fence)) {
		t.Fatalf("runner request label leaked capability: %q", label)
	}
	apiErr := &runnerAPIError{Method: http.MethodGet, URL: label, Err: fmt.Errorf("request failed for ?fence=%s", fence)}
	if bytes.Contains([]byte(apiErr.Error()), []byte(fence)) {
		t.Fatalf("runner transport error leaked nested capability: %q", apiErr.Error())
	}
	apiErr = &runnerAPIError{Method: http.MethodGet, URL: label, StatusCode: http.StatusNotFound, Status: "404 Not Found", Detail: `{"error":"fence ` + fence + `"}`}
	if bytes.Contains([]byte(apiErr.Error()), []byte(fence)) {
		t.Fatalf("runner status error leaked reflected capability: %q", apiErr.Error())
	}
}

func TestAttemptReporterRetriesCommittedEventAfterLostResponse(t *testing.T) {
	lease := replayTestLease()
	supervisor := newAttemptSupervisor(lease)
	defer supervisor.Close()
	journal := openReplayTestJournal(t)
	defer journal.Close()

	var mu sync.Mutex
	requests := 0
	committedKey := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/runners/events/batch" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var batch runnerEventBatchRequest
		if err := json.NewDecoder(request.Body).Decode(&batch); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests++
		current := requests
		if committedKey == "" {
			committedKey = batch.Events[0].EventKey
		} else if committedKey != batch.Events[0].EventKey {
			t.Errorf("retry event key changed from %q to %q", committedKey, batch.Events[0].EventKey)
		}
		mu.Unlock()
		if current == 1 {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
			return
		}
		ack := runnerEventBatchAck{Events: make([]domain.RunLog, 0, len(batch.Events))}
		for i, event := range batch.Events {
			ack.Events = append(ack.Events, domain.RunLog{ID: fmt.Sprintf("log-%d", i), RunID: batch.RunID, LeaseID: batch.LeaseID, Attempt: batch.Attempt, EventKey: event.EventKey, Sequence: event.Sequence, RequestedSequence: event.Sequence, Stream: event.Stream, Message: event.Message})
		}
		_ = json.NewEncoder(w).Encode(ack)
	}))
	defer server.Close()

	reporter := startAttemptReporter(supervisor.Context(), supervisor, journal, server.URL, "credential", lease.RunID, lease)
	defer reporter.Stop()
	if err := reporter.Emit(domain.LogStdout, "committed once", 4); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := reporter.WaitEmpty(ctx); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 || committedKey == "" || journal.Depth() != 0 || supervisor.Context().Err() != nil {
		t.Fatalf("requests=%d key=%q depth=%d supervisor=%v", requests, committedKey, journal.Depth(), supervisor.Context().Err())
	}
}

func TestJournaledCompletionRetriesCommittedMutationAfterLostResponse(t *testing.T) {
	lease := replayTestLease()
	supervisor := newAttemptSupervisor(lease)
	defer supervisor.Close()
	journal := openReplayTestJournal(t)
	defer journal.Close()

	var mu sync.Mutex
	requests := 0
	completionKey := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var completion runnerCompleteRequest
		if err := json.NewDecoder(request.Body).Decode(&completion); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		requests++
		current := requests
		if completionKey == "" {
			completionKey = completion.CompletionKey
		} else if completionKey != completion.CompletionKey {
			t.Errorf("completion retry key changed from %q to %q", completionKey, completion.CompletionKey)
		}
		mu.Unlock()
		if current == 1 {
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
			return
		}
		completed := lease
		completed.Status = domain.RunSucceeded
		_ = json.NewEncoder(w).Encode(completed)
	}))
	defer server.Close()
	var completed domain.RunLease
	if err := journaledCompletion(supervisor, journal, server.URL, "credential", lease.RunID, lease, domain.RunSucceeded, &completed); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if requests != 2 || completionKey == "" || journal.Depth() != 0 || completed.Status != domain.RunSucceeded {
		t.Fatalf("requests=%d key=%q depth=%d completed=%#v", requests, completionKey, journal.Depth(), completed)
	}
}

func TestReconcileRunnerJournalFlushesBeforeAnyNewRunnerTraffic(t *testing.T) {
	lease := replayTestLease()
	journal := openReplayTestJournal(t)
	defer journal.Close()
	authority := journalAttemptIdentity(lease.RunID, lease, nil)
	event := runner.JournalEvent{ID: "event_pending", Attempt: authority, Sequence: 4, Stream: domain.LogSystem, Message: "pending", CreatedAt: time.Now().UTC()}
	completion := runner.JournalCompletion{ID: "completion_pending", Attempt: authority, Status: domain.RunSucceeded, CreatedAt: time.Now().UTC()}
	if _, err := journal.AppendEvent(event); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.AppendCompletion(completion); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	order := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mu.Lock()
		order = append(order, request.URL.Path)
		mu.Unlock()
		switch request.URL.Path {
		case "/api/v1/runners/lease":
			active := lease
			active.ExpiresAt = time.Now().Add(30 * time.Second)
			_ = json.NewEncoder(w).Encode(active)
		case "/api/v1/runners/events/batch":
			writeRunnerEventAck(t, w, request)
		case "/api/v1/runners/complete":
			var body runnerCompleteRequest
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.CompletionKey != completion.ID {
				t.Errorf("completion key=%q", body.CompletionKey)
			}
			completed := lease
			completed.Status = domain.RunSucceeded
			_ = json.NewEncoder(w).Encode(completed)
		default:
			t.Errorf("unexpected traffic before reconciliation: %s", request.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	if err := reconcileRunnerJournal(server.URL, "credential", journal); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[0] != "/api/v1/runners/lease" || order[1] != "/api/v1/runners/events/batch" || order[2] != "/api/v1/runners/complete" || journal.Depth() != 0 {
		t.Fatalf("order=%v depth=%d", order, journal.Depth())
	}
}

func TestRunnerStartupPreservesJournalWhenAuthorityIsUnproven(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{name: "unauthorized", status: http.StatusUnauthorized},
		{name: "forbidden", status: http.StatusForbidden},
		{name: "conflict", status: http.StatusConflict},
		{name: "transient", status: http.StatusBadGateway},
		{name: "unverified not found", status: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lease := replayTestLease()
			journalDir, credentialPath, before := createPendingRunnerStartup(t, lease)
			var mu sync.Mutex
			paths := []string{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				mu.Lock()
				paths = append(paths, request.URL.Path)
				mu.Unlock()
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			if err := runRunner([]string{"--server", server.URL, "--credential-file", credentialPath, "--journal-dir", journalDir, "--once"}); err == nil {
				t.Fatal("runner startup unexpectedly continued")
			}
			after, err := os.ReadFile(filepath.Join(journalDir, journalStateFilenameForTest))
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !bytes.Equal(before, after) || len(paths) != 1 || paths[0] != "/api/v1/runners/lease" {
				t.Fatalf("journal changed or later traffic occurred: equal=%v paths=%v", bytes.Equal(before, after), paths)
			}
		})
	}
}

func TestRunnerStartupLocalGuardExpiryPreservesJournal(t *testing.T) {
	lease := replayTestLease()
	journalDir, credentialPath, before := createPendingRunnerStartup(t, lease)
	var mu sync.Mutex
	paths := []string{}
	releaseRenew := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mu.Lock()
		paths = append(paths, request.URL.Path)
		mu.Unlock()
		switch request.URL.Path {
		case "/api/v1/runners/lease":
			active := lease
			active.CreatedAt = time.Now()
			active.ExpiresAt = active.CreatedAt.Add(1200 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(active)
		case "/api/v1/runners/renew":
			select {
			case <-request.Context().Done():
			case <-releaseRenew:
			}
		default:
			t.Errorf("unexpected request after guard expiry: %s", request.URL.Path)
		}
	}))
	defer server.Close()
	started := time.Now()
	if err := runRunner([]string{"--server", server.URL, "--credential-file", credentialPath, "--journal-dir", journalDir, "--once"}); err == nil {
		t.Fatal("runner startup unexpectedly survived local guard expiry")
	}
	close(releaseRenew)
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("local guard blocked startup for %v", elapsed)
	}
	after, err := os.ReadFile(filepath.Join(journalDir, journalStateFilenameForTest))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !bytes.Equal(before, after) || len(paths) != 2 || paths[0] != "/api/v1/runners/lease" || paths[1] != "/api/v1/runners/renew" {
		t.Fatalf("journal changed or later traffic occurred: equal=%v paths=%v", bytes.Equal(before, after), paths)
	}
}

func TestRunnerStartupInvalidAuthorityProbePreservesJournal(t *testing.T) {
	lease := replayTestLease()
	journalDir, credentialPath, before := createPendingRunnerStartup(t, lease)
	paths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		invalid := lease
		invalid.Fence = "different-fence"
		invalid.ExpiresAt = time.Now().Add(time.Minute)
		_ = json.NewEncoder(w).Encode(invalid)
	}))
	defer server.Close()
	if err := runRunner([]string{"--server", server.URL, "--credential-file", credentialPath, "--journal-dir", journalDir, "--once"}); err == nil {
		t.Fatal("runner startup accepted invalid authority probe")
	}
	after, err := os.ReadFile(filepath.Join(journalDir, journalStateFilenameForTest))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || len(paths) != 1 || paths[0] != "/api/v1/runners/lease" {
		t.Fatalf("journal changed or later traffic occurred: equal=%v paths=%v", bytes.Equal(before, after), paths)
	}
}

func TestReconcileRunnerJournalDiscardsOnlyExplicitFencedProbe(t *testing.T) {
	lease := replayTestLease()
	journal := openReplayTestJournal(t)
	defer journal.Close()
	appendReplayTestEvent(t, journal, lease)
	authority := journalAttemptIdentity(lease.RunID, lease, nil)
	if _, err := journal.AppendCompletion(runner.JournalCompletion{ID: "completion_startup_pending", Attempt: authority, Status: domain.RunSucceeded, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/v1/runners/lease" {
			t.Errorf("unexpected request after fenced probe: %s", request.URL.Path)
		}
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"not_found","error":"record not found"}`))
	}))
	defer server.Close()
	if err := reconcileRunnerJournal(server.URL, "credential", journal); err != nil {
		t.Fatal(err)
	}
	if journal.Depth() != 0 {
		t.Fatalf("journal depth=%d after explicit fenced probe", journal.Depth())
	}
}

func TestReconcileRunnerJournalActiveProbeRefreshesExpiredAuthorityAndReplays(t *testing.T) {
	lease := replayTestLease()
	lease.CreatedAt = time.Now().Add(-10 * time.Minute)
	lease.ExpiresAt = time.Now().Add(-30 * time.Second)
	journal := openReplayTestJournal(t)
	defer journal.Close()
	appendReplayTestEvent(t, journal, lease)
	var mu sync.Mutex
	paths := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mu.Lock()
		paths = append(paths, request.URL.Path)
		mu.Unlock()
		switch request.URL.Path {
		case "/api/v1/runners/lease", "/api/v1/runners/renew":
			active := lease
			active.ExpiresAt = time.Now().Add(5 * time.Second)
			active.Status = domain.LeaseActive
			_ = json.NewEncoder(w).Encode(active)
		case "/api/v1/runners/events/batch":
			writeRunnerEventAck(t, w, request)
		default:
			t.Errorf("unexpected replay request: %s", request.URL.Path)
		}
	}))
	defer server.Close()
	if err := reconcileRunnerJournal(server.URL, "credential", journal); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if journal.Depth() != 0 || len(paths) != 3 || paths[0] != "/api/v1/runners/lease" || paths[1] != "/api/v1/runners/renew" || paths[2] != "/api/v1/runners/events/batch" {
		t.Fatalf("depth=%d paths=%v", journal.Depth(), paths)
	}
}

func TestAttemptSupervisorOldCreatedAtUsesCurrentRemainingAuthority(t *testing.T) {
	lease := replayTestLease()
	lease.CreatedAt = time.Now().Add(-10 * time.Minute)
	lease.ExpiresAt = time.Now().Add(5 * time.Second)
	supervisor := newAttemptSupervisor(lease)
	defer supervisor.Close()
	if supervisor.Context().Err() != nil {
		t.Fatalf("old attempt age canceled fresh authority: %v", supervisor.Context().Err())
	}
	guard := supervisor.GuardDeadline()
	if remaining := time.Until(guard); remaining < 3*time.Second || remaining > 5*time.Second {
		t.Fatalf("guard remaining=%v, want current lease window", remaining)
	}
	ctx, cancel, err := supervisor.RequestContext()
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	if deadline, ok := ctx.Deadline(); !ok || deadline.After(guard.Add(20*time.Millisecond)) {
		t.Fatalf("request deadline=%v guard=%v", deadline, guard)
	}
}

func TestRunnerStartupMultiBatchFailurePreservesExactJournal(t *testing.T) {
	for _, mode := range []string{"invalid_ack", "conflict", "transient"} {
		t.Run(mode, func(t *testing.T) {
			lease := replayTestLease()
			lease.CreatedAt = time.Now().Add(-10 * time.Minute)
			journalDir, credentialPath, before := createReplayRunnerStartup(t, lease, 65, true)
			var mu sync.Mutex
			paths := []string{}
			batches := []int{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				mu.Lock()
				paths = append(paths, request.URL.Path)
				mu.Unlock()
				switch request.URL.Path {
				case "/api/v1/runners/lease":
					active := lease
					active.ExpiresAt = time.Now().Add(2200 * time.Millisecond)
					_ = json.NewEncoder(w).Encode(active)
				case "/api/v1/runners/events/batch":
					batch := decodeRunnerEventBatch(t, request)
					mu.Lock()
					batches = append(batches, len(batch.Events))
					call := len(batches)
					mu.Unlock()
					if call == 1 {
						writeRunnerEventBatchAck(t, w, batch)
						return
					}
					switch mode {
					case "invalid_ack":
						_ = json.NewEncoder(w).Encode(runnerEventBatchAck{})
					case "conflict":
						w.WriteHeader(http.StatusConflict)
					case "transient":
						w.WriteHeader(http.StatusBadGateway)
					}
				default:
					t.Errorf("unexpected later startup traffic: %s", request.URL.Path)
				}
			}))
			defer server.Close()
			if err := runRunner([]string{"--server", server.URL, "--credential-file", credentialPath, "--journal-dir", journalDir, "--once"}); err == nil {
				t.Fatal("partial multi-batch replay unexpectedly succeeded")
			}
			after, err := os.ReadFile(filepath.Join(journalDir, journalStateFilenameForTest))
			if err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if !bytes.Equal(before, after) || len(batches) < 2 || batches[0] != 64 || batches[1] != 1 {
				t.Fatalf("journal changed or batching invalid: equal=%v batches=%v paths=%v", bytes.Equal(before, after), batches, paths)
			}
			for _, path := range paths {
				if path == "/api/v1/runners/heartbeat" || path == "/api/v1/runners/claim" || path == "/api/v1/runners/complete" {
					t.Fatalf("later traffic occurred after partial replay: %v", paths)
				}
			}
		})
	}
}

func TestRunnerStartupCompletionFailurePreservesAllAcknowledgedEvents(t *testing.T) {
	lease := replayTestLease()
	lease.CreatedAt = time.Now().Add(-10 * time.Minute)
	journalDir, credentialPath, before := createReplayRunnerStartup(t, lease, 65, true)
	var mu sync.Mutex
	paths := []string{}
	batches := []int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		mu.Lock()
		paths = append(paths, request.URL.Path)
		mu.Unlock()
		switch request.URL.Path {
		case "/api/v1/runners/lease":
			active := lease
			active.ExpiresAt = time.Now().Add(30 * time.Second)
			_ = json.NewEncoder(w).Encode(active)
		case "/api/v1/runners/events/batch":
			batch := decodeRunnerEventBatch(t, request)
			mu.Lock()
			batches = append(batches, len(batch.Events))
			mu.Unlock()
			writeRunnerEventBatchAck(t, w, batch)
		case "/api/v1/runners/complete":
			w.WriteHeader(http.StatusConflict)
		default:
			t.Errorf("unexpected later startup traffic: %s", request.URL.Path)
		}
	}))
	defer server.Close()
	if err := runRunner([]string{"--server", server.URL, "--credential-file", credentialPath, "--journal-dir", journalDir, "--once"}); err == nil {
		t.Fatal("completion failure unexpectedly allowed startup")
	}
	after, err := os.ReadFile(filepath.Join(journalDir, journalStateFilenameForTest))
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !bytes.Equal(before, after) || len(batches) != 2 || batches[0] != 64 || batches[1] != 1 {
		t.Fatalf("journal changed after completion failure: equal=%v batches=%v paths=%v", bytes.Equal(before, after), batches, paths)
	}
	for _, path := range paths {
		if path == "/api/v1/runners/heartbeat" || path == "/api/v1/runners/claim" {
			t.Fatalf("later traffic occurred after completion failure: %v", paths)
		}
	}
}

func TestReconcileRunnerJournalMultiBatchSuccessDrainsAtomically(t *testing.T) {
	lease := replayTestLease()
	lease.CreatedAt = time.Now().Add(-10 * time.Minute)
	journalDir, _, _ := createReplayRunnerStartup(t, lease, 65, true)
	journal, err := runner.OpenAttemptJournal(journalDir)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	batches := []int{}
	completions := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/runners/lease":
			active := lease
			active.ExpiresAt = time.Now().Add(30 * time.Second)
			_ = json.NewEncoder(w).Encode(active)
		case "/api/v1/runners/events/batch":
			batch := decodeRunnerEventBatch(t, request)
			batches = append(batches, len(batch.Events))
			writeRunnerEventBatchAck(t, w, batch)
		case "/api/v1/runners/complete":
			completions++
			completed := lease
			completed.Status = domain.RunSucceeded
			_ = json.NewEncoder(w).Encode(completed)
		default:
			t.Errorf("unexpected replay request: %s", request.URL.Path)
		}
	}))
	defer server.Close()
	if err := reconcileRunnerJournal(server.URL, "credential", journal); err != nil {
		t.Fatal(err)
	}
	if journal.Depth() != 0 || len(batches) != 2 || batches[0] != 64 || batches[1] != 1 || completions != 1 {
		t.Fatalf("depth=%d batches=%v completions=%d", journal.Depth(), batches, completions)
	}
}

func TestLeaseRenewerRetriesTransientFailureWithoutCancelingAuthority(t *testing.T) {
	lease := replayTestLease()
	supervisor := newAttemptSupervisor(lease)
	defer supervisor.Close()
	var mu sync.Mutex
	renewCalls := 0
	recovered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/runners/renew":
			mu.Lock()
			renewCalls++
			current := renewCalls
			mu.Unlock()
			if current == 1 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			select {
			case <-recovered:
			default:
				close(recovered)
			}
			renewed := lease
			renewed.ExpiresAt = time.Now().Add(time.Minute)
			_ = json.NewEncoder(w).Encode(renewed)
		case "/api/v1/runners/heartbeat":
			_ = json.NewEncoder(w).Encode(domain.Runner{ID: lease.RunnerID})
		}
	}))
	defer server.Close()
	renewer := startLeaseRenewer(supervisor.Context(), supervisor, server.URL, "credential", lease, time.Nanosecond)
	select {
	case <-recovered:
	case <-time.After(2 * time.Second):
		t.Fatal("transient renewal did not recover")
	}
	renewer.Stop()
	if supervisor.Context().Err() != nil {
		t.Fatalf("transient retry canceled authority: %v", supervisor.Context().Err())
	}
}

func openReplayTestJournal(t *testing.T) *runner.AttemptJournal {
	t.Helper()
	journal, err := runner.OpenAttemptJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	return journal
}

const journalStateFilenameForTest = "journal.json"

func createPendingRunnerStartup(t *testing.T, lease domain.RunLease) (string, string, []byte) {
	t.Helper()
	journalDir := filepath.Join(t.TempDir(), "journal")
	journal, err := runner.OpenAttemptJournal(journalDir)
	if err != nil {
		t.Fatal(err)
	}
	appendReplayTestEvent(t, journal, lease)
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(journalDir, journalStateFilenameForTest))
	if err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(credentialPath, []byte("dedicated-runner-credential\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return journalDir, credentialPath, before
}

func createReplayRunnerStartup(t *testing.T, lease domain.RunLease, eventCount int, completion bool) (string, string, []byte) {
	t.Helper()
	root := t.TempDir()
	journalDir := filepath.Join(root, "journal")
	journal, err := runner.OpenAttemptJournal(journalDir)
	if err != nil {
		t.Fatal(err)
	}
	authority := journalAttemptIdentity(lease.RunID, lease, nil)
	for index := 0; index < eventCount; index++ {
		event := runner.JournalEvent{
			ID: fmt.Sprintf("event_startup_%03d", index), Attempt: authority, Sequence: index + 4,
			Stream: domain.LogStdout, Message: fmt.Sprintf("startup-event-%03d", index), CreatedAt: time.Now().UTC(),
		}
		if _, err := journal.AppendEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if completion {
		if _, err := journal.AppendCompletion(runner.JournalCompletion{ID: "completion_startup", Attempt: authority, Status: domain.RunSucceeded, CreatedAt: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(journalDir, journalStateFilenameForTest))
	if err != nil {
		t.Fatal(err)
	}
	credentialPath := filepath.Join(root, "credential")
	if err := os.WriteFile(credentialPath, []byte("dedicated-runner-credential\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return journalDir, credentialPath, before
}

func decodeRunnerEventBatch(t *testing.T, request *http.Request) runnerEventBatchRequest {
	t.Helper()
	var batch runnerEventBatchRequest
	if err := json.NewDecoder(request.Body).Decode(&batch); err != nil {
		t.Error(err)
	}
	return batch
}

func writeRunnerEventBatchAck(t *testing.T, w http.ResponseWriter, batch runnerEventBatchRequest) {
	t.Helper()
	ack := runnerEventBatchAck{Events: make([]domain.RunLog, 0, len(batch.Events))}
	for index, event := range batch.Events {
		ack.Events = append(ack.Events, domain.RunLog{
			ID: fmt.Sprintf("log-startup-%d", index), RunID: batch.RunID, LeaseID: batch.LeaseID, Attempt: batch.Attempt,
			EventKey: event.EventKey, Sequence: event.Sequence, RequestedSequence: event.Sequence, Stream: event.Stream, Message: event.Message,
		})
	}
	if err := json.NewEncoder(w).Encode(ack); err != nil {
		t.Error(err)
	}
}

func appendReplayTestEvent(t *testing.T, journal *runner.AttemptJournal, lease domain.RunLease) {
	t.Helper()
	authority := journalAttemptIdentity(lease.RunID, lease, nil)
	if _, err := journal.AppendEvent(runner.JournalEvent{ID: "event_startup_pending", Attempt: authority, Sequence: 4, Stream: domain.LogSystem, Message: "pending", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
}

func replayTestLease() domain.RunLease {
	now := time.Now().UTC()
	return domain.RunLease{ID: "lease_replay", RunID: "run_replay", RunnerID: "runner_replay", Attempt: 1, Fence: "opaque_fence", Status: domain.LeaseActive, CreatedAt: now, ExpiresAt: now.Add(30 * time.Second)}
}

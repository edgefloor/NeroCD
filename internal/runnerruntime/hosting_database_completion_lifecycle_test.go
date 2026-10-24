//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

// A terminal lease is published only after the completion transaction commits.
// The ACK waits for the real authority workers to join. No sleeps or elapsed-time
// assumption determine whether their terminal observation wins this boundary.
func TestDatabaseCompletionDoesNotCancelItsOwnAcknowledgement(t *testing.T) {
	for _, worker := range []string{"watcher", "renewer"} {
		t.Run(worker, func(t *testing.T) {
			f, j, _, c, e := databaseCoordinatorFixture(t, "postgresql")
			parent, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			supervisor := newAttemptSupervisorFrom(parent, c.lease)
			defer supervisor.Close()
			entered, committed := make(chan struct{}), make(chan struct{})
			var enteredOnce sync.Once
			var watcher *leaseWatcher
			var renewer *leaseRenewer
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/runners/lease", "/api/v1/runners/renew":
					enteredOnce.Do(func() { close(entered) })
					select {
					case <-r.Context().Done():
						return
					case <-committed:
					}
					if r.URL.Path == "/api/v1/runners/renew" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					terminal := c.lease
					terminal.Status = "succeeded"
					if err := json.NewEncoder(w).Encode(terminal); err != nil {
						t.Error(err)
					}
				case "/api/v1/runners/heartbeat":
					if err := json.NewEncoder(w).Encode(domain.Runner{ID: c.lease.RunnerID}); err != nil {
						t.Error(err)
					}
				case "/api/v1/runners/hosting/complete":
					rec := httptest.NewRecorder()
					c.serve(rec, r)
					if rec.Code != http.StatusOK {
						t.Error("completion did not commit")
						w.WriteHeader(rec.Code)
						return
					}
					close(committed)
					<-watcher.done
					<-renewer.done
					// A canceled client may have left; do not leak or stringify its error.
					_, _ = w.Write(rec.Body.Bytes())
				default:
					c.serve(w, r)
				}
			}))
			defer server.Close()
			watchInterval, renewInterval := time.Hour, time.Hour
			if worker == "watcher" {
				watchInterval = time.Nanosecond
			} else {
				renewInterval = time.Nanosecond
			}
			watcher = startLeaseWatcher(supervisor.Context(), server.URL, "fixture_token", c.lease, watchInterval, supervisor.cancel)
			defer watcher.Stop()
			renewer = startLeaseRenewer(supervisor.Context(), supervisor, server.URL, "fixture_token", c.lease, renewInterval)
			defer renewer.Stop()
			select {
			case <-entered:
			case <-parent.Done():
				t.Fatal("authority worker did not enter handshake")
			}
			reporter := startAttemptReporter(supervisor.Context(), supervisor, j, server.URL, "fixture_token", c.a.RunID, c.lease)
			defer reporter.Stop()
			e.beforeApply = func() {
				select {
				case <-watcher.done:
					t.Error("watcher stopped during mutation")
				default:
				}
				select {
				case <-renewer.done:
					t.Error("renewer stopped during mutation")
				default:
				}
				if c.hasPhase("result") {
					t.Error("result preceded engine effects")
				}
			}
			err := executeDatabasePrepared(supervisor, j, databaseCompletionDrain(supervisor, reporter, watcher, renewer), runnerclient.New(server.URL, "fixture_token"), e, c.a, c.lease, c.op, DatabaseExecutionConfig{TrustedLoopbackHTTP: true}, f.config)
			if err != nil || supervisor.Context().Err() != nil {
				t.Fatalf("own committed completion canceled its ACK: result_error=%t supervisor_canceled=%t", err != nil, supervisor.Context().Err() != nil)
			}
			if c.completeCalls != 1 || f.creates != 1 || f.starts != 1 || len(j.Snapshot().Hosting) != 0 {
				t.Fatal("exact settled completion was duplicated or not retired")
			}
		})
	}
}

func TestDatabaseCompletionDrainLostACKRestartReplaysExactResult(t *testing.T) {
	f, j, dir, c, e := databaseCoordinatorFixture(t, "redis")
	c.lostComplete = true
	var bodies [][]byte
	var bodyMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/hosting/complete":
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error("completion request unavailable")
				return
			}
			bodyMu.Lock()
			bodies = append(bodies, append([]byte(nil), raw...))
			bodyMu.Unlock()
			r.Body = io.NopCloser(bytes.NewReader(raw))
			c.serve(w, r)
		default:
			c.serve(w, r)
		}
	}))
	defer server.Close()
	s := newAttemptSupervisorFrom(t.Context(), c.lease)
	defer s.Close()
	watcher := startLeaseWatcher(s.Context(), server.URL, "fixture_token", c.lease, time.Hour, s.cancel)
	defer watcher.Stop()
	renewer := startLeaseRenewer(s.Context(), s, server.URL, "fixture_token", c.lease, time.Hour)
	defer renewer.Stop()
	reporter := startAttemptReporter(s.Context(), s, j, server.URL, "fixture_token", c.a.RunID, c.lease)
	defer reporter.Stop()
	if err := executeDatabasePrepared(s, j, databaseCompletionDrain(s, reporter, watcher, renewer), runnerclient.New(server.URL, "fixture_token"), e, c.a, c.lease, c.op, DatabaseExecutionConfig{TrustedLoopbackHTTP: true}, f.config); err == nil {
		t.Fatal("unavailable committed ACK reported success")
	}
	select {
	case <-watcher.done:
	default:
		t.Fatal("watcher not joined before completion")
	}
	select {
	case <-renewer.done:
	default:
		t.Fatal("renewer not joined before completion")
	}
	if s.Context().Err() != nil || len(j.Snapshot().Hosting) == 0 {
		t.Fatal("orderly drain canceled supervisor or erased unresolved receipt")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := runner.OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Error(err)
		}
	}()
	c.journal = reopened
	c.lostComplete = false
	creates, starts, credentials := f.creates, f.starts, c.credentials
	if err := reconcileDatabaseJournalWithEngine(t.Context(), databaseReplayConfig(server.URL, c), reopened, func() (databaseExecutionEngine, DatabaseEngineConfig, error) {
		t.Fatal("result replay opened an engine")
		return nil, DatabaseEngineConfig{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	bodyMu.Lock()
	defer bodyMu.Unlock()
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) || c.completeCalls != 2 || len(reopened.Snapshot().Hosting) != 0 || f.creates != creates || f.starts != starts || c.credentials != credentials {
		t.Fatal("restart changed exact result or repeated engine/credential work")
	}
}

func TestDatabaseCompletionDrainCancellationRetainsResult(t *testing.T) {
	_, j, _, c, _ := databaseCoordinatorFixture(t, "redis")
	s := newAttemptSupervisorFrom(t.Context(), c.lease)
	defer s.Close()
	completeCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { completeCalls++ }))
	defer server.Close()
	watcher := startLeaseWatcher(s.Context(), server.URL, "fixture_token", c.lease, time.Hour, s.cancel)
	defer watcher.Stop()
	renewer := startLeaseRenewer(s.Context(), s, server.URL, "fixture_token", c.lease, time.Hour)
	defer renewer.Stop()
	reporter := startAttemptReporter(s.Context(), s, j, server.URL, "fixture_token", c.a.RunID, c.lease)
	defer reporter.Stop()
	drain := func(ctx context.Context) error {
		s.cancel()
		return databaseCompletionDrain(s, reporter, watcher, renewer)(ctx)
	}
	if _, err := journalDatabaseResult(s, j, drain, runnerclient.New(server.URL, "fixture_token"), c.a, c.lease, c.op, domain.HostingResult{Outcome: "failed"}); err == nil {
		t.Fatal("canceled completion published")
	}
	if completeCalls != 0 || !c.hasPhase("result") {
		t.Fatal("cancellation sent completion or lost durable result")
	}
}

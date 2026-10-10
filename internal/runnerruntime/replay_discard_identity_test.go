package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func TestReconcileRunnerJournalFencedCleanupRetainsLaterUnprovenFullIdentityAfterReopen(t *testing.T) {
	for _, tc := range []struct {
		name  string
		older runner.AttemptIdentity
		later runner.AttemptIdentity
	}{
		{
			name:  "run_id_only",
			older: runner.AttemptIdentity{RunID: "run_older", LeaseID: "lease_shared", Attempt: 4, Fence: "fence_shared"},
			later: runner.AttemptIdentity{RunID: "run_later", LeaseID: "lease_shared", Attempt: 4, Fence: "fence_shared"},
		},
		{
			name:  "fence_only",
			older: runner.AttemptIdentity{RunID: "run_shared", LeaseID: "lease_shared", Attempt: 4, Fence: "fence_older"},
			later: runner.AttemptIdentity{RunID: "run_shared", LeaseID: "lease_shared", Attempt: 4, Fence: "fence_later"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC()
			older, later := tc.older, tc.later
			older.CreatedAt, later.CreatedAt = now, now.Add(time.Second)
			older.ExpiresAt, later.ExpiresAt = now.Add(time.Minute), now.Add(time.Minute)
			journalDir := filepath.Join(t.TempDir(), "journal")
			openJournal := func() (*runner.AttemptJournal, func()) {
				journal, err := runner.OpenAttemptJournal(journalDir)
				if err != nil {
					t.Fatal(err)
				}
				closed := false
				closeJournal := func() {
					if closed {
						return
					}
					if err := journal.Close(); err != nil {
						t.Fatal(err)
					}
					closed = true
				}
				t.Cleanup(func() {
					if !closed {
						_ = journal.Close()
					}
				})
				return journal, closeJournal
			}

			journal, closeJournal := openJournal()
			for _, event := range []runner.JournalEvent{
				{ID: "event_older", Attempt: older, Sequence: 1, Stream: domain.LogSystem, Message: "older", CreatedAt: older.CreatedAt},
				{ID: "event_later", Attempt: later, Sequence: 2, Stream: domain.LogSystem, Message: "later", CreatedAt: later.CreatedAt},
			} {
				if _, err := journal.AppendEvent(event); err != nil {
					t.Fatal(err)
				}
			}
			closeJournal()
			journal, closeJournal = openJournal()

			var mu sync.Mutex
			probes := []string{}
			// The response sequence is synthetic: the first ordered probe proves only
			// older authority is fenced, then the later probe fails transiently. This
			// contract-hardening fixture does not claim that sequence is production-reachable.
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/api/v1/runners/lease" {
					t.Errorf("unexpected request %s", request.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				query := request.URL.Query()
				mu.Lock()
				index := len(probes)
				probes = append(probes, request.URL.RawQuery)
				mu.Unlock()
				expected := older
				if index == 1 {
					expected = later
				}
				if query.Get("lease_id") != expected.LeaseID || query.Get("attempt") != fmt.Sprint(expected.Attempt) || query.Get("fence") != expected.Fence {
					t.Errorf("probe %d query=%s, want lease_id=%q attempt=%d fence=%q", index, request.URL.RawQuery, expected.LeaseID, expected.Attempt, expected.Fence)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if index == 0 {
					w.WriteHeader(http.StatusNotFound)
					_, _ = w.Write([]byte(`{"code":"not_found","error":"record not found"}`))
					return
				}
				if index == 1 {
					w.WriteHeader(http.StatusBadGateway)
					_, _ = w.Write([]byte(`{"code":"temporary_unavailable"}`))
					return
				}
				t.Errorf("unexpected extra authority probe %d", index)
				w.WriteHeader(http.StatusBadRequest)
			}))
			defer server.Close()

			err := reconcileRunnerJournal(server.URL, "credential", journal)
			if err == nil {
				t.Fatal("reconciliation succeeded despite later unproven authority")
			}
			var apiErr *runnerAPIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusBadGateway {
				t.Fatalf("reconciliation error=%v, want later transient authority probe error", err)
			}
			mu.Lock()
			probeCount := len(probes)
			mu.Unlock()
			if probeCount != 2 {
				t.Fatalf("authority probe count=%d, want older then later", probeCount)
			}
			closeJournal()
			journal, closeJournal = openJournal()

			snapshot := journal.Snapshot()
			if len(snapshot.Events) != 1 || snapshot.Events[0].Attempt != later {
				t.Fatalf("retained events=%#v, want later full identity=%#v", snapshot.Events, later)
			}
			closeJournal()
		})
	}
}

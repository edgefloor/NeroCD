package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func TestHostingLogDrainFailureRetainsKnownResultAndApplyIntent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "journal")
	journal, err := runner.OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	lease := domain.RunLease{ID: "lease_image", RunID: "run_image", RunnerID: "runner_image", Attempt: 1, Fence: "fence_image", CreatedAt: now, ExpiresAt: now.Add(time.Minute), Status: domain.LeaseActive}
	supervisor := newAttemptSupervisor(lease)
	defer supervisor.Close()
	a := hostingAuthority(lease.RunID, "hop_image", lease)
	plan := imageTestPlan(t)
	intent := runner.JournalHosting{ID: "intent_image", Attempt: journalAttemptIdentity(lease.RunID, lease, supervisor), OperationID: a.OperationID, Phase: "apply_intent", Plan: &plan, CreatedAt: now}
	if _, err = journal.AppendHosting(intent); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	result := domain.HostingResult{Outcome: "succeeded", ContainerID: "container_owned", PreviousContainerID: "container_previous", ImageID: plan.Release.ImageID, DaemonID: plan.Release.DaemonID, PlanHash: plan.Hash, Ready: true}
	drainErr := errors.New("event acknowledgement unavailable")
	_, err = journalHostingResult(supervisor, journal, runnerclient.New(server.URL, "token"), a, lease, result, func(context.Context) error {
		snapshot := journal.Snapshot()
		if len(snapshot.Hosting) != 2 || snapshot.Hosting[1].Result == nil || snapshot.Hosting[1].Result.Result != result {
			t.Fatal("result was not durable before log delivery")
		}
		return drainErr
	})
	if !errors.Is(err, drainErr) || requests != 0 {
		t.Fatalf("completion preceded log acknowledgement: requests=%d err=%v", requests, err)
	}
	before := journal.Snapshot().Hosting
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = runner.OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	if !reflect.DeepEqual(journal.Snapshot().Hosting, before) {
		t.Fatal("restart changed known result or unacknowledged mutation intent")
	}
}

func TestHostingRestartRetainsUncertainIntentBeforeHeartbeatOrClaim(t *testing.T) {
	for _, restored := range []bool{false, true} {
		t.Run(fmt.Sprint(restored), func(t *testing.T) { testHostingRestartRetainsIntent(t, restored) })
	}
}

func testHostingRestartRetainsIntent(t *testing.T, restored bool) {
	dir := filepath.Join(t.TempDir(), "journal")
	journal, err := runner.OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	a := runner.AttemptIdentity{RunID: "run_image", LeaseID: "lease_image", Attempt: 1, Fence: "fence_image", CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(-time.Second)}
	plan := imageTestPlan(t)
	intent := runner.JournalHosting{ID: "intent_image", Attempt: a, OperationID: "hop_image", Phase: "apply_intent", Plan: &plan, CreatedAt: now}
	if _, err = journal.AppendHosting(intent); err != nil {
		t.Fatal(err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path)
		if r.URL.Path != "/api/v1/runners/hosting/status" {
			t.Error("startup contacted ordinary lifecycle before reconciling mutation intent")
		}
		_ = json.NewEncoder(w).Encode(domain.HostingOperation{ID: "hop_image", Plan: &plan, RestoreOwnershipHeld: restored, MutationHold: !restored, State: "reconciliation_required"})
	}))
	defer server.Close()
	if err = Run(Config{Server: server.URL, Token: "runner_token", RunnerID: "runner_image", JournalDir: dir, WorkDir: t.TempDir(), Execute: true, Once: true}); err == nil {
		t.Fatal("unknown external effects were silently discarded")
	}
	if len(requests) != 1 {
		t.Fatalf("unexpected startup requests: %v", requests)
	}
	journal, err = runner.OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	if len(journal.Snapshot().Hosting) != 1 || journal.Snapshot().Hosting[0].ID != intent.ID {
		t.Fatal("restart lost uncertain intent")
	}
}

func TestHostingResultReplayRejectsCompletionBeforePendingEvents(t *testing.T) {
	journal, err := runner.OpenAttemptJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	now := time.Now().UTC()
	lease := domain.RunLease{ID: "lease_image", RunID: "run_image", RunnerID: "runner_image", Attempt: 1, Fence: "fence_image", CreatedAt: now, ExpiresAt: now.Add(time.Minute), Status: domain.LeaseActive}
	a := runner.AttemptIdentity{RunID: lease.RunID, LeaseID: lease.ID, Attempt: lease.Attempt, Fence: lease.Fence, CreatedAt: now, ExpiresAt: lease.ExpiresAt}
	if _, err = journal.AppendEvent(runner.JournalEvent{ID: "event_image", Attempt: a, Sequence: 1, Stream: "system", Message: "prepared", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	request := domain.HostingCompleteRequest{HostingAuthority: hostingAuthority(lease.RunID, "hop_image", lease), ReceiptID: "receipt_result", Result: domain.HostingResult{Outcome: "failed"}}
	if _, err = journal.AppendHosting(runner.JournalHosting{ID: request.ReceiptID, Attempt: a, OperationID: "hop_image", Phase: "result", Result: &request, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	completed := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/lease", "/api/v1/runners/renew":
			_ = json.NewEncoder(w).Encode(lease)
		case "/api/v1/runners/events/batch":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"forbidden","error":"denied"}`))
		case "/api/v1/runners/hosting/complete":
			completed = true
			t.Error("completion preceded event acknowledgement")
		default:
			t.Errorf("unexpected replay request: %s", r.URL.Path)
		}
	}))
	defer server.Close()
	if err = reconcileHostingJournal(server.URL, "runner_token", journal, ""); err == nil || completed {
		t.Fatalf("replay ignored event failure: completed=%v err=%v", completed, err)
	}
	if len(journal.Snapshot().Hosting) != 1 || len(journal.Snapshot().Events) != 1 {
		t.Fatal("failed replay discarded durable evidence")
	}
}

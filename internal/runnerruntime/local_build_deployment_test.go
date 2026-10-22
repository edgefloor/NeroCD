package runnerruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
)

func TestLocalBuildReplayRetainsRestoreAndCancellationHolds(t *testing.T) {
	for _, restored := range []bool{false, true} {
		journal, err := runner.OpenAttemptJournal(filepath.Join(t.TempDir(), "journal"))
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		a := runner.AttemptIdentity{RunID: "run_op", LeaseID: "lease", Attempt: 1, Fence: "fence", CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(-time.Second)}
		plan := localEnginePlan(t)
		entry := runner.JournalHosting{ID: "intent", Attempt: a, OperationID: "op", Phase: "apply_intent", Plan: &plan, CreatedAt: now}
		if _, err = journal.AppendHosting(entry); err != nil {
			t.Fatal(err)
		}
		var calls atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.URL.Path != "/api/v1/runners/hosting/status" {
				t.Error("replay escaped read-only status")
			}
			_ = json.NewEncoder(w).Encode(domain.HostingOperation{ID: "op", Snapshot: plan.Snapshot, Plan: &plan, RestoreOwnershipHeld: restored, MutationHold: !restored, State: "reconciliation_required"})
		}))
		if reconcileHostingJournal(server.URL, "fixture", journal, "") == nil || calls.Load() != 0 {
			t.Fatal("legacy wrapper replayed local evidence")
		}
		if reconcileHostingJournalWithLocalBuild(server.URL, "fixture", journal, "", true) == nil || len(journal.Snapshot().Hosting) != 1 {
			t.Fatal("uncertain/restored local intent lost")
		}
		server.Close()
		if err = journal.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLocalBuildPreparedSnapshotAllowsOnlyPortAllocation(t *testing.T) {
	before := localEnginePlan(t).Snapshot
	before.HostPort = 0
	after := before
	after.HostPort = 20001
	if !hostingPreparedSnapshotMatches(before, after) {
		t.Fatal("server port allocation rejected")
	}
	after.LocalBuild.Artifact.BuildID = "substituted"
	if hostingPreparedSnapshotMatches(before, after) {
		t.Fatal("artifact substitution accepted during allocation")
	}
	after = before
	after.HostPort = 20001
	after.RunnerID = "different"
	if hostingPreparedSnapshotMatches(before, after) {
		t.Fatal("placement changed during allocation")
	}
	before.HostPort = 20000
	after = before
	after.HostPort = 20001
	if hostingPreparedSnapshotMatches(before, after) {
		t.Fatal("existing port reassigned")
	}
	for _, port := range []int{1023, 65536} {
		before.HostPort = 0
		after = before
		after.HostPort = port
		if hostingPreparedSnapshotMatches(before, after) {
			t.Fatal("invalid allocated port accepted")
		}
	}
}

func TestLocalBuildPreparationReceiptDoesNotRequirePlanReadAnnotation(t *testing.T) {
	p := localEnginePlan(t)
	a := domain.HostingAuthority{OperationID: "op", RunID: "run_op"}
	op := domain.HostingOperation{ID: a.OperationID, RunID: a.RunID, BindingID: p.Snapshot.BindingID, Snapshot: p.Snapshot, Release: &p.Release, Plan: &p}
	if validateHostingPreparedResponse(op, a, p.Snapshot, p.Release) != nil {
		t.Fatal("valid receipt without TaskMode rejected")
	}
	op.Plan.Hash = "sha256:changed"
	if validateHostingPreparedResponse(op, a, p.Snapshot, p.Release) == nil {
		t.Fatal("changed compiled plan hash accepted")
	}
}

func TestLocalBuildClaimDeniesGenericAndMalformedTasksBeforeRequests(t *testing.T) {
	for _, mode := range []string{"disabled", "process", "wrong-run", "primitive", "nested"} {
		t.Run(mode, func(t *testing.T) {
			now := time.Now()
			claim := domain.ClaimedRun{Run: domain.TaskRun{ID: "run_op", RunSpec: domain.RunSpec{Type: domain.RunTypeLocalBuildDeploy, HostingOperationID: "op", HostingMode: "deploy"}}, Lease: domain.RunLease{ID: "lease", RunID: "run_op", RunnerID: "runner", Attempt: 1, Fence: "fence", Status: "active", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, PrimitivePlan: domain.RunnerPrimitivePlan{RunID: "run_op"}}
			switch mode {
			case "process":
				claim.Run.RunSpec.Process = &domain.ProcessSpec{Command: []string{"forbidden"}}
			case "wrong-run":
				claim.Run.ID = "wrong"
			case "primitive":
				claim.PrimitivePlan.Process = &domain.ProcessSpec{}
			case "nested":
				claim.Run.RunSpec.Workflow = &domain.Workflow{}
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(500) }))
			defer server.Close()
			err := executeClaimWithOptions(server.URL, "fixture", claim, t.TempDir(), time.Millisecond, nil, "", composeImagePolicy("preloaded"), nil, claimExecutionOptions{localBuild: mode != "disabled"})
			if err == nil || calls.Load() != 0 {
				t.Fatal("malformed local task reached generic/process/request path")
			}
		})
	}
}

func TestLocalBuildDirectRunCapabilityMismatchPrecedesHeartbeat(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		var effects atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/v1/runners/identity" {
				effects.Add(1)
				w.WriteHeader(500)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": []string{domain.RunTypeLocalBuildDeploy, "shell"}})
		}))
		err := Run(Config{LocalBuildDeploy: enabled, Server: server.URL, Token: "fixture", RunnerID: "runner", Execute: true, Once: true, WorkDir: t.TempDir()})
		server.Close()
		if err == nil || effects.Load() != 0 {
			t.Fatal("mismatched local configuration reached heartbeat/claim")
		}
	}
}

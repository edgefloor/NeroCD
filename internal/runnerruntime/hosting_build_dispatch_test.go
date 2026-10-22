package runnerruntime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingbuild"
	"nerocd/internal/runner"
)

func buildDispatchClaim(a domain.HostingBuildAuthority) domain.ClaimedRun {
	now := time.Now().UTC()
	return domain.ClaimedRun{Run: domain.TaskRun{ID: a.RunID, RunSpec: domain.RunSpec{Type: domain.HostingBuildTaskType, HostingBuildID: a.BuildID}}, Lease: domain.RunLease{ID: a.LeaseID, RunID: a.RunID, RunnerID: "runner", Attempt: a.Attempt, Fence: a.Fence, Status: "active", CreatedAt: now, ExpiresAt: now.Add(5 * time.Second)}, PrimitivePlan: domain.RunnerPrimitivePlan{RunID: a.RunID}}
}
func TestBuildDispatchRenewsBeyondInitialExpiryWithoutGenericTraffic(t *testing.T) {
	e, _, a, _ := buildFixture(t)
	e.config.Policy.Limits.Deadline = 8 * time.Second
	e.engine = buildEngineFunc(func(c context.Context, r hostingbuild.Request) (hostingbuild.Result, error) {
		select {
		case <-time.After(5500 * time.Millisecond):
			return fixtureBuildResult(r), nil
		case <-c.Done():
			return hostingbuild.Result{}, c.Err()
		}
	})
	claim := buildDispatchClaim(a)
	initial := claim.Lease.ExpiresAt
	var renewals atomic.Int32
	var generic atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lease := claim.Lease
		switch r.URL.Path {
		case "/api/v1/runners/renew":
			renewals.Add(1)
			lease.ExpiresAt = time.Now().Add(5 * time.Second)
			_ = json.NewEncoder(w).Encode(lease)
		case "/api/v1/runners/lease":
			lease.ExpiresAt = time.Now().Add(5 * time.Second)
			_ = json.NewEncoder(w).Encode(lease)
		case "/api/v1/runners/heartbeat":
			_ = json.NewEncoder(w).Encode(domain.Runner{ID: "runner"})
		default:
			generic.Add(1)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	// Keep the fixture's persisted snapshot/policy consistent with the bounded
	// execution duration selected above.
	f := e.client.(*buildFixtureClient)
	f.build.Snapshot.Policy = e.config.Policy
	s := f.build.Snapshot
	s.InputID = ""
	f.build.Snapshot.InputID = domain.HostingDocumentHash(s)
	if err := executeBuildClaim(t.Context(), server.URL, "fixture", claim, "runner", 20*time.Millisecond, e, &runnerOperationalCounters{}); err != nil {
		t.Fatal(err)
	}
	if time.Now().Before(initial) || renewals.Load() < 2 || generic.Load() != 0 {
		t.Fatal("renewed execution entered generic traffic or did not cross initial expiry")
	}
}
func TestBuildDispatchCanonicalDenialBeforeAnyRequest(t *testing.T) {
	for _, mode := range []string{"type", "id", "process", "nested", "primitive", "runner", "fence"} {
		t.Run(mode, func(t *testing.T) {
			e, _, a, _ := buildFixture(t)
			claim := buildDispatchClaim(a)
			switch mode {
			case "type":
				claim.Run.RunSpec.Type = "shell"
			case "id":
				claim.Run.ID = "other"
			case "process":
				claim.Run.RunSpec.Process = &domain.ProcessSpec{Command: []string{"must-not-run"}}
			case "nested":
				claim.Run.RunSpec.Workflow = &domain.Workflow{}
			case "primitive":
				claim.PrimitivePlan.RunID = "other"
			case "runner":
				claim.Lease.RunnerID = "other"
			case "fence":
				claim.Lease.Fence = ""
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(500) }))
			defer server.Close()
			if executeBuildClaim(t.Context(), server.URL, "fixture", claim, "runner", time.Millisecond, e, &runnerOperationalCounters{}) == nil || calls.Load() != 0 {
				t.Fatal("invalid build reached authority/process path")
			}
		})
	}
}
func TestBuildDirectRunDeniesPersistedCapabilityMismatchBeforeHeartbeat(t *testing.T) {
	var effects atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/runners/identity" {
			effects.Add(1)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": "runner", "capabilities": []string{"shell", domain.HostingBuildTaskType}})
	}))
	defer server.Close()
	if Run(Config{Server: server.URL, Token: "fixture", RunnerID: "runner", WorkDir: t.TempDir(), Execute: true, Once: true}) == nil || effects.Load() != 0 {
		t.Fatal("unconfigured build identity reached heartbeat/claim")
	}
}
func TestBuildMissingConfigRetainsJournal(t *testing.T) {
	e, _, a, at := buildFixture(t)
	entry := runner.JournalBuild{ID: "intent", BuildID: a.BuildID, Attempt: at, Phase: "attempt_intent", InputHash: domain.HostingDocumentHash("snapshot"), CreatedAt: time.Now()}
	if _, err := e.journal.AppendBuild(entry); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcileRuntimeBuilds(t.Context(), Config{}, e.journal); err == nil || len(e.journal.Snapshot().Build) != 1 {
		t.Fatal("unconfigured startup discarded build evidence")
	}
}

func TestBuildClaimCannotUseExecuteFalseOrGenericCompletion(t *testing.T) {
	for _, status := range []string{"", "succeeded", "failed", "canceled"} {
		t.Run(status, func(t *testing.T) {
			_, _, a, _ := buildFixture(t)
			claim := buildDispatchClaim(a)
			var generic atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var out any = map[string]any{}
				switch r.URL.Path {
				case "/api/v1/runners/identity":
					out = map[string]any{"runner_id": "runner", "capabilities": []string{"shell"}}
				case "/api/v1/runners/heartbeat", "/api/v1/runners/telemetry":
				case "/api/v1/runners/claim":
					out = claim
				default:
					generic.Add(1)
					w.WriteHeader(http.StatusConflict)
					return
				}
				_ = json.NewEncoder(w).Encode(out)
			}))
			defer server.Close()
			if Run(Config{Server: server.URL, Token: "fixture", RunnerID: "runner", WorkDir: t.TempDir(), Execute: false, Once: true, CompleteStatus: status}) == nil || generic.Load() != 0 {
				t.Fatal("build entered generic completion path")
			}
		})
	}
}
func TestBuildLeaseLossOrChangedTupleJoins(t *testing.T) {
	for _, mode := range []string{"lost", "changed"} {
		t.Run(mode, func(t *testing.T) {
			e, _, a, _ := buildFixture(t)
			claim := buildDispatchClaim(a)
			started := make(chan struct{})
			joined := make(chan struct{})
			e.engine = buildEngineFunc(func(c context.Context, _ hostingbuild.Request) (hostingbuild.Result, error) {
				close(started)
				<-c.Done()
				close(joined)
				return hostingbuild.Result{}, c.Err()
			})
			var polls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				lease := claim.Lease
				switch r.URL.Path {
				case "/api/v1/runners/renew":
					lease.ExpiresAt = time.Now().Add(5 * time.Second)
				case "/api/v1/runners/lease":
					<-started
					if mode == "lost" {
						w.WriteHeader(http.StatusForbidden)
						return
					}
					lease.Fence = "changed"
					polls.Add(1)
				case "/api/v1/runners/heartbeat":
					_ = json.NewEncoder(w).Encode(domain.Runner{ID: "runner"})
					return
				default:
					t.Errorf("unexpected build lifecycle request %s", r.URL.Path)
				}
				_ = json.NewEncoder(w).Encode(lease)
			}))
			defer server.Close()
			if executeBuildClaim(t.Context(), server.URL, "fixture", claim, "runner", 10*time.Millisecond, e, &runnerOperationalCounters{}) == nil {
				t.Fatal("lost authority succeeded")
			}
			select {
			case <-joined:
			default:
				t.Fatal("builder outlived dispatcher")
			}
			if len(e.journal.Snapshot().Build) == 0 {
				t.Fatal("lost authority erased intent")
			}
		})
	}
}

func TestBuildDispatcherParentCancellationJoins(t *testing.T) {
	e, _, a, _ := buildFixture(t)
	claim := buildDispatchClaim(a)
	started := make(chan struct{})
	joined := make(chan struct{})
	e.engine = buildEngineFunc(func(c context.Context, _ hostingbuild.Request) (hostingbuild.Result, error) {
		close(started)
		<-c.Done()
		close(joined)
		return hostingbuild.Result{}, c.Err()
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lease := claim.Lease
		lease.ExpiresAt = time.Now().Add(5 * time.Second)
		_ = json.NewEncoder(w).Encode(lease)
	}))
	defer server.Close()
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- executeBuildClaim(parent, server.URL, "fixture", claim, "runner", time.Second, e, &runnerOperationalCounters{})
	}()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("parent cancellation succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("parent cancellation failed to join")
	}
	select {
	case <-joined:
	default:
		t.Fatal("engine outlived parent")
	}
	if len(e.journal.Snapshot().Build) == 0 {
		t.Fatal("cancellation erased intent")
	}
}
func TestRunContextCanceledReplayPreservesEveryFamily(t *testing.T) {
	for _, family := range []string{"generic", "image", "proxy"} {
		t.Run(family, func(t *testing.T) {
			e, _, a, at := buildFixture(t)
			journal := e.journal
			var reconcile func(context.Context, string) error
			switch family {
			case "generic":
				if _, err := journal.AppendCompletion(runner.JournalCompletion{ID: "pending", Attempt: at, Status: "failed", CreatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
				reconcile = func(c context.Context, url string) error {
					return reconcileRunnerJournalWithCounters(url, "fixture", journal, &runnerOperationalCounters{}, c)
				}
			case "image":
				plan := imageTestPlan(t)
				if _, err := journal.AppendHosting(runner.JournalHosting{ID: "image-intent", Attempt: at, OperationID: "image-op", Phase: "apply_intent", Plan: &plan, CreatedAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
				reconcile = func(c context.Context, url string) error {
					return reconcileHostingJournal(url, "fixture", journal, "", c)
				}
			case "proxy":
				claim, op, proxyJournal, config, _ := proxyRuntimeFixture(t)
				journal = proxyJournal
				supervisor := newAttemptSupervisor(claim.Lease)
				defer supervisor.Close()
				_, _, preparation, err := prepareProxyEngine(t.Context(), config, *op.Plan, nil)
				if err != nil {
					t.Fatal(err)
				}
				entry := proxyJournalEntry(supervisor, claim.Lease, hostingAuthority(claim.Run.ID, op.ID, claim.Lease), *op.Plan, preparation, "apply_intent")
				if _, err = journal.AppendProxy(entry); err != nil {
					t.Fatal(err)
				}
				reconcile = func(c context.Context, url string) error {
					return reconcileProxyJournal(url, "fixture", claim.Lease.RunnerID, journal, config, c)
				}
			}
			_ = a
			before, _ := json.Marshal(journal.Snapshot())
			started := make(chan struct{})
			requestJoined := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				<-r.Context().Done()
				close(requestJoined)
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- reconcile(ctx, server.URL) }()
			<-started
			cancel()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("canceled replay succeeded")
				}
			case <-time.After(time.Second):
				t.Fatal("canceled replay did not join")
			}
			select {
			case <-requestJoined:
			case <-time.After(time.Second):
				t.Fatal("replay request did not cancel")
			}
			after, _ := json.Marshal(journal.Snapshot())
			if string(before) != string(after) {
				t.Fatal("canceled replay mutated retained records")
			}
		})
	}
}

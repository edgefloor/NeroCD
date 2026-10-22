package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/hostingproxy"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

type fakeManagedProxy struct {
	settings hostingproxy.Settings
	apply    func(context.Context, hostingproxy.Plan) (hostingproxy.Result, error)
	verify   func() error
	effects  atomic.Int32
}

func TestProxyCanceledCompletionOnlyReplaysExactCommittedReceipt(t *testing.T) {
	for _, mode := range []string{"committed", "uncommitted", "wrong_receipt", "wrong_body", "changed_result", "changed_plan", "changed_preparation", "restore", "held", "wrong_tuple"} {
		t.Run(mode, func(t *testing.T) {
			claim, op, journal, config, engine := proxyRuntimeFixture(t)
			supervisor := newAttemptSupervisor(claim.Lease)
			defer supervisor.Close()
			a := hostingAuthority(claim.Run.ID, op.ID, claim.Lease)
			_, _, preparation, err := prepareProxyEngine(t.Context(), config, *op.Plan, nil)
			if err != nil {
				t.Fatal(err)
			}
			entry := proxyJournalEntry(supervisor, claim.Lease, a, *op.Plan, preparation, "result")
			entry.Result = &domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: entry.ID, Result: domain.HostingProxyResult{Outcome: "applied", LoadedGeneration: 2, PlanHash: op.Plan.Hash, EngineHash: preparation.EngineHash, DaemonID: preparation.DaemonID}}
			if _, err = journal.AppendProxy(entry); err != nil {
				t.Fatal(err)
			}
			result := entry.Result.Result
			op.Preparation = &preparation
			op.Result = &result
			op.State = "applied"
			op.CompleteReceipt = &domain.HostingReceipt{ID: entry.ID, BodyHash: domain.HostingDocumentHash(result)}
			switch mode {
			case "uncommitted":
				op.Result, op.CompleteReceipt, op.State, op.MutationHold = nil, nil, "applying", true
			case "wrong_receipt":
				op.CompleteReceipt.ID = "foreign"
			case "wrong_body":
				op.CompleteReceipt.BodyHash = "foreign"
			case "changed_result":
				op.Result.LoadedGeneration = 1
			case "changed_plan":
				op.Plan.Hash = "foreign"
			case "changed_preparation":
				changed := preparation
				changed.StaticHash = strings.Repeat("e", 64)
				op.Preparation = &changed
			case "restore":
				op.RestoreOwnershipHeld = true
			case "held":
				op.MutationHold = true
			}
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/runners/proxy/status":
					var request domain.HostingAuthority
					if json.NewDecoder(r.Body).Decode(&request) != nil || request != a {
						t.Error("status lost full tuple")
					}
					if mode == "wrong_tuple" {
						w.WriteHeader(http.StatusConflict)
						return
					}
				case "/api/v1/runners/proxy/complete":
					posts.Add(1)
					var request domain.HostingProxyCompleteRequest
					if json.NewDecoder(r.Body).Decode(&request) != nil || request.HostingAuthority != a || request.ReceiptID != entry.ID || domain.HostingDocumentHash(request.Result) != domain.HostingDocumentHash(entry.Result.Result) {
						t.Error("replay changed exact receipt")
					}
				default:
					t.Errorf("canceled recovery made authority request %s", r.URL.Path)
				}
				if err := json.NewEncoder(w).Encode(op); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			supervisor.cancel()
			_, err = replayProxyResult(runnerclient.New(server.URL, "dedicated"), entry, claim.Lease.RunnerID, false)
			want := mode == "committed"
			if (err == nil) != want || posts.Load() != int32(map[bool]int{true: 1, false: 0}[want]) || journal.Depth() != 1 || engine.effects.Load() != 0 {
				t.Fatalf("canceled replay mode=%s err=%v posts=%d depth=%d effects=%d", mode, err, posts.Load(), journal.Depth(), engine.effects.Load())
			}
		})
	}
}

func (e *fakeManagedProxy) PrepareManaged(_ context.Context, identity hostingproxy.Identity, generation, previous uint64, routes []hostingproxy.Route) (hostingproxy.Plan, string, error) {
	identity.DaemonID = "daemon_one"
	plan, err := hostingproxy.Prepare(identity, e.settings, generation, previous, routes)
	return plan, identity.DaemonID, err
}
func (e *fakeManagedProxy) Install(ctx context.Context, plan hostingproxy.Plan) (hostingproxy.Result, error) {
	e.effects.Add(1)
	return e.apply(ctx, plan)
}
func (e *fakeManagedProxy) Apply(ctx context.Context, plan hostingproxy.Plan) (hostingproxy.Result, error) {
	return e.Install(ctx, plan)
}
func (*fakeManagedProxy) Observe(context.Context, hostingproxy.Plan) (hostingproxy.Result, error) {
	return hostingproxy.Result{}, errors.New("unexpected observation")
}
func (e *fakeManagedProxy) VerifyManagedReceipt(context.Context, hostingproxy.Plan, hostingproxy.Outcome, uint64) error {
	return e.verify()
}

func proxyRuntimeFixture(t *testing.T) (domain.ClaimedRun, domain.HostingProxyOperation, *runner.AttemptJournal, ProxyConfig, *fakeManagedProxy) {
	t.Helper()
	now := time.Now().UTC()
	snapshot := domain.HostingProxySnapshot{ProxyID: "proxy_one", ServerID: "srv_one", DestinationID: "dest_one", RunnerID: "runner_one", Generation: 2, Configuration: domain.HostingProxyConfiguration{Email: "operator@nerocd.invalid", CA: "letsencrypt-staging", ManagementPort: 19080}, Routes: []domain.HostingProxyRouteTarget{{RouteID: "route_one", ResourceID: "res_one", ProjectID: "proj_one", BindingID: "bind_one", ReleaseOperationID: "hop_one", Hostname: "web.nerocd.invalid", PathPrefix: "/", HostPort: 18080}}}
	plan, err := domain.CompileHostingProxyPlan(snapshot, 1, "daemon_one")
	if err != nil {
		t.Fatal(err)
	}
	claim := domain.ClaimedRun{Run: domain.TaskRun{ID: "proxy_pop_one", RunSpec: domain.RunSpec{Type: domain.RunTypeProxyApply, ProxyMode: "apply", ProxyOperationID: "pop_one"}}, Lease: domain.RunLease{ID: "lease_one", RunID: "proxy_pop_one", RunnerID: "runner_one", Attempt: 1, Fence: "fence_one", Status: domain.LeaseActive, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}, PrimitivePlan: domain.RunnerPrimitivePlan{RunID: "proxy_pop_one"}}
	op := domain.HostingProxyOperation{ID: "pop_one", ProxyID: snapshot.ProxyID, RunID: claim.Run.ID, State: "dispatched", Snapshot: snapshot, Plan: &plan, Generation: 2}
	journal, err := runner.OpenAttemptJournal(filepath.Join(t.TempDir(), "journal"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	engine := &fakeManagedProxy{verify: func() error { return nil }}
	config := ProxyConfig{Directory: t.TempDir(), DockerBinary: "/usr/bin/docker", DockerSocket: "/var/run/docker.sock", Protocol: domain.HostingProxyCompilerVersion, engineFactory: func(settings hostingproxy.Settings) (managedProxyEngine, error) {
		engine.settings = settings
		return engine, nil
	}}
	return claim, op, journal, config, engine
}

func TestProxyExecutionDurableOrderingAndLostCompletionResponse(t *testing.T) {
	claim, op, journal, config, engine := proxyRuntimeFixture(t)
	var completions atomic.Int32
	engine.apply = func(_ context.Context, plan hostingproxy.Plan) (hostingproxy.Result, error) {
		entries := journal.Snapshot().Proxy
		if len(entries) != 1 || entries[0].Phase != "apply_intent" {
			t.Error("proxy effect preceded durable intent")
		}
		return hostingproxy.Result{Outcome: hostingproxy.Applied, DesiredGeneration: 2, LoadedGeneration: 2, Hash: plan.Hash(), ContainerID: strings.Repeat("c", 64), Routes: []hostingproxy.RouteObservation{{RouteID: "route_one", HTTPReady: true, HTTPSReady: true, Certificate: hostingproxy.Certificate{Hostname: "web.nerocd.invalid", Status: "trusted", Issuer: "fixture", Fingerprint: strings.Repeat("d", 64), ExpiresAt: time.Now().UTC().Add(time.Hour)}}}}, nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer dedicated" {
			t.Error("missing dedicated credential")
		}
		switch r.URL.Path {
		case "/api/v1/runners/proxy/plan":
		case "/api/v1/runners/proxy/prepared":
			var in domain.HostingProxyPreparedRequest
			if json.NewDecoder(r.Body).Decode(&in) != nil {
				t.Error("decode preparation")
			}
			if len(journal.Snapshot().Proxy) != 1 || journal.Snapshot().Proxy[0].Phase != "prepared" {
				t.Error("preparation sent before journal append")
			}
			op.Preparation = &in.HostingProxyPreparation
			op.PreparedReceipt = &domain.HostingReceipt{ID: in.ReceiptID, BodyHash: domain.HostingDocumentHash(in.HostingProxyPreparation)}
			op.State = "prepared"
		case "/api/v1/runners/proxy/apply-begin":
			if len(journal.Snapshot().Proxy) != 1 || journal.Snapshot().Proxy[0].Phase != "apply_intent" {
				t.Error("admission sent before intent append")
			}
			op.MutationHold = true
			op.State = "applying"
		case "/api/v1/runners/proxy/complete":
			var in domain.HostingProxyCompleteRequest
			if json.NewDecoder(r.Body).Decode(&in) != nil {
				t.Error("decode result")
			}
			if len(journal.Snapshot().Proxy) != 2 || journal.Snapshot().Proxy[1].Phase != "result" {
				t.Error("result sent before durable append")
			}
			op.Result = &in.Result
			op.CompleteReceipt = &domain.HostingReceipt{ID: in.ReceiptID, BodyHash: domain.HostingDocumentHash(in.Result)}
			op.MutationHold = false
			op.State = "applied"
			if completions.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if err := json.NewEncoder(w).Encode(op); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	supervisor := newAttemptSupervisor(claim.Lease)
	defer supervisor.Close()
	reporter := startAttemptReporter(supervisor.Context(), supervisor, journal, server.URL, "dedicated", claim.Run.ID, claim.Lease)
	defer reporter.Stop()
	if err := executeProxyClaim(supervisor, journal, reporter, server.URL, "dedicated", claim, config); err != nil {
		t.Fatal(err)
	}
	if engine.effects.Load() != 1 || completions.Load() != 2 || journal.Depth() != 0 {
		t.Fatalf("effect replay or lost acknowledgment: effects=%d completions=%d depth=%d", engine.effects.Load(), completions.Load(), journal.Depth())
	}
}

func TestProxyResponseRejectsChangedTargetAndCompiler(t *testing.T) {
	claim, original, _, _, _ := proxyRuntimeFixture(t)
	a := hostingAuthority(claim.Run.ID, original.ID, claim.Lease)
	for _, mutate := range []func(*domain.HostingProxyOperation){
		func(op *domain.HostingProxyOperation) { op.RunID = "foreign_run" },
		func(op *domain.HostingProxyOperation) { op.Snapshot.RunnerID = "foreign_runner" },
		func(op *domain.HostingProxyOperation) { op.Plan.CompilerVersion = "old.v0" },
		func(op *domain.HostingProxyOperation) { op.Plan.Hash = "foreign_hash" },
	} {
		op := original
		plan := *original.Plan
		op.Plan = &plan
		mutate(&op)
		if proxyResponseMatches(op, a, claim.Lease.RunnerID, original.Plan) == nil {
			t.Fatal("changed proxy target or protocol accepted")
		}
	}
}

func TestProxyEngineIdentityBoundsFailBeforeEffects(t *testing.T) {
	for _, size := range []int{32, 80, 81, 128} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			_, op, _, config, engine := proxyRuntimeFixture(t)
			snapshot := op.Snapshot
			snapshot.RunnerID = "runner_" + strings.Repeat("a", size-7)
			plan, err := domain.CompileHostingProxyPlan(snapshot, 1, "daemon_one")
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, err = prepareProxyEngine(t.Context(), config, plan, nil)
			if (err == nil) != (size <= 80) || engine.effects.Load() != 0 {
				t.Fatalf("size=%d err=%v effects=%d", size, err, engine.effects.Load())
			}
			if size > 80 && !strings.Contains(err.Error(), "80-byte limit") {
				t.Fatal(err)
			}
		})
	}
}

func TestProxySnapshotResourceOrderMatchesCanonicalPlan(t *testing.T) {
	claim, op, _, _, _ := proxyRuntimeFixture(t)
	second := op.Snapshot.Routes[0]
	second.RouteID, second.PathPrefix = "aaa_route", "/api"
	op.Snapshot.Routes = append(op.Snapshot.Routes, second)
	plan, err := domain.CompileHostingProxyPlan(op.Snapshot, 1, "daemon_one")
	if err != nil {
		t.Fatal(err)
	}
	op.Plan = &plan
	a := hostingAuthority(claim.Run.ID, op.ID, claim.Lease)
	if err = proxyResponseMatches(op, a, claim.Lease.RunnerID, &plan); err != nil {
		t.Fatal(err)
	}
	op.Snapshot.Routes[0].HostPort++
	if proxyResponseMatches(op, a, claim.Lease.RunnerID, &plan) == nil {
		t.Fatal("snapshot changed upstream accepted")
	}
}

func TestProxyEngineUnknownOrMismatchedEvidenceRemainsUncertain(t *testing.T) {
	_, op, _, config, _ := proxyRuntimeFixture(t)
	_, local, preparation, err := prepareProxyEngine(t.Context(), config, *op.Plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range []hostingproxy.Result{
		{Outcome: "unknown", Hash: local.Hash(), DesiredGeneration: 2},
		{Outcome: hostingproxy.Applied, Hash: "foreign", DesiredGeneration: 2, LoadedGeneration: 2},
		{Outcome: hostingproxy.Applied, Hash: local.Hash(), DesiredGeneration: 3, LoadedGeneration: 2},
	} {
		evidence := proxyResultEvidence(result, *op.Plan, preparation)
		if evidence.Outcome != "uncertain" || evidence.LoadedGeneration != 0 || len(evidence.Routes) != 0 {
			t.Fatal("mismatched engine output implied a loaded generation")
		}
	}
}

func TestProxyTaskCannotEnterUnconfiguredExecutionOrGenericCompletion(t *testing.T) {
	for _, execute := range []bool{false, true} {
		t.Run(fmt.Sprint(execute), func(t *testing.T) {
			claim, _, _, _, _ := proxyRuntimeFixture(t)
			var unexpected atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var response any = map[string]any{}
				switch r.URL.Path {
				case "/api/v1/runners/identity":
					response = map[string]any{"runner_id": claim.Lease.RunnerID}
				case "/api/v1/runners/heartbeat", "/api/v1/runners/telemetry":
				case "/api/v1/runners/claim":
					response = claim
				default:
					unexpected.Add(1)
					w.WriteHeader(409)
					return
				}
				if err := json.NewEncoder(w).Encode(response); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			work := t.TempDir()
			config := Config{Server: server.URL, Token: "dedicated", RunnerID: claim.Lease.RunnerID, WorkDir: work, JournalDir: filepath.Join(work, "journal"), Execute: execute, Once: true, CompleteStatus: "succeeded"}
			err := Run(config)
			if err == nil || unexpected.Load() != 0 {
				t.Fatalf("unconfigured proxy reached effect or generic reporting: err=%v requests=%d", err, unexpected.Load())
			}
			journal, err := runner.OpenAttemptJournal(config.JournalDir)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := journal.Close(); err != nil {
					t.Error(err)
				}
			}()
			if journal.Depth() != 0 {
				t.Fatal("proxy task created generic completion evidence")
			}
		})
	}
}

func TestProxyStartupReplayNeverRetriesEffectsOrClearsUncertainty(t *testing.T) {
	for _, mode := range []string{"committed", "canceled_before_admission", "uncommitted_expired", "uncertain", "restore", "changed_local", "missing_local_receipt"} {
		t.Run(mode, func(t *testing.T) {
			claim, op, journal, config, engine := proxyRuntimeFixture(t)
			supervisor := newAttemptSupervisor(claim.Lease)
			defer supervisor.Close()
			a := hostingAuthority(claim.Run.ID, op.ID, claim.Lease)
			_, _, preparation, err := prepareProxyEngine(t.Context(), config, *op.Plan, nil)
			if err != nil {
				t.Fatal(err)
			}
			op.Preparation = &preparation
			intent := proxyJournalEntry(supervisor, claim.Lease, a, *op.Plan, preparation, "apply_intent")
			if _, err = journal.AppendProxy(intent); err != nil {
				t.Fatal(err)
			}
			result := proxyJournalEntry(supervisor, claim.Lease, a, *op.Plan, preparation, "result")
			result.Result = &domain.HostingProxyCompleteRequest{HostingAuthority: a, ReceiptID: result.ID, Result: domain.HostingProxyResult{Outcome: "applied", LoadedGeneration: 2, PlanHash: op.Plan.Hash, EngineHash: preparation.EngineHash, DaemonID: preparation.DaemonID}}
			op.State = "applied"
			if mode == "uncertain" {
				result.Result.Result.Outcome = "uncertain"
				op.State, op.MutationHold = "uncertain", true
			}
			if mode == "canceled_before_admission" {
				op.State = "canceled"
			} else {
				if _, err = journal.AppendProxy(result); err != nil {
					t.Fatal(err)
				}
				op.Result = &result.Result.Result
				op.CompleteReceipt = &domain.HostingReceipt{ID: result.ID, BodyHash: domain.HostingDocumentHash(*op.Result)}
			}
			if mode == "restore" {
				op.RestoreOwnershipHeld = true
			}
			if mode == "changed_local" {
				config.Directory = filepath.Join(config.Directory, "different")
			}
			if mode == "missing_local_receipt" {
				engine.verify = func() error { return errors.New("local engine receipt missing") }
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/api/v1/runners/proxy/complete" && r.URL.Path != "/api/v1/runners/proxy/status" {
					t.Errorf("replay made nonreceipt request %s", r.URL.Path)
				}
				if mode == "uncommitted_expired" {
					w.WriteHeader(http.StatusConflict)
					return
				}
				if err := json.NewEncoder(w).Encode(op); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			err = reconcileProxyJournal(server.URL, "dedicated", claim.Lease.RunnerID, journal, config)
			wantSuccess := mode == "committed" || mode == "canceled_before_admission"
			if (err == nil) != wantSuccess || wantSuccess && journal.Depth() != 0 || !wantSuccess && len(journal.Snapshot().Proxy) != 2 || engine.effects.Load() != 0 {
				t.Fatalf("replay changed evidence or effects: mode=%s err=%v depth=%d effects=%d", mode, err, journal.Depth(), engine.effects.Load())
			}
			if mode == "changed_local" && requests.Load() != 0 {
				t.Fatal("changed local engine plan sent a receipt")
			}
		})
	}
}

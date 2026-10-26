//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"nerocd/internal/domain"
	"nerocd/internal/hostingproxy"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func TestApplicationStorageProxyPlansReconstructOrdinaryAAndVerifiedBAfterMaintenance(t *testing.T) {
	config := ProxyConfig{Directory: t.TempDir(), DockerBinary: "/usr/bin/docker", DockerSocket: "/var/run/docker.sock", VerifiedSwitch: true}
	snapshot := domain.HostingProxySnapshot{ProxyID: "proxy", ServerID: "server", DestinationID: "destination", RunnerID: "runner", Generation: 3, Configuration: domain.HostingProxyConfiguration{Email: "operator@nerocd.invalid", CA: "letsencrypt-staging", ManagementPort: 19080}, Routes: []domain.HostingProxyRouteTarget{{RouteID: "route", ProjectID: "project", ResourceID: "application", BindingID: "binding", ReleaseOperationID: "storage_a", Hostname: "app.example.test", PathPrefix: "/", HostPort: 21001}}}
	settings := hostingproxy.Settings{Directory: filepath.Join(config.Directory, snapshot.ProxyID), DockerBinary: config.DockerBinary, DockerSocket: config.DockerSocket, Email: snapshot.Configuration.Email, CA: hostingproxy.CA(snapshot.Configuration.CA), ManagementPort: snapshot.Configuration.ManagementPort}
	identity := hostingproxy.Identity{ProxyID: snapshot.ProxyID, ServerID: snapshot.ServerID, DestinationID: snapshot.DestinationID, RunnerID: snapshot.RunnerID, DaemonID: "daemon"}
	engineRoutes := func(s domain.HostingProxySnapshot) []hostingproxy.Route {
		var out []hostingproxy.Route
		for _, r := range s.Routes {
			out = append(out, hostingproxy.Route{ID: r.RouteID, Hostname: r.Hostname, PathPrefix: r.PathPrefix, HostPort: r.HostPort})
		}
		return out
	}
	prior, err := hostingproxy.Prepare(identity, settings, 3, 2, engineRoutes(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	storageID, _ := domain.HostingStorageIdentity("binding")
	var last domain.HostingProxyPlan
	for _, operation := range []string{"storage_b", "storage_c"} {
		before := snapshot.Routes[0]
		snapshot.Generation++
		snapshot.Routes = append([]domain.HostingProxyRouteTarget(nil), snapshot.Routes...)
		snapshot.Routes[0].ReleaseOperationID = operation
		snapshot.Routes[0].HostPort++
		snapshot.VerifiedSwitch = &domain.HostingVerifiedSwitch{Version: 1, ExpectedLoadedGeneration: snapshot.Generation - 1, PriorAppliedProxyOperationID: "prior_" + before.ReleaseOperationID, Routes: []domain.HostingVerifiedSwitchRoute{{RouteID: before.RouteID, PriorVersion: snapshot.Generation - 3, PriorTarget: before}}}
		required := []string{}
		if operation == "storage_c" {
			required = []string{"route"}
		}
		proof := domain.HostingProxyStorageSwitch{Version: domain.HostingProxyStorageSwitchVersion, Action: "switch", OperationID: operation, ContinuationHash: domain.HostingDocumentHash("continuation_" + operation), StorageRef: domain.HostingStorageRef{StorageID: storageID, Generation: 1, Hash: domain.HostingDocumentHash("storage")}, StorageOriginHash: domain.HostingDocumentHash("materialized_origin_a"), WriterStateHash: domain.HostingDocumentHash("stopped_" + before.ReleaseOperationID), CandidateHash: domain.HostingDocumentHash("ready_" + operation), RouteScopeHash: domain.HostingDocumentHash(snapshot.Routes), PriorGeneration: snapshot.Generation - 1, PriorPreviousGeneration: snapshot.Generation - 2, PriorEngineHash: prior.Hash(), PriorRequiredRouteIDs: required}
		proof.Hash = proof.DocumentHash()
		snapshot.StorageSwitch = &proof
		plan, err := domain.CompileHostingProxyPlan(snapshot, snapshot.Generation-1, identity.DaemonID)
		if err != nil {
			t.Fatal("compile persisted storage proxy", err)
		}
		raw, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		var persisted domain.HostingProxyPlan
		if json.Unmarshal(raw, &persisted) != nil || domain.ValidateHostingProxyPlan(persisted) != nil {
			t.Fatal("persisted plan did not validate")
		}
		reconstructed, failed, err := applicationStorageProxyPlans(config, persisted)
		if err != nil || failed != nil || reconstructed.Hash() != prior.Hash() || reconstructed.Generation() != prior.Generation() || reconstructed.StaticHash() != prior.StaticHash() {
			t.Fatal("adapter changed exact committed predecessor", operation, err)
		}
		if operation == "storage_c" {
			ordinary, err := hostingproxy.Prepare(identity, settings, 4, 3, reconstructed.Routes())
			if err != nil || ordinary.Hash() == reconstructed.Hash() {
				t.Fatal("verified B was silently reconstructed as ordinary")
			}
		}
		prior, err = hostingproxy.PrepareVerifiedSwitch(identity, settings, uint64(snapshot.Generation), uint64(snapshot.Generation-1), engineRoutes(snapshot), []string{"route"})
		if err != nil {
			t.Fatal(err)
		}
		last = persisted
	}
	for _, mode := range []string{"prior_hash", "prior_previous", "prior_required", "destination", "prior_route"} {
		t.Run(mode, func(t *testing.T) {
			bad := last
			proof := *last.Snapshot.StorageSwitch
			bad.Snapshot.StorageSwitch = &proof
			switch mode {
			case "prior_hash":
				proof.PriorEngineHash = strings.Repeat("f", 64)
			case "prior_previous":
				proof.PriorPreviousGeneration--
			case "prior_required":
				proof.PriorRequiredRouteIDs = []string{}
			case "destination":
				bad.Snapshot.DestinationID = "foreign"
			case "prior_route":
				v := *last.Snapshot.VerifiedSwitch
				v.Routes = append([]domain.HostingVerifiedSwitchRoute(nil), v.Routes...)
				v.Routes[0].PriorTarget.HostPort--
				bad.Snapshot.VerifiedSwitch = &v
			}
			proof.Hash = proof.DocumentHash()
			if _, _, err := applicationStorageProxyPlans(config, bad); err == nil {
				t.Fatal("changed persisted predecessor reconstructed")
			}
		})
	}
}

// The ordinary engine intentionally remains unaware of the interrupted protocol.
type fakeInterruptedStorageProxy struct {
	*fakeManagedProxy
	failVerification atomic.Bool
	verifications    atomic.Int32
	priorHash        string
}

func (e *fakeInterruptedStorageProxy) PrepareManagedVerifiedSwitch(_ context.Context, identity hostingproxy.Identity, generation, previous uint64, routes []hostingproxy.Route, required []string) (hostingproxy.Plan, string, error) {
	identity.DaemonID = "daemon_one"
	p, err := hostingproxy.PrepareVerifiedSwitch(identity, e.settings, generation, previous, routes, required)
	return p, identity.DaemonID, err
}
func (e *fakeInterruptedStorageProxy) ApplyInterruptedStorageSwitch(_ context.Context, p, prior hostingproxy.Plan, _ *hostingproxy.Plan, _ func(context.Context) error) (hostingproxy.Result, error) {
	e.effects.Add(1)
	e.priorHash = prior.Hash()
	return hostingproxy.Result{Outcome: hostingproxy.Refused, DesiredGeneration: p.Generation(), LoadedGeneration: prior.Generation(), Hash: p.Hash()}, errors.New("owned proxy preflight refused before mutation")
}
func (e *fakeInterruptedStorageProxy) VerifyInterruptedStorageReceipt(_ context.Context, p, prior hostingproxy.Plan, failed *hostingproxy.Plan, outcome hostingproxy.Outcome, generation uint64) error {
	e.verifications.Add(1)
	if e.failVerification.Load() {
		return errors.New("local evidence temporarily unavailable")
	}
	if prior.Hash() != e.priorHash || failed != nil || outcome != hostingproxy.Refused || generation != prior.Generation() || p.Generation() != prior.Generation()+1 {
		return errors.New("changed interrupted receipt context")
	}
	return nil
}

func TestApplicationStorageProxyRefusedReceiptRestartReachesFreshClaim(t *testing.T) {
	for _, intentOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "result_and_intent", true: "intent_only"}[intentOnly], func(t *testing.T) {
			claim, op, journal, config, legacy := proxyRuntimeFixture(t)
			if err := journal.Close(); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(t.TempDir(), "restart-journal")
			var err error
			journal, err = runner.OpenAttemptJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := journal.Close(); err != nil {
					t.Error("fixture cleanup failed")
				}
			}()
			legacy.verify = func() error { return errors.New("ordinary verifier rejects stopped selected backend502") }
			special := &fakeInterruptedStorageProxy{fakeManagedProxy: legacy}
			special.failVerification.Store(true)
			originalFactory := config.engineFactory
			config.engineFactory = func(settings hostingproxy.Settings) (managedProxyEngine, error) {
				_, err := originalFactory(settings)
				return special, err
			}
			config.VerifiedSwitch = true
			config.storageDatabase = &DatabaseExecutionConfig{ApplicationStorage: true, RouteContinuity: true}
			prior := op.Snapshot.Routes[0]
			prior.ReleaseOperationID = "hop_prior"
			prior.HostPort--
			op.Snapshot.VerifiedSwitch = &domain.HostingVerifiedSwitch{Version: 1, ExpectedLoadedGeneration: 1, PriorAppliedProxyOperationID: "pop_prior", Routes: []domain.HostingVerifiedSwitchRoute{{RouteID: prior.RouteID, PriorVersion: 1, PriorTarget: prior}}}
			settings := hostingproxy.Settings{Directory: filepath.Join(config.Directory, op.ProxyID), DockerBinary: config.DockerBinary, DockerSocket: config.DockerSocket, Email: op.Snapshot.Configuration.Email, CA: hostingproxy.CA(op.Snapshot.Configuration.CA), ManagementPort: op.Snapshot.Configuration.ManagementPort}
			old, err := hostingproxy.Prepare(hostingproxy.Identity{ProxyID: op.ProxyID, ServerID: op.Snapshot.ServerID, DestinationID: op.Snapshot.DestinationID, RunnerID: op.Snapshot.RunnerID, DaemonID: "daemon_one"}, settings, 1, 0, []hostingproxy.Route{{ID: prior.RouteID, Hostname: prior.Hostname, PathPrefix: prior.PathPrefix, HostPort: prior.HostPort}})
			if err != nil {
				t.Fatal(err)
			}
			storageID, _ := domain.HostingStorageIdentity("binding")
			proof := domain.HostingProxyStorageSwitch{Version: domain.HostingProxyStorageSwitchVersion, Action: "switch", OperationID: "hosting_one", ContinuationHash: domain.HostingDocumentHash("continuation"), StorageRef: domain.HostingStorageRef{StorageID: storageID, Generation: 1, Hash: domain.HostingDocumentHash("storage")}, StorageOriginHash: domain.HostingDocumentHash("origin"), WriterStateHash: domain.HostingDocumentHash("stopped"), CandidateHash: domain.HostingDocumentHash("ready"), RouteScopeHash: domain.HostingDocumentHash(op.Snapshot.Routes), PriorGeneration: 1, PriorPreviousGeneration: 0, PriorEngineHash: old.Hash(), PriorRequiredRouteIDs: []string{}}
			proof.Hash = proof.DocumentHash()
			op.Snapshot.StorageSwitch = &proof
			plan, err := domain.CompileHostingProxyPlan(op.Snapshot, 1, "daemon_one")
			if err != nil {
				t.Fatal(err)
			}
			op.Plan = &plan
			var completions, claims atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/runners/proxy/plan":
					var in domain.HostingAuthority
					if json.NewDecoder(r.Body).Decode(&in) != nil || in != hostingAuthority(claim.Run.ID, op.ID, claim.Lease) {
						t.Error("plan lost exact original authority")
					}
				case "/api/v1/runners/proxy/prepared":
					var in domain.HostingProxyPreparedRequest
					if json.NewDecoder(r.Body).Decode(&in) != nil {
						t.Error("bad prepared")
					}
					op.Preparation = &in.HostingProxyPreparation
					op.PreparedReceipt = &domain.HostingReceipt{ID: in.ReceiptID, BodyHash: domain.HostingDocumentHash(in.HostingProxyPreparation)}
					op.State = "prepared"
				case "/api/v1/runners/proxy/apply-begin":
					op.State = "applying"
					op.MutationHold = true
				case "/api/v1/runners/proxy/complete":
					var in domain.HostingProxyCompleteRequest
					if json.NewDecoder(r.Body).Decode(&in) != nil || in.HostingAuthority != hostingAuthority(claim.Run.ID, op.ID, claim.Lease) {
						t.Error("bad complete or changed original authority")
					}
					if op.CompleteReceipt != nil && (in.ReceiptID != op.CompleteReceipt.ID || domain.HostingDocumentHash(in.Result) != op.CompleteReceipt.BodyHash) {
						t.Error("replay changed immutable completion")
					}
					completions.Add(1)
					op.Result = &in.Result
					op.CompleteReceipt = &domain.HostingReceipt{ID: in.ReceiptID, BodyHash: domain.HostingDocumentHash(in.Result)}
					op.State = "refused"
					op.MutationHold = false
				case "/api/v1/runners/proxy/status":
					var in domain.HostingAuthority
					if json.NewDecoder(r.Body).Decode(&in) != nil || in != hostingAuthority(claim.Run.ID, op.ID, claim.Lease) {
						t.Error("status lost exact original authority")
					}
				case "/api/v1/runners/claim":
					if journal.Depth() != 0 {
						t.Error("claim preceded journal retirement")
					}
					claims.Add(1)
					next := claim
					next.Run.ID = "proxy_pop_recovery"
					next.Run.RunSpec.ProxyOperationID = "pop_recovery"
					next.Lease.RunID = next.Run.ID
					next.Lease.ID = "lease_recovery"
					next.Lease.Fence = "fence_recovery"
					_ = json.NewEncoder(w).Encode(next)
					return
				default:
					t.Errorf("replay made effect/authority request %s", r.URL.Path)
					w.WriteHeader(500)
					return
				}
				_ = json.NewEncoder(w).Encode(op)
			}))
			defer server.Close()
			supervisor := newAttemptSupervisor(claim.Lease)
			defer supervisor.Close()
			reporter := startAttemptReporter(supervisor.Context(), supervisor, journal, server.URL, "dedicated", claim.Run.ID, claim.Lease)
			defer reporter.Stop()
			err = executeProxyClaim(supervisor, journal, reporter, server.URL, "dedicated", claim, config)
			reporter.Stop()
			if err == nil || journal.Depth() != 2 || op.State != "refused" || completions.Load() != 1 || special.effects.Load() != 1 {
				t.Fatalf("committed refusal lost evidence: err=%v depth=%d posts=%d effects=%d", err, journal.Depth(), completions.Load(), special.effects.Load())
			}
			entries := journal.Snapshot().Proxy
			if intentOnly {
				for _, entry := range entries {
					if entry.Phase == "result" {
						if err = journal.AckProxy(entry.ID); err != nil {
							t.Fatal(err)
						}
					}
				}
			}

			if err = journal.Close(); err != nil {
				t.Fatal(err)
			}
			journal, err = runner.OpenAttemptJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := journal.Close(); err != nil {
					t.Error("fixture cleanup failed")
				}
			}()
			// Dispatch must fail closed before optional verification when the
			// durable scope, result correlation or local capability changes.
			_, local, _, prepareErr := prepareProxyEngine(t.Context(), config, *op.Plan, op.Preparation)
			if prepareErr != nil {
				t.Fatal(prepareErr)
			}
			beforeChecks := special.verifications.Load()
			if verifyProxyLocalReceipt(legacy, local, *op.Plan, config, *op.Result) == nil {
				t.Fatal("legacy verifier accepted interrupted result")
			}
			disabled := config
			disabled.VerifiedSwitch = false
			if verifyProxyLocalReceipt(special, local, *op.Plan, disabled, *op.Result) == nil {
				t.Fatal("missing opt-in accepted")
			}
			changed := *op.Result
			changed.EngineHash = strings.Repeat("f", 64)
			if verifyProxyLocalReceipt(special, local, *op.Plan, config, changed) == nil {
				t.Fatal("changed result correlation accepted")
			}
			raw, marshalErr := json.Marshal(op.Plan)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			var invalid domain.HostingProxyPlan
			if json.Unmarshal(raw, &invalid) != nil {
				t.Fatal("decode changed plan")
			}
			invalid.Snapshot.StorageSwitch.Hash = domain.HostingDocumentHash("foreign")
			if verifyProxyLocalReceipt(special, local, invalid, config, *op.Result) == nil {
				t.Fatal("changed durable storage proof accepted")
			}
			if special.verifications.Load() != beforeChecks {
				t.Fatal("unqualified evidence reached optional verifier")
			}
			special.failVerification.Store(false)
			if err = reconcileProxyJournal(server.URL, "dedicated", claim.Lease.RunnerID, journal, config); err != nil {
				t.Fatalf("exact committed interrupted refusal blocks startup: %v", err)
			}
			wantCompletions, wantVerifications := int32(2), int32(3)
			if intentOnly {
				wantCompletions, wantVerifications = 1, 2
			}
			if journal.Depth() != 0 || special.effects.Load() != 1 || completions.Load() != wantCompletions || special.verifications.Load() != wantVerifications {
				t.Fatalf("replay changed effects/evidence: depth=%d effects=%d complete=%d verify=%d", journal.Depth(), special.effects.Load(), completions.Load(), special.verifications.Load())
			}
			next, _, err := claimRunnerIteration(t.Context(), nil, runnerclient.New(server.URL, "dedicated").Claim)
			if err != nil || next.Run.ID != "proxy_pop_recovery" || claims.Load() != 1 {
				t.Fatal("fresh recovery claim unreachable", err)
			}
		})
	}
}

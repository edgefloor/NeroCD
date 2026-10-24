//go:build unix

package runnerruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

type databaseUpgradeCoordinatorEngine struct {
	*databaseCoordinatorEngine
	applyCalls, observeUpgradeCalls int
}

func (e *databaseUpgradeCoordinatorEngine) ApplyDatabaseNetworkUpgrade(ctx context.Context, id string, in databaseNetworkUpgradeInput, volume hostingOwnedVolumeRequest, evidence hostingOwnedVolumeEvidence, credentials *DatabaseCredentials, config DatabaseEngineConfig, before func(context.Context) error) domain.HostingResult {
	e.applyCalls++
	if e.beforeApply != nil {
		e.beforeApply()
	}
	if volume.CreatePermitted {
		e.f.t.Fatal("upgrade received create permission")
	}
	return e.f.e.ApplyDatabaseNetworkUpgrade(ctx, id, in, volume, evidence, credentials, config, func(ctx context.Context) error {
		if err := before(ctx); err != nil {
			return err
		}
		return e.f.before(ctx)
	})
}

func (e *databaseUpgradeCoordinatorEngine) ObserveDatabaseNetworkUpgrade(ctx context.Context, op domain.HostingOperation, in databaseNetworkUpgradeInput, credentials *DatabaseCredentials, config DatabaseEngineConfig) (domain.HostingObservation, error) {
	e.observeUpgradeCalls++
	return e.f.e.ObserveDatabaseNetworkUpgrade(ctx, op, in, credentials, config)
}

func databaseUpgradeCoordinatorFixture(t *testing.T, kind string) (*databaseFake, *runner.AttemptJournal, string, *databaseCoordinatorController, *databaseUpgradeCoordinatorEngine) {
	t.Helper()
	f, in, _ := upgradeFixture(t, kind)
	previous, healthy, origin := in.PreviousPlan, *in.Plan.PreviousHealthy, in.Proof.Origin
	now := time.Now().UTC()
	receipt := domain.HostingReceipt{ID: "original_materialization", BodyHash: "sha256:" + strings.Repeat("e", 64), CreatedAt: now}
	m := domain.HostingStorageMaterialization{StorageRef: f.req.Storage.Reference(), State: "materialized", CreationOperationID: f.req.OriginOperationID, CreationPlanHash: f.req.OriginPlanHash, CreationNonce: f.req.CreationNonce, DaemonID: f.req.DaemonID, Labels: f.req.labels(), MaterializedReceipt: &receipt}
	capture := domain.HostingDatabaseNetworkCapturePlan{Version: domain.HostingDatabaseNetworkCaptureVersion, OperationID: domain.HostingDatabaseNetworkCaptureID(healthy.OperationID), SourceOperationID: healthy.OperationID, ProjectID: f.req.Storage.ProjectID, ResourceID: previous.Snapshot.ResourceID, BindingID: previous.Snapshot.BindingID, DestinationID: previous.Snapshot.DestinationID, ServerID: previous.Snapshot.ServerID, RunnerID: previous.Snapshot.RunnerID, SourcePlan: previous, SourceHealthy: healthy, Storage: f.req.Storage, Materialization: m, Creation: domain.HostingDatabaseNetworkCaptureCreation{Receipt: receipt, VolumeName: f.req.Storage.VolumeName, Driver: "local", Scope: "local", Labels: m.Labels, Options: map[string]string{}}}
	capture.Hash = capture.DocumentHash()
	originReceipt := domain.HostingDatabaseNetworkOriginReceipt{HostingReceipt: domain.HostingReceipt{ID: "origin_receipt", BodyHash: "sha256:" + strings.Repeat("c", 64), CreatedAt: now}, OperationID: capture.OperationID, SourceOperationID: capture.SourceOperationID, PlanHash: capture.Hash, OriginHash: origin.DocumentHash(), Origin: origin, ObservedAt: now, RunnerID: capture.RunnerID}
	proof := domain.HostingDatabaseUpgradeProof{Version: domain.HostingDatabaseUpgradeVersion, CapturePlan: capture, OriginReceipt: originReceipt, PreviousPlan: previous, PreviousHealthy: healthy}
	proof.Hash = proof.DocumentHash()
	f.plan = compileUpgradeCoordinatorPlan(t, proof)
	j, dir := openStorageCoordinatorJournal(t)
	c := newStorageCoordinatorController(t, j, &mountedApplyFixture{plan: f.plan, request: f.req})
	c.a.RunID = "run_" + c.a.OperationID
	c.lease.RunID, c.op.RunID = c.a.RunID, c.a.RunID
	c.ack.Materialization = m
	c.op.DatabaseUpgrade = &proof
	if err := domain.ValidateHostingDatabaseUpgradeOperation(c.op); err != nil {
		t.Fatal("invalid coordinator proof", err)
	}
	return f, j, dir, &databaseCoordinatorController{storageCoordinatorController: c, deliveryUntil: now.Add(40 * time.Second)}, &databaseUpgradeCoordinatorEngine{databaseCoordinatorEngine: &databaseCoordinatorEngine{f: f}}
}

func compileUpgradeCoordinatorPlan(t *testing.T, proof domain.HostingDatabaseUpgradeProof) domain.HostingApplyPlan {
	t.Helper()
	in, err := domain.HostingDatabaseUpgradePlanInput(proof)
	if err != nil {
		t.Fatal(err)
	}
	db := *proof.PreviousPlan.Database
	db.Network = nil
	p, err := domain.CompileHostingDatabasePlanV2(proof.PreviousPlan.Snapshot, proof.PreviousPlan.Release, proof.PreviousHealthy, proof.PreviousPlan, *proof.PreviousPlan.Storage, db, in)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The original materialization receipt remains immutable. Each inspection gets
// its own exact ACK receipt without replacing the original volume provenance.
func serveDatabaseUpgrade(c *databaseCoordinatorController, w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/v1/runners/heartbeat" {
		_ = json.NewEncoder(w).Encode(domain.Runner{ID: c.lease.RunnerID})
		return
	}
	if r.URL.Path == "/api/v1/runners/hosting/storage-materialized" {
		c.mu.Lock()
		defer c.mu.Unlock()
		var in domain.HostingStorageMaterializedRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.Matches(c.ack.Storage, c.ack.Materialization) != nil || !c.hasPhase("storage_materialized") {
			c.t.Error("upgrade materialization escaped durable exact evidence")
		}
		c.materializedCalls++
		c.materialized = true
		if c.lostMaterialized {
			w.WriteHeader(http.StatusConflict)
			return
		}
		_ = json.NewEncoder(w).Encode(domain.HostingStorageMaterializedAck{Materialization: c.ack.Materialization, Receipt: domain.HostingReceipt{ID: in.ReceiptID, BodyHash: domain.HostingDocumentHash(in), CreatedAt: time.Now().UTC()}})
		return
	}
	c.serve(w, r)
}

func runDatabaseUpgradeCoordinator(t *testing.T, f *databaseFake, j *runner.AttemptJournal, c *databaseCoordinatorController, e *databaseUpgradeCoordinatorEngine, server string, enabled bool) error {
	t.Helper()
	s := newAttemptSupervisorFrom(t.Context(), c.lease)
	defer s.Close()
	return executeDatabaseDeployment(s, j, func(context.Context) error { return nil }, runnerclient.New(server, "fixture_token"), e, c.a, c.lease, c.op, DatabaseExecutionConfig{TrustedLoopbackHTTP: true, NetworkUpgrade: enabled}, f.config)
}

func TestDatabaseUpgradeCoordinatorTransitionsAndLostACK(t *testing.T) {
	for _, kind := range []string{"postgresql", "redis"} {
		for _, lost := range []string{"none", "materialized", "credentials", "complete"} {
			t.Run(kind+"/"+lost, func(t *testing.T) {
				f, j, dir, c, e := databaseUpgradeCoordinatorFixture(t, kind)
				original, err := os.ReadFile(filepath.Join(f.config.stateRoot, f.plan.Database.NetworkName+".json"))
				if err != nil {
					t.Fatal(err)
				}
				c.lostMaterialized, c.failCredentials, c.lostComplete = lost == "materialized", lost == "credentials", lost == "complete"
				var completeBodies [][]byte
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/api/v1/runners/hosting/complete" {
						raw, readErr := io.ReadAll(r.Body)
						if readErr != nil {
							t.Error(readErr)
							return
						}
						completeBodies = append(completeBodies, bytes.Clone(raw))
						r.Body = io.NopCloser(bytes.NewReader(raw))
					}
					serveDatabaseUpgrade(c, w, r)
				}))
				defer server.Close()
				err = runDatabaseUpgradeCoordinator(t, f, j, c, e, server.URL, true)
				if (err == nil) != (lost == "none") {
					t.Fatalf("wrong interruption: %v credentials=%d apply=%d input=%v", err, c.credentials, c.applyCalls, func() error { _, e := databaseOperationUpgradeInput(c.op); return e }())
				}
				if lost != "none" {
					for _, entry := range j.Snapshot().Hosting {
						if !databaseUpgradeProofMatches(entry.DatabaseUpgrade, c.op.DatabaseUpgrade) {
							t.Fatal("phase omitted proof")
						}
					}
					if err = j.Close(); err != nil {
						t.Fatal(err)
					}
					j, err = runner.OpenAttemptJournal(dir)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = j.Close() }()
					c.journal = j
					c.lostMaterialized, c.failCredentials, c.lostComplete = false, false, false
					cfg := databaseReplayConfig(server.URL, c)
					cfg.Database.NetworkUpgrade = true
					err = reconcileDatabaseJournalWithEngine(t.Context(), cfg, j, func() (databaseExecutionEngine, DatabaseEngineConfig, error) {
						if lost == "complete" {
							t.Fatal("completion replay opened engine")
						}
						return e, f.config, nil
					})
					if err != nil {
						t.Fatal(err)
					}
					if lost == "credentials" && (len(c.deliveryIDs) != 2 || c.deliveryIDs[0] != c.deliveryIDs[1]) {
						t.Fatal("delivery changed across restart")
					}
					if lost == "complete" && (len(completeBodies) != 2 || !bytes.Equal(completeBodies[0], completeBodies[1])) {
						t.Fatal("completion replay changed exact body")
					}
				}
				after, err := os.ReadFile(filepath.Join(f.config.stateRoot, f.plan.Database.NetworkName+".json"))
				if err != nil || !bytes.Equal(original, after) || f.networkCreates != 1 || f.creates != 2 || f.starts != 2 || e.applyCalls != 1 || len(j.Snapshot().Hosting) != 0 {
					t.Fatal("upgrade changed origin or duplicated writer")
				}
				// Maintain an already empty v2 plan using the same captured origin.
				proof := *c.op.DatabaseUpgrade
				proof.PreviousPlan = *c.op.Plan
				proof.PreviousHealthy = domain.HostingHealthy{OperationID: c.op.ID, ContainerID: c.op.Result.ContainerID, Snapshot: c.op.Snapshot, Release: *c.op.Release, PlanHash: c.op.Plan.Hash, Storage: &domain.HostingHealthyStorage{Plan: *c.op.Plan.Storage, CreationPlanHash: f.req.OriginPlanHash}}
				proof.Hash = proof.DocumentHash()
				f.plan = compileUpgradeCoordinatorPlan(t, proof)
				c.a.OperationID, c.a.RunID, c.lease.RunID = "hop_maintenance", "run_hop_maintenance", "run_hop_maintenance"
				c.op.ID, c.op.RunID, c.op.State = c.a.OperationID, c.a.RunID, "prepared"
				c.op.Plan, c.op.Release, c.op.DatabaseUpgrade = &f.plan, &f.plan.Release, &proof
				c.op.Result, c.op.CompleteReceipt = nil, nil
				c.applyCalls = 0
				c.lostComplete = false
				if err = runDatabaseUpgradeCoordinator(t, f, j, c, e, server.URL, true); err != nil {
					t.Fatal("empty v2 maintenance failed", err)
				}
				if f.creates != 3 || f.networkCreates != 1 || e.applyCalls != 2 || len(j.Snapshot().Hosting) != 0 {
					t.Fatal("maintenance failed existing-only sequence")
				}
			})
		}
	}
}

func TestDatabaseUpgradeCoordinatorFailClosed(t *testing.T) {
	for _, mode := range []string{"disabled", "create_permission", "changed_origin", "lost_apply", "revoked", "expired", "missing_proof", "changed_fence"} {
		t.Run(mode, func(t *testing.T) {
			f, j, _, c, e := databaseUpgradeCoordinatorFixture(t, "redis")
			switch mode {
			case "create_permission":
				c.ack.CreatePermitted = true
			case "changed_origin":
				c.ack.Materialization.CreationPlanHash = "sha256:" + strings.Repeat("f", 64)
			case "lost_apply":
				c.lostApply = true
			case "revoked":
				c.revoked = true
			case "expired":
				c.lease.ExpiresAt = time.Now().Add(-time.Second)
			case "missing_proof":
				c.op.DatabaseUpgrade = nil
			case "changed_fence":
				c.a.Fence = "changed"
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveDatabaseUpgrade(c, w, r) }))
			defer server.Close()
			if err := runDatabaseUpgradeCoordinator(t, f, j, c, e, server.URL, mode != "disabled"); err == nil {
				t.Fatal("invalid admission accepted")
			}
			if f.creates != 1 || e.applyCalls != 0 {
				t.Fatal("failure escaped into engine mutation")
			}
			if mode == "lost_apply" {
				cfg := databaseReplayConfig(server.URL, c)
				cfg.Database.NetworkUpgrade = true
				before := c.credentials
				open := func() (databaseExecutionEngine, DatabaseEngineConfig, error) {
					t.Error("uncertain replay opened engine")
					return nil, DatabaseEngineConfig{}, errors.New("forbidden")
				}
				if reconcileDatabaseJournalWithEngine(t.Context(), cfg, j, open) == nil || c.credentials != before || len(j.Snapshot().Hosting) == 0 {
					t.Fatal("applying replay refreshed delivery or lost hold")
				}
				c.op.ReconcileRequested = true
				c.op.Result = &domain.HostingResult{Outcome: "failed", Uncertain: true, PlanHash: c.op.Plan.Hash}
				if reconcileDatabaseJournalWithEngine(t.Context(), cfg, j, open) == nil || c.observeCalls != 0 || c.credentials != before {
					t.Fatal("observation invented durable candidate")
				}
			}
		})
	}
}

func TestDatabaseUpgradePreparationRetainsProofAndExactReleaseAcrossRestart(t *testing.T) {
	f, j, dir, c, e := databaseUpgradeCoordinatorFixture(t, "postgresql")
	c.op.State, c.op.Plan, c.op.Release = "preparing", nil, nil
	lost, preparedCalls := true, 0
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/runners/hosting/plan":
			_ = json.NewEncoder(w).Encode(c.op)
		case "/api/v1/runners/hosting/prepared":
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			bodies = append(bodies, bytes.Clone(raw))
			var in domain.HostingPreparedRequest
			if json.Unmarshal(raw, &in) != nil || in.Release != f.plan.Release || !c.hasPhase("prepared") {
				t.Error("preparation changed retained release or preceded journal")
			}
			for _, entry := range c.journal.Snapshot().Hosting {
				if entry.Prepared != nil && !databaseUpgradeProofMatches(entry.DatabaseUpgrade, c.op.DatabaseUpgrade) {
					t.Error("preparation omitted sidecar")
				}
			}
			preparedCalls++
			c.op.Plan, c.op.Release, c.op.State = &f.plan, &f.plan.Release, "prepared"
			c.op.PreparedReceipt = &domain.HostingReceipt{ID: in.ReceiptID, BodyHash: domain.HostingDocumentHash(in.Release), CreatedAt: time.Now().UTC()}
			if lost {
				w.WriteHeader(http.StatusConflict)
				return
			}
			_ = json.NewEncoder(w).Encode(c.op)
		default:
			serveDatabaseUpgrade(c, w, r)
		}
	}))
	defer server.Close()
	if err := runDatabaseUpgradeCoordinator(t, f, j, c, e, server.URL, true); err == nil || c.credentials != 0 || e.applyCalls != 0 {
		t.Fatal("lost preparation ACK escaped into writer")
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	var err error
	j, err = runner.OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	c.journal, lost = j, false
	cfg := databaseReplayConfig(server.URL, c)
	cfg.Database.NetworkUpgrade = true
	if err = reconcileDatabaseJournalWithEngine(t.Context(), cfg, j, func() (databaseExecutionEngine, DatabaseEngineConfig, error) { return e, f.config, nil }); err != nil {
		t.Fatal(err)
	}
	if preparedCalls != 2 || len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) || e.applyCalls != 1 || len(j.Snapshot().Hosting) != 0 {
		t.Fatal("restart changed exact preparation or duplicated writer")
	}
}

func TestDatabaseUpgradeCompletionJoinsWorkersAndObservationUsesDurableID(t *testing.T) {
	f, j, _, c, e := databaseUpgradeCoordinatorFixture(t, "redis")
	parent, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	s := newAttemptSupervisorFrom(parent, c.lease)
	defer s.Close()
	var watcher *leaseWatcher
	var renewer *leaseRenewer
	observing := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runners/hosting/complete" {
			select {
			case <-watcher.done:
			default:
				t.Error("completion preceded watcher join")
			}
			select {
			case <-renewer.done:
			default:
				t.Error("completion preceded renewer join")
			}
		}
		if r.URL.Path == "/api/v1/runners/hosting/reconciliation" {
			var in domain.HostingObservation
			if json.NewDecoder(r.Body).Decode(&in) != nil || !in.Ready || in.ContainerID != c.op.Result.ContainerID {
				t.Error("observation lacked exact durable candidate")
			}
			c.op.MutationHold = false
			_ = json.NewEncoder(w).Encode(c.op)
			return
		}
		if observing && (r.URL.Path == "/api/v1/runners/hosting/apply-begin" || r.URL.Path == "/api/v1/runners/hosting/storage-create-begin") {
			t.Error("observation issued mutation admission")
		}
		serveDatabaseUpgrade(c, w, r)
	}))
	defer server.Close()
	watcher = startLeaseWatcher(s.Context(), server.URL, "fixture_token", c.lease, time.Hour, s.cancel)
	defer watcher.Stop()
	renewer = startLeaseRenewer(s.Context(), s, server.URL, "fixture_token", c.lease, time.Hour)
	defer renewer.Stop()
	reporter := startAttemptReporter(s.Context(), s, j, server.URL, "fixture_token", c.a.RunID, c.lease)
	defer reporter.Stop()
	e.beforeApply = func() {
		select {
		case <-watcher.done:
			t.Error("watcher joined before engine returned")
		default:
		}
		select {
		case <-renewer.done:
			t.Error("renewer joined before engine returned")
		default:
		}
	}
	client := runnerclient.New(server.URL, "fixture_token")
	config := DatabaseExecutionConfig{TrustedLoopbackHTTP: true, NetworkUpgrade: true}
	if err := executeDatabasePrepared(s, j, databaseCompletionDrain(s, reporter, watcher, renewer), client, e, c.a, c.lease, c.op, config, f.config); err != nil {
		t.Fatal(err)
	}
	if s.Context().Err() != nil {
		t.Fatal("orderly worker join canceled completion context")
	}
	observing = true
	c.op.TaskMode, c.op.ReconcileRequested, c.op.MutationHold = "observe", true, true
	c.op.ReconciliationRunID = "observe_" + c.op.ID
	lease := c.lease
	lease.ID, lease.RunID, lease.Fence = "observe_lease", c.op.ReconciliationRunID, "observe_fence"
	observe := newAttemptSupervisorFrom(t.Context(), lease)
	defer observe.Close()
	a := hostingAuthority(lease.RunID, c.op.ID, lease)
	before := len(f.calls)
	if err := executeDatabaseObservation(observe, client, e, a, c.op, config, f.config); err != nil {
		t.Fatal(err)
	}
	if e.observeUpgradeCalls != 1 {
		t.Fatal("observation did not select upgrade engine")
	}
	for _, call := range f.calls[before:] {
		for _, mutation := range []string{"container create", "container start", "container stop", "container rm", "container exec", "network create", "volume create"} {
			if strings.HasPrefix(call, mutation) {
				t.Fatal("observation mutated Docker", call)
			}
		}
	}
}

func TestDatabaseUpgradeOriginalWindowAndChangedLiveProofFailClosed(t *testing.T) {
	for _, mode := range []string{"expired_window", "changed_live_proof"} {
		t.Run(mode, func(t *testing.T) {
			f, j, _, c, e := databaseUpgradeCoordinatorFixture(t, "redis")
			if mode == "expired_window" {
				entry := runner.JournalHosting{ID: "original_upgrade_intent", Attempt: journalAttemptIdentity(c.a.RunID, c.lease, nil), OperationID: c.op.ID, Phase: "apply_intent", Plan: c.op.Plan, DeliveryID: strings.Repeat("a", 32), CreatedAt: time.Now().Add(-2 * time.Minute)}
				databaseEntry(&entry, c.op)
				if _, err := j.AppendHosting(entry); err != nil {
					t.Fatal(err)
				}
			} else {
				e.beforeApply = func() {
					proof := *c.op.DatabaseUpgrade
					proof.OriginReceipt.BodyHash = "sha256:" + strings.Repeat("f", 64)
					proof.Hash = proof.DocumentHash()
					c.op.DatabaseUpgrade = &proof
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveDatabaseUpgrade(c, w, r) }))
			defer server.Close()
			if err := runDatabaseUpgradeCoordinator(t, f, j, c, e, server.URL, true); err == nil {
				t.Fatal("invalid upgrade continued")
			}
			if f.creates != 1 || len(j.Snapshot().Hosting) == 0 || mode == "expired_window" && c.credentials != 0 {
				t.Fatal("lost proof/window boundary or durable evidence")
			}
		})
	}
}

func TestDatabaseUpgradeCapabilityExactCaptureNeverClaimsDeployment(t *testing.T) {
	f := newCaptureExecutionFixture(t, "redis")
	c, _, h := captureRuntimeHTTPFixture(t, f)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/runners/identity" {
			_ = json.NewEncoder(w).Encode(map[string]any{"runner_id": h.claim.Lease.RunnerID, "capabilities": []string{domain.HostingDatabaseNetworkCaptureCapability, domain.HostingDatabaseUpgradeCapability, domain.HostingDatabaseCapability, domain.RunTypeDatabaseDeploy, domain.HostingOwnedStorageCapability}})
			return
		}
		h.serve(t, w, r)
	}))
	defer server.Close()
	c.Server = server.URL
	client := runnerclient.New(server.URL, "capture_runtime_canary")
	client.HTTP = server.Client()
	if err := runCaptureRuntimeTest(t.Context(), c, client, func(context.Context, DatabaseEngineConfig, string) (databaseNetworkCaptureObserver, func() error, error) {
		return f.observer, func() error { return nil }, nil
	}); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, path := range h.paths {
		if !strings.HasPrefix(path, "/api/v1/runners/database-network-captures/") {
			t.Fatal("capture entered deployment polling or execution", path)
		}
	}
	if len(h.paths) == 0 {
		t.Fatal("capture did not execute its exact-ID protocol")
	}
}

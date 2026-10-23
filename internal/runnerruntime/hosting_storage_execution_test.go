package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

type storageCoordinatorEngine struct {
	volume  func(context.Context, hostingOwnedVolumeRequest) (hostingOwnedVolumeEvidence, error)
	apply   func(context.Context, string, domain.HostingApplyPlan, hostingOwnedVolumeRequest, hostingOwnedVolumeEvidence, ...func() error) domain.HostingResult
	observe func(context.Context, domain.HostingOperation) (domain.HostingObservation, error)
}

func (*storageCoordinatorEngine) EnsurePreparedRelease(context.Context, domain.HostingRelease) error {
	return nil
}
func (*storageCoordinatorEngine) Close() error { return nil }
func (e *storageCoordinatorEngine) ensureOwnedVolume(ctx context.Context, r hostingOwnedVolumeRequest) (hostingOwnedVolumeEvidence, error) {
	return e.volume(ctx, r)
}
func (e *storageCoordinatorEngine) ApplyWithOwnedStorage(ctx context.Context, id string, p domain.HostingApplyPlan, r hostingOwnedVolumeRequest, v hostingOwnedVolumeEvidence, verifying ...func() error) domain.HostingResult {
	return e.apply(ctx, id, p, r, v, verifying...)
}
func (e *storageCoordinatorEngine) Observe(ctx context.Context, op domain.HostingOperation) (domain.HostingObservation, error) {
	return e.observe(ctx, op)
}

type storageCoordinatorController struct {
	t                                                                       *testing.T
	mu                                                                      sync.Mutex
	journal                                                                 *runner.AttemptJournal
	op                                                                      domain.HostingOperation
	lease                                                                   domain.RunLease
	a                                                                       domain.HostingAuthority
	ack                                                                     domain.HostingStorageCreateAck
	createCalls, materializedCalls, applyCalls, completeCalls, observeCalls int
	lostMaterialized, lostComplete, lostApply, lostCreate, revoked          bool
	materialized                                                            bool
	allowEmptyObservation                                                   bool
	materializedHashes                                                      []string
	requests                                                                []string
}

func newStorageCoordinatorController(t *testing.T, journal *runner.AttemptJournal, f *mountedApplyFixture) *storageCoordinatorController {
	t.Helper()
	now := time.Now().UTC()
	lease := domain.RunLease{ID: "lease_storage", RunID: "run_storage", RunnerID: f.plan.Snapshot.RunnerID, Attempt: 1, Fence: "fence_storage", Status: domain.LeaseActive, CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	a := hostingAuthority(lease.RunID, "hop_candidate", lease)
	op := domain.HostingOperation{ID: a.OperationID, RunID: a.RunID, ProjectID: f.request.Storage.ProjectID, ResourceID: f.request.Storage.ResourceID, BindingID: f.request.Storage.BindingID, TaskMode: "deploy", State: "prepared", Snapshot: f.plan.Snapshot, Plan: &f.plan, Release: &f.plan.Release}
	m := domain.HostingStorageMaterialization{StorageRef: f.request.Storage.Reference(), State: "create_admitted", CreationOperationID: f.request.OriginOperationID, CreationPlanHash: f.request.OriginPlanHash, CreationNonce: f.request.CreationNonce, DaemonID: f.request.DaemonID}
	m.Labels = domain.ExpectedHostingStorageLabels(f.request.Storage, m)
	return &storageCoordinatorController{t: t, journal: journal, op: op, lease: lease, a: a, ack: domain.HostingStorageCreateAck{Storage: f.request.Storage, Materialization: m}}
}

func (c *storageCoordinatorController) serve(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requests = append(c.requests, r.URL.Path)
	write := func(value any) {
		if err := json.NewEncoder(w).Encode(value); err != nil {
			c.t.Error(err)
		}
	}
	switch r.URL.Path {
	case "/api/v1/runners/hosting/prepared":
		var in domain.HostingPreparedRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.HostingAuthority != c.a || in.Release != *c.op.Release {
			c.t.Error("prepared replay identity changed")
		}
		c.op.PreparedReceipt = &domain.HostingReceipt{ID: in.ReceiptID, BodyHash: domain.HostingDocumentHash(in.Release), CreatedAt: time.Now().UTC()}
		write(c.op)
	case "/api/v1/runners/hosting/storage-create-begin":
		var in domain.HostingStorageCreateRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.HostingAuthority != c.a || in.PlanHash != c.op.Plan.Hash {
			c.t.Error("creation authority/plan changed")
		}
		if !c.hasPhase("storage_create_intent") {
			c.t.Error("create admission escaped durable intent")
		}
		c.createCalls++
		c.op.MutationHold = true
		if c.lostCreate {
			w.WriteHeader(http.StatusConflict)
			return
		}
		ack := c.ack
		if c.createCalls > 1 {
			ack.CreatePermitted = false
		}
		write(ack)
	case "/api/v1/runners/hosting/storage-materialized":
		var in domain.HostingStorageMaterializedRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.HostingAuthority != c.a || in.Matches(c.ack.Storage, c.ack.Materialization) != nil {
			c.t.Error("materialization target changed")
		}
		if !c.hasPhase("storage_materialized") {
			c.t.Error("materialization escaped durable evidence")
		}
		c.materializedCalls++
		hash := domain.HostingDocumentHash(in)
		if len(c.materializedHashes) != 0 && c.materializedHashes[0] != hash {
			c.t.Error("materialization replay changed exact durable body")
		}
		c.materializedHashes = append(c.materializedHashes, hash)
		c.materialized = true
		receipt := domain.HostingReceipt{ID: in.ReceiptID, BodyHash: domain.HostingDocumentHash(in), CreatedAt: time.Now().UTC()}
		c.ack.Materialization.State = "materialized"
		c.ack.Materialization.MaterializedReceipt = &receipt
		if c.lostMaterialized {
			w.WriteHeader(http.StatusConflict)
			return
		}
		write(domain.HostingStorageMaterializedAck{Materialization: c.ack.Materialization, Receipt: receipt})
	case "/api/v1/runners/hosting/apply-begin":
		c.applyCalls++
		if !c.materialized || !c.hasPhase("apply_intent") {
			c.t.Error("writer admitted before materialization/intent")
		}
		if c.revoked {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		c.op.State = "applying"
		if c.lostApply {
			w.WriteHeader(http.StatusConflict)
			return
		}
		write(c.op)
	case "/api/v1/runners/hosting/complete":
		var in domain.HostingCompleteRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.HostingAuthority != c.a {
			c.t.Error("completion authority changed")
		}
		if !c.hasPhase("result") {
			c.t.Error("completion escaped durable result")
		}
		c.completeCalls++
		c.op.Result = &in.Result
		c.op.CompleteReceipt = &domain.HostingReceipt{ID: in.ReceiptID, BodyHash: domain.HostingDocumentHash(in.Result), CreatedAt: time.Now().UTC()}
		c.op.State = in.Result.Outcome
		c.op.MutationHold = in.Result.Uncertain
		if c.lostComplete {
			w.WriteHeader(http.StatusConflict)
			return
		}
		write(c.op)
	case "/api/v1/runners/hosting/status":
		var in domain.HostingStatusRequest
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.HostingAuthority != c.a {
			c.t.Error("status authority changed")
		}
		if in.Phase == "claim_observation" {
			c.observeCalls++
			op := c.op
			lease := c.lease
			lease.ID, lease.RunID, lease.Fence = "lease_observe", "run_observe", "fence_observe"
			lease.CreatedAt, lease.ExpiresAt, lease.Status = time.Now().UTC(), time.Now().UTC().Add(time.Minute), domain.LeaseActive
			op.TaskMode, op.ReconciliationRunID, op.ObservationLease = "observe", lease.RunID, &lease
			write(op)
			return
		}
		if in.Phase == "verifying" {
			c.op.State = "verifying"
		}
		write(c.op)
	case "/api/v1/runners/hosting/plan":
		op := c.op
		op.TaskMode, op.ReconciliationRunID = "observe", "run_observe"
		write(op)
	case "/api/v1/runners/hosting/reconciliation":
		// The current controller cannot settle unknown/uncertain writer effects.
		// Even an empty container listing must retain that ownership hold.
		var in domain.HostingObservation
		if json.NewDecoder(r.Body).Decode(&in) != nil || in.RunID != "run_observe" || !in.NoOwnedContainers {
			c.t.Error("observation did not use exact read-only lease")
		}
		if c.allowEmptyObservation {
			c.op.MutationHold = false
			write(c.op)
			return
		}
		w.WriteHeader(http.StatusConflict)
	default:
		if r.URL.Path == "/api/v1/runners/lease" || r.URL.Path == "/api/v1/runners/renew" {
			if c.revoked {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"code":"not_found"}`))
				return
			}
			write(c.lease)
			return
		}
		c.t.Errorf("unexpected or competing claim request: %s", r.URL.Path)
		w.WriteHeader(http.StatusConflict)
	}
}

func (c *storageCoordinatorController) hasPhase(phase string) bool {
	for _, entry := range c.journal.Snapshot().Hosting {
		if entry.Phase == phase {
			return true
		}
	}
	return false
}

func openStorageCoordinatorJournal(t *testing.T) (*runner.AttemptJournal, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "journal")
	j, err := runner.OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = j.Close() })
	return j, dir
}

func TestStorageCoordinatorUsesRealMountedEngineAfterControllerACK(t *testing.T) {
	f := mountedTestFixture(t, "storage")
	j, _ := openStorageCoordinatorJournal(t)
	c := newStorageCoordinatorController(t, j, f)
	server := httptest.NewServer(http.HandlerFunc(c.serve))
	defer server.Close()
	s := newAttemptSupervisorFrom(t.Context(), c.lease)
	defer s.Close()
	if err := executeHostingStoragePrepared(s, j, func(context.Context) error { return nil }, runnerclient.New(server.URL, "token"), f.engine, c.a, c.lease, c.op); err != nil {
		t.Fatal(err)
	}
	if c.createCalls != 1 || c.materializedCalls != 1 || c.applyCalls != 1 || c.completeCalls != 1 || !f.created || len(j.Snapshot().Hosting) != 0 {
		t.Fatalf("incomplete admitted sequence: %+v journal=%+v", c.requests, j.Snapshot().Hosting)
	}
	previous := f.plan.PreviousHealthy
	if previous == nil || f.containers[previous.ContainerID].State.Running || !f.containers[f.candidate].State.Running {
		t.Fatal("writer switch did not stop predecessor before starting mounted candidate")
	}
}

func TestStorageCoordinatorRestartsMaterializationACKLossWithoutCreatePermission(t *testing.T) {
	f := mountedTestFixture(t, "")
	j, dir := openStorageCoordinatorJournal(t)
	c := newStorageCoordinatorController(t, j, f)
	c.lostMaterialized = true
	server := httptest.NewServer(http.HandlerFunc(c.serve))
	defer server.Close()
	s := newAttemptSupervisorFrom(t.Context(), c.lease)
	err := executeHostingStoragePrepared(s, j, func(context.Context) error { return nil }, runnerclient.New(server.URL, "token"), f.engine, c.a, c.lease, c.op)
	s.Close()
	if err == nil || c.applyCalls != 0 || f.created || !c.hasPhase("storage_materialized") {
		t.Fatalf("lost ACK allowed writer: %v", err)
	}
	before := j.Snapshot().Hosting
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = runner.OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	if !reflect.DeepEqual(before, j.Snapshot().Hosting) {
		t.Fatal("restart changed exact storage evidence")
	}
	c.mu.Lock()
	c.journal = j
	c.lostMaterialized = false
	c.mu.Unlock()
	// A fresh Docker projection may turn absent options into an empty map.
	// Keep the original null-options receipt unchanged across this restart.
	f.volume.Options = map[string]string{}
	if err = reconcileHostingStorageJournalWithEngine(server.URL, "token", j, "", t.Context(), func(string) (hostingStorageEngine, error) { return f.engine, nil }); err != nil {
		t.Fatal(err)
	}
	if c.applyCalls != 1 || c.createCalls != 2 || c.materializedCalls != 2 || len(j.Snapshot().Hosting) != 0 {
		t.Fatalf("restart sequence wrong: %v", c.requests)
	}
}

func TestStorageCoordinatorCompleteACKLossReplaysWithoutOpeningEngine(t *testing.T) {
	f := mountedTestFixture(t, "")
	j, dir := openStorageCoordinatorJournal(t)
	c := newStorageCoordinatorController(t, j, f)
	c.lostComplete = true
	server := httptest.NewServer(http.HandlerFunc(c.serve))
	defer server.Close()
	s := newAttemptSupervisorFrom(t.Context(), c.lease)
	err := executeHostingStoragePrepared(s, j, func(context.Context) error { return nil }, runnerclient.New(server.URL, "token"), f.engine, c.a, c.lease, c.op)
	s.Close()
	if err == nil || !c.hasPhase("result") || !f.created {
		t.Fatalf("result ACK loss was not durable: %v", err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = runner.OpenAttemptJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = j.Close() }()
	c.mu.Lock()
	c.journal = j
	c.lostComplete = false
	c.mu.Unlock()
	if err = reconcileHostingStorageJournalWithEngine(server.URL, "token", j, "", t.Context(), func(string) (hostingStorageEngine, error) {
		t.Error("known result replay opened mutation engine")
		return nil, errors.New("forbidden")
	}); err != nil {
		t.Fatal(err)
	}
	if c.applyCalls != 1 || c.completeCalls != 2 || c.materializedCalls != 1 || len(j.Snapshot().Hosting) != 0 {
		t.Fatalf("completion replay duplicated mutation: %v", c.requests)
	}
}

func TestStorageCoordinatorMissingVolumeAfterAdmissionNeverRecreates(t *testing.T) {
	f := mountedTestFixture(t, "")
	f.mode = "missing-volume"
	j, _ := openStorageCoordinatorJournal(t)
	c := newStorageCoordinatorController(t, j, f)
	server := httptest.NewServer(http.HandlerFunc(c.serve))
	defer server.Close()
	s := newAttemptSupervisorFrom(t.Context(), c.lease)
	defer s.Close()
	err := executeHostingStoragePrepared(s, j, func(context.Context) error { return nil }, runnerclient.New(server.URL, "token"), f.engine, c.a, c.lease, c.op)
	if !errors.Is(err, errHostingOwnedVolumeMissing) || c.applyCalls != 0 || c.materializedCalls != 0 || len(j.Snapshot().Hosting) != 1 {
		t.Fatalf("missing admitted volume did not block: %v", err)
	}
}

func TestStorageCoordinatorAmbiguousWriterCanReachOwnerObservationBeforeClaims(t *testing.T) {
	for _, acceptEmpty := range []bool{false, true} {
		t.Run(map[bool]string{false: "controller_retains_hold", true: "empty_observation_is_not_settlement"}[acceptEmpty], func(t *testing.T) {
			f := mountedTestFixture(t, "")
			j, _ := openStorageCoordinatorJournal(t)
			c := newStorageCoordinatorController(t, j, f)
			c.allowEmptyObservation = acceptEmpty
			c.op.State, c.op.MutationHold, c.op.ReconcileRequested = "applying", true, true
			entry := runner.JournalHosting{ID: "storage_apply", Attempt: journalAttemptIdentity(c.a.RunID, c.lease, nil), OperationID: c.a.OperationID, Phase: "apply_intent", Plan: c.op.Plan, StorageRef: c.op.Snapshot.Storage, CreatedAt: time.Now().UTC()}
			if _, err := j.AppendHosting(entry); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(c.serve))
			defer server.Close()
			observed := false
			engine := &storageCoordinatorEngine{observe: func(ctx context.Context, op domain.HostingOperation) (domain.HostingObservation, error) {
				observed = true
				return domain.HostingObservation{NoOwnedContainers: true, DaemonID: f.request.DaemonID}, nil
			}}
			err := reconcileHostingStorageJournalWithEngine(server.URL, "token", j, "", t.Context(), func(string) (hostingStorageEngine, error) { return engine, nil })
			if err == nil || !observed || c.observeCalls != 1 || c.applyCalls != 0 || c.createCalls != 0 || len(j.Snapshot().Hosting) != 1 {
				t.Fatalf("read-only recovery unreachable or discarded effects: %v requests=%v", err, c.requests)
			}
		})
	}
}

func freshStorageCoordinatorFixture(t *testing.T) *mountedApplyFixture {
	t.Helper()
	f := mountedTestFixture(t, "")
	f.request.OriginOperationID = "hop_candidate"
	f.request.CreationNonce = domain.HostingStorageCreationNonce(f.request.Storage.Reference(), f.request.OriginOperationID, f.request.DaemonID)
	descriptor := *f.plan.Storage
	descriptor.CreationOperationID, descriptor.CreationNonce = f.request.OriginOperationID, f.request.CreationNonce
	plan, err := domain.CompileHostingOwnedStoragePlan(f.plan.Snapshot, f.plan.Release, nil, nil, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	f.plan, f.request.OriginPlanHash = plan, plan.Hash
	f.volume = ownedVolumeInspection(f.request)
	f.evidence = hostingOwnedVolumeEvidence{Storage: f.request.Storage.Reference(), VolumeName: f.request.Storage.VolumeName, DaemonID: f.request.DaemonID, CreatedAt: f.volume.CreatedAt, OriginOperationID: f.request.OriginOperationID, OriginPlanHash: f.request.OriginPlanHash, CreationNonce: f.request.CreationNonce, Driver: "local", Scope: "local", Labels: f.request.labels()}
	f.install(t)
	return f
}

func TestStorageCoordinatorLostCreateResponseInspectsOnceWithoutDuplicateCreate(t *testing.T) {
	f := freshStorageCoordinatorFixture(t)
	j, _ := openStorageCoordinatorJournal(t)
	c := newStorageCoordinatorController(t, j, f)
	c.ack.CreatePermitted = true
	server := httptest.NewServer(http.HandlerFunc(c.serve))
	defer server.Close()
	s := newAttemptSupervisorFrom(t.Context(), c.lease)
	defer s.Close()
	var permissions []bool
	engine := &storageCoordinatorEngine{volume: func(ctx context.Context, req hostingOwnedVolumeRequest) (hostingOwnedVolumeEvidence, error) {
		permissions = append(permissions, req.CreatePermitted)
		if len(permissions) == 1 {
			return hostingOwnedVolumeEvidence{}, errHostingOwnedVolumeUncertain
		}
		return f.evidence, nil
	}, apply: func(ctx context.Context, id string, p domain.HostingApplyPlan, req hostingOwnedVolumeRequest, v hostingOwnedVolumeEvidence, verifying ...func() error) domain.HostingResult {
		if req.CreatePermitted || !c.materialized || !c.hasPhase("storage_materialized") {
			t.Error("fresh creation permission escaped into workload execution")
		}
		return domain.HostingResult{Outcome: "succeeded", ContainerID: strings.Repeat("d", 64), ImageID: p.Release.ImageID, DaemonID: p.Release.DaemonID, PlanHash: p.Hash, Ready: true}
	}}
	if err := executeHostingStoragePrepared(s, j, func(context.Context) error { return nil }, runnerclient.New(server.URL, "token"), engine, c.a, c.lease, c.op); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(permissions, []bool{true, false}) || c.applyCalls != 1 || len(j.Snapshot().Hosting) != 0 {
		t.Fatalf("unsafe create retry permissions=%v requests=%v", permissions, c.requests)
	}
}

func TestStorageCoordinatorLostAdmissionACKNeverCreatesAfterRestart(t *testing.T) {
	f := freshStorageCoordinatorFixture(t)
	f.mode = "missing-volume"
	j, _ := openStorageCoordinatorJournal(t)
	c := newStorageCoordinatorController(t, j, f)
	c.ack.CreatePermitted = true
	c.lostCreate = true
	server := httptest.NewServer(http.HandlerFunc(c.serve))
	defer server.Close()
	s := newAttemptSupervisorFrom(t.Context(), c.lease)
	err := executeHostingStoragePrepared(s, j, func(context.Context) error { return nil }, runnerclient.New(server.URL, "token"), f.engine, c.a, c.lease, c.op)
	s.Close()
	if err == nil || c.createCalls != 1 || len(j.Snapshot().Hosting) != 1 {
		t.Fatalf("lost create admission was not retained: %v", err)
	}
	c.mu.Lock()
	c.lostCreate = false
	c.mu.Unlock()
	err = reconcileHostingStorageJournalWithEngine(server.URL, "token", j, "", t.Context(), func(string) (hostingStorageEngine, error) { return f.engine, nil })
	if !errors.Is(err, errHostingOwnedVolumeMissing) || c.createCalls != 2 || c.applyCalls != 0 || c.materializedCalls != 0 || len(j.Snapshot().Hosting) != 1 {
		t.Fatalf("lost admission ACK enabled empty recreation: %v requests=%v", err, c.requests)
	}
}

func TestStorageCoordinatorRefusesExpiredOrRevokedWriterAuthority(t *testing.T) {
	for _, mode := range []string{"expired", "revoked_apply", "revoked_replay", "apply_ack_loss"} {
		t.Run(mode, func(t *testing.T) {
			f := mountedTestFixture(t, "")
			j, _ := openStorageCoordinatorJournal(t)
			c := newStorageCoordinatorController(t, j, f)
			if mode == "expired" {
				c.lease.ExpiresAt = time.Now().Add(-time.Second)
			}
			if mode == "revoked_apply" {
				c.revoked = true
			}
			if mode == "apply_ack_loss" {
				c.lostApply = true
			}
			server := httptest.NewServer(http.HandlerFunc(c.serve))
			defer server.Close()
			s := newAttemptSupervisorFrom(t.Context(), c.lease)
			err := executeHostingStoragePrepared(s, j, func(context.Context) error { return nil }, runnerclient.New(server.URL, "token"), f.engine, c.a, c.lease, c.op)
			s.Close()
			if mode == "revoked_replay" {
				// Keep a real admitted creation intent, then fence its attempt.
				if err != nil {
					t.Fatal(err)
				}
				entry := runner.JournalHosting{ID: "stale_storage", Attempt: journalAttemptIdentity(c.a.RunID, c.lease, nil), OperationID: c.a.OperationID, Phase: "apply_intent", Plan: c.op.Plan, StorageRef: c.op.Snapshot.Storage, CreatedAt: time.Now().UTC()}
				if _, err = j.AppendHosting(entry); err != nil {
					t.Fatal(err)
				}
				c.mu.Lock()
				c.revoked = true
				c.op.State = "prepared"
				c.op.Result = nil
				c.op.CompleteReceipt = nil
				c.op.MutationHold = true
				c.mu.Unlock()
				err = reconcileHostingStorageJournalWithEngine(server.URL, "token", j, "", t.Context(), func(string) (hostingStorageEngine, error) {
					t.Error("fenced authority opened engine")
					return nil, errors.New("forbidden")
				})
				if err == nil || len(j.Snapshot().Hosting) != 1 {
					t.Fatal("fenced storage evidence discarded")
				}
				return
			}
			if err == nil || f.created {
				t.Fatalf("unauthorized writer started: %v", err)
			}
			if mode == "apply_ack_loss" {
				before := c.applyCalls
				err = reconcileHostingStorageJournalWithEngine(server.URL, "token", j, "", t.Context(), func(string) (hostingStorageEngine, error) {
					t.Error("ambiguous writer reran engine")
					return nil, errors.New("forbidden")
				})
				if err == nil || c.applyCalls != before || len(j.Snapshot().Hosting) != 3 {
					t.Fatal("ambiguous apply admission was replayed or discarded")
				}
			}
		})
	}
}

func TestStorageCoordinatorCancellationCannotRetireAdmittedEffects(t *testing.T) {
	for _, mode := range []string{"canceled_before_admission", "canceled_held", "restore_held", "other_attempt_receipt", "lost_plan"} {
		t.Run(mode, func(t *testing.T) {
			f := mountedTestFixture(t, "")
			j, _ := openStorageCoordinatorJournal(t)
			c := newStorageCoordinatorController(t, j, f)
			entry := runner.JournalHosting{ID: "storage_intent", Attempt: journalAttemptIdentity(c.a.RunID, c.lease, nil), OperationID: c.a.OperationID, Phase: "storage_create_intent", Plan: c.op.Plan, StorageRef: c.op.Snapshot.Storage, StorageCreate: &domain.HostingStorageCreateRequest{HostingAuthority: c.a, PlanHash: c.op.Plan.Hash, StorageRef: c.op.Snapshot.Storage, CreationNonce: c.op.Plan.Storage.CreationNonce}, CreatedAt: time.Now().UTC()}
			if _, err := j.AppendHosting(entry); err != nil {
				t.Fatal(err)
			}
			c.op.State = "canceled"
			switch mode {
			case "canceled_held":
				c.op.MutationHold = true
			case "restore_held":
				c.op.RestoreOwnershipHeld = true
			case "other_attempt_receipt":
				result := domain.HostingResult{Outcome: "failed"}
				c.op.Result = &result
				c.op.CompleteReceipt = &domain.HostingReceipt{ID: "other_attempt", BodyHash: domain.HostingDocumentHash(result)}
			case "lost_plan":
				c.op.Plan = nil
				c.op.Release = nil
			}
			server := httptest.NewServer(http.HandlerFunc(c.serve))
			defer server.Close()
			err := reconcileHostingStorageJournalWithEngine(server.URL, "token", j, "", t.Context(), func(string) (hostingStorageEngine, error) {
				t.Error("cancellation opened mutation engine")
				return nil, errors.New("forbidden")
			})
			settled := mode == "canceled_before_admission"
			if (err == nil) != settled || (len(j.Snapshot().Hosting) == 0) != settled {
				t.Fatalf("mode=%s err=%v remaining=%d", mode, err, len(j.Snapshot().Hosting))
			}
		})
	}
}

func TestStorageCoordinatorActualStartupCanClaimOnlyOwnerObservation(t *testing.T) {
	f := mountedTestFixture(t, "")
	j, dir := openStorageCoordinatorJournal(t)
	c := newStorageCoordinatorController(t, j, f)
	c.op.State, c.op.MutationHold, c.op.ReconcileRequested = "applying", true, true
	entry := runner.JournalHosting{ID: "storage_apply", Attempt: journalAttemptIdentity(c.a.RunID, c.lease, nil), OperationID: c.a.OperationID, Phase: "apply_intent", Plan: c.op.Plan, StorageRef: c.op.Snapshot.Storage, CreatedAt: time.Now().UTC()}
	if _, err := j.AppendHosting(entry); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(c.serve))
	defer server.Close()
	// Constructor rejection stops before any Docker subprocess. The runtime
	// must first reach the exact read-only lease and plan, never normal claims.
	err := RunContext(t.Context(), Config{Server: server.URL, Token: "token", RunnerID: c.lease.RunnerID, WorkDir: t.TempDir(), JournalDir: dir, Execute: true, Once: true, ImageDockerEndpoint: "tcp://unapproved.invalid:2375"})
	if err == nil || !strings.Contains(err.Error(), "explicit local Docker Unix socket") || c.observeCalls != 1 {
		t.Fatalf("actual startup could not reach owner recovery: %v requests=%v", err, c.requests)
	}
	for _, path := range c.requests {
		if path != "/api/v1/runners/hosting/status" && path != "/api/v1/runners/hosting/plan" {
			t.Errorf("startup performed unrelated traffic before storage recovery: %s", path)
		}
	}
}

func TestStorageCoordinatorPreparedReceiptRestartFinishesWithOneWriter(t *testing.T) {
	f := mountedTestFixture(t, "")
	j, _ := openStorageCoordinatorJournal(t)
	c := newStorageCoordinatorController(t, j, f)
	prepared := domain.HostingPreparedRequest{HostingAuthority: c.a, ReceiptID: attemptMutationKey("hosting_prepared", c.a.LeaseID, c.a.Attempt, "release"), Release: *c.op.Release}
	entry := runner.JournalHosting{ID: prepared.ReceiptID, Attempt: journalAttemptIdentity(c.a.RunID, c.lease, nil), OperationID: c.a.OperationID, Phase: "prepared", Prepared: &prepared, StorageRef: c.op.Snapshot.Storage, CreatedAt: time.Now().UTC()}
	if _, err := j.AppendHosting(entry); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(c.serve))
	defer server.Close()
	if err := reconcileHostingStorageJournalWithEngine(server.URL, "token", j, "", t.Context(), func(string) (hostingStorageEngine, error) { return f.engine, nil }); err != nil {
		t.Fatal(err)
	}
	if c.applyCalls != 1 || !f.created || len(j.Snapshot().Hosting) != 0 || c.requests[0] != "/api/v1/runners/hosting/prepared" {
		t.Fatalf("prepared replay did not finish one exact operation: %v", c.requests)
	}
}

func TestStorageCoordinatorPreparationFailureReceiptSettlesWithoutPlan(t *testing.T) {
	f := mountedTestFixture(t, "")
	j, _ := openStorageCoordinatorJournal(t)
	c := newStorageCoordinatorController(t, j, f)
	c.op.Plan, c.op.Release = nil, nil
	request := domain.HostingCompleteRequest{HostingAuthority: c.a, ReceiptID: attemptMutationKey("hosting_result", c.a.LeaseID, c.a.Attempt, "result"), Result: domain.HostingResult{Outcome: "failed"}}
	entry := runner.JournalHosting{ID: request.ReceiptID, Attempt: journalAttemptIdentity(c.a.RunID, c.lease, nil), OperationID: c.a.OperationID, Phase: "result", Result: &request, StorageRef: c.op.Snapshot.Storage, CreatedAt: time.Now().UTC()}
	if _, err := j.AppendHosting(entry); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(c.serve))
	defer server.Close()
	if err := reconcileHostingStorageJournalWithEngine(server.URL, "token", j, "", t.Context(), func(string) (hostingStorageEngine, error) {
		t.Error("preparation failure opened engine")
		return nil, errors.New("forbidden")
	}); err != nil {
		t.Fatal(err)
	}
	if len(j.Snapshot().Hosting) != 0 || c.completeCalls != 1 {
		t.Fatal("known pre-admission failure retained unresolved storage intent")
	}
}

func TestStorageCoordinatorCancellationAfterVolumeInspectionCannotStartWriter(t *testing.T) {
	f := mountedTestFixture(t, "")
	j, _ := openStorageCoordinatorJournal(t)
	c := newStorageCoordinatorController(t, j, f)
	server := httptest.NewServer(http.HandlerFunc(c.serve))
	defer server.Close()
	s := newAttemptSupervisorFrom(t.Context(), c.lease)
	defer s.Close()
	engine := &storageCoordinatorEngine{volume: func(context.Context, hostingOwnedVolumeRequest) (hostingOwnedVolumeEvidence, error) {
		s.cancel()
		return f.evidence, nil
	}, apply: func(context.Context, string, domain.HostingApplyPlan, hostingOwnedVolumeRequest, hostingOwnedVolumeEvidence, ...func() error) domain.HostingResult {
		t.Error("canceled authority reached writer")
		return domain.HostingResult{Outcome: "failed"}
	}}
	err := executeHostingStoragePrepared(s, j, func(context.Context) error { return nil }, runnerclient.New(server.URL, "token"), engine, c.a, c.lease, c.op)
	if err == nil || c.materializedCalls != 0 || c.applyCalls != 0 || !c.hasPhase("storage_create_intent") {
		t.Fatalf("cancellation escaped volume phase: %v", err)
	}
}

func TestStorageRecoveryRejectedCanceledResultStillReachesOwnerObservation(t *testing.T) {
	for _, mode := range []string{"stale_conflict", "server_error", "unauthorized", "forbidden", "not_found", "bad_request", "transport", "malformed_success", "changed_status", "conflict_without_owner", "unacknowledged_settled_status"} {
		t.Run(mode, func(t *testing.T) {
			f := mountedTestFixture(t, "")
			j, _ := openStorageCoordinatorJournal(t)
			c := newStorageCoordinatorController(t, j, f)
			intent := runner.JournalHosting{ID: "storage_apply", Attempt: journalAttemptIdentity(c.a.RunID, c.lease, nil), OperationID: c.a.OperationID, Phase: "apply_intent", Plan: c.op.Plan, StorageRef: c.op.Snapshot.Storage, CreatedAt: time.Now().UTC()}
			if _, err := j.AppendHosting(intent); err != nil {
				t.Fatal(err)
			}
			c.op.State, c.op.MutationHold, c.op.ReconcileRequested = "applying", true, true
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/runners/hosting/complete" {
					c.mu.Lock()
					c.completeCalls++
					c.requests = append(c.requests, r.URL.Path)
					c.mu.Unlock()
					switch mode {
					case "server_error":
						w.WriteHeader(http.StatusServiceUnavailable)
					case "unauthorized":
						w.WriteHeader(http.StatusUnauthorized)
					case "forbidden":
						w.WriteHeader(http.StatusForbidden)
					case "not_found":
						w.WriteHeader(http.StatusNotFound)
					case "bad_request":
						w.WriteHeader(http.StatusBadRequest)
					case "transport":
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						_ = conn.Close()
					case "malformed_success":
						_, _ = w.Write([]byte(`{"invalid":`))
					default:
						w.WriteHeader(http.StatusConflict)
					}
					return
				}
				c.serve(w, r)
			}))
			defer server.Close()
			client := runnerclient.New(server.URL, "token")
			s := newAttemptSupervisorFrom(t.Context(), c.lease)
			s.cancel()
			result := domain.HostingResult{Outcome: "failed", Uncertain: true, PlanHash: f.plan.Hash, ImageID: f.plan.Release.ImageID, DaemonID: f.plan.Release.DaemonID}
			if mode == "unacknowledged_settled_status" {
				result.Outcome, result.Uncertain, result.Ready, result.ContainerID = "succeeded", false, true, f.candidate
			}
			_, err := journalHostingResultWithRuntimeEnvironment(s, j, client, c.a, c.lease, result, func(context.Context) error { t.Error("canceled helper drained events"); return nil }, false, domain.HostingRuntimeConfigurationRef{}, c.op.Snapshot.Storage)
			s.Close()
			if !errors.Is(err, errLeaseAuthorityLost) || !c.hasPhase("result") || c.completeCalls != 0 {
				t.Fatalf("canceled live helper did not persist result without publication: %v", err)
			}
			before := j.Snapshot().Hosting
			c.lease.ExpiresAt = time.Now().Add(-time.Second)
			if mode == "changed_status" {
				c.op.ID = "different_operation"
			}
			if mode == "conflict_without_owner" {
				c.op.ReconcileRequested = false
			}
			if mode == "unacknowledged_settled_status" {
				c.op.State, c.op.MutationHold, c.op.Result = "succeeded", false, &result
				c.op.CompleteReceipt = &domain.HostingReceipt{ID: attemptMutationKey("hosting_result", c.a.LeaseID, c.a.Attempt, "result"), BodyHash: domain.HostingDocumentHash(result), CreatedAt: time.Now().UTC()}
			}
			observations := 0
			engine := &storageCoordinatorEngine{observe: func(context.Context, domain.HostingOperation) (domain.HostingObservation, error) {
				observations++
				return domain.HostingObservation{NoOwnedContainers: true, DaemonID: f.request.DaemonID}, nil
			}, apply: func(context.Context, string, domain.HostingApplyPlan, hostingOwnedVolumeRequest, hostingOwnedVolumeEvidence, ...func() error) domain.HostingResult {
				t.Error("rejected stale result started writer")
				return domain.HostingResult{Outcome: "failed"}
			}}
			err = reconcileHostingStorageJournalWithEngine(server.URL, "token", j, "", t.Context(), func(string) (hostingStorageEngine, error) { return engine, nil })
			c.mu.Lock()
			defer c.mu.Unlock()
			wantObservation := mode == "stale_conflict"
			if err == nil || (observations == 1) != wantObservation || (c.observeCalls == 1) != wantObservation || c.applyCalls != 0 || c.createCalls != 0 || !reflect.DeepEqual(before, j.Snapshot().Hosting) {
				t.Fatalf("rejected result recovery mode=%s err=%v observations=%d requests=%v", mode, err, observations, c.requests)
			}
			if !wantObservation && mode != "changed_status" && mode != "conflict_without_owner" && mode != "unacknowledged_settled_status" && len(c.requests) != 1 {
				t.Fatalf("unrelated failure bypassed replay: %v", c.requests)
			}
		})
	}
}

func TestStorageRecoveryPreparedOriginCanObserveAfterOldAuthorityExpires(t *testing.T) {
	for _, mode := range []string{"expired", "fenced"} {
		t.Run(mode, func(t *testing.T) {
			f := mountedTestFixture(t, "")
			j, _ := openStorageCoordinatorJournal(t)
			c := newStorageCoordinatorController(t, j, f)
			c.op.MutationHold, c.op.ReconcileRequested = true, true
			entry := runner.JournalHosting{ID: "storage_origin", Attempt: journalAttemptIdentity(c.a.RunID, c.lease, nil), OperationID: c.a.OperationID, Phase: "storage_create_intent", Plan: c.op.Plan, StorageRef: c.op.Snapshot.Storage, StorageCreate: &domain.HostingStorageCreateRequest{HostingAuthority: c.a, PlanHash: c.op.Plan.Hash, StorageRef: c.op.Snapshot.Storage, CreationNonce: c.op.Plan.Storage.CreationNonce}, CreatedAt: time.Now().UTC()}
			if _, err := j.AppendHosting(entry); err != nil {
				t.Fatal(err)
			}
			c.lease.ExpiresAt = time.Now().Add(-time.Second)
			if mode == "fenced" {
				c.revoked = true
			}
			server := httptest.NewServer(http.HandlerFunc(c.serve))
			defer server.Close()
			observations := 0
			engine := &storageCoordinatorEngine{observe: func(context.Context, domain.HostingOperation) (domain.HostingObservation, error) {
				observations++
				return domain.HostingObservation{NoOwnedContainers: true, DaemonID: f.request.DaemonID}, nil
			}}
			err := reconcileHostingStorageJournalWithEngine(server.URL, "token", j, "", t.Context(), func(string) (hostingStorageEngine, error) { return engine, nil })
			if err == nil || observations != 1 || c.observeCalls != 1 || c.applyCalls != 0 || c.createCalls != 0 || len(j.Snapshot().Hosting) != 1 {
				t.Fatalf("prepared origin owner observation blocked by stale deployment mode=%s err=%v requests=%v", mode, err, c.requests)
			}
		})
	}
}

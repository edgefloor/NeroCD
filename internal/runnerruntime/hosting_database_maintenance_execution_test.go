//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func TestDatabaseMaintenanceSavedCompletionPrecedesHostAndPreservesHolds(t *testing.T) {
	for _, mode := range []string{"settled", "foreign-ack", "malformed-proof", "malformed-result", "disabled", "capture-mixed"} {
		t.Run(mode, func(t *testing.T) {
			f, j, c, e := maintenanceCoordinatorFixture(t, "redis")
			c.lostComplete = true
			foreign := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if foreign && r.URL.Path == "/api/v1/runners/hosting/complete" {
					ack := c.op
					receipt := *ack.CompleteReceipt
					receipt.ID = "foreign_complete"
					ack.CompleteReceipt = &receipt
					if err := json.NewEncoder(w).Encode(ack); err != nil {
						t.Error(err)
					}
					return
				}
				serveDatabaseUpgrade(c, w, r)
			}))
			defer server.Close()
			s := newAttemptSupervisorFrom(t.Context(), c.lease)
			err := executeDatabaseDeployment(s, j, func(context.Context) error { return nil }, runnerclient.New(server.URL, "fixture"), e, c.a, c.lease, c.op, DatabaseExecutionConfig{MembershipMaintenance: true, TrustedLoopbackHTTP: true}, f.db.config)
			s.Close()
			if err == nil || !c.hasPhase("result") {
				t.Fatal("missing retained complete fixture")
			}
			c.lostComplete = false
			cfg := databaseReplayConfig(server.URL, c)
			cfg.Database.MembershipMaintenance = mode != "disabled"
			snapshot := j.Snapshot()
			if mode == "malformed-proof" {
				snapshot.Hosting[0].DatabaseMaintenance.Hash = "sha256:" + strings.Repeat("f", 64)
			}
			if mode == "malformed-result" {
				for i := range snapshot.Hosting {
					if snapshot.Hosting[i].Result != nil {
						snapshot.Hosting[i].Result.Result.ImageID = "sha256:" + strings.Repeat("f", 64)
					}
				}
			}
			if mode == "capture-mixed" {
				snapshot.DatabaseNetworkCapture = []runner.JournalDatabaseNetworkCapture{{}}
			}
			foreign = mode == "foreign-ack"
			preflights, ordinary, replays := 0, 0, 0
			original := domain.HostingDocumentHash(j.Snapshot())
			// This is the exact zero-capture startup seam used by Run before its
			// automatic capture dispatcher can invoke a daemon preflight.
			err = reconcileConnectedBeforeCapture(t.Context(), snapshot, func(ctx context.Context) error {
				replays++
				return replaySavedDatabaseMaintenanceSnapshot(ctx, cfg, j, snapshot)
			})
			if mode == "settled" {
				if err != nil || replays != 1 || len(j.Snapshot().Hosting) != 0 || e.applyCalls != 1 {
					t.Fatal("saved Complete did not retire before daemon", err)
				}
				preflight := func() error {
					preflights++
					if len(j.Snapshot().Hosting) != 0 {
						t.Error("daemon preceded saved completion")
					}
					return errors.New("Docker unavailable")
				}
				if preflight() == nil {
					ordinary++
				}
				if preflights != 1 || ordinary != 0 {
					t.Fatal("host failure admitted ordinary work")
				}
			} else {
				if mode == "capture-mixed" {
					if err != nil || replays != 0 {
						t.Fatal("mixed capture bypassed its first dispatcher")
					}
					if replaySavedDatabaseMaintenanceSnapshot(t.Context(), cfg, j, snapshot) == nil {
						t.Fatal("mixed capture permitted direct retirement")
					}
				} else if err == nil {
					t.Fatal("foreign or unsupported completion retired")
				}
				if preflights != 0 || ordinary != 0 || domain.HostingDocumentHash(j.Snapshot()) != original || e.applyCalls != 1 {
					t.Fatal("failed replay cleared hold or repeated effects")
				}
			}
		})
	}
}

type maintenanceCoordinatorEngine struct {
	*databaseCoordinatorEngine
	applyCalls  int
	observation *domain.HostingObservation
}

func (e *maintenanceCoordinatorEngine) ApplyDatabaseMemberMaintenance(ctx context.Context, id string, in databaseMemberMaintenanceInput, r hostingOwnedVolumeRequest, v hostingOwnedVolumeEvidence, c *DatabaseCredentials, config DatabaseEngineConfig, before func(context.Context) error) domain.HostingResult {
	e.applyCalls++
	return e.f.e.ApplyDatabaseMemberMaintenance(ctx, id, in, r, v, c, config, func(ctx context.Context) error {
		if err := before(ctx); err != nil {
			return err
		}
		return e.f.before(ctx)
	})
}
func (e *maintenanceCoordinatorEngine) ObserveDatabaseMemberMaintenance(ctx context.Context, op domain.HostingOperation, in databaseMemberMaintenanceInput, c *DatabaseCredentials, config DatabaseEngineConfig) (domain.HostingObservation, error) {
	if e.observation != nil {
		return *e.observation, nil
	}
	return e.f.e.ObserveDatabaseMemberMaintenance(ctx, op, in, c, config)
}

func TestDatabaseMaintenanceObservationPreservesOriginalSettlement(t *testing.T) {
	for _, mode := range []string{"ready", "absence", "settlement-forged"} {
		t.Run(mode, func(t *testing.T) {
			f, j, c, e := maintenanceCoordinatorFixture(t, "redis")
			c.op.TaskMode, c.op.State, c.op.MutationHold, c.op.ReconcileRequested = "observe", "applying", true, true
			c.op.ReconciliationRunID = "observe_" + c.op.ID
			c.op.Result = &domain.HostingResult{Outcome: "failed", Uncertain: true, PlanHash: c.op.Plan.Hash, ContainerID: strings.Repeat("a", 64), ImageID: c.op.Release.ImageID, DaemonID: c.op.Release.DaemonID}
			lease := c.lease
			lease.ID, lease.RunID, lease.Fence = "observer_lease", c.op.ReconciliationRunID, "observer_fence"
			a := hostingAuthority(lease.RunID, c.op.ID, lease)
			observation := domain.HostingObservation{DaemonID: c.op.Release.DaemonID, BindingID: c.op.BindingID, PlanHash: c.op.Plan.Hash, ReleaseHash: c.op.Release.Hash, ContainerID: c.op.Result.ContainerID, ImageID: c.op.Result.ImageID, Ready: true}
			if mode == "absence" {
				observation.NoOwnedContainers, observation.ContainerID, observation.ImageID, observation.Ready = true, "", "", false
			}
			e.observation = &observation
			entry := runner.JournalHosting{ID: "original_intent", Attempt: journalAttemptIdentity(c.a.RunID, c.lease, nil), OperationID: c.op.ID, Phase: "apply_intent", Plan: c.op.Plan, DeliveryID: strings.Repeat("a", 32), CreatedAt: time.Now().UTC()}
			databaseEntry(&entry, c.op)
			if _, err := j.AppendHosting(entry); err != nil {
				t.Fatal(err)
			}
			original := domain.HostingDocumentHash(j.Snapshot())
			reports, drained := 0, false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api/v1/runners/hosting/reconciliation" {
					var got domain.HostingObservation
					if json.NewDecoder(r.Body).Decode(&got) != nil || got.HostingAuthority != a || got.PlanHash != observation.PlanHash || got.Ready != observation.Ready || got.NoOwnedContainers != observation.NoOwnedContainers || !drained {
						t.Error("observation escaped exact target or joined boundary")
					}
					reports++
					ack := c.op
					if mode == "settlement-forged" {
						ack.MutationHold = false
						ack.State = "succeeded"
					}
					if err := json.NewEncoder(w).Encode(ack); err != nil {
						t.Error(err)
					}
					return
				}
				c.serve(w, r)
			}))
			defer server.Close()
			s := newAttemptSupervisorFrom(t.Context(), lease)
			defer s.Close()
			err := executeDatabaseObservation(s, runnerclient.New(server.URL, "fixture"), e, a, c.op, DatabaseExecutionConfig{TrustedLoopbackHTTP: true, MembershipMaintenance: true}, f.db.config, func(context.Context) error { drained = true; return nil })
			if (err == nil) != (mode != "settlement-forged") || reports != 1 || e.applyCalls != 0 || domain.HostingDocumentHash(j.Snapshot()) != original {
				t.Fatal("maintenance observer changed original effects or evidence", err)
			}
		})
	}
}

func maintenanceCoordinatorFixture(t *testing.T, kind string) (*applicationDatabaseFake, *runner.AttemptJournal, *databaseCoordinatorController, *maintenanceCoordinatorEngine) {
	t.Helper()
	f, in, _ := memberMaintenanceFixture(t, kind)
	source := f.in.Proof.BindingDatabasePlan
	healthy := domain.HostingHealthy{Snapshot: source.Snapshot, Release: source.Release, OperationID: source.Storage.CreationOperationID, ContainerID: in.Plan.PreviousHealthy.ContainerID, PlanHash: source.Hash, Storage: &domain.HostingHealthyStorage{Plan: *source.Storage, CreationPlanHash: f.db.req.OriginPlanHash}}
	now := time.Now().UTC()
	receipt := domain.HostingReceipt{ID: "original_materialization", BodyHash: "sha256:" + strings.Repeat("e", 64), CreatedAt: now}
	m := domain.HostingStorageMaterialization{StorageRef: f.db.req.Storage.Reference(), State: "materialized", CreationOperationID: f.db.req.OriginOperationID, CreationPlanHash: f.db.req.OriginPlanHash, CreationNonce: f.db.req.CreationNonce, DaemonID: f.db.req.DaemonID, Labels: f.db.req.labels(), MaterializedReceipt: &receipt}
	capture := domain.HostingDatabaseNetworkCapturePlan{Version: domain.HostingDatabaseNetworkCaptureVersion, OperationID: domain.HostingDatabaseNetworkCaptureID(healthy.OperationID), SourceOperationID: healthy.OperationID, ProjectID: f.db.req.Storage.ProjectID, ResourceID: source.Snapshot.ResourceID, BindingID: source.Snapshot.BindingID, DestinationID: source.Snapshot.DestinationID, ServerID: source.Snapshot.ServerID, RunnerID: source.Snapshot.RunnerID, SourcePlan: source, SourceHealthy: healthy, Storage: f.db.req.Storage, Materialization: m, Creation: domain.HostingDatabaseNetworkCaptureCreation{Receipt: receipt, VolumeName: f.db.req.Storage.VolumeName, Driver: "local", Scope: "local", Labels: m.Labels, Options: map[string]string{}}}
	capture.Hash = capture.DocumentHash()
	origin := domain.HostingDatabaseNetworkOriginReceipt{HostingReceipt: domain.HostingReceipt{ID: "origin_receipt", BodyHash: "sha256:" + strings.Repeat("c", 64), CreatedAt: now}, OperationID: capture.OperationID, SourceOperationID: capture.SourceOperationID, PlanHash: capture.Hash, OriginHash: in.Proof.Origin.DocumentHash(), Origin: in.Proof.Origin, ObservedAt: now, RunnerID: capture.RunnerID}
	proof, err := domain.NewHostingDatabaseMaintenanceProof(capture, origin, in.PreviousPlan, *in.Plan.PreviousHealthy, in.Proof)
	if err != nil {
		t.Fatal("maintenance full proof", err)
	}
	j, _ := openStorageCoordinatorJournal(t)
	base := newStorageCoordinatorController(t, j, &mountedApplyFixture{plan: in.Plan, request: f.db.req})
	base.a.RunID = "run_" + base.a.OperationID
	base.lease.RunID = base.a.RunID
	base.op.RunID = base.a.RunID
	base.op.DatabaseMaintenance = &proof
	base.ack.Materialization = m
	if domain.ValidateHostingDatabaseMaintenanceOperation(base.op) != nil {
		t.Fatal("maintenance operation")
	}
	return f, j, &databaseCoordinatorController{storageCoordinatorController: base, deliveryUntil: now.Add(40 * time.Second)}, &maintenanceCoordinatorEngine{databaseCoordinatorEngine: &databaseCoordinatorEngine{f: f.db}}
}

func TestDatabaseMaintenanceCoordinatorPreservesMembersAndReplaysComplete(t *testing.T) {
	for _, kind := range []string{"postgresql", "redis"} {
		t.Run(kind, func(t *testing.T) {
			f, j, c, e := maintenanceCoordinatorFixture(t, kind)
			c.lostComplete = true
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveDatabaseUpgrade(c, w, r) }))
			defer server.Close()
			s := newAttemptSupervisorFrom(t.Context(), c.lease)
			defer s.Close()
			err := executeDatabaseDeployment(s, j, func(context.Context) error { return nil }, runnerclient.New(server.URL, "fixture"), e, c.a, c.lease, c.op, DatabaseExecutionConfig{TrustedLoopbackHTTP: true, MembershipMaintenance: true}, f.db.config)
			if err == nil || e.applyCalls != 1 || len(j.Snapshot().Hosting) == 0 {
				t.Fatal("lost ACK not retained", err, e.applyCalls)
			}
			c.lostComplete = false
			cfg := databaseReplayConfig(server.URL, c)
			cfg.Database.MembershipMaintenance = true
			opens := 0
			err = reconcileDatabaseJournalWithEngine(t.Context(), cfg, j, func() (databaseExecutionEngine, DatabaseEngineConfig, error) { opens++; return e, f.db.config, nil })
			if err != nil || opens != 0 || e.applyCalls != 1 || len(j.Snapshot().Hosting) != 0 {
				t.Fatal("maintenance completion replay repeated writer or lost proof", err)
			}
			for id, app := range f.apps {
				if !app.State.Running || app.NetworkSettings.Networks[f.in.Proof.Binding.NetworkName].EndpointID == "" {
					t.Fatal("settled app member changed", id)
				}
			}
		})
	}
}

func TestDatabaseMaintenanceCoordinatorRequiresExplicitModeAndNoVolumeCreate(t *testing.T) {
	for _, mode := range []string{"disabled", "create-permission"} {
		t.Run(mode, func(t *testing.T) {
			f, j, c, e := maintenanceCoordinatorFixture(t, "redis")
			if mode == "create-permission" {
				c.ack.CreatePermitted = true
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { serveDatabaseUpgrade(c, w, r) }))
			defer server.Close()
			s := newAttemptSupervisorFrom(t.Context(), c.lease)
			defer s.Close()
			err := executeDatabaseDeployment(s, j, func(context.Context) error { return nil }, runnerclient.New(server.URL, "fixture"), e, c.a, c.lease, c.op, DatabaseExecutionConfig{TrustedLoopbackHTTP: true, MembershipMaintenance: mode != "disabled"}, f.db.config)
			if err == nil || e.applyCalls != 0 {
				t.Fatal("maintenance permission bypass", err)
			}
		})
	}
}

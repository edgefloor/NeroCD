//go:build unix

package runnerruntime

import (
	"context"
	"encoding/json"
	"errors"
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

func TestPrepareApplicationStorageReauthorizesHeldCreation(t *testing.T) {
	for _, kind := range []string{"postgresql", "redis"} {
		for _, mode := range []string{"fresh", "revoked", "changed_origin", "changed_daemon", "changed_storage", "replayed_permission", "cancelled"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				f := newConnectedExecutionFixture(t, kind, true)
				in, err := domain.HostingApplicationDatabaseExecutionPlanInput(*f.op.ApplicationDatabase)
				if err != nil {
					t.Fatal(err)
				}
				snapshot := f.op.Snapshot
				id, name := domain.HostingStorageIdentity(snapshot.BindingID)
				storage := domain.HostingStorage{ID: id, ProjectID: f.op.ProjectID, ResourceID: snapshot.ResourceID, BindingID: snapshot.BindingID, DestinationID: snapshot.DestinationID, Kind: "platform_managed", Driver: "local", VolumeName: name, MountPath: "/data/uploads", AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "actor", CreatedAt: time.Now().UTC()}
				storage.Hash = storage.DocumentHash()
				snapshot.Storage = storage.Reference()
				snapshot.PolicyVersion = domain.HostingApplicationDatabaseStoragePolicyVersion
				descriptor := domain.HostingStoragePlan{Reference: snapshot.Storage, VolumeName: name, MountPath: storage.MountPath, CreationOperationID: f.op.ID, CreationNonce: domain.HostingStorageCreationNonce(snapshot.Storage, f.op.ID, f.op.Release.DaemonID), DaemonID: f.op.Release.DaemonID}
				plan, err := domain.CompileHostingApplicationDatabaseStoragePlan(snapshot, *f.op.Release, f.op.Plan.PreviousHealthy, nil, in, descriptor)
				if err != nil {
					t.Fatal(err)
				}
				proof := domain.NewHostingApplicationDatabaseStorage(snapshot.Storage)
				f.op.Plan, f.op.Snapshot, f.op.ApplicationDatabaseStorage = &plan, snapshot, &proof
				origin := domain.HostingStorageMaterialization{StorageRef: storage.Reference(), State: "create_admitted", CreationOperationID: f.op.ID, CreationPlanHash: plan.Hash, CreationNonce: descriptor.CreationNonce, DaemonID: descriptor.DaemonID}
				origin.Labels = domain.ExpectedHostingStorageLabels(storage, origin)
				ack := domain.HostingStorageCreateAck{Storage: storage, Materialization: origin, CreatePermitted: mode != "replayed_permission"}
				volume, err := storageVolumeRequest(f.op, ack)
				if err != nil {
					t.Fatal(err)
				}
				image := testImageEngine(t)
				docker := &ownedVolumeFake{req: volume}
				docker.install(t, image)
				engine := &applicationDatabaseEngine{image: image}
				ctx, cancel := context.WithCancel(runnerclient.WithDatabaseTransport(t.Context(), runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true}))
				defer cancel()
				s := newAttemptSupervisorFrom(ctx, f.lease)
				defer s.Close()
				admissions, materialized, plans := 0, 0, 0
				create := domain.HostingStorageCreateRequest{HostingAuthority: f.a, PlanHash: plan.Hash, StorageRef: snapshot.Storage, CreationNonce: descriptor.CreationNonce}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/api/v1/runners/hosting/storage-create-begin":
						var request domain.HostingStorageCreateRequest
						if json.NewDecoder(r.Body).Decode(&request) != nil || request != create || !f.has("storage_create_intent") {
							t.Error("creation admission lost exact journaled authority")
							w.WriteHeader(http.StatusConflict)
							return
						}
						admissions++
						response := ack
						if admissions > 1 {
							response.CreatePermitted = false
							switch mode {
							case "revoked":
								w.WriteHeader(http.StatusConflict)
								return
							case "changed_origin":
								response.Materialization.CreationOperationID = "foreign_origin"
							case "changed_daemon":
								response.Materialization.DaemonID = "foreign_daemon"
							case "changed_storage":
								response.Storage.CreatedBy = "different_actor"
							case "cancelled":
								cancel()
							}
						}
						// Admission holds the operation. Ordinary Plan must remain denied.
						f.op.MutationHold = true
						_ = json.NewEncoder(w).Encode(response)
					case "/api/v1/runners/hosting/plan":
						plans++
						w.WriteHeader(http.StatusConflict)
					case "/api/v1/runners/hosting/storage-materialized":
						var request domain.HostingStorageMaterializedRequest
						if json.NewDecoder(r.Body).Decode(&request) != nil || request.HostingAuthority != f.a || request.Matches(storage, origin) != nil || !f.has("storage_materialized") || docker.creates != 1 {
							t.Error("materialization preceded exact durable evidence or volume creation")
							w.WriteHeader(http.StatusConflict)
							return
						}
						materialized++
						receipt := domain.HostingReceipt{ID: request.ReceiptID, BodyHash: domain.HostingDocumentHash(request), CreatedAt: time.Now().UTC()}
						m := origin
						m.State, m.MaterializedReceipt = "materialized", &receipt
						_ = json.NewEncoder(w).Encode(domain.HostingStorageMaterializedAck{Materialization: m, Receipt: receipt})
					default:
						t.Errorf("unexpected storage preparation request %s", r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
				x := applicationDatabaseExchange{s: s, j: f.j, c: runnerclient.New(server.URL, "fixture"), e: engine, a: f.a, lease: f.lease, op: f.op, in: applicationDatabaseEngineInput{Plan: plan, Proof: in}}
				err = prepareApplicationStorage(&x)
				if mode == "fresh" {
					if err != nil || admissions != 2 || materialized != 1 || docker.creates != 1 || !docker.present || engine.storage == nil || engine.storage.CreatePermitted {
						t.Fatalf("first volume did not materialize under held admission: err=%v admissions=%d materialized=%d creates=%d", err, admissions, materialized, docker.creates)
					}
				} else if err == nil || docker.creates != 0 || docker.present || materialized != 0 || engine.storage != nil {
					t.Fatalf("changed/revoked/replayed authority created or materialized storage: err=%v creates=%d materialized=%d", err, docker.creates, materialized)
				}
				if plans != 0 {
					t.Fatalf("held creation used ordinary Plan (%d calls)", plans)
				}
			})
		}
	}
}

type storageWriterEngine struct {
	*connectedExecutionEngine
	stops, removals, resumes int
	removeErr                error
}

func (e *storageWriterEngine) bindStorage(domain.HostingOperation, domain.HostingStorageCreateAck) error {
	return nil
}
func (e *storageWriterEngine) StopStoragePredecessor(ctx context.Context, _ applicationDatabaseEngineInput, before func(context.Context) error) (string, error) {
	if err := before(ctx); err != nil {
		return "", err
	}
	e.stops++
	return domain.HostingDocumentHash("stopped predecessor"), nil
}
func (e *storageWriterEngine) StopStorageCandidate(ctx context.Context, _ applicationDatabaseEngineInput, _ applicationDatabaseCandidate, before func(context.Context) error) (string, error) {
	if err := before(ctx); err != nil {
		return "", err
	}
	e.stops++
	return domain.HostingDocumentHash("stopped candidate"), nil
}
func (e *storageWriterEngine) ResumeStoragePredecessor(ctx context.Context, in applicationDatabaseEngineInput, _ applicationDatabaseCandidate, before func(context.Context) error) (string, *domain.HostingDatabaseBindingEndpointObservation, error) {
	if err := before(ctx); err != nil {
		return "", nil, err
	}
	e.removals++
	if e.removeErr != nil {
		return "", nil, e.removeErr
	}
	if err := before(ctx); err != nil {
		return "", nil, err
	}
	e.resumes++
	endpoint := in.Plan.ApplicationDatabase.PreviousMembers[0].Endpoint
	endpoint.EndpointID = strings.Repeat("4", 64)
	return domain.HostingDocumentHash("resumed"), &domain.HostingDatabaseBindingEndpointObservation{Endpoint: endpoint, Present: true}, nil
}
func TestApplicationDatabaseStorageJournal17FailureAndFreshResumeReplay(t *testing.T) {
	for _, tc := range []struct {
		kind        string
		lostRemoval bool
	}{{kind: "postgresql"}, {kind: "redis"}, {kind: "redis", lostRemoval: true}} {
		name := tc.kind
		if tc.lostRemoval {
			name += "_lost_removal_response"
		}
		t.Run(name, func(t *testing.T) {
			kind := tc.kind
			f := newConnectedExecutionFixture(t, kind, true)
			in, _ := domain.HostingApplicationDatabaseExecutionPlanInput(*f.op.ApplicationDatabase)
			snapshot := f.op.Snapshot
			id, volume := domain.HostingStorageIdentity(snapshot.BindingID)
			storage := domain.HostingStorage{ID: id, ProjectID: f.op.ProjectID, ResourceID: snapshot.ResourceID, BindingID: snapshot.BindingID, DestinationID: snapshot.DestinationID, Kind: "platform_managed", Driver: "local", VolumeName: volume, MountPath: "/data/uploads", AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "actor", CreatedAt: time.Now().UTC()}
			storage.Hash = storage.DocumentHash()
			snapshot.Storage = storage.Reference()
			snapshot.PolicyVersion = domain.HostingApplicationDatabaseStoragePolicyVersion
			descriptor := domain.HostingStoragePlan{Reference: snapshot.Storage, VolumeName: volume, MountPath: storage.MountPath, CreationOperationID: f.op.ID, CreationNonce: domain.HostingStorageCreationNonce(snapshot.Storage, f.op.ID, f.op.Release.DaemonID), DaemonID: f.op.Release.DaemonID}
			plan, err := domain.CompileHostingApplicationDatabaseStoragePlan(snapshot, *f.op.Release, f.op.Plan.PreviousHealthy, nil, in, descriptor)
			if err != nil {
				t.Fatal(err)
			}
			proof := domain.NewHostingApplicationDatabaseStorage(snapshot.Storage)
			f.op.Plan = &plan
			f.op.Snapshot = snapshot
			f.op.ApplicationDatabaseStorage = &proof
			f.op.State = "applying"
			f.op.MutationHold = true
			origin := domain.HostingStorageMaterialization{StorageRef: storage.Reference(), State: "materialized", CreationOperationID: f.op.ID, CreationPlanHash: plan.Hash, CreationNonce: descriptor.CreationNonce, DaemonID: descriptor.DaemonID, MaterializedReceipt: &domain.HostingReceipt{ID: "materialized", BodyHash: domain.HostingDocumentHash("materialized"), CreatedAt: time.Now().UTC()}}
			origin.Labels = domain.ExpectedHostingStorageLabels(storage, origin)
			ack := domain.HostingStorageCreateAck{Storage: storage, Materialization: origin}
			var writer *domain.HostingApplicationDatabaseStorageWriterRequest
			var recovery *domain.HostingApplicationDatabaseStorageRecoveryPlan
			lostPhase := "replacement_failed"
			saved := map[string]domain.HostingOperation{}
			grants := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/runners/hosting/database-binding/state":
					_ = json.NewEncoder(w).Encode(domain.HostingApplicationDatabaseExecutionState{OperationID: f.op.ID, PlanHash: plan.Hash, Stage: f.stage, MembershipRevision: in.MembershipRevision, HoldOperationID: f.op.ID, Storage: &ack, StorageWriter: writer, StorageRecovery: recovery})
				case "/api/v1/runners/hosting/database-binding/storage/authorize":
					var request domain.HostingApplicationDatabaseStorageEffectRequest
					if json.NewDecoder(r.Body).Decode(&request) != nil || writer == nil || request.HostingAuthority != f.a || request.StateHash != writer.Receipt.Hash {
						t.Error("storage effect escaped fresh tuple/state")
						w.WriteHeader(409)
						return
					}
					grants++
					_ = json.NewEncoder(w).Encode(domain.HostingApplicationDatabaseStorageEffectAuthorization{HostingApplicationDatabaseEffectAuthorization: domain.HostingApplicationDatabaseEffectAuthorization{AuthorizationID: request.AuthorizationID, OperationID: f.op.ID, PlanHash: plan.Hash, Action: request.Action, StageHash: request.StageHash, LeaseID: f.a.LeaseID, Attempt: f.a.Attempt, Fence: f.a.Fence, MembershipRevision: in.MembershipRevision, UseUntil: time.Now().Add(10 * time.Second)}, StateHash: request.StateHash})
				case "/api/v1/runners/hosting/database-binding/storage/writer":
					var request domain.HostingApplicationDatabaseStorageWriterRequest
					if json.NewDecoder(r.Body).Decode(&request) != nil {
						t.Error("closed receipt")
						w.WriteHeader(409)
						return
					}
					found := false
					for _, entry := range f.j.Snapshot().Hosting {
						if entry.ApplicationStorageWriter != nil && domain.HostingDocumentHash(*entry.ApplicationStorageWriter) == domain.HostingDocumentHash(request) {
							found = true
						}
					}
					if !found {
						t.Error("writer sent before durable append")
					}
					if response, ok := saved[request.Receipt.ReceiptID]; ok {
						_ = json.NewEncoder(w).Encode(response)
						return
					}
					writer = &request
					state := request.Receipt.State()
					f.op.ApplicationDatabaseStorageState = &state
					if request.Receipt.Phase == "replacement_failed" {
						f.op.State = "failed"
					}
					if recovery != nil {
						state.RecoveryAction = "resume_predecessor"
						state.RecoveryState = "running"
					}
					if request.Receipt.Phase == "resumed" {
						f.op.MutationHold = false
						state.RecoveryState = "succeeded"
					}
					// Save a detached wire copy, as the controller's historical response does.
					raw, _ := json.Marshal(f.op)
					var response domain.HostingOperation
					if json.Unmarshal(raw, &response) != nil {
						t.Error("response copy")
					}
					saved[request.Receipt.ReceiptID] = response
					if request.Receipt.Phase == lostPhase {
						w.WriteHeader(409)
						return
					}
					_ = json.NewEncoder(w).Encode(response)
				default:
					f.serve(w, r)
				}
			}))
			defer server.Close()
			ctx := runnerclient.WithDatabaseTransport(t.Context(), runnerclient.DatabaseTransport{TrustedLoopbackHTTP: true})
			s := newAttemptSupervisorFrom(ctx, f.lease)
			defer s.Close()
			client := runnerclient.New(server.URL, "fixture")
			intent := applicationDatabaseEntry(s, f.a, f.lease, f.op, "apply_intent", "storage_apply_intent")
			intent.Plan = &plan
			intent.DeliveryID = strings.Repeat("a", 32)
			if _, err = appendApplicationDatabaseEntry(f.j, intent); err != nil {
				t.Fatal(err)
			}
			engine := &storageWriterEngine{connectedExecutionEngine: &connectedExecutionEngine{f: f}}
			x := applicationDatabaseExchange{s: s, j: f.j, c: client, a: f.a, lease: f.lease, op: f.op, in: applicationDatabaseEngineInput{Plan: plan, Proof: in}, e: engine}
			if err = x.stopStoragePredecessor(); err != nil {
				t.Fatal("durable stop chain", err)
			}
			candidate := domain.HostingApplicationDatabaseCandidate{Version: domain.HostingApplicationDatabaseCandidateVersion, OperationID: f.op.ID, PlanHash: plan.Hash, CreateNonce: in.CreateNonce, ContainerID: strings.Repeat("5", 64), ImageID: plan.Release.ImageID, DaemonID: plan.Release.DaemonID, DefaultNetworkID: strings.Repeat("6", 64)}
			candidate.Hash = candidate.DocumentHash()
			entry := applicationDatabaseEntry(s, f.a, f.lease, f.op, "application_candidate", "storage_candidate")
			entry.Plan = &plan
			entry.ApplicationCandidate = &candidate
			if _, err = appendApplicationDatabaseEntry(f.j, entry); err != nil {
				t.Fatal(err)
			}
			attach, err := domain.CompileHostingApplicationDatabaseAttachPlan(plan, in, candidate)
			if err != nil {
				t.Fatal(err)
			}
			stage := domain.HostingApplicationDatabaseStage{Version: domain.HostingApplicationDatabaseStageVersion, OperationID: f.op.ID, PlanHash: plan.Hash, Candidate: candidate, AttachPlan: attach, Phase: "candidate_created"}
			if err = x.publish(stage); err != nil {
				t.Fatal(err)
			}
			if err = x.storageFailed(privateApplicationDatabaseCandidate(candidate), func(context.Context) error { return nil }); err == nil {
				t.Fatal("lost closed failure ACK accepted")
			}
			if engine.stops != 2 || engine.resumes != 0 || f.op.State != "failed" || !f.op.MutationHold || f.complete != 0 || f.j.Depth() == 0 {
				t.Fatal("failed replacement settled or lost evidence")
			}
			raw, err := os.ReadFile(filepath.Join(f.dir, "journal.json"))
			if err != nil {
				t.Fatal(err)
			}
			var frame struct {
				Version int `json:"version"`
			}
			if json.Unmarshal(raw, &frame) != nil || frame.Version != 17 {
				t.Fatal("storage evidence lacks journal17")
			}
			if err = f.j.Close(); err != nil {
				t.Fatal(err)
			}
			f.j, err = runner.OpenAttemptJournal(f.dir)
			if err != nil {
				t.Fatal("journal17 restart", err)
			}
			defer func() {
				if err := f.j.Close(); err != nil {
					t.Error("fixture cleanup failed")
				}
			}()
			first := f.j.Snapshot().Hosting[0]
			config := f.config(server.URL)
			config.Database.ApplicationStorage = true
			lostPhase = ""
			if err = reconcileApplicationDatabaseAttempt(ctx, config, client, f.j, first, func() (applicationDatabaseExecutionEngine, error) {
				t.Fatal("closed failure replay opened Docker")
				return nil, nil
			}); err != nil {
				t.Fatal("closed failure replay", err)
			}
			if f.j.Depth() != 0 || engine.stops != 2 {
				t.Fatal("replay repeated stop")
			}
			recovery = &domain.HostingApplicationDatabaseStorageRecoveryPlan{Version: domain.HostingApplicationDatabaseStorageVersion, OperationID: f.op.ID, Action: "resume_predecessor", RunID: "storage_recover_" + f.op.ID, StorageRef: storage.Reference(), StorageStateHash: writer.Receipt.Hash, RequestHash: domain.HostingDocumentHash("explicit owner request")}
			recovery.Hash = recovery.DocumentHash()
			f.lease = domain.RunLease{ID: "fresh_recovery_lease", RunID: recovery.RunID, RunnerID: f.lease.RunnerID, Attempt: 1, Fence: "fresh_recovery_fence", Status: domain.LeaseActive, ExpiresAt: time.Now().Add(time.Minute), CreatedAt: time.Now().UTC()}
			f.a = hostingAuthority(f.lease.RunID, f.op.ID, f.lease)
			f.op.TaskMode = "storage_recover"
			fresh := newAttemptSupervisorFrom(ctx, f.lease)
			defer fresh.Close()
			if tc.lostRemoval {
				engine.removeErr = errors.New("lost exact candidate removal response")
			}
			err = executeApplicationStorageRecovery(fresh, f.j, func(context.Context) error { return nil }, client, engine, f.a, f.lease, f.op)
			if tc.lostRemoval {
				if err == nil || engine.removals != 1 || engine.resumes != 0 || !f.op.MutationHold || f.j.Depth() == 0 {
					t.Fatal("uncertain removal settled recovery or lost intent")
				}
				if err = f.j.Close(); err != nil {
					t.Fatal(err)
				}
				f.j, err = runner.OpenAttemptJournal(f.dir)
				if err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := f.j.Close(); err != nil {
						t.Error("fixture cleanup failed")
					}
				}()
				first := f.j.Snapshot().Hosting[0]
				if err = reconcileApplicationDatabaseAttempt(ctx, config, client, f.j, first, func() (applicationDatabaseExecutionEngine, error) {
					t.Fatal("open removal intent reopened Docker on restart")
					return nil, nil
				}); err == nil {
					t.Fatal("uncertain removal was silently recovered")
				}
				if engine.removals != 1 || engine.resumes != 0 || !f.op.MutationHold || f.j.Depth() == 0 {
					t.Fatal("restart repeated removal or resumed A")
				}
				return
			}
			if err != nil {
				t.Fatal("fresh recovery execution", err)
			}
			if engine.removals != 1 || engine.resumes != 1 || engine.stops != 2 || engine.creates != 0 || engine.configure != 0 || engine.starts != 0 || engine.retirements != 0 || grants != 4 || f.op.State != "failed" || f.op.MutationHold || f.j.Depth() != 0 {
				t.Fatal("recovery borrowed prepare or failed exact settlement")
			}
		})
	}
}

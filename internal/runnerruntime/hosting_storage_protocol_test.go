package runnerruntime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nerocd/internal/domain"
	"nerocd/internal/runner"
	"nerocd/internal/runnerclient"
)

func storageRuntimeFixture(t *testing.T) (runner.JournalHosting, domain.HostingStorageCreateAck, runner.JournalHosting, domain.HostingStorageMaterializedAck) {
	t.Helper()
	now := time.Now().UTC()
	a := runner.AttemptIdentity{RunID: "run_storage", LeaseID: "lease_storage", Attempt: 1, Fence: "fence_storage", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	authority := domain.HostingAuthority{OperationID: "operation", RunID: a.RunID, LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence}
	id, name := domain.HostingStorageIdentity("binding")
	storage := domain.HostingStorage{ID: id, ProjectID: "project", ResourceID: "resource", BindingID: "binding", DestinationID: "destination", Kind: "platform_managed", Driver: "local", VolumeName: name, MountPath: "/app/uploads", AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "actual_creator", CreatedAt: now}
	storage.Hash = storage.DocumentHash()
	release := domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "amd64", DaemonID: "daemon"}
	release.Hash = domain.HostingDocumentHash(release)
	snapshot := domain.HostingSnapshot{ResourceID: storage.ResourceID, BindingID: storage.BindingID, DestinationID: storage.DestinationID, HostPort: 20001, PolicyVersion: domain.HostingOwnedStoragePolicyVersion, Storage: storage.Reference()}
	descriptor := domain.HostingStoragePlan{Reference: storage.Reference(), VolumeName: name, MountPath: storage.MountPath, CreationOperationID: authority.OperationID, DaemonID: release.DaemonID}
	descriptor.CreationNonce = domain.HostingStorageCreationNonce(storage.Reference(), authority.OperationID, release.DaemonID)
	plan, err := domain.CompileHostingOwnedStoragePlan(snapshot, release, nil, nil, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	create := domain.HostingStorageCreateRequest{HostingAuthority: authority, StorageRef: storage.Reference(), PlanHash: plan.Hash, CreationNonce: descriptor.CreationNonce}
	origin := domain.HostingStorageMaterialization{StorageRef: storage.Reference(), State: "create_admitted", CreationOperationID: authority.OperationID, CreationPlanHash: plan.Hash, CreationNonce: descriptor.CreationNonce, DaemonID: release.DaemonID}
	origin.Labels = domain.ExpectedHostingStorageLabels(storage, origin)
	ack := domain.HostingStorageCreateAck{Storage: storage, Materialization: origin, CreatePermitted: true}
	proof := domain.HostingStorageMaterializedRequest{HostingAuthority: authority, ReceiptID: "storage_receipt", PlanHash: plan.Hash, StorageRef: storage.Reference(), CreationOperationID: authority.OperationID, CreationPlanHash: plan.Hash, CreationNonce: descriptor.CreationNonce, DaemonID: release.DaemonID, VolumeName: name, Driver: "local", Scope: "local", Labels: origin.Labels}
	receipt := domain.HostingReceipt{ID: proof.ReceiptID, BodyHash: domain.HostingDocumentHash(proof), CreatedAt: now}
	origin.State, origin.MaterializedReceipt = "materialized", &receipt
	intent := runner.JournalHosting{ID: "storage_intent", Attempt: a, OperationID: authority.OperationID, Phase: "storage_create_intent", Plan: &plan, StorageRef: storage.Reference(), StorageCreate: &create, CreatedAt: now}
	evidence := runner.JournalHosting{ID: "storage_evidence", Attempt: a, OperationID: authority.OperationID, Phase: "storage_materialized", Plan: &plan, StorageRef: storage.Reference(), StorageMaterialized: &proof, StorageMetadata: &storage, CreatedAt: now}
	return intent, ack, evidence, domain.HostingStorageMaterializedAck{Materialization: origin, Receipt: receipt}
}

func TestStorageProtocolPersistsBeforeSendAndReplaysLostMaterializationACK(t *testing.T) {
	intent, ack, evidence, ready := storageRuntimeFixture(t)
	directory := filepath.Join(t.TempDir(), "journal")
	journal, err := runner.OpenAttemptJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := os.ReadFile(filepath.Join(directory, "journal.json"))
		if err != nil || !strings.Contains(string(raw), `"version":10`) || strings.Contains(string(raw), `create_permitted`) {
			t.Error("request escaped durable v10 evidence or persisted permission", err)
		}
		switch r.URL.Path {
		case "/api/v1/runners/hosting/storage-create-begin":
			if len(journal.Snapshot().Hosting) != 1 {
				t.Error("creation request preceded durable intent")
			}
			_ = json.NewEncoder(w).Encode(ack)
		case "/api/v1/runners/hosting/storage-materialized":
			if len(journal.Snapshot().Hosting) != 2 {
				t.Error("materialization send preceded durable evidence")
			}
			// The controller has committed ready, but its ACK is lost.
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			t.Error("unexpected workload or Docker request")
		}
	}))
	client := runnerclient.New(server.URL, "token")
	got, err := journalHostingStorageCreate(t.Context(), journal, client, intent)
	if err != nil || !got.CreatePermitted || len(journal.Snapshot().Hosting) != 1 {
		t.Fatalf("creation intent not retained after fresh ACK: %+v %v", got, err)
	}
	if _, err = journalHostingStorageMaterialized(t.Context(), journal, client, evidence); err == nil || len(journal.Snapshot().Hosting) != 2 {
		t.Fatal("lost ACK discarded materialization evidence")
	}
	server.Close()
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	journal, err = runner.OpenAttemptJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	replayed := journal.Snapshot().Hosting[1]
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var proof domain.HostingStorageMaterializedRequest
		if r.URL.Path != "/api/v1/runners/hosting/storage-materialized" || json.NewDecoder(r.Body).Decode(&proof) != nil || domain.HostingDocumentHash(proof) != ready.Receipt.BodyHash {
			t.Error("restart changed immutable materialization receipt")
		}
		_ = json.NewEncoder(w).Encode(ready)
	}))
	defer server.Close()
	if _, err = journalHostingStorageMaterialized(t.Context(), journal, runnerclient.New(server.URL, "token"), replayed); err != nil {
		t.Fatal(err)
	}
	remaining := journal.Snapshot().Hosting
	if len(remaining) != 1 || remaining[0].ID != intent.ID || validateStorageJournalStartup(journal) == nil {
		t.Fatal("materialization ACK erased unresolved workload creation intent")
	}
}

func TestStorageJournalBlocksActualStartupBeforeAnyClaimOrReplay(t *testing.T) {
	intent, _, evidence, _ := storageRuntimeFixture(t)
	for _, entry := range []runner.JournalHosting{intent, evidence} {
		t.Run(entry.Phase, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "journal")
			journal, err := runner.OpenAttemptJournal(directory)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = journal.AppendHosting(entry); err != nil {
				t.Fatal(err)
			}
			if err = journal.Close(); err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
			defer server.Close()
			err = RunContext(t.Context(), Config{Server: server.URL, Token: "token", RunnerID: "runner", WorkDir: t.TempDir(), JournalDir: directory, Execute: true, Once: true})
			if err == nil || !strings.Contains(err.Error(), "storage recovery support") || requests.Load() != 0 {
				t.Fatalf("storage phase silently ignored: requests=%d err=%v", requests.Load(), err)
			}
		})
	}
}

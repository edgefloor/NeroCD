package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nerocd/internal/domain"
)

func storageJournalFixture(t *testing.T) (JournalHosting, JournalHosting) {
	t.Helper()
	a := testJournalAuthority()
	id, name := domain.HostingStorageIdentity("binding_storage")
	storage := domain.HostingStorage{ID: id, ProjectID: "project", ResourceID: "resource", BindingID: "binding_storage", DestinationID: "destination", Kind: "platform_managed", Driver: "local", VolumeName: name, MountPath: "/app/uploads", AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "creator", CreatedAt: a.CreatedAt}
	storage.Hash = storage.DocumentHash()
	release := domain.HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "amd64", DaemonID: "daemon"}
	release.Hash = domain.HostingDocumentHash(release)
	snapshot := domain.HostingSnapshot{ResourceID: storage.ResourceID, BindingID: storage.BindingID, DestinationID: storage.DestinationID, HostPort: 20001, PolicyVersion: domain.HostingOwnedStoragePolicyVersion, Storage: storage.Reference()}
	descriptor := domain.HostingStoragePlan{Reference: storage.Reference(), VolumeName: name, MountPath: storage.MountPath, CreationOperationID: "operation", DaemonID: release.DaemonID}
	descriptor.CreationNonce = domain.HostingStorageCreationNonce(descriptor.Reference, descriptor.CreationOperationID, descriptor.DaemonID)
	plan, err := domain.CompileHostingOwnedStoragePlan(snapshot, release, nil, nil, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	authority := domain.HostingAuthority{OperationID: "operation", RunID: a.RunID, LeaseID: a.LeaseID, Attempt: a.Attempt, Fence: a.Fence}
	create := domain.HostingStorageCreateRequest{HostingAuthority: authority, StorageRef: storage.Reference(), PlanHash: plan.Hash, CreationNonce: descriptor.CreationNonce}
	origin := domain.HostingStorageMaterialization{StorageRef: storage.Reference(), State: "create_admitted", CreationOperationID: authority.OperationID, CreationPlanHash: plan.Hash, CreationNonce: descriptor.CreationNonce, DaemonID: release.DaemonID}
	origin.Labels = domain.ExpectedHostingStorageLabels(storage, origin)
	proof := domain.HostingStorageMaterializedRequest{HostingAuthority: authority, ReceiptID: "storage_receipt", PlanHash: plan.Hash, StorageRef: storage.Reference(), CreationOperationID: origin.CreationOperationID, CreationPlanHash: origin.CreationPlanHash, CreationNonce: origin.CreationNonce, DaemonID: release.DaemonID, VolumeName: name, Driver: "local", Scope: "local", Labels: origin.Labels}
	intent := JournalHosting{ID: "storage_intent", Attempt: a, OperationID: "operation", Phase: "storage_create_intent", Plan: &plan, StorageRef: storage.Reference(), StorageCreate: &create, CreatedAt: a.CreatedAt}
	evidence := JournalHosting{ID: "storage_evidence", Attempt: a, OperationID: "operation", Phase: "storage_materialized", Plan: &plan, StorageRef: storage.Reference(), StorageMaterialized: &proof, StorageMetadata: &storage, CreatedAt: a.CreatedAt}
	return intent, evidence
}

func TestStorageJournalV10PhasesAndDowngrade(t *testing.T) {
	intent, evidence := storageJournalFixture(t)
	apply := cloneJournalHosting(intent)
	apply.ID, apply.Phase, apply.StorageCreate = "apply", "apply_intent", nil
	prepared := cloneJournalHosting(intent)
	prepared.ID, prepared.Phase, prepared.StorageCreate, prepared.Plan = "prepared", "prepared", nil, nil
	prepared.Prepared = &domain.HostingPreparedRequest{HostingAuthority: intent.StorageCreate.HostingAuthority, ReceiptID: "prepared", Release: intent.Plan.Release}
	result := cloneJournalHosting(prepared)
	result.ID, result.Phase, result.Prepared = "result", "result", nil
	result.Result = &domain.HostingCompleteRequest{HostingAuthority: intent.StorageCreate.HostingAuthority, ReceiptID: "result"}
	healthy := domain.HostingHealthy{Snapshot: intent.Plan.Snapshot, Release: intent.Plan.Release, OperationID: "operation", ContainerID: "retained_container", PlanHash: intent.Plan.Hash, Storage: &domain.HostingHealthyStorage{Plan: *intent.Plan.Storage, CreationPlanHash: intent.Plan.Hash}}
	cleanupPlan, err := domain.CompileHostingCleanupPlan(healthy, "newer_operation")
	if err != nil {
		t.Fatal(err)
	}
	cleanup := cloneJournalHosting(intent)
	cleanup.ID, cleanup.OperationID, cleanup.Phase, cleanup.Plan, cleanup.StorageCreate = "cleanup", "cleanup_operation", "cleanup_intent", nil, nil
	cleanup.CleanupPlan = &cleanupPlan
	for _, entry := range []JournalHosting{intent, evidence, apply, prepared, result, cleanup} {
		if err := validateJournalState(journalState{Version: 10, Hosting: []JournalHosting{entry}}); err != nil {
			t.Fatalf("%s v10: %v", entry.Phase, err)
		}
		for version := 1; version <= 9; version++ {
			if validateJournalState(journalState{Version: version, Hosting: []JournalHosting{entry}}) == nil {
				t.Fatalf("version%d accepted %s", version, entry.Phase)
			}
		}
	}
	for version := 1; version <= 8; version++ {
		if err := validateJournalState(journalState{Version: version}); err != nil {
			t.Fatalf("legacy empty journal changed: v%d %v", version, err)
		}
	}
}

func TestStorageJournalRestartImmutableEvidenceAndNoPermission(t *testing.T) {
	intent, evidence := storageJournalFixture(t)
	journal, directory := openBuildJournal(t)
	if _, err := journal.AppendHosting(intent); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.AppendHosting(evidence); err != nil {
		t.Fatal(err)
	}
	if err := journal.DiscardAttempt(intent.Attempt); err == nil {
		t.Fatal("stale authority discarded creation evidence")
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(directory, "journal.json"))
	if err != nil || strings.Contains(string(raw), "create_permitted") {
		t.Fatal("journal stored reusable create permission", err)
	}
	journal, err = OpenAttemptJournal(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	if journal.state.Version != 10 || len(journal.Snapshot().Hosting) != 2 {
		t.Fatal("restart lost storage intent")
	}
	snapshot := journal.Snapshot()
	snapshot.Hosting[1].StorageMetadata.MountPath = "/tampered"
	if journal.Snapshot().Hosting[1].StorageMetadata.MountPath != "/app/uploads" {
		t.Fatal("snapshot aliased durable metadata")
	}
	if err = journal.AckHosting(evidence.ID); err != nil || journal.state.Version != 10 {
		t.Fatal("acknowledging receipt dropped unacked creation intent", err)
	}
	if err = journal.AckHosting(intent.ID); err != nil || journal.state.Version != 8 {
		t.Fatal("settled storage evidence did not permit legacy format", err)
	}
}

func TestStorageJournalRejectsTamperingAndUnexpectedPhase(t *testing.T) {
	_, evidence := storageJournalFixture(t)
	for name, mutate := range map[string]func(*JournalHosting){
		"metadata_hash":    func(e *JournalHosting) { e.StorageMetadata.Hash = "sha256:" + strings.Repeat("0", 64) },
		"metadata_mount":   func(e *JournalHosting) { e.StorageMetadata.MountPath = "/moved" },
		"metadata_missing": func(e *JournalHosting) { e.StorageMetadata = nil },
		"wrong_phase":      func(e *JournalHosting) { e.Phase = "apply_intent" },
		"foreign_label":    func(e *JournalHosting) { e.StorageMaterialized.Labels["io.nerocd.storage.project"] = "other" },
		"stale_fence":      func(e *JournalHosting) { e.StorageMaterialized.Fence = "stale" },
		"missing_ref":      func(e *JournalHosting) { e.StorageRef = domain.HostingStorageRef{} },
	} {
		t.Run(name, func(t *testing.T) {
			entry := cloneJournalHosting(evidence)
			mutate(&entry)
			if validateJournalState(journalState{Version: 10, Hosting: []JournalHosting{entry}}) == nil {
				t.Fatal("tampered storage evidence accepted")
			}
		})
	}
	state := journalState{Version: 10, Hosting: []JournalHosting{evidence}}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	raw = []byte(strings.Replace(string(raw), `"storage_metadata":`, `"create_permitted":true,"storage_metadata":`, 1))
	if decodeStorageJournal(raw, new(journalState)) == nil {
		t.Fatal("unexpected persisted creation permission accepted")
	}
}

func TestStorageJournalCoexistsWithVerifiedProxyV9(t *testing.T) {
	intent, _ := storageJournalFixture(t)
	journal, _ := openBuildJournal(t)
	if _, err := journal.AppendHosting(intent); err != nil {
		t.Fatal(err)
	}
	entry := proxyJournalFixture(t)
	prior := entry.Plan.Snapshot.Routes[0]
	prior.ReleaseOperationID, prior.HostPort = "hop_prior", prior.HostPort-1
	entry.Plan.Snapshot.VerifiedSwitch = &domain.HostingVerifiedSwitch{Version: 1, ExpectedLoadedGeneration: 1, PriorAppliedProxyOperationID: "pop_prior", Routes: []domain.HostingVerifiedSwitchRoute{{RouteID: prior.RouteID, PriorVersion: 4, PriorTarget: prior}}}
	plan, err := domain.CompileHostingProxyPlan(entry.Plan.Snapshot, 1, "daemon_one")
	if err != nil {
		t.Fatal(err)
	}
	entry.Plan, entry.Preparation.PlanHash = plan, plan.Hash
	if _, err = journal.AppendProxy(entry); err != nil || journal.state.Version != 10 {
		t.Fatal("verified proxy downgraded storage journal", err)
	}
	if err = journal.AckHosting(intent.ID); err != nil || journal.state.Version != 9 {
		t.Fatal("remaining verified proxy lost v9", err)
	}
	if err = journal.AckProxy(entry.ID); err != nil || journal.state.Version != 8 {
		t.Fatal("settled proxy failed legacy downgrade", err)
	}
}

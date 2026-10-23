package domain

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"
)

func TestHostingStorageMaterializationIdentityAndFamilies(t *testing.T) {
	id, volume := HostingStorageIdentity("binding")
	storage := HostingStorage{ID: id, ProjectID: "project", ResourceID: "resource", BindingID: "binding", DestinationID: "destination", Kind: "platform_managed", Driver: "local", VolumeName: volume, MountPath: "/app/uploads", AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "actor", CreatedAt: time.Now()}
	storage.Hash = storage.DocumentHash()
	snap := HostingSnapshot{ResourceID: storage.ResourceID, BindingID: storage.BindingID, DestinationID: storage.DestinationID, HostPort: 20001, PolicyVersion: HostingOwnedStoragePolicyVersion, Storage: storage.Reference()}
	release := HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "amd64", DaemonID: "daemon"}
	release.Hash = HostingDocumentHash(release)
	descriptor := HostingStoragePlan{Reference: storage.Reference(), VolumeName: volume, MountPath: storage.MountPath, CreationOperationID: "operation", DaemonID: release.DaemonID}
	descriptor.CreationNonce = HostingStorageCreationNonce(descriptor.Reference, descriptor.CreationOperationID, descriptor.DaemonID)
	plan, err := CompileHostingOwnedStoragePlan(snap, release, nil, nil, descriptor)
	if err != nil || !HostingPlanFamilyMatches(snap, plan) {
		t.Fatalf("compile: %+v %v", plan, err)
	}
	expected := plan
	expected.Hash = ""
	if plan.Hash != HostingDocumentHash(expected) {
		t.Fatal("nonce was not part of frozen plan")
	}
	for _, policy := range []string{HostingPolicyVersion, HostingReleasePolicyVersion, HostingRuntimePolicyVersion, HostingLocalBuildPolicyVersion} {
		bad := snap
		bad.PolicyVersion = policy
		if ValidateHostingStorageSnapshot(bad) == nil {
			t.Fatalf("old policy accepted storage: %s", policy)
		}
	}
	bad := snap
	bad.RuntimeConfiguration = HostingRuntimeConfigurationRef{Generation: 1, Hash: storage.Hash}
	if ValidateHostingStorageSnapshot(bad) == nil {
		t.Fatal("mixed runtime/storage family accepted")
	}
	origin := HostingStorageMaterialization{StorageRef: storage.Reference(), State: "create_admitted", CreationOperationID: "operation", CreationPlanHash: plan.Hash, CreationNonce: descriptor.CreationNonce, DaemonID: release.DaemonID}
	origin.Labels = ExpectedHostingStorageLabels(storage, origin)
	if origin.Validate(storage) != nil || len(origin.Labels) != 11 || origin.Labels["io.nerocd.storage.origin_plan"] != plan.Hash {
		t.Fatal(origin)
	}
	proof := HostingStorageMaterializedRequest{ReceiptID: "receipt", PlanHash: plan.Hash, StorageRef: storage.Reference(), CreationOperationID: origin.CreationOperationID, CreationPlanHash: origin.CreationPlanHash, CreationNonce: origin.CreationNonce, DaemonID: origin.DaemonID, VolumeName: volume, Driver: "local", Scope: "local", Labels: maps.Clone(origin.Labels)}
	if proof.Matches(storage, origin) != nil {
		t.Fatal("exact proof rejected")
	}
	for _, mutate := range []func(*HostingStorageMaterializedRequest){
		func(p *HostingStorageMaterializedRequest) { p.CreationNonce = strings.Repeat("0", 32) }, func(p *HostingStorageMaterializedRequest) { p.Driver = "nfs" }, func(p *HostingStorageMaterializedRequest) { p.Scope = "global" }, func(p *HostingStorageMaterializedRequest) { p.Options = map[string]string{"type": "nfs"} }, func(p *HostingStorageMaterializedRequest) { p.DaemonID = "other" }, func(p *HostingStorageMaterializedRequest) { p.Labels["io.nerocd.storage.id"] = "foreign" }, func(p *HostingStorageMaterializedRequest) { p.Labels["extra"] = "adopted" },
	} {
		changed := proof
		changed.Labels = maps.Clone(proof.Labels)
		mutate(&changed)
		if changed.Matches(storage, origin) == nil {
			t.Fatalf("foreign proof accepted: %+v", changed)
		}
	}
	if (HostingStoragePlan{}).Validate(HostingSnapshot{}, HostingRelease{}) == nil {
		t.Fatal("empty descriptor accepted")
	}
	healthy := HostingHealthy{Snapshot: snap, Release: release, OperationID: "operation", ContainerID: "container", PlanHash: plan.Hash, Storage: &HostingHealthyStorage{Plan: descriptor, CreationPlanHash: plan.Hash}}
	var roundtrip HostingHealthy
	raw, err := json.Marshal(healthy)
	if err != nil || json.Unmarshal(raw, &roundtrip) != nil || ValidateHostingHealthyStorage(roundtrip) != nil || roundtrip.Storage.CreationPlanHash != plan.Hash {
		t.Fatal("healthy evidence did not round trip")
	}
	next := snap
	next.HostPort++
	nextPlan, err := CompileHostingOwnedStoragePlan(next, release, &healthy, nil, descriptor)
	if err != nil || nextPlan.RetainPrevious || nextPlan.Hash == plan.Hash || nextPlan.PreviousHealthy.Storage.CreationPlanHash != plan.Hash {
		t.Fatalf("next release lost original storage identity or permits two writers: %+v %v", nextPlan, err)
	}
	healthy.Snapshot.Storage = HostingStorageRef{}
	healthy.Snapshot.PolicyVersion = HostingReleasePolicyVersion
	if ValidateHostingHealthyStorage(healthy) == nil {
		t.Fatal("old family accepted nonnil storage evidence")
	}
	healthy.Storage = nil
	legacyRaw, err := json.Marshal(healthy)
	if err != nil || strings.Contains(string(legacyRaw), `"storage"`) {
		t.Fatal("nil storage changed legacy healthy representation")
	}
}

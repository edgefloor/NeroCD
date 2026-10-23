package domain

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestHostingStorageRouteContinuityInput(t *testing.T) {
	in := HostingStorageRouteContinuityInput{ExpectedLoadedGeneration: 2, ExpectedLoadedProxyOperationID: "pop_loaded", Routes: []HostingRouteSwitchSelection{{RouteID: "route_b", ExpectedVersion: 2, ExpectedSelectedReleaseOperationID: "hop_old"}, {RouteID: "route_a", ExpectedVersion: 1, ExpectedSelectedReleaseOperationID: "hop_old"}}}
	canonical, err := CanonicalHostingStorageRouteContinuityInput(in)
	if err != nil || canonical.Routes[0].RouteID != "route_a" || in.Routes[0].RouteID != "route_b" {
		t.Fatalf("canonical copy: %+v %v", canonical, err)
	}
	for _, mutate := range []func(*HostingStorageRouteContinuityInput){func(c *HostingStorageRouteContinuityInput) { c.ExpectedLoadedGeneration = 0 }, func(c *HostingStorageRouteContinuityInput) { c.ExpectedLoadedProxyOperationID = "bad/id" }, func(c *HostingStorageRouteContinuityInput) { c.Routes = nil }, func(c *HostingStorageRouteContinuityInput) { c.Routes = append(c.Routes, c.Routes[0]) }, func(c *HostingStorageRouteContinuityInput) { c.Routes[0].ExpectedVersion = 0 }} {
		bad := canonical
		bad.Routes = append([]HostingRouteSwitchSelection(nil), canonical.Routes...)
		mutate(&bad)
		if _, err = CanonicalHostingStorageRouteContinuityInput(bad); err == nil {
			t.Fatalf("invalid scope accepted: %+v", bad)
		}
	}
}

func TestHostingStorageRouteContinuityLegacyHashCompatibility(t *testing.T) {
	routeJSON, routeErr := json.Marshal(HostingRoute{})
	if routeErr != nil || strings.Contains(string(routeJSON), `"reason"`) {
		t.Fatalf("empty route reason changed legacy JSON: %s %v", routeJSON, routeErr)
	}
	// These serialized snapshot and input bytes are the existing public-image
	// contract. An absent continuity descriptor must not add a JSON member.
	snapshotJSON := `{"resource_id":"resource","resource_generation":1,"image_reference":"nginx:latest","binding_id":"binding","destination_id":"destination","server_id":"server","runner_id":"runner","runtime_namespace":"web","container_port":80,"host_port":18080,"readiness":{"path":"/","timeout_seconds":5},"policy_version":"public-image.loopback.v1"}`
	var s HostingSnapshot
	if err := json.Unmarshal([]byte(snapshotJSON), &s); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(s)
	if err != nil || string(raw) != snapshotJSON {
		t.Fatalf("legacy snapshot bytes changed: %s %v", raw, err)
	}
	r := HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "amd64", DaemonID: "daemon"}
	r.Hash = HostingDocumentHash(r)
	plan, err := CompileHostingPlan(s, r, nil)
	if err != nil {
		t.Fatal(err)
	}
	legacy := struct {
		Snapshot        HostingSnapshot `json:"snapshot"`
		Release         HostingRelease  `json:"release"`
		PreviousHealthy *HostingHealthy `json:"previous_healthy,omitempty"`
		CompilerVersion string          `json:"compiler_version"`
		Hash            string          `json:"hash"`
	}{Snapshot: s, Release: r, CompilerVersion: HostingCompilerVersion}
	if plan.Hash != HostingDocumentHash(legacy) {
		t.Fatal("legacy plan hash changed")
	}
	input := HostingDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "request", ContainerPort: 80, HostPort: 18080, Readiness: s.Readiness}
	raw, err = json.Marshal(input)
	if err != nil || strings.Contains(string(raw), "storage_route_continuity") {
		t.Fatalf("legacy input changed: %s %v", raw, err)
	}
	before := s
	s.StorageRouteContinuity.Hash = "sha256:" + strings.Repeat("a", 64)
	if ValidateHostingStorageRouteContinuitySnapshot(s) == nil {
		t.Fatal("nonstorage continuity accepted")
	}
	if _, err = CompileHostingPlan(s, r, nil); err == nil {
		t.Fatal("legacy compiler accepted continuity")
	}
	if reflect.DeepEqual(before, s) {
		t.Fatal("comparable ref failed to bind snapshot")
	}
}

func TestHostingStorageRouteContinuitySnapshotRoundTripAndPlanHash(t *testing.T) {
	id, volume := HostingStorageIdentity("binding")
	storage := HostingStorage{ID: id, ProjectID: "project", ResourceID: "resource", BindingID: "binding", DestinationID: "destination", Kind: "platform_managed", Driver: "local", VolumeName: volume, MountPath: "/uploads", AccessMode: "read_write", Retention: "retain", Generation: 1, CreatedBy: "actor"}
	storage.Hash = storage.DocumentHash()
	s := HostingSnapshot{ResourceID: storage.ResourceID, BindingID: storage.BindingID, DestinationID: storage.DestinationID, HostPort: 20001, PolicyVersion: HostingOwnedStoragePolicyVersion, Storage: storage.Reference()}
	r := HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "amd64", DaemonID: "daemon"}
	r.Hash = HostingDocumentHash(r)
	descriptor := HostingStoragePlan{Reference: storage.Reference(), VolumeName: volume, MountPath: storage.MountPath, CreationOperationID: "hop_origin", DaemonID: r.DaemonID}
	descriptor.CreationNonce = HostingStorageCreationNonce(descriptor.Reference, descriptor.CreationOperationID, descriptor.DaemonID)
	plain, err := CompileHostingOwnedStoragePlan(s, r, nil, nil, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	s.StorageRouteContinuity.Hash = "sha256:" + strings.Repeat("c", 64)
	bound, err := CompileHostingOwnedStoragePlan(s, r, nil, nil, descriptor)
	if err != nil || bound.Hash == plain.Hash {
		t.Fatalf("reservation not hashed into apply plan %+v %v", bound, err)
	}
	raw, err := json.Marshal(bound)
	if err != nil {
		t.Fatal(err)
	}
	var decoded HostingApplyPlan
	if err = json.Unmarshal(raw, &decoded); err != nil || !reflect.DeepEqual(decoded, bound) {
		t.Fatalf("transport dropped binding %+v %v", decoded, err)
	}
	withoutHash := decoded
	withoutHash.Hash = ""
	if HostingDocumentHash(withoutHash) != decoded.Hash {
		t.Fatal("plan hash changed through JSON transport")
	}
	for _, ref := range []HostingStorageRouteContinuityRef{{Hash: "invalid"}, {Hash: "sha256:" + strings.Repeat("c", 63)}} {
		bad := s
		bad.StorageRouteContinuity = ref
		if _, err = CompileHostingOwnedStoragePlan(bad, r, nil, nil, descriptor); err == nil {
			t.Fatal("malformed continuity accepted")
		}
	}
}

package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestHostingRuntimeConfigurationRefFamilies(t *testing.T) {
	release := HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "amd64", DaemonID: "daemon"}
	release.Hash = HostingDocumentHash(release)
	snap := HostingSnapshot{HostPort: 21001, PolicyVersion: HostingRuntimePolicyVersion, RuntimeConfiguration: HostingRuntimeConfigurationRef{Generation: 1, Hash: "sha256:" + strings.Repeat("c", 64)}}
	plan, err := CompileRetainedHostingPlan(snap, release, nil, nil)
	if err != nil || plan.CompilerVersion != HostingRuntimeCompilerVersion || !HostingPlanFamilyMatches(snap, plan) {
		t.Fatalf("runtime plan %+v %v", plan, err)
	}
	for _, mutate := range []func(*HostingSnapshot){
		func(s *HostingSnapshot) { s.RuntimeConfiguration.Generation = 0 },
		func(s *HostingSnapshot) { s.RuntimeConfiguration.Hash = "bad" },
		func(s *HostingSnapshot) { s.RuntimeConfiguration = HostingRuntimeConfigurationRef{} },
		func(s *HostingSnapshot) { s.PolicyVersion = HostingReleasePolicyVersion },
		func(s *HostingSnapshot) { s.PolicyVersion = HostingLocalBuildRuntimePolicyVersion },
	} {
		bad := snap
		mutate(&bad)
		if _, err := CompileRetainedHostingPlan(bad, release, nil, nil); err == nil {
			t.Fatalf("invalid reference accepted %+v", bad)
		}
	}
	plan.CompilerVersion = HostingReleaseCompilerVersion
	if HostingPlanFamilyMatches(snap, plan) {
		t.Fatal("mixed compiler accepted")
	}
}

func TestHostingRuntimeZeroReferenceGoldenCompatibility(t *testing.T) {
	// Removing only the new field recreates the old typed JSON protocol exactly,
	// including nested plan documents and request idempotency hashes.
	input := HostingDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "key", ContainerPort: 80, HostPort: 21001, Readiness: HostingReadiness{Path: "/", TimeoutSeconds: 5}}
	const golden = `{"expected_generation":1,"idempotency_key":"key","container_port":80,"host_port":21001,"readiness":{"path":"/","timeout_seconds":5}}`
	raw, _ := json.Marshal(input)
	if string(raw) != golden {
		t.Fatalf("request protocol %s", raw)
	}
	snapshot := HostingSnapshot{PolicyVersion: HostingReleasePolicyVersion}
	raw, _ = json.Marshal(snapshot)
	const snapshotGolden = `{"resource_id":"","resource_generation":0,"image_reference":"","binding_id":"","destination_id":"","server_id":"","runner_id":"","runtime_namespace":"","container_port":0,"host_port":0,"readiness":{"path":"","timeout_seconds":0},"policy_version":"public-image.release-ports.v1"}`
	if string(raw) != snapshotGolden {
		t.Fatalf("snapshot protocol %s", raw)
	}
	var old any
	if err := json.Unmarshal([]byte(golden), &old); err != nil {
		t.Fatal(err)
	}
	if HostingDocumentHash(input) != "sha256:326377b677066b4994fd47360e567d0a7e63d03508631e2db11104af646eefd9" {
		t.Fatalf("request golden hash %s", HostingDocumentHash(input))
	}
	build := HostingBuildDeploymentInput{ExpectedRuntimeConfigurationGeneration: 7}
	if build.DeploymentInput().ExpectedRuntimeConfigurationGeneration != 7 {
		t.Fatal("conversion dropped generation")
	}
	input.ExpectedRuntimeConfigurationGeneration = -1
	if ValidateHostingDeployment(input) == nil {
		t.Fatal("negative runtime generation accepted")
	}
}

func TestHostingRuntimeLocalBuildPlanKeepsArtifactIdentity(t *testing.T) {
	artifact := localBuildArtifactFixture()
	release, err := NewHostingLocalBuildRelease(artifact)
	if err != nil {
		t.Fatal(err)
	}
	snap := HostingSnapshot{HostPort: 21001, PolicyVersion: HostingLocalBuildRuntimePolicyVersion, RuntimeConfiguration: HostingRuntimeConfigurationRef{Generation: 1, Hash: "sha256:" + strings.Repeat("d", 64)}, LocalBuild: HostingLocalBuildSource{SourceGeneration: 1, ArtifactHash: HostingDocumentHash(artifact), Artifact: artifact}}
	first, err := CompileLocalBuildHostingPlan(snap, release, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	snap.RuntimeConfiguration.Generation++
	snap.RuntimeConfiguration.Hash = "sha256:" + strings.Repeat("e", 64)
	second, err := CompileLocalBuildHostingPlan(snap, release, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash == second.Hash || first.Release != second.Release || first.Snapshot.LocalBuild != second.Snapshot.LocalBuild || second.CompilerVersion != HostingLocalBuildRuntimeCompilerVersion {
		t.Fatal("runtime edit changed artifact or failed to change plan")
	}
	bad := snap
	bad.PolicyVersion = HostingLocalBuildPolicyVersion
	if _, err = CompileLocalBuildHostingPlan(bad, release, nil, nil); err == nil {
		t.Fatal("runtime reference accepted legacy policy")
	}
}

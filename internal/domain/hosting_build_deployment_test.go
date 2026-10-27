package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

func legacyHostingGoldenDocuments(t *testing.T) map[string]any {
	t.Helper()
	release := HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "amd64", DaemonID: "daemon-golden"}
	release.Hash = HostingDocumentHash(release)
	snapshot := HostingSnapshot{ResourceID: "resource", ResourceGeneration: 7, ImageReference: "nginx:1.27", BindingID: "binding", DestinationID: "destination", ServerID: "server", RunnerID: "runner", RuntimeNamespace: "namespace", ContainerPort: 80, HostPort: 20001, Readiness: HostingReadiness{Path: "/", TimeoutSeconds: 5}, PolicyVersion: HostingPolicyVersion}
	plan, err := CompileHostingPlan(snapshot, release, nil)
	if err != nil {
		t.Fatal(err)
	}
	healthy := HostingHealthy{Snapshot: snapshot, OperationID: "old", ContainerID: "container", Release: release, PlanHash: plan.Hash}
	cleanup, err := CompileHostingCleanupPlan(healthy, "current")
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"snapshot": snapshot, "release": release, "apply": plan, "healthy": healthy, "cleanup": cleanup}
}
func TestLegacyHostingDocumentGoldens(t *testing.T) {
	expected := map[string]struct{ hash, json string }{
		"snapshot": {hash: "sha256:698a1e952517659b6aa10f34acedf5459d865614ec6ba37314e55118f2b22f61", json: `{"resource_id":"resource","resource_generation":7,"image_reference":"nginx:1.27","binding_id":"binding","destination_id":"destination","server_id":"server","runner_id":"runner","runtime_namespace":"namespace","container_port":80,"host_port":20001,"readiness":{"path":"/","timeout_seconds":5},"policy_version":"public-image.loopback.v1"}`},
		"release":  {hash: "sha256:f830a59816b255c1aa5a11f6564ee0514cc0205673b5f4461f4b8351e9d278ac", json: `{"canonical_image":"docker.io/library/nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","image_id":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","os":"linux","architecture":"amd64","daemon_id":"daemon-golden","hash":"sha256:c8e810076bb92a7d5223645da4f8f6291480711bedc18366f1398b83f012b34d"}`},
		"apply":    {hash: "sha256:81a1cd709dcb68c0af9d743c3f3f1ceff7f90411f7221bdaab50b0eb3bfca801", json: `{"snapshot":{"resource_id":"resource","resource_generation":7,"image_reference":"nginx:1.27","binding_id":"binding","destination_id":"destination","server_id":"server","runner_id":"runner","runtime_namespace":"namespace","container_port":80,"host_port":20001,"readiness":{"path":"/","timeout_seconds":5},"policy_version":"public-image.loopback.v1"},"release":{"canonical_image":"docker.io/library/nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","image_id":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","os":"linux","architecture":"amd64","daemon_id":"daemon-golden","hash":"sha256:c8e810076bb92a7d5223645da4f8f6291480711bedc18366f1398b83f012b34d"},"compiler_version":"image-hosting.v1","hash":"sha256:8732fbea3060d8a63e1718a7030089eddf6575d46dcbadec14b0b73d9e9e9f08"}`},
		"healthy":  {hash: "sha256:b0c85bb7d42c92b74bf33f5e5de1e99b4d21da65d500abbdc5c00fe6725fd813", json: `{"snapshot":{"resource_id":"resource","resource_generation":7,"image_reference":"nginx:1.27","binding_id":"binding","destination_id":"destination","server_id":"server","runner_id":"runner","runtime_namespace":"namespace","container_port":80,"host_port":20001,"readiness":{"path":"/","timeout_seconds":5},"policy_version":"public-image.loopback.v1"},"operation_id":"old","container_id":"container","release":{"canonical_image":"docker.io/library/nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","image_id":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","os":"linux","architecture":"amd64","daemon_id":"daemon-golden","hash":"sha256:c8e810076bb92a7d5223645da4f8f6291480711bedc18366f1398b83f012b34d"},"plan_hash":"sha256:8732fbea3060d8a63e1718a7030089eddf6575d46dcbadec14b0b73d9e9e9f08"}`},
		"cleanup":  {hash: "sha256:f46dc01640d8b3c822b155ed205cee0a8fca81e204a5d62e65cb19789b5c688a", json: `{"target":{"snapshot":{"resource_id":"resource","resource_generation":7,"image_reference":"nginx:1.27","binding_id":"binding","destination_id":"destination","server_id":"server","runner_id":"runner","runtime_namespace":"namespace","container_port":80,"host_port":20001,"readiness":{"path":"/","timeout_seconds":5},"policy_version":"public-image.loopback.v1"},"operation_id":"old","container_id":"container","release":{"canonical_image":"docker.io/library/nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","image_id":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","os":"linux","architecture":"amd64","daemon_id":"daemon-golden","hash":"sha256:c8e810076bb92a7d5223645da4f8f6291480711bedc18366f1398b83f012b34d"},"plan_hash":"sha256:8732fbea3060d8a63e1718a7030089eddf6575d46dcbadec14b0b73d9e9e9f08"},"current_healthy_operation_id":"current","compiler_version":"image-hosting.release-ports.v1","hash":"sha256:955a5bc9366ef3fc1570b3510266bfe0eef9e1f955478e957a37801203bebd52"}`},
	}
	for name, value := range legacyHostingGoldenDocuments(t) {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != expected[name].json {
				t.Fatalf("legacy JSON changed: %s", raw)
			}
			if got := HostingDocumentHash(value); got != expected[name].hash {
				t.Fatalf("legacy hash changed: %s", got)
			}
		})
	}
}

func localBuildArtifactFixture() HostingBuildArtifact {
	digest := "sha256:" + strings.Repeat("a", 64)
	return HostingBuildArtifact{Origin: "local_build", BuildID: "build", SourceID: "source", InputID: "input", SourceReceiptHash: digest, ContextDigest: digest, RequestDigest: digest, DaemonID: "daemon", ImageID: digest, IndexDigest: "sha256:" + strings.Repeat("b", 64), ManifestDigest: "sha256:" + strings.Repeat("c", 64), ConfigDigest: digest, Platform: "linux/amd64"}
}

func TestLocalBuildReleaseAndPlanBindExactArtifact(t *testing.T) {
	artifact := localBuildArtifactFixture()
	release, err := NewHostingLocalBuildRelease(artifact)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := HostingSnapshot{ResourceID: "resource", ResourceGeneration: 3, BindingID: "binding", DestinationID: "destination", HostPort: 20001, PolicyVersion: HostingLocalBuildPolicyVersion, LocalBuild: HostingLocalBuildSource{SourceGeneration: 2, ArtifactHash: HostingDocumentHash(artifact), Artifact: artifact}}
	plan, err := CompileLocalBuildHostingPlan(snapshot, release, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.CompilerVersion != HostingLocalBuildCompilerVersion || release.CanonicalImage != "" || release.ImageID != artifact.ImageID || release.ImageID == artifact.IndexDigest {
		t.Fatalf("local identities lost: %+v", plan)
	}
	for _, mutate := range []struct {
		name   string
		change func(*HostingSnapshot, *HostingRelease)
	}{
		{"registry reference", func(s *HostingSnapshot, r *HostingRelease) { s.ImageReference = "nginx:1.27" }},
		{"registry canonical image", func(s *HostingSnapshot, r *HostingRelease) { r.CanonicalImage = "nginx@" + artifact.ImageID }},
		{"artifact hash", func(s *HostingSnapshot, r *HostingRelease) {
			s.LocalBuild.ArtifactHash = "sha256:" + strings.Repeat("b", 64)
		}},
		{"artifact identity", func(s *HostingSnapshot, r *HostingRelease) { s.LocalBuild.Artifact.InputID = "other" }},
		{"daemon identity", func(s *HostingSnapshot, r *HostingRelease) { r.DaemonID = "other" }},
		{"platform identity", func(s *HostingSnapshot, r *HostingRelease) { r.Architecture = "arm64" }},
		{"missing local snapshot", func(s *HostingSnapshot, r *HostingRelease) { s.LocalBuild = HostingLocalBuildSource{} }},
		{"missing local release", func(s *HostingSnapshot, r *HostingRelease) { r.LocalBuild = HostingBuildArtifact{} }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			s, r := snapshot, release
			mutate.change(&s, &r)
			if _, err := CompileLocalBuildHostingPlan(s, r, nil, nil); err == nil {
				t.Fatal("accepted substituted artifact")
			}
		})
	}
	healthy := HostingHealthy{Snapshot: snapshot, OperationID: "old", ContainerID: "container", Release: release, PlanHash: plan.Hash}
	cleanup, err := CompileHostingCleanupPlan(healthy, "current")
	if err != nil {
		t.Fatal(err)
	}
	if cleanup.CompilerVersion != HostingLocalBuildCompilerVersion {
		t.Fatal("cleanup lost local capability floor")
	}
}

func TestLocalBuildTaskIsInternalAndCanonical(t *testing.T) {
	for _, mode := range []string{"deploy", "observe", "cleanup"} {
		prefix := map[string]string{"deploy": "run_", "observe": "observe_", "cleanup": "cleanup_"}[mode]
		run := TaskRun{ID: prefix + "operation", RunSpec: RunSpec{Type: RunTypeLocalBuildDeploy, HostingOperationID: "operation", HostingMode: mode}}
		if err := ValidateLocalBuildDeploymentTask(run); err != nil {
			t.Fatal(err)
		}
		if !ContainsHostingExecution(run.RunSpec, Workflow{}) {
			t.Fatal("local task exposed to public workflow")
		}
		run.RunSpec.HostingBuildID = "build"
		if ValidateLocalBuildDeploymentTask(run) == nil {
			t.Fatal("accepted mixed build authority")
		}
	}
	nested := RunSpec{Type: "shell", Workflow: &Workflow{Steps: []WorkflowStep{{RunSpec: RunSpec{Type: RunTypeLocalBuildDeploy}}}}}
	if !ContainsHostingExecution(nested, Workflow{}) {
		t.Fatal("nested internal task accepted")
	}
}

func TestBuildDeploymentContinuityRequiresManagedCompose(t *testing.T) {
	in := HostingBuildDeploymentInput{BuildID: "build", ExpectedSourceGeneration: 1, ExpectedGeneration: 1, IdempotencyKey: "deploy", ContainerPort: 80, HostPort: 18080, Readiness: HostingReadiness{Path: "/", TimeoutSeconds: 5}}
	old := HostingDocumentHash(in)
	raw, err := json.Marshal(in)
	if err != nil || strings.Contains(string(raw), "storage_route_continuity") {
		t.Fatal("absent field changed legacy document", err)
	}
	in.StorageRouteContinuity = &HostingStorageRouteContinuityInput{ExpectedLoadedGeneration: 2, ExpectedLoadedProxyOperationID: "pop_loaded", Routes: []HostingRouteSwitchSelection{{RouteID: "route_web", ExpectedVersion: 1, ExpectedSelectedReleaseOperationID: "hop_prior"}}}
	if ValidateHostingBuildDeployment(in) == nil {
		t.Fatal("ordinary local build accepted route continuity")
	}
	in.ManagedStack = &HostingStackDeploymentInput{AcknowledgeInterruption: true}
	if err = ValidateHostingBuildDeployment(in); err != nil {
		t.Fatal(err)
	}
	if in.DeploymentInput().StorageRouteContinuity != in.StorageRouteContinuity || HostingDocumentHash(in) == old {
		t.Fatal("continuity absent from shared validation/hash")
	}
	in.HostPort = 0
	if ValidateHostingBuildDeployment(in) == nil {
		t.Fatal("continuity accepted without explicit stable port")
	}
}

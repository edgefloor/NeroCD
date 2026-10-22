package domain

import (
	"strings"
	"testing"
)

func TestHostingDeploymentBoundary(t *testing.T) {
	valid := HostingDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "deploy-key", ContainerPort: 80, HostPort: 18080, Readiness: HostingReadiness{Path: "/ready?check=1", TimeoutSeconds: 10}}
	if err := ValidateHostingDeployment(valid); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"https://example.invalid/", "//example.invalid/", "/ready#fragment", "/ready\n", "/%0d%0aHost:evil", "/\\evil", "relative"} {
		t.Run(path, func(t *testing.T) {
			bad := valid
			bad.Readiness.Path = path
			if ValidateHostingDeployment(bad) == nil {
				t.Fatal("unsafe readiness path accepted")
			}
		})
	}
	for _, port := range []int{0, 80, 65536} {
		bad := valid
		bad.HostPort = port
		if ValidateHostingDeployment(bad) == nil {
			t.Fatalf("host port%d accepted", port)
		}
	}
	got, err := NormalizeHostingImage("nginx:1.27")
	if err != nil || got != "docker.io/library/nginx:1.27" {
		t.Fatalf("normalize=%s err=%v", got, err)
	}
	for _, ref := range []string{"nginx", "https://registry/web:v1", "user:pass@registry/web:v1", "sha256:" + strings.Repeat("a", 64)} {
		if _, err := NormalizeHostingImage(ref); err == nil {
			t.Fatalf("invalid reference accepted: %s", ref)
		}
	}
}

func TestHostingReleaseAndPlanHashBindTarget(t *testing.T) {
	release := HostingRelease{CanonicalImage: "docker.io/library/nginx@sha256:" + strings.Repeat("a", 64), ImageID: "sha256:" + strings.Repeat("b", 64), OS: "linux", Architecture: "amd64", DaemonID: "daemon-1"}
	release.Hash = HostingDocumentHash(release)
	if err := ValidateHostingRelease(release); err != nil {
		t.Fatal(err)
	}
	snapshot := HostingSnapshot{BindingID: "binding_1", RunnerID: "runner_1", HostPort: 18080, PolicyVersion: HostingPolicyVersion}
	plan, err := CompileHostingPlan(snapshot, release, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.RunnerID = "runner_2"
	other, err := CompileHostingPlan(snapshot, release, nil)
	if err != nil {
		t.Fatal(err)
	}
	if other.Hash == plan.Hash {
		t.Fatal("target not bound to plan hash")
	}
	release.DaemonID = "daemon-2"
	if ValidateHostingRelease(release) == nil {
		t.Fatal("daemon substitution accepted")
	}
	snapshot.PolicyVersion = "protected-unimplemented"
	if _, err := CompileHostingPlan(snapshot, other.Release, nil); err == nil {
		t.Fatal("unsupported policy bypassed")
	}
}

func TestHostingExecutionDetectionCoversEveryWorkflowRepresentation(t *testing.T) {
	internal := RunSpec{Type: RunTypeImageDeploy}
	for _, spec := range []RunSpec{internal, {Type: "ansible", HostingMode: "deploy"}, {Type: "ansible", HostingOperationID: "hop_internal"}} {
		if !ContainsHostingExecution(spec, Workflow{}) {
			t.Fatal("internal root was public")
		}
		inner := &Workflow{Steps: []WorkflowStep{{ID: "internal", RunSpec: spec}}}
		for range 10000 {
			inner = &Workflow{Steps: []WorkflowStep{{ID: "wrapper", RunSpec: RunSpec{Type: "ansible", Workflow: inner}}}}
		}
		if !ContainsHostingExecution(RunSpec{Type: "ansible", Workflow: inner}, Workflow{}) || !ContainsHostingExecution(RunSpec{Type: "ansible"}, *inner) {
			t.Fatal("deep nested authority was public")
		}
	}
	shared := &Workflow{Steps: []WorkflowStep{{ID: "generic", RunSpec: RunSpec{Type: "ansible"}}}}
	if ContainsHostingExecution(RunSpec{Type: "ansible", Workflow: shared}, *shared) {
		t.Fatal("legitimate generic workflow rejected")
	}
}

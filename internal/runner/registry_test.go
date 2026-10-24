package runner

import (
	"reflect"
	"testing"

	"nerocd/internal/domain"
)

func TestShellAdapterBuildsProcessFromCommandInput(t *testing.T) {
	plan, err := NewRegistry().BuildPlan(domain.TaskRun{
		ID: "run_1",
		RunSpec: domain.RunSpec{
			Type:   "shell",
			Inputs: map[string]any{"command": "echo ok"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Process == nil || !reflect.DeepEqual(plan.Process.Command, []string{"sh", "-c", "echo ok"}) {
		t.Fatalf("unexpected shell process plan: %#v", plan.Process)
	}
}

func TestShellAdapterKeepsExplicitProcess(t *testing.T) {
	explicit := &domain.ProcessSpec{Command: []string{"echo", "explicit"}}
	plan, err := NewRegistry().BuildPlan(domain.TaskRun{ID: "run_1", RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "echo input"}, Process: explicit}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Process != explicit {
		t.Fatalf("explicit process was not preserved: %#v", plan.Process)
	}
}

func TestImagePlanRequiresCanonicalInternalTaskAndNoGenericPrimitives(t *testing.T) {
	valid := domain.TaskRun{ID: "run_hop_internal", RunSpec: domain.RunSpec{Type: domain.RunTypeImageDeploy, HostingOperationID: "hop_internal", HostingMode: "deploy"}}
	plan, err := NewRegistry().BuildPlan(valid)
	if err != nil || plan.RunID != valid.ID || plan.Process != nil || plan.Checkout != nil || len(plan.Secrets) != 0 {
		t.Fatalf("valid internal image plan: %+v %v", plan, err)
	}
	for _, change := range []func(*domain.TaskRun){
		func(run *domain.TaskRun) { run.RunSpec.HostingOperationID = "" },
		func(run *domain.TaskRun) { run.RunSpec.HostingMode = "generic" },
		func(run *domain.TaskRun) { run.ID = "run_other" },
		func(run *domain.TaskRun) {
			run.RunSpec.Process = &domain.ProcessSpec{Command: []string{"fixture-only-not-executed"}}
		},
		func(run *domain.TaskRun) {
			run.Workflow = domain.Workflow{Steps: []domain.WorkflowStep{{ID: "unexpected", RunSpec: domain.RunSpec{Type: "ansible"}}}}
		},
	} {
		bad := valid
		change(&bad)
		if _, err := NewRegistry().BuildPlan(bad); err == nil {
			t.Fatal("invalid image task produced a plan")
		}
	}
	generic := domain.TaskRun{ID: "run_generic", RunSpec: domain.RunSpec{Type: "ansible"}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{{ID: "nested", RunSpec: valid.RunSpec}}}}
	if _, err := NewRegistry().BuildPlan(generic); err == nil {
		t.Fatal("internal nested task produced generic primitives")
	}
}

func TestHostingBuildRegistryTypedPlanIsInternalOnly(t *testing.T) {
	r := NewRegistry()
	run := domain.TaskRun{ID: "build_run_build-a", RunSpec: domain.RunSpec{Type: domain.HostingBuildTaskType, HostingBuildID: "build-a"}}
	plan, err := r.BuildPlan(run)
	if err != nil || plan.Process != nil || plan.Checkout != nil {
		t.Fatalf("typed plan=%+v %v", plan, err)
	}
	run.RunSpec.Process = &domain.ProcessSpec{Command: []string{"unused"}}
	if _, err = r.BuildPlan(run); err == nil {
		t.Fatal("generic process accepted")
	}
}

func TestLocalBuildDeploymentRegistryIsTypedAndEmpty(t *testing.T) {
	registry := NewRegistry()
	run := domain.TaskRun{ID: "run_operation", RunSpec: domain.RunSpec{Type: domain.RunTypeLocalBuildDeploy, HostingOperationID: "operation", HostingMode: "deploy"}}
	plan, err := registry.BuildPlan(run)
	if err != nil || plan.RunID != run.ID || plan.Process != nil || plan.Checkout != nil {
		t.Fatalf("typed local plan: %+v %v", plan, err)
	}
	run.RunSpec.HostingBuildID = "build"
	if _, err = registry.BuildPlan(run); err == nil {
		t.Fatal("mixed authority accepted")
	}
}

func TestDatabaseDeploymentRegistryRequiresCanonicalInternalTask(t *testing.T) {
	registry := NewRegistry()
	valid := domain.TaskRun{ID: "run_hop_database", RunSpec: domain.RunSpec{Type: domain.RunTypeDatabaseDeploy, HostingOperationID: "hop_database", HostingMode: "deploy"}}
	for _, mode := range []string{"deploy", "observe"} {
		t.Run(mode, func(t *testing.T) {
			run := valid
			run.RunSpec.HostingMode = mode
			if mode == "observe" {
				run.ID = "observe_hop_database"
			}
			plan, err := registry.BuildPlan(run)
			if err != nil || !reflect.DeepEqual(plan, domain.RunnerPrimitivePlan{RunID: run.ID}) {
				t.Fatal("canonical database task did not produce an identity-only plan")
			}
		})
	}
	for name, change := range map[string]func(*domain.TaskRun){
		"missing_operation": func(run *domain.TaskRun) { run.RunSpec.HostingOperationID = "" },
		"wrong_identity":    func(run *domain.TaskRun) { run.ID = "run_other" },
		"generic_mode":      func(run *domain.TaskRun) { run.RunSpec.HostingMode = "generic" },
		"cleanup_mode":      func(run *domain.TaskRun) { run.RunSpec.HostingMode = "cleanup" },
		"mixed_build":       func(run *domain.TaskRun) { run.RunSpec.HostingBuildID = "build" },
		"mixed_proxy":       func(run *domain.TaskRun) { run.RunSpec.ProxyOperationID = "proxy" },
		"process": func(run *domain.TaskRun) {
			run.RunSpec.Process = &domain.ProcessSpec{Command: []string{"fixture-only-never-executed"}}
		},
		"repository": func(run *domain.TaskRun) {
			run.RunSpec.Repository = &domain.RepositoryRef{URL: "https://example.invalid/fixture"}
		},
		"inputs": func(run *domain.TaskRun) { run.RunSpec.Inputs = map[string]any{"command": "fixture-only"} },
		"workflow": func(run *domain.TaskRun) {
			run.Workflow.Steps = []domain.WorkflowStep{{ID: "generic", RunSpec: domain.RunSpec{Type: "shell"}}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			bad := valid
			change(&bad)
			if _, err := registry.BuildPlan(bad); err == nil {
				t.Fatal("noncanonical database task reached generic execution")
			}
		})
	}
	for _, specWorkflow := range []bool{false, true} {
		nested := domain.Workflow{Steps: []domain.WorkflowStep{{ID: "database", RunSpec: valid.RunSpec}}}
		run := domain.TaskRun{ID: "run_generic", RunSpec: domain.RunSpec{Type: "shell"}}
		if specWorkflow {
			run.RunSpec.Workflow = &nested
		} else {
			run.Workflow = nested
		}
		if _, err := registry.BuildPlan(run); err == nil {
			t.Fatal("nested database task reached generic execution")
		}
	}
	if registry.Supports(domain.RunTypeDatabaseDeploy) {
		t.Fatal("internal database routing registered a generic adapter")
	}
}

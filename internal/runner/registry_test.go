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

package domain

import (
	"strings"
	"testing"
)

func TestGenericDefinitionBoundsDepthAndCycles(t *testing.T) {
	var workflow *Workflow
	for range MaxWorkflowDepth {
		workflow = &Workflow{Steps: []WorkflowStep{{ID: "step", RunSpec: RunSpec{Type: "ansible", Workflow: workflow}}}}
	}
	if err := ValidateGenericRunDefinition(RunSpec{Type: "ansible", Workflow: workflow}, Workflow{}); err != nil {
		t.Fatalf("exact depth rejected: %v", err)
	}
	workflow = &Workflow{Steps: []WorkflowStep{{ID: "step", RunSpec: RunSpec{Type: "ansible", Workflow: workflow}}}}
	if err := ValidateGenericRunDefinition(RunSpec{Type: "ansible", Workflow: workflow}, Workflow{}); err == nil || !strings.Contains(err.Error(), "nesting") {
		t.Fatalf("depth overflow: %v", err)
	}
	cyclic := &Workflow{}
	cyclic.Steps = []WorkflowStep{{ID: "cycle", RunSpec: RunSpec{Type: "ansible", Workflow: cyclic}}}
	if err := ValidateGenericRunDefinition(RunSpec{Type: "ansible", Workflow: cyclic}, Workflow{}); err == nil {
		t.Fatal("cyclic in-memory workflow accepted")
	}
}

func TestGenericDefinitionPreservesNestedInternalFieldChecks(t *testing.T) {
	for _, spec := range []RunSpec{
		{Type: "ansible", HostingBuildID: "build"}, {Type: "ansible", HostingOperationID: "operation"},
		{Type: "ansible", HostingMode: "deploy"}, {Type: "ansible", ProxyOperationID: "proxy"}, {Type: "ansible", ProxyMode: "apply"},
		{Type: " " + RunTypeComposeDeploy + " "},
	} {
		leaf := &Workflow{Steps: []WorkflowStep{{ID: "internal", RunSpec: spec}}}
		root := RunSpec{Type: "ansible", Workflow: &Workflow{Steps: []WorkflowStep{{ID: "outer", RunSpec: RunSpec{Type: "ansible", Workflow: leaf}}}}}
		if err := ValidateGenericRunDefinition(root, Workflow{}); err == nil {
			t.Fatalf("nested internal fields accepted: %+v", spec)
		}
	}
}

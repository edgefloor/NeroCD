package domain

import (
	"errors"
	"strings"
)

// Generic workflow limits bound normalization and adapter validation before
// either recursively processes an untrusted definition.
const (
	MaxWorkflowSteps = 1024
	MaxWorkflowDepth = 16
)

// ValidateGenericRunDefinition checks the whole document once, including both
// workflow representations. Generic admission cannot grant deployment authority
// or promise step approvals that the scheduler does not implement.
func ValidateGenericRunDefinition(spec RunSpec, workflow Workflow) error {
	internal := func(spec RunSpec) bool {
		return isHostingExecution(spec) || strings.TrimSpace(spec.Type) == RunTypeComposeDeploy
	}
	if internal(spec) {
		return errors.New("deployment execution tasks are internal-only")
	}
	type frame struct {
		workflow *Workflow
		depth    int
	}
	pending := []frame{{&workflow, 1}, {spec.Workflow, 1}}
	seen := map[*Workflow]bool{}
	total := 0
	for len(pending) > 0 {
		item := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if item.workflow == nil {
			continue
		}
		if item.depth > MaxWorkflowDepth {
			return errors.New("workflow nesting exceeds limit")
		}
		if seen[item.workflow] {
			return errors.New("workflow references must not repeat")
		}
		seen[item.workflow] = true
		if len(item.workflow.Steps) > MaxWorkflowSteps-total {
			return errors.New("workflow step count exceeds limit")
		}
		total += len(item.workflow.Steps)
		for _, step := range item.workflow.Steps {
			if step.RequiresAck {
				return errors.New("step requires_ack is unsupported; use run or template requires_ack")
			}
			if internal(step.RunSpec) {
				return errors.New("deployment execution tasks are internal-only")
			}
			if step.RunSpec.Workflow != nil {
				pending = append(pending, frame{step.RunSpec.Workflow, item.depth + 1})
			}
		}
	}
	return nil
}

// ValidateComposeDeploymentTask checks the typed shape. The claim boundary must
// also resolve deployment_id against the persisted deployment and live lease.
// Environment secret bindings remain permitted for fenced secret delivery.
func ValidateComposeDeploymentTask(run TaskRun) error {
	spec := run.RunSpec
	id, ok := spec.Inputs["deployment_id"].(string)
	if spec.Type != RunTypeComposeDeploy || !ok || id == "" || len(id) > 128 || strings.TrimSpace(id) != id || spec.Process != nil || spec.Repository != nil || spec.Workflow != nil || len(spec.Artifacts) != 0 || len(run.Workflow.Steps) != 0 || isHostingExecution(spec) {
		return errors.New("invalid internal compose deployment task")
	}
	for key, value := range spec.Inputs {
		switch key {
		case "deployment_id", "environment_id", "desired_revision_id", "rollback_of_id":
			if text, ok := value.(string); !ok || len(text) > 128 || strings.TrimSpace(text) != text {
				return errors.New("invalid compose deployment input")
			}
		default:
			return errors.New("generic inputs are not permitted on compose deployment tasks")
		}
	}
	return nil
}

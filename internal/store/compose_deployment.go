package store

import (
	"maps"

	"nerocd/internal/domain"
)

// The authorized deployment transaction owns the task's deployment reference.
// Direct repository callers may omit it, but cannot substitute another target.
func canonicalComposeDeploymentRun(deployment domain.Deployment, run domain.TaskRun) (domain.TaskRun, error) {
	inputs := maps.Clone(run.RunSpec.Inputs)
	if inputs == nil {
		inputs = map[string]any{}
	}
	if value, exists := inputs["deployment_id"]; exists && value != deployment.ID {
		return domain.TaskRun{}, ErrConflict
	}
	inputs["deployment_id"] = deployment.ID
	run.RunSpec.Inputs = inputs
	if domain.ValidateComposeDeploymentTask(run) != nil {
		return domain.TaskRun{}, ErrConflict
	}
	return run, nil
}

package runner

import (
	"errors"
	"fmt"
	"strings"

	"nerocd/internal/domain"
)

type Adapter struct {
	Type        string
	Name        string
	Status      string
	Description string
	BuildPlan   func(domain.TaskRun) (domain.RunnerPrimitivePlan, error)
}

type Registry struct {
	adapters map[string]Adapter
}

func NewRegistry() Registry {
	adapters := []Adapter{
		{Type: "shell", Name: "Shell", Status: "implemented", Description: "Builds a local process plan from shell command input and shared runner primitives.", BuildPlan: buildShellPlan},
		{Type: "ansible", Name: "Ansible", Status: "planned", Description: "Thin adapter over git checkout, process execution, artifacts, and secrets."},
		{Type: "opentofu", Name: "OpenTofu", Status: "planned", Description: "Thin adapter over git checkout, process execution, artifacts, and secrets."},
		{Type: "terraform", Name: "Terraform", Status: "planned", Description: "Thin adapter over git checkout, process execution, artifacts, and secrets."},
		{Type: "powershell", Name: "PowerShell", Status: "planned", Description: "Process execution through the runner process primitive."},
		{Type: "python", Name: "Python", Status: "planned", Description: "Process execution through the runner process primitive."},
	}
	registry := Registry{adapters: map[string]Adapter{}}
	for _, adapter := range adapters {
		registry.adapters[adapter.Type] = adapter
	}
	return registry
}

func (r Registry) Supports(runType string) bool {
	_, ok := r.adapters[runType]
	return ok
}

func (r Registry) BuildPlan(run domain.TaskRun) (domain.RunnerPrimitivePlan, error) {
	adapter, ok := r.adapters[run.RunSpec.Type]
	if !ok {
		return domain.RunnerPrimitivePlan{}, fmt.Errorf("run_spec.type %q is not registered", run.RunSpec.Type)
	}
	if adapter.BuildPlan == nil {
		return primitivePlanForRun(run), nil
	}
	return adapter.BuildPlan(run)
}

func (r Registry) Capabilities() []domain.Capability {
	capabilities := []domain.Capability{
		{Name: "Local auth", Status: "scaffolded", Description: "Session-backed local provider with future password and recovery policy."},
		{Name: "Audit", Status: "scaffolded", Description: "Append-only event log for user-visible mutations."},
		{Name: "Runner primitives", Status: "partial", Description: "Process execution, git checkout, artifact checks, and local env secret injection are implemented; artifact storage and production secret backends are still planned."},
	}
	for _, adapter := range r.adapters {
		capabilities = append(capabilities, domain.Capability{Name: adapter.Name, Status: adapter.Status, Description: adapter.Description})
	}
	return capabilities
}

func primitivePlanForRun(run domain.TaskRun) domain.RunnerPrimitivePlan {
	plan := domain.RunnerPrimitivePlan{RunID: run.ID, Process: run.RunSpec.Process, Artifacts: run.RunSpec.Artifacts, Secrets: run.RunSpec.Secrets}
	if run.RunSpec.Repository != nil {
		dest := run.RunSpec.Repository.Path
		if dest == "" {
			dest = "workspace"
		}
		plan.Checkout = &domain.CheckoutPlan{Repository: *run.RunSpec.Repository, DestPath: dest}
	}
	return plan
}

func buildShellPlan(run domain.TaskRun) (domain.RunnerPrimitivePlan, error) {
	plan := primitivePlanForRun(run)
	if plan.Process != nil {
		return plan, nil
	}
	command, ok := stringInput(run.RunSpec.Inputs, "command")
	if !ok {
		return domain.RunnerPrimitivePlan{}, errors.New("shell run requires run_spec.process.command or run_spec.inputs.command")
	}
	plan.Process = &domain.ProcessSpec{Command: []string{"sh", "-c", command}}
	return plan, nil
}

func stringInput(inputs map[string]any, key string) (string, bool) {
	value, ok := inputs[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(text)
	return text, text != ""
}

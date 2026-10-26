package runner

import (
	"errors"
	"fmt"
	"strings"

	"nerocd/internal/domain"
)

// Adapter constructs a runner primitive plan for one run type.
type Adapter struct {
	Type        string
	Name        string
	Status      string
	Description string
	BuildPlan   func(domain.TaskRun) (domain.RunnerPrimitivePlan, error)
}

// Registry maps run types to runner adapters.
type Registry struct {
	adapters map[string]Adapter
}

// NewRegistry returns the built-in runner adapter registry.
func NewRegistry() Registry {
	adapters := []Adapter{
		{Type: domain.RunTypeLocalBuildDeploy, Name: "Local build deployment", Status: "controller", Description: "Internal local artifact plan; execution requires an upgraded agent."},
		{Type: domain.HostingBuildTaskType, Name: "Git application build", Status: "controller", Description: "Internal fenced source/build task; execution requires an upgraded agent."},
		{Type: domain.RunTypeImageDeploy, Name: "Image deployment", Status: "implemented", Description: "Fenced immutable public-image preparation and loopback HTTP deployment."},
		{Type: "shell", Name: "Shell", Status: "implemented", Description: "Builds a local process plan from shell command input and shared runner primitives.", BuildPlan: buildShellPlan},
		{Type: "ansible", Name: "Ansible", Status: "planned", Description: "Thin adapter over git checkout, process execution, artifacts, and secrets."},
		{Type: "opentofu", Name: "OpenTofu", Status: "planned", Description: "Thin adapter over git checkout, process execution, artifacts, and secrets."},
		{Type: "terraform", Name: "Terraform", Status: "planned", Description: "Thin adapter over git checkout, process execution, artifacts, and secrets."},
		{Type: "powershell", Name: "PowerShell", Status: "planned", Description: "Process execution through the runner process primitive."},
		{Type: "python", Name: "Python", Status: "planned", Description: "Process execution through the runner process primitive."},
		{Type: domain.RunTypeComposeDeploy, Name: "Compose deployment", Status: "implemented", Description: "Fenced typed Compose execution with immutable provenance, production policy, health verification, rollback, and reconciliation."},
	}
	registry := Registry{adapters: map[string]Adapter{}}
	for _, adapter := range adapters {
		registry.adapters[adapter.Type] = adapter
	}
	return registry
}

// Supports reports whether runType has an adapter.
func (r Registry) Supports(runType string) bool {
	_, ok := r.adapters[runType]
	return ok
}

// BuildPlan converts run into its runner primitive plan.
func (r Registry) BuildPlan(run domain.TaskRun) (domain.RunnerPrimitivePlan, error) {
	if run.RunSpec.Type == domain.RunTypeStorageBackupRetention {
		if err := domain.ValidateStorageBackupRetentionTask(run); err != nil {
			return domain.RunnerPrimitivePlan{}, err
		}
		return domain.RunnerPrimitivePlan{RunID: run.ID}, nil
	}
	if run.RunSpec.Type == domain.RunTypeDatabaseBackupRetention {
		if domain.ValidateDatabaseBackupRetentionTask(run) != nil {
			return domain.RunnerPrimitivePlan{}, domain.ErrInvalidDatabaseBackupRetention
		}
		return domain.RunnerPrimitivePlan{RunID: run.ID}, nil
	}
	if run.RunSpec.Type == domain.RunTypeStorageBackupTransfer {
		if err := domain.ValidateStorageBackupTransferTask(run); err != nil {
			return domain.RunnerPrimitivePlan{}, err
		}
		return domain.RunnerPrimitivePlan{RunID: run.ID}, nil
	}
	if run.RunSpec.Type == domain.RunTypeDatabaseBackupTransfer {
		if err := domain.ValidateDatabaseBackupTransferTask(run); err != nil {
			return domain.RunnerPrimitivePlan{}, err
		}
		return domain.RunnerPrimitivePlan{RunID: run.ID}, nil
	}
	if run.RunSpec.Type == domain.RunTypeDatabaseBackup {
		if err := domain.ValidateDatabaseBackupTask(run); err != nil {
			return domain.RunnerPrimitivePlan{}, err
		}
		return domain.RunnerPrimitivePlan{RunID: run.ID}, nil
	}
	if run.RunSpec.Type == domain.RunTypeStorageArchive {
		if err := domain.ValidateStorageArchiveTask(run); err != nil {
			return domain.RunnerPrimitivePlan{}, err
		}
		return domain.RunnerPrimitivePlan{RunID: run.ID}, nil
	}
	if run.RunSpec.Type == domain.RunTypeLocalBuildDeploy {
		if err := domain.ValidateLocalBuildDeploymentTask(run); err != nil {
			return domain.RunnerPrimitivePlan{}, err
		}
		return domain.RunnerPrimitivePlan{RunID: run.ID}, nil
	}
	if run.RunSpec.Type == domain.HostingBuildTaskType {
		if err := domain.ValidateHostingBuildTask(run); err != nil {
			return domain.RunnerPrimitivePlan{}, err
		}
		return domain.RunnerPrimitivePlan{RunID: run.ID}, nil
	}
	if run.RunSpec.Type == domain.RunTypeProxyApply {
		if err := domain.ValidateProxyApplyTask(run); err != nil {
			return domain.RunnerPrimitivePlan{}, err
		}
		return domain.RunnerPrimitivePlan{RunID: run.ID}, nil
	}
	if run.RunSpec.Type == domain.RunTypeImageDeploy {
		if err := domain.ValidateImageDeploymentTask(run); err != nil {
			return domain.RunnerPrimitivePlan{}, err
		}
		return domain.RunnerPrimitivePlan{RunID: run.ID}, nil
	}
	if run.RunSpec.Type == domain.RunTypeDatabaseDeploy {
		if err := domain.ValidateDatabaseDeploymentTask(run); err != nil {
			return domain.RunnerPrimitivePlan{}, err
		}
		return domain.RunnerPrimitivePlan{RunID: run.ID}, nil
	}
	if domain.ContainsHostingExecution(run.RunSpec, run.Workflow) {
		return domain.RunnerPrimitivePlan{}, errors.New("hosting execution cannot be embedded in a generic plan")
	}
	adapter, ok := r.adapters[run.RunSpec.Type]
	if !ok {
		return domain.RunnerPrimitivePlan{}, fmt.Errorf("run_spec.type %q is not registered", run.RunSpec.Type)
	}
	if adapter.BuildPlan == nil {
		return primitivePlanForRun(run), nil
	}
	return adapter.BuildPlan(run)
}

// Capabilities returns the capabilities represented by registered adapters.
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

package app

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func genericWorkflow(count int) domain.Workflow {
	workflow := domain.Workflow{}
	for i := 0; i < count; i++ {
		workflow.Steps = append(workflow.Steps, domain.WorkflowStep{ID: fmt.Sprintf("step%d", i), RunSpec: domain.RunSpec{Type: "ansible"}})
	}
	return workflow
}

func TestTemplateValidationBoundsCombinedWorkflowBeforePlanning(t *testing.T) {
	s := newTestService()
	ctx := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	workflow := genericWorkflow(domain.MaxWorkflowSteps / 2)
	spec := domain.RunSpec{Type: "ansible", Workflow: &workflow}
	input := TemplateInput{ProjectID: "proj_platform", Name: "bounded", RunSpec: spec, Workflow: genericWorkflow(domain.MaxWorkflowSteps / 2)}
	template, err := s.CreateTemplate(ctx, input)
	if err != nil {
		t.Fatalf("combined exact limit rejected: %v", err)
	}
	input.Workflow = genericWorkflow(domain.MaxWorkflowSteps/2 + 1)
	if _, err := s.UpdateTemplate(ctx, template.ID, input); !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "step count exceeds") {
		t.Fatalf("combined limit: %v", err)
	}
	// A large chain must be rejected before adapter validation or recursion.
	deep := genericWorkflow(1)
	// Construct independent pointers, not a self-referential graph.
	for range 2000 {
		child := deep
		deep = domain.Workflow{Steps: []domain.WorkflowStep{{ID: "nested", RunSpec: domain.RunSpec{Type: "ansible", Workflow: &child}}}}
	}
	input.RunSpec = domain.RunSpec{Type: "shell", Workflow: &deep}
	input.Workflow = domain.Workflow{}
	if _, err := s.CreateTemplate(ctx, input); !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "nesting exceeds") {
		t.Fatalf("preflight did not bound nesting: %v", err)
	}
	stored, err := s.templates.GetTemplate(ctx, template.ID)
	if err != nil || len(stored.Workflow.Steps) != domain.MaxWorkflowSteps/2 {
		t.Fatal("rejected update mutated template", err)
	}
}

func TestGenericAdmissionRejectsStoredStepApprovalAndComposeDefinitions(t *testing.T) {
	s := newTestService()
	ctx := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	for name, step := range map[string]domain.WorkflowStep{
		"approval": {ID: "step", RequiresAck: true, RunSpec: domain.RunSpec{Type: "ansible"}},
		"compose":  {ID: "step", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy, Process: &domain.ProcessSpec{Command: []string{"not-executed"}}}},
	} {
		t.Run(name, func(t *testing.T) {
			template := domain.TaskTemplate{ID: "legacy_" + name, ProjectID: "proj_platform", Name: "legacy", RunSpec: domain.RunSpec{Type: "ansible"}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{step}}}
			if _, err := s.templates.CreateTemplate(ctx, template); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RequestRun(ctx, template.ID); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("stored unsafe template accepted: %v", err)
			}
		})
	}
	valid, err := s.RequestRunWithSpec(ctx, RunRequestInput{ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: "ansible"}, Workflow: genericWorkflow(2), RequiresAck: true})
	if err != nil || valid.Status != domain.RunWaitingApproval {
		t.Fatal("run-level approval rejected", err)
	}
	if _, err := s.ApproveRun(ctx, valid.ID); err != nil {
		t.Fatal("run-level approval failed", err)
	}
}

type executionClaimRepository struct {
	store.RunnerRepository
	claim domain.ClaimedRun
}

func (r *executionClaimRepository) ClaimRun(context.Context, string, time.Time, time.Duration, ...store.MutationOption) (domain.ClaimedRun, error) {
	return r.claim, nil
}

type executionDeploymentRepository struct {
	store.DeploymentRepository
	plan  domain.DeploymentPlan
	err   error
	calls int
}

func (r *executionDeploymentRepository) DeploymentPlan(_ context.Context, deployment, run, lease, runner string, attempt int, fence string) (domain.DeploymentPlan, error) {
	r.calls++
	if deployment != r.plan.DeploymentID || run != r.plan.RunID || lease != r.plan.LeaseID || attempt != r.plan.Attempt || fence != r.plan.Fence || runner != "runner_claim" {
		return domain.DeploymentPlan{}, store.ErrNotFound
	}
	return r.plan, r.err
}

func TestServiceClaimRequiresTypedComposeAuthorityAndRejectsStepApproval(t *testing.T) {
	for _, mode := range []string{"valid", "process", "unbound", "nested", "approval"} {
		t.Run(mode, func(t *testing.T) {
			s := newTestService()
			ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "runner_claim", Provider: domain.PrincipalRunner})
			claim := domain.ClaimedRun{Run: domain.TaskRun{ID: "run_compose", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy, Inputs: map[string]any{"deployment_id": "dep_claim"}}}, Lease: domain.RunLease{ID: "lease_claim", RunID: "run_compose", RunnerID: "runner_claim", Attempt: 1, Fence: "fence_claim"}}
			deployments := &executionDeploymentRepository{DeploymentRepository: s.deployments, plan: domain.DeploymentPlan{DeploymentID: "dep_claim", ProjectID: "proj_platform", RunID: claim.Run.ID, LeaseID: claim.Lease.ID, Attempt: 1, Fence: claim.Lease.Fence}}
			switch mode {
			case "process":
				claim.Run.RunSpec.Process = &domain.ProcessSpec{Command: []string{"not-executed"}}
			case "unbound":
				deployments.err = store.ErrNotFound
			case "nested":
				claim.Run.Workflow = domain.Workflow{Steps: []domain.WorkflowStep{{ID: "nested", RunSpec: claim.Run.RunSpec}}}
				claim.Run.RunSpec = domain.RunSpec{Type: "ansible"}
			case "approval":
				claim.Run.RunSpec = domain.RunSpec{Type: "ansible"}
				claim.Run.Workflow = domain.Workflow{Steps: []domain.WorkflowStep{{ID: "approval", RequiresAck: true, RunSpec: domain.RunSpec{Type: "ansible"}}}}
			}
			s.runners = &executionClaimRepository{RunnerRepository: s.runners, claim: claim}
			s.deployments = deployments
			got, err := s.ClaimRun(ctx)
			if mode == "valid" {
				if err != nil || !reflect.DeepEqual(got.PrimitivePlan, domain.RunnerPrimitivePlan{RunID: claim.Run.ID}) || deployments.calls != 1 {
					t.Fatalf("typed claim=%+v err=%v calls=%d", got, err, deployments.calls)
				}
			} else if err == nil || !reflect.DeepEqual(got, domain.ClaimedRun{}) {
				t.Fatalf("unsafe claim escaped: %+v %v", got, err)
			}
		})
	}
}

func TestClaimPreservesNestedGenericWorkflow(t *testing.T) {
	s := newTestService()
	ctx := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	leaf := domain.Workflow{Steps: []domain.WorkflowStep{{ID: "leaf", RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "true"}}}}}
	outer := domain.Workflow{Steps: []domain.WorkflowStep{{ID: "outer", RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "true"}, Workflow: &leaf}}}}
	run, err := s.RequestRunWithSpec(ctx, RunRequestInput{ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: "shell", Inputs: map[string]any{"command": "true"}}, Workflow: outer, RunnerTags: []string{"nested-regression"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if _, err := s.runners.RegisterRunner(ctx, domain.Runner{ID: "runner_nested", Name: "nested", Tags: []string{"nested-regression"}, Capabilities: []string{"shell"}, Status: domain.RunnerActive, RegisteredAt: now, LastHeartbeatAt: now}); err != nil {
		t.Fatal(err)
	}
	runnerCtx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "runner_nested", Provider: domain.PrincipalRunner})
	claim, err := s.ClaimRun(runnerCtx)
	if err != nil || claim.Run.ID != run.ID || claim.Run.WorkflowState.CurrentStepID != "outer" || claim.PrimitivePlan.Process == nil {
		t.Fatalf("valid nested workflow claim=%+v err=%v", claim, err)
	}
}

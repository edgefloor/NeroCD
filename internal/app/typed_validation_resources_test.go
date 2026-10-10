package app

import (
	"errors"
	"strings"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestResourceRequestValidationErrorsAreTypedAndDoNotPersist(t *testing.T) {
	mem := newSeededTestStore()
	service, err := NewService(Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	runnerContext := auth.WithPrincipal(t.Context(), auth.Principal{ID: "runner_validation", Provider: domain.PrincipalRunner})

	createdService, err := service.CreateService(admin, ServiceInput{ProjectID: "proj_platform", Name: "validation", RepositoryID: "repo_platform_runbooks", ComposePath: "compose.yml"})
	if err != nil {
		t.Fatalf("CreateService(valid) error = %v", err)
	}
	createdEnvironment, err := service.CreateEnvironment(admin, EnvironmentInput{ServiceID: createdService.ID, Name: "prod", ComposeProject: "validation", TimeoutSeconds: 60})
	if err != nil {
		t.Fatalf("CreateEnvironment(valid) error = %v", err)
	}
	createdRevision, err := service.CreateRevision(admin, RevisionInput{ServiceID: createdService.ID, RequestedRef: "main"})
	if err != nil {
		t.Fatalf("CreateRevision(valid) error = %v", err)
	}
	validTemplateInput := TemplateInput{
		ProjectID: "proj_platform",
		Name:      "validation",
		Kind:      domain.RunTypeShell,
		RunSpec:   domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "true"}},
	}
	createdTemplate, err := service.CreateTemplate(admin, validTemplateInput)
	if err != nil {
		t.Fatalf("CreateTemplate(valid) error = %v", err)
	}

	auditsBefore, err := service.audit.ListAuditEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		want string
		call func() error
	}{
		{name: "project role", want: "project_id is required", call: func() error { _, err := service.ProjectRole(admin, " "); return err }},
		{name: "create project", want: "name is required", call: func() error { _, err := service.CreateProject(admin, ProjectInput{}); return err }},
		{name: "update project ID", want: "project_id is required", call: func() error { _, err := service.UpdateProject(admin, " ", ProjectInput{Name: "name"}); return err }},
		{name: "project member", want: "role must be owner, maintainer, or viewer", call: func() error {
			_, err := service.UpsertProjectMember(admin, ProjectMemberInput{ProjectID: "proj_platform", Email: "viewer@example.local", Role: "operator"})
			return err
		}},
		{name: "access key", want: "kind must be ssh, password, or token", call: func() error {
			_, err := service.CreateAccessKey(admin, AccessKeyInput{ProjectID: "proj_platform", Name: "key", Kind: "certificate", Fingerprint: "fingerprint"})
			return err
		}},
		{name: "inventory", want: "kind must be static or dynamic", call: func() error {
			_, err := service.CreateInventory(admin, InventoryInput{ProjectID: "proj_platform", Name: "inventory", Kind: "manual", Source: "hosts"})
			return err
		}},
		{name: "repository URL", want: `repository url host "169.254.169.254" is not allowed`, call: func() error {
			_, err := service.CreateRepository(admin, RepositoryInput{ProjectID: "proj_platform", Name: "unsafe", URL: "http://169.254.169.254/latest/meta-data"})
			return err
		}},
		{name: "repository policy", want: "repository policy contains invalid or duplicate values", call: func() error {
			_, _, err := canonicalRepositoryPolicy(domain.RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https", "https"}, AllowedHosts: []string{"git.example.local"}})
			return err
		}},
		{name: "repository policy configuration", want: "repository policy configuration_id is invalid", call: func() error {
			_, err := service.ConfigureRepositoryPolicy(admin, RepositoryPolicyInput{ID: "repo_platform_runbooks", ProjectID: "proj_platform", ConfigurationID: "bad"})
			return err
		}},
		{name: "template project update", want: "template project_id cannot be changed", call: func() error {
			input := validTemplateInput
			input.ProjectID = "proj_other"
			_, err := service.UpdateTemplate(admin, createdTemplate.ID, input)
			return err
		}},
		{name: "update template ID", want: "id is required", call: func() error {
			_, err := service.UpdateTemplate(admin, " ", validTemplateInput)
			return err
		}},
		{name: "template project", want: "project_id is required", call: func() error {
			_, err := service.CreateTemplate(admin, TemplateInput{Name: "template"})
			return err
		}},
		{name: "run spec", want: "run_spec.process.command is required", call: func() error {
			_, err := service.normalizeRunSpec(domain.RunSpec{Type: domain.RunTypeShell, Process: &domain.ProcessSpec{}}, "")
			return err
		}},
		{name: "workflow", want: "workflow step id is required", call: func() error {
			_, err := service.normalizeWorkflow(domain.Workflow{Steps: []domain.WorkflowStep{{RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "true"}}}}})
			return err
		}},
		{name: "create service", want: "name, repository_id, and compose_path are required", call: func() error {
			_, err := service.CreateService(admin, ServiceInput{ProjectID: "proj_platform"})
			return err
		}},
		{name: "service project", want: "project_id is required", call: func() error {
			_, err := service.CreateService(admin, ServiceInput{Name: "service", RepositoryID: "repository", ComposePath: "compose.yml"})
			return err
		}},
		{name: "create environment", want: "name, compose_project, and positive timeout_seconds are required", call: func() error {
			_, err := service.CreateEnvironment(admin, EnvironmentInput{ServiceID: createdService.ID})
			return err
		}},
		{name: "create environment service", want: "service_id is required", call: func() error {
			_, err := service.CreateEnvironment(admin, EnvironmentInput{ServiceID: " ", Name: "prod", ComposeProject: "validation", TimeoutSeconds: 60})
			return err
		}},
		{name: "create revision", want: "requested_ref is required", call: func() error {
			_, err := service.CreateRevision(admin, RevisionInput{ServiceID: createdService.ID})
			return err
		}},
		{name: "create revision service", want: "service_id is required", call: func() error {
			_, err := service.CreateRevision(admin, RevisionInput{ServiceID: " ", RequestedRef: "main"})
			return err
		}},
		{name: "list revisions service", want: "service_id is required", call: func() error {
			_, err := service.ListRevisions(admin, " ")
			return err
		}},
		{name: "list deployments environment", want: "environment_id is required", call: func() error {
			_, err := service.ListDeployments(admin, " ")
			return err
		}},
		{name: "confirm deployment ID", want: "id is required", call: func() error {
			_, err := service.ConfirmDeployment(admin, " ")
			return err
		}},
		{name: "create deployment", want: "idempotency_key and desired_revision_id are required", call: func() error {
			_, err := service.CreateDeployment(admin, DeploymentInput{EnvironmentID: createdEnvironment.ID})
			return err
		}},
		{name: "create deployment environment", want: "environment_id is required", call: func() error {
			_, err := service.CreateDeployment(admin, DeploymentInput{EnvironmentID: " ", DesiredRevisionID: createdRevision.ID, IdempotencyKey: "request"})
			return err
		}},
		{name: "server-assigned task run", want: "task_run_id is server assigned", call: func() error {
			runID := "caller-selected"
			_, err := service.CreateDeployment(admin, DeploymentInput{EnvironmentID: createdEnvironment.ID, DesiredRevisionID: createdRevision.ID, IdempotencyKey: "request", TaskRunID: &runID})
			return err
		}},
		{name: "cancel deployment", want: "deployment_id and request_id are required", call: func() error {
			_, err := service.CancelDeployment(admin, DeploymentCancelInput{})
			return err
		}},
		{name: "preassignment failure", want: "id and failure_code are required", call: func() error {
			_, err := service.FailPreAssignmentDeployment(admin, PreAssignmentFailureInput{})
			return err
		}},
		{name: "rollback request", want: "deployment_id, run_id, lease_id, attempt, fence, request_id, expected_status, and failure_code are required", call: func() error {
			_, err := service.FailDeploymentAndCreateRollback(runnerContext, DeploymentFailureRollbackInput{})
			return err
		}},
		{name: "transition request", want: "deployment_id, run_id, lease_id, attempt, fence, transition_key, expected_status, and target_status are required", call: func() error {
			_, err := service.TransitionDeploymentAttempt(runnerContext, DeploymentTransitionInput{})
			return err
		}},
		{name: "provenance request", want: "git_commit and compose_hash must be immutable sha256 values and image_digests must be non-empty repository digests", call: func() error {
			_, err := service.ResolveDeploymentProvenance(runnerContext, ProvenanceResolutionInput{})
			return err
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want errors.Is(ErrInvalidInput)", err)
			}
			if err.Error() != test.want {
				t.Errorf("error text = %q, want %q", err.Error(), test.want)
			}
		})
	}

	t.Run("nonblank identifiers retain lookup behavior", func(t *testing.T) {
		if _, err := service.UpdateTemplate(admin, "tpl_missing", validTemplateInput); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("UpdateTemplate(missing) error = %v, want store.ErrNotFound", err)
		}
		if _, err := service.ListRevisions(admin, "svc_missing"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("ListRevisions(missing) error = %v, want store.ErrNotFound", err)
		}
		deployments, err := service.ListDeployments(admin, "env_missing")
		if err != nil || len(deployments) != 0 {
			t.Fatalf("ListDeployments(missing) = %#v, %v; want empty results without error", deployments, err)
		}
		if _, err := service.ConfirmDeployment(admin, "dep_missing"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("ConfirmDeployment(missing) error = %v, want store.ErrNotFound", err)
		}
	})

	auditsAfter, err := service.audit.ListAuditEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(auditsAfter) != len(auditsBefore) {
		t.Fatalf("rejected requests persisted audit records: before=%d after=%d", len(auditsBefore), len(auditsAfter))
	}
	storedTemplate, err := service.templates.GetTemplate(t.Context(), createdTemplate.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedTemplate.ProjectID != validTemplateInput.ProjectID || storedTemplate.Name != validTemplateInput.Name {
		t.Fatalf("rejected update changed template: %#v", storedTemplate)
	}
}

func TestNestedWorkflowPlanValidationRetainsContextAndCause(t *testing.T) {
	service := newTestService()
	ctx := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	inner := domain.Workflow{Steps: []domain.WorkflowStep{{ID: "inner-step", RunSpec: domain.RunSpec{Type: domain.RunTypeShell}}}}
	outer := domain.Workflow{Steps: []domain.WorkflowStep{{
		ID:      "outer-step",
		RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "true"}, Workflow: &inner},
	}}}
	_, err := service.CreateTemplate(ctx, TemplateInput{
		ProjectID: "proj_platform",
		Name:      "invalid nested plan",
		Kind:      domain.RunTypeShell,
		RunSpec:   domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "true"}, Workflow: &outer},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want errors.Is(ErrInvalidInput)", err)
	}
	var marked invalidInputError
	if !errors.As(err, &marked) {
		t.Fatalf("error = %v, want errors.As(invalidInputError)", err)
	}
	for _, context := range []string{`run_spec.workflow`, `workflow step "outer-step" nested workflow`, `invalid workflow step "inner-step"`} {
		if !strings.Contains(err.Error(), context) {
			t.Errorf("error %q lost context %q", err, context)
		}
	}
	const causeMessage = "shell run requires run_spec.process.command or run_spec.inputs.command"
	if !strings.HasSuffix(err.Error(), causeMessage) {
		t.Fatalf("error %q lost cause message %q", err, causeMessage)
	}
}

func TestInvalidEnvironmentSecretBindingPreservesCause(t *testing.T) {
	cause := errors.New("runner binding cause")
	err := invalidEnvironmentSecretBinding(cause)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want errors.Is(ErrInvalidInput)", err)
	}
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v, want errors.Is(cause)", err)
	}
	if err.Error() != "invalid environment secret binding: runner binding cause" {
		t.Fatalf("error text = %q", err)
	}
}

func TestResourceInternalFailuresRemainUntyped(t *testing.T) {
	service := newTestService()
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))

	_, err := service.CreateProject(t.Context(), ProjectInput{})
	if !errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, ErrInvalidInput) {
		t.Fatalf("authentication error = %v", err)
	}

	service.deployments = nil
	_, err = service.CancelDeployment(admin, DeploymentCancelInput{})
	if err == nil || err.Error() != "deployment control plane is unavailable" || errors.Is(err, ErrInvalidInput) {
		t.Fatalf("dependency error = %v", err)
	}

	service = newTestService()
	_, err = service.CreateDeployment(admin, DeploymentInput{EnvironmentID: "missing", DesiredRevisionID: "missing", IdempotencyKey: "request"})
	if !errors.Is(err, store.ErrNotFound) || errors.Is(err, ErrInvalidInput) {
		t.Fatalf("store lookup error = %v", err)
	}
}

package app

import (
	"errors"
	"strings"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func newTestService() *Service {
	mem := newSeededTestStore()
	return NewService(Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem})
}

func newSeededTestStore() *store.MemoryStore {
	hash, err := auth.HashPassword("admin")
	if err != nil {
		panic(err)
	}
	return store.NewFixtureMemoryStore("admin@example.local", "viewer@example.local", hash, hash)
}

func testPrincipalContext(t *testing.T) auth.Principal {
	t.Helper()
	return auth.Principal{
		ID:       "usr_bootstrap",
		Email:    "admin@example.local",
		Name:     "Bootstrap Admin",
		Roles:    []string{domain.RoleSystemAdmin},
		Provider: domain.PrincipalLocal,
	}
}

func TestCreateRepositoryRejectsUnsafeURL(t *testing.T) {
	service := newTestService()
	ctx := auth.WithPrincipal(t.Context(), testPrincipalContext(t))

	_, err := service.CreateRepository(ctx, RepositoryInput{
		ProjectID: "proj_platform",
		Name:      "Local Metadata",
		URL:       "http://169.254.169.254/latest/meta-data",
		Provider:  domain.ProviderGit,
	})
	if err == nil {
		t.Fatal("expected unsafe repository url error")
	}
	if !strings.Contains(err.Error(), "repository url host") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNormalizeWorkflowRejectsInvalidGraphs(t *testing.T) {
	service := newTestService()
	valid := func(id string, deps ...string) domain.WorkflowStep {
		return domain.WorkflowStep{ID: id, RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "true"}}, DependsOn: deps}
	}
	for name, workflow := range map[string]domain.Workflow{
		"missing_dependency":   {Steps: []domain.WorkflowStep{valid("a", "missing")}},
		"self_cycle":           {Steps: []domain.WorkflowStep{valid("a", "a")}},
		"cycle":                {Steps: []domain.WorkflowStep{valid("a", "b"), valid("b", "a")}},
		"duplicate_dependency": {Steps: []domain.WorkflowStep{valid("a"), valid("b", "a", "a")}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.normalizeWorkflow(workflow); err == nil {
				t.Fatal("accepted invalid workflow graph")
			}
		})
	}
	workflow, err := service.normalizeWorkflow(domain.Workflow{Steps: []domain.WorkflowStep{valid("a"), valid("b", "a")}})
	if err != nil || len(workflow.Steps) != 2 || workflow.Steps[1].DependsOn[0] != "a" {
		t.Fatalf("valid workflow=%#v err=%v", workflow, err)
	}
}

func FuzzNormalizeWorkflowNeverAcceptsCycles(f *testing.F) {
	f.Add(uint8(2))
	f.Add(uint8(5))
	f.Fuzz(func(t *testing.T, nodes uint8) {
		n := int(nodes%12) + 1
		service := newTestService()
		steps := make([]domain.WorkflowStep, n)
		for i := range steps {
			id := string(rune('a' + i))
			deps := []string{}
			if i > 0 {
				deps = append(deps, string(rune('a'+i-1)))
			}
			steps[i] = domain.WorkflowStep{ID: id, DependsOn: deps, RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "true"}}}
		}
		if _, err := service.normalizeWorkflow(domain.Workflow{Steps: steps}); err != nil {
			t.Fatalf("acyclic graph rejected: %v", err)
		}
		steps[0].DependsOn = append(steps[0].DependsOn, string(rune('a'+n-1)))
		if _, err := service.normalizeWorkflow(domain.Workflow{Steps: steps}); err == nil {
			t.Fatal("cycle accepted")
		}
	})
}

func TestConfigureRepositoryPolicyIsAtomicAndIdempotent(t *testing.T) {
	service := newTestService()
	ctx := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	input := RepositoryPolicyInput{ID: "repo_platform_runbooks", ProjectID: "proj_platform", ConfigurationID: "cfg_12345678", Policy: domain.RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https"}, AllowedHosts: []string{"git.example.local"}, CredentialReferenceID: "cred_12345678"}}
	configured, err := service.ConfigureRepositoryPolicy(ctx, input)
	if err != nil || configured.Policy.State != "configured" {
		t.Fatalf("configure = %#v, %v", configured, err)
	}
	if _, err = service.ConfigureRepositoryPolicy(ctx, input); err != nil {
		t.Fatalf("exact retry = %v", err)
	}
	if _, err = service.ConfigureRepositoryPolicy(ctx, RepositoryPolicyInput{ID: input.ID, ProjectID: input.ProjectID, ConfigurationID: input.ConfigurationID, Policy: domain.RepositoryPolicy{Version: 1, State: "configured", Mode: "public", AllowedSchemes: []string{"https"}, AllowedHosts: []string{"other.example.local"}}}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("mismatched replay = %v", err)
	}
	if _, err = service.ConfigureRepositoryPolicy(ctx, RepositoryPolicyInput{ID: input.ID, ProjectID: input.ProjectID, ConfigurationID: "cfg_87654321", Policy: input.Policy}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("second configuration = %v", err)
	}
	audits, err := service.audit.ListAuditEvents(t.Context())
	if err != nil || len(audits) != 1 {
		t.Fatalf("audits = %#v, %v", audits, err)
	}
}

func TestDeploymentMutationsRequireMaintainer(t *testing.T) {
	mem := newSeededTestStore()
	service := NewService(Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem})
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	if _, err := service.UpsertProjectMember(admin, ProjectMemberInput{ProjectID: "proj_platform", Email: "viewer@example.local", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	created, err := service.CreateService(admin, ServiceInput{ProjectID: "proj_platform", Name: "deploy-auth", RepositoryID: "repo_platform_runbooks", ComposePath: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Email: "viewer@example.local", Roles: []string{"user"}, Provider: domain.PrincipalLocal})
	if _, err := service.ListServices(viewer, "proj_platform"); err != nil {
		t.Fatalf("viewer should read own project: %v", err)
	}
	if _, err := service.CreateEnvironment(viewer, EnvironmentInput{ServiceID: created.ID, Name: "prod", ComposeProject: "deploy-auth", TimeoutSeconds: 60}); err != auth.ErrForbidden {
		t.Fatalf("viewer environment mutation = %v", err)
	}
	if _, err := service.CreateRevision(viewer, RevisionInput{ServiceID: created.ID, RequestedRef: "main"}); err != auth.ErrForbidden {
		t.Fatalf("viewer revision mutation = %v", err)
	}
	if _, err := service.CreateEnvironment(admin, EnvironmentInput{ServiceID: created.ID, Name: "prod", ComposeProject: "deploy-auth", TimeoutSeconds: 60}); err != nil {
		t.Fatalf("admin mutation: %v", err)
	}
	if _, err := service.CreateRevision(admin, RevisionInput{ServiceID: created.ID, RequestedRef: "main"}); err != nil {
		t.Fatal(err)
	}
	revisions, err := service.ListRevisions(admin, created.ID)
	if err != nil || len(revisions) != 1 {
		t.Fatalf("revisions: %#v %v", revisions, err)
	}
	environments, err := service.ListEnvironments(admin, created.ID)
	if err != nil || len(environments) != 1 {
		t.Fatalf("environments: %#v %v", environments, err)
	}
	if _, err = service.CreateDeployment(admin, DeploymentInput{EnvironmentID: environments[0].ID, DesiredRevisionID: revisions[0].ID, IdempotencyKey: "no-typed-runner"}); err == nil || !strings.Contains(err.Error(), "compose-deploy") {
		t.Fatalf("typed runner eligibility: %v", err)
	}
	if _, err := mem.RegisterRunner(admin, domain.Runner{ID: "runner_compose", Name: "compose", Tags: []string{}, Capabilities: []string{domain.RunTypeComposeDeploy}, Status: domain.RunnerActive, RegisteredAt: time.Now().UTC(), LastHeartbeatAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	deployment, err := service.CreateDeployment(admin, DeploymentInput{EnvironmentID: environments[0].ID, DesiredRevisionID: revisions[0].ID, IdempotencyKey: "server-run"})
	if err != nil || deployment.TaskRunID == nil {
		t.Fatalf("server-owned deployment run: %#v %v", deployment, err)
	}
}

func TestCreateEnvironmentRejectsInvalidSecretBindingBeforePersistence(t *testing.T) {
	mem := newSeededTestStore()
	service := NewService(Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem})
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	created, err := service.CreateService(admin, ServiceInput{ProjectID: "proj_platform", Name: "binding-validation", RepositoryID: "repo_platform_runbooks", ComposePath: "compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateEnvironment(admin, EnvironmentInput{
		ServiceID: created.ID, Name: "prod", ComposeProject: "binding-validation", TimeoutSeconds: 60,
		SecretBindings: []domain.SecretBinding{{Name: "git", Provider: domain.ProviderRunnerFile, Reference: "cred", Target: "file:git", Version: "v1"}},
	}); err == nil {
		t.Fatal("invalid runner-file target was accepted")
	}
	environments, err := service.ListEnvironments(admin, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(environments) != 0 {
		t.Fatalf("invalid environment was persisted: %#v", environments)
	}
}

func TestRequestRunWithSpecRejectsUnsafeInlineRepositoryURL(t *testing.T) {
	service := newTestService()
	ctx := auth.WithPrincipal(t.Context(), testPrincipalContext(t))

	_, err := service.RequestRunWithSpec(ctx, RunRequestInput{
		ProjectID: "proj_platform",
		RunSpec: domain.RunSpec{
			Type:       domain.RunTypeShell,
			Inputs:     map[string]any{"command": "echo ok"},
			Repository: &domain.RepositoryRef{URL: "file:///tmp/repo"},
		},
	})
	if err == nil {
		t.Fatal("expected unsafe run_spec repository url error")
	}
	if !strings.Contains(err.Error(), "run_spec.repository.url") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCreateTemplateRejectsUnsafeWorkflowRepositoryURL(t *testing.T) {
	service := newTestService()
	ctx := auth.WithPrincipal(t.Context(), testPrincipalContext(t))

	_, err := service.CreateTemplate(ctx, TemplateInput{
		ProjectID: "proj_platform",
		Name:      "Unsafe Workflow",
		Kind:      domain.RunTypeShell,
		RunSpec: domain.RunSpec{
			Type:   domain.RunTypeShell,
			Inputs: map[string]any{"command": "echo ok"},
		},
		Workflow: domain.Workflow{Steps: []domain.WorkflowStep{{
			ID:   "checkout",
			Name: "Checkout",
			RunSpec: domain.RunSpec{
				Type:       domain.RunTypeShell,
				Inputs:     map[string]any{"command": "echo ok"},
				Repository: &domain.RepositoryRef{URL: "https://127.0.0.1/repo.git"},
			},
		}}},
	})
	if err == nil {
		t.Fatal("expected unsafe workflow repository url error")
	}
	if !strings.Contains(err.Error(), "run_spec.repository.url") {
		t.Fatalf("unexpected error: %v", err)
	}
}

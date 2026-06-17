package app

import (
	"strings"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func newTestService() *Service {
	mem := store.NewMemoryStore()
	return NewService(auth.ContextProvider{}, mem, mem, mem, mem, mem, mem, mem, mem, mem, mem, mem)
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

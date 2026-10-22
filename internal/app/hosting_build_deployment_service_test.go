package app

import (
	"errors"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"testing"
)

func TestHostingBuildDeploymentMemoryAuthorizationAndUnavailable(t *testing.T) {
	s, mem := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	env, err := s.CreateProjectEnvironment(admin, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "local-deploy"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := s.CreateResource(admin, ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "local", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	in := domain.HostingBuildDeploymentInput{BuildID: "build", ExpectedGeneration: 1, ExpectedSourceGeneration: 1, IdempotencyKey: "local", ContainerPort: 80, HostPort: 18080, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 5}}
	if _, err = s.RequestHostingBuildDeployment(admin, resource.ID, in); err == nil {
		t.Fatal("image admitted as local build")
	}
	resource.Source = domain.ResourceSource{Kind: "git", Git: &domain.GitResourceSource{SourceID: "source", Generation: 1}}
	s.hosting = buildResourceReader{s.hosting, resource}
	before, _ := mem.ListAuditEvents(t.Context())
	if _, err = s.RequestHostingBuildDeployment(admin, resource.ID, in); !errors.Is(err, hostingjobs.ErrUnavailable) {
		t.Fatal("memory authority", err)
	}
	after, _ := mem.ListAuditEvents(t.Context())
	if len(after) != len(before) {
		t.Fatal("unavailable operation audited as saved")
	}
	if _, err = mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "local_viewer", ProjectID: resource.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Provider: domain.PrincipalLocal})
	if _, err = s.RequestHostingBuildDeployment(viewer, resource.ID, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatal("viewer mutation", err)
	}
	for _, project := range []string{"proj_security", "proj_platform"} {
		account, e := s.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: project, Name: "local-" + project, Role: domain.RoleMaintainer})
		if e != nil {
			t.Fatal(e)
		}
		principal, e := s.AuthenticateSessionToken(t.Context(), account.Token)
		if e != nil {
			t.Fatal(e)
		}
		accountCtx := auth.WithPrincipal(t.Context(), principal)
		_, e = s.RequestHostingBuildDeployment(accountCtx, resource.ID, in)
		if project == "proj_security" {
			if !errors.Is(e, auth.ErrForbidden) {
				t.Fatal("cross-project service account", e)
			}
		} else {
			if !errors.Is(e, hostingjobs.ErrUnavailable) {
				t.Fatal("maintainer service account", e)
			}
			role := domain.RoleViewer
			if _, e = s.UpdateServiceAccount(admin, ServiceAccountUpdateInput{ServiceAccountID: account.ServiceAccount.ID, Role: &role}); e != nil {
				t.Fatal(e)
			}
			if _, e = s.RequestHostingBuildDeployment(accountCtx, resource.ID, in); !errors.Is(e, auth.ErrForbidden) {
				t.Fatal("revoked maintainer scope", e)
			}
		}
	}
	for _, spec := range []domain.RunSpec{{Type: domain.RunTypeLocalBuildDeploy}, {Type: "ansible", Workflow: &domain.Workflow{Steps: []domain.WorkflowStep{{ID: "local", RunSpec: domain.RunSpec{Type: domain.RunTypeLocalBuildDeploy}}}}}} {
		if _, err = s.RequestRunWithSpec(admin, RunRequestInput{ProjectID: resource.ProjectID, RunSpec: spec}); err == nil {
			t.Fatal("public local task accepted")
		}
	}
}

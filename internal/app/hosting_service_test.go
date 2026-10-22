package app

import (
	"errors"
	"nerocd/internal/hostingjobs"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestHostingServiceAuthorizationAndGeneration(t *testing.T) {
	service, mem := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	environment, err := service.CreateProjectEnvironment(admin, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "production"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.CreateProjectEnvironment(admin, ProjectEnvironmentInput{ProjectID: "proj_security", Name: "production"})
	if err != nil {
		t.Fatal(err)
	}
	input := ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: environment.ID, Name: "web", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "ghcr.io/acme/web:v1"}}}
	created, err := service.CreateResource(admin, input)
	if err != nil || created.Generation != 1 || created.CreatedBy != "usr_bootstrap" {
		t.Fatalf("create: %+v %v", created, err)
	}
	name := "renamed"
	updated, err := service.UpdateResource(admin, created.ID, ResourcePatch{Name: &name, ExpectedGeneration: 1})
	if err != nil || updated.Generation != 2 || updated.CreatedAt != created.CreatedAt || updated.CreatedBy != created.CreatedBy {
		t.Fatalf("name update: %+v %v", updated, err)
	}
	source := input.Source
	source.Image.Reference = "nginx:1.27"
	updated, err = service.UpdateResource(admin, created.ID, ResourcePatch{Source: &source, ExpectedGeneration: 2})
	if err != nil || updated.Generation != 3 || updated.Name != name || updated.Source != source {
		t.Fatalf("source update: %+v %v", updated, err)
	}
	if _, err = service.UpdateResource(admin, created.ID, ResourcePatch{Name: &name, ExpectedGeneration: 1}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale: %v", err)
	}
	if _, err = service.ListResources(admin, "", "", store.Page{}); err == nil {
		t.Fatal("missing project accepted")
	}
	if _, err = service.ListProjectEnvironments(admin, "missing", store.Page{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing project: %v", err)
	}
	if _, err = service.CreateProjectEnvironment(admin, ProjectEnvironmentInput{ProjectID: "missing", Name: "test"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("admin missing project: %v", err)
	}
	input.ProjectEnvironmentID = other.ID
	if _, err = service.CreateResource(admin, input); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("cross project environment: %v", err)
	}
	if _, err = service.ListResources(admin, "proj_platform", other.ID, store.Page{}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("cross project list: %v", err)
	}
	input.ProjectEnvironmentID = environment.ID
	if _, err = mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "member_hosting_viewer", ProjectID: "proj_platform", UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleViewer}})
	if _, err = service.GetResource(viewer, created.ID); err != nil {
		t.Fatalf("viewer read: %v", err)
	}
	if _, err = service.CreateResource(viewer, input); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("viewer create: %v", err)
	}
	if _, err = service.UpdateResource(viewer, created.ID, ResourcePatch{Name: &name, ExpectedGeneration: 3}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("viewer update: %v", err)
	}
	if _, err = service.CreateProjectEnvironment(viewer, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "forbidden"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("viewer env create: %v", err)
	}
	outsider := auth.WithPrincipal(t.Context(), auth.Principal{ID: "outsider", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleMaintainer}})
	if _, err = service.GetResource(outsider, created.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("outsider read: %v", err)
	}
	// Persisted memberships, not global role claims, grant project writes.
	if _, err = mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "member_hosting", ProjectID: "proj_platform", UserID: "usr_viewer", Role: domain.RoleMaintainer}); err != nil {
		t.Fatal(err)
	}
	input.Name = "maintained"
	if _, err = service.CreateResource(viewer, input); err != nil {
		t.Fatalf("maintainer write: %v", err)
	}
	account, err := service.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: "proj_platform", Name: "hosting bot", Role: domain.RoleMaintainer})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.AuthenticateSessionToken(t.Context(), account.Token)
	if err != nil {
		t.Fatal(err)
	}
	accountCtx := auth.WithPrincipal(t.Context(), principal)
	input.Name = "bot-web"
	botResource, err := service.CreateResource(accountCtx, input)
	if err != nil || botResource.CreatedBy != account.ServiceAccount.ID {
		t.Fatalf("bot resource: %+v %v", botResource, err)
	}
	if _, err = service.ListResources(accountCtx, "proj_security", "", store.Page{}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("bot cross project: %v", err)
	}
	if _, err = service.CreateProjectEnvironment(accountCtx, ProjectEnvironmentInput{ProjectID: "proj_security", Name: "bot"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("bot cross project create: %v", err)
	}
	role := domain.RoleViewer
	if _, err = service.UpdateServiceAccount(admin, ServiceAccountUpdateInput{ServiceAccountID: account.ServiceAccount.ID, Role: &role}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.GetResource(accountCtx, created.ID); err != nil {
		t.Fatalf("viewer bot read: %v", err)
	}
	if _, err = service.UpdateResource(accountCtx, created.ID, ResourcePatch{Name: &name, ExpectedGeneration: 3}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("viewer bot write: %v", err)
	}
}

func TestHostingServiceRejectsInvalidConfiguration(t *testing.T) {
	service := newTestService()
	ctx := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	environment, err := service.CreateProjectEnvironment(ctx, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	base := ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: environment.ID, Name: "web", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}}
	for _, tc := range []struct {
		name   string
		change func(*ResourceInput)
	}{
		{"name", func(in *ResourceInput) { in.Name = "" }},
		{"kind", func(in *ResourceInput) { in.Kind = "database" }},
		{"source kind", func(in *ResourceInput) { in.Source.Kind = "git" }},
		{"image", func(in *ResourceInput) { in.Source.Image.Reference = "nginx" }},
		{"environment", func(in *ResourceInput) { in.ProjectEnvironmentID = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			tc.change(&in)
			if _, err := service.CreateResource(ctx, in); err == nil {
				t.Fatal("invalid configuration accepted")
			}
		})
	}
	created, err := service.CreateResource(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, patch := range []ResourcePatch{{ExpectedGeneration: 1}, {Name: hostingString("web")}, {ExpectedGeneration: 1, Name: hostingString("")}, {ExpectedGeneration: 1, Source: &domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "https://registry/web:v1"}}}} {
		if _, err := service.UpdateResource(ctx, created.ID, patch); err == nil {
			t.Fatalf("invalid patch accepted: %+v", patch)
		}
	}
	got, err := service.GetResource(ctx, created.ID)
	if err != nil || got.Generation != 1 {
		t.Fatalf("invalid writes changed generation: %+v %v", got, err)
	}
}

func hostingString(value string) *string { return &value }

func TestGitResourcePublicPolicyAndMemoryUnavailable(t *testing.T) {
	service, mem := newServiceAccountTestService(t)
	ctx := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	env, err := service.CreateProjectEnvironment(ctx, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "git"})
	if err != nil {
		t.Fatal(err)
	}
	input := ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "Git web", Kind: "application", Source: domain.ResourceSource{Kind: "git", Git: &domain.GitResourceSource{GitResourceConfiguration: domain.GitResourceConfiguration{RepositoryURL: "https://github.com/acme/web", RequestedRef: "main", CredentialKind: "public", Settings: domain.HostingBuildSettings{Strategy: "dockerfile"}}}}}
	if _, err = service.CreateResource(ctx, input); !errors.Is(err, hostingjobs.ErrUnavailable) {
		t.Fatal("memory pretended durable save", err)
	}
	items, err := mem.ListResources(ctx, env.ProjectID, env.ID, store.Page{})
	if err != nil || len(items.Items) != 0 {
		t.Fatal("partial memory mutation", items, err)
	}
	for _, url := range []string{"https://gitlab.com/acme/web", "ssh://git@github.com/acme/web", "http://github.com/acme/web", "https://user:password@github.com/acme/web"} {
		input.Source.Git.RepositoryURL = url
		if _, err = service.CreateResource(ctx, input); err == nil || errors.Is(err, hostingjobs.ErrUnavailable) {
			t.Fatal("invalid public source admitted", url, err)
		}
	}
	input.Source.Git.RepositoryURL = "https://github.com/acme/web"
	input.Source.Git.CredentialKind = "https_basic"
	if _, err = service.CreateResource(ctx, input); !errors.Is(err, hostingjobs.ErrUnavailable) {
		t.Fatal("private Git memory must refuse durable save", err)
	}
	input.Source.Git.CredentialKind = "ssh_private_key"
	if _, err = service.CreateResource(ctx, input); err == nil || errors.Is(err, hostingjobs.ErrUnavailable) {
		t.Fatal("unsupported SSH accepted", err)
	}
}

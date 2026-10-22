package app

import (
	"errors"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestHostingRuntimeServiceMemoryAndAuthority(t *testing.T) {
	service, mem := newServiceAccountTestService(t)
	ctx := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	env, err := service.CreateProjectEnvironment(ctx, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "runtime"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := service.CreateResource(ctx, ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: env.ID, Name: "runtime", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	in := domain.HostingRuntimeConfigurationInput{ExpectedResourceGeneration: 1, RequestID: "0123456789abcdef0123456789abcdef", Entries: []domain.HostingRuntimeEntry{}}
	if _, err = service.GetHostingRuntimeConfiguration(ctx, resource.ID); !errors.Is(err, store.ErrHostingRuntimeConfigurationUnavailable) {
		t.Fatalf("memory GET %v", err)
	}
	if _, err = service.PutHostingRuntimeConfiguration(ctx, resource.ID, in); !errors.Is(err, store.ErrHostingRuntimeConfigurationUnavailable) {
		t.Fatalf("memory PUT %v", err)
	}
	if _, err = mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "runtime_member", ProjectID: "proj_platform", UserID: "usr_viewer", Role: domain.RoleMaintainer}); err != nil {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleMaintainer}})
	if _, err = service.PutHostingRuntimeConfiguration(viewer, resource.ID, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("maintainer %v", err)
	}
	if _, err = service.GetHostingRuntimeConfiguration(viewer, resource.ID); !errors.Is(err, store.ErrHostingRuntimeConfigurationUnavailable) {
		t.Fatalf("maintainer read %v", err)
	}
	if supportedRuntimeConfiguration(domain.Resource{Kind: "compose", Source: resource.Source}) == nil {
		t.Fatal("unsupported kind accepted")
	}
}

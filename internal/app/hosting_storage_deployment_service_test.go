package app

import (
	"context"
	"errors"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type storageDeploymentServiceRepository struct {
	store.HostingRepository
	calls               int
	resource, operation string
	err                 error
}

func (r *storageDeploymentServiceRepository) HostingStorageDeploymentOptions(_ context.Context, resource, _ string) (domain.HostingStorageDeploymentOptions, error) {
	r.calls++
	if r.resource != "" {
		resource = r.resource
	}
	return domain.HostingStorageDeploymentOptions{ResourceID: resource, Reason: "no_previous_healthy", Routes: []domain.HostingRouteSwitchRoute{}}, r.err
}

func (r *storageDeploymentServiceRepository) HostingStorageRouteContinuityStatus(_ context.Context, resource, _, operation string) (domain.HostingStorageRouteContinuityStatus, error) {
	r.calls++
	if r.resource != "" {
		resource = r.resource
	}
	if r.operation != "" {
		operation = r.operation
	}
	return domain.HostingStorageRouteContinuityStatus{ResourceID: resource, OperationID: operation, State: "proxy_pending", Reason: "pending"}, r.err
}

func TestHostingStorageDeploymentServiceAuthorizationAndScope(t *testing.T) {
	s, mem := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	env, err := s.CreateProjectEnvironment(admin, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "storage options"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateResource(admin, ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: env.ID, Name: "uploads", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	repo := &storageDeploymentServiceRepository{HostingRepository: mem}
	s.hosting = repo
	if _, err = s.HostingStorageDeploymentOptions(t.Context(), r.ID); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("anonymous: %v", err)
	}
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Provider: domain.PrincipalLocal})
	if _, err = s.HostingStorageRouteContinuityStatus(viewer, r.ID, "hop_next"); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("foreign viewer: %v", err)
	}
	if repo.calls != 0 {
		t.Fatal("unauthorized repository access")
	}
	if _, err = mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "member_storage_options", ProjectID: r.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.HostingStorageDeploymentOptions(viewer, r.ID); err != nil {
		t.Fatal(err)
	}
	account, err := s.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: r.ProjectID, Name: "storage viewer", Role: domain.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	machine := auth.WithPrincipal(t.Context(), auth.Principal{ID: account.ServiceAccount.ID, Provider: domain.PrincipalServiceAccount})
	if _, err = s.HostingStorageRouteContinuityStatus(machine, r.ID, "hop_next"); err != nil {
		t.Fatal(err)
	}
	foreign, err := s.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: "proj_security", Name: "foreign storage viewer", Role: domain.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.HostingStorageDeploymentOptions(auth.WithPrincipal(t.Context(), auth.Principal{ID: foreign.ServiceAccount.ID, Provider: domain.PrincipalServiceAccount}), r.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("foreign machine: %v", err)
	}
	before := repo.calls
	if _, err = s.HostingStorageRouteContinuityStatus(admin, r.ID, "bad/operation"); err == nil {
		t.Fatal("malformed operation accepted")
	}
	if repo.calls != before {
		t.Fatal("malformed input reached repository")
	}
	repo.resource = "private_resource"
	if _, err = s.HostingStorageDeploymentOptions(admin, r.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("wrong resource: %v", err)
	}
	repo.resource, repo.operation = "", "hop_other"
	if _, err = s.HostingStorageRouteContinuityStatus(admin, r.ID, "hop_next"); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("wrong operation: %v", err)
	}
	s.hosting = mem
	if _, err = s.HostingStorageDeploymentOptions(admin, r.ID); !errors.Is(err, store.ErrHostingStorageUnavailable) {
		t.Fatalf("missing repository: %v", err)
	}
}

package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type storageServiceRepository struct {
	store.HostingRepository
	actor, resource string
	input           domain.HostingStorageInput
	event           domain.AuditEvent
	reads, writes   int
}

func (r *storageServiceRepository) GetHostingStorage(_ context.Context, actor, resource string) (domain.HostingStorage, error) {
	r.actor, r.resource = actor, resource
	r.reads++
	return domain.HostingStorage{}, nil
}
func (r *storageServiceRepository) CreateHostingStorage(_ context.Context, actor, resource string, in domain.HostingStorageInput, event domain.AuditEvent) (domain.HostingStorage, error) {
	r.actor, r.resource, r.input, r.event = actor, resource, in, event
	r.writes++
	return domain.HostingStorage{}, nil
}

func TestHostingStorageServiceAuthorityAndDelegation(t *testing.T) {
	s, mem := newServiceAccountTestService(t)
	owner := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	env, err := s.CreateProjectEnvironment(owner, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "storage"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := s.CreateResource(owner, ResourceInput{ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "uploads", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	in := domain.HostingStorageInput{RequestID: strings.Repeat("a", 32), ExpectedResourceGeneration: 1, MountPath: "/app/uploads"}
	if _, err = s.CreateHostingStorage(owner, resource.ID, in); !errors.Is(err, store.ErrHostingStorageUnavailable) {
		t.Fatalf("memory write: %v", err)
	}
	if _, err = s.GetHostingStorage(owner, resource.ID); !errors.Is(err, store.ErrHostingStorageUnavailable) {
		t.Fatalf("memory read: %v", err)
	}
	repo := &storageServiceRepository{HostingRepository: s.hosting}
	s.hosting = repo
	if _, err = s.CreateHostingStorage(t.Context(), resource.ID, in); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("anonymous: %v", err)
	}
	if _, err = s.CreateHostingStorage(owner, resource.ID, in); err != nil {
		t.Fatal(err)
	}
	if repo.actor != "usr_bootstrap" || repo.resource != resource.ID || repo.input != in || repo.event.Action != "hosting.storage.created" || repo.event.ActorID != repo.actor || repo.writes != 1 {
		t.Fatalf("delegation: %+v", repo)
	}
	if _, err = mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "storage_viewer", ProjectID: env.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleViewer}})
	if _, err = s.GetHostingStorage(viewer, resource.ID); err != nil {
		t.Fatalf("viewer read: %v", err)
	}
	if _, err = s.CreateHostingStorage(viewer, resource.ID, in); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("viewer write: %v", err)
	}
	bad := in
	bad.MountPath = "/dev/shm"
	if _, err = s.CreateHostingStorage(owner, resource.ID, bad); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid path: %v", err)
	}
	if repo.writes != 1 || repo.reads != 1 {
		t.Fatalf("rejected input reached persistence: %+v", repo)
	}
	if supportedHostingStorage(domain.Resource{Kind: "application", Source: domain.ResourceSource{Kind: "git"}}) == nil || supportedHostingStorage(domain.Resource{Kind: "compose", Source: resource.Source}) == nil {
		t.Fatal("unsupported source/kind accepted")
	}
}

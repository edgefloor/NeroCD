package app

import (
	"context"
	"errors"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type bindingServiceRepository struct {
	store.HostingRepository
	puts, gets int
	actor, id  string
	audit      domain.AuditEvent
}

func (r *bindingServiceRepository) PutHostingDatabaseBinding(_ context.Context, actor, id string, _ domain.HostingDatabaseBindingInput, event domain.AuditEvent) (domain.HostingDatabaseBinding, error) {
	r.puts++
	r.actor, r.id, r.audit = actor, id, event
	return domain.HostingDatabaseBinding{ApplicationResourceID: id}, nil
}
func (r *bindingServiceRepository) GetHostingDatabaseBinding(_ context.Context, actor, id string) (domain.HostingDatabaseBinding, error) {
	r.gets++
	r.actor, r.id = actor, id
	return domain.HostingDatabaseBinding{ApplicationResourceID: id}, nil
}

func TestHostingDatabaseBindingServiceAuthorityAndPendingBoundary(t *testing.T) {
	s, mem := newServiceAccountTestService(t)
	ctx := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	environment, err := s.CreateProjectEnvironment(ctx, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "binding"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := s.CreateResource(ctx, ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: environment.ID, Name: "binding app", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	in := domain.HostingDatabaseBindingInput{DatabaseResourceID: "db", ExpectedResourceGeneration: 1, ExpectedDatabaseGeneration: 1, RequestID: "0123456789abcdef0123456789abcdef"}
	if _, err = s.PutHostingDatabaseBinding(ctx, resource.ID, in); !errors.Is(err, store.ErrHostingDatabaseBindingUnavailable) {
		t.Fatalf("memory pending put: %v", err)
	}
	if _, err = s.GetHostingDatabaseBinding(ctx, resource.ID); !errors.Is(err, store.ErrHostingDatabaseBindingUnavailable) {
		t.Fatalf("memory pending get: %v", err)
	}
	repository := &bindingServiceRepository{HostingRepository: s.hosting}
	s.hosting = repository
	if _, err = s.PutHostingDatabaseBinding(ctx, resource.ID, in); err != nil || repository.puts != 1 || repository.actor != testPrincipalContext(t).ID || repository.id != resource.ID || repository.audit.ActorID != repository.actor {
		t.Fatalf("current actor was not inferred at service boundary: %v", err)
	}
	if _, err = mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "binding_viewer", ProjectID: "proj_platform", UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Provider: domain.PrincipalLocal})
	if _, err = s.PutHostingDatabaseBinding(viewer, resource.ID, in); !errors.Is(err, auth.ErrForbidden) || repository.puts != 1 {
		t.Fatal("viewer mutation reached repository")
	}
	if _, err = s.GetHostingDatabaseBinding(viewer, resource.ID); err != nil || repository.gets != 1 || repository.actor != "usr_viewer" {
		t.Fatalf("viewer reference read failed: %v", err)
	}
	in.RequestID = "bad"
	if _, err = s.PutHostingDatabaseBinding(ctx, resource.ID, in); err == nil || repository.puts != 1 {
		t.Fatal("invalid selection reached repository")
	}
	if _, err = s.PutHostingDatabaseBinding(t.Context(), resource.ID, in); err == nil || repository.puts != 1 {
		t.Fatal("unauthenticated mutation reached repository")
	}
}

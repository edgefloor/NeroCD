package app

import (
	"context"
	"errors"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

type databaseExecutionServiceRepository struct {
	store.RunRepository
	calls int
	err   error
}

func (r *databaseExecutionServiceRepository) RequestHostingDatabaseDeployment(_ context.Context, id, resource, actor string, in domain.HostingDatabaseDeploymentInput, event domain.AuditEvent) (domain.HostingOperation, error) {
	r.calls++
	if event.ActorID != actor || event.TargetID != id || in.Validate() != nil {
		return domain.HostingOperation{}, store.ErrConflict
	}
	return domain.HostingOperation{ID: id, ResourceID: resource, CreatedBy: actor, State: "queued"}, r.err
}

func TestHostingDatabaseExecutionServiceAuthorizationAndOptionalRepository(t *testing.T) {
	s, metadata, admin, input := databaseServiceFixture(t)
	view, err := s.CreateHostingDatabase(admin, input)
	if err != nil {
		t.Fatal(err)
	}
	in := domain.HostingDatabaseDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "database"}
	if _, err = s.RequestHostingDatabaseDeployment(admin, view.Resource.ID, in); !errors.Is(err, hostingjobs.ErrUnavailable) {
		t.Fatalf("memory manufactured deployment: %v", err)
	}
	repo := &databaseExecutionServiceRepository{RunRepository: s.runs}
	s.runs = repo
	if _, err = s.RequestHostingDatabaseDeployment(t.Context(), view.Resource.ID, in); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("anonymous deployment: %v", err)
	}
	for _, role := range []string{domain.RoleViewer, domain.RoleMaintainer, domain.RoleOwner} {
		account, e := s.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: input.ProjectID, Name: "database executor " + role, Role: role})
		if e != nil {
			t.Fatal(e)
		}
		ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: account.ServiceAccount.ID, Provider: domain.PrincipalServiceAccount})
		op, e := s.RequestHostingDatabaseDeployment(ctx, view.Resource.ID, in)
		if role == domain.RoleViewer {
			if !errors.Is(e, auth.ErrForbidden) {
				t.Fatalf("viewer deployment: %v", e)
			}
		} else if e != nil || op.CreatedBy != account.ServiceAccount.ID || op.ResourceID != view.Resource.ID {
			t.Fatalf("maintainer deployment attribution: %+v %v", op, e)
		}
	}
	if repo.calls != 2 {
		t.Fatalf("unauthorized request reached repository: %d", repo.calls)
	}
	if _, err = s.RequestHostingDatabaseDeployment(admin, view.Resource.ID, domain.HostingDatabaseDeploymentInput{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid request: %v", err)
	}
	metadata.view.Resource.Kind = "application"
	if _, err = s.RequestHostingDatabaseDeployment(admin, view.Resource.ID, in); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("application entered database family: %v", err)
	}
	if repo.calls != 2 {
		t.Fatal("invalid request reached repository")
	}
}

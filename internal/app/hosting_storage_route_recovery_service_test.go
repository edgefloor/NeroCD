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

type storageRouteRecoveryRepository struct {
	store.HostingRepository
	calls      int
	actor      string
	wrongScope bool
	err        error
}

func (r *storageRouteRecoveryRepository) RecoverHostingStorageRouteContinuity(_ context.Context, resource, operation, actor string, _ domain.HostingStorageRouteRecoveryInput, _ domain.AuditEvent) (domain.HostingStorageRouteContinuityStatus, error) {
	r.calls++
	r.actor = actor
	if r.wrongScope {
		resource = "private_resource"
	}
	return domain.HostingStorageRouteContinuityStatus{ResourceID: resource, OperationID: operation, State: "proxy_pending", Reason: "pending"}, r.err
}

func TestHostingStorageRouteRecoveryService(t *testing.T) {
	s, mem := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	env, err := s.CreateProjectEnvironment(admin, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "storage recovery"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := s.CreateResource(admin, ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: env.ID, Name: "uploads", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	repo := &storageRouteRecoveryRepository{HostingRepository: mem}
	s.hosting = repo
	in := domain.HostingStorageRouteRecoveryInput{ExpectedDescriptorHash: "sha256:" + strings.Repeat("a", 64), ExpectedCompleteReceiptID: "complete_next", ExpectedCompleteReceiptHash: "sha256:" + strings.Repeat("b", 64)}
	if _, err = s.RecoverHostingStorageRouteContinuity(t.Context(), resource.ID, "hop_next", in); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("anonymous: %v", err)
	}
	for _, role := range []string{domain.RoleViewer, domain.RoleMaintainer} {
		account, e := s.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: resource.ProjectID, Name: "recovery " + role, Role: role})
		if e != nil {
			t.Fatal(e)
		}
		ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: account.ServiceAccount.ID, Provider: domain.PrincipalServiceAccount})
		_, e = s.RecoverHostingStorageRouteContinuity(ctx, resource.ID, "hop_next", in)
		if role == domain.RoleViewer && !errors.Is(e, auth.ErrForbidden) || role == domain.RoleMaintainer && e != nil {
			t.Fatalf("%s: %v", role, e)
		}
	}
	before := repo.calls
	if _, err = s.RecoverHostingStorageRouteContinuity(admin, resource.ID, "bad/id", in); err == nil {
		t.Fatal("invalid operation accepted")
	}
	if _, err = s.RecoverHostingStorageRouteContinuity(admin, resource.ID, "hop_next", domain.HostingStorageRouteRecoveryInput{}); err == nil {
		t.Fatal("missing evidence accepted")
	}
	if repo.calls != before {
		t.Fatal("invalid input reached repository")
	}
	if _, err = s.RecoverHostingStorageRouteContinuity(admin, resource.ID, "hop_next", in); err != nil || repo.actor != "usr_bootstrap" {
		t.Fatalf("admin: %s %v", repo.actor, err)
	}
	repo.wrongScope = true
	if _, err = s.RecoverHostingStorageRouteContinuity(admin, resource.ID, "hop_next", in); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("foreign response: %v", err)
	}
	repo.wrongScope = false
	repo.err = store.ErrConflict
	if _, err = s.RecoverHostingStorageRouteContinuity(admin, resource.ID, "hop_next", in); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("conflict lost: %v", err)
	}
	s.hosting = mem
	if _, err = s.RecoverHostingStorageRouteContinuity(admin, resource.ID, "hop_next", in); !errors.Is(err, store.ErrHostingStorageUnavailable) {
		t.Fatalf("missing repository: %v", err)
	}
}

package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/hostingjobs"
	"nerocd/internal/store"
)

type databaseUpgradeServiceRepository struct {
	store.RunRepository
	calls int
}

func (r *databaseUpgradeServiceRepository) RequestHostingDatabaseNetworkUpgrade(_ context.Context, id, resource, actor string, in domain.HostingDatabaseUpgradeInput, event domain.AuditEvent) (domain.HostingOperation, error) {
	r.calls++
	if event.ActorID != actor || event.TargetID != id || in.Validate() != nil {
		return domain.HostingOperation{}, store.ErrConflict
	}
	return domain.HostingOperation{ID: id, ResourceID: resource, CreatedBy: actor, State: "queued"}, nil
}

func TestHostingDatabaseUpgradeServiceAuthorization(t *testing.T) {
	s, metadata, admin, input := databaseServiceFixture(t)
	view, err := s.CreateHostingDatabase(admin, input)
	if err != nil {
		t.Fatal(err)
	}
	in := domain.HostingDatabaseUpgradeInput{ExpectedGeneration: 1, CaptureID: "hnc_" + strings.Repeat("a", 32), ExpectedOriginHash: "sha256:" + strings.Repeat("b", 64), IdempotencyKey: "upgrade"}
	if _, err = s.RequestHostingDatabaseNetworkUpgrade(admin, view.Resource.ID, in); !errors.Is(err, hostingjobs.ErrUnavailable) {
		t.Fatal("memory store manufactured upgrade authority", err)
	}
	repo := &databaseUpgradeServiceRepository{RunRepository: s.runs}
	s.runs = repo
	if _, err = s.RequestHostingDatabaseNetworkUpgrade(t.Context(), view.Resource.ID, in); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatal("anonymous upgrade accepted", err)
	}
	for _, role := range []string{domain.RoleViewer, domain.RoleMaintainer, domain.RoleOwner} {
		account, e := s.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: input.ProjectID, Name: "database upgrade " + role, Role: role})
		if e != nil {
			t.Fatal(e)
		}
		ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: account.ServiceAccount.ID, Provider: domain.PrincipalServiceAccount})
		op, e := s.RequestHostingDatabaseNetworkUpgrade(ctx, view.Resource.ID, in)
		if role != domain.RoleOwner {
			if !errors.Is(e, auth.ErrForbidden) {
				t.Fatal("non-owner upgrade accepted", role, e)
			}
		} else if e != nil || op.CreatedBy != account.ServiceAccount.ID || op.ResourceID != view.Resource.ID {
			t.Fatal("owner upgrade attribution mismatch", e)
		}
	}
	if _, err = s.RequestHostingDatabaseNetworkUpgrade(admin, view.Resource.ID, domain.HostingDatabaseUpgradeInput{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("invalid request accepted", err)
	}
	metadata.view.Resource.Kind = "application"
	if _, err = s.RequestHostingDatabaseNetworkUpgrade(admin, view.Resource.ID, in); !errors.Is(err, ErrInvalidInput) {
		t.Fatal("application accepted as database", err)
	}
	if repo.calls != 1 {
		t.Fatalf("unauthorized or invalid requests reached repository: %d", repo.calls)
	}
}

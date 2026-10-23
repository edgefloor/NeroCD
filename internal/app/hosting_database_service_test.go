package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/secretcrypto"
	"nerocd/internal/store"
)

type databaseServiceRepository struct {
	store.HostingRepository
	view           domain.HostingDatabase
	records        []store.HostingDatabaseCredentialRecord
	calls, updates int
	wrongScope     bool
}

func (r *databaseServiceRepository) CreateHostingDatabase(_ context.Context, actor string, in domain.HostingDatabaseCreateInput, records []store.HostingDatabaseCredentialRecord, _ domain.AuditEvent) (domain.HostingDatabase, error) {
	r.calls++
	r.records = records
	recipe, err := domain.HostingDatabaseRecipeFor(in.Engine, in.RecipeVersion)
	if err != nil {
		return domain.HostingDatabase{}, err
	}
	r.view = domain.HostingDatabase{Resource: domain.Resource{ID: domain.HostingDatabaseResourceID(in.ProjectID, in.RequestID), ProjectID: in.ProjectID, ProjectEnvironmentID: in.ProjectEnvironmentID, Name: in.Name, Kind: "database", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: recipe.ImageReference}}, Generation: 1, CreatedBy: actor}, Engine: in.Engine, RecipeVersion: in.RecipeVersion, RuntimeState: domain.HostingDatabaseUnqualified}
	for _, record := range records {
		r.view.Credentials = append(r.view.Credentials, record.Reference)
	}
	view := r.view
	if r.wrongScope {
		view.Resource.ProjectID = "private_project"
	}
	return view, nil
}
func (r *databaseServiceRepository) GetHostingDatabase(context.Context, string, string) (domain.HostingDatabase, error) {
	return r.view, nil
}
func (r *databaseServiceRepository) GetResource(ctx context.Context, id string) (domain.Resource, error) {
	if id == r.view.Resource.ID {
		return r.view.Resource, nil
	}
	return r.HostingRepository.GetResource(ctx, id)
}
func (r *databaseServiceRepository) UpdateResource(_ context.Context, item domain.Resource, _ int64, _ ...store.MutationOption) (domain.Resource, error) {
	r.updates++
	return item, nil
}

func databaseServiceFixture(t *testing.T) (*Service, *databaseServiceRepository, context.Context, domain.HostingDatabaseCreateInput) {
	t.Helper()
	s, mem := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	env, err := s.CreateProjectEnvironment(admin, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "database unit"})
	if err != nil {
		t.Fatal(err)
	}
	repo := &databaseServiceRepository{HostingRepository: mem}
	s.hosting = repo
	s.secretKeyring = managedTestRing(t, "database-test")
	in := domain.HostingDatabaseCreateInput{RequestID: strings.Repeat("a", 32), ProjectID: env.ProjectID, ProjectEnvironmentID: env.ID, Name: "database", DestinationID: "dest_database", Engine: "postgresql", RecipeVersion: domain.HostingDatabasePostgresRecipe}
	return s, repo, admin, in
}

func TestHostingDatabaseServiceAuthorityAndGeneratedCredentials(t *testing.T) {
	s, repo, admin, in := databaseServiceFixture(t)
	if _, err := s.CreateHostingDatabase(t.Context(), in); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("anonymous creation: %v", err)
	}
	for _, role := range []string{domain.RoleViewer, domain.RoleMaintainer, domain.RoleOwner} {
		account, err := s.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: in.ProjectID, Name: "database " + role, Role: role})
		if err != nil {
			t.Fatal(err)
		}
		ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: account.ServiceAccount.ID, Provider: domain.PrincipalServiceAccount})
		_, err = s.CreateHostingDatabase(ctx, in)
		if role != domain.RoleOwner && !errors.Is(err, auth.ErrForbidden) || role == domain.RoleOwner && err != nil {
			t.Fatalf("%s database authority: %v", role, err)
		}
	}
	if repo.calls != 1 {
		t.Fatalf("unauthorized creation reached repository: %d", repo.calls)
	}
	values := map[string]bool{}
	for _, record := range repo.records {
		plain, err := s.secretKeyring.Open(secretcrypto.Binding{ProjectID: in.ProjectID, SecretID: record.Reference.SecretID, Version: 1}, record.Record.Envelope)
		if err != nil || len(plain) != 43 || values[string(plain)] {
			t.Fatalf("generated credential length=%d error=%v", len(plain), err)
		}
		values[string(plain)] = true
	}
	invalid := in
	invalid.Name = ""
	if _, err := s.CreateHostingDatabase(admin, invalid); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("invalid input: %v", err)
	}
	s.secretKeyring = nil
	if _, err := s.CreateHostingDatabase(admin, in); !errors.Is(err, ErrManagedSecretsUnavailable) {
		t.Fatalf("missing keyring: %v", err)
	}
	s.secretKeyring = managedTestRing(t, "database-test")
	repo.wrongScope = true
	if _, err := s.CreateHostingDatabase(admin, in); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("foreign repository response: %v", err)
	}
}

func TestHostingDatabaseServiceClosesGenericDeployment(t *testing.T) {
	s, repo, admin, in := databaseServiceFixture(t)
	view, err := s.CreateHostingDatabase(admin, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RequestHostingDeployment(admin, view.Resource.ID, domain.HostingDeploymentInput{}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("generic database deployment: %v", err)
	}
	if _, err = s.GetHostingDatabase(admin, view.Resource.ID); err != nil {
		t.Fatal(err)
	}
	repo.view.RuntimeState = "ready"
	if _, err = s.GetHostingDatabase(admin, view.Resource.ID); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("unqualified metadata claimed ready: %v", err)
	}
}

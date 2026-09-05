package app

import (
	"errors"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func newServiceAccountTestService(t *testing.T) (*Service, *store.MemoryStore) {
	t.Helper()
	mem := newSeededTestStore()
	service, err := NewService(Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	return service, mem
}

func TestServiceAccountLifecycleUsesStableScopedIdentity(t *testing.T) {
	service, _ := newServiceAccountTestService(t)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))

	created, err := service.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: "proj_platform", Name: "release bot", Role: domain.RoleMaintainer})
	if err != nil {
		t.Fatal(err)
	}
	if created.Token == "" || created.Credential.ExpiresAt == nil || !created.Credential.ExpiresAt.Equal(now.Add(defaultManagedCredentialLifetime)) {
		t.Fatalf("created credential = %#v", created)
	}
	if created.Credential.ServiceAccountID == nil || *created.Credential.ServiceAccountID != created.ServiceAccount.ID || len(created.Credential.Roles) != 0 {
		t.Fatalf("managed credential scope = %#v", created.Credential)
	}

	principal, err := service.AuthenticateSessionToken(t.Context(), created.Token)
	if err != nil {
		t.Fatal(err)
	}
	if principal.ID != created.ServiceAccount.ID || principal.Provider != domain.PrincipalServiceAccount || len(principal.Roles) != 0 {
		t.Fatalf("principal = %#v", principal)
	}
	accountCtx := auth.WithPrincipal(t.Context(), principal)
	role, err := service.ProjectRole(accountCtx, "proj_platform")
	if err != nil || role.Role != domain.RoleMaintainer || !role.CanRun || role.CanAdmin {
		t.Fatalf("project role = %#v, %v", role, err)
	}
	projects, err := service.ListProjects(accountCtx)
	if err != nil || len(projects) != 1 || projects[0].ID != "proj_platform" {
		t.Fatalf("projects = %#v, %v", projects, err)
	}
	run, err := service.RequestRunWithSpec(accountCtx, RunRequestInput{ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "true"}}})
	if err != nil || run.RequestedBy != created.ServiceAccount.ID {
		t.Fatalf("run = %#v, %v", run, err)
	}
	if _, err := service.RequestRunWithSpec(accountCtx, RunRequestInput{ProjectID: "proj_security", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "true"}}}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("cross-project run error = %v", err)
	}
	if _, err := service.ListServiceAccounts(accountCtx, "proj_platform", store.Page{}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("self-management error = %v", err)
	}

	rotated, err := service.RotateServiceAccountCredential(admin, ServiceAccountRotateInput{ServiceAccountID: created.ServiceAccount.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateSessionToken(t.Context(), created.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("old credential error = %v", err)
	}
	rotatedPrincipal, err := service.AuthenticateSessionToken(t.Context(), rotated.Token)
	if err != nil || rotatedPrincipal.ID != created.ServiceAccount.ID {
		t.Fatalf("rotated principal = %#v, %v", rotatedPrincipal, err)
	}
	viewerRole := domain.RoleViewer
	updatedName := "read-only bot"
	updated, err := service.UpdateServiceAccount(admin, ServiceAccountUpdateInput{ServiceAccountID: created.ServiceAccount.ID, Name: &updatedName, Role: &viewerRole})
	if err != nil || updated.Name != updatedName || updated.Role != domain.RoleViewer {
		t.Fatalf("updated = %#v, %v", updated, err)
	}
	if _, err := service.RequestRunWithSpec(auth.WithPrincipal(t.Context(), rotatedPrincipal), RunRequestInput{ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "true"}}}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("viewer run error = %v", err)
	}
	if _, err := service.RevokeServiceAccount(admin, ServiceAccountRevokeInput{ServiceAccountID: created.ServiceAccount.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.AuthenticateSessionToken(t.Context(), rotated.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("revoked credential error = %v", err)
	}
}

func TestServiceAccountManagementRequiresHumanOwnerAndBoundedExpiry(t *testing.T) {
	service, _ := newServiceAccountTestService(t)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	tooLate := now.Add(maxManagedCredentialLifetime + time.Second)
	if _, err := service.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: "proj_platform", Name: "late", Role: domain.RoleViewer, ExpiresAt: &tooLate}); err == nil {
		t.Fatal("accepted credential expiry beyond one year")
	}
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Email: "viewer@example.local", Name: "Viewer", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleUser}})
	if _, err := service.CreateServiceAccount(viewer, ServiceAccountCreateInput{ProjectID: "proj_security", Name: "viewer bot", Role: domain.RoleViewer}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("viewer create error = %v", err)
	}

	legacy, err := service.CreateAPIToken(admin, APITokenInput{Name: "legacy runner admin", Kind: domain.TokenKindServiceAccount, Roles: []string{domain.RoleRunnerAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	legacyPrincipal, err := service.AuthenticateSessionToken(t.Context(), legacy.Token)
	if err != nil || legacyPrincipal.ID != legacy.APIToken.ID || legacyPrincipal.Provider != domain.PrincipalAPIToken || len(legacyPrincipal.Roles) != 1 || legacyPrincipal.Roles[0] != domain.RoleRunnerAdmin {
		t.Fatalf("legacy principal = %#v, %v", legacyPrincipal, err)
	}
}

func TestOwnerOffboardingRevokesOwnedServiceAccounts(t *testing.T) {
	service, _ := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	if _, err := service.UpsertProjectMember(admin, ProjectMemberInput{ProjectID: "proj_security", Email: "viewer@example.local", Role: domain.RoleOwner}); err != nil {
		t.Fatal(err)
	}
	owner := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Email: "viewer@example.local", Name: "Security Viewer", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleUser}})
	created, err := service.CreateServiceAccount(owner, ServiceAccountCreateInput{ProjectID: "proj_security", Name: "security bot", Role: domain.RoleMaintainer})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.OffboardUser(admin, OffboardUserInput{UserID: "usr_viewer"})
	if err != nil {
		t.Fatal(err)
	}
	if result.RevokedServiceAccounts != 1 || result.RevokedAPITokens != 1 {
		t.Fatalf("offboarding result = %#v", result)
	}
	if _, err := service.AuthenticateSessionToken(t.Context(), created.Token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("owned service account credential error = %v", err)
	}
}

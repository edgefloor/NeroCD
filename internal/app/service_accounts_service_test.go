package app

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func newServiceAccountTestService(t *testing.T) (*Service, *store.MemoryStore) {
	t.Helper()
	mem := newSeededTestStore()
	service, err := NewService(Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	return service, mem
}

type serviceAccountAccessProbe struct {
	store.ServiceAccountRepository
	scopeCalls int
}

func (p *serviceAccountAccessProbe) ServiceAccountProjectRole(context.Context, string) (string, string, error) {
	p.scopeCalls++
	return "", "", errors.New("unexpected service-account scope lookup")
}

type projectMemberAccessProbe struct {
	store.ProjectMemberRepository
	lookupCalls int
}

func (p *projectMemberAccessProbe) ListProjectMembers(context.Context, string) ([]domain.ProjectMember, error) {
	p.lookupCalls++
	return nil, errors.New("unexpected project-member lookup")
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

func TestServiceAccountValidationUsesTypedInvalidInputWithoutPersistence(t *testing.T) {
	service, mem := newServiceAccountTestService(t)
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	service.SetClock(func() time.Time { return now })
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	created, err := service.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: "proj_platform", Name: "validation bot", Role: domain.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}

	emptyName := " "
	invalidRole := domain.RoleSystemAdmin
	expired := now
	tooLate := now.Add(maxManagedCredentialLifetime + time.Second)
	validName := "valid name"
	for _, test := range []struct {
		name string
		call func() error
	}{
		{name: "list missing project", call: func() error { _, err := service.ListServiceAccounts(admin, " ", store.Page{}); return err }},
		{name: "create missing project", call: func() error {
			_, err := service.CreateServiceAccount(admin, ServiceAccountCreateInput{Name: "bot", Role: domain.RoleViewer})
			return err
		}},
		{name: "create invalid name", call: func() error {
			_, err := service.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: "proj_platform", Name: " ", Role: domain.RoleViewer})
			return err
		}},
		{name: "create invalid role", call: func() error {
			_, err := service.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: "proj_platform", Name: "bot", Role: invalidRole})
			return err
		}},
		{name: "create expired credential", call: func() error {
			_, err := service.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: "proj_platform", Name: "bot", Role: domain.RoleViewer, ExpiresAt: &expired})
			return err
		}},
		{name: "create unbounded credential", call: func() error {
			_, err := service.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: "proj_platform", Name: "bot", Role: domain.RoleViewer, ExpiresAt: &tooLate})
			return err
		}},
		{name: "update missing account", call: func() error {
			_, err := service.UpdateServiceAccount(admin, ServiceAccountUpdateInput{Name: &validName})
			return err
		}},
		{name: "update missing fields", call: func() error {
			_, err := service.UpdateServiceAccount(admin, ServiceAccountUpdateInput{ServiceAccountID: created.ServiceAccount.ID})
			return err
		}},
		{name: "update invalid name", call: func() error {
			_, err := service.UpdateServiceAccount(admin, ServiceAccountUpdateInput{ServiceAccountID: created.ServiceAccount.ID, Name: &emptyName})
			return err
		}},
		{name: "update invalid role", call: func() error {
			_, err := service.UpdateServiceAccount(admin, ServiceAccountUpdateInput{ServiceAccountID: created.ServiceAccount.ID, Role: &invalidRole})
			return err
		}},
		{name: "rotate missing account", call: func() error {
			_, err := service.RotateServiceAccountCredential(admin, ServiceAccountRotateInput{})
			return err
		}},
		{name: "rotate expired credential", call: func() error {
			_, err := service.RotateServiceAccountCredential(admin, ServiceAccountRotateInput{ServiceAccountID: created.ServiceAccount.ID, ExpiresAt: &expired})
			return err
		}},
		{name: "revoke missing account", call: func() error { _, err := service.RevokeServiceAccount(admin, ServiceAccountRevokeInput{}); return err }},
	} {
		t.Run(test.name, func(t *testing.T) {
			accountsBefore, err := mem.ListServiceAccounts(t.Context(), "proj_platform", store.Page{})
			if err != nil {
				t.Fatal(err)
			}
			auditsBefore, err := mem.ListAuditEvents(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = test.call()
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want errors.Is(ErrInvalidInput)", err)
			}
			accountsAfter, err := mem.ListServiceAccounts(t.Context(), "proj_platform", store.Page{})
			if err != nil {
				t.Fatal(err)
			}
			auditsAfter, err := mem.ListAuditEvents(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(accountsAfter, accountsBefore) || !reflect.DeepEqual(auditsAfter, auditsBefore) {
				t.Fatalf("validation changed persisted state: accounts=%#v audits=%#v", accountsAfter, auditsAfter)
			}
		})
	}
}

func TestServiceAccountValidationPreservesAuthenticationPrecedence(t *testing.T) {
	service, mem := newServiceAccountTestService(t)
	for _, test := range []struct {
		name string
		call func() error
	}{
		{name: "list", call: func() error { _, err := service.ListServiceAccounts(t.Context(), "", store.Page{}); return err }},
		{name: "create", call: func() error {
			_, err := service.CreateServiceAccount(t.Context(), ServiceAccountCreateInput{})
			return err
		}},
		{name: "update", call: func() error {
			_, err := service.UpdateServiceAccount(t.Context(), ServiceAccountUpdateInput{})
			return err
		}},
		{name: "rotate", call: func() error {
			_, err := service.RotateServiceAccountCredential(t.Context(), ServiceAccountRotateInput{})
			return err
		}},
		{name: "revoke", call: func() error {
			_, err := service.RevokeServiceAccount(t.Context(), ServiceAccountRevokeInput{})
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if !errors.Is(err, auth.ErrUnauthenticated) || errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want unauthenticated without invalid-input marker", err)
			}
		})
	}
	if audits, err := mem.ListAuditEvents(t.Context()); err != nil || len(audits) != 0 {
		t.Fatalf("unauthenticated validation audits = %#v, %v", audits, err)
	}
}

func TestCategoricallyIneligiblePrincipalsAreDeniedBeforeValidationOrLookup(t *testing.T) {
	service, mem := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	created, err := service.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: "proj_platform", Name: "authorization target", Role: domain.RoleViewer})
	if err != nil {
		t.Fatal(err)
	}
	accountsBefore, err := mem.ListServiceAccounts(t.Context(), "proj_platform", store.Page{})
	if err != nil {
		t.Fatal(err)
	}
	auditsBefore, err := mem.ListAuditEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	serviceAccounts := &serviceAccountAccessProbe{ServiceAccountRepository: mem}
	members := &projectMemberAccessProbe{ProjectMemberRepository: mem}
	service.serviceAccounts = serviceAccounts
	service.members = members

	projectIDs := []string{" ", "malformed project", "proj_platform", "proj_missing"}
	accountIDs := []string{" ", "malformed account", created.ServiceAccount.ID, "svcacct_missing"}
	principals := []auth.Principal{
		{ID: created.ServiceAccount.ID, Name: "managed account", Provider: domain.PrincipalServiceAccount},
		{ID: "pat_legacy", Name: "legacy API token", Provider: domain.PrincipalAPIToken, Roles: []string{domain.RoleRunnerAdmin}},
	}
	for _, principal := range principals {
		principal := principal
		t.Run(principal.Provider, func(t *testing.T) {
			ctx := auth.WithPrincipal(t.Context(), principal)
			for _, projectID := range projectIDs {
				for _, call := range []func() error{
					func() error { _, err := service.ListServiceAccounts(ctx, projectID, store.Page{}); return err },
					func() error {
						_, err := service.CreateServiceAccount(ctx, ServiceAccountCreateInput{ProjectID: projectID, Name: "forbidden", Role: domain.RoleViewer})
						return err
					},
				} {
					if err := call(); !errors.Is(err, auth.ErrForbidden) || errors.Is(err, ErrInvalidInput) {
						t.Fatalf("project_id=%q error=%v, want forbidden without invalid-input marker", projectID, err)
					}
				}
			}
			for _, accountID := range accountIDs {
				name := "forbidden update"
				for _, call := range []func() error{
					func() error {
						_, err := service.UpdateServiceAccount(ctx, ServiceAccountUpdateInput{ServiceAccountID: accountID, Name: &name})
						return err
					},
					func() error {
						_, err := service.RotateServiceAccountCredential(ctx, ServiceAccountRotateInput{ServiceAccountID: accountID})
						return err
					},
					func() error {
						_, err := service.RevokeServiceAccount(ctx, ServiceAccountRevokeInput{ServiceAccountID: accountID})
						return err
					},
				} {
					if err := call(); !errors.Is(err, auth.ErrForbidden) || errors.Is(err, ErrInvalidInput) {
						t.Fatalf("service_account_id=%q error=%v, want forbidden without invalid-input marker", accountID, err)
					}
				}
			}
		})
	}

	if serviceAccounts.scopeCalls != 0 || members.lookupCalls != 0 {
		t.Fatalf("categorical denial reached authorization lookups: service_account_scopes=%d member_lookups=%d", serviceAccounts.scopeCalls, members.lookupCalls)
	}
	accountsAfter, err := mem.ListServiceAccounts(t.Context(), "proj_platform", store.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(accountsAfter, accountsBefore) {
		t.Fatalf("categorical denial changed service accounts: before=%#v after=%#v", accountsBefore, accountsAfter)
	}
	auditsAfter, err := mem.ListAuditEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const deniedCalls = 2 * (4*2 + 4*3)
	if got := len(auditsAfter) - len(auditsBefore); got != deniedCalls {
		t.Fatalf("denied audit count=%d, want=%d", got, deniedCalls)
	}
	actionCounts := map[string]int{}
	for _, event := range auditsAfter[:deniedCalls] {
		actionCounts[event.Action]++
		if _, ok := event.Metadata["project_id"]; ok && event.Action == "service_account.manage.denied" {
			t.Fatalf("early mutation denial leaked looked-up project metadata: %#v", event)
		}
	}
	if actionCounts["service_account.list.denied"] != 8 || actionCounts["service_account.create.denied"] != 8 || actionCounts["service_account.manage.denied"] != 24 {
		t.Fatalf("denied audit actions=%v", actionCounts)
	}
}

func TestHumanViewerUsesScopedAuthorizationBeforeServiceAccountDenial(t *testing.T) {
	service, mem := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	created, err := service.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: "proj_platform", Name: "viewer target", Role: domain.RoleMaintainer})
	if err != nil {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Email: "viewer@example.local", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleUser}})
	accountsBefore, err := mem.ListServiceAccounts(t.Context(), "proj_platform", store.Page{})
	if err != nil {
		t.Fatal(err)
	}
	name := "forbidden update"
	for _, call := range []func() error{
		func() error { _, err := service.ListServiceAccounts(viewer, "proj_platform", store.Page{}); return err },
		func() error {
			_, err := service.CreateServiceAccount(viewer, ServiceAccountCreateInput{ProjectID: "proj_platform", Name: "forbidden", Role: domain.RoleViewer})
			return err
		},
		func() error {
			_, err := service.UpdateServiceAccount(viewer, ServiceAccountUpdateInput{ServiceAccountID: created.ServiceAccount.ID, Name: &name})
			return err
		},
		func() error {
			_, err := service.RotateServiceAccountCredential(viewer, ServiceAccountRotateInput{ServiceAccountID: created.ServiceAccount.ID})
			return err
		},
		func() error {
			_, err := service.RevokeServiceAccount(viewer, ServiceAccountRevokeInput{ServiceAccountID: created.ServiceAccount.ID})
			return err
		},
	} {
		if err := call(); !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("viewer error=%v, want forbidden", err)
		}
	}
	accountsAfter, err := mem.ListServiceAccounts(t.Context(), "proj_platform", store.Page{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(accountsAfter, accountsBefore) {
		t.Fatalf("viewer denial changed service accounts: before=%#v after=%#v", accountsBefore, accountsAfter)
	}
}

func TestServiceAccountCannotCreateProjectsOrManageHumanMemberships(t *testing.T) {
	service, mem := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	platformAccount, err := service.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: "proj_platform", Name: "platform owner bot", Role: domain.RoleOwner})
	if err != nil {
		t.Fatal(err)
	}
	securityAccount, err := service.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: "proj_security", Name: "security owner bot", Role: domain.RoleOwner})
	if err != nil {
		t.Fatal(err)
	}
	platformPrincipal, err := service.AuthenticateSessionToken(t.Context(), platformAccount.Token)
	if err != nil {
		t.Fatal(err)
	}
	securityPrincipal, err := service.AuthenticateSessionToken(t.Context(), securityAccount.Token)
	if err != nil {
		t.Fatal(err)
	}
	platformCtx := auth.WithPrincipal(t.Context(), platformPrincipal)
	securityCtx := auth.WithPrincipal(t.Context(), securityPrincipal)

	projectsBefore, err := mem.ListProjects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	membersBefore, err := mem.ListProjectMembers(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []struct {
		name  string
		ctx   context.Context
		input ProjectMemberInput
	}{
		{name: "add human", ctx: platformCtx, input: ProjectMemberInput{ProjectID: "proj_platform", Email: "viewer@example.local", Role: domain.RoleViewer}},
		{name: "promote human", ctx: securityCtx, input: ProjectMemberInput{ProjectID: "proj_security", Email: "viewer@example.local", Role: domain.RoleOwner}},
		{name: "demote human", ctx: securityCtx, input: ProjectMemberInput{ProjectID: "proj_security", Email: "admin@example.local", Role: domain.RoleViewer}},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			if _, err := service.UpsertProjectMember(attempt.ctx, attempt.input); !errors.Is(err, auth.ErrForbidden) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if _, err := service.CreateProject(securityCtx, ProjectInput{Name: "forbidden project"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("project create error = %v", err)
	}
	projectsAfter, err := mem.ListProjects(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	membersAfter, err := mem.ListProjectMembers(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projectsAfter, projectsBefore) || !reflect.DeepEqual(membersAfter, membersBefore) {
		t.Fatalf("administration state changed: projects=%#v members=%#v", projectsAfter, membersAfter)
	}
	visible, err := service.ListProjects(securityCtx)
	if err != nil || len(visible) != 1 || visible[0].ID != "proj_security" {
		t.Fatalf("allowed metadata read = %#v, %v", visible, err)
	}
	if _, err := service.RequestRunWithSpec(securityCtx, RunRequestInput{ProjectID: "proj_security", RunSpec: domain.RunSpec{Type: domain.RunTypeShell, Inputs: map[string]any{"command": "true"}}}); err != nil {
		t.Fatalf("allowed run: %v", err)
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

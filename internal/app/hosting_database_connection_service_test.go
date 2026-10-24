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

type connectionServiceRepository struct {
	store.RunRepository
	store.HostingApplicationDatabaseRunnerRepository
	calls, reads        int
	actor, resource     string
	runner, credential  string
	effects, stateReads int
}

func (r *connectionServiceRepository) ConnectHostingApplicationDatabase(_ context.Context, id, resource, actor string, _ domain.HostingDatabaseConnectionInput, event domain.AuditEvent) (domain.HostingDatabaseConnection, error) {
	r.calls++
	r.actor, r.resource = actor, resource
	if event.ActorID != actor || event.TargetID != id {
		return domain.HostingDatabaseConnection{}, store.ErrConflict
	}
	return domain.HostingDatabaseConnection{ID: id, ApplicationResourceID: resource}, nil
}
func (r *connectionServiceRepository) RequestHostingApplicationDatabaseDeployment(_ context.Context, id, resource, actor string, _ domain.HostingDeploymentInput, _ domain.AuditEvent) (domain.HostingOperation, error) {
	r.calls++
	r.actor, r.resource = actor, resource
	return domain.HostingOperation{ID: id, ResourceID: resource, CreatedBy: actor}, nil
}
func (r *connectionServiceRepository) RequestHostingDatabaseMaintenance(_ context.Context, id, resource, actor string, _ domain.HostingDatabaseMaintenanceInput, _ domain.AuditEvent) (domain.HostingOperation, error) {
	r.calls++
	r.actor, r.resource = actor, resource
	return domain.HostingOperation{ID: id, ResourceID: resource, CreatedBy: actor}, nil
}
func (r *connectionServiceRepository) RequestHostingDatabaseConnectionRecovery(_ context.Context, _, resource, actor string, in domain.HostingDatabaseConnectionRecoveryInput, _ domain.AuditEvent) (domain.HostingOperation, error) {
	r.calls++
	r.actor, r.resource = actor, resource
	return domain.HostingOperation{ID: in.OperationID, ResourceID: resource, CreatedBy: actor}, nil
}
func (r *connectionServiceRepository) GetHostingDatabaseConnection(_ context.Context, actor, resource string) (domain.HostingDatabaseConnection, error) {
	r.reads++
	r.actor, r.resource = actor, resource
	return domain.HostingDatabaseConnection{ApplicationResourceID: resource}, nil
}
func (r *connectionServiceRepository) ListEligibleHostingDatabases(_ context.Context, actor, resource string) ([]domain.HostingDatabaseConnectionOption, error) {
	r.reads++
	r.actor, r.resource = actor, resource
	return []domain.HostingDatabaseConnectionOption{}, nil
}
func (r *connectionServiceRepository) GetHostingApplicationDatabaseState(_ context.Context, runner, credential string, _ domain.HostingAuthority) (domain.HostingApplicationDatabaseExecutionState, error) {
	r.stateReads++
	r.runner, r.credential = runner, credential
	return domain.HostingApplicationDatabaseExecutionState{}, store.ErrNotFound
}
func (r *connectionServiceRepository) AuthorizeHostingApplicationDatabaseEffect(_ context.Context, runner, credential string, _ domain.HostingApplicationDatabaseEffectRequest) (domain.HostingApplicationDatabaseEffectAuthorization, error) {
	r.effects++
	r.runner, r.credential = runner, credential
	return domain.HostingApplicationDatabaseEffectAuthorization{}, store.ErrConflict
}

func TestDatabaseConnectionServiceOwnerAndViewerBoundaries(t *testing.T) {
	s, mem := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	env, err := s.CreateProjectEnvironment(admin, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "connection"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := s.CreateResource(admin, ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: env.ID, Name: "connected image", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	in := domain.HostingDatabaseConnectionInput{Binding: domain.HostingDatabaseBindingInput{DatabaseResourceID: "database", ExpectedResourceGeneration: 1, ExpectedDatabaseGeneration: 1, RequestID: strings.Repeat("a", 32)}, Deployment: domain.HostingDeploymentInput{ExpectedGeneration: 1, IdempotencyKey: "connect", ContainerPort: 8080, Readiness: domain.HostingReadiness{Path: "/", TimeoutSeconds: 10}}}
	if _, err = s.ConnectHostingApplicationDatabase(admin, resource.ID, in); !errors.Is(err, store.ErrHostingDatabaseConnectionUnavailable) {
		t.Fatal("memory backend manufactured connection", err)
	}
	repo := &connectionServiceRepository{RunRepository: s.runs}
	s.runs = repo
	recovery := domain.HostingDatabaseConnectionRecoveryInput{Action: "observe", OperationID: "original", ExpectedMembershipRevision: 1, IdempotencyKey: "observe"}
	mutations := []func(context.Context) error{
		func(ctx context.Context) error {
			_, e := s.ConnectHostingApplicationDatabase(ctx, resource.ID, in)
			return e
		},
		func(ctx context.Context) error {
			_, e := s.RequestHostingApplicationDatabaseDeployment(ctx, resource.ID, in.Deployment)
			return e
		},
		func(ctx context.Context) error {
			_, e := s.RequestHostingDatabaseConnectionRecovery(ctx, resource.ID, recovery)
			return e
		},
	}
	for _, role := range []string{domain.RoleViewer, domain.RoleMaintainer, domain.RoleOwner} {
		account, e := s.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: "proj_platform", Name: "connection " + role, Role: role})
		if e != nil {
			t.Fatal(e)
		}
		ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: account.ServiceAccount.ID, Provider: domain.PrincipalServiceAccount})
		for _, call := range mutations {
			before := repo.calls
			err = call(ctx)
			if role != domain.RoleOwner {
				if !errors.Is(err, auth.ErrForbidden) || repo.calls != before {
					t.Fatal("nonowner mutation reached repository", role, err)
				}
			} else if err != nil || repo.calls != before+1 || repo.actor != account.ServiceAccount.ID || repo.resource != resource.ID {
				t.Fatal("owner identity was not bound", err)
			}
		}
	}
	if _, err = mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "connection_viewer", ProjectID: "proj_platform", UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_viewer", Provider: domain.PrincipalLocal})
	if _, err = s.GetHostingDatabaseConnection(viewer, resource.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ListEligibleHostingDatabases(viewer, resource.ID); err != nil || repo.reads != 2 || repo.actor != "usr_viewer" {
		t.Fatal("viewer read was not bound", err)
	}
	before := repo.calls
	recovery.Action = "resume"
	if _, err = s.RequestHostingDatabaseConnectionRecovery(admin, resource.ID, recovery); !errors.Is(err, ErrInvalidInput) || repo.calls != before {
		t.Fatal("effectful recovery reached repository", err)
	}
}

func TestDatabaseConnectionRunnerIdentityAndReadDoesNotAuthorize(t *testing.T) {
	s, _ := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	registered, err := s.RegisterRunner(admin, RunnerInput{ID: "runner_connection", Name: "connection", Capabilities: []string{domain.HostingApplicationDatabaseCapability}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: registered.Runner.ID, Provider: domain.PrincipalRunner, Roles: []string{domain.RoleRunner}})
	a := domain.HostingAuthority{OperationID: "op", RunID: "run_op", LeaseID: "lease", Attempt: 1, Fence: "fence"}
	if _, err = s.RunnerHostingApplicationDatabaseState(ctx, registered.Token, a); !errors.Is(err, store.ErrHostingDatabaseConnectionUnavailable) {
		t.Fatal("memory backend manufactured state", err)
	}
	repo := &connectionServiceRepository{RunRepository: s.runs}
	s.runs = repo
	if _, err = s.RunnerHostingApplicationDatabaseState(ctx, registered.Token, a); !errors.Is(err, store.ErrNotFound) || repo.stateReads != 1 || repo.effects != 0 || repo.runner != registered.Runner.ID || repo.credential != runnerTokenHash(registered.Token) || repo.credential == registered.Token {
		t.Fatal("state read acquired effect authority or lost identity", err)
	}
	for _, invalid := range []context.Context{admin, t.Context(), auth.WithPrincipal(t.Context(), auth.Principal{ID: "foreign", Provider: domain.PrincipalRunner, Roles: []string{domain.RoleRunner}})} {
		if _, err = s.RunnerHostingApplicationDatabaseState(invalid, registered.Token, a); err == nil || repo.stateReads != 1 {
			t.Fatal("foreign actor reached state", err)
		}
	}
	in := domain.HostingApplicationDatabaseEffectRequest{HostingAuthority: a, AuthorizationID: strings.Repeat("b", 32), Action: "create"}
	if _, err = s.RunnerAuthorizeHostingApplicationDatabaseEffect(ctx, "wrong", in); err == nil || repo.effects != 0 {
		t.Fatal("wrong bearer reached effects")
	}
	if _, err = s.RunnerAuthorizeHostingApplicationDatabaseEffect(ctx, registered.Token, in); !errors.Is(err, store.ErrConflict) || repo.effects != 1 {
		t.Fatal("explicit effect request was not separate", err)
	}
}

func TestDatabaseConnectionMaintenanceRequiresOwner(t *testing.T) {
	s, _, admin, create := databaseServiceFixture(t)
	view, err := s.CreateHostingDatabase(admin, create)
	if err != nil {
		t.Fatal(err)
	}
	repo := &connectionServiceRepository{RunRepository: s.runs}
	s.runs = repo
	in := domain.HostingDatabaseMaintenanceInput{ExpectedGeneration: 1, ExpectedMembershipRevision: 2, IdempotencyKey: "maintain_members"}
	for _, role := range []string{domain.RoleViewer, domain.RoleMaintainer, domain.RoleOwner} {
		account, e := s.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: create.ProjectID, Name: "membership " + role, Role: role})
		if e != nil {
			t.Fatal(e)
		}
		ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: account.ServiceAccount.ID, Provider: domain.PrincipalServiceAccount})
		before := repo.calls
		op, e := s.RequestHostingDatabaseMaintenance(ctx, view.Resource.ID, in)
		if role != domain.RoleOwner {
			if !errors.Is(e, auth.ErrForbidden) || repo.calls != before {
				t.Fatal("nonowner requested member maintenance", role, e)
			}
		} else if e != nil || repo.calls != before+1 || op.CreatedBy != account.ServiceAccount.ID || op.ResourceID != view.Resource.ID {
			t.Fatal("maintenance owner identity was not preserved", e)
		}
	}
}

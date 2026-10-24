package app

import (
	"context"
	"errors"
	"testing"

	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestDatabaseConnectionRunnerContextRequiresOriginalBearer(t *testing.T) {
	s, _ := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	registered, err := s.RegisterRunner(admin, RunnerInput{ID: "runner_connection_context", Name: "connection context", Capabilities: []string{domain.HostingApplicationDatabaseCapability}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: registered.Runner.ID, Provider: domain.PrincipalRunner, Roles: []string{domain.RoleRunner}})
	if _, err = s.HostingDatabaseConnectionRunnerContext(ctx, registered.Token); err != nil {
		t.Fatal("authenticated original bearer rejected", err)
	}
	for _, invalid := range []context.Context{admin, t.Context(), auth.WithPrincipal(t.Context(), auth.Principal{ID: "foreign", Provider: domain.PrincipalRunner, Roles: []string{domain.RoleRunner}})} {
		if _, err = s.HostingDatabaseConnectionRunnerContext(invalid, registered.Token); err == nil {
			t.Fatal("foreign principal bound runner fingerprint")
		}
	}
	if _, err = s.HostingDatabaseConnectionRunnerContext(ctx, "wrong"); err == nil {
		t.Fatal("wrong bearer bound current row identity")
	}
}

type connectionControlRepository struct {
	store.RunRepository
	store.HostingExecutionRepository
	operation domain.HostingOperation
	cancels   int
}

func (r *connectionControlRepository) GetHostingOperation(context.Context, string) (domain.HostingOperation, error) {
	return r.operation, nil
}
func (r *connectionControlRepository) CancelHostingOperation(context.Context, string, domain.AuditEvent) (domain.HostingOperation, error) {
	r.cancels++
	return r.operation, nil
}

func TestDatabaseConnectionCommonControlRequiresOwner(t *testing.T) {
	s, _ := newServiceAccountTestService(t)
	admin := auth.WithPrincipal(t.Context(), testPrincipalContext(t))
	env, err := s.CreateProjectEnvironment(admin, ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "connection control"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := s.CreateResource(admin, ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: env.ID, Name: "bound control", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	repo := &connectionControlRepository{RunRepository: s.runs, operation: domain.HostingOperation{ID: "hop_bound", ProjectID: resource.ProjectID, ResourceID: resource.ID}}
	s.runs = repo
	for _, role := range []string{domain.RoleMaintainer, domain.RoleOwner} {
		account, e := s.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: resource.ProjectID, Name: "control " + role, Role: role})
		if e != nil {
			t.Fatal(e)
		}
		ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: account.ServiceAccount.ID, Provider: domain.PrincipalServiceAccount})
		for _, family := range []string{"application", "maintenance", "ordinary"} {
			repo.operation.ApplicationDatabase = nil
			repo.operation.DatabaseMaintenance = nil
			if family == "application" {
				repo.operation.ApplicationDatabase = &domain.HostingApplicationDatabaseExecutionProof{}
			}
			if family == "maintenance" {
				repo.operation.DatabaseMaintenance = &domain.HostingDatabaseMaintenanceProof{}
			}
			before := repo.cancels
			_, e = s.CancelHostingOperation(ctx, repo.operation.ID)
			if family != "ordinary" && role != domain.RoleOwner {
				if !errors.Is(e, auth.ErrForbidden) || repo.cancels != before {
					t.Fatal("bound cancellation bypassed owner", family, e)
				}
			} else if e != nil || repo.cancels != before+1 {
				t.Fatal("authorized control or ordinary compatibility", family, e)
			}
		}
	}
}

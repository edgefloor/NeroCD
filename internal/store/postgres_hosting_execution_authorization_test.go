package store_test

import (
	"errors"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresHostingApplicationUsesPersistedProjectAndServiceAccountScope(t *testing.T) {
	pg, _, resource, _, _ := hostingExecutionFixture(t)
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, Projects: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	if _, err = pg.UpsertProjectMember(ctx, domain.ProjectMember{ID: "member_image_viewer", ProjectID: resource.ProjectID, UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	viewer := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_viewer", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleViewer}})
	op, err := service.RequestHostingDeployment(admin, resource.ID, hostingDeploymentRequest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.HostingOperation(viewer, op.ID); err != nil {
		t.Fatalf("persisted project viewer cannot read: %v", err)
	}
	page, err := service.ListHostingDeployments(viewer, resource.ID, store.Page{})
	if err != nil || page.Total != 1 || page.Items[0].ID != op.ID || page.Items[0].ObservationLease != nil {
		t.Fatalf("viewer history: %+v %v", page, err)
	}
	for _, mutate := range []func() error{
		func() error {
			_, err := service.RequestHostingDeployment(viewer, resource.ID, hostingDeploymentRequest())
			return err
		},
		func() error { _, err := service.CancelHostingOperation(viewer, op.ID); return err },
		func() error { _, err := service.ReconcileHostingOperation(viewer, op.ID); return err },
	} {
		if err := mutate(); !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("viewer mutated deployment: %v", err)
		}
	}
	for _, project := range []string{resource.ProjectID, "proj_security"} {
		account, err := service.CreateServiceAccount(admin, app.ServiceAccountCreateInput{ProjectID: project, Name: "image bot " + project, Role: domain.RoleMaintainer})
		if err != nil {
			t.Fatal(err)
		}
		principal, err := service.AuthenticateSessionToken(ctx, account.Token)
		if err != nil {
			t.Fatal(err)
		}
		scoped := auth.WithPrincipal(ctx, principal)
		_, readErr := service.HostingOperation(scoped, op.ID)
		_, listErr := service.ListHostingDeployments(scoped, resource.ID, store.Page{})
		_, requestErr := service.RequestHostingDeployment(scoped, resource.ID, hostingDeploymentRequest())
		if project == resource.ProjectID {
			if readErr != nil || listErr != nil || requestErr != nil {
				t.Fatalf("authorized project bot denied: read=%v list=%v request=%v", readErr, listErr, requestErr)
			}
			if _, err = service.ReconcileHostingOperation(scoped, op.ID); !errors.Is(err, auth.ErrForbidden) {
				t.Fatalf("maintainer bot acquired owner observation: %v", err)
			}
		} else {
			for _, denied := range []error{readErr, listErr, requestErr} {
				if !errors.Is(denied, auth.ErrForbidden) {
					t.Fatalf("cross-project service account accepted: %v", denied)
				}
			}
			if _, err = service.CancelHostingOperation(scoped, op.ID); !errors.Is(err, auth.ErrForbidden) {
				t.Fatalf("cross-project cancellation: %v", err)
			}
		}
	}
}

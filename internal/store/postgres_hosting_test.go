package store_test

import (
	"errors"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func TestPostgresHostingSchemaReadinessAndConstraints(t *testing.T) {
	ctx := t.Context()
	pg, database := openPostgresIntegrationStore(t, ctx, "hosting_readiness")
	defer closePostgresStore(t, pg)
	if ok, err := pg.SchemaCompatible(ctx); err != nil || !ok {
		t.Fatalf("complete schema: %v %v", ok, err)
	}
	if _, err := database.Exec(ctx, `ALTER TABLE resources DISABLE TRIGGER resources_immutable_identity`); err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.SchemaCompatible(ctx); err != nil || ok {
		t.Fatalf("disabled immutability trigger: %v %v", ok, err)
	}
	if _, err := database.Exec(ctx, `ALTER TABLE resources ENABLE TRIGGER resources_immutable_identity`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(ctx, `DROP TABLE resources CASCADE; DROP TABLE project_environments; DROP FUNCTION nerocd_hosting_resource_update(); DROP FUNCTION nerocd_hosting_schema_compatible()`); err != nil {
		t.Fatal(err)
	}
	if ok, err := pg.SchemaCompatible(ctx); ok {
		t.Fatalf("missing 0040 readiness: %v %v", ok, err)
	}
	// The same aggregate query gates opening a fresh store as well as live probes.
	if _, err := store.OpenPostgres(ctx, database.Config().ConnConfig.ConnString()); err == nil {
		t.Fatal("startup accepted schema without hosting migration")
	}
	if err := applyEmbeddedMigrations(ctx, database, "0039_service_accounts.sql", "0040_hosting_resources.sql"); err != nil {
		t.Fatal(err)
	}
	// The intentional 0040 teardown removes this later migration's dependent FK.
	if _, err := database.Exec(ctx, `ALTER TABLE resource_bindings ADD CONSTRAINT resource_bindings_resource_id_fkey FOREIGN KEY(resource_id) REFERENCES resources(id) ON DELETE RESTRICT`); err != nil {
		t.Fatal(err)
	}
	var restored bool
	if err := database.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='resource_bindings'::regclass AND conname='resource_bindings_resource_id_fkey' AND contype='f' AND convalidated) AND NOT EXISTS(SELECT 1 FROM resource_bindings b LEFT JOIN resources r ON r.id=b.resource_id WHERE r.id IS NULL)`).Scan(&restored); err != nil || !restored {
		t.Fatalf("restored binding FK: %v %v", restored, err)
	}
	if ok, err := pg.SchemaCompatible(ctx); err != nil || !ok {
		t.Fatalf("restored 0040 readiness: %v %v", ok, err)
	}
	if _, err := database.Exec(ctx, `INSERT INTO project_environments VALUES ('env_constraint','proj_platform','production',now()); INSERT INTO resources (id,project_id,project_environment_id,name,kind,source,created_by,created_at,updated_at) VALUES ('res_constraint','proj_platform','env_constraint','web','application','{"kind":"image","image":{"reference":"nginx:v1"}}','usr_bootstrap',now(),now())`); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`UPDATE resources SET project_id='proj_security',generation=generation+1 WHERE id='res_constraint'`,
		`UPDATE resources SET project_environment_id='missing',generation=generation+1 WHERE id='res_constraint'`,
		`UPDATE resources SET created_by='usr_viewer',generation=generation+1 WHERE id='res_constraint'`,
		`UPDATE resources SET kind='database',generation=generation+1 WHERE id='res_constraint'`,
		`UPDATE resources SET generation=generation+2 WHERE id='res_constraint'`,
		`UPDATE resources SET source='{}',generation=generation+1 WHERE id='res_constraint'`,
	} {
		if _, err := database.Exec(ctx, query); err == nil {
			t.Fatalf("constraint accepted %s", query)
		}
	}
}

func TestPostgresHostingServiceAccountScope(t *testing.T) {
	ctx := t.Context()
	pg, _ := openPostgresIntegrationStore(t, ctx, "hosting_account")
	defer closePostgresStore(t, pg)
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Projects: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	admin := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	account, err := service.CreateServiceAccount(admin, app.ServiceAccountCreateInput{ProjectID: "proj_platform", Name: "hosting bot", Role: domain.RoleMaintainer})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.AuthenticateSessionToken(ctx, account.Token)
	if err != nil {
		t.Fatal(err)
	}
	bot := auth.WithPrincipal(ctx, principal)
	environment, err := service.CreateProjectEnvironment(bot, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "production"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := service.CreateResource(bot, app.ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: environment.ID, Name: "web", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil || resource.CreatedBy != account.ServiceAccount.ID {
		t.Fatalf("bot creator: %+v %v", resource, err)
	}
	if _, err = service.ListResources(bot, "proj_security", "", store.Page{}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("bot cross project read: %v", err)
	}
	role := domain.RoleViewer
	if _, err = service.UpdateServiceAccount(admin, app.ServiceAccountUpdateInput{ServiceAccountID: account.ServiceAccount.ID, Role: &role}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.GetResource(bot, resource.ID); err != nil {
		t.Fatalf("viewer bot read: %v", err)
	}
	name := "renamed"
	if _, err = service.UpdateResource(bot, resource.ID, app.ResourcePatch{Name: &name, ExpectedGeneration: 1}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("viewer bot write: %v", err)
	}
}

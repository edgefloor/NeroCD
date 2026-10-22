package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"nerocd/db"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

func enrollApplicationDestinationRunner(t *testing.T, service *Service, ctx context.Context, id string, capabilities []string) domain.Runner {
	t.Helper()
	created, err := service.CreateRunnerEnrollment(ctx, RunnerEnrollmentInput{RunnerID: id, RunnerName: id, Capabilities: capabilities})
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(id))
	digest := hex.EncodeToString(sum[:])
	consumed, err := service.ConsumeRunnerEnrollment(t.Context(), created.Token, RunnerEnrollmentConsumeInput{RequestID: "enroll_consume_" + digest[:32], CredentialHash: digest})
	if err != nil {
		t.Fatal(err)
	}
	return consumed.Runner
}
func destinationTestResource(t *testing.T, service *Service, ctx context.Context, project, name string) domain.Resource {
	t.Helper()
	environment, err := service.CreateProjectEnvironment(ctx, ProjectEnvironmentInput{ProjectID: project, Name: name})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := service.CreateResource(ctx, ResourceInput{ProjectID: project, ProjectEnvironmentID: environment.ID, Name: name, Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	return resource
}

func TestDestinationServiceAuthorizationAndImmutablePlacement(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		service, _ := newServiceAccountTestService(t)
		destinationServiceContract(t, service)
	})
	t.Run("postgres", func(t *testing.T) { destinationServiceContract(t, newPostgresDestinationTestService(t)) })
}

func destinationServiceContract(t *testing.T, service *Service) {
	t.Helper()
	ctx := t.Context()
	admin := auth.WithPrincipal(ctx, testPrincipalContext(t))
	legacyAdmin, err := service.CreateAPIToken(admin, APITokenInput{Name: "legacy runner administrator", Roles: []string{domain.RoleRunnerAdmin}})
	if err != nil {
		t.Fatal(err)
	}
	legacyPrincipal, err := service.AuthenticateSessionToken(ctx, legacyAdmin.Token)
	if err != nil {
		t.Fatal(err)
	}
	runnerAdmin := auth.WithPrincipal(ctx, legacyPrincipal)
	viewer := auth.WithPrincipal(ctx, auth.Principal{ID: "usr_viewer", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleViewer}})
	eligible := enrollApplicationDestinationRunner(t, service, admin, "runner_dest_first", []string{domain.RunTypeComposeDeploy})
	detail, err := service.CreateServer(runnerAdmin, ServerInput{Name: "First host", RunnerID: eligible.ID})
	if err != nil {
		t.Fatal(err)
	}
	if detail.Server.ID == "" || detail.Server.RunnerID != eligible.ID || detail.Server.CreatedBy != legacyPrincipal.ID || detail.Destination.ServerID != detail.Server.ID || detail.Destination.Name != "default" || detail.Destination.RuntimeKind != "docker_compose" {
		t.Fatalf("server detail=%+v", detail)
	}
	if got, err := service.GetServer(runnerAdmin, detail.Server.ID); err != nil || got != detail {
		t.Fatalf("get=%+v err=%v", got, err)
	}
	if _, err = service.CreateServer(admin, ServerInput{Name: "Duplicate", RunnerID: eligible.ID}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate association: %v", err)
	}
	for _, operation := range []func() error{
		func() error { _, err := service.ListServers(viewer, store.Page{}); return err },
		func() error { _, err := service.GetServer(viewer, detail.Server.ID); return err },
		func() error {
			_, err := service.CreateServer(viewer, ServerInput{Name: "Denied", RunnerID: eligible.ID})
			return err
		},
		func() error {
			_, err := service.ListDestinations(viewer, "", detail.Server.ID, store.Page{})
			return err
		},
		func() error {
			_, err := service.ListDestinationProjectGrants(viewer, detail.Destination.ID, store.Page{})
			return err
		},
		func() error {
			_, err := service.GrantDestinationProject(viewer, detail.Destination.ID, DestinationProjectInput{ProjectID: "proj_security"})
			return err
		},
		func() error {
			_, err := service.RevokeDestinationProject(viewer, detail.Destination.ID, DestinationProjectInput{ProjectID: "proj_security"})
			return err
		},
	} {
		if err := operation(); !errors.Is(err, auth.ErrForbidden) {
			t.Fatalf("viewer global operation: %v", err)
		}
	}
	if _, err = service.ListServers(ctx, store.Page{}); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("anonymous list: %v", err)
	}
	for _, scope := range [][2]string{{"", ""}, {"proj_security", detail.Server.ID}} {
		if _, err = service.ListDestinations(admin, scope[0], scope[1], store.Page{}); err == nil {
			t.Fatal("ambiguous destination scope accepted")
		}
	}
	for _, bad := range []string{"", "bad/id", strings.Repeat("x", 129)} {
		if _, err = service.GetServer(admin, bad); err == nil {
			t.Fatalf("invalid server id accepted %q", bad)
		}
	}
	if _, err = service.GetServer(admin, "missing_server"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown server: %v", err)
	}
	if _, err = service.CreateServer(admin, ServerInput{Name: "Missing", RunnerID: "missing_runner"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing runner: %v", err)
	}
	legacy, err := service.RegisterRunner(admin, RunnerInput{ID: "runner_dest_legacy", Name: "legacy", Capabilities: []string{"compose_deploy"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.CreateServer(admin, ServerInput{Name: "Unenrolled", RunnerID: legacy.Runner.ID}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("unenrolled runner: %v", err)
	}
	shell := enrollApplicationDestinationRunner(t, service, admin, "runner_dest_shell", []string{"shell"})
	if _, err = service.CreateServer(admin, ServerInput{Name: "Shell", RunnerID: shell.ID}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("shell runner: %v", err)
	}
	revoked := enrollApplicationDestinationRunner(t, service, admin, "runner_dest_revoked", []string{"compose_deploy"})
	if _, err = service.RevokeRunnerToken(admin, RunnerTokenInput{RunnerID: revoked.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.CreateServer(admin, ServerInput{Name: "Revoked", RunnerID: revoked.ID}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("revoked runner: %v", err)
	}
	resource := destinationTestResource(t, service, admin, "proj_security", "security-web")
	cross := destinationTestResource(t, service, admin, "proj_platform", "platform-web")
	pending := destinationTestResource(t, service, admin, "proj_security", "pending-web")
	account, err := service.CreateServiceAccount(admin, ServiceAccountCreateInput{ProjectID: "proj_security", Name: "placement bot", Role: domain.RoleMaintainer})
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.AuthenticateSessionToken(ctx, account.Token)
	if err != nil {
		t.Fatal(err)
	}
	bot := auth.WithPrincipal(ctx, principal)
	if _, err = service.ListServers(bot, store.Page{}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("project bot global administration: %v", err)
	}
	if _, err = service.GrantDestinationProject(bot, detail.Destination.ID, DestinationProjectInput{ProjectID: "proj_security"}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("bot grant: %v", err)
	}
	if _, err = service.BindResourceDestination(bot, resource.ID, ResourceBindingInput{DestinationID: detail.Destination.ID}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("binding without grant: %v", err)
	}
	for _, project := range []string{"proj_security", "proj_platform"} {
		if _, err = service.GrantDestinationProject(runnerAdmin, detail.Destination.ID, DestinationProjectInput{ProjectID: project}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = service.GrantDestinationProject(admin, detail.Destination.ID, DestinationProjectInput{ProjectID: "proj_security"}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate grant: %v", err)
	}
	page, err := service.ListDestinations(viewer, "proj_security", "", store.Page{Enabled: true, LimitSpecified: true, Limit: 1})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != detail.Destination.ID {
		t.Fatalf("project destinations=%+v err=%v", page, err)
	}
	if _, err = service.ListDestinations(bot, "proj_platform", "", store.Page{}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("cross project list: %v", err)
	}
	if _, err = service.BindResourceDestination(bot, cross.ID, ResourceBindingInput{DestinationID: detail.Destination.ID}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("cross project binding: %v", err)
	}
	if _, err = service.BindResourceDestination(viewer, resource.ID, ResourceBindingInput{DestinationID: detail.Destination.ID}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("viewer binding: %v", err)
	}
	if _, err = service.GetResourceBinding(viewer, resource.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unbound resource: %v", err)
	}
	binding, err := service.BindResourceDestination(bot, resource.ID, ResourceBindingInput{DestinationID: detail.Destination.ID})
	if err != nil || binding.ResourceID != resource.ID || binding.DestinationID != detail.Destination.ID || !strings.HasPrefix(binding.RuntimeNamespace, "ncd_") || binding.CreatedBy != account.ServiceAccount.ID {
		t.Fatalf("binding=%+v err=%v", binding, err)
	}
	if got, err := service.GetResourceBinding(viewer, resource.ID); err != nil || got != binding {
		t.Fatalf("viewer binding read=%+v err=%v", got, err)
	}
	if _, err = service.GetResourceBinding(bot, cross.ID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("cross project binding read: %v", err)
	}
	if _, err = service.BindResourceDestination(bot, resource.ID, ResourceBindingInput{DestinationID: detail.Destination.ID}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate binding: %v", err)
	}
	role := domain.RoleViewer
	if _, err = service.UpdateServiceAccount(admin, ServiceAccountUpdateInput{ServiceAccountID: account.ServiceAccount.ID, Role: &role}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.BindResourceDestination(bot, pending.ID, ResourceBindingInput{DestinationID: detail.Destination.ID}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("persisted bot role downgrade: %v", err)
	}
	role = domain.RoleMaintainer
	if _, err = service.UpdateServiceAccount(admin, ServiceAccountUpdateInput{ServiceAccountID: account.ServiceAccount.ID, Role: &role}); err != nil {
		t.Fatal(err)
	}
	grant, err := service.RevokeDestinationProject(runnerAdmin, detail.Destination.ID, DestinationProjectInput{ProjectID: "proj_security"})
	if err != nil || grant.RevokedAt == nil {
		t.Fatalf("revoke=%+v err=%v", grant, err)
	}
	page, err = service.ListDestinations(viewer, "proj_security", "", store.Page{})
	if err != nil || page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("revoked project destination=%+v err=%v", page, err)
	}
	if got, err := service.GetResourceBinding(viewer, resource.ID); err != nil || got != binding {
		t.Fatalf("revoke moved binding=%+v err=%v", got, err)
	}
	if _, err = service.BindResourceDestination(bot, pending.ID, ResourceBindingInput{DestinationID: detail.Destination.ID}); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("new binding after revoke: %v", err)
	}
	grants, err := service.ListDestinationProjectGrants(admin, detail.Destination.ID, store.Page{})
	if err != nil || grants.Total != 2 {
		t.Fatalf("retained grants=%+v err=%v", grants, err)
	}
	grant, err = service.GrantDestinationProject(admin, detail.Destination.ID, DestinationProjectInput{ProjectID: "proj_security"})
	if err != nil || grant.RevokedAt != nil {
		t.Fatalf("reactivated grant=%+v err=%v", grant, err)
	}
	otherRunner := enrollApplicationDestinationRunner(t, service, admin, "runner_dest_second", []string{"compose_deploy"})
	other, err := service.CreateServer(admin, ServerInput{Name: "Second host", RunnerID: otherRunner.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.GrantDestinationProject(admin, other.Destination.ID, DestinationProjectInput{ProjectID: "proj_security"}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.BindResourceDestination(bot, resource.ID, ResourceBindingInput{DestinationID: other.Destination.ID}); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("binding replacement: %v", err)
	}
	servers, err := service.ListServers(admin, store.Page{Enabled: true, LimitSpecified: true, Limit: 1})
	if err != nil || servers.Total != 2 || len(servers.Items) != 1 {
		t.Fatalf("server page=%+v err=%v", servers, err)
	}
	targetPage, err := service.ListDestinations(admin, "", detail.Server.ID, store.Page{})
	if err != nil || targetPage.Total != 1 {
		t.Fatalf("server destinations=%+v err=%v", targetPage, err)
	}
	unchanged, err := service.GetResource(admin, resource.ID)
	if err != nil || unchanged.Generation != resource.Generation {
		t.Fatalf("placement changed configuration generation: %+v %v", unchanged, err)
	}
}

func newPostgresDestinationTestService(t *testing.T) *Service {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("NEROCD_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("set NEROCD_TEST_DATABASE_URL for real PostgreSQL destination API wiring")
	}
	ctx := t.Context()
	adminDB, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("nerocd_destination_app_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err = adminDB.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		adminDB.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := adminDB.Exec(cleanup, "DROP SCHEMA "+identifier+" CASCADE"); err != nil {
			t.Error(err)
		}
		adminDB.Close()
	})
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	database, err := pgxpool.New(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	entries, err := db.Files.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		sql, err := db.Files.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err = database.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("migration %s: %v", entry.Name(), err)
		}
	}
	seed, err := os.ReadFile("../../db/seeds/dev.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec(ctx, string(seed)); err != nil {
		t.Fatal(err)
	}
	pg, err := store.OpenPostgres(ctx, parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pg.Close(); err != nil {
			t.Error(err)
		}
	})
	if compatible, err := pg.SchemaCompatible(ctx); err != nil || !compatible {
		t.Fatalf("destination schema readiness=%v err=%v", compatible, err)
	}
	service, err := NewService(Dependencies{Auth: auth.ContextProvider{}, Users: pg, Sessions: pg, APITokens: pg, ServiceAccounts: pg, OIDC: pg, Projects: pg, Hosting: pg, Destinations: pg, ManagedSecrets: pg, Members: pg, Templates: pg, Sources: pg, Runs: pg, Runners: pg, Approvals: pg, Audit: pg, Deployments: pg, Observability: pg, ObservationWriter: pg, ObservationReader: pg, Retention: pg})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

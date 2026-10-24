package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/web"
)

type databaseExecutionHTTPRepository struct {
	*databaseHTTPRepository
	calls    int
	response *domain.HostingOperation
}

func (r *databaseExecutionHTTPRepository) RequestHostingDatabaseDeployment(_ context.Context, id, resource, actor string, _ domain.HostingDatabaseDeploymentInput, _ domain.AuditEvent) (domain.HostingOperation, error) {
	r.calls++
	if r.response != nil {
		return *r.response, r.err
	}
	return domain.HostingOperation{ID: id, ResourceID: resource, ProjectID: r.view.Resource.ProjectID, CreatedBy: actor, State: "queued"}, r.err
}

func databaseOperationResponse(t *testing.T, engine string) domain.HostingOperation {
	t.Helper()
	version := domain.HostingDatabasePostgresRecipe
	if engine == "redis" {
		version = domain.HostingDatabaseRedisRecipe
	}
	recipe, err := domain.HostingDatabaseRecipeFor(engine, version)
	if err != nil {
		t.Fatal(err)
	}
	resource, binding, operation := "res_database_execution", "binding_database_execution", "hop_database_response"
	storageID, volume := domain.HostingStorageIdentity(binding)
	snapshot := domain.HostingSnapshot{Database: domain.HostingDatabaseExecutionRef{Engine: engine, RecipeVersion: version}, Storage: domain.HostingStorageRef{StorageID: storageID, Generation: 1, Hash: "sha256:" + strings.Repeat("a", 64)}, ResourceID: resource, ResourceGeneration: 1, ImageReference: recipe.ImageReference, BindingID: binding, DestinationID: "dest_database", ServerID: "server_database", RunnerID: "runner_database", RuntimeNamespace: "ncd_" + strings.Repeat("b", 36), ContainerPort: recipe.ContainerPort, PolicyVersion: domain.HostingDatabasePolicyVersion}
	credentials := recipe.Credentials
	for i := range credentials {
		credentials[i].SecretID = domain.HostingDatabaseSecretID(resource, credentials[i].Role)
		credentials[i].Version = 1
	}
	snapshot.Database.ConfigurationHash, err = domain.HostingDatabaseConfigurationHash(snapshot, credentials)
	if err != nil {
		t.Fatal(err)
	}
	platform := recipe.Platforms[0]
	release := domain.HostingRelease{CanonicalImage: strings.Split(recipe.ImageReference, "@")[0] + "@" + platform.Digest, ImageID: "sha256:" + strings.Repeat("c", 64), OS: platform.OS, Architecture: platform.Architecture, Variant: platform.Variant, DaemonID: "database_response_daemon"}
	release.Hash = domain.HostingDocumentHash(release)
	storage := domain.HostingStoragePlan{Reference: snapshot.Storage, VolumeName: volume, MountPath: recipe.MountPath, CreationOperationID: operation, DaemonID: release.DaemonID}
	storage.CreationNonce = domain.HostingStorageCreationNonce(storage.Reference, operation, release.DaemonID)
	plan, err := domain.CompileHostingDatabasePlan(snapshot, release, nil, storage, domain.HostingDatabasePlan{Reference: snapshot.Database, NetworkName: domain.HostingDatabaseNetworkName(binding), Credentials: credentials})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 24, 0, 0, 0, 0, time.UTC)
	return domain.HostingOperation{ID: operation, ProjectID: "proj_platform", ResourceID: resource, BindingID: binding, RunID: "run_" + operation, TaskMode: "deploy", State: "prepared", Snapshot: snapshot, Release: &release, Plan: &plan, PreparedReceipt: &domain.HostingReceipt{ID: "prepared_database_response", BodyHash: release.Hash, CreatedAt: now}, CreatedBy: "usr_bootstrap", CreatedAt: now, UpdatedAt: now}
}

func TestHostingDatabaseExecutionHTTPCompleteResponseSchema(t *testing.T) {
	document, err := openapi3.NewLoader().LoadFromFile("../../openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"postgresql", "redis"} {
		t.Run(engine, func(t *testing.T) {
			operation := databaseOperationResponse(t, engine)
			mem := newSeededTestStore()
			repo := &databaseExecutionHTTPRepository{databaseHTTPRepository: &databaseHTTPRepository{MemoryStore: mem, view: domain.HostingDatabase{Resource: domain.Resource{ID: operation.ResourceID, ProjectID: operation.ProjectID, Kind: "database"}}}, response: &operation}
			service, e := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
			if e != nil {
				t.Fatal(e)
			}
			server := NewServer(service, slog.Default(), web.Static())
			token := createTestSession(t, server)
			req := httptest.NewRequest("POST", "/api/v1/resources/"+operation.ResourceID+"/database/deployments", strings.NewReader(`{"expected_generation":1,"idempotency_key":"response"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != 202 || rec.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("database operation response: %d %s", rec.Code, rec.Body.String())
			}
			var value any
			if e = json.Unmarshal(rec.Body.Bytes(), &value); e != nil {
				t.Fatal(e)
			}
			if e = document.Components.Schemas["HostingOperation"].Value.VisitJSON(value); e != nil {
				t.Fatalf("complete database operation violates response schema: %v", e)
			}
			// A private no-HTTP readiness value must not become an ordinary
			// image request or image snapshot through the shared schemas.
			image := map[string]any{"expected_generation": float64(1), "idempotency_key": "image", "container_port": float64(80), "readiness": map[string]any{"path": "", "timeout_seconds": float64(0)}}
			if document.Components.Schemas["HostingDeploymentInput"].Value.VisitJSON(image) == nil {
				t.Fatal("ordinary image input accepted private database readiness")
			}
			image["readiness"] = map[string]any{"path": "/", "timeout_seconds": float64(5)}
			if e = document.Components.Schemas["HostingDeploymentInput"].Value.VisitJSON(image); e != nil {
				t.Fatalf("ordinary HTTP image input rejected: %v", e)
			}
			body := value.(map[string]any)
			snapshot := body["snapshot"].(map[string]any)
			privateReadiness := snapshot["readiness"]
			snapshot["readiness"] = image["readiness"]
			if document.Components.Schemas["HostingSnapshot"].Value.VisitJSON(snapshot) == nil {
				t.Fatal("database snapshot accepted an HTTP probe")
			}
			snapshot["readiness"] = privateReadiness
			snapshot["host_port"] = float64(18080)
			if document.Components.Schemas["HostingSnapshot"].Value.VisitJSON(snapshot) == nil {
				t.Fatal("database snapshot accepted a public host port")
			}
			snapshot["host_port"] = float64(0)
			snapshot["runtime_configuration"] = map[string]any{"generation": float64(1), "hash": "sha256:" + strings.Repeat("d", 64)}
			if document.Components.Schemas["HostingSnapshot"].Value.VisitJSON(snapshot) == nil {
				t.Fatal("database snapshot accepted a mixed runtime family")
			}
			delete(snapshot, "runtime_configuration")
			delete(snapshot, "database")
			snapshot["policy_version"] = domain.HostingOwnedStoragePolicyVersion
			snapshot["host_port"] = float64(18080)
			if document.Components.Schemas["HostingSnapshot"].Value.VisitJSON(snapshot) == nil {
				t.Fatal("ordinary image snapshot accepted private database readiness")
			}
			snapshot["readiness"] = image["readiness"]
			if e = document.Components.Schemas["HostingSnapshot"].Value.VisitJSON(snapshot); e != nil {
				t.Fatalf("ordinary HTTP image snapshot rejected: %v", e)
			}
		})
	}
}

func TestHostingDatabaseExecutionHTTPAuthorizationAndClosedContract(t *testing.T) {
	mem := newSeededTestStore()
	repo := &databaseExecutionHTTPRepository{databaseHTTPRepository: &databaseHTTPRepository{MemoryStore: mem, view: domain.HostingDatabase{Resource: domain.Resource{ID: "res_database_execution", ProjectID: "proj_platform", Kind: "database"}}}}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repo, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(service, slog.Default(), web.Static())
	token := createTestSession(t, server)
	path := "/api/v1/resources/" + repo.view.Resource.ID + "/database/deployments"
	valid := `{"expected_generation":1,"idempotency_key":"database"}`
	request := func(url, credential, body string, status int) {
		t.Helper()
		r := httptest.NewRequest("POST", url, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if credential != "" {
			r.Header.Set("Authorization", "Bearer "+credential)
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if w.Code != status || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("database deployment response: %d %s cache=%s", w.Code, w.Body.String(), w.Header().Get("Cache-Control"))
		}
		if status == 202 {
			var operation domain.HostingOperation
			if e := json.Unmarshal(w.Body.Bytes(), &operation); e != nil || operation.State != "queued" || operation.ResourceID != repo.view.Resource.ID {
				t.Fatalf("operation response: %s %v", w.Body.String(), e)
			}
		}
	}
	request(path, "", valid, 401)
	for _, bad := range []string{`{}`, `null`, valid + `{}`, `{"expected_generation":null,"idempotency_key":"x"}`, `{"expected_generation":1,"expected_generation":2,"idempotency_key":"x"}`, `{"expected_generation":1,"idempotency_key":"x","host_port":18080}`, `{"Expected_generation":1,"idempotency_key":"x"}`, `{"expected_generation":1,"idempotency_key":"x","credentials":[]}`} {
		request(path, token, bad, 400)
	}
	request(path+"?host_port=18080", token, valid, 400)
	if repo.calls != 0 {
		t.Fatal("invalid input reached repository")
	}
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	for _, role := range []string{domain.RoleViewer, domain.RoleMaintainer} {
		account, e := service.CreateServiceAccount(admin, app.ServiceAccountCreateInput{ProjectID: "proj_platform", Name: "database deploy HTTP " + role, Role: role})
		if e != nil {
			t.Fatal(e)
		}
		status := 403
		if role == domain.RoleMaintainer {
			status = 202
		}
		request(path, account.Token, valid, status)
	}
	request(path, token, valid, 202)
	if repo.calls != 2 {
		t.Fatalf("unexpected authorized calls: %d", repo.calls)
	}
	// Generic resources cannot enter the private database endpoint.
	repo.view.Resource.Kind = "application"
	request(path, token, valid, 400)
	repo.view.Resource.Kind = "database"
	for _, failure := range []struct {
		err    error
		status int
	}{{store.ErrConflict, 409}, {store.ErrNotFound, 404}, {auth.ErrForbidden, 403}, {store.ErrHostingDatabaseUnavailable, 503}} {
		repo.err = failure.err
		request(path, token, valid, failure.status)
	}
	var _ store.HostingDatabaseExecutionRepository = repo
}

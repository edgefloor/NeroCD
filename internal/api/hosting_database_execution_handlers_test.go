package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/web"
)

type databaseExecutionHTTPRepository struct {
	*databaseHTTPRepository
	calls int
}

func (r *databaseExecutionHTTPRepository) RequestHostingDatabaseDeployment(_ context.Context, id, resource, actor string, _ domain.HostingDatabaseDeploymentInput, _ domain.AuditEvent) (domain.HostingOperation, error) {
	r.calls++
	return domain.HostingOperation{ID: id, ResourceID: resource, ProjectID: r.view.Resource.ProjectID, CreatedBy: actor, State: "queued"}, r.err
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

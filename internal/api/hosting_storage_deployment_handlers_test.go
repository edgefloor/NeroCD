package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/web"
)

type storageDeploymentHTTPRepository struct {
	*store.MemoryStore
	calls int
	err   error
}

func (r *storageDeploymentHTTPRepository) HostingStorageDeploymentOptions(_ context.Context, resource, _ string) (domain.HostingStorageDeploymentOptions, error) {
	r.calls++
	return domain.HostingStorageDeploymentOptions{ResourceID: resource, ResourceGeneration: 7, Reason: "no_previous_healthy", Routes: []domain.HostingRouteSwitchRoute{}}, r.err
}

func (r *storageDeploymentHTTPRepository) HostingStorageRouteContinuityStatus(_ context.Context, resource, _, operation string) (domain.HostingStorageRouteContinuityStatus, error) {
	r.calls++
	return domain.HostingStorageRouteContinuityStatus{ResourceID: resource, OperationID: operation, State: "proxy_pending", Reason: "pending"}, r.err
}

func TestHostingStorageDeploymentHTTP(t *testing.T) {
	mem := newSeededTestStore()
	repo := &storageDeploymentHTTPRepository{MemoryStore: mem}
	s, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: repo, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(s, slog.Default(), web.Static())
	admin := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	env, err := s.CreateProjectEnvironment(admin, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "storage API"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.CreateResource(admin, app.ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: env.ID, Name: "uploads", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/resources/" + r.ID
	paths := []string{base + "/storage-deployment-options", base + "/storage-route-continuity/hop_next"}
	request := func(method, path, token, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if method == http.MethodGet && rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("no-store absent: %s %s %+v", method, path, rec.Header())
		}
		return rec
	}
	for _, path := range paths {
		if rec := request(http.MethodGet, path, "", ""); rec.Code != http.StatusUnauthorized {
			t.Fatalf("anonymous %d %s", rec.Code, rec.Body.String())
		}
	}
	if repo.calls != 0 {
		t.Fatal("anonymous repository access")
	}
	token := createTestSession(t, server)
	viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	for _, path := range paths {
		if rec := request(http.MethodGet, path, viewer, ""); rec.Code != http.StatusForbidden {
			t.Fatalf("cross-project %d %s", rec.Code, rec.Body.String())
		}
		for _, suffix := range []string{"?unexpected=1", "?id=other", "?invalid=%zz"} {
			if rec := request(http.MethodGet, path+suffix, token, ""); rec.Code != http.StatusBadRequest {
				t.Fatalf("query %d %s", rec.Code, rec.Body.String())
			}
		}
		if rec := request(http.MethodGet, path, token, "{}"); rec.Code != http.StatusBadRequest {
			t.Fatalf("body %d %s", rec.Code, rec.Body.String())
		}
		if rec := request(http.MethodPost, path, token, "{}"); rec.Code != http.StatusNotFound {
			t.Fatalf("method %d %s", rec.Code, rec.Body.String())
		}
	}
	if repo.calls != 0 {
		t.Fatal("rejected request reached repository")
	}
	for _, path := range paths {
		rec := request(http.MethodGet, path, token, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("valid %d %s", rec.Code, rec.Body.String())
		}
		var data map[string]json.RawMessage
		if err = json.Unmarshal(rec.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		if string(data["resource_id"]) != `"`+r.ID+`"` {
			t.Fatal(rec.Body.String())
		}
		for _, private := range []string{"snapshot", "daemon_id", "configuration", "https_ready", "loaded_generation"} {
			if _, found := data[private]; found {
				t.Fatalf("private/unobserved %s", rec.Body.String())
			}
		}
	}
	for _, project := range []string{r.ProjectID, "proj_security"} {
		account, createErr := s.CreateServiceAccount(admin, app.ServiceAccountCreateInput{ProjectID: project, Name: "storage API viewer", Role: domain.RoleViewer})
		if createErr != nil {
			t.Fatal(createErr)
		}
		want := http.StatusForbidden
		if project == r.ProjectID {
			want = http.StatusOK
		}
		for _, path := range paths {
			if rec := request(http.MethodGet, path, account.Token, ""); rec.Code != want {
				t.Fatalf("machine %s: %d %s", project, rec.Code, rec.Body.String())
			}
		}
	}
	for _, mapping := range []struct {
		err  error
		code int
	}{{store.ErrHostingStorageUnavailable, 503}, {store.ErrNotFound, 404}, {store.ErrConflict, 409}, {auth.ErrForbidden, 403}} {
		repo.err = mapping.err
		if rec := request(http.MethodGet, paths[0], token, ""); rec.Code != mapping.code {
			t.Fatalf("%v: %d %s", mapping.err, rec.Code, rec.Body.String())
		}
	}
}

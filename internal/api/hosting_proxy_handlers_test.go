package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
)

type proxyAuthorityAPIStore struct {
	*store.MemoryStore
	store.HostingProxyRepository
	expected domain.HostingAuthority
	calls    int
}

func TestRouteSwitchReadCacheHeadersPrecedeAuthentication(t *testing.T) {
	server, _ := newTestServer()
	for _, test := range []struct {
		method, path string
		noStore      bool
	}{
		{http.MethodGet, "/api/v1/resources/res_own/route-switches", true},
		{http.MethodGet, "/api/v1/resources/res_own/route-switches/pop_own", true},
		{http.MethodGet, "/api/v1/resources/res_own/route-switchess", false},
		{http.MethodGet, "/api/v1/resources/res_own/route-switches/pop_own/extra", false},
		{http.MethodGet, "/api/v1/resources/res_own/route-switches/", false},
		{http.MethodGet, "/api/v1/resources//route-switches", false},
		{http.MethodGet, "/api/v1/projects/res_own/route-switches", false},
		{http.MethodPost, "/api/v1/resources/res_own/route-switches", false},
	} {
		t.Run(test.method+test.path, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if (recorder.Header().Get("Cache-Control") == "no-store") != test.noStore {
				t.Fatalf("cache header status=%d headers=%v", recorder.Code, recorder.Header())
			}
			if test.noStore && recorder.Code != http.StatusUnauthorized {
				t.Fatalf("unauthenticated status=%d", recorder.Code)
			}
		})
	}
}

func (s *proxyAuthorityAPIStore) check(runner string, authority domain.HostingAuthority) (domain.HostingProxyOperation, error) {
	s.calls++
	if runner != "runner_proxy_auth" || authority != s.expected {
		return domain.HostingProxyOperation{}, store.ErrConflict
	}
	return domain.HostingProxyOperation{ID: authority.OperationID}, nil
}
func (s *proxyAuthorityAPIStore) RunnerProxyPlan(_ context.Context, runner string, in domain.HostingAuthority) (domain.HostingProxyOperation, error) {
	return s.check(runner, in)
}
func (s *proxyAuthorityAPIStore) RunnerProxyPrepared(_ context.Context, runner string, in domain.HostingProxyPreparedRequest) (domain.HostingProxyOperation, error) {
	return s.check(runner, in.HostingAuthority)
}
func (s *proxyAuthorityAPIStore) RunnerProxyApplyBegin(_ context.Context, runner string, in domain.HostingAuthority) (domain.HostingProxyOperation, error) {
	return s.check(runner, in)
}
func (s *proxyAuthorityAPIStore) RunnerProxyComplete(_ context.Context, runner string, in domain.HostingProxyCompleteRequest) (domain.HostingProxyOperation, error) {
	return s.check(runner, in.HostingAuthority)
}
func (s *proxyAuthorityAPIStore) RunnerProxyStatus(_ context.Context, runner string, in domain.HostingAuthority) (domain.HostingProxyOperation, error) {
	return s.check(runner, in)
}

func TestHostingProxyRunnerHandlersPreserveFullAuthority(t *testing.T) {
	server, mem := newTestServer()
	repository := &proxyAuthorityAPIStore{MemoryStore: mem, expected: domain.HostingAuthority{OperationID: "pop_one", RunID: "proxy_pop_one", LeaseID: "lease_one", Attempt: 2, Fence: "fence_one"}}
	service, err := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, ServiceAccounts: mem, OIDC: mem, Hosting: mem, Destinations: mem, ManagedSecrets: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: repository, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem, Observability: mem, ObservationWriter: mem, ObservationReader: mem, Retention: mem})
	if err != nil {
		t.Fatal(err)
	}
	server.app = service
	token := "proxy-authority-fixture-token"
	hash := sha256.Sum256([]byte(token))
	if _, err = mem.RegisterRunner(t.Context(), domain.Runner{ID: "runner_proxy_auth", Name: "proxy authority", TokenHash: hex.EncodeToString(hash[:]), Status: domain.RunnerActive, Capabilities: []string{domain.RunTypeProxyApply}}); err != nil {
		t.Fatal(err)
	}
	valid := `{"operation_id":"pop_one","run_id":"proxy_pop_one","lease_id":"lease_one","attempt":2,"fence":"fence_one"}`
	for _, endpoint := range []string{"plan", "prepared", "apply-begin", "complete", "status"} {
		for _, body := range []string{valid, strings.Replace(valid, `"fence_one"`, `"foreign_fence"`, 1), strings.Replace(valid, `"lease_one"`, `"foreign_lease"`, 1), strings.Replace(valid, `"attempt":2`, `"attempt":1`, 1)} {
			want := http.StatusConflict
			if body == valid {
				want = http.StatusOK
			}
			rec := runnerEnvelopeRequest(t, server, token, http.MethodPost, "/api/v1/runners/proxy/"+endpoint, body)
			if rec.Code != want {
				t.Fatalf("%s authority status=%d want=%d body=%s", endpoint, rec.Code, want, rec.Body.String())
			}
		}
	}
	if repository.calls != 20 {
		t.Fatalf("authority repository calls=%d want=20", repository.calls)
	}
}

func TestHostingProxyRunnerEndpointsRequireRunnerCredentials(t *testing.T) {
	server, mem := newTestServer()
	actor := createTestSession(t, server)
	token := "proxy-api-fixture-runner-token"
	hash := sha256.Sum256([]byte(token))
	if _, err := mem.RegisterRunner(t.Context(), domain.Runner{ID: "runner_proxy_auth", Name: "proxy auth", TokenHash: hex.EncodeToString(hash[:]), Status: domain.RunnerActive, Capabilities: []string{domain.RunTypeProxyApply}}); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{"plan", "prepared", "apply-begin", "complete", "status"} {
		t.Run(endpoint, func(t *testing.T) {
			for _, credential := range []struct {
				name, token string
				status      int
			}{
				{"missing", "", http.StatusUnauthorized},
				{"wrong", "invalid-runner-token", http.StatusUnauthorized},
				{"actor", actor, http.StatusUnauthorized},
				{"runner-memory-unavailable", token, http.StatusServiceUnavailable},
			} {
				t.Run(credential.name, func(t *testing.T) {
					req := httptest.NewRequest(http.MethodPost, "/api/v1/runners/proxy/"+endpoint, strings.NewReader(`{}`))
					req.Header.Set("Content-Type", "application/json")
					if credential.token != "" {
						req.Header.Set("Authorization", "Bearer "+credential.token)
					}
					rec := httptest.NewRecorder()
					server.ServeHTTP(rec, req)
					if rec.Code != credential.status {
						t.Fatalf("status=%d want=%d body=%s", rec.Code, credential.status, rec.Body.String())
					}
				})
			}
		})
	}
}

func TestHostingProxyAPIMemoryFailsClosed(t *testing.T) {
	server, _ := newTestServer()
	admin := createTestSession(t, server)
	viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	for _, test := range []struct {
		method, path, token, body string
		status                    int
	}{
		{http.MethodGet, "/api/v1/servers/srv_proxy/proxy", "", "", 401},
		{http.MethodGet, "/api/v1/servers/srv_proxy/proxy", viewer, "", 403},
		{http.MethodGet, "/api/v1/servers/srv_proxy/proxy", admin, "", 503},
		{http.MethodPost, "/api/v1/servers/srv_proxy/proxy", admin, `{"control_project_id":"proj_platform","idempotency_key":"enable","configuration":{"email":"operator@nerocd.invalid","ca":"letsencrypt-staging","management_port":19080}}`, 503},
		{http.MethodPost, "/api/v1/resources/res_proxy/routes", admin, `{"hostname":"web.nerocd.invalid","path_prefix":"/","idempotency_key":"route","expected_healthy_operation_id":"hop_healthy","host_port":18080}`, 400},
		{http.MethodPost, "/api/v1/resources/res_proxy/routes", admin, `{"hostname":"web.nerocd.invalid","path_prefix":"/","idempotency_key":"route","expected_healthy_operation_id":"hop_healthy","upstream_url":"http://foreign:8080"}`, 400},
	} {
		req := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
		req.Header.Set("Content-Type", "application/json")
		if test.token != "" {
			req.Header.Set("Authorization", "Bearer "+test.token)
		}
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != test.status {
			t.Fatalf("%s %s status=%d want=%d body=%s", test.method, test.path, rec.Code, test.status, rec.Body.String())
		}
	}
}

func TestHostingRetainedReleaseAPIMemoryFailsClosed(t *testing.T) {
	server, mem := newTestServer()
	ctx := auth.WithPrincipal(t.Context(), auth.Principal{ID: "usr_bootstrap", Provider: domain.PrincipalLocal, Roles: []string{domain.RoleSystemAdmin}})
	environment, err := server.app.CreateProjectEnvironment(ctx, app.ProjectEnvironmentInput{ProjectID: "proj_platform", Name: "retained"})
	if err != nil {
		t.Fatal(err)
	}
	resource, err := server.app.CreateResource(ctx, app.ResourceInput{ProjectID: "proj_platform", ProjectEnvironmentID: environment.ID, Name: "retained-web", Kind: "application", Source: domain.ResourceSource{Kind: "image", Image: domain.ImageSource{Reference: "nginx:1.27"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = mem.UpsertProjectMember(t.Context(), domain.ProjectMember{ID: "member_retained_viewer", ProjectID: "proj_platform", UserID: "usr_viewer", Role: domain.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	admin := createTestSession(t, server)
	viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	for _, test := range []struct {
		method, path, token, body string
		status                    int
	}{
		{http.MethodPost, "/route-switches", "", `{}`, 401},
		{http.MethodGet, "/route-switches", "", "", 401},
		{http.MethodGet, "/route-switches", viewer, "", 503},
		{http.MethodGet, "/route-switches", admin, "", 503},
		{http.MethodGet, "/route-switches/pop_switch", viewer, "", 503},
		{http.MethodGet, "/route-switches/pop_switch", admin, "", 503},
		{http.MethodPost, "/route-switches", viewer, `{"idempotency_key":"switch","expected_loaded_generation":1,"target_release_operation_id":"hop_b","routes":[{"route_id":"route_a","expected_version":1,"expected_selected_release_operation_id":"hop_a"}]}`, 403},
		{http.MethodPost, "/route-switches", admin, `{"idempotency_key":"switch","expected_loaded_generation":1,"target_release_operation_id":"hop_b","routes":[{"route_id":"route_a","expected_version":1,"expected_selected_release_operation_id":"hop_a"}]}`, 503},
		{http.MethodPost, "/route-switches", admin, `{"idempotency_key":"switch","host_port":18080}`, 400},
		{http.MethodPost, "/route-switches", admin, `{"idempotency_key":"switch","expected_loaded_generation":1,"target_release_operation_id":"hop_b","routes":[]}`, 400},
		{http.MethodGet, "/retained-releases", "", "", 401},
		{http.MethodGet, "/retained-releases", viewer, "", 503},
		{http.MethodPost, "/retained-releases/hop_previous/cleanup", viewer, `{"expected_generation":1,"idempotency_key":"cleanup"}`, 403},
		{http.MethodPost, "/retained-releases/hop_previous/cleanup", admin, `{"expected_generation":1,"idempotency_key":"cleanup"}`, 503},
		{http.MethodPost, "/retained-releases/hop_previous/cleanup", admin, `{"expected_generation":1,"idempotency_key":"cleanup","container_id":"foreign"}`, 400},
	} {
		r := httptest.NewRequest(test.method, "/api/v1/resources/"+resource.ID+test.path, strings.NewReader(test.body))
		r.Header.Set("Content-Type", "application/json")
		if test.token != "" {
			r.Header.Set("Authorization", "Bearer "+test.token)
		}
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if test.method == http.MethodGet && strings.HasPrefix(test.path, "/route-switches") && test.token != "" && w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("missing no-store: %v", w.Header())
		}
		if w.Code != test.status {
			t.Fatalf("%s %s status=%d want=%d body=%s", test.method, test.path, w.Code, test.status, w.Body.String())
		}
	}
}

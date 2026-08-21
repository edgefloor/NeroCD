package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/web"
)

type unavailableProjects struct{ store.ProjectRepository }

func (unavailableProjects) ListProjects(context.Context) ([]domain.Project, error) {
	return nil, errors.New("database schema unavailable")
}

type incompatibleProjects struct{ store.ProjectRepository }

func (incompatibleProjects) SchemaCompatible(context.Context) (bool, error) { return false, nil }

func newTestServer() (*Server, *store.MemoryStore) {
	mem := newSeededTestStore()
	service := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem})
	return NewServer(service, slog.Default(), web.Static()), mem
}

func newTestServerWithConfig(cfg ServerConfig) (*Server, *store.MemoryStore) {
	mem := newSeededTestStore()
	service := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, Projects: mem, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem, Deployments: mem})
	return NewServerWithConfig(service, slog.Default(), web.Static(), cfg), mem
}

func newSeededTestStore() *store.MemoryStore {
	hash, err := auth.HashPassword("admin")
	if err != nil {
		panic(err)
	}
	viewerHash, err := auth.HashPassword("viewer")
	if err != nil {
		panic(err)
	}
	return store.NewFixtureMemoryStore("admin@example.local", "viewer@example.local", hash, viewerHash)
}

func TestServerServesEmbeddedApplicationShell(t *testing.T) {
	server, _ := newTestServer()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / returned %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Fatalf("GET / Content-Type = %q, want HTML", contentType)
	}
	for header, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "strict-origin-when-cross-origin",
	} {
		if got := rec.Header().Get(header); got != want {
			t.Fatalf("GET / %s=%q want %q", header, got, want)
		}
	}
	for _, marker := range []string{"<!doctype html>", "<title>NeroCD</title>", `type="module"`, "<body"} {
		if !strings.Contains(body, marker) {
			t.Fatalf("GET / did not serve a valid embedded application shell; missing %q in %q", marker, body)
		}
	}
}

func TestStaticAssetsDenySourceMapsAndSetCacheHeaders(t *testing.T) {
	static := fstest.MapFS{
		"index.html":                     {Data: []byte("<!doctype html><title>NeroCD</title>")},
		"assets/index-z8Z1weLE.js":       {Data: []byte("console.log('app')")},
		"assets/index-CqM-jSVW.css":      {Data: []byte("body{}")},
		"assets/operator-report-2024.js": {Data: []byte("console.log('report')")},
		"scripts/index-z8Z1weLE.js":      {Data: []byte("console.log('not an asset')")},
		"assets/index-z8Z1weLE.js.map":   {Data: []byte(`{"sources":["private.ts"]}`)},
	}
	handler := spaFileServer(static)
	for _, test := range []struct {
		path      string
		wantCode  int
		wantCache string
		wantBody  string
	}{
		{path: "/", wantCode: http.StatusOK, wantCache: "no-cache", wantBody: "NeroCD"},
		{path: "/projects", wantCode: http.StatusOK, wantCache: "no-cache", wantBody: "NeroCD"},
		{path: "/assets/index-z8Z1weLE.js", wantCode: http.StatusOK, wantCache: "public, max-age=31536000, immutable", wantBody: "console.log('app')"},
		{path: "/assets/index-CqM-jSVW.css", wantCode: http.StatusOK, wantCache: "public, max-age=31536000, immutable", wantBody: "body{}"},
		{path: "/assets/operator-report-2024.js", wantCode: http.StatusOK, wantCache: "no-cache", wantBody: "report"},
		{path: "/scripts/index-z8Z1weLE.js", wantCode: http.StatusOK, wantCache: "no-cache", wantBody: "not an asset"},
		{path: "/assets/index-z8Z1weLE.js.map", wantCode: http.StatusNotFound},
		{path: "/unknown.map", wantCode: http.StatusNotFound},
	} {
		t.Run(test.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, test.path, nil))
			if rec.Code != test.wantCode {
				t.Fatalf("GET %s = %d, want %d", test.path, rec.Code, test.wantCode)
			}
			if test.wantCache != "" && rec.Header().Get("Cache-Control") != test.wantCache {
				t.Fatalf("GET %s Cache-Control = %q, want %q", test.path, rec.Header().Get("Cache-Control"), test.wantCache)
			}
			if test.wantCode == http.StatusOK && rec.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("GET %s X-Content-Type-Options = %q, want nosniff", test.path, rec.Header().Get("X-Content-Type-Options"))
			}
			if test.wantBody != "" && !strings.Contains(rec.Body.String(), test.wantBody) {
				t.Fatalf("GET %s body = %q, want %q", test.path, rec.Body.String(), test.wantBody)
			}
		})
	}
}

func TestProtectedRoutesRequireBearerSession(t *testing.T) {
	server, _ := newTestServer()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/me without token returned %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestGetDeploymentByIDUsesProjectViewAuthorizationWithoutExistenceLeak(t *testing.T) {
	server, mem := newTestServer()
	now := time.Now().UTC()
	if _, err := mem.CreateService(t.Context(), domain.Service{ID: "svc_detail", ProjectID: "proj_platform", RepositoryID: "repo_platform_runbooks", Name: "Detail", ComposePath: "compose.yaml", OwnerID: "usr_bootstrap", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.CreateEnvironment(t.Context(), domain.Environment{ID: "env_detail", ServiceID: "svc_detail", Name: "Production", ComposeProject: "detail", TimeoutSeconds: 60, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, err := mem.CreateRevision(t.Context(), domain.Revision{ID: "rev_detail", ServiceID: "svc_detail", RequestedRef: "main", GitCommit: strings.Repeat("a", 40), ComposeHash: "sha256:detail", ImageDigests: []string{"sha256:detail"}, ContentIdentity: "detail", CreatedBy: "usr_bootstrap", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	run := domain.TaskRun{ID: "run_detail", ProjectID: "proj_platform", RunSpec: domain.RunSpec{Type: domain.RunTypeComposeDeploy, Inputs: map[string]any{}}, Workflow: domain.Workflow{Steps: []domain.WorkflowStep{}}, WorkflowState: domain.WorkflowState{Steps: []domain.WorkflowStepState{}}, Status: domain.RunQueued, RequestedBy: "usr_bootstrap", StartedAt: now}
	deployment, err := mem.CreateDeploymentRequest(t.Context(), domain.Deployment{ID: "dep_detail", EnvironmentID: "env_detail", DesiredRevisionID: "rev_detail", IdempotencyKey: "detail-intent", Status: domain.DeploymentQueued, RequestedBy: "usr_bootstrap", CreatedAt: now, UpdatedAt: now, FenceRequired: true}, run, domain.AuditEvent{ID: "audit_detail", ActorID: "usr_bootstrap", Action: "deployment.create", TargetID: "dep_detail", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	admin := createTestSession(t, server)
	adminReq := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/dep_detail", nil)
	adminReq.Header.Set("Authorization", "Bearer "+admin)
	adminRec := httptest.NewRecorder()
	server.ServeHTTP(adminRec, adminReq)
	if adminRec.Code != http.StatusOK || !strings.Contains(adminRec.Body.String(), `"task_run_id":"run_detail"`) || !strings.Contains(adminRec.Body.String(), `"id":"`+deployment.ID+`"`) {
		t.Fatalf("authorized deployment detail=%d %s", adminRec.Code, adminRec.Body.String())
	}
	viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	for _, id := range []string{"dep_detail", "dep_absent"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/deployments/"+id, nil)
		req.Header.Set("Authorization", "Bearer "+viewer)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "dep_detail") {
			t.Fatalf("viewer deployment %s = %d %s, want indistinguishable not-found", id, rec.Code, rec.Body.String())
		}
	}
}

func TestRequestBoundaryRejectsAmbiguousJSONAndUnboundedPagination(t *testing.T) {
	server, _ := newTestServerWithConfig(ServerConfig{PublicOrigin: "https://nerocd.example.invalid"})
	token := createTestSession(t, server)
	for _, test := range []struct {
		name        string
		contentType string
		body        string
		want        int
	}{
		{name: "missing content type", body: `{}`, want: http.StatusUnsupportedMediaType},
		{name: "wrong content type", contentType: "text/plain", body: `{}`, want: http.StatusUnsupportedMediaType},
		{name: "multiple documents", contentType: "application/json", body: `{} {}`, want: http.StatusBadRequest},
		{name: "valid content type parameter", contentType: "application/json; charset=utf-8", body: `{"name":"bounded"}`, want: http.StatusCreated},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(test.body))
			req.Header.Set("Authorization", "Bearer "+token)
			if test.contentType != "" {
				req.Header.Set("Content-Type", test.contentType)
			}
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != test.want {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if rec.Header().Get("X-Request-ID") == "" || rec.Header().Get("Strict-Transport-Security") == "" || rec.Header().Get("Content-Security-Policy") == "" {
				t.Fatalf("security/request headers=%v", rec.Header())
			}
		})
	}
	large := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(strings.Repeat(" ", maxJSONBodyBytes+1)))
	large.Header.Set("Authorization", "Bearer "+token)
	large.Header.Set("Content-Type", "application/json")
	largeRec := httptest.NewRecorder()
	server.ServeHTTP(largeRec, large)
	if largeRec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("large body status=%d", largeRec.Code)
	}
	page := httptest.NewRequest(http.MethodGet, "/api/v1/projects?limit=101", nil)
	page.Header.Set("Authorization", "Bearer "+token)
	pageRec := httptest.NewRecorder()
	server.ServeHTTP(pageRec, page)
	if pageRec.Code != http.StatusBadRequest {
		t.Fatalf("unbounded page status=%d", pageRec.Code)
	}
	unsafeID := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	unsafeID.Header.Set("Authorization", "Bearer "+token)
	unsafeID.Header.Set("X-Request-ID", strings.Repeat("a", maxRequestIDLen+1))
	unsafeRec := httptest.NewRecorder()
	server.ServeHTTP(unsafeRec, unsafeID)
	if got := unsafeRec.Header().Get("X-Request-ID"); got == strings.Repeat("a", maxRequestIDLen+1) || !safeRequestID.MatchString(got) {
		t.Fatalf("unsafe request ID echoed as %q", got)
	}
}

func TestSecurityHeadersKeepExecutableContentStrict(t *testing.T) {
	server, _ := newTestServerWithConfig(ServerConfig{PublicOrigin: "https://nerocd.example.invalid"})
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	const wantCSP = "default-src 'self'; style-src 'self' 'unsafe-inline'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'; object-src 'none'"
	if got := rec.Header().Get("Content-Security-Policy"); got != wantCSP {
		t.Fatalf("Content-Security-Policy=%q, want %q", got, wantCSP)
	}
	for _, forbidden := range []string{"script-src", "'unsafe-eval'", "data:", "blob:", "*", "http://", "https://"} {
		if strings.Contains(rec.Header().Get("Content-Security-Policy"), forbidden) {
			t.Fatalf("CSP unexpectedly admits executable source %q: %q", forbidden, rec.Header().Get("Content-Security-Policy"))
		}
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options=%q, want nosniff", got)
	}
	if got := rec.Header().Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Fatalf("Referrer-Policy=%q", got)
	}
}

func TestEveryMutationRouteRejectsUnknownTopLevelFieldsWithoutSideEffects(t *testing.T) {
	server, mem := newTestServer()
	adminToken := createTestSession(t, server)
	runnerToken := "runner-strict-schema-token"
	hash := sha256.Sum256([]byte(runnerToken))
	if _, err := mem.RegisterRunner(t.Context(), domain.Runner{ID: "runner_strict_schema", Name: "strict", Tags: []string{}, Capabilities: []string{"shell", "compose_deploy"}, TokenHash: hex.EncodeToString(hash[:]), Status: domain.RunnerActive, RegisteredAt: time.Now().UTC(), LastHeartbeatAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	beforeAudits, err := mem.ListAuditEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	beforeProjects, _ := mem.ListProjects(t.Context())
	beforeRuns, _ := mem.ListRuns(t.Context(), "")
	beforeRunners, _ := mem.ListRunners(t.Context())
	for _, route := range PublicRoutes() {
		if route.Method != http.MethodPost && route.Method != http.MethodPut && route.Method != http.MethodPatch {
			continue
		}
		path := strings.Replace(route.Path, "{id}", "repo_platform_runbooks", 1)
		t.Run(route.Method+" "+path, func(t *testing.T) {
			req := httptest.NewRequest(route.Method, path, strings.NewReader(`{"unknown_field":true}`))
			req.Header.Set("Content-Type", "application/json")
			switch {
			case requiresRunnerAuth(route.Path):
				req.Header.Set("Authorization", "Bearer "+runnerToken)
			case route.Path == "/api/v1/runner-enrollments/consume":
				// This endpoint receives the enrollment credential directly; schema
				// validation must still happen before it can consume anything.
				req.Header.Set("Authorization", "Bearer enrollment-placeholder")
			case requiresAuth(route.Path):
				req.Header.Set("Authorization", "Bearer "+adminToken)
			}
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
				t.Fatalf("unknown top-level response=%d %s", rec.Code, rec.Body.String())
			}
		})
	}
	afterAudits, _ := mem.ListAuditEvents(t.Context())
	afterProjects, _ := mem.ListProjects(t.Context())
	afterRuns, _ := mem.ListRuns(t.Context(), "")
	afterRunners, _ := mem.ListRunners(t.Context())
	if len(afterAudits) != len(beforeAudits) || len(afterProjects) != len(beforeProjects) || len(afterRuns) != len(beforeRuns) || len(afterRunners) != len(beforeRunners) {
		t.Fatalf("unknown field caused state change: audits %d/%d projects %d/%d runs %d/%d runners %d/%d", len(beforeAudits), len(afterAudits), len(beforeProjects), len(afterProjects), len(beforeRuns), len(afterRuns), len(beforeRunners), len(afterRunners))
	}
}

func FuzzStrictJSONDocument(f *testing.F) {
	f.Add([]byte(`{}`))
	f.Add([]byte(`{} {}`))
	f.Add([]byte(`{"unterminated"`))
	f.Fuzz(func(t *testing.T, body []byte) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		strictJSONDocument(rec, req)
	})
}

func FuzzUnknownTopLevelFieldIsRejected(f *testing.F) {
	f.Add("unknown_field")
	f.Add("x")
	f.Fuzz(func(t *testing.T, field string) {
		if field == "" {
			t.Skip()
		}
		field = "unknown_" + hex.EncodeToString([]byte(field))
		server, _ := newTestServer()
		token := createTestSession(t, server)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"valid","`+field+`":true}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"code":"invalid_request"`) {
			t.Fatalf("field %q response=%d %s", field, rec.Code, rec.Body.String())
		}
	})
}

func TestRepositoryPolicyConfigurationIsStrictIdempotentAndNonLeaking(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSession(t, server)
	body := `{"project_id":"proj_platform","configuration_id":"cfg_12345678","policy":{"version":1,"state":"configured","mode":"public","allowed_schemes":["https"],"allowed_hosts":["git.example.local"],"credential_reference_id":"cred_12345678"}}`
	request := func(payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/repositories/repo_platform_runbooks/policy", strings.NewReader(payload))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	first := request(body)
	if first.Code != http.StatusOK {
		t.Fatalf("first configure = %d: %s", first.Code, first.Body.String())
	}
	if strings.Contains(first.Body.String(), "cred_12345678") {
		t.Fatalf("credential reference leaked: %s", first.Body.String())
	}
	if retry := request(body); retry.Code != http.StatusOK {
		t.Fatalf("idempotent retry = %d: %s", retry.Code, retry.Body.String())
	}
	if conflict := request(strings.Replace(body, "git.example.local", "other.example.local", 1)); conflict.Code != http.StatusConflict {
		t.Fatalf("mismatched replay = %d", conflict.Code)
	}
	if unknown := request(strings.Replace(body, `"policy":{`, `"policy":{"secret":"sentinel",`, 1)); unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown policy field = %d", unknown.Code)
	}
	if outer := request(strings.Replace(body, `"project_id":`, `"unknown":"sentinel","project_id":`, 1)); outer.Code != http.StatusBadRequest {
		t.Fatalf("unknown outer field = %d", outer.Code)
	}
	viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	req := httptest.NewRequest(http.MethodPut, "/api/v1/repositories/repo_platform_runbooks/policy", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+viewer)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("viewer configure = %d", rec.Code)
	}
}

func TestRunSecretProviderValidationRejectsUnsafeProductionBindings(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSession(t, server)
	tests := []struct {
		name   string
		secret string
	}{
		{name: "unsupported", secret: `{"name":"db","provider":"database","reference":"logical","target":"env:TOKEN","required":true}`},
		{name: "unclassified_env", secret: `{"name":"env","provider":"env","reference":"TOKEN","target":"env:TOKEN","required":true}`},
		{name: "path_reference", secret: `{"name":"file","provider":"runner_file","reference":"../host/path","target":"env:TOKEN","required":true,"version":"v1"}`},
		{name: "host_path_reference", secret: `{"name":"file","provider":"runner_file","reference":"/etc/shadow","target":"env:TOKEN","required":true,"version":"v1"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := `{"project_id":"proj_platform","run_spec":{"type":"shell","inputs":{},"process":{"command":["true"]},"secrets":[` + tt.secret + `]},"runner_tags":["local"]}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(body))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("unsafe binding returned %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
	validBody := `{"project_id":"proj_platform","run_spec":{"type":"shell","inputs":{},"process":{"command":["true"]},"secrets":[{"name":"database-password","provider":"runner_file","reference":"database-password","target":"env:DATABASE_PASSWORD","required":true,"version":"v1","redact_encodings":["base64","base64url","hex"]}]},"runner_tags":["local"]}`
	validReq := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(validBody))
	validReq.Header.Set("Authorization", "Bearer "+token)
	validReq.Header.Set("Content-Type", "application/json")
	validRec := httptest.NewRecorder()
	server.ServeHTTP(validRec, validReq)
	if validRec.Code != http.StatusCreated || strings.Contains(validRec.Body.String(), "/etc/") || !strings.Contains(validRec.Body.String(), `"reference":"database-password"`) {
		t.Fatalf("safe logical binding response=%d %s", validRec.Code, validRec.Body.String())
	}
}

func TestReadinessIsPublicAndMetricsRequireAdmin(t *testing.T) {
	server, _ := newTestServer()

	readyReq := httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil)
	readyRec := httptest.NewRecorder()
	server.ServeHTTP(readyRec, readyReq)
	if readyRec.Code != http.StatusOK || !strings.Contains(readyRec.Body.String(), `"status":"ready"`) {
		t.Fatalf("GET /api/v1/ready returned %d: %s", readyRec.Code, readyRec.Body.String())
	}

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	server.ServeHTTP(metricsRec, metricsReq)
	if metricsRec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /metrics without credentials returned %d: %s", metricsRec.Code, metricsRec.Body.String())
	}
	metricsReq.Header.Set("Authorization", "Bearer "+createTestSession(t, server))
	metricsRec = httptest.NewRecorder()
	server.ServeHTTP(metricsRec, metricsReq)
	if metricsRec.Code != http.StatusOK || !strings.Contains(metricsRec.Body.String(), "nerocd_http_requests_total") || !strings.Contains(metricsRec.Body.String(), "nerocd_queue_depth") || strings.Contains(metricsRec.Body.String(), "runner_id=") {
		t.Fatalf("GET /metrics as admin returned %d: %s", metricsRec.Code, metricsRec.Body.String())
	}
}

func TestBootstrapAndOperationsStatusAreBoundedAndRoleProtected(t *testing.T) {
	empty := store.NewMemoryStore()
	emptyService := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: empty, Sessions: empty, APITokens: empty, Projects: empty, Members: empty, Templates: empty, Sources: empty, Runs: empty, Runners: empty, Approvals: empty, Audit: empty})
	emptyServer := NewServer(emptyService, slog.Default(), web.Static())
	required := httptest.NewRecorder()
	emptyServer.ServeHTTP(required, httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap-status", nil))
	if required.Code != http.StatusOK || required.Body.String() != "{\"status\":\"required\"}\n" {
		t.Fatalf("empty bootstrap state=%d %s", required.Code, required.Body.String())
	}

	server, _ := newTestServer()
	complete := httptest.NewRecorder()
	server.ServeHTTP(complete, httptest.NewRequest(http.MethodGet, "/api/v1/bootstrap-status", nil))
	if complete.Code != http.StatusOK || complete.Body.String() != "{\"status\":\"complete\"}\n" {
		t.Fatalf("complete bootstrap state=%d %s", complete.Code, complete.Body.String())
	}
	unauthenticated := httptest.NewRecorder()
	server.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/api/v1/operations/status", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous operations status=%d", unauthenticated.Code)
	}
	viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	forbidden := httptest.NewRecorder()
	viewerRequest := httptest.NewRequest(http.MethodGet, "/api/v1/operations/status", nil)
	viewerRequest.Header.Set("Authorization", "Bearer "+viewer)
	server.ServeHTTP(forbidden, viewerRequest)
	if forbidden.Code != http.StatusForbidden {
		t.Fatalf("viewer operations status=%d", forbidden.Code)
	}
	admin := createTestSession(t, server)
	ready := httptest.NewRecorder()
	adminRequest := httptest.NewRequest(http.MethodGet, "/api/v1/operations/status", nil)
	adminRequest.Header.Set("Authorization", "Bearer "+admin)
	server.ServeHTTP(ready, adminRequest)
	if ready.Code != http.StatusOK || !strings.Contains(ready.Body.String(), `"readiness":"ready"`) || !strings.Contains(ready.Body.String(), `"snapshot"`) || strings.Contains(ready.Body.String(), "admin@example.local") {
		t.Fatalf("admin operations status=%d %s", ready.Code, ready.Body.String())
	}

	incompatible := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: empty, Sessions: empty, APITokens: empty, Projects: incompatibleProjects{empty}, Members: empty, Templates: empty, Sources: empty, Runs: empty, Runners: empty, Approvals: empty, Audit: empty})
	if _, err := incompatible.BootstrapAdmin(t.Context(), app.BootstrapAdminInput{Email: "ops@example.invalid", Name: "Ops", Password: "correct horse battery staple"}); err != nil {
		t.Fatal(err)
	}
	opsSession, err := incompatible.CreateSession(t.Context(), "ops@example.invalid", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	incompatibleServer := NewServer(incompatible, slog.Default(), web.Static())
	noPartial := httptest.NewRecorder()
	noPartialRequest := httptest.NewRequest(http.MethodGet, "/api/v1/operations/status", nil)
	noPartialRequest.Header.Set("Authorization", "Bearer "+opsSession.Token)
	incompatibleServer.ServeHTTP(noPartial, noPartialRequest)
	if noPartial.Code != http.StatusServiceUnavailable || strings.Contains(noPartial.Body.String(), "snapshot") || strings.Contains(noPartial.Body.String(), "schema") {
		t.Fatalf("incompatible operations status=%d %s", noPartial.Code, noPartial.Body.String())
	}
}

func TestRunLogRetentionIsSystemAdminOnlyAndRequestReplaySafe(t *testing.T) {
	server, _ := newTestServer()
	viewer := createTestSessionFor(t, server, "viewer@example.local", "viewer")
	forbidden := httptest.NewRequest(http.MethodGet, "/api/v1/run-log-retention", nil)
	forbidden.Header.Set("Authorization", "Bearer "+viewer)
	forbiddenRec := httptest.NewRecorder()
	server.ServeHTTP(forbiddenRec, forbidden)
	if forbiddenRec.Code != http.StatusForbidden {
		t.Fatalf("viewer retention status=%d", forbiddenRec.Code)
	}
	admin := createTestSession(t, server)
	update := httptest.NewRequest(http.MethodPut, "/api/v1/run-log-retention", strings.NewReader(`{"enabled":true,"keep_days":7,"batch_size":5}`))
	update.Header.Set("Authorization", "Bearer "+admin)
	update.Header.Set("Content-Type", "application/json")
	updated := httptest.NewRecorder()
	server.ServeHTTP(updated, update)
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"enabled":true`) || strings.Contains(updated.Body.String(), "log_") {
		t.Fatalf("update retention=%d %s", updated.Code, updated.Body.String())
	}
	var status struct {
		Policy struct {
			Version int `json:"version"`
		} `json:"policy"`
	}
	if err := json.Unmarshal(updated.Body.Bytes(), &status); err != nil || status.Policy.Version < 1 {
		t.Fatalf("decode retention status=%#v err=%v", status, err)
	}
	execute := func(version int) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/run-log-retention/execute", strings.NewReader(fmt.Sprintf(`{"policy_version":%d}`, version)))
		req.Header.Set("Authorization", "Bearer "+admin)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Request-ID", "retention_api_replay_001")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	first := execute(status.Policy.Version)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"deleted"`) || strings.Contains(first.Body.String(), "run_") {
		t.Fatalf("first retention execute=%d %s", first.Code, first.Body.String())
	}
	// A policy update must not invalidate the durable result of an exact old
	// request; it only prevents a new stale execution.
	secondUpdate := httptest.NewRequest(http.MethodPut, "/api/v1/run-log-retention", strings.NewReader(`{"enabled":true,"keep_days":8,"batch_size":5}`))
	secondUpdate.Header.Set("Authorization", "Bearer "+admin)
	secondUpdate.Header.Set("Content-Type", "application/json")
	secondUpdateRec := httptest.NewRecorder()
	server.ServeHTTP(secondUpdateRec, secondUpdate)
	replayed := execute(status.Policy.Version)
	if replayed.Code != http.StatusOK || replayed.Body.String() != first.Body.String() {
		t.Fatalf("exact replay=%d %s want %s", replayed.Code, replayed.Body.String(), first.Body.String())
	}
	changed := execute(status.Policy.Version + 1)
	if changed.Code != http.StatusConflict {
		t.Fatalf("changed replay=%d %s", changed.Code, changed.Body.String())
	}
}

func TestRunnerTelemetryIsAuthenticatedBoundedAndAppearsOnlyAsAggregate(t *testing.T) {
	server, _ := newTestServer()
	admin := createTestSession(t, server)
	register := httptest.NewRequest(http.MethodPost, "/api/v1/runners/register", strings.NewReader(`{"id":"runner_metrics","name":"private runner name","tags":[],"capabilities":["shell"]}`))
	register.Header.Set("Authorization", "Bearer "+admin)
	register.Header.Set("Content-Type", "application/json")
	registered := httptest.NewRecorder()
	server.ServeHTTP(registered, register)
	if registered.Code != http.StatusCreated {
		t.Fatalf("register=%d %s", registered.Code, registered.Body.String())
	}
	var payload struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(registered.Body.Bytes(), &payload); err != nil || payload.Token == "" {
		t.Fatalf("runner token=%q err=%v", payload.Token, err)
	}
	telemetry := httptest.NewRequest(http.MethodPost, "/api/v1/runners/telemetry", strings.NewReader(`{"journal_depth":4,"retry_count":2,"renew_failures":1}`))
	telemetry.Header.Set("Authorization", "Bearer "+payload.Token)
	telemetry.Header.Set("Content-Type", "application/json")
	telemetryRec := httptest.NewRecorder()
	server.ServeHTTP(telemetryRec, telemetry)
	if telemetryRec.Code != http.StatusNoContent {
		t.Fatalf("telemetry=%d %s", telemetryRec.Code, telemetryRec.Body.String())
	}
	detail := httptest.NewRequest(http.MethodGet, "/api/v1/runners/runner_metrics", nil)
	detail.Header.Set("Authorization", "Bearer "+admin)
	detailRec := httptest.NewRecorder()
	server.ServeHTTP(detailRec, detail)
	if detailRec.Code != http.StatusOK || !strings.Contains(detailRec.Body.String(), `"journal_depth":4`) || !strings.Contains(detailRec.Body.String(), `"retry_count":2`) || strings.Contains(detailRec.Body.String(), payload.Token) {
		t.Fatalf("safe runner telemetry detail=%d %s", detailRec.Code, detailRec.Body.String())
	}
	bad := httptest.NewRequest(http.MethodPost, "/api/v1/runners/telemetry", strings.NewReader(`{"journal_depth":9000,"retry_count":0,"renew_failures":0}`))
	bad.Header.Set("Authorization", "Bearer "+payload.Token)
	bad.Header.Set("Content-Type", "application/json")
	badRec := httptest.NewRecorder()
	server.ServeHTTP(badRec, bad)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("unbounded telemetry=%d %s", badRec.Code, badRec.Body.String())
	}
	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsReq.Header.Set("Authorization", "Bearer "+admin)
	metricsRec := httptest.NewRecorder()
	server.ServeHTTP(metricsRec, metricsReq)
	if metricsRec.Code != http.StatusOK || !strings.Contains(metricsRec.Body.String(), "nerocd_runner_journal_depth 4") || strings.Contains(metricsRec.Body.String(), "private runner name") || strings.Contains(metricsRec.Body.String(), "runner_metrics") {
		t.Fatalf("metrics=%d %s", metricsRec.Code, metricsRec.Body.String())
	}
}

func TestDrainKeepsLivenessButRejectsReadinessAndOrdinaryWork(t *testing.T) {
	server, _ := newTestServer()
	server.SetDraining(true)
	for _, check := range []struct {
		path string
		want int
		body string
	}{
		{"/api/v1/health", http.StatusOK, `"status":"ok"`},
		{"/api/v1/ready", http.StatusServiceUnavailable, `"status":"not_ready"`},
		{"/api/v1/projects", http.StatusServiceUnavailable, `"status":"draining"`},
	} {
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, check.path, nil))
		if rec.Code != check.want || !strings.Contains(rec.Body.String(), check.body) {
			t.Fatalf("draining %s status=%d body=%s", check.path, rec.Code, rec.Body.String())
		}
	}
}

func TestReadinessDoesNotConflateLivenessWithDatabaseCompatibility(t *testing.T) {
	mem := store.NewMemoryStore()
	service := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, Projects: unavailableProjects{mem}, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem})
	server := NewServer(service, slog.Default(), web.Static())

	health := httptest.NewRecorder()
	server.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"status":"ok"`) {
		t.Fatalf("liveness response=%d %s", health.Code, health.Body.String())
	}

	ready := httptest.NewRecorder()
	server.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil))
	if ready.Code != http.StatusServiceUnavailable || !strings.Contains(ready.Body.String(), `"status":"not_ready"`) {
		t.Fatalf("readiness response=%d %s", ready.Code, ready.Body.String())
	}
}

func TestReadinessRejectsOldOrPartialSchema(t *testing.T) {
	mem := store.NewMemoryStore()
	service := app.NewService(app.Dependencies{Auth: auth.ContextProvider{}, Users: mem, Sessions: mem, APITokens: mem, Projects: incompatibleProjects{mem}, Members: mem, Templates: mem, Sources: mem, Runs: mem, Runners: mem, Approvals: mem, Audit: mem})
	server := NewServer(service, slog.Default(), web.Static())
	health := httptest.NewRecorder()
	server.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	ready := httptest.NewRecorder()
	server.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil))
	if health.Code != http.StatusOK || ready.Code != http.StatusServiceUnavailable || !strings.Contains(ready.Body.String(), `"status":"not_ready"`) {
		t.Fatalf("old-schema liveness=%d readiness=%d body=%s", health.Code, ready.Code, ready.Body.String())
	}
}

func TestPanicRecoveryReturnsSafeJSONAndServerRemainsUsable(t *testing.T) {
	server, _ := newTestServer()
	var logs bytes.Buffer
	server.logger = slog.New(slog.NewTextHandler(&logs, nil))
	server.mux.Handle("GET /panic-test", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("sentinel-secret-must-not-leak")
	}))
	panicRequest := httptest.NewRequest(http.MethodGet, "/panic-test", nil)
	panicRequest.Header.Set("X-Request-ID", "req_panic_safe")
	panicResponse := httptest.NewRecorder()
	server.ServeHTTP(panicResponse, panicRequest)
	if panicResponse.Code != http.StatusInternalServerError || !strings.Contains(panicResponse.Body.String(), `"error":"internal_error"`) || !strings.Contains(panicResponse.Body.String(), `"request_id":"req_panic_safe"`) {
		t.Fatalf("panic response=%d %s", panicResponse.Code, panicResponse.Body.String())
	}
	if strings.Contains(panicResponse.Body.String(), "sentinel-secret") || strings.Contains(logs.String(), "sentinel-secret") {
		t.Fatalf("panic sentinel leaked response=%q logs=%q", panicResponse.Body.String(), logs.String())
	}
	ready := httptest.NewRecorder()
	server.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, "/api/v1/ready", nil))
	if ready.Code != http.StatusOK {
		t.Fatalf("server did not survive recovered panic: %d %s", ready.Code, ready.Body.String())
	}
}

func TestCreatedSessionAuthenticatesCurrentPrincipal(t *testing.T) {
	server, _ := newTestServer()

	sessionReq := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(`{"email":"admin@example.local","password":"admin"}`))
	sessionReq.Header.Set("Content-Type", "application/json")
	sessionRec := httptest.NewRecorder()
	server.ServeHTTP(sessionRec, sessionReq)

	if sessionRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/sessions returned %d, want %d: %s", sessionRec.Code, http.StatusCreated, sessionRec.Body.String())
	}
	var sessionBody struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(sessionRec.Body).Decode(&sessionBody); err != nil {
		t.Fatal(err)
	}
	if sessionBody.Token == "" {
		t.Fatal("session response did not include token")
	}

	meReq := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+sessionBody.Token)
	meRec := httptest.NewRecorder()
	server.ServeHTTP(meRec, meReq)

	if meRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/me with token returned %d, want %d: %s", meRec.Code, http.StatusOK, meRec.Body.String())
	}
	var principal auth.Principal
	if err := json.NewDecoder(meRec.Body).Decode(&principal); err != nil {
		t.Fatal(err)
	}
	if principal.Email != "admin@example.local" || principal.Provider != "local" || !slices.Contains(principal.Roles, "system_admin") {
		t.Fatalf("unexpected principal: %#v", principal)
	}
}

func TestBrowserSessionsUseCookieOnlyAuthenticationAndCSRF(t *testing.T) {
	server, _ := newTestServer()
	cookie := createBrowserSession(t, server)

	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/api/v1" || !cookie.Secure || cookie.Expires.IsZero() {
		t.Fatalf("browser session cookie attributes = %#v", cookie)
	}

	me := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	me.AddCookie(cookie)
	meRec := httptest.NewRecorder()
	server.ServeHTTP(meRec, me)
	if meRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/me with browser cookie = %d: %s", meRec.Code, meRec.Body.String())
	}

	for _, csrf := range []string{"", "wrong"} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"cookie csrf"}`))
		req.Header.Set("Content-Type", "application/json")
		if csrf != "" {
			req.Header.Set("X-NeroCD-CSRF", csrf)
		}
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("cookie POST with CSRF %q = %d: %s", csrf, rec.Code, rec.Body.String())
		}
	}
	accepted := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"cookie accepted"}`))
	accepted.Header.Set("Content-Type", "application/json")
	accepted.Header.Set("X-NeroCD-CSRF", "1")
	accepted.AddCookie(cookie)
	acceptedRec := httptest.NewRecorder()
	server.ServeHTTP(acceptedRec, accepted)
	if acceptedRec.Code != http.StatusCreated {
		t.Fatalf("cookie POST with CSRF = %d: %s", acceptedRec.Code, acceptedRec.Body.String())
	}

	bearer := createTestSession(t, server)
	bearerReq := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"bearer mutation"}`))
	bearerReq.Header.Set("Content-Type", "application/json")
	bearerReq.Header.Set("Authorization", "Bearer "+bearer)
	bearerRec := httptest.NewRecorder()
	server.ServeHTTP(bearerRec, bearerReq)
	if bearerRec.Code != http.StatusCreated {
		t.Fatalf("bearer POST without CSRF = %d: %s", bearerRec.Code, bearerRec.Body.String())
	}
}

func TestBrowserMutationsRequireExactConfiguredOriginButBearerDoesNot(t *testing.T) {
	server, _ := newTestServerWithConfig(ServerConfig{PublicOrigin: "https://console.example.invalid"})
	login := httptest.NewRequest(http.MethodPost, "/api/v1/browser-sessions", strings.NewReader(`{"email":"admin@example.local","password":"admin"}`))
	login.Header.Set("Content-Type", "application/json")
	login.Header.Set("Origin", "https://console.example.invalid")
	loginRec := httptest.NewRecorder()
	server.ServeHTTP(loginRec, login)
	if loginRec.Code != http.StatusCreated {
		t.Fatalf("configured-origin browser login=%d: %s", loginRec.Code, loginRec.Body.String())
	}
	cookie := loginRec.Result().Cookies()[0]
	cookieMutation := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"blocked"}`))
	cookieMutation.Header.Set("Content-Type", "application/json")
	cookieMutation.Header.Set("X-NeroCD-CSRF", "1")
	cookieMutation.Header.Set("Origin", "https://evil.example.invalid")
	cookieMutation.AddCookie(cookie)
	cookieRec := httptest.NewRecorder()
	server.ServeHTTP(cookieRec, cookieMutation)
	if cookieRec.Code != http.StatusForbidden {
		t.Fatalf("wrong-origin cookie mutation=%d: %s", cookieRec.Code, cookieRec.Body.String())
	}
	bearer := createTestSession(t, server)
	bearerMutation := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"bearer"}`))
	bearerMutation.Header.Set("Content-Type", "application/json")
	bearerMutation.Header.Set("Origin", "https://evil.example.invalid")
	bearerMutation.Header.Set("Authorization", "Bearer "+bearer)
	bearerRec := httptest.NewRecorder()
	server.ServeHTTP(bearerRec, bearerMutation)
	if bearerRec.Code != http.StatusCreated {
		t.Fatalf("bearer origin-independent mutation=%d: %s", bearerRec.Code, bearerRec.Body.String())
	}
}

func TestBrowserSessionCookieSecureDefaultsAndExplicitLocalOverride(t *testing.T) {
	secureServer, _ := newTestServerWithConfig(ServerConfig{})
	if cookie := createBrowserSession(t, secureServer); !cookie.Secure {
		t.Fatalf("zero-value server config emitted insecure cookie: %#v", cookie)
	}
	insecureServer, _ := newTestServerWithConfig(ServerConfig{AllowInsecureCookies: true})
	if cookie := createBrowserSession(t, insecureServer); cookie.Secure {
		t.Fatalf("explicit insecure local override emitted Secure cookie: %#v", cookie)
	}
}

func TestBrowserSessionLogoutRevokesCookieAndRunnerRoutesRejectIt(t *testing.T) {
	server, _ := newTestServer()
	cookie := createBrowserSession(t, server)

	runnerReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/heartbeat", strings.NewReader(`{}`))
	runnerReq.Header.Set("Content-Type", "application/json")
	runnerReq.AddCookie(cookie)
	runnerRec := httptest.NewRecorder()
	server.ServeHTTP(runnerRec, runnerReq)
	if runnerRec.Code != http.StatusUnauthorized {
		t.Fatalf("runner endpoint accepted browser cookie: %d", runnerRec.Code)
	}

	missingCSRF := httptest.NewRequest(http.MethodDelete, "/api/v1/browser-sessions", nil)
	missingCSRF.AddCookie(cookie)
	missingCSRFRec := httptest.NewRecorder()
	server.ServeHTTP(missingCSRFRec, missingCSRF)
	if missingCSRFRec.Code != http.StatusForbidden {
		t.Fatalf("browser logout without CSRF = %d: %s", missingCSRFRec.Code, missingCSRFRec.Body.String())
	}

	logout := httptest.NewRequest(http.MethodDelete, "/api/v1/browser-sessions", nil)
	logout.Header.Set("X-NeroCD-CSRF", "1")
	logout.AddCookie(cookie)
	logoutRec := httptest.NewRecorder()
	server.ServeHTTP(logoutRec, logout)
	if logoutRec.Code != http.StatusNoContent {
		t.Fatalf("browser logout = %d: %s", logoutRec.Code, logoutRec.Body.String())
	}
	cleared := logoutRec.Result().Cookies()
	if len(cleared) != 1 || cleared[0].Name != "nerocd_session" || cleared[0].MaxAge >= 0 || cleared[0].Path != "/api/v1" || !cleared[0].HttpOnly || !cleared[0].Secure || cleared[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cleared cookie = %#v", cleared)
	}
	old := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	old.AddCookie(cookie)
	oldRec := httptest.NewRecorder()
	server.ServeHTTP(oldRec, old)
	if oldRec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked browser cookie authenticated /me: %d", oldRec.Code)
	}
}

func TestBrowserCookieDoesNotAcceptAPIToken(t *testing.T) {
	server, _ := newTestServer()
	bearer := createTestSession(t, server)
	create := httptest.NewRequest(http.MethodPost, "/api/v1/api-tokens", strings.NewReader(`{"name":"cookie rejection","roles":["system_admin"]}`))
	create.Header.Set("Content-Type", "application/json")
	create.Header.Set("Authorization", "Bearer "+bearer)
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, create)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create API token = %d: %s", createRec.Code, createRec.Body.String())
	}
	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(createRec.Body).Decode(&body); err != nil || body.Token == "" {
		t.Fatalf("decode API token: %v %#v", err, body)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: "nerocd_session", Value: body.Token})
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("API token in browser cookie = %d: %s", rec.Code, rec.Body.String())
	}
}

func createBrowserSession(t *testing.T, server http.Handler) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/browser-sessions", strings.NewReader(`{"email":"admin@example.local","password":"admin"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/browser-sessions = %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if _, exists := body["token"]; exists {
		t.Fatalf("browser session response included token: %s", rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "nerocd_session" || cookies[0].Value == "" {
		t.Fatalf("browser login cookies = %#v", cookies)
	}
	return cookies[0]
}

func TestSessionCanBeRevoked(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSession(t, server)

	revokeReq := httptest.NewRequest(http.MethodDelete, "/api/v1/sessions", nil)
	revokeReq.Header.Set("Authorization", "Bearer "+token)
	revokeRec := httptest.NewRecorder()
	server.ServeHTTP(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusNoContent {
		t.Fatalf("DELETE /api/v1/sessions returned %d: %s", revokeRec.Code, revokeRec.Body.String())
	}

	meReq := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+token)
	meRec := httptest.NewRecorder()
	server.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/me with revoked token returned %d, want %d", meRec.Code, http.StatusUnauthorized)
	}
}

func TestLoginIsEnumerationSafeRateLimitedAndUsesTrustedProxySource(t *testing.T) {
	server, _ := newTestServerWithConfig(ServerConfig{TrustedProxyCIDRs: []string{"10.0.0.0/8"}})
	metricsToken := createTestSession(t, server)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	server.app.SetLoginLimiter(auth.NewLoginLimiter(func() time.Time { return now }, 2, time.Minute, 64))
	login := func(email string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(`{"email":"`+email+`","password":"wrong"}`))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "10.1.2.3:4444"
		req.Header.Set("X-Forwarded-For", "203.0.113.9, 10.2.3.4")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	unknown := login("nobody@example.invalid")
	known := login("admin@example.local")
	if unknown.Code != http.StatusUnauthorized || known.Code != http.StatusUnauthorized || unknown.Body.String() != known.Body.String() {
		t.Fatalf("enumeration responses unknown=%d/%s known=%d/%s", unknown.Code, unknown.Body.String(), known.Code, known.Body.String())
	}
	limited := login("another@example.invalid")
	if limited.Code != http.StatusTooManyRequests || !strings.Contains(limited.Body.String(), `"code":"rate_limited"`) || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("rate limit response=%d headers=%v body=%s", limited.Code, limited.Header(), limited.Body.String())
	}
	metricsRequest := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRequest.Header.Set("Authorization", "Bearer "+metricsToken)
	metrics := httptest.NewRecorder()
	server.ServeHTTP(metrics, metricsRequest)
	if metrics.Code != http.StatusOK || !strings.Contains(metrics.Body.String(), "nerocd_auth_login_rate_limited_total 1") {
		t.Fatalf("login limiter metric=%d %s", metrics.Code, metrics.Body.String())
	}
	now = now.Add(time.Minute)
	if cooled := login("another@example.invalid"); cooled.Code != http.StatusUnauthorized {
		t.Fatalf("expired rate-limit login=%d %s", cooled.Code, cooled.Body.String())
	}
	if source := server.clientSource(httptest.NewRequest(http.MethodGet, "/", nil)); source != "192.0.2.1" {
		t.Fatalf("untrusted/request default source=%q", source)
	}
	proxyReq := httptest.NewRequest(http.MethodGet, "/", nil)
	proxyReq.RemoteAddr = "10.1.2.3:443"
	proxyReq.Header.Set("X-Forwarded-For", "203.0.113.9, 10.2.3.4")
	if source := server.clientSource(proxyReq); source != "203.0.113.9" {
		t.Fatalf("trusted proxy source=%q", source)
	}
}

func TestAdminCanListAndRevokeSessionMetadata(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSession(t, server)
	list := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	list.Header.Set("Authorization", "Bearer "+token)
	listRec := httptest.NewRecorder()
	server.ServeHTTP(listRec, list)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list sessions=%d %s", listRec.Code, listRec.Body.String())
	}
	var body struct {
		Items []domain.Session `json:"items"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &body); err != nil || len(body.Items) == 0 {
		t.Fatalf("session list=%s err=%v", listRec.Body.String(), err)
	}
	var current domain.Session
	for _, item := range body.Items {
		if item.SourceIP != "" {
			current = item
			break
		}
	}
	if current.ID == "" || current.LastSeenAt == nil {
		t.Fatalf("missing session metadata %#v", body.Items)
	}
	revoke := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/revoke", strings.NewReader(`{"session_id":"`+current.ID+`"}`))
	revoke.Header.Set("Authorization", "Bearer "+token)
	revoke.Header.Set("Content-Type", "application/json")
	revokeRec := httptest.NewRecorder()
	server.ServeHTTP(revokeRec, revoke)
	if revokeRec.Code != http.StatusOK {
		t.Fatalf("admin revoke=%d %s", revokeRec.Code, revokeRec.Body.String())
	}
}

func TestAPITokenCanBootstrapRunnerRegistration(t *testing.T) {
	server, _ := newTestServer()
	adminToken := createTestSession(t, server)
	viewerToken := createTestSessionFor(t, server, "viewer@example.local", "viewer")

	viewerCreateReq := httptest.NewRequest(http.MethodPost, "/api/v1/api-tokens", strings.NewReader(`{"name":"Viewer Token","roles":["system_admin"]}`))
	viewerCreateReq.Header.Set("Authorization", "Bearer "+viewerToken)
	viewerCreateReq.Header.Set("Content-Type", "application/json")
	viewerCreateRec := httptest.NewRecorder()
	server.ServeHTTP(viewerCreateRec, viewerCreateReq)
	if viewerCreateRec.Code != http.StatusForbidden {
		t.Fatalf("viewer POST /api/v1/api-tokens returned %d, want %d", viewerCreateRec.Code, http.StatusForbidden)
	}

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/api-tokens", strings.NewReader(`{"name":"Runner Bootstrap","kind":"bootstrap","roles":["runner_admin"]}`))
	createReq.Header.Set("Authorization", "Bearer "+adminToken)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/api-tokens returned %d: %s", createRec.Code, createRec.Body.String())
	}
	var created app.CreatedAPIToken
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Token == "" || !strings.HasPrefix(created.Token, "nca_") || created.APIToken.Kind != "bootstrap" || created.APIToken.Status != "active" || !slices.Contains(created.APIToken.Roles, "runner_admin") {
		t.Fatalf("unexpected created API token: %#v", created)
	}

	meReq := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	meReq.Header.Set("Authorization", "Bearer "+created.Token)
	meRec := httptest.NewRecorder()
	server.ServeHTTP(meRec, meReq)
	if meRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/me with api token returned %d: %s", meRec.Code, meRec.Body.String())
	}
	var principal auth.Principal
	if err := json.NewDecoder(meRec.Body).Decode(&principal); err != nil {
		t.Fatal(err)
	}
	if principal.Provider != "api_token" || principal.ID != created.APIToken.ID || !slices.Contains(principal.Roles, "runner_admin") {
		t.Fatalf("unexpected api token principal: %#v", principal)
	}

	registerReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/register", strings.NewReader(`{"id":"runner_bootstrap","name":"Bootstrap Runner","capabilities":["shell"]}`))
	registerReq.Header.Set("Authorization", "Bearer "+created.Token)
	registerReq.Header.Set("Content-Type", "application/json")
	registerRec := httptest.NewRecorder()
	server.ServeHTTP(registerRec, registerReq)
	if registerRec.Code != http.StatusCreated {
		t.Fatalf("api token runner registration returned %d: %s", registerRec.Code, registerRec.Body.String())
	}

	viewerRevokeReq := httptest.NewRequest(http.MethodPost, "/api/v1/api-tokens/revoke", strings.NewReader(`{"token_id":"`+created.APIToken.ID+`"}`))
	viewerRevokeReq.Header.Set("Authorization", "Bearer "+viewerToken)
	viewerRevokeReq.Header.Set("Content-Type", "application/json")
	viewerRevokeRec := httptest.NewRecorder()
	server.ServeHTTP(viewerRevokeRec, viewerRevokeReq)
	if viewerRevokeRec.Code != http.StatusForbidden {
		t.Fatalf("viewer POST /api/v1/api-tokens/revoke returned %d, want %d", viewerRevokeRec.Code, http.StatusForbidden)
	}

	revokeReq := httptest.NewRequest(http.MethodPost, "/api/v1/api-tokens/revoke", strings.NewReader(`{"token_id":"`+created.APIToken.ID+`"}`))
	revokeReq.Header.Set("Authorization", "Bearer "+adminToken)
	revokeReq.Header.Set("Content-Type", "application/json")
	revokeRec := httptest.NewRecorder()
	server.ServeHTTP(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusOK || !strings.Contains(revokeRec.Body.String(), `"status":"revoked"`) {
		t.Fatalf("POST /api/v1/api-tokens/revoke did not revoke token: %d %s", revokeRec.Code, revokeRec.Body.String())
	}

	revokedMeReq := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	revokedMeReq.Header.Set("Authorization", "Bearer "+created.Token)
	revokedMeRec := httptest.NewRecorder()
	server.ServeHTTP(revokedMeRec, revokedMeReq)
	if revokedMeRec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked api token GET /api/v1/me returned %d, want %d", revokedMeRec.Code, http.StatusUnauthorized)
	}
}

func TestMutationsWriteAuditEvents(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSession(t, server)

	projectReq := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader(`{"name":"Release Ops","description":"release automation"}`))
	projectReq.Header.Set("Authorization", "Bearer "+token)
	projectReq.Header.Set("Content-Type", "application/json")
	projectReq.Header.Set("X-Request-ID", "req_test_audit")
	projectRec := httptest.NewRecorder()
	server.ServeHTTP(projectRec, projectReq)
	if projectRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/projects returned %d: %s", projectRec.Code, projectRec.Body.String())
	}

	runReq := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(`{"template_id":"tpl_patch"}`))
	runReq.Header.Set("Authorization", "Bearer "+token)
	runReq.Header.Set("Content-Type", "application/json")
	runRec := httptest.NewRecorder()
	server.ServeHTTP(runRec, runReq)
	if runRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/runs returned %d: %s", runRec.Code, runRec.Body.String())
	}
	var run domain.TaskRun
	if err := json.NewDecoder(runRec.Body).Decode(&run); err != nil {
		t.Fatal(err)
	}
	if run.Status != "waiting_approval" {
		t.Fatalf("requested gated run status = %q, want waiting_approval", run.Status)
	}
	if run.TemplateID == nil || *run.TemplateID != "tpl_patch" {
		t.Fatalf("templated run TemplateID = %v, want tpl_patch", run.TemplateID)
	}

	approveReq := httptest.NewRequest(http.MethodPost, "/api/v1/runs/approve", strings.NewReader(`{"run_id":"`+run.ID+`"}`))
	approveReq.Header.Set("Authorization", "Bearer "+token)
	approveReq.Header.Set("Content-Type", "application/json")
	approveRec := httptest.NewRecorder()
	server.ServeHTTP(approveRec, approveReq)
	if approveRec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/runs/approve returned %d: %s", approveRec.Code, approveRec.Body.String())
	}

	adhocReq := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(`{"project_id":"proj_platform","run_spec":{"type":"shell","inputs":{"command":"echo ok"}},"runner_tags":["local"]}`))
	adhocReq.Header.Set("Authorization", "Bearer "+token)
	adhocReq.Header.Set("Content-Type", "application/json")
	adhocRec := httptest.NewRecorder()
	server.ServeHTTP(adhocRec, adhocReq)
	if adhocRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/runs ad-hoc returned %d: %s", adhocRec.Code, adhocRec.Body.String())
	}
	var adhocRun domain.TaskRun
	if err := json.NewDecoder(adhocRec.Body).Decode(&adhocRun); err != nil {
		t.Fatal(err)
	}
	if adhocRun.TemplateID != nil || adhocRun.RunSpec.Type != "shell" {
		t.Fatalf("unexpected ad-hoc run: %#v", adhocRun)
	}

	auditReq := httptest.NewRequest(http.MethodGet, "/api/v1/audit-events", nil)
	auditReq.Header.Set("Authorization", "Bearer "+token)
	auditRec := httptest.NewRecorder()
	server.ServeHTTP(auditRec, auditReq)
	if auditRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/audit-events returned %d: %s", auditRec.Code, auditRec.Body.String())
	}
	if !strings.Contains(auditRec.Body.String(), "project.create") || !strings.Contains(auditRec.Body.String(), "run.request") || !strings.Contains(auditRec.Body.String(), "run.approve") || !strings.Contains(auditRec.Body.String(), "req_test_audit") {
		t.Fatalf("audit events did not include expected actions: %s", auditRec.Body.String())
	}
}

func TestProjectMemberAccessCanBeListedAndUpdated(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSession(t, server)

	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/project-members?project_id=proj_platform", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	listRec := httptest.NewRecorder()
	server.ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), `"role":"owner"`) {
		t.Fatalf("GET /api/v1/project-members returned %d: %s", listRec.Code, listRec.Body.String())
	}

	upsertReq := httptest.NewRequest(http.MethodPost, "/api/v1/project-members", strings.NewReader(`{"project_id":"proj_platform","email":"admin@example.local","role":"maintainer"}`))
	upsertReq.Header.Set("Authorization", "Bearer "+token)
	upsertReq.Header.Set("Content-Type", "application/json")
	upsertRec := httptest.NewRecorder()
	server.ServeHTTP(upsertRec, upsertReq)
	if upsertRec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/project-members returned %d: %s", upsertRec.Code, upsertRec.Body.String())
	}
	var member domain.ProjectMember
	if err := json.NewDecoder(upsertRec.Body).Decode(&member); err != nil {
		t.Fatal(err)
	}
	if member.ProjectID != "proj_platform" || member.Email != "admin@example.local" || member.Role != "maintainer" {
		t.Fatalf("unexpected project member: %#v", member)
	}

	auditReq := httptest.NewRequest(http.MethodGet, "/api/v1/audit-events", nil)
	auditReq.Header.Set("Authorization", "Bearer "+token)
	auditRec := httptest.NewRecorder()
	server.ServeHTTP(auditRec, auditReq)
	if auditRec.Code != http.StatusOK || !strings.Contains(auditRec.Body.String(), "project.member.upsert") {
		t.Fatalf("audit events did not include project member update: %d %s", auditRec.Code, auditRec.Body.String())
	}
}

func TestProjectAuthorizationScopesReadsAndMutations(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSessionFor(t, server, "viewer@example.local", "viewer")

	projectsReq := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	projectsReq.Header.Set("Authorization", "Bearer "+token)
	projectsRec := httptest.NewRecorder()
	server.ServeHTTP(projectsRec, projectsReq)
	if projectsRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/projects returned %d: %s", projectsRec.Code, projectsRec.Body.String())
	}
	if strings.Contains(projectsRec.Body.String(), "proj_platform") || !strings.Contains(projectsRec.Body.String(), "proj_security") {
		t.Fatalf("viewer project list was not scoped: %s", projectsRec.Body.String())
	}

	roleReq := httptest.NewRequest(http.MethodGet, "/api/v1/project-role?project_id=proj_security", nil)
	roleReq.Header.Set("Authorization", "Bearer "+token)
	roleRec := httptest.NewRecorder()
	server.ServeHTTP(roleRec, roleReq)
	if roleRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/project-role returned %d: %s", roleRec.Code, roleRec.Body.String())
	}
	var role domain.ProjectRole
	if err := json.NewDecoder(roleRec.Body).Decode(&role); err != nil {
		t.Fatalf("decode project role: %v", err)
	}
	if role.Role != "viewer" || !role.CanView || role.CanRun || role.CanAdmin {
		t.Fatalf("viewer project role did not expose expected permissions: %+v", role)
	}

	templatesReq := httptest.NewRequest(http.MethodGet, "/api/v1/templates?project_id=proj_platform", nil)
	templatesReq.Header.Set("Authorization", "Bearer "+token)
	templatesRec := httptest.NewRecorder()
	server.ServeHTTP(templatesRec, templatesReq)
	if templatesRec.Code != http.StatusForbidden {
		t.Fatalf("GET /api/v1/templates for inaccessible project returned %d, want %d: %s", templatesRec.Code, http.StatusForbidden, templatesRec.Body.String())
	}

	forbiddenRoleReq := httptest.NewRequest(http.MethodGet, "/api/v1/project-role?project_id=proj_platform", nil)
	forbiddenRoleReq.Header.Set("Authorization", "Bearer "+token)
	forbiddenRoleRec := httptest.NewRecorder()
	server.ServeHTTP(forbiddenRoleRec, forbiddenRoleReq)
	if forbiddenRoleRec.Code != http.StatusForbidden {
		t.Fatalf("GET /api/v1/project-role for inaccessible project returned %d, want %d: %s", forbiddenRoleRec.Code, http.StatusForbidden, forbiddenRoleRec.Body.String())
	}

	runReq := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(`{"project_id":"proj_security","run_spec":{"type":"shell","inputs":{"command":"echo no"}}}`))
	runReq.Header.Set("Authorization", "Bearer "+token)
	runReq.Header.Set("Content-Type", "application/json")
	runRec := httptest.NewRecorder()
	server.ServeHTTP(runRec, runReq)
	if runRec.Code != http.StatusForbidden {
		t.Fatalf("viewer POST /api/v1/runs returned %d, want %d: %s", runRec.Code, http.StatusForbidden, runRec.Body.String())
	}

	memberReq := httptest.NewRequest(http.MethodPost, "/api/v1/project-members", strings.NewReader(`{"project_id":"proj_security","email":"admin@example.local","role":"viewer"}`))
	memberReq.Header.Set("Authorization", "Bearer "+token)
	memberReq.Header.Set("Content-Type", "application/json")
	memberRec := httptest.NewRecorder()
	server.ServeHTTP(memberRec, memberReq)
	if memberRec.Code != http.StatusForbidden {
		t.Fatalf("viewer POST /api/v1/project-members returned %d, want %d: %s", memberRec.Code, http.StatusForbidden, memberRec.Body.String())
	}

	logsReq := httptest.NewRequest(http.MethodGet, "/api/v1/run-logs?run_id=run_001", nil)
	logsReq.Header.Set("Authorization", "Bearer "+token)
	logsRec := httptest.NewRecorder()
	server.ServeHTTP(logsRec, logsReq)
	if logsRec.Code != http.StatusForbidden {
		t.Fatalf("GET /api/v1/run-logs for inaccessible run returned %d, want %d: %s", logsRec.Code, http.StatusForbidden, logsRec.Body.String())
	}
}

func TestProjectRoleReportsSystemAdminPermissions(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSession(t, server)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/project-role?project_id=proj_platform", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/project-role returned %d: %s", rec.Code, rec.Body.String())
	}
	var role domain.ProjectRole
	if err := json.NewDecoder(rec.Body).Decode(&role); err != nil {
		t.Fatalf("decode project role: %v", err)
	}
	if role.Role != "system_admin" || !role.CanView || !role.CanRun || !role.CanAdmin {
		t.Fatalf("system admin project role did not expose expected permissions: %+v", role)
	}
}

func TestListEndpointsExposePaginationMetadata(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSession(t, server)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/runs?limit=1&offset=1", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/runs returned %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Items  []domain.TaskRun `json:"items"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
		Count  int              `json:"count"`
		Total  int              `json:"total"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Limit != 1 || payload.Offset != 1 || payload.Count != 1 || payload.Total < 2 || len(payload.Items) != 1 {
		t.Fatalf("unexpected pagination payload: %#v", payload)
	}
}

func TestAccessKeysAreScopedAndAudited(t *testing.T) {
	server, _ := newTestServer()
	adminToken := createTestSession(t, server)
	viewerToken := createTestSessionFor(t, server, "viewer@example.local", "viewer")

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/access-keys", strings.NewReader(`{"project_id":"proj_platform","name":"Deploy SSH","kind":"ssh","fingerprint":"SHA256:test"}`))
	createReq.Header.Set("Authorization", "Bearer "+adminToken)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/access-keys returned %d: %s", createRec.Code, createRec.Body.String())
	}
	var key domain.AccessKey
	if err := json.NewDecoder(createRec.Body).Decode(&key); err != nil {
		t.Fatalf("decode access key: %v", err)
	}
	if key.ProjectID != "proj_platform" || key.Kind != "ssh" || key.Fingerprint != "SHA256:test" {
		t.Fatalf("created access key did not preserve metadata: %+v", key)
	}

	viewerListReq := httptest.NewRequest(http.MethodGet, "/api/v1/access-keys", nil)
	viewerListReq.Header.Set("Authorization", "Bearer "+viewerToken)
	viewerListRec := httptest.NewRecorder()
	server.ServeHTTP(viewerListRec, viewerListReq)
	if viewerListRec.Code != http.StatusOK {
		t.Fatalf("viewer GET /api/v1/access-keys returned %d: %s", viewerListRec.Code, viewerListRec.Body.String())
	}
	if strings.Contains(viewerListRec.Body.String(), "key_ansible_vault") || !strings.Contains(viewerListRec.Body.String(), "key_token_admin") {
		t.Fatalf("viewer access key list was not scoped: %s", viewerListRec.Body.String())
	}

	viewerCreateReq := httptest.NewRequest(http.MethodPost, "/api/v1/access-keys", strings.NewReader(`{"project_id":"proj_security","name":"Forbidden","kind":"token","fingerprint":"SHA256:no"}`))
	viewerCreateReq.Header.Set("Authorization", "Bearer "+viewerToken)
	viewerCreateReq.Header.Set("Content-Type", "application/json")
	viewerCreateRec := httptest.NewRecorder()
	server.ServeHTTP(viewerCreateRec, viewerCreateReq)
	if viewerCreateRec.Code != http.StatusForbidden {
		t.Fatalf("viewer POST /api/v1/access-keys returned %d, want %d: %s", viewerCreateRec.Code, http.StatusForbidden, viewerCreateRec.Body.String())
	}

	auditReq := httptest.NewRequest(http.MethodGet, "/api/v1/audit-events", nil)
	auditReq.Header.Set("Authorization", "Bearer "+adminToken)
	auditRec := httptest.NewRecorder()
	server.ServeHTTP(auditRec, auditReq)
	if auditRec.Code != http.StatusOK || !strings.Contains(auditRec.Body.String(), "access_key.create") {
		t.Fatalf("audit events did not include access key create: %d %s", auditRec.Code, auditRec.Body.String())
	}
}

func TestInventoriesAreScopedAndAudited(t *testing.T) {
	server, _ := newTestServer()
	adminToken := createTestSession(t, server)
	viewerToken := createTestSessionFor(t, server, "viewer@example.local", "viewer")

	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/inventories", strings.NewReader(`{"project_id":"proj_platform","name":"Blue Fleet","kind":"static","source":"inventories/blue.ini"}`))
	createReq.Header.Set("Authorization", "Bearer "+adminToken)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/inventories returned %d: %s", createRec.Code, createRec.Body.String())
	}
	var inventory domain.Inventory
	if err := json.NewDecoder(createRec.Body).Decode(&inventory); err != nil {
		t.Fatalf("decode inventory: %v", err)
	}
	if inventory.ProjectID != "proj_platform" || inventory.Kind != "static" || inventory.Source != "inventories/blue.ini" {
		t.Fatalf("created inventory did not preserve metadata: %+v", inventory)
	}

	viewerListReq := httptest.NewRequest(http.MethodGet, "/api/v1/inventories", nil)
	viewerListReq.Header.Set("Authorization", "Bearer "+viewerToken)
	viewerListRec := httptest.NewRecorder()
	server.ServeHTTP(viewerListRec, viewerListReq)
	if viewerListRec.Code != http.StatusOK {
		t.Fatalf("viewer GET /api/v1/inventories returned %d: %s", viewerListRec.Code, viewerListRec.Body.String())
	}
	if strings.Contains(viewerListRec.Body.String(), "inv_platform_prod") || !strings.Contains(viewerListRec.Body.String(), "inv_security_response") {
		t.Fatalf("viewer inventory list was not scoped: %s", viewerListRec.Body.String())
	}

	viewerCreateReq := httptest.NewRequest(http.MethodPost, "/api/v1/inventories", strings.NewReader(`{"project_id":"proj_security","name":"Forbidden","kind":"static","source":"no.ini"}`))
	viewerCreateReq.Header.Set("Authorization", "Bearer "+viewerToken)
	viewerCreateReq.Header.Set("Content-Type", "application/json")
	viewerCreateRec := httptest.NewRecorder()
	server.ServeHTTP(viewerCreateRec, viewerCreateReq)
	if viewerCreateRec.Code != http.StatusForbidden {
		t.Fatalf("viewer POST /api/v1/inventories returned %d, want %d: %s", viewerCreateRec.Code, http.StatusForbidden, viewerCreateRec.Body.String())
	}

	auditReq := httptest.NewRequest(http.MethodGet, "/api/v1/audit-events", nil)
	auditReq.Header.Set("Authorization", "Bearer "+adminToken)
	auditRec := httptest.NewRecorder()
	server.ServeHTTP(auditRec, auditReq)
	if auditRec.Code != http.StatusOK || !strings.Contains(auditRec.Body.String(), "inventory.create") {
		t.Fatalf("audit events did not include inventory create: %d %s", auditRec.Code, auditRec.Body.String())
	}
}

func TestAuditEventsAreScopedByProjectMembership(t *testing.T) {
	server, _ := newTestServer()
	adminToken := createTestSession(t, server)
	viewerToken := createTestSessionFor(t, server, "viewer@example.local", "viewer")

	repoReq := httptest.NewRequest(http.MethodPost, "/api/v1/repositories", strings.NewReader(`{"project_id":"proj_platform","name":"Platform Audit Repo","url":"https://example.local/audit.git"}`))
	repoReq.Header.Set("Authorization", "Bearer "+adminToken)
	repoReq.Header.Set("Content-Type", "application/json")
	repoRec := httptest.NewRecorder()
	server.ServeHTTP(repoRec, repoReq)
	if repoRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/repositories returned %d: %s", repoRec.Code, repoRec.Body.String())
	}

	rejectReq := httptest.NewRequest(http.MethodPost, "/api/v1/runs/reject", strings.NewReader(`{"run_id":"run_002"}`))
	rejectReq.Header.Set("Authorization", "Bearer "+adminToken)
	rejectReq.Header.Set("Content-Type", "application/json")
	rejectRec := httptest.NewRecorder()
	server.ServeHTTP(rejectRec, rejectReq)
	if rejectRec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/runs/reject returned %d: %s", rejectRec.Code, rejectRec.Body.String())
	}

	auditReq := httptest.NewRequest(http.MethodGet, "/api/v1/audit-events", nil)
	auditReq.Header.Set("Authorization", "Bearer "+viewerToken)
	auditRec := httptest.NewRecorder()
	server.ServeHTTP(auditRec, auditReq)
	if auditRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/audit-events returned %d: %s", auditRec.Code, auditRec.Body.String())
	}
	body := auditRec.Body.String()
	if strings.Contains(body, "repository.create") {
		t.Fatalf("viewer saw audit event for inaccessible platform project: %s", body)
	}
	if !strings.Contains(body, "run.reject") {
		t.Fatalf("viewer did not see audit event for accessible security project: %s", body)
	}
}

func TestRejectRunCancelsPendingApproval(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSession(t, server)

	runReq := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(`{"template_id":"tpl_patch"}`))
	runReq.Header.Set("Authorization", "Bearer "+token)
	runReq.Header.Set("Content-Type", "application/json")
	runRec := httptest.NewRecorder()
	server.ServeHTTP(runRec, runReq)
	if runRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/runs returned %d: %s", runRec.Code, runRec.Body.String())
	}
	var run domain.TaskRun
	if err := json.NewDecoder(runRec.Body).Decode(&run); err != nil {
		t.Fatal(err)
	}

	rejectReq := httptest.NewRequest(http.MethodPost, "/api/v1/runs/reject", strings.NewReader(`{"run_id":"`+run.ID+`"}`))
	rejectReq.Header.Set("Authorization", "Bearer "+token)
	rejectReq.Header.Set("Content-Type", "application/json")
	rejectRec := httptest.NewRecorder()
	server.ServeHTTP(rejectRec, rejectReq)
	if rejectRec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/runs/reject returned %d: %s", rejectRec.Code, rejectRec.Body.String())
	}
	var approval domain.Approval
	if err := json.NewDecoder(rejectRec.Body).Decode(&approval); err != nil {
		t.Fatal(err)
	}
	if approval.Status != "rejected" {
		t.Fatalf("rejected approval status = %q, want rejected", approval.Status)
	}

	runsReq := httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil)
	runsReq.Header.Set("Authorization", "Bearer "+token)
	runsRec := httptest.NewRecorder()
	server.ServeHTTP(runsRec, runsReq)
	if runsRec.Code != http.StatusOK || !strings.Contains(runsRec.Body.String(), `"status":"canceled"`) {
		t.Fatalf("rejected run was not canceled: %d %s", runsRec.Code, runsRec.Body.String())
	}
}

func TestRunnerRegistrationClaimAndComplete(t *testing.T) {
	server, mem := newTestServer()
	token := createTestSession(t, server)
	viewerToken := createTestSessionFor(t, server, "viewer@example.local", "viewer")

	runReq := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(`{"project_id":"proj_platform","run_spec":{"type":"shell","inputs":{"command":"echo ok"},"process":{"command":["echo","ok"]},"secrets":[{"name":"database-password","provider":"runner_file","reference":"database-password","target":"env:DATABASE_PASSWORD","required":true,"version":"v1","fingerprint":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]},"runner_tags":["local"]}`))
	runReq.Header.Set("Authorization", "Bearer "+token)
	runReq.Header.Set("Content-Type", "application/json")
	runRec := httptest.NewRecorder()
	server.ServeHTTP(runRec, runReq)
	if runRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/runs returned %d: %s", runRec.Code, runRec.Body.String())
	}

	registerReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/register", strings.NewReader(`{"id":"runner_test","name":"Test Runner","tags":["local"],"capabilities":["shell"]}`))
	registerReq.Header.Set("Authorization", "Bearer "+token)
	registerReq.Header.Set("Content-Type", "application/json")
	registerRec := httptest.NewRecorder()
	server.ServeHTTP(registerRec, registerReq)
	if registerRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/runners/register returned %d: %s", registerRec.Code, registerRec.Body.String())
	}
	var registered app.RegisteredRunner
	if err := json.NewDecoder(registerRec.Body).Decode(&registered); err != nil {
		t.Fatal(err)
	}
	if registered.Runner.ID != "runner_test" || registered.Token == "" {
		t.Fatalf("registration did not return runner and token: %#v", registered)
	}
	getRunnerReq := httptest.NewRequest(http.MethodGet, "/api/v1/runners/runner_test", nil)
	getRunnerReq.Header.Set("Authorization", "Bearer "+token)
	getRunnerRec := httptest.NewRecorder()
	server.ServeHTTP(getRunnerRec, getRunnerReq)
	if getRunnerRec.Code != http.StatusOK || strings.Contains(getRunnerRec.Body.String(), registered.Token) || strings.Contains(getRunnerRec.Body.String(), "token_hash") {
		t.Fatalf("GET runner detail is not safe/successful: %d %s", getRunnerRec.Code, getRunnerRec.Body.String())
	}
	viewerGetRunnerReq := httptest.NewRequest(http.MethodGet, "/api/v1/runners/runner_test", nil)
	viewerGetRunnerReq.Header.Set("Authorization", "Bearer "+viewerToken)
	viewerGetRunnerRec := httptest.NewRecorder()
	server.ServeHTTP(viewerGetRunnerRec, viewerGetRunnerReq)
	if viewerGetRunnerRec.Code != http.StatusForbidden {
		t.Fatalf("viewer runner detail returned %d, want %d", viewerGetRunnerRec.Code, http.StatusForbidden)
	}

	viewerRegisterReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/register", strings.NewReader(`{"id":"runner_viewer","name":"Viewer Runner","capabilities":["shell"]}`))
	viewerRegisterReq.Header.Set("Authorization", "Bearer "+viewerToken)
	viewerRegisterReq.Header.Set("Content-Type", "application/json")
	viewerRegisterRec := httptest.NewRecorder()
	server.ServeHTTP(viewerRegisterRec, viewerRegisterReq)
	if viewerRegisterRec.Code != http.StatusForbidden {
		t.Fatalf("viewer runner registration returned %d, want %d", viewerRegisterRec.Code, http.StatusForbidden)
	}

	heartbeatReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/heartbeat", strings.NewReader(`{}`))
	heartbeatReq.Header.Set("Authorization", "Bearer "+registered.Token)
	heartbeatReq.Header.Set("Content-Type", "application/json")
	heartbeatRec := httptest.NewRecorder()
	server.ServeHTTP(heartbeatRec, heartbeatReq)
	if heartbeatRec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/runners/heartbeat returned %d: %s", heartbeatRec.Code, heartbeatRec.Body.String())
	}

	rotateReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/rotate-token", strings.NewReader(`{"runner_id":"runner_test"}`))
	rotateReq.Header.Set("Authorization", "Bearer "+token)
	rotateReq.Header.Set("Content-Type", "application/json")
	rotateRec := httptest.NewRecorder()
	server.ServeHTTP(rotateRec, rotateReq)
	if rotateRec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/runners/rotate-token returned %d: %s", rotateRec.Code, rotateRec.Body.String())
	}
	var rotated app.RegisteredRunner
	if err := json.NewDecoder(rotateRec.Body).Decode(&rotated); err != nil {
		t.Fatal(err)
	}
	if rotated.Token == "" || rotated.Token == registered.Token || rotated.Runner.Status != "active" {
		t.Fatalf("rotation did not return a fresh active runner token: %#v", rotated)
	}

	viewerRotateReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/rotate-token", strings.NewReader(`{"runner_id":"runner_test"}`))
	viewerRotateReq.Header.Set("Authorization", "Bearer "+viewerToken)
	viewerRotateReq.Header.Set("Content-Type", "application/json")
	viewerRotateRec := httptest.NewRecorder()
	server.ServeHTTP(viewerRotateRec, viewerRotateReq)
	if viewerRotateRec.Code != http.StatusForbidden {
		t.Fatalf("viewer runner token rotation returned %d, want %d", viewerRotateRec.Code, http.StatusForbidden)
	}

	oldTokenHeartbeatReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/heartbeat", strings.NewReader(`{}`))
	oldTokenHeartbeatReq.Header.Set("Authorization", "Bearer "+registered.Token)
	oldTokenHeartbeatReq.Header.Set("Content-Type", "application/json")
	oldTokenHeartbeatRec := httptest.NewRecorder()
	server.ServeHTTP(oldTokenHeartbeatRec, oldTokenHeartbeatReq)
	if oldTokenHeartbeatRec.Code != http.StatusUnauthorized {
		t.Fatalf("old runner token heartbeat returned %d, want %d", oldTokenHeartbeatRec.Code, http.StatusUnauthorized)
	}
	registered.Token = rotated.Token

	userClaimReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/claim", strings.NewReader(`{}`))
	userClaimReq.Header.Set("Authorization", "Bearer "+token)
	userClaimReq.Header.Set("Content-Type", "application/json")
	userClaimRec := httptest.NewRecorder()
	server.ServeHTTP(userClaimRec, userClaimReq)
	if userClaimRec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /api/v1/runners/claim with user token returned %d, want %d", userClaimRec.Code, http.StatusUnauthorized)
	}

	claimReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/claim", strings.NewReader(`{}`))
	claimReq.Header.Set("Authorization", "Bearer "+registered.Token)
	claimReq.Header.Set("Content-Type", "application/json")
	claimRec := httptest.NewRecorder()
	server.ServeHTTP(claimRec, claimReq)
	if claimRec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/runners/claim returned %d: %s", claimRec.Code, claimRec.Body.String())
	}
	var claim domain.ClaimedRun
	if err := json.NewDecoder(claimRec.Body).Decode(&claim); err != nil {
		t.Fatal(err)
	}
	if claim.Run.Status != "running" || claim.Run.RunnerID == nil || *claim.Run.RunnerID != "runner_test" {
		t.Fatalf("unexpected claim run: %#v", claim.Run)
	}
	if claim.PrimitivePlan.Process == nil || len(claim.PrimitivePlan.Process.Command) == 0 {
		t.Fatalf("claim did not include primitive process plan: %#v", claim.PrimitivePlan)
	}

	userLeaseReq := httptest.NewRequest(http.MethodGet, "/api/v1/runners/lease?lease_id="+claim.Lease.ID, nil)
	userLeaseReq.Header.Set("Authorization", "Bearer "+token)
	userLeaseRec := httptest.NewRecorder()
	server.ServeHTTP(userLeaseRec, userLeaseReq)
	if userLeaseRec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/runners/lease with user token returned %d, want %d", userLeaseRec.Code, http.StatusUnauthorized)
	}

	leaseReq := httptest.NewRequest(http.MethodGet, "/api/v1/runners/lease?lease_id="+claim.Lease.ID+"&attempt="+strconv.Itoa(claim.Lease.Attempt)+"&fence="+claim.Lease.Fence, nil)
	leaseReq.Header.Set("Authorization", "Bearer "+registered.Token)
	leaseRec := httptest.NewRecorder()
	server.ServeHTTP(leaseRec, leaseReq)
	if leaseRec.Code != http.StatusOK || !strings.Contains(leaseRec.Body.String(), `"status":"active"`) {
		t.Fatalf("GET /api/v1/runners/lease did not return active lease: %d %s", leaseRec.Code, leaseRec.Body.String())
	}

	secretAccessBody := `{"access_id":"secret_access_0123456789abcdef0123456789abcdef","run_id":"` + claim.Run.ID + `","lease_id":"` + claim.Lease.ID + `","attempt":` + strconv.Itoa(claim.Lease.Attempt) + `,"fence":"` + claim.Lease.Fence + `","binding":"database-password","provider":"runner_file","version":"v1"}`
	for attempt := 0; attempt < 2; attempt++ {
		secretReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/secrets/access", strings.NewReader(secretAccessBody))
		secretReq.Header.Set("Authorization", "Bearer "+registered.Token)
		secretReq.Header.Set("Content-Type", "application/json")
		secretRec := httptest.NewRecorder()
		server.ServeHTTP(secretRec, secretReq)
		if secretRec.Code != http.StatusOK || strings.Contains(secretRec.Body.String(), claim.Lease.Fence) || strings.Contains(secretRec.Body.String(), "reference") || strings.Contains(secretRec.Body.String(), "target") || strings.Contains(secretRec.Body.String(), "fingerprint") || strings.Contains(secretRec.Body.String(), strings.Repeat("a", 64)) {
			t.Fatalf("secret access response was unsafe or failed: %d %s", secretRec.Code, secretRec.Body.String())
		}
	}
	conflictSecretReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/secrets/access", strings.NewReader(strings.Replace(secretAccessBody, `"version":"v1"`, `"version":"v2"`, 1)))
	conflictSecretReq.Header.Set("Authorization", "Bearer "+registered.Token)
	conflictSecretReq.Header.Set("Content-Type", "application/json")
	conflictSecretRec := httptest.NewRecorder()
	server.ServeHTTP(conflictSecretRec, conflictSecretReq)
	if conflictSecretRec.Code != http.StatusConflict {
		t.Fatalf("conflicting secret access returned %d: %s", conflictSecretRec.Code, conflictSecretRec.Body.String())
	}

	badLogReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/logs", strings.NewReader(`{"run_id":"`+claim.Run.ID+`","sequence":4,"stream":"stdout","message":"ok"}`))
	badLogReq.Header.Set("Authorization", "Bearer "+registered.Token)
	badLogReq.Header.Set("Content-Type", "application/json")
	badLogRec := httptest.NewRecorder()
	server.ServeHTTP(badLogRec, badLogReq)
	if badLogRec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/v1/runners/logs without lease returned %d, want %d", badLogRec.Code, http.StatusBadRequest)
	}

	logReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/logs", strings.NewReader(`{"run_id":"`+claim.Run.ID+`","lease_id":"`+claim.Lease.ID+`","attempt":`+strconv.Itoa(claim.Lease.Attempt)+`,"fence":"`+claim.Lease.Fence+`","event_key":"event_api_1","sequence":4,"stream":"stdout","message":"ok"}`))
	logReq.Header.Set("Authorization", "Bearer "+registered.Token)
	logReq.Header.Set("Content-Type", "application/json")
	logRec := httptest.NewRecorder()
	server.ServeHTTP(logRec, logReq)
	if logRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/runners/logs returned %d: %s", logRec.Code, logRec.Body.String())
	}
	var firstLog domain.RunLog
	if err := json.NewDecoder(logRec.Body).Decode(&firstLog); err != nil {
		t.Fatal(err)
	}
	retryLogReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/logs", strings.NewReader(`{"run_id":"`+claim.Run.ID+`","lease_id":"`+claim.Lease.ID+`","attempt":`+strconv.Itoa(claim.Lease.Attempt)+`,"fence":"`+claim.Lease.Fence+`","event_key":"event_api_1","sequence":4,"stream":"stdout","message":"ok"}`))
	retryLogReq.Header.Set("Authorization", "Bearer "+registered.Token)
	retryLogReq.Header.Set("Content-Type", "application/json")
	retryLogRec := httptest.NewRecorder()
	server.ServeHTTP(retryLogRec, retryLogReq)
	var retriedLog domain.RunLog
	if retryLogRec.Code != http.StatusCreated || json.NewDecoder(retryLogRec.Body).Decode(&retriedLog) != nil || retriedLog.ID != firstLog.ID || retriedLog.Sequence != firstLog.Sequence {
		t.Fatalf("exact log replay response = %d %s", retryLogRec.Code, retryLogRec.Body.String())
	}
	conflictLogReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/logs", strings.NewReader(`{"run_id":"`+claim.Run.ID+`","lease_id":"`+claim.Lease.ID+`","attempt":`+strconv.Itoa(claim.Lease.Attempt)+`,"fence":"`+claim.Lease.Fence+`","event_key":"event_api_1","sequence":4,"stream":"stdout","message":"conflict"}`))
	conflictLogReq.Header.Set("Authorization", "Bearer "+registered.Token)
	conflictLogReq.Header.Set("Content-Type", "application/json")
	conflictLogRec := httptest.NewRecorder()
	server.ServeHTTP(conflictLogRec, conflictLogReq)
	if conflictLogRec.Code != http.StatusConflict {
		t.Fatalf("conflicting log replay returned %d, want 409", conflictLogRec.Code)
	}

	artifactReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/artifacts", strings.NewReader(`{"run_id":"`+claim.Run.ID+`","lease_id":"`+claim.Lease.ID+`","attempt":`+strconv.Itoa(claim.Lease.Attempt)+`,"fence":"`+claim.Lease.Fence+`","name":"stdout","path":"stdout.txt","found":true,"required":false,"size":12,"kind":"file"}`))
	artifactReq.Header.Set("Authorization", "Bearer "+registered.Token)
	artifactReq.Header.Set("Content-Type", "application/json")
	artifactRec := httptest.NewRecorder()
	server.ServeHTTP(artifactRec, artifactReq)
	if artifactRec.Code != http.StatusCreated || !strings.Contains(artifactRec.Body.String(), `"name":"stdout"`) {
		t.Fatalf("POST /api/v1/runners/artifacts returned %d: %s", artifactRec.Code, artifactRec.Body.String())
	}

	completeReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/complete", strings.NewReader(`{"lease_id":"`+claim.Lease.ID+`","attempt":`+strconv.Itoa(claim.Lease.Attempt)+`,"fence":"`+claim.Lease.Fence+`","completion_key":"completion_api_1","status":"succeeded"}`))
	completeReq.Header.Set("Authorization", "Bearer "+registered.Token)
	completeReq.Header.Set("Content-Type", "application/json")
	completeRec := httptest.NewRecorder()
	server.ServeHTTP(completeRec, completeReq)
	if completeRec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/runners/complete returned %d: %s", completeRec.Code, completeRec.Body.String())
	}
	var firstCompletion domain.RunLease
	if err := json.NewDecoder(completeRec.Body).Decode(&firstCompletion); err != nil {
		t.Fatal(err)
	}
	retryCompleteReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/complete", strings.NewReader(`{"lease_id":"`+claim.Lease.ID+`","attempt":`+strconv.Itoa(claim.Lease.Attempt)+`,"fence":"`+claim.Lease.Fence+`","completion_key":"completion_api_1","status":"succeeded"}`))
	retryCompleteReq.Header.Set("Authorization", "Bearer "+registered.Token)
	retryCompleteReq.Header.Set("Content-Type", "application/json")
	retryCompleteRec := httptest.NewRecorder()
	server.ServeHTTP(retryCompleteRec, retryCompleteReq)
	var retriedCompletion domain.RunLease
	if retryCompleteRec.Code != http.StatusOK || json.NewDecoder(retryCompleteRec.Body).Decode(&retriedCompletion) != nil || firstCompletion.CompletedAt == nil || retriedCompletion.CompletedAt == nil || !firstCompletion.CompletedAt.Equal(*retriedCompletion.CompletedAt) {
		t.Fatalf("exact completion replay response = %d %s", retryCompleteRec.Code, retryCompleteRec.Body.String())
	}
	conflictCompleteReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/complete", strings.NewReader(`{"lease_id":"`+claim.Lease.ID+`","attempt":`+strconv.Itoa(claim.Lease.Attempt)+`,"fence":"`+claim.Lease.Fence+`","completion_key":"completion_api_conflict","status":"failed"}`))
	conflictCompleteReq.Header.Set("Authorization", "Bearer "+registered.Token)
	conflictCompleteReq.Header.Set("Content-Type", "application/json")
	conflictCompleteRec := httptest.NewRecorder()
	server.ServeHTTP(conflictCompleteRec, conflictCompleteReq)
	if conflictCompleteRec.Code != http.StatusConflict {
		t.Fatalf("conflicting completion replay returned %d, want 409", conflictCompleteRec.Code)
	}
	staleSecretReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/secrets/access", strings.NewReader(secretAccessBody))
	staleSecretReq.Header.Set("Authorization", "Bearer "+registered.Token)
	staleSecretReq.Header.Set("Content-Type", "application/json")
	staleSecretRec := httptest.NewRecorder()
	server.ServeHTTP(staleSecretRec, staleSecretReq)
	if staleSecretRec.Code != http.StatusNotFound {
		t.Fatalf("terminal secret access replay returned %d, want 404: %s", staleSecretRec.Code, staleSecretRec.Body.String())
	}

	revokeReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/revoke-token", strings.NewReader(`{"runner_id":"runner_test"}`))
	revokeReq.Header.Set("Authorization", "Bearer "+token)
	revokeReq.Header.Set("Content-Type", "application/json")
	revokeRec := httptest.NewRecorder()
	server.ServeHTTP(revokeRec, revokeReq)
	if revokeRec.Code != http.StatusOK || !strings.Contains(revokeRec.Body.String(), `"status":"revoked"`) {
		t.Fatalf("POST /api/v1/runners/revoke-token did not revoke runner: %d %s", revokeRec.Code, revokeRec.Body.String())
	}

	viewerRevokeReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/revoke-token", strings.NewReader(`{"runner_id":"runner_test"}`))
	viewerRevokeReq.Header.Set("Authorization", "Bearer "+viewerToken)
	viewerRevokeReq.Header.Set("Content-Type", "application/json")
	viewerRevokeRec := httptest.NewRecorder()
	server.ServeHTTP(viewerRevokeRec, viewerRevokeReq)
	if viewerRevokeRec.Code != http.StatusForbidden {
		t.Fatalf("viewer runner token revocation returned %d, want %d", viewerRevokeRec.Code, http.StatusForbidden)
	}
	rawAfterDeniedRevoke, err := mem.ListAuditEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !auditActions(rawAfterDeniedRevoke)["runner.token.revoke.denied"] {
		t.Fatalf("persisted audit events missing runner.token.revoke.denied: %#v", rawAfterDeniedRevoke)
	}

	revokedHeartbeatReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/heartbeat", strings.NewReader(`{}`))
	revokedHeartbeatReq.Header.Set("Authorization", "Bearer "+registered.Token)
	revokedHeartbeatReq.Header.Set("Content-Type", "application/json")
	revokedHeartbeatRec := httptest.NewRecorder()
	server.ServeHTTP(revokedHeartbeatRec, revokedHeartbeatReq)
	if revokedHeartbeatRec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked runner token heartbeat returned %d, want %d", revokedHeartbeatRec.Code, http.StatusUnauthorized)
	}

	logsReq := httptest.NewRequest(http.MethodGet, "/api/v1/run-logs?run_id="+claim.Run.ID, nil)
	logsReq.Header.Set("Authorization", "Bearer "+token)
	logsRec := httptest.NewRecorder()
	server.ServeHTTP(logsRec, logsReq)
	if logsRec.Code != http.StatusOK || !strings.Contains(logsRec.Body.String(), `"stream":"stdout"`) {
		t.Fatalf("GET /api/v1/run-logs did not include runner stdout log: %d %s", logsRec.Code, logsRec.Body.String())
	}

	artifactsReq := httptest.NewRequest(http.MethodGet, "/api/v1/artifacts?run_id="+claim.Run.ID, nil)
	artifactsReq.Header.Set("Authorization", "Bearer "+token)
	artifactsRec := httptest.NewRecorder()
	server.ServeHTTP(artifactsRec, artifactsReq)
	if artifactsRec.Code != http.StatusOK || !strings.Contains(artifactsRec.Body.String(), `"path":"stdout.txt"`) {
		t.Fatalf("GET /api/v1/artifacts did not include runner artifact: %d %s", artifactsRec.Code, artifactsRec.Body.String())
	}

	auditReq := httptest.NewRequest(http.MethodGet, "/api/v1/audit-events", nil)
	auditReq.Header.Set("Authorization", "Bearer "+token)
	auditRec := httptest.NewRecorder()
	server.ServeHTTP(auditRec, auditReq)
	if auditRec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/audit-events returned %d: %s", auditRec.Code, auditRec.Body.String())
	}
	auditBody := auditRec.Body.String()
	for _, action := range []string{"runner.register", "runner.token.rotate", "runner.claim", "secret.access", "runner.complete", "runner.token.revoke"} {
		if !strings.Contains(auditBody, action) {
			t.Fatalf("audit events missing %s: %s", action, auditBody)
		}
	}
	rawEvents, err := mem.ListAuditEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rawActions := map[string]bool{}
	for _, event := range rawEvents {
		rawActions[event.Action] = true
	}
	for _, action := range []string{"runner.register", "runner.token.rotate", "runner.claim", "secret.access", "runner.complete", "runner.token.revoke"} {
		if !rawActions[action] {
			t.Fatalf("persisted audit events missing %s: %#v", action, rawEvents)
		}
	}
	if rawActions["runner.heartbeat"] {
		t.Fatalf("heartbeat must not flood append-only audit events: %#v", rawEvents)
	}
}

func TestRunnerEnrollmentConsumesGeneratedCredentialIdempotently(t *testing.T) {
	server, mem := newTestServer()
	adminToken := createTestSession(t, server)
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/runner-enrollments", strings.NewReader(`{"runner_id":"runner_enrollment_api","runner_name":"Enrollment API","tags":["linux"],"capabilities":["shell"],"ttl_seconds":600}`))
	createReq.Header.Set("Authorization", "Bearer "+adminToken)
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	server.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create enrollment status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var created app.CreatedRunnerEnrollment
	if err := json.NewDecoder(createRec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Token, "nce_") || created.Enrollment.TokenHash != "" || created.Enrollment.CredentialHash != nil || created.Enrollment.ConsumeRequestID != nil {
		t.Fatalf("unsafe enrollment response: %+v", created)
	}
	credential := "ncr_0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	credentialDigest := sha256.Sum256([]byte(credential))
	credentialHash := hex.EncodeToString(credentialDigest[:])
	consumeBody := `{"request_id":"enroll_consume_0123456789abcdef0123456789abcdef","credential_hash":"` + credentialHash + `"}`
	consume := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runner-enrollments/consume", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+created.Token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	first := consume(consumeBody)
	replayed := consume(consumeBody)
	if first.Code != http.StatusOK || replayed.Code != http.StatusOK || first.Body.String() != replayed.Body.String() {
		t.Fatalf("consume/replay statuses=%d/%d bodies=%s / %s", first.Code, replayed.Code, first.Body.String(), replayed.Body.String())
	}
	conflict := consume(strings.Replace(consumeBody, "0123456789abcdef0123456789abcdef", "1123456789abcdef0123456789abcdef", 1))
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflicting consume status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	heartbeatReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/heartbeat", strings.NewReader(`{}`))
	heartbeatReq.Header.Set("Authorization", "Bearer "+credential)
	heartbeatReq.Header.Set("Content-Type", "application/json")
	heartbeatRec := httptest.NewRecorder()
	server.ServeHTTP(heartbeatRec, heartbeatReq)
	if heartbeatRec.Code != http.StatusOK || !strings.Contains(heartbeatRec.Body.String(), `"id":"runner_enrollment_api"`) {
		t.Fatalf("enrolled credential heartbeat status=%d body=%s", heartbeatRec.Code, heartbeatRec.Body.String())
	}
	audits, err := mem.ListAuditEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	consumeAudits := 0
	for _, audit := range audits {
		encoded, _ := json.Marshal(audit)
		for _, forbidden := range []string{created.Token, credential, credentialHash, "enroll_consume_0123456789abcdef0123456789abcdef"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("audit leaked enrollment material: %s", encoded)
			}
		}
		if audit.Action == "runner.enrollment.consume" {
			consumeAudits++
		}
	}
	if consumeAudits != 1 {
		t.Fatalf("consume audit count=%d", consumeAudits)
	}
}

func TestExpiredSessionIsRejected(t *testing.T) {
	server, mem := newTestServer()
	token := "ncd_expired"
	if err := mem.CreateSession(
		t.Context(),
		domain.Session{
			ID:        "ses_expired",
			UserID:    "usr_bootstrap",
			ExpiresAt: time.Now().UTC().Add(-time.Minute),
			CreatedAt: time.Now().UTC().Add(-2 * time.Minute),
		},
		tokenHash(token),
	); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /api/v1/me with expired token returned %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestRunnerExecutesWorkflowStepsAcrossClaims(t *testing.T) {
	server, _ := newTestServer()
	token := createTestSession(t, server)

	runBody := `{
		"project_id":"proj_platform",
		"run_spec":{"type":"shell","inputs":{"command":"echo parent"},"process":{"command":["echo","parent"]}},
		"workflow":{"steps":[
			{"id":"prepare","name":"Prepare","run_spec":{"type":"shell","inputs":{"command":"echo prepare"},"process":{"command":["echo","prepare"]}},"requires_ack":false},
			{"id":"execute","name":"Execute","depends_on":["prepare"],"run_spec":{"type":"shell","inputs":{"command":"echo execute"},"process":{"command":["echo","execute"]}},"requires_ack":false}
		]},
		"runner_tags":["local"]
	}`
	runReq := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(runBody))
	runReq.Header.Set("Authorization", "Bearer "+token)
	runReq.Header.Set("Content-Type", "application/json")
	runRec := httptest.NewRecorder()
	server.ServeHTTP(runRec, runReq)
	if runRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/runs workflow returned %d: %s", runRec.Code, runRec.Body.String())
	}
	var run domain.TaskRun
	if err := json.NewDecoder(runRec.Body).Decode(&run); err != nil {
		t.Fatal(err)
	}
	if len(run.WorkflowState.Steps) != 2 || run.WorkflowState.Steps[0].Status != "pending" {
		t.Fatalf("workflow state was not initialized: %#v", run.WorkflowState)
	}

	registerReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/register", strings.NewReader(`{"id":"runner_workflow","name":"Workflow Runner","tags":["local"],"capabilities":["shell"]}`))
	registerReq.Header.Set("Authorization", "Bearer "+token)
	registerReq.Header.Set("Content-Type", "application/json")
	registerRec := httptest.NewRecorder()
	server.ServeHTTP(registerRec, registerReq)
	if registerRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/runners/register workflow returned %d: %s", registerRec.Code, registerRec.Body.String())
	}
	var registered app.RegisteredRunner
	if err := json.NewDecoder(registerRec.Body).Decode(&registered); err != nil {
		t.Fatal(err)
	}

	first := claimRunnerRun(t, server, registered.Token)
	if first.Run.ID != run.ID || first.Run.WorkflowState.CurrentStepID != "prepare" {
		t.Fatalf("first claim did not start prepare step: %#v", first.Run.WorkflowState)
	}
	if first.PrimitivePlan.Process == nil || strings.Join(first.PrimitivePlan.Process.Command, " ") != "echo prepare" {
		t.Fatalf("first claim did not use prepare process plan: %#v", first.PrimitivePlan)
	}
	completeRunnerLease(t, server, registered.Token, first.Lease, "succeeded")

	runsReq := httptest.NewRequest(http.MethodGet, "/api/v1/runs?project_id=proj_platform", nil)
	runsReq.Header.Set("Authorization", "Bearer "+token)
	runsRec := httptest.NewRecorder()
	server.ServeHTTP(runsRec, runsReq)
	if runsRec.Code != http.StatusOK || !strings.Contains(runsRec.Body.String(), `"status":"queued"`) || !strings.Contains(runsRec.Body.String(), `"status":"succeeded"`) {
		t.Fatalf("workflow run was not requeued with succeeded first step: %d %s", runsRec.Code, runsRec.Body.String())
	}

	second := claimRunnerRun(t, server, registered.Token)
	if second.Run.ID != run.ID || second.Run.WorkflowState.CurrentStepID != "execute" {
		t.Fatalf("second claim did not start execute step: %#v", second.Run.WorkflowState)
	}
	if second.PrimitivePlan.Process == nil || strings.Join(second.PrimitivePlan.Process.Command, " ") != "echo execute" {
		t.Fatalf("second claim did not use execute process plan: %#v", second.PrimitivePlan)
	}
	completeRunnerLease(t, server, registered.Token, second.Lease, "succeeded")

	finalReq := httptest.NewRequest(http.MethodGet, "/api/v1/runs?project_id=proj_platform", nil)
	finalReq.Header.Set("Authorization", "Bearer "+token)
	finalRec := httptest.NewRecorder()
	server.ServeHTTP(finalRec, finalReq)
	if finalRec.Code != http.StatusOK || !strings.Contains(finalRec.Body.String(), `"status":"succeeded"`) {
		t.Fatalf("workflow run did not finish: %d %s", finalRec.Code, finalRec.Body.String())
	}
	var finalRuns struct {
		Items []domain.TaskRun `json:"items"`
	}
	if err := json.NewDecoder(finalRec.Body).Decode(&finalRuns); err != nil {
		t.Fatal(err)
	}
	var finalRun domain.TaskRun
	for _, item := range finalRuns.Items {
		if item.ID == run.ID {
			finalRun = item
			break
		}
	}
	if finalRun.ID == "" || finalRun.WorkflowState.CurrentStepID != "" || finalRun.WorkflowState.Steps[1].Status != "succeeded" {
		t.Fatalf("workflow state did not finish cleanly: %#v", finalRun.WorkflowState)
	}
}

func TestRunnerSecretAccessUsesOnlyCurrentWorkflowStep(t *testing.T) {
	server, _ := newTestServer()
	userToken := createTestSession(t, server)
	runBody := `{
		"project_id":"proj_platform",
		"run_spec":{"type":"shell","process":{"command":["echo","parent"]},"secrets":[{"name":"top-secret","provider":"runner_file","reference":"top-secret","target":"env:TOP_SECRET","required":true,"version":"top-v1","fingerprint":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]},
		"workflow":{"steps":[
			{"id":"first","name":"First","run_spec":{"type":"shell","process":{"command":["echo","first"]},"secrets":[{"name":"first-secret","provider":"runner_file","reference":"first-secret","target":"env:FIRST_SECRET","required":true,"version":"first-v1","fingerprint":"sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}]}},
			{"id":"second","name":"Second","depends_on":["first"],"run_spec":{"type":"shell","process":{"command":["echo","second"]},"secrets":[{"name":"second-secret","provider":"runner_file","reference":"second-secret","target":"env:SECOND_SECRET","required":true,"version":"second-v1","fingerprint":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}]}}
		]},
		"runner_tags":["local"]
	}`
	runReq := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(runBody))
	runReq.Header.Set("Authorization", "Bearer "+userToken)
	runReq.Header.Set("Content-Type", "application/json")
	runRec := httptest.NewRecorder()
	server.ServeHTTP(runRec, runReq)
	if runRec.Code != http.StatusCreated {
		t.Fatalf("create workflow run returned %d: %s", runRec.Code, runRec.Body.String())
	}

	registerReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/register", strings.NewReader(`{"id":"runner_secret_workflow","name":"Secret Workflow Runner","tags":["local"],"capabilities":["shell"]}`))
	registerReq.Header.Set("Authorization", "Bearer "+userToken)
	registerReq.Header.Set("Content-Type", "application/json")
	registerRec := httptest.NewRecorder()
	server.ServeHTTP(registerRec, registerReq)
	if registerRec.Code != http.StatusCreated {
		t.Fatalf("register workflow runner returned %d: %s", registerRec.Code, registerRec.Body.String())
	}
	var registered app.RegisteredRunner
	if err := json.NewDecoder(registerRec.Body).Decode(&registered); err != nil {
		t.Fatal(err)
	}

	access := func(claim domain.ClaimedRun, accessID, binding, provider, version string) int {
		t.Helper()
		body := `{"access_id":"` + accessID + `","run_id":"` + claim.Run.ID + `","lease_id":"` + claim.Lease.ID + `","attempt":` + strconv.Itoa(claim.Lease.Attempt) + `,"fence":"` + claim.Lease.Fence + `","binding":"` + binding + `","provider":"` + provider + `","version":"` + version + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/v1/runners/secrets/access", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+registered.Token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec.Code
	}

	first := claimRunnerRun(t, server, registered.Token)
	if first.Run.WorkflowState.CurrentStepID != "first" {
		t.Fatalf("first claim current step=%q", first.Run.WorkflowState.CurrentStepID)
	}
	if got := access(first, "secret_access_50000000000000000000000000000000", "first-secret", "runner_file", "first-v1"); got != http.StatusOK {
		t.Fatalf("current first-step access status=%d", got)
	}
	if got := access(first, "secret_access_51000000000000000000000000000000", "top-secret", "runner_file", "top-v1"); got != http.StatusNotFound {
		t.Fatalf("top-level access during workflow status=%d", got)
	}
	if got := access(first, "secret_access_52000000000000000000000000000000", "second-secret", "runner_file", "second-v1"); got != http.StatusNotFound {
		t.Fatalf("other-step access status=%d", got)
	}
	completeRunnerLease(t, server, registered.Token, first.Lease, "succeeded")

	second := claimRunnerRun(t, server, registered.Token)
	if second.Run.WorkflowState.CurrentStepID != "second" {
		t.Fatalf("second claim current step=%q", second.Run.WorkflowState.CurrentStepID)
	}
	if got := access(second, "secret_access_53000000000000000000000000000000", "first-secret", "runner_file", "first-v1"); got != http.StatusNotFound {
		t.Fatalf("prior-step access after transition status=%d", got)
	}
	if got := access(second, "secret_access_54000000000000000000000000000000", "second-secret", "runner_file", "second-v1"); got != http.StatusOK {
		t.Fatalf("current second-step access status=%d", got)
	}
}

func claimRunnerRun(t *testing.T, server *Server, token string) domain.ClaimedRun {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runners/claim", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/runners/claim returned %d: %s", rec.Code, rec.Body.String())
	}
	var claim domain.ClaimedRun
	if err := json.NewDecoder(rec.Body).Decode(&claim); err != nil {
		t.Fatal(err)
	}
	return claim
}

func completeRunnerLease(t *testing.T, server *Server, token string, lease domain.RunLease, status string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runners/complete", strings.NewReader(`{"lease_id":"`+lease.ID+`","attempt":`+strconv.Itoa(lease.Attempt)+`,"fence":"`+lease.Fence+`","completion_key":"completion_`+lease.ID+`_`+status+`","status":"`+status+`"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/runners/complete returned %d: %s", rec.Code, rec.Body.String())
	}
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func auditActions(events []domain.AuditEvent) map[string]bool {
	actions := map[string]bool{}
	for _, event := range events {
		actions[event.Action] = true
	}
	return actions
}

func createTestSession(t *testing.T, server *Server) string {
	t.Helper()
	return createTestSessionFor(t, server, "admin@example.local", "admin")
}

func createTestSessionFor(t *testing.T, server *Server, email string, password string) string {
	t.Helper()
	body := `{"email":"` + email + `","password":"` + password + `"}`
	sessionReq := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", strings.NewReader(body))
	sessionReq.Header.Set("Content-Type", "application/json")
	sessionRec := httptest.NewRecorder()
	server.ServeHTTP(sessionRec, sessionReq)
	if sessionRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/sessions returned %d: %s", sessionRec.Code, sessionRec.Body.String())
	}
	var sessionBody struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(sessionRec.Body).Decode(&sessionBody); err != nil {
		t.Fatal(err)
	}
	return sessionBody.Token
}

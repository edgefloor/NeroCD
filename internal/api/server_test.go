package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"nerocd/internal/app"
	"nerocd/internal/auth"
	"nerocd/internal/domain"
	"nerocd/internal/store"
	"nerocd/web"
)

func newTestServer() (*Server, *store.MemoryStore) {
	mem := store.NewMemoryStore()
	service := app.NewService(auth.ContextProvider{}, mem, mem, mem, mem, mem, mem, mem, mem, mem, mem, mem)
	return NewServer(service, slog.Default(), web.Static()), mem
}

func TestServerServesBuiltWebDistribution(t *testing.T) {
	server, _ := newTestServer()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET / returned %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `type="module"`) || !strings.Contains(body, "/assets/") {
		t.Fatalf("GET / did not serve built Vite index.html: %q", body)
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

func TestReadinessAndMetricsArePublic(t *testing.T) {
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
	if metricsRec.Code != http.StatusOK || !strings.Contains(metricsRec.Body.String(), "nerocd_http_requests_total") {
		t.Fatalf("GET /metrics returned %d: %s", metricsRec.Code, metricsRec.Body.String())
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

	runReq := httptest.NewRequest(http.MethodPost, "/api/v1/runs", strings.NewReader(`{"project_id":"proj_platform","run_spec":{"type":"shell","inputs":{"command":"echo ok"},"process":{"command":["echo","ok"]}},"runner_tags":["local"]}`))
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

	leaseReq := httptest.NewRequest(http.MethodGet, "/api/v1/runners/lease?lease_id="+claim.Lease.ID, nil)
	leaseReq.Header.Set("Authorization", "Bearer "+registered.Token)
	leaseRec := httptest.NewRecorder()
	server.ServeHTTP(leaseRec, leaseReq)
	if leaseRec.Code != http.StatusOK || !strings.Contains(leaseRec.Body.String(), `"status":"active"`) {
		t.Fatalf("GET /api/v1/runners/lease did not return active lease: %d %s", leaseRec.Code, leaseRec.Body.String())
	}

	badLogReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/logs", strings.NewReader(`{"run_id":"`+claim.Run.ID+`","sequence":4,"stream":"stdout","message":"ok"}`))
	badLogReq.Header.Set("Authorization", "Bearer "+registered.Token)
	badLogReq.Header.Set("Content-Type", "application/json")
	badLogRec := httptest.NewRecorder()
	server.ServeHTTP(badLogRec, badLogReq)
	if badLogRec.Code != http.StatusBadRequest {
		t.Fatalf("POST /api/v1/runners/logs without lease returned %d, want %d", badLogRec.Code, http.StatusBadRequest)
	}

	logReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/logs", strings.NewReader(`{"run_id":"`+claim.Run.ID+`","lease_id":"`+claim.Lease.ID+`","sequence":4,"stream":"stdout","message":"ok"}`))
	logReq.Header.Set("Authorization", "Bearer "+registered.Token)
	logReq.Header.Set("Content-Type", "application/json")
	logRec := httptest.NewRecorder()
	server.ServeHTTP(logRec, logReq)
	if logRec.Code != http.StatusCreated {
		t.Fatalf("POST /api/v1/runners/logs returned %d: %s", logRec.Code, logRec.Body.String())
	}

	artifactReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/artifacts", strings.NewReader(`{"run_id":"`+claim.Run.ID+`","lease_id":"`+claim.Lease.ID+`","name":"stdout","path":"stdout.txt","found":true,"required":false,"size":12,"kind":"file"}`))
	artifactReq.Header.Set("Authorization", "Bearer "+registered.Token)
	artifactReq.Header.Set("Content-Type", "application/json")
	artifactRec := httptest.NewRecorder()
	server.ServeHTTP(artifactRec, artifactReq)
	if artifactRec.Code != http.StatusCreated || !strings.Contains(artifactRec.Body.String(), `"name":"stdout"`) {
		t.Fatalf("POST /api/v1/runners/artifacts returned %d: %s", artifactRec.Code, artifactRec.Body.String())
	}

	completeReq := httptest.NewRequest(http.MethodPost, "/api/v1/runners/complete", strings.NewReader(`{"lease_id":"`+claim.Lease.ID+`","status":"succeeded"}`))
	completeReq.Header.Set("Authorization", "Bearer "+registered.Token)
	completeReq.Header.Set("Content-Type", "application/json")
	completeRec := httptest.NewRecorder()
	server.ServeHTTP(completeRec, completeReq)
	if completeRec.Code != http.StatusOK {
		t.Fatalf("POST /api/v1/runners/complete returned %d: %s", completeRec.Code, completeRec.Body.String())
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
	for _, action := range []string{"runner.register", "runner.token.rotate", "runner.claim", "runner.complete", "runner.token.revoke"} {
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
	for _, action := range []string{"runner.register", "runner.heartbeat", "runner.token.rotate", "runner.claim", "runner.complete", "runner.token.revoke"} {
		if !rawActions[action] {
			t.Fatalf("persisted audit events missing %s: %#v", action, rawEvents)
		}
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
	completeRunnerLease(t, server, registered.Token, first.Lease.ID, "succeeded")

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
	completeRunnerLease(t, server, registered.Token, second.Lease.ID, "succeeded")

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

func completeRunnerLease(t *testing.T, server *Server, token string, leaseID string, status string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/runners/complete", strings.NewReader(`{"lease_id":"`+leaseID+`","status":"`+status+`"}`))
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
